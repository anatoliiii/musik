package config

import "testing"

func TestResolveDatabase(t *testing.T) {
	cases := []struct {
		name, url, legacy, fallback, want string
		wantError                         bool
	}{
		{"legacy default", "", "", "/data/musik.db", "/data/musik.db", false},
		{"legacy explicit", "", "/other.db", "/other.db", "/other.db", false},
		{"SQLite URL", "sqlite:///data/music%20db.sqlite", "", "/old.db", "/data/music db.sqlite", false},
		{"conflict", "sqlite:///data/db", "/other.db", "/other.db", "", true},
		{"relative URL", "sqlite://relative/db", "", "/old.db", "", true},
		{"PostgreSQL URL", "postgresql://user:secret@host/db", "", "/old.db", "postgresql://user:secret@host/db", false},
		{"PostgreSQL alias", "postgres://user:secret@host/db", "", "/old.db", "postgresql://user:secret@host/db", false},
		{"PostgreSQL requires host", "postgresql:///musik", "", "/old.db", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDatabase(tc.url, tc.legacy, tc.fallback)
			if got != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("ResolveDatabase = %q, %v; want %q, error=%v", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestDataRootIsIndependentFromDatabaseBackend(t *testing.T) {
	if got := (Config{DataDir: "/data", DBPath: "/app/data/db/musik.db"}).DataRoot(); got != "/data" {
		t.Fatalf("configured data root = %q, want /data", got)
	}
	if got := (Config{DBPath: "/music/data/db/musik.db"}).DataRoot(); got != "/music/data" {
		t.Fatalf("legacy data root = %q, want /music/data", got)
	}
}
