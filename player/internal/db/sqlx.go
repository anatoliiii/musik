package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// Dialect names the configured backend. Repository SQL is written once in the
// subset both engines share; the few real differences are methods here, so
// they stay visible at the call site instead of being rewritten behind it.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// LockForUpdate is appended to a SELECT that reads a row before a dependent
// write. PostgreSQL takes a row lock; SQLite write transactions already hold
// the database write lock from BEGIN IMMEDIATE.
func (d Dialect) LockForUpdate() string {
	if d == Postgres {
		return " FOR UPDATE"
	}
	return ""
}

// HourUTC is the UTC hour of an ISO-8601 timestamp column.
func (d Dialect) HourUTC(column string) string {
	if d == Postgres {
		return "CAST(EXTRACT(HOUR FROM CAST(" + column + " AS TIMESTAMPTZ) AT TIME ZONE 'UTC') AS INTEGER)"
	}
	return "CAST(strftime('%H', " + column + ") AS INTEGER)"
}

// DateUTC is the UTC calendar date (YYYY-MM-DD text) of an ISO-8601 timestamp column.
func (d Dialect) DateUTC(column string) string {
	if d == Postgres {
		return "to_char(CAST(" + column + " AS TIMESTAMPTZ) AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
	}
	return "date(" + column + ")"
}

// DB is the shared connection pool. Queries use '?' placeholders, which are
// rebound for the selected driver; the SQL text is otherwise sent unchanged.
type DB struct {
	x       *sqlx.DB
	Dialect Dialect
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), query, args...)
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.x.ExecContext(ctx, d.x.Rebind(query), args...)
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), query, args...)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.x.QueryContext(ctx, d.x.Rebind(query), args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), query, args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.x.QueryRowContext(ctx, d.x.Rebind(query), args...)
}

// Get scans one row into dest (a struct with `db` tags or a scalar).
func (d *DB) Get(ctx context.Context, dest any, query string, args ...any) error {
	return d.x.GetContext(ctx, dest, d.x.Rebind(query), args...)
}

// Select scans all rows into dest (a slice of structs or scalars).
func (d *DB) Select(ctx context.Context, dest any, query string, args ...any) error {
	return d.x.SelectContext(ctx, dest, d.x.Rebind(query), args...)
}

func (d *DB) Begin() (*Tx, error) { return d.BeginTx(context.Background(), nil) }

func (d *DB) BeginTx(ctx context.Context, options *sql.TxOptions) (*Tx, error) {
	tx, err := d.x.BeginTxx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Tx{x: tx, Dialect: d.Dialect}, nil
}

// InTx runs fn in one transaction and commits only if fn succeeds.
func (d *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *DB) Close() error { return d.x.Close() }

// Tx mirrors DB inside one transaction.
type Tx struct {
	x       *sqlx.Tx
	Dialect Dialect
}

func (tx *Tx) Commit() error { return tx.x.Commit() }

// Rollback is safe to defer after Commit.
func (tx *Tx) Rollback() error {
	if err := tx.x.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

func (tx *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(context.Background(), query, args...)
}

func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.x.ExecContext(ctx, tx.x.Rebind(query), args...)
}

func (tx *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(context.Background(), query, args...)
}

func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.x.QueryContext(ctx, tx.x.Rebind(query), args...)
}

func (tx *Tx) QueryRow(query string, args ...any) *sql.Row {
	return tx.QueryRowContext(context.Background(), query, args...)
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.x.QueryRowContext(ctx, tx.x.Rebind(query), args...)
}

func (tx *Tx) Get(ctx context.Context, dest any, query string, args ...any) error {
	return tx.x.GetContext(ctx, dest, tx.x.Rebind(query), args...)
}

// Prepare returns a statement bound for this transaction.
func (tx *Tx) Prepare(query string) (*sql.Stmt, error) {
	return tx.x.Prepare(tx.x.Rebind(query))
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// QueryAll runs query and scans every row into a T with scan.
func QueryAll[T any](q querier, scan func(rows *sql.Rows, v *T) error, query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
