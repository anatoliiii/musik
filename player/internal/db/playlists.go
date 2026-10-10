package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type PlaylistTrack struct {
	Position    int     `json:"position"`
	TrackID     int64   `json:"track_id"`
	Artist      string  `json:"artist"`
	Title       string  `json:"title"`
	Duration    float64 `json:"duration"`
	Explanation string  `json:"explanation"`
}

type Playlist struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	CreatedAt string          `json:"created_at"`
	Tracks    []PlaylistTrack `json:"tracks"`
}

func (s *Store) PlaylistMeta(kind string) (id int64, name string, n int, coverID int64, createdAt string, err error) {
	err = s.DB.QueryRow(`
SELECT p.id, p.name, p.created_at,
  (SELECT COUNT(*) FROM playlist_tracks pt WHERE pt.profile_id = p.profile_id AND pt.playlist_id = p.id),
  COALESCE((SELECT pt.track_id FROM playlist_tracks pt WHERE pt.profile_id = p.profile_id AND pt.playlist_id = p.id ORDER BY pt.position ASC LIMIT 1), 0)
FROM playlists p
WHERE p.profile_id = ? AND p.kind = ?
ORDER BY p.id DESC LIMIT 1`, s.ProfileID, kind).Scan(&id, &name, &createdAt, &n, &coverID)
	if err == sql.ErrNoRows {
		return 0, "", 0, 0, "", nil
	}
	return
}

// listen_later and favorites are ordered track lists of the same shape.
func (s *Store) trackList(table string) ([]PlaylistTrack, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, t *PlaylistTrack) error {
		return rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration)
	}, `
SELECT l.position, l.track_id, COALESCE(t.artist,''), COALESCE(t.title,''), COALESCE(t.duration,0)
FROM `+table+` l
JOIN tracks t ON t.id = l.track_id
WHERE l.profile_id = ?
ORDER BY l.position ASC, l.added_at DESC`, s.ProfileID)
}

func (s *Store) trackListAdd(table string, trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO `+table+`(track_id, added_at, position, profile_id)
SELECT ?, ?, COALESCE(MAX(position),0)+1, ? FROM `+table+` WHERE profile_id = ?
ON CONFLICT(profile_id,track_id) DO UPDATE SET added_at=excluded.added_at`,
		trackID, now, s.ProfileID, s.ProfileID)
	return err
}

func (s *Store) trackListRemove(table string, trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM `+table+` WHERE profile_id = ? AND track_id = ?`, s.ProfileID, trackID)
	return err
}

func (s *Store) trackListCount(table string) int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE profile_id = ?`, s.ProfileID).Scan(&n)
	return n
}

func (s *Store) LaterList() ([]PlaylistTrack, error)     { return s.trackList("listen_later") }
func (s *Store) LaterAdd(trackID int64) error            { return s.trackListAdd("listen_later", trackID) }
func (s *Store) LaterRemove(trackID int64) error         { return s.trackListRemove("listen_later", trackID) }
func (s *Store) LaterCount() int                         { return s.trackListCount("listen_later") }
func (s *Store) FavoritesList() ([]PlaylistTrack, error) { return s.trackList("favorites") }
func (s *Store) FavoritesAdd(trackID int64) error        { return s.trackListAdd("favorites", trackID) }
func (s *Store) FavoritesRemove(trackID int64) error     { return s.trackListRemove("favorites", trackID) }
func (s *Store) FavoritesCount() int                     { return s.trackListCount("favorites") }

func (s *Store) FavoritesHas(trackID int64) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorites WHERE profile_id = ? AND track_id = ?`, s.ProfileID, trackID).Scan(&n)
	return n == 1
}

type FavArtist struct {
	Artist   string `json:"artist"`
	Position int    `json:"position"`
	AddedAt  string `json:"added_at"`
}

type FavAlbum struct {
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Position int    `json:"position"`
	AddedAt  string `json:"added_at"`
}

func (s *Store) FavArtistsList() ([]FavArtist, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, a *FavArtist) error {
		return rows.Scan(&a.Artist, &a.Position, &a.AddedAt)
	}, `SELECT artist, position, added_at FROM favorite_artists WHERE profile_id = ? ORDER BY position ASC, added_at DESC`, s.ProfileID)
}

