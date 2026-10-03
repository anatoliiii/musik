package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ResolveDatabase selects and validates the same backend for player and worker.
func ResolveDatabase(databaseURL, legacyPath, defaultPath string) (string, error) {
	if databaseURL == "" {
		return defaultPath, nil
	}
	if legacyPath != "" {
		return "", fmt.Errorf("MUSIK_DATABASE_URL and MUSIK_DB_PATH cannot both be set")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid MUSIK_DATABASE_URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "sqlite":
		if u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(u.Path) {
			return "", fmt.Errorf("MUSIK_DATABASE_URL must be an absolute sqlite:/// path without options")
		}
		path, err := url.PathUnescape(u.EscapedPath())
		if err != nil || path == "" {
			return "", fmt.Errorf("invalid SQLite path in MUSIK_DATABASE_URL")
		}
		return path, nil
	case "postgres", "postgresql":
		if u.Host == "" || strings.Trim(u.Path, "/") == "" {
			return "", fmt.Errorf("PostgreSQL MUSIK_DATABASE_URL must include a host and database")
		}
		parts := strings.SplitN(databaseURL, "://", 2)
		return "postgresql://" + parts[1], nil
	default:
		return "", fmt.Errorf("unsupported MUSIK_DATABASE_URL scheme")
	}
}
