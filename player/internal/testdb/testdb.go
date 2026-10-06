// Package testdb creates the explicit SQL fixture used by Go tests. Production
// code never creates or migrates the shared database.
package testdb

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Schema is generated from the migration history by
// scripts/go_test_schema.py; tests/test_go_test_schema.py rejects drift.
//
//go:embed schema.sql
var Schema string

func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Exec(Schema); err != nil {
		return fmt.Errorf("create test database: %w", err)
	}
	return nil
}
