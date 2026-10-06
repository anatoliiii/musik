package db

import (
	"database/sql"
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

func (s *Store) LoadEntityVectors(model string) ([]EntityVector, error) {
	if model == "" {
		model = EntityModelVersion
	}
	rows, err := s.DB.Query(`
SELECT entity_type, entity_key, embedding, embedding_dim, model_version, track_count, computed_at
FROM entity_vectors WHERE model_version = ?`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityVector
	for rows.Next() {
		var row EntityVector
		if err := rows.Scan(&row.Type, &row.Key, &row.Embedding, &row.Dim, &row.Model, &row.TrackCount, &row.ComputedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) EntityVector(entityType, key, model string) (*EntityVector, error) {
	if model == "" {
		model = EntityModelVersion
	}
	var row EntityVector
	err := s.DB.QueryRow(`
SELECT entity_type, entity_key, embedding, embedding_dim, model_version, track_count, computed_at
FROM entity_vectors WHERE entity_type = ? AND entity_key = ? AND model_version = ?`,
		entityType, key, model).Scan(
		&row.Type, &row.Key, &row.Embedding, &row.Dim, &row.Model, &row.TrackCount, &row.ComputedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