func (s *Store) FavArtistAdd(artist string) error {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return fmt.Errorf("artist required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO favorite_artists(artist, added_at, position, profile_id)
SELECT ?, ?, COALESCE(MAX(position),0)+1, ? FROM favorite_artists WHERE profile_id = ?
ON CONFLICT(profile_id,artist) DO UPDATE SET added_at=excluded.added_at`, artist, now, s.ProfileID, s.ProfileID)
	return err
}

func (s *Store) FavArtistRemove(artist string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_artists WHERE profile_id = ? AND artist = ?`, s.ProfileID, strings.TrimSpace(artist))
	return err
}

func (s *Store) FavArtistHas(artist string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorite_artists WHERE profile_id = ? AND artist = ?`, s.ProfileID, strings.TrimSpace(artist)).Scan(&n)
	return n == 1
}

func (s *Store) FavArtistCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM favorite_artists WHERE profile_id = ?`, s.ProfileID).Scan(&n)
	return n
}

func (s *Store) FavAlbumsList() ([]FavAlbum, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, a *FavAlbum) error {
		return rows.Scan(&a.Artist, &a.Album, &a.Position, &a.AddedAt)
	}, `SELECT artist, album, position, added_at FROM favorite_albums WHERE profile_id = ? ORDER BY position ASC, added_at DESC`, s.ProfileID)
}

func (s *Store) FavAlbumAdd(artist, album string) error {
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)
	if album == "" {
		return fmt.Errorf("album required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO favorite_albums(artist, album, added_at, position, profile_id)
SELECT ?, ?, ?, COALESCE(MAX(position),0)+1, ? FROM favorite_albums WHERE profile_id = ?
ON CONFLICT(profile_id,artist, album) DO UPDATE SET added_at=excluded.added_at`,
		artist, album, now, s.ProfileID, s.ProfileID)
	return err
}

func (s *Store) FavAlbumRemove(artist, album string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_albums WHERE profile_id = ? AND artist = ? AND album = ?`,
		s.ProfileID, strings.TrimSpace(artist), strings.TrimSpace(album))
	return err
}

func (s *Store) FavAlbumHas(artist, album string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorite_albums WHERE profile_id = ? AND artist = ? AND album = ?`,
		s.ProfileID, strings.TrimSpace(artist), strings.TrimSpace(album)).Scan(&n)
	return n == 1
}

func (s *Store) FavAlbumCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM favorite_albums WHERE profile_id = ?`, s.ProfileID).Scan(&n)
	return n
}

func (s *Store) LatestPlaylist(kind string) (*Playlist, error) {
	var pl Playlist
	err := s.DB.QueryRow(`
SELECT id, kind, name, created_at FROM playlists
WHERE profile_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, s.ProfileID, kind).Scan(&pl.ID, &pl.Kind, &pl.Name, &pl.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`
SELECT pt.position, pt.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), COALESCE(pt.explanation,'')
FROM playlist_tracks pt
JOIN tracks t ON t.id = pt.track_id
WHERE pt.profile_id = ? AND pt.playlist_id = ?
ORDER BY pt.position`, s.ProfileID, pl.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		pl.Tracks = append(pl.Tracks, t)
	}
	return &pl, rows.Err()
}

type DiscoverTip struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	Score       float64 `json:"score"`
	TrackIDs    []int64 `json:"track_ids"`
	Explanation string  `json:"explanation"`
	CreatedAt   string  `json:"created_at"`
}

func (s *Store) ListDiscoverTips(kind string, limit int) ([]DiscoverTip, error) {
	if limit < 1 {
		limit = 20
	}
	q := `
SELECT id, kind, COALESCE(artist,''), COALESCE(album,''), score,
       track_ids_json, COALESCE(explanation,''), created_at
FROM discover_tips WHERE profile_id = ?`
	args := []any{s.ProfileID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY score DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return QueryAll(s.DB, func(rows *sql.Rows, t *DiscoverTip) error {
		var idsJSON string
		if err := rows.Scan(&t.ID, &t.Kind, &t.Artist, &t.Album, &t.Score, &idsJSON, &t.Explanation, &t.CreatedAt); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(idsJSON), &t.TrackIDs)
		return nil
	}, q, args...)
}
