package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Connection keeps profile binding inside the repository adapter. SQL uses a
// named profile parameter; services never supply SQL or owner parameters.
type Connection struct {
	*sql.DB
	ProfileID string
}
type Tx struct {
	*sql.Tx
	ProfileID string
}
type Statement struct {
	*sql.Stmt
	ProfileID string
	profile   bool
}

// bindSQL recognizes placeholders outside quoted strings and comments. It does
// not replace question marks in JSON, literals or comments.
func bindSQL(query string) (string, bool) {
	var out strings.Builder
	index := 0
	profile := false
	for i := 0; i < len(query); {
		start := i
		if query[i] == '\'' || query[i] == '"' || query[i] == '`' || query[i] == '[' {
			end := query[i]
			if end == '[' {
				end = ']'
			}
			i++
			for i < len(query) {
				if query[i] == end {
					i++
					if i < len(query) && query[i] == end {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(query[start:i])
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "--" {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			out.WriteString(query[start:i])
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			i += 2
			for i+1 < len(query) && query[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(query) {
				i += 2
			}
			out.WriteString(query[start:i])
			continue
		}
		if query[i] == '?' {
			index++
			out.WriteString(fmt.Sprintf(":arg%d", index))
			i++
			continue
		}
		if strings.HasPrefix(query[i:], ":musik_profile") {
			profile = true
		}
		out.WriteByte(query[i])
		i++
	}
	return out.String(), profile
}
func bindArgs(args []any, profile bool, id string) []any {
	out := make([]any, 0, len(args)+1)
	for i, arg := range args {
		out = append(out, sql.Named(fmt.Sprintf("arg%d", i+1), arg))
	}
	if profile {
		out = append(out, sql.Named("musik_profile", id))
	}
	return out
}
func (c *Connection) Exec(q string, a ...any) (sql.Result, error) {
	return c.ExecContext(context.Background(), q, a...)
}
func (c *Connection) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	q, p := bindSQL(q)
	return c.DB.ExecContext(ctx, q, bindArgs(a, p, c.ProfileID)...)
}
func (c *Connection) Query(q string, a ...any) (*sql.Rows, error) {
	return c.QueryContext(context.Background(), q, a...)
}
func (c *Connection) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	q, p := bindSQL(q)
	return c.DB.QueryContext(ctx, q, bindArgs(a, p, c.ProfileID)...)
}
func (c *Connection) QueryRow(q string, a ...any) *sql.Row {
	return c.QueryRowContext(context.Background(), q, a...)
}
func (c *Connection) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	q, p := bindSQL(q)
	return c.DB.QueryRowContext(ctx, q, bindArgs(a, p, c.ProfileID)...)
}
func (c *Connection) Begin() (*Tx, error) { return c.BeginTx(context.Background(), nil) }
func (c *Connection) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := c.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, ProfileID: c.ProfileID}, nil
}
func (t *Tx) Exec(q string, a ...any) (sql.Result, error) {
	return t.ExecContext(context.Background(), q, a...)
}
func (t *Tx) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	q, p := bindSQL(q)
	return t.Tx.ExecContext(ctx, q, bindArgs(a, p, t.ProfileID)...)
}
func (t *Tx) Query(q string, a ...any) (*sql.Rows, error) {
	q, p := bindSQL(q)
	return t.Tx.Query(q, bindArgs(a, p, t.ProfileID)...)
}
func (t *Tx) QueryRow(q string, a ...any) *sql.Row {
	return t.QueryRowContext(context.Background(), q, a...)
}
func (t *Tx) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	q, p := bindSQL(q)
	return t.Tx.QueryRowContext(ctx, q, bindArgs(a, p, t.ProfileID)...)
}
func (t *Tx) Prepare(q string) (*Statement, error) {
	q, p := bindSQL(q)
	stmt, err := t.Tx.Prepare(q)
	if err != nil {
		return nil, err
	}
	return &Statement{Stmt: stmt, ProfileID: t.ProfileID, profile: p}, nil
}
func (s *Statement) Exec(a ...any) (sql.Result, error) {
	return s.Stmt.Exec(bindArgs(a, s.profile, s.ProfileID)...)
}
