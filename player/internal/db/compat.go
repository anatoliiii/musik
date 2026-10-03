package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Database is the compatibility boundary for older repository SQL. GORM owns
// backend creation and typed writes; this adapter keeps legacy repository
// reads portable while they are moved to mapped models.
type Database struct {
	*sql.DB
	Dialect string
}

type DatabaseTx struct {
	*sql.Tx
	owner *Database
}

type insertResult struct {
	id       int64
	rows     int64
	hasID    bool
	delegate sql.Result
}

func (r insertResult) LastInsertId() (int64, error) {
	if r.hasID {
		return r.id, nil
	}
	if r.delegate != nil {
		return r.delegate.LastInsertId()
	}
	return 0, fmt.Errorf("last insert ID is unavailable for this statement")
}

func (r insertResult) RowsAffected() (int64, error) {
	if r.delegate != nil {
		return r.delegate.RowsAffected()
	}
	return r.rows, nil
}

var (
	datetimeNowModifier = regexp.MustCompile(`(?i)datetime\(\s*'now'\s*,\s*(\$[0-9]+)\s*\)`)
	datetimeNow         = regexp.MustCompile(`(?i)datetime\(\s*'now'\s*\)`)
	julianNowDiff       = regexp.MustCompile(`(?i)julianday\(\s*'now'\s*\)\s*-\s*julianday\(\s*MIN\(played_at\)\s*\)`)
	sqliteHour          = regexp.MustCompile(`(?i)CAST\(\s*strftime\('%H'\s*,\s*([^)]*)\)\s+AS\s+INTEGER\s*\)`)
	sqliteDate          = regexp.MustCompile(`(?i)\bdate\(\s*([^()]+)\s*\)`)
	insertIgnore        = regexp.MustCompile(`(?i)\bINSERT\s+OR\s+IGNORE\s+INTO\b`)
	insertIDTable       = regexp.MustCompile(`(?i)^\s*INSERT\s+INTO\s+(jobs|listening_history|playlists)\b`)
)

func bindPlaceholders(query string, args []any) (string, error) {
	var out strings.Builder
	out.Grow(len(query) + len(args)*3)
	state := byte('n')
	arg := 0
	for i := 0; i < len(query); i++ {
		ch := query[i]
		var next byte
		if i+1 < len(query) {
			next = query[i+1]
		}
		switch state {
		case 'n':
			switch {
			case ch == '\'':
				state = 's'
			case ch == '"':
				state = 'd'
			case ch == '`':
				state = 'b'
			case ch == '[':
				state = 'a'
			case ch == '-' && next == '-':
				state = 'l'
			case ch == '/' && next == '*':
				state = 'c'
			case ch == '?':
				if arg >= len(args) {
					return "", fmt.Errorf("SQL query has more placeholders than arguments")
				}
				arg++
				fmt.Fprintf(&out, "$%d", arg)
				continue
			}
		case 's':
			if ch == '\'' {
				if next == '\'' {
					out.WriteByte(ch)
					i++
					out.WriteByte(next)
					continue
				}
				state = 'n'
			}
		case 'd':
			if ch == '"' {
				if next == '"' {
					out.WriteByte(ch)
					i++
					out.WriteByte(next)
					continue
				}
				state = 'n'
			}
		case 'b':
			if ch == '`' {
				state = 'n'
			}
		case 'a':
			if ch == ']' {
				state = 'n'
			}
		case 'l':
			if ch == '\n' {
				state = 'n'
			}
		case 'c':
			if ch == '*' && next == '/' {
				out.WriteByte(ch)
				i++
				out.WriteByte(next)
				state = 'n'
				continue
			}
		}
		out.WriteByte(ch)
	}
	if arg != len(args) {
		return "", fmt.Errorf("SQL query has %d placeholders but %d arguments", arg, len(args))
	}
	return out.String(), nil
}

