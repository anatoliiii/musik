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
  (SELECT COUNT(*) FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) pt WHERE pt.playlist_id = p.id),
  COALESCE((SELECT pt.track_id FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) pt WHERE pt.playlist_id = p.id ORDER BY pt.position ASC LIMIT 1), 0)
FROM (SELECT * FROM playlists WHERE profile_id=:musik_profile) p
WHERE p.kind = ?
ORDER BY p.id DESC LIMIT 1`, kind).Scan(&id, &name, &createdAt, &n, &coverID)
	if err == sql.ErrNoRows {
		return 0, "", 0, 0, "", nil
	}
	return
}

func (s *Store) LaterList() ([]PlaylistTrack, error) {
	rows, err := s.DB.Query(`
SELECT l.position, l.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), ''
FROM (SELECT * FROM listen_later WHERE profile_id=:musik_profile) l
JOIN tracks t ON t.id = l.track_id
ORDER BY l.position ASC, l.added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistTrack
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) LaterAdd(trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM (SELECT * FROM listen_later WHERE profile_id=:musik_profile) AS listen_later`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO listen_later(track_id, added_at, position,profile_id) VALUES (?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET added_at=excluded.added_at`,
		trackID, now, pos+1)
	return err
}

func (s *Store) LaterRemove(trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM listen_later WHERE listen_later.profile_id=:musik_profile AND ( track_id = ?) `, trackID)
	return err
}

func (s *Store) LaterCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT * FROM listen_later WHERE profile_id=:musik_profile) AS listen_later`).Scan(&n)
	return n
}

func (s *Store) FavoritesList() ([]PlaylistTrack, error) {
	rows, err := s.DB.Query(`
SELECT f.position, f.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), ''
FROM (SELECT * FROM favorites WHERE profile_id=:musik_profile) f
JOIN tracks t ON t.id = f.track_id
ORDER BY f.position ASC, f.added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistTrack
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) FavoritesAdd(trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM (SELECT * FROM favorites WHERE profile_id=:musik_profile) AS favorites`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorites(track_id, added_at, position,profile_id) VALUES (?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET added_at=excluded.added_at`,
		trackID, now, pos+1)
	return err
}

func (s *Store) FavoritesRemove(trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM favorites WHERE favorites.profile_id=:musik_profile AND ( track_id = ?) `, trackID)
	return err
}

func (s *Store) FavoritesCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT * FROM favorites WHERE profile_id=:musik_profile) AS favorites`).Scan(&n)
	return n
}

func (s *Store) FavoritesHas(trackID int64) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM (SELECT * FROM favorites WHERE profile_id=:musik_profile) AS favorites WHERE track_id = ?`, trackID).Scan(&n)
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
	rows, err := s.DB.Query(`SELECT artist, position, added_at FROM (SELECT * FROM favorite_artists WHERE profile_id=:musik_profile) AS favorite_artists ORDER BY position ASC, added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavArtist
	for rows.Next() {
		var a FavArtist
		if err := rows.Scan(&a.Artist, &a.Position, &a.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) FavArtistAdd(artist string) error {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return fmt.Errorf("artist required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM (SELECT * FROM favorite_artists WHERE profile_id=:musik_profile) AS favorite_artists`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorite_artists(artist, added_at, position,profile_id) VALUES (?,?,?,:musik_profile)
ON CONFLICT(profile_id,artist) DO UPDATE SET added_at=excluded.added_at`, artist, now, pos+1)
	return err
}

func (s *Store) FavArtistRemove(artist string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_artists WHERE favorite_artists.profile_id=:musik_profile AND ( artist = ?) `, strings.TrimSpace(artist))
	return err
}

func (s *Store) FavArtistHas(artist string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM (SELECT * FROM favorite_artists WHERE profile_id=:musik_profile) AS favorite_artists WHERE artist = ?`, strings.TrimSpace(artist)).Scan(&n)
	return n == 1
}

func (s *Store) FavArtistCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT * FROM favorite_artists WHERE profile_id=:musik_profile) AS favorite_artists`).Scan(&n)
	return n
}

func (s *Store) FavAlbumsList() ([]FavAlbum, error) {
	rows, err := s.DB.Query(`SELECT artist, album, position, added_at FROM (SELECT * FROM favorite_albums WHERE profile_id=:musik_profile) AS favorite_albums ORDER BY position ASC, added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavAlbum
	for rows.Next() {
		var a FavAlbum
		if err := rows.Scan(&a.Artist, &a.Album, &a.Position, &a.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) FavAlbumAdd(artist, album string) error {
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)
	if album == "" {
		return fmt.Errorf("album required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM (SELECT * FROM favorite_albums WHERE profile_id=:musik_profile) AS favorite_albums`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorite_albums(artist, album, added_at, position,profile_id) VALUES (?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,artist, album) DO UPDATE SET added_at=excluded.added_at`,
		artist, album, now, pos+1)
	return err
}

func (s *Store) FavAlbumRemove(artist, album string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_albums WHERE favorite_albums.profile_id=:musik_profile AND ( artist = ? AND album = ?) `,
		strings.TrimSpace(artist), strings.TrimSpace(album))
	return err
}

func (s *Store) FavAlbumHas(artist, album string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM (SELECT * FROM favorite_albums WHERE profile_id=:musik_profile) AS favorite_albums WHERE artist = ? AND album = ?`,
		strings.TrimSpace(artist), strings.TrimSpace(album)).Scan(&n)
	return n == 1
}

func (s *Store) FavAlbumCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT * FROM favorite_albums WHERE profile_id=:musik_profile) AS favorite_albums`).Scan(&n)
	return n
}

func (s *Store) LatestPlaylist(kind string) (*Playlist, error) {
	var pl Playlist
	err := s.DB.QueryRow(`
SELECT id, kind, name, created_at FROM (SELECT * FROM playlists WHERE profile_id=:musik_profile) AS playlists
WHERE kind = ? ORDER BY id DESC LIMIT 1`, kind).Scan(&pl.ID, &pl.Kind, &pl.Name, &pl.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`
SELECT pt.position, pt.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), COALESCE(pt.explanation,'')
FROM (SELECT * FROM playlist_tracks WHERE profile_id=:musik_profile) pt
JOIN tracks t ON t.id = pt.track_id
WHERE pt.playlist_id = ?
ORDER BY pt.position`, pl.ID)
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
FROM (SELECT * FROM discover_tips WHERE profile_id=:musik_profile) AS discover_tips`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY score DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoverTip
	for rows.Next() {
		var t DiscoverTip
		var idsJSON string
		if err := rows.Scan(&t.ID, &t.Kind, &t.Artist, &t.Album, &t.Score, &idsJSON, &t.Explanation, &t.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(idsJSON), &t.TrackIDs)
		out = append(out, t)
	}
	return out, rows.Err()
}
