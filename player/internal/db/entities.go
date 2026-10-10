package db

import (
	"time"
)

const EntityModelVersion = "clap-default"

type EntityVector struct {
	Type       string
	Key        string
	Embedding  []byte
	Dim        int
	Model      string
	TrackCount int
	ComputedAt string
}

func (s *Store) ReplaceEntityVectors(model string, rows []EntityVector) error {
	if model == "" {
		model = EntityModelVersion
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entity_vectors WHERE model_version = ?`, model); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range rows {
		if row.ComputedAt == "" {
			row.ComputedAt = now
		}
		if row.Model == "" {
			row.Model = model
		}
		if _, err := tx.Exec(`
INSERT INTO entity_vectors(entity_type, entity_key, embedding, embedding_dim, model_version, track_count, computed_at)
VALUES (?,?,?,?,?,?,?)`,
			row.Type, row.Key, row.Embedding, row.Dim, row.Model, row.TrackCount, row.ComputedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
