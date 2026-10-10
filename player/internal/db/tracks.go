package db

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// parseSegments decodes the JSON array stored in tracks.artist_segments.
// A malformed value yields nil; callers fall back to the raw artist label.
func parseSegments(raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	clean := out[:0]
	for _, name := range out {
		name = strings.TrimSpace(name)
		if name != "" {
			clean = append(clean, name)
		}
	}
	return append([]string(nil), clean...)
}

// VariousArtists credits a compilation whose tags set only the flag.
const VariousArtists = "Various Artists"

// albumArtistSQL is the album-level credit of t: the album artist tag, or
// VariousArtists for a flagged compilation, else ”. The Python worker groups
// albums by the same rule (musik.db.store.ALBUM_OWNER_SQL).
const albumArtistSQL = `COALESCE(NULLIF(trim(t.album_artist), ''), CASE WHEN t.is_compilation = 1 THEN '` + VariousArtists + `' END, '')`

const readyTrackWhere = `
FROM tracks t
JOIN features f ON f.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
  AND f.status = 'ready' AND f.embedding IS NOT NULL`

// LoadCatalogRows reads the profile-independent fields of every ready track,
// without embeddings; LoadEmbeddings fetches the vectors that changed.
func (s *Store) LoadCatalogRows() ([]TrackRow, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, tr *TrackRow) error {
		var bpm, lufs sql.NullFloat64
		var segments string
		if err := rows.Scan(&tr.ID, &tr.Path, &tr.Title, &tr.Artist, &segments, &tr.AlbumArtist, &tr.Album,
			&tr.Duration, &tr.FileMD5, &tr.CreatedAt, &tr.ArtworkPath, &tr.ClusterID,
			&tr.Dim, &tr.EmbeddingStamp, &tr.Year, &bpm, &lufs, &tr.KeyName); err != nil {
			return err
		}
		tr.ArtistSegments = parseSegments(segments)
		if bpm.Valid {
			tr.BPM, tr.HasBPM = bpm.Float64, true
		}
		if lufs.Valid {
			tr.LUFS, tr.HasLUFS = lufs.Float64, true
		}
		return nil
	}, `
SELECT t.id, t.path, COALESCE(t.title,''), COALESCE(t.artist,''), COALESCE(t.artist_segments,''),
       `+albumArtistSQL+` AS album_artist, COALESCE(t.album,''),
       COALESCE(t.duration,0), COALESCE(t.file_md5,''), COALESCE(t.created_at,''),
       COALESCE(t.artwork_path,''), COALESCE(f.cluster_id, -1),
       COALESCE(f.embedding_dim,0), COALESCE(f.computed_at,''),
       COALESCE(t.year, 0), f.bpm, COALESCE(f.lufs, t.lufs), COALESCE(f.key_name,'')`+readyTrackWhere+`
ORDER BY t.id`)
}

// LoadEmbeddings returns the stored embeddings of ids, read in batches.
func (s *Store) LoadEmbeddings(ids []int64) (map[int64][]byte, error) {
	out := make(map[int64][]byte, len(ids))
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := min(start+batch, len(ids))
		args := make([]any, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		rows, err := s.DB.Query(`SELECT track_id, embedding FROM features WHERE embedding IS NOT NULL AND track_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = blob
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TrackStats are one profile's counters for a track.
type TrackStats struct {
	Shown        int
	SkipEarly    int
	Completed    int
	Plays        int
	Finishes     int
	EarlySkips   int
	LastPlayedAt string
}

// LoadTrackStats reads this profile's recommendation and listening counters.
func (s *Store) LoadTrackStats() (map[int64]TrackStats, error) {
	out := map[int64]TrackStats{}
	rows, err := s.DB.Query(`SELECT track_id, shown, skipped_early, completed FROM rec_stats WHERE profile_id = ?`, s.ProfileID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var st TrackStats
		if err := rows.Scan(&id, &st.Shown, &st.SkipEarly, &st.Completed); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = st
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(`SELECT track_id, plays, finishes, early_skips, COALESCE(last_played_at,'') FROM track_stats WHERE profile_id = ?`, s.ProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var plays, finishes, early int
		var last string
		if err := rows.Scan(&id, &plays, &finishes, &early, &last); err != nil {
			return nil, err
		}
		st := out[id]
		st.Plays, st.Finishes, st.EarlySkips, st.LastPlayedAt = plays, finishes, early, last
		out[id] = st
	}
	return out, rows.Err()
}

type CatalogTrack struct {
	ID          int64
	Path        string
	Title       string
	Artist      string
	Artists     []string
	AlbumArtist string
	Album       string
	Duration    float64
	Artwork     string
	Cluster     int
	Status      string
}

func (s *Store) ListCatalogTracks() ([]CatalogTrack, error) {
	return s.ListCatalogTracksLimit(0)
}

func (s *Store) ListCatalogTracksLimit(limit int) ([]CatalogTrack, error) {
	q := `
SELECT t.id, t.path, COALESCE(t.title,''), COALESCE(t.artist,''), COALESCE(t.artist_segments,''),
       ` + albumArtistSQL + ` AS album_artist, COALESCE(t.album,''), COALESCE(t.duration,0), COALESCE(t.artwork_path,''),
       COALESCE(f.cluster_id, -1), COALESCE(f.status, 'pending')
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
ORDER BY t.id`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return QueryAll(s.DB, func(rows *sql.Rows, tr *CatalogTrack) error {
		var segments string
		if err := rows.Scan(&tr.ID, &tr.Path, &tr.Title, &tr.Artist, &segments, &tr.AlbumArtist, &tr.Album,
			&tr.Duration, &tr.Artwork, &tr.Cluster, &tr.Status); err != nil {
			return err
		}
		tr.Artists = parseSegments(segments)
		return nil
	}, q, args...)
}

func (s *Store) TrackPath(id int64) (string, error) {
	var path string
	err := s.DB.QueryRow(`SELECT path FROM tracks WHERE id = ? AND is_active = 1`, id).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}
