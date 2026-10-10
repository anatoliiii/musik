package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type CustomTag struct {
	ID         string `json:"tag_id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	ArchivedAt string `json:"archived_at,omitempty"`
	TrackCount int    `json:"track_count"`
}

func (s *Store) CreateCustomTag(name string) (CustomTag, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CustomTag{}, fmt.Errorf("name required")
	}
	tag := CustomTag{ID: NewID(), Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	_, err := s.DB.Exec(`INSERT INTO custom_tags(tag_id, name, created_at, profile_id) VALUES (?,?,?,?)`,
		tag.ID, tag.Name, tag.CreatedAt, s.ProfileID)
	return tag, err
}

func (s *Store) ListCustomTags() ([]CustomTag, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, tag *CustomTag) error {
		return rows.Scan(&tag.ID, &tag.Name, &tag.CreatedAt, &tag.ArchivedAt, &tag.TrackCount)
	}, `
SELECT t.tag_id, t.name, t.created_at, COALESCE(t.archived_at,''),
       (SELECT COUNT(*) FROM track_tags tt WHERE tt.profile_id = t.profile_id AND tt.tag_id = t.tag_id)
FROM custom_tags t
WHERE t.profile_id = ? AND t.archived_at IS NULL
ORDER BY t.name`, s.ProfileID)
}

func (s *Store) TagTrack(tagID string, trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO track_tags(tag_id, track_id, created_at, profile_id) VALUES (?,?,?,?)
ON CONFLICT DO NOTHING`,
		tagID, trackID, now, s.ProfileID)
	return err
}

func (s *Store) UntagTrack(tagID string, trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM track_tags WHERE profile_id = ? AND tag_id = ? AND track_id = ?`, s.ProfileID, tagID, trackID)
	return err
}
