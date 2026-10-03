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
		`SELECT decayed_weight, updated_at FROM (SELECT * FROM transition_stats WHERE profile_id=:musik_profile) AS transition_stats WHERE from_id = ? AND to_id = ?`,
		fromID, toID,
	).Scan(&prevWeight, &prevUpdated)
	decayed := DecayTransitionWeight(prevWeight, prevUpdated, now) + delta
	_, err := s.DB.Exec(`
INSERT INTO transition_stats(
  from_id, to_id, manual_count, radio_count, finished_count, partial_count,
  skip_count, decayed_weight, updated_at
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,from_id, to_id) DO UPDATE SET
  manual_count = manual_count + excluded.manual_count,
  radio_count = radio_count + excluded.radio_count,
  finished_count = finished_count + excluded.finished_count,
  partial_count = partial_count + excluded.partial_count,
  skip_count = skip_count + excluded.skip_count,
  decayed_weight = excluded.decayed_weight,
  updated_at = excluded.updated_at`,
		fromID, toID, manual, radio, finished, partial, skip, decayed, nowStr)
	return err
}

func (s *Store) LoadTransitionStatsFrom(fromID int64) (map[int64]float64, error) {
	rows, err := s.DB.Query(`
SELECT to_id, decayed_weight, updated_at FROM (SELECT * FROM transition_stats WHERE profile_id=:musik_profile) AS transition_stats WHERE from_id = ?`, fromID)
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

func (s *Store) RebuildTransitionStats() (int, error) {
	if _, err := s.DB.Exec(`DELETE FROM transition_stats WHERE transition_stats.profile_id=:musik_profile `); err != nil {
		return 0, err
	}
	rows, err := s.DB.Query(`
SELECT h.track_id, h.action, h.reason, COALESCE(h.source,''), h.ts, h.session_id,
       COALESCE(i.outcome,''), COALESCE(i.source,'')
FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
LEFT JOIN (SELECT * FROM recommendation_impressions WHERE profile_id=:musik_profile) i ON i.impression_id = h.impression_id
WHERE h.action IN ('track_end','skip','finish')
ORDER BY COALESCE(h.session_id,''), h.ts`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type ev struct {
		trackID    int64
		sessionID  string
		outcome    string
		provenance string
	}
	prev := map[string]int64{}
	n := 0
	for rows.Next() {
		var trackID int64
		var action, reason, source, ts, sessionID, outcome, impSource string
		if err := rows.Scan(&trackID, &action, &reason, &source, &ts, &sessionID, &outcome, &impSource); err != nil {
			return n, err
		}
		if outcome == "" {
			if reason == "completed" || action == "finish" {
				outcome = "finished"
			} else if action == "skip" || reason == "skipped" {
				outcome = "early_skip"
			} else {
				outcome = "partial"
			}
		}
		provenance := "radio"
		if source == "manual" || impSource == "manual" {
			provenance = "manual"
		}
		if sessionID == "" {
			sessionID = "_"
		}
		fromID := prev[sessionID]
		if fromID != 0 && fromID != trackID {
			if err := s.RecordOutcomeTransition(fromID, trackID, outcome, provenance); err != nil {
				return n, err
			}
			n++
		}
		prev[sessionID] = trackID
	}
	return n, rows.Err()
}
