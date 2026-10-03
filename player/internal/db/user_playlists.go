package db

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type UserPlaylist struct {
	ID              int64              `json:"id"`
	Kind            string             `json:"kind"`
	Type            string             `json:"type"`
	Name            string             `json:"name"`
	Description     string             `json:"description,omitempty"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at,omitempty"`
	CoverTrackID    int64              `json:"cover_track_id,omitempty"`
	CoverArtwork    string             `json:"cover_artwork,omitempty"`
	SortMode        string             `json:"sort_mode"`
	ArchivedAt      string             `json:"archived_at,omitempty"`
	RuleSchema      int                `json:"rule_schema_version,omitempty"`
	RuleJSON        string             `json:"rule_json,omitempty"`
	AllowDuplicates bool               `json:"allow_duplicates"`
	Tracks          []UserPlaylistItem `json:"tracks,omitempty"`
	TrackCount      int                `json:"track_count"`
}

type UserPlaylistItem struct {
	ItemID           string  `json:"item_id"`
	Position         int     `json:"position"`
	TrackID          int64   `json:"track_id,omitempty"`
	Artist           string  `json:"artist,omitempty"`
	Title            string  `json:"title,omitempty"`
	Album            string  `json:"album,omitempty"`
	Duration         float64 `json:"duration,omitempty"`
	Source           string  `json:"source"`
	Note             string  `json:"note,omitempty"`
	Explanation      string  `json:"explanation,omitempty"`
	AddedAt          string  `json:"added_at,omitempty"`
	UnresolvedArtist string  `json:"unresolved_artist,omitempty"`
	UnresolvedTitle  string  `json:"unresolved_title,omitempty"`
	UnresolvedPath   string  `json:"unresolved_path,omitempty"`
	Unresolved       bool    `json:"unresolved,omitempty"`
}

func validPlaylistType(v string) bool {
	switch v {
	case "generated", "manual", "smart":
		return true
	default:
		return false
	}
}

func validItemSource(v string) bool {
	switch v {
	case "manual", "radio", "import", "rule":
		return true
	default:
		return false
	}
}

