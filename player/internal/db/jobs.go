package db

import (
	"errors"
	"time"

	"gorm.io/gorm"
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

// ListDoneJobsAfter returns jobs completed after the given RFC3339/Nano timestamp
// (exclusive). Used by the player to auto-reload after worker finishes.
func (s *Store) ListDoneJobsAfter(after string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 20
	}
	var rows []JobRecord
	if err := s.ORM.Where("status = ? AND updated_at > ?", "done", after).
		Order("updated_at ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapJob(row))
	}
	return out, nil
}

func (s *Store) ListJobs(status string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	query := s.ORM
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var rows []JobRecord
	if err := query.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapJob(row))
	}
	return out, nil
}

func (s *Store) GetJob(id int64) (*Job, error) {
	var row JobRecord
	err := s.ORM.Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job := mapJob(row)
	return &job, nil
}
