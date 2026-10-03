package db

import (
	"database/sql"
	"time"
)

func (s *Store) EnqueueJob(kind, payloadJSON string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var payload *string
	if payloadJSON != "" {
		payload = &payloadJSON
	}
	job := JobRecord{Kind: kind, Status: "pending", PayloadJSON: payload, CreatedAt: now, UpdatedAt: now}
	if err := s.ORM.Create(&job).Error; err != nil {
		return 0, err
	}
	return job.ID, nil
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

// ListDoneJobsAfter returns jobs completed after the given RFC3339/Nano timestamp
// (exclusive). Used by the player to auto-reload after worker finishes.
func (s *Store) ListDoneJobsAfter(after string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 20
	}
	rows, err := s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs
WHERE status = 'done' AND updated_at > ?
ORDER BY updated_at ASC
LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Status, &j.Payload, &j.Result, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ListJobs(status string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs WHERE status = ? ORDER BY id DESC LIMIT ?`, status, limit)
	} else {
		rows, err = s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Status, &j.Payload, &j.Result, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetJob(id int64) (*Job, error) {
	var j Job
	var payload, result, errStr sql.NullString
	err := s.DB.QueryRow(`
SELECT id, kind, status, payload_json, result_json, error, created_at, updated_at
FROM jobs WHERE id = ?`, id).Scan(&j.ID, &j.Kind, &j.Status, &payload, &result, &errStr, &j.CreatedAt, &j.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = payload.String
	j.Result = result.String
	j.Error = errStr.String
	return &j, nil
}