func adaptQuery(query string, args []any, dialect string) (string, []any, error) {
	if dialect != "postgres" {
		return query, args, nil
	}
	bound, err := bindPlaceholders(query, args)
	if err != nil {
		return "", nil, err
	}
	ignore := insertIgnore.MatchString(bound)
	if ignore {
		bound = insertIgnore.ReplaceAllString(bound, "INSERT INTO")
	}
	bound = datetimeNow.ReplaceAllString(bound, "CURRENT_TIMESTAMP")
	bound = datetimeNowModifier.ReplaceAllString(bound, "(CURRENT_TIMESTAMP + CAST($1 AS INTERVAL))")
	bound = julianNowDiff.ReplaceAllString(bound, "EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - CAST(MIN(played_at) AS TIMESTAMPTZ))) / 86400.0")
	bound = sqliteHour.ReplaceAllString(bound, "CAST(EXTRACT(HOUR FROM CAST($1 AS TIMESTAMPTZ)) AS INTEGER)")
	bound = sqliteDate.ReplaceAllString(bound, "CAST(CAST($1 AS TIMESTAMPTZ) AS DATE)")
	if ignore && !strings.Contains(strings.ToUpper(bound), "ON CONFLICT") {
		bound = strings.TrimSuffix(strings.TrimSpace(bound), ";") + " ON CONFLICT DO NOTHING"
	}
	return bound, args, nil
}

func (d *Database) Exec(query string, args ...any) (sql.Result, error) {
	query, bound, err := adaptQuery(query, args, d.Dialect)
	if err != nil {
		return nil, err
	}
	if d.Dialect == "postgres" && insertIDTable.MatchString(query) && !strings.Contains(strings.ToUpper(query), "RETURNING") {
		var id int64
		err := d.DB.QueryRow(query+" RETURNING id", bound...).Scan(&id)
		if err == sql.ErrNoRows {
			return insertResult{rows: 0, hasID: true}, nil
		}
		if err != nil {
			return nil, err
		}
		return insertResult{id: id, rows: 1, hasID: true}, nil
	}
	return d.DB.Exec(query, bound...)
}

func (d *Database) Query(query string, args ...any) (*sql.Rows, error) {
	query, bound, err := adaptQuery(query, args, d.Dialect)
	if err != nil {
		return nil, err
	}
	return d.DB.Query(query, bound...)
}

func (d *Database) QueryRow(query string, args ...any) *sql.Row {
	query, bound, err := adaptQuery(query, args, d.Dialect)
	if err != nil {
		return d.DB.QueryRow("SELECT FROM musik_invalid_query")
	}
	return d.DB.QueryRow(query, bound...)
}

func (d *Database) Begin() (*DatabaseTx, error) {
	tx, err := d.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &DatabaseTx{Tx: tx, owner: d}, nil
}

func (tx *DatabaseTx) Exec(query string, args ...any) (sql.Result, error) {
	query, bound, err := adaptQuery(query, args, tx.owner.Dialect)
	if err != nil {
		return nil, err
	}
	if tx.owner.Dialect == "postgres" && insertIDTable.MatchString(query) && !strings.Contains(strings.ToUpper(query), "RETURNING") {
		var id int64
		err := tx.Tx.QueryRow(query+" RETURNING id", bound...).Scan(&id)
		if err == sql.ErrNoRows {
			return insertResult{rows: 0, hasID: true}, nil
		}
		if err != nil {
			return nil, err
		}
		return insertResult{id: id, rows: 1, hasID: true}, nil
	}
	return tx.Tx.Exec(query, bound...)
}

func (tx *DatabaseTx) Query(query string, args ...any) (*sql.Rows, error) {
	query, bound, err := adaptQuery(query, args, tx.owner.Dialect)
	if err != nil {
		return nil, err
	}
	return tx.Tx.Query(query, bound...)
}

func (tx *DatabaseTx) QueryRow(query string, args ...any) *sql.Row {
	query, bound, err := adaptQuery(query, args, tx.owner.Dialect)
	if err != nil {
		return tx.Tx.QueryRow("SELECT FROM musik_invalid_query")
	}
	return tx.Tx.QueryRow(query, bound...)
}
