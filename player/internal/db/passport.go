package db

import (
	"database/sql"
	"path/filepath"
	"strings"
)

// TrackPassport is the technical card of a track shown on the now-playing
// screen: container, encoding and the audio features the scanner measured.
type TrackPassport struct {
	Format     string   `json:"format,omitempty"`
	Bitrate    int64    `json:"bitrate,omitempty"`
	SampleRate int64    `json:"sample_rate,omitempty"`
	Channels   int64    `json:"channels,omitempty"`
	LUFS       *float64 `json:"lufs,omitempty"`
	BPM        *float64 `json:"bpm,omitempty"`
	Key        string   `json:"key,omitempty"`
	Mode       string   `json:"mode,omitempty"`
}

// TrackPassport returns nil when the track does not exist.
func (s *Store) TrackPassport(id int64) (*TrackPassport, error) {
	var (
		path                 string
		bitrate, rate, chans sql.NullInt64
		lufs, bpm            sql.NullFloat64
		keyName, mode        sql.NullString
	)
	err := s.DB.QueryRow(`
SELECT t.path, t.bitrate, t.sample_rate, t.channels, COALESCE(f.lufs, t.lufs), f.bpm, f.key_name, f.mode
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
WHERE t.id = ?`, id).Scan(&path, &bitrate, &rate, &chans, &lufs, &bpm, &keyName, &mode)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &TrackPassport{
		Format:     strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), ".")),
		Bitrate:    bitrate.Int64,
		SampleRate: rate.Int64,
		Channels:   chans.Int64,
		Key:        keyName.String,
		Mode:       mode.String,
	}
	if lufs.Valid {
		p.LUFS = &lufs.Float64
	}
	if bpm.Valid && bpm.Float64 > 0 {
		p.BPM = &bpm.Float64
	}
	return p, nil
}
