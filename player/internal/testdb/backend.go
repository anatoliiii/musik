package testdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Target prepares an isolated test database and returns the arguments for
// db.OpenDatabase. SQLite is the default. MUSIK_TEST_BACKEND=postgres runs the
// same test against a fresh PostgreSQL database cloned from the migrated
// template named by MUSIK_TEST_POSTGRES_TEMPLATE (default "musik_template"),
// using the server in MUSIK_TEST_POSTGRES_URL.
func Target(t testing.TB, sqlitePath string) (databaseURL, path string) {
	t.Helper()
	if os.Getenv("MUSIK_TEST_BACKEND") != "postgres" {
		if err := Create(sqlitePath); err != nil {
			t.Fatal(err)
		}
		return "", sqlitePath
	}
	server := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if server == "" {
		t.Fatal("MUSIK_TEST_BACKEND=postgres requires MUSIK_TEST_POSTGRES_URL")
	}
	template := os.Getenv("MUSIK_TEST_POSTGRES_TEMPLATE")
	if template == "" {
		template = "musik_template"
	}
	admin, err := sql.Open("pgx", server)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "musik_t_" + hex.EncodeToString(suffix[:])
	// Clones from one template are serialized so no copy observes another.
	if _, err := admin.Exec(`SELECT pg_advisory_lock(727001)`); err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(`CREATE DATABASE ` + name + ` TEMPLATE ` + quoteIdent(template))
	_, _ = admin.Exec(`SELECT pg_advisory_unlock(727001)`)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("clone PostgreSQL template %q: %v", template, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
		_ = admin.Close()
	})
	u, err := url.Parse(server)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String(), sqlitePath
}

func quoteIdent(name string) string {
	out := []byte{'"'}
	for i := 0; i < len(name); i++ {
		if name[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, name[i])
	}
	return string(append(out, '"'))
}
