package db

import (
	"math"
	"time"
)

const TransitionHalfLifeDays = 90

type TransitionStat struct {
	FromID        int64   `json:"from_id"`
	ToID          int64   `json:"to_id"`
	ManualCount   int     `json:"manual_count"`
	RadioCount    int     `json:"radio_count"`
	FinishedCount int     `json:"finished_count"`
	PartialCount  int     `json:"partial_count"`
	SkipCount     int     `json:"skip_count"`
	DecayedWeight float64 `json:"decayed_weight"`
	UpdatedAt     string  `json:"updated_at"`
}

func OutcomeTransitionDelta(outcome string, manual bool) float64 {
	var delta float64
	switch outcome {
	case "finished":
		delta = 1
	case "partial":
		delta = 0.3
	case "early_skip":
		delta = -0.1
	default:
		return 0
	}
	if manual {
		delta *= 1.5
	}
	return delta
}

func DecayTransitionWeight(weight float64, updatedAt string, now time.Time) float64 {
	if weight == 0 || updatedAt == "" {
		return weight
	}
	at, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		at, err = time.Parse(time.RFC3339, updatedAt)
	}
	if err != nil {
		return weight
	}
	days := now.Sub(at).Hours() / 24
	if days <= 0 {
		return weight
	}
	return weight * math.Pow(0.5, days/TransitionHalfLifeDays)
}

func (s *Store) RecordOutcomeTransition(fromID, toID int64, outcome, provenance string) error {
	if fromID == 0 || toID == 0 || fromID == toID {
		return nil
	}
	delta := OutcomeTransitionDelta(outcome, provenance == "manual")
	if delta == 0 && outcome != "early_skip" {
		return nil
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	manual, radio, finished, partial, skip := 0, 0, 0, 0, 0
	if provenance == "manual" {
		manual = 1
	} else {
		radio = 1
	}
	switch outcome {
	case "finished":
		finished = 1
	case "partial":
		partial = 1
	case "early_skip":
		skip = 1
	}
	var prevWeight float64
	var prevUpdated string
	_ = s.DB.QueryRow(
		`SELECT decayed_weight, updated_at FROM transition_stats WHERE profile_id = ? AND from_id = ? AND to_id = ?`,
		s.ProfileID, fromID, toID,
	).Scan(&prevWeight, &prevUpdated)
	decayed := DecayTransitionWeight(prevWeight, prevUpdated, now) + delta
	_, err := s.DB.Exec(`
INSERT INTO transition_stats(
  from_id, to_id, manual_count, radio_count, finished_count, partial_count,
  skip_count, decayed_weight, updated_at, profile_id
) VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(profile_id,from_id, to_id) DO UPDATE SET
  manual_count = transition_stats.manual_count + excluded.manual_count,
  radio_count = transition_stats.radio_count + excluded.radio_count,
  finished_count = transition_stats.finished_count + excluded.finished_count,
  partial_count = transition_stats.partial_count + excluded.partial_count,
  skip_count = transition_stats.skip_count + excluded.skip_count,
  decayed_weight = excluded.decayed_weight,
  updated_at = excluded.updated_at`,
		fromID, toID, manual, radio, finished, partial, skip, decayed, nowStr, s.ProfileID)
	return err
}

func (s *Store) LoadTransitionStatsFrom(fromID int64) (map[int64]float64, error) {
	rows, err := s.DB.Query(`
SELECT to_id, decayed_weight, updated_at FROM transition_stats WHERE profile_id = ? AND from_id = ?`, s.ProfileID, fromID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	out := map[int64]float64{}
	for rows.Next() {
		var toID int64
		var weight float64
		var updated string
		if err := rows.Scan(&toID, &weight, &updated); err != nil {
			return nil, err
		}
		w := DecayTransitionWeight(weight, updated, now)
		if w != 0 {
			out[toID] = w
		}
	}
	return out, rows.Err()
}
