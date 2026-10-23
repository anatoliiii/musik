package db

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
)

// TrackPassport is the card of one track: the file's measured audio, and this
// profile's listening counters. Rating is the existing like or dislike.
type TrackPassport struct {
	Format       string   `json:"format,omitempty"`
	Bitrate      int64    `json:"bitrate,omitempty"`
	SampleRate   int64    `json:"sample_rate,omitempty"`
	Channels     int64    `json:"channels,omitempty"`
	LUFS         *float64 `json:"lufs,omitempty"`
	BPM          *float64 `json:"bpm,omitempty"`
	Key          string   `json:"key,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Year         int      `json:"year,omitempty"`
	Duration     float64  `json:"duration,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	Plays        int      `json:"plays"`
	Finishes     int      `json:"finishes"`
	EarlySkips   int      `json:"early_skips"`
	LastPlayedAt string   `json:"last_played_at,omitempty"`
	Rating       string   `json:"rating,omitempty"`
}

// TrackPassport reads one track. Stats and the like/dislike come from this
// profile in the same query, not from a scan of every track_stats row.
// It returns nil when the track does not exist.
func (s *Store) TrackPassport(id int64) (*TrackPassport, error) {
	var (
		path                         string
		bitrate, rate, chans         sql.NullInt64
		lufs, bpm, duration          sql.NullFloat64
		keyName, mode, last, rating  sql.NullString
		genres                       sql.NullString
		year, plays, finishes, early int
	)
	err := s.DB.QueryRow(`
SELECT t.path, t.bitrate, t.sample_rate, t.channels,
       COALESCE(f.lufs, t.lufs), f.bpm, f.key_name, f.mode,
       COALESCE(t.year, 0), COALESCE(t.duration, 0),
       COALESCE(ts.plays, 0), COALESCE(ts.finishes, 0), COALESCE(ts.early_skips, 0),
       COALESCE(ts.last_played_at, ''),
       COALESCE(pref.rating, ''),
       COALESCE(`+s.Dialect.GenreNames("t.id")+`, '')
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
LEFT JOIN track_stats ts ON ts.track_id = t.id AND ts.profile_id = ?
LEFT JOIN track_preferences pref ON pref.track_id = t.id AND pref.profile_id = ?
WHERE t.id = ?`, s.ProfileID, s.ProfileID, id).Scan(
		&path, &bitrate, &rate, &chans, &lufs, &bpm, &keyName, &mode,
		&year, &duration, &plays, &finishes, &early, &last, &rating, &genres,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &TrackPassport{
		Format:       strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), ".")),
		Bitrate:      bitrate.Int64,
		SampleRate:   rate.Int64,
		Channels:     chans.Int64,
		Key:          keyName.String,
		Mode:         mode.String,
		Year:         year,
		Plays:        plays,
		Finishes:     finishes,
		EarlySkips:   early,
		LastPlayedAt: last.String,
	}
	if duration.Valid && duration.Float64 > 0 {
		p.Duration = duration.Float64
	}
	if lufs.Valid {
		p.LUFS = &lufs.Float64
	}
	if bpm.Valid && bpm.Float64 > 0 {
		p.BPM = &bpm.Float64
	}
	switch rating.String {
	case "like", "dislike":
		p.Rating = rating.String
	}
	if names := strings.TrimSpace(genres.String); names != "" {
		parts := strings.Split(names, " · ")
		sort.Strings(parts)
		p.Genres = parts
	}
	return p, nil
}
