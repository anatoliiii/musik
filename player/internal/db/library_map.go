package db

import "database/sql"

// LibraryMapKey is the scan_state row the worker writes the library map to
// (musik.index.library_map).
const LibraryMapKey = "library_map"

// LibraryMapJSON returns the raw map document, or "" before the first build.
func (s *Store) LibraryMapJSON() (string, error) {
	var value string
	err := s.DB.QueryRow(`SELECT value FROM scan_state WHERE key = ?`, LibraryMapKey).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