func (s *Store) CreateUserPlaylist(pl UserPlaylist) (UserPlaylist, error) {
	pl.Name = strings.TrimSpace(pl.Name)
	if pl.Name == "" {
		return pl, fmt.Errorf("name required")
	}
	if pl.Type == "" {
		pl.Type = "manual"
	}
	if !validPlaylistType(pl.Type) {
		return pl, fmt.Errorf("type must be generated, manual or smart")
	}
	if pl.Kind == "" {
		pl.Kind = "user"
	}
	if pl.SortMode == "" {
		pl.SortMode = "manual"
	}
	if pl.Type == "smart" && pl.RuleJSON != "" && pl.RuleSchema == 0 {
		pl.RuleSchema = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pl.CreatedAt, pl.UpdatedAt = now, now
	dup := 0
	if pl.AllowDuplicates {
		dup = 1
	}
	var ruleJSON *string
	var ruleVer *int64
	if pl.RuleJSON != "" {
		ruleJSON = &pl.RuleJSON
		version := int64(pl.RuleSchema)
		ruleVer = &version
	}
	var coverTrackID *int64
	if pl.CoverTrackID > 0 {
		coverTrackID = &pl.CoverTrackID
	}
	record := PlaylistRecord{
		ProfileID: s.DB.ProfileID, Kind: pl.Kind, Name: pl.Name, CreatedAt: pl.CreatedAt,
		Type: pl.Type, Description: optionalString(pl.Description), UpdatedAt: &pl.UpdatedAt,
		CoverTrackID: coverTrackID, CoverArtwork: optionalString(pl.CoverArtwork),
		SortMode: pl.SortMode, RuleSchemaVersion: ruleVer, RuleJSON: ruleJSON,
		AllowDuplicates: dup,
	}
	if err := s.ORM.Create(&record).Error; err != nil {
		return pl, err
	}
	pl.ID = record.ID
	return pl, nil
}

func (s *Store) UpdateUserPlaylist(pl UserPlaylist) error {
	if pl.ID == 0 {
		return fmt.Errorf("playlist id required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	dup := 0
	if pl.AllowDuplicates {
		dup = 1
	}
	var ruleJSON sql.NullString
	var ruleVer sql.NullInt64
	if pl.RuleJSON != "" {
		ruleJSON = sql.NullString{String: pl.RuleJSON, Valid: true}
		if pl.RuleSchema == 0 {
			pl.RuleSchema = 1
		}
		ruleVer = sql.NullInt64{Int64: int64(pl.RuleSchema), Valid: true}
	}
	_, err := s.DB.Exec(`
UPDATE playlists SET
  name = COALESCE(NULLIF(?, ''), name),
  description = COALESCE(?, description),
  cover_track_id = CASE WHEN ? > 0 THEN ? ELSE cover_track_id END,
  cover_artwork = COALESCE(NULLIF(?, ''), cover_artwork),
  sort_mode = COALESCE(NULLIF(?, ''), sort_mode),
  allow_duplicates = ?,
  rule_schema_version = CASE WHEN ? THEN ? ELSE rule_schema_version END,
  rule_json = CASE WHEN ? THEN ? ELSE rule_json END,
  updated_at = ?
WHERE playlists.profile_id=:musik_profile AND ( id = ?) `,
		pl.Name, nullStr(pl.Description), pl.CoverTrackID, pl.CoverTrackID,
		pl.CoverArtwork, pl.SortMode, dup,
		ruleJSON.Valid, ruleVer, ruleJSON.Valid, ruleJSON, now, pl.ID)
	return err
}

func (s *Store) ArchiveUserPlaylist(id int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`UPDATE playlists SET archived_at = ?, updated_at = ? WHERE playlists.profile_id=:musik_profile AND ( id = ?) `, now, now, id)
	return err
}

func (s *Store) DeleteUserPlaylist(id int64) error {
	_, err := s.DB.Exec(`
DELETE FROM playlists
WHERE playlists.profile_id=:musik_profile AND ( id = ? AND (type IN ('manual','smart') OR kind = 'user')) `, id)
	return err
}

func (s *Store) GetUserPlaylist(id int64) (*UserPlaylist, error) {
	pl, err := s.scanUserPlaylist(s.DB.QueryRow(`
SELECT id, kind, COALESCE(type,'generated'), name, COALESCE(description,''), created_at,
       COALESCE(updated_at,''), COALESCE(cover_track_id,0), COALESCE(cover_artwork,''),
       COALESCE(sort_mode,'manual'), COALESCE(archived_at,''),
       COALESCE(rule_schema_version,0), COALESCE(rule_json,''), COALESCE(allow_duplicates,0)
FROM (SELECT * FROM playlists WHERE profile_id=:musik_profile) AS playlists WHERE id = ?`, id))
	if err != nil || pl == nil {
		return pl, err
	}
	items, err := s.ListPlaylistItems(id)
	if err != nil {
		return nil, err
	}
	pl.Tracks = items
	pl.TrackCount = len(items)
	return pl, nil
}

func (s *Store) ListUserPlaylists(types []string, includeArchived bool) ([]UserPlaylist, error) {
	q := `
SELECT id, kind, COALESCE(type,'generated'), name, COALESCE(description,''), created_at,
       COALESCE(updated_at,''), COALESCE(cover_track_id,0), COALESCE(cover_artwork,''),
       COALESCE(sort_mode,'manual'), COALESCE(archived_at,''),
       COALESCE(rule_schema_version,0), COALESCE(rule_json,''), COALESCE(allow_duplicates,0)
FROM (SELECT * FROM playlists WHERE profile_id=:musik_profile) AS playlists WHERE 1=1`
	args := []any{}
	if len(types) > 0 {
		placeholders := make([]string, len(types))
		for i, t := range types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		q += ` AND type IN (` + strings.Join(placeholders, ",") + `)`
	} else {
		q += ` AND (type IN ('manual','smart') OR kind = 'user')`
	}
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY updated_at DESC, id DESC`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserPlaylist
	for rows.Next() {
		pl, err := s.scanUserPlaylist(rows)
		if err != nil {
			return nil, err
		}
		if pl == nil {
			continue
		}
		if err := s.DB.QueryRow(
			`SELECT COUNT(*) FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) AS playlist_tracks WHERE playlist_id = ?`, pl.ID,
		).Scan(&pl.TrackCount); err != nil {
			return nil, err
		}
		out = append(out, *pl)
	}
	return out, rows.Err()
}

func (s *Store) ListPlaylistItems(playlistID int64) ([]UserPlaylistItem, error) {
	rows, err := s.DB.Query(`
SELECT COALESCE(pt.item_id,''), pt.position, COALESCE(pt.track_id,0),
       COALESCE(t.artist, pt.unresolved_artist, ''),
       COALESCE(t.title, pt.unresolved_title, ''),
       COALESCE(t.album,''), COALESCE(t.duration,0),
       COALESCE(pt.source,'manual'), COALESCE(pt.note,''), COALESCE(pt.explanation,''),
       COALESCE(pt.added_at,''), COALESCE(pt.unresolved_artist,''),
       COALESCE(pt.unresolved_title,''), COALESCE(pt.unresolved_path,'')
FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) pt
LEFT JOIN tracks t ON t.id = pt.track_id
WHERE pt.playlist_id = ?
ORDER BY pt.position`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserPlaylistItem
	for rows.Next() {
		var item UserPlaylistItem
		if err := rows.Scan(&item.ItemID, &item.Position, &item.TrackID,
			&item.Artist, &item.Title, &item.Album, &item.Duration,
			&item.Source, &item.Note, &item.Explanation, &item.AddedAt,
			&item.UnresolvedArtist, &item.UnresolvedTitle, &item.UnresolvedPath); err != nil {
			return nil, err
		}
		item.Unresolved = item.TrackID == 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) AddPlaylistItem(playlistID int64, item UserPlaylistItem) (UserPlaylistItem, error) {
	if item.Source == "" {
		item.Source = "manual"
	}
	if !validItemSource(item.Source) {
		return item, fmt.Errorf("invalid item source")
	}
	if item.ItemID == "" {
		item.ItemID = NewID()
	}
	var allowDup int
	if err := s.DB.QueryRow(`SELECT COALESCE(allow_duplicates,0) FROM (SELECT * FROM playlists WHERE profile_id=:musik_profile) AS playlists WHERE id = ?`, playlistID).Scan(&allowDup); err != nil {
		return item, err
	}
	if allowDup == 0 && item.TrackID != 0 {
		var n int
		_ = s.DB.QueryRow(
			`SELECT COUNT(*) FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) AS playlist_tracks WHERE playlist_id = ? AND track_id = ?`,
			playlistID, item.TrackID,
		).Scan(&n)
		if n > 0 {
			return item, fmt.Errorf("duplicate track in playlist")
		}
	}
	if item.AddedAt == "" {
		item.AddedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) AS playlist_tracks WHERE playlist_id = ?`, playlistID).Scan(&item.Position); err != nil {
		return item, err
	}
	if _, err := tx.Exec(`
INSERT INTO playlist_tracks(
  item_id, playlist_id, position, track_id, unresolved_artist, unresolved_title,
  unresolved_path, added_at, source, note, explanation
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,:musik_profile)`,
		item.ItemID, playlistID, item.Position, nullInt64(item.TrackID),
		nullStr(item.UnresolvedArtist), nullStr(item.UnresolvedTitle), nullStr(item.UnresolvedPath),
		item.AddedAt, item.Source, nullStr(item.Note), nullStr(item.Explanation)); err != nil {
		return item, err
	}
	if _, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE playlists.profile_id=:musik_profile AND ( id = ?) `, item.AddedAt, playlistID); err != nil {
		return item, err
	}
	return item, tx.Commit()
}

func (s *Store) RemovePlaylistItem(playlistID int64, itemID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND item_id = ?) `, playlistID, itemID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if trackID, convErr := strconv.ParseInt(itemID, 10, 64); convErr == nil && trackID != 0 {
			if _, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND track_id = ?) `, playlistID, trackID); err != nil {
				return err
			}
		}
	}
	if err := compactPlaylistPositions(tx, playlistID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE playlists.profile_id=:musik_profile AND ( id = ?) `, now, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReorderPlaylistItems(playlistID int64, itemIDs []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range itemIDs {
		if _, err := tx.Exec(
			`UPDATE playlist_tracks SET position = ? WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND item_id = ?) `,
			-(i + 1), playlistID, id,
		); err != nil {
			return err
		}
	}
	for i, id := range itemIDs {
		if _, err := tx.Exec(
			`UPDATE playlist_tracks SET position = ? WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND item_id = ?) `,
			i, playlistID, id,
		); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE playlists.profile_id=:musik_profile AND ( id = ?) `, now, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReplacePlaylistItems(playlistID int64, items []UserPlaylistItem) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ?) `, playlistID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, item := range items {
		if item.ItemID == "" {
			item.ItemID = NewID()
		}
		if item.Source == "" {
			item.Source = "rule"
		}
		if item.AddedAt == "" {
			item.AddedAt = now
		}
		if _, err := tx.Exec(`
INSERT INTO playlist_tracks(
  item_id, playlist_id, position, track_id, unresolved_artist, unresolved_title,
  unresolved_path, added_at, source, note, explanation
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,:musik_profile)`,
			item.ItemID, playlistID, i, nullInt64(item.TrackID),
			nullStr(item.UnresolvedArtist), nullStr(item.UnresolvedTitle), nullStr(item.UnresolvedPath),
			item.AddedAt, item.Source, nullStr(item.Note), nullStr(item.Explanation)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE playlists SET updated_at = ? WHERE playlists.profile_id=:musik_profile AND ( id = ?) `, now, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DuplicateUserPlaylist(id int64) (*UserPlaylist, error) {
	src, err := s.GetUserPlaylist(id)
	if err != nil || src == nil {
		return src, err
	}
	copy := *src
	copy.ID = 0
	copy.Name = src.Name + " (копия)"
	copy.Kind = "user"
	if copy.Type == "generated" {
		copy.Type = "manual"
	}
	created, err := s.CreateUserPlaylist(copy)
	if err != nil {
		return nil, err
	}
	items := make([]UserPlaylistItem, 0, len(src.Tracks))
	for _, item := range src.Tracks {
		item.ItemID = ""
		if item.Source == "rule" && copy.Type == "manual" {
			item.Source = "manual"
		}
		items = append(items, item)
	}
	if err := s.ReplacePlaylistItems(created.ID, items); err != nil {
		return nil, err
	}
	return s.GetUserPlaylist(created.ID)
}

func (s *Store) ResolvePlaylistEntries() (int, error) {
	rows, err := s.DB.Query(`
SELECT item_id, playlist_id, unresolved_artist, unresolved_title, unresolved_path
FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) AS playlist_tracks WHERE track_id IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type unresolved struct {
		itemID, artist, title, path string
	}
	var pending []unresolved
	for rows.Next() {
		var u unresolved
		var playlistID int64
		if err := rows.Scan(&u.itemID, &playlistID, &u.artist, &u.title, &u.path); err != nil {
			return 0, err
		}
		pending = append(pending, u)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	matched := 0
	for _, u := range pending {
		id, ok, err := s.matchUnresolvedTrack(u.artist, u.title, u.path)
		if err != nil || !ok {
			continue
		}
		if _, err := s.DB.Exec(`UPDATE playlist_tracks SET track_id = ? WHERE playlist_tracks.profile_id=:musik_profile AND ( item_id = ?) `, id, u.itemID); err != nil {
			return matched, err
		}
		matched++
	}
	return matched, nil
}

func (s *Store) matchUnresolvedTrack(artist, title, path string) (int64, bool, error) {
	artist = strings.TrimSpace(artist)
	title = strings.TrimSpace(title)
	path = strings.TrimSpace(path)
	if artist != "" && title != "" {
		var id int64
		err := s.DB.QueryRow(`
SELECT id FROM tracks
WHERE is_active = 1 AND lower(trim(artist)) = lower(?) AND lower(trim(title)) = lower(?)
ORDER BY id LIMIT 1`, artist, title).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if err != sql.ErrNoRows {
			return 0, false, err
		}
	}
	if path != "" {
		base := path
		if i := strings.LastIndexAny(path, `/\`); i >= 0 {
			base = path[i+1:]
		}
		var id int64
		err := s.DB.QueryRow(`
SELECT id FROM tracks
WHERE is_active = 1 AND (path = ? OR path LIKE ?)
ORDER BY id LIMIT 1`, path, "%"+base).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if err != sql.ErrNoRows {
			return 0, false, err
		}
	}
	return 0, false, nil
}

type playlistScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanUserPlaylist(row playlistScanner) (*UserPlaylist, error) {
	var pl UserPlaylist
	var dup int
	if err := row.Scan(&pl.ID, &pl.Kind, &pl.Type, &pl.Name, &pl.Description, &pl.CreatedAt,
		&pl.UpdatedAt, &pl.CoverTrackID, &pl.CoverArtwork, &pl.SortMode, &pl.ArchivedAt,
		&pl.RuleSchema, &pl.RuleJSON, &dup); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	pl.AllowDuplicates = dup == 1
	if pl.Kind == "user" && (pl.Type == "" || pl.Type == "generated") {
		pl.Type = "manual"
	}
	return &pl, nil
}

func compactPlaylistPositions(tx *Tx, playlistID int64) error {
	rows, err := tx.Query(`SELECT item_id FROM playlist_tracks WHERE playlist_id = ? AND profile_id=:musik_profile ORDER BY position`, playlistID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE playlist_tracks SET position = ? WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND item_id = ?) `,
			-(i + 1), playlistID, id); err != nil {
			return err
		}
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE playlist_tracks SET position = ? WHERE playlist_tracks.profile_id=:musik_profile AND ( playlist_id = ? AND item_id = ?) `,
			i, playlistID, id); err != nil {
			return err
		}
	}
	return nil
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
