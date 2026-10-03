package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// Database is the compatibility boundary for older repository SQL. GORM owns
// backend creation and typed writes; this adapter keeps legacy repository
// reads portable while they are moved to mapped models.
type Database struct {
	*sql.DB
	ORM       *gorm.DB
	Dialect   string
	ProfileID string
}

type DatabaseTx struct {
	ORM   *gorm.DB
	owner *Database
}

type DatabaseStatement struct {
	tx    *DatabaseTx
	query string
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
	sqliteDatetime      = regexp.MustCompile(`(?i)\bdatetime\(\s*([^(),]+)\s*\)`)
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
	return adaptQueryProfile(query, args, dialect, "")
}

// bindRepositoryPlaceholders binds positional values and the server-selected
// profile ID in one pass. The repository never accepts an owner/profile value
// from an HTTP payload.
func bindRepositoryPlaceholders(query string, args []any, profileID, dialect string) (string, []any, error) {
	var out strings.Builder
	bound := make([]any, 0, len(args)+1)
	arg := 0
	state := byte('n')
	for i := 0; i < len(query); i++ {
		ch := query[i]
		var next byte
		if i+1 < len(query) {
			next = query[i+1]
		}
		if state == 'n' {
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
					return "", nil, fmt.Errorf("SQL query has more placeholders than arguments")
				}
				arg++
				bound = append(bound, args[arg-1])
				if dialect == "postgres" {
					fmt.Fprintf(&out, "$%d", len(bound))
				} else {
					out.WriteByte('?')
				}
				continue
			case strings.HasPrefix(query[i:], ":musik_profile"):
				bound = append(bound, profileID)
				if dialect == "postgres" {
					fmt.Fprintf(&out, "$%d", len(bound))
				} else {
					out.WriteByte('?')
				}
				i += len(":musik_profile") - 1
				continue
			}
		} else {
			switch state {
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
		}
		out.WriteByte(ch)
	}
	if arg != len(args) {
		return "", nil, fmt.Errorf("SQL query has %d placeholders but %d arguments", arg, len(args))
	}
	return out.String(), bound, nil
}

func adaptQueryProfile(query string, args []any, dialect, profileID string) (string, []any, error) {
	query, args, err := bindRepositoryPlaceholders(query, args, profileID, dialect)
	if err != nil {
		return "", nil, err
	}
	if dialect != "postgres" {
		return query, args, nil
	}
	bound := query
	ignore := insertIgnore.MatchString(bound)
	if ignore {
		bound = insertIgnore.ReplaceAllString(bound, "INSERT INTO")
	}
	bound = datetimeNow.ReplaceAllString(bound, "CURRENT_TIMESTAMP")
	bound = datetimeNowModifier.ReplaceAllString(bound, "(CURRENT_TIMESTAMP + CAST($1 AS INTERVAL))")
	bound = sqliteDatetime.ReplaceAllString(bound, "CAST($1 AS TIMESTAMPTZ)")
	bound = julianNowDiff.ReplaceAllString(bound, "EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - CAST(MIN(played_at) AS TIMESTAMPTZ))) / 86400.0")
	bound = sqliteHour.ReplaceAllString(bound, "CAST(EXTRACT(HOUR FROM CAST($1 AS TIMESTAMPTZ)) AS INTEGER)")
	bound = sqliteDate.ReplaceAllString(bound, "CAST(CAST($1 AS TIMESTAMPTZ) AS DATE)")
	if ignore && !strings.Contains(strings.ToUpper(bound), "ON CONFLICT") {
		bound = strings.TrimSuffix(strings.TrimSpace(bound), ";") + " ON CONFLICT DO NOTHING"
	}
	return bound, args, nil
}

func (d *Database) Exec(query string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), query, args...)
}

func (d *Database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, bound, err := adaptQueryProfile(query, args, d.Dialect, d.ProfileID)
	if err != nil {
		return nil, err
	}
	if d.Dialect == "postgres" && insertIDTable.MatchString(query) && !strings.Contains(strings.ToUpper(query), "RETURNING") {
		var id int64
		err := d.ORM.WithContext(ctx).Raw(query+" RETURNING id", bound...).Row().Scan(&id)
		if err == sql.ErrNoRows {
			return insertResult{rows: 0, hasID: true}, nil
		}
		if err != nil {
			return nil, err
		}
		return insertResult{id: id, rows: 1, hasID: true}, nil
	}
	result := d.ORM.WithContext(ctx).Exec(query, bound...)
	if result.Error != nil {
		return nil, result.Error
	}
	return insertResult{rows: result.RowsAffected}, nil
}

