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

func TestLoadDefaultWebEdition(t *testing.T) {
	t.Setenv("MUSIK_UI", "mozgolika")
	if got := Load().UI; got != "mozgolika" {
		t.Fatalf("UI=%q", got)
	}
	t.Setenv("MUSIK_UI", "")
	if got := Load().UI; got != "mozgolika" {
		t.Fatalf("default UI=%q", got)
	}
}

func TestLoadAuthAllowedOrigins(t *testing.T) {
	t.Setenv("MUSIK_AUTH_ALLOWED_ORIGINS", " https://home.example.test, ,https://vpn.example.test:8443 ")
	got := Load().AuthAllowedOrigins
	if len(got) != 2 || got[0] != "https://home.example.test" || got[1] != "https://vpn.example.test:8443" {
		t.Fatalf("AuthAllowedOrigins=%q", got)
	}
	t.Setenv("MUSIK_AUTH_ALLOWED_ORIGINS", "")
	if len(Load().AuthAllowedOrigins) != 0 {
		t.Fatal("additional origins must be disabled by default")
	}
}
