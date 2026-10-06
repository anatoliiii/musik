package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) EnqueueJob(kind, payloadJSON string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var id int64
	err := s.DB.QueryRow(`
INSERT INTO jobs(kind, status, payload_json, created_at, updated_at)
VALUES (?, 'pending', ?, ?, ?)
RETURNING id`, kind, nullStr(payloadJSON), now, now).Scan(&id)
	return id, err
}

type Job struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Payload   string `json:"payload_json,omitempty"`
	Result    string `json:"result_json,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func mapJob(row JobRecord) Job {
	job := Job{ID: row.ID, Kind: row.Kind, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.PayloadJSON != nil {
		job.Payload = *row.PayloadJSON
	}
	if row.ResultJSON != nil {
		job.Result = *row.ResultJSON
	}
	if row.Error != nil {
		job.Error = *row.Error
	}
	return job
}

func mapJobs(rows []JobRecord) []Job {
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapJob(row))
	}
	return out
}

// ListDoneJobsAfter returns jobs completed after the given RFC3339/Nano timestamp
// (exclusive). Used by the player to auto-reload after worker finishes.
func (s *Store) ListDoneJobsAfter(after string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 20
	}
	var rows []JobRecord
	if err := s.DB.Select(context.Background(), &rows,
		`SELECT `+jobColumns+` FROM jobs WHERE status = 'done' AND updated_at > ? ORDER BY updated_at ASC LIMIT ?`,
		after, limit); err != nil {
		return nil, err
	}
	return mapJobs(rows), nil
}

func (s *Store) ListJobs(status string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	query := `SELECT ` + jobColumns + ` FROM jobs`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	var rows []JobRecord
	if err := s.DB.Select(context.Background(), &rows, query, args...); err != nil {
		return nil, err
	}
	return mapJobs(rows), nil
}

func (s *Store) GetJob(id int64) (*Job, error) {
	var row JobRecord
	err := s.DB.Get(context.Background(), &row, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job := mapJob(row)
	return &job, nil
}
