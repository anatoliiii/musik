package db

import (
	"database/sql"
	"encoding/json"
)

type RecommendationPolicyRow struct {
	RequestID      string         `json:"request_id"`
	Reason         string         `json:"reason"`
	PolicyVersion  string         `json:"policy_version"`
	ModelVersion   string         `json:"model_version,omitempty"`
	CandidateCount int            `json:"candidate_count"`
	LatencyMS      float64        `json:"latency_ms"`
	CreatedAt      string         `json:"created_at"`
	Policy         map[string]any `json:"policy,omitempty"`
}

type TrainingRunRow struct {
	RunID         string         `json:"run_id"`
	ModelVersion  string         `json:"model_version,omitempty"`
	Status        string         `json:"status"`
	PositiveCount int            `json:"positive_count"`
	NegativeCount int            `json:"negative_count"`
	CreatedAt     string         `json:"created_at"`
	Metrics       map[string]any `json:"metrics,omitempty"`
}

func (s *Store) LatestRecommendationPolicies(limit int) ([]RecommendationPolicyRow, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT request_id, reason, policy_version, COALESCE(model_version,''),
       candidate_count, latency_ms, created_at, COALESCE(policy_json,'')
FROM (SELECT * FROM recommendation_requests WHERE profile_id=:musik_profile) AS recommendation_requests
ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecommendationPolicyRow
	for rows.Next() {
		var row RecommendationPolicyRow
		var raw string
		if err := rows.Scan(
			&row.RequestID, &row.Reason, &row.PolicyVersion, &row.ModelVersion,
			&row.CandidateCount, &row.LatencyMS, &row.CreatedAt, &raw,
		); err != nil {
			return nil, err
		}
		if raw != "" {
			var policy map[string]any
			if json.Unmarshal([]byte(raw), &policy) == nil {
				row.Policy = policy
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) ListTrainingRuns(limit int) ([]TrainingRunRow, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT run_id, COALESCE(model_version,''), status, positive_count, negative_count,
       created_at, metrics_json
FROM training_runs
WHERE profile_id=:musik_profile
ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []TrainingRunRow
	for rows.Next() {
		var row TrainingRunRow
		var raw string
		if err := rows.Scan(
			&row.RunID, &row.ModelVersion, &row.Status, &row.PositiveCount,
			&row.NegativeCount, &row.CreatedAt, &raw,
		); err != nil {
			return nil, err
		}
		if raw != "" {
			var metrics map[string]any
			if json.Unmarshal([]byte(raw), &metrics) == nil {
				row.Metrics = metrics
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