func (d *Database) Query(query string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), query, args...)
}

func (d *Database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, bound, err := adaptQueryProfile(query, args, d.Dialect, d.ProfileID)
	if err != nil {
		return nil, err
	}
	return d.ORM.WithContext(ctx).Raw(query, bound...).Rows()
}

func (d *Database) QueryRow(query string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), query, args...)
}

func (d *Database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query, bound, err := adaptQueryProfile(query, args, d.Dialect, d.ProfileID)
	if err != nil {
		return d.ORM.WithContext(ctx).Raw("SELECT FROM musik_invalid_query").Row()
	}
	return d.ORM.WithContext(ctx).Raw(query, bound...).Row()
}

func (d *Database) Begin() (*DatabaseTx, error) {
	return d.BeginTx(context.Background(), nil)
}

func (d *Database) BeginTx(ctx context.Context, options *sql.TxOptions) (*DatabaseTx, error) {
	var tx *gorm.DB
	if options != nil {
		tx = d.ORM.WithContext(ctx).Begin(options)
	} else {
		tx = d.ORM.WithContext(ctx).Begin()
	}
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &DatabaseTx{ORM: tx, owner: d}, nil
}

func (tx *DatabaseTx) Commit() error { return tx.ORM.Commit().Error }

func (tx *DatabaseTx) Rollback() error { return tx.ORM.Rollback().Error }

func (tx *DatabaseTx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(context.Background(), query, args...)
}

func (tx *DatabaseTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, bound, err := adaptQueryProfile(query, args, tx.owner.Dialect, tx.owner.ProfileID)
	if err != nil {
		return nil, err
	}
	if tx.owner.Dialect == "postgres" && insertIDTable.MatchString(query) && !strings.Contains(strings.ToUpper(query), "RETURNING") {
		var id int64
		err := tx.ORM.WithContext(ctx).Raw(query+" RETURNING id", bound...).Row().Scan(&id)
		if err == sql.ErrNoRows {
			return insertResult{rows: 0, hasID: true}, nil
		}
		if err != nil {
			return nil, err
		}
		return insertResult{id: id, rows: 1, hasID: true}, nil
	}
	result := tx.ORM.WithContext(ctx).Exec(query, bound...)
	if result.Error != nil {
		return nil, result.Error
	}
	return insertResult{rows: result.RowsAffected}, nil
}

func (tx *DatabaseTx) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(context.Background(), query, args...)
}

func (tx *DatabaseTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, bound, err := adaptQueryProfile(query, args, tx.owner.Dialect, tx.owner.ProfileID)
	if err != nil {
		return nil, err
	}
	return tx.ORM.WithContext(ctx).Raw(query, bound...).Rows()
}

func (tx *DatabaseTx) QueryRow(query string, args ...any) *sql.Row {
	return tx.QueryRowContext(context.Background(), query, args...)
}

func (tx *DatabaseTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	query, bound, err := adaptQueryProfile(query, args, tx.owner.Dialect, tx.owner.ProfileID)
	if err != nil {
		return tx.ORM.WithContext(ctx).Raw("SELECT FROM musik_invalid_query").Row()
	}
	return tx.ORM.WithContext(ctx).Raw(query, bound...).Row()
}

func (tx *DatabaseTx) Prepare(query string) (*DatabaseStatement, error) {
	return &DatabaseStatement{tx: tx, query: query}, nil
}

func (s *DatabaseStatement) Exec(args ...any) (sql.Result, error) {
	query, bound, err := adaptQueryProfile(s.query, args, s.tx.owner.Dialect, s.tx.owner.ProfileID)
	if err != nil {
		return nil, err
	}
	if s.tx.owner.Dialect == "postgres" && insertIDTable.MatchString(query) && !strings.Contains(strings.ToUpper(query), "RETURNING") {
		var id int64
		if err := s.tx.ORM.Raw(query+" RETURNING id", bound...).Row().Scan(&id); err != nil {
			if err == sql.ErrNoRows {
				return insertResult{rows: 0, hasID: true}, nil
			}
			return nil, err
		}
		return insertResult{id: id, rows: 1, hasID: true}, nil
	}
	result := s.tx.ORM.Exec(query, bound...)
	if result.Error != nil {
		return nil, result.Error
	}
	return insertResult{rows: result.RowsAffected}, nil
}

func (s *DatabaseStatement) Close() error { return nil }
