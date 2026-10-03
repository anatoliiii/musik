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
	_, err := s.DB.Exec(`INSERT INTO custom_tags(tag_id, name, created_at,profile_id) VALUES (?,?,?,:musik_profile)`,
		tag.ID, tag.Name, tag.CreatedAt)
	return tag, err
}

func (s *Store) ListCustomTags() ([]CustomTag, error) {
	rows, err := s.DB.Query(`
SELECT t.tag_id, t.name, t.created_at, COALESCE(t.archived_at,''),
       (SELECT COUNT(*) FROM (SELECT * FROM track_tags WHERE profile_id=:musik_profile) tt WHERE tt.tag_id = t.tag_id)
FROM (SELECT * FROM custom_tags WHERE profile_id=:musik_profile) t
WHERE t.archived_at IS NULL
ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomTag
	for rows.Next() {
		var tag CustomTag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.CreatedAt, &tag.ArchivedAt, &tag.TrackCount); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (s *Store) ArchiveCustomTag(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`UPDATE custom_tags SET archived_at = ? WHERE custom_tags.profile_id=:musik_profile AND ( tag_id = ?) `, now, id)
	return err
}

func (s *Store) TagTrack(tagID string, trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT OR IGNORE INTO track_tags(tag_id, track_id, created_at,profile_id) VALUES (?,?,?,:musik_profile)`,
		tagID, trackID, now)
	return err
}

func (s *Store) UntagTrack(tagID string, trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM track_tags WHERE track_tags.profile_id=:musik_profile AND ( tag_id = ? AND track_id = ?) `, tagID, trackID)
	return err
}

func (s *Store) TrackIDsForTagNames(names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = "?"
		args[i] = strings.ToLower(strings.TrimSpace(name))
	}
	rows, err := s.DB.Query(`
SELECT DISTINCT tt.track_id
FROM (SELECT * FROM track_tags WHERE profile_id=:musik_profile) tt
JOIN (SELECT * FROM custom_tags WHERE profile_id=:musik_profile) t ON t.tag_id = tt.tag_id
WHERE t.archived_at IS NULL AND lower(t.name) IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) SetTrackPreference(trackID int64, rating string, favorite *bool) error {
	if rating == "" {
		rating = "neutral"
	}
	switch rating {
	case "like", "dislike", "neutral":
	default:
		return fmt.Errorf("rating must be like, dislike or neutral")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fav := 0
	if favorite != nil && *favorite {
		fav = 1
	}
	if favorite == nil {
		var existing int
		err := s.DB.QueryRow(`SELECT favorite FROM (SELECT * FROM track_preferences WHERE profile_id=:musik_profile) AS track_preferences WHERE track_id = ?`, trackID).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			fav = existing
		}
	}
	_, err := s.DB.Exec(`
INSERT INTO track_preferences(track_id, rating, favorite, updated_at,profile_id) VALUES (?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET
  rating=excluded.rating,
  favorite=excluded.favorite,
  updated_at=excluded.updated_at`,
		trackID, rating, fav, now)
	return err
}
