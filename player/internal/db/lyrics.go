package db

import (
	"database/sql"
)

// Lyrics is plain/synced text for a track (filled by Python `musik lyrics`).
type Lyrics struct {
	TrackID      int64  `json:"track_id"`
	PlainLyrics  string `json:"plain_lyrics"`
	SyncedLyrics string `json:"synced_lyrics"`
	Source       string `json:"source"`
	SourceID     string `json:"source_id"`
	Instrumental bool   `json:"instrumental"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Store) GetLyrics(trackID int64) (*Lyrics, bool, error) {
	row := s.DB.QueryRow(`
SELECT track_id, plain_lyrics, synced_lyrics, source, source_id,
       instrumental, status, COALESCE(error,''), updated_at
FROM lyrics WHERE track_id = ?`, trackID)
	var ly Lyrics
	var instr int
	err := row.Scan(
		&ly.TrackID, &ly.PlainLyrics, &ly.SyncedLyrics, &ly.Source, &ly.SourceID,
		&instr, &ly.Status, &ly.Error, &ly.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	ly.Instrumental = instr != 0
	return &ly, true, nil
}
