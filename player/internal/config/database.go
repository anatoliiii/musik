package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ResolveDatabase selects the SQLite path shared by the player and worker.
// PostgreSQL URLs are recognized but rejected until both storage adapters exist.
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
		return "", fmt.Errorf("PostgreSQL storage adapter is not available yet")
	default:
		return "", fmt.Errorf("unsupported MUSIK_DATABASE_URL scheme")
	}
}
