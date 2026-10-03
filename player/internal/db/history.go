package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrAmbiguousImpression = errors.New("multiple pending impressions match session and track")

func NewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("generate random identity: %v", err))
	}
	return hex.EncodeToString(raw[:])
}

// RecentTrackIDs returns distinct track ids heard in the last hours (most recent first).
func (s *Store) RecentTrackIDs(hours int, limit int) ([]int64, error) {
	if hours < 1 {
		hours = 24
	}
	if limit < 1 {
		limit = 40
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
	rows, err := s.DB.Query(`
SELECT track_id FROM (
  SELECT track_id, MAX(ts) AS last_ts
  FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) AS listening_history
  WHERE ts >= ?
  GROUP BY track_id
  ORDER BY last_ts DESC
  LIMIT ?
)
`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) InsertListen(trackID int64, action, source, sessionID, reason string,
	position, duration, listened *float64) (int64, error) {
	now := time.Now().UTC()
	daypart := dayPart(now.Hour())
	weekday := mondayZeroWeekday(now.Weekday())
	record := ListenRecord{
		ProfileID: s.DB.ProfileID, TrackID: trackID, TS: now.Format(time.RFC3339Nano),
		Source: optionalString(source), Action: action, Daypart: optionalString(daypart),
		Weekday: &weekday, PositionSec: position, DurationSec: duration,
		ListenedSec: listened, SessionID: optionalString(sessionID), Reason: optionalString(reason),
	}
	if err := s.ORM.Create(&record).Error; err != nil {
		return 0, err
	}
	return record.ID, nil
}

func (s *Store) BumpTransition(fromID, toID int64, weight float64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO transitions(from_id, to_id, weight, updated_at,profile_id) VALUES (?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,from_id, to_id) DO UPDATE SET
  weight = weight + excluded.weight,
  updated_at = excluded.updated_at`, fromID, toID, weight, now)
	return err
}

func (s *Store) BumpRecStats(trackID int64, shown, skipEarly, completed int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO rec_stats(track_id, shown, skipped_early, completed, updated_at,profile_id)
VALUES (?,?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET
  shown = shown + excluded.shown,
  skipped_early = skipped_early + excluded.skipped_early,
  completed = completed + excluded.completed,
  updated_at = excluded.updated_at`,
		trackID, shown, skipEarly, completed, now)
	return err
}

type RecommendationImpression struct {
	ImpressionID  string
	RequestID     string
	SessionID     string
	TrackID       int64
	Position      int
	Score         float64
	CosineTaste   float64
	CosineCurrent float64
	Explore       bool
	NewBoost      bool
	Maturity      string
	Mode          string
	Source        string
	FeaturesJSON  string
}

type RecommendationRequest struct {
	RequestID      string
	SessionID      string
	Reason         string
	PolicyVersion  string
	ModelVersion   string
	CandidateCount int
	LatencyMS      float64
	PolicyJSON     string
}

// CreateRecommendationRequest records a queue decision and its new pending
// impressions. Reused queue items are omitted; displaced pending items are
// closed as superseded in the same transaction.
func (s *Store) CreateRecommendationRequest(
	request RecommendationRequest,
	items []RecommendationImpression,
	superseded []string,
) error {
	if request.RequestID == "" {
		return errors.New("request_id is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	policyVersion := any(nil)
	policyJSON := any(nil)
	if request.PolicyJSON != "" {
		policyVersion = 1
		policyJSON = request.PolicyJSON
	}
	if _, err = tx.Exec(`
INSERT INTO recommendation_requests(
  request_id, session_id, reason, policy_version, model_version,
  candidate_count, latency_ms, created_at, policy_schema_version, policy_json
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,:musik_profile)`,
		request.RequestID, request.SessionID, request.Reason, request.PolicyVersion,
		nullStr(request.ModelVersion), request.CandidateCount, request.LatencyMS, now,
		policyVersion, policyJSON); err != nil {
		return err
	}
	for _, impressionID := range superseded {
		if impressionID == "" {
			continue
		}
		if _, err = tx.Exec(`
UPDATE recommendation_impressions
SET outcome='superseded', closed_at=?
WHERE recommendation_impressions.profile_id=:musik_profile AND ( impression_id=? AND outcome='pending' AND played_at IS NULL AND closed_at IS NULL) `,
			now, impressionID); err != nil {
			return err
		}
	}
	stmt, err := tx.Prepare(`
INSERT INTO recommendation_impressions(
  impression_id, request_id, session_id, track_id, position, score,
  cosine_taste, cosine_current, explore, new_boost, maturity, mode,
  source, features_schema_version, features_json, queued_at, shown_at,
  outcome, legacy
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending',0,:musik_profile)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		featuresVersion := any(nil)
		featuresJSON := any(nil)
		if item.FeaturesJSON != "" {
			featuresVersion = 1
			featuresJSON = item.FeaturesJSON
		}
		if _, err = stmt.Exec(
			item.ImpressionID, request.RequestID, item.SessionID, item.TrackID,
			item.Position, item.Score, item.CosineTaste, item.CosineCurrent,
			item.Explore, item.NewBoost, item.Maturity, item.Mode, item.Source,
			featuresVersion, featuresJSON, now, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertRecommendationImpressions is retained for internal compatibility. New
// playback code should call CreateRecommendationRequest so request provenance
// and lifecycle closure are never omitted.
func (s *Store) InsertRecommendationImpressions(items []RecommendationImpression) error {
	if len(items) == 0 {
		return nil
	}
	requestID := NewID()
	for i := range items {
		if items[i].ImpressionID == "" {
			items[i].ImpressionID = NewID()
		}
		items[i].RequestID = requestID
		if items[i].Source == "" {
			items[i].Source = "exploit"
			if items[i].Explore {
				items[i].Source = "explore"
			}
		}
	}
	return s.CreateRecommendationRequest(RecommendationRequest{
		RequestID: requestID, SessionID: items[0].SessionID, Reason: "compat",
		PolicyVersion: "legacy-go-compat", CandidateCount: len(items),
	}, items, nil)
}

type ImpressionRef struct {
	ImpressionID string
	RequestID    string
	Source       string
	Played       bool
}

func (s *Store) ResolveImpression(
	impressionID, sessionID string,
	trackID int64,
) (ImpressionRef, error) {
	var ref ImpressionRef
	if impressionID != "" {
		err := s.DB.QueryRow(`
SELECT impression_id, COALESCE(request_id,''), source, played_at IS NOT NULL
FROM (SELECT * FROM recommendation_impressions WHERE profile_id=:musik_profile) AS recommendation_impressions
WHERE impression_id=? AND session_id=? AND track_id=? AND legacy=0`,
			impressionID, sessionID, trackID,
		).Scan(&ref.ImpressionID, &ref.RequestID, &ref.Source, &ref.Played)
		return ref, err
	}
	rows, err := s.DB.Query(`
SELECT impression_id, COALESCE(request_id,''), source, played_at IS NOT NULL
FROM (SELECT * FROM recommendation_impressions WHERE profile_id=:musik_profile) AS recommendation_impressions
WHERE session_id=? AND track_id=? AND outcome='pending'
  AND closed_at IS NULL AND legacy=0
ORDER BY queued_at DESC LIMIT 2`, sessionID, trackID)
	if err != nil {
		return ref, err
	}
	defer rows.Close()
	var refs []ImpressionRef
	for rows.Next() {
		var candidate ImpressionRef
		if err := rows.Scan(&candidate.ImpressionID, &candidate.RequestID, &candidate.Source, &candidate.Played); err != nil {
			return ref, err
		}
		refs = append(refs, candidate)
	}
	if err := rows.Err(); err != nil {
		return ref, err
	}
	if len(refs) == 0 {
		return ref, sql.ErrNoRows
	}
	if len(refs) > 1 {
		return ref, ErrAmbiguousImpression
	}
	return refs[0], nil
}

type LifecycleEvent struct {
	EventID       string
	Type          string
	TrackID       int64
	SessionID     string
	ImpressionID  string
	RequestID     string
	Source        string
	DeviceID      string
	ClientID      string
	Reason        string
	PositionSec   *float64
	DurationSec   *float64
	ListenedSec   *float64
	ListenedRatio float64
	Outcome       string
}

type LifecycleResult struct {
	Inserted         bool
	LifecycleChanged bool
}

// ApplyLifecycleEvent appends an event exactly once and conditionally advances
// the linked impression. Derived track_stats are changed only when the
// lifecycle transition itself wins, making browser retries harmless.
func (s *Store) ApplyLifecycleEvent(ev LifecycleEvent) (LifecycleResult, error) {
	if ev.EventID == "" {
		return LifecycleResult{}, errors.New("event_id is required")
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	tx, err := s.DB.Begin()
	if err != nil {
		return LifecycleResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`
INSERT OR IGNORE INTO listening_history(
  track_id, ts, source, action, daypart, weekday, position_sec,
  duration_sec, listened_sec, session_id, reason, event_id,
  event_schema_version, request_id, impression_id, device_id, client_id
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,:musik_profile)`,
		ev.TrackID, nowText, ev.Source, ev.Type, dayPart(now.Hour()),
		mondayZeroWeekday(now.Weekday()), ev.PositionSec, ev.DurationSec,
		ev.ListenedSec, nullStr(ev.SessionID), nullStr(ev.Reason), ev.EventID,
		nullStr(ev.RequestID), nullStr(ev.ImpressionID), nullStr(ev.DeviceID),
		nullStr(ev.ClientID))
	if err != nil {
		return LifecycleResult{}, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		return LifecycleResult{Inserted: false}, tx.Commit()
	}
	out := LifecycleResult{Inserted: true}
	switch ev.Type {
	case "track_start":
		result, err = tx.Exec(`
UPDATE recommendation_impressions
SET played_at=?
WHERE recommendation_impressions.profile_id=:musik_profile AND ( impression_id=? AND session_id=? AND track_id=? AND legacy=0
  AND outcome='pending' AND played_at IS NULL AND closed_at IS NULL) `,
			nowText, ev.ImpressionID, ev.SessionID, ev.TrackID)
		if err == nil {
			out.LifecycleChanged, _ = changed(result)
		}
		if err == nil && out.LifecycleChanged {
			_, err = tx.Exec(`
INSERT INTO track_stats(track_id, plays, last_played_at, updated_at,profile_id)
VALUES (?,1,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET
  plays=plays+1, last_played_at=excluded.last_played_at,
  updated_at=excluded.updated_at`, ev.TrackID, nowText, nowText)
		}
	case "track_end", "skip":
		if ev.ImpressionID != "" {
			result, err = tx.Exec(`
UPDATE recommendation_impressions
SET outcome=?, listened_ratio=?, closed_at=?
WHERE recommendation_impressions.profile_id=:musik_profile AND ( impression_id=? AND session_id=? AND track_id=? AND legacy=0
  AND outcome='pending' AND played_at IS NOT NULL AND closed_at IS NULL) `,
				ev.Outcome, ev.ListenedRatio, nowText, ev.ImpressionID, ev.SessionID, ev.TrackID)
			if err == nil {
				out.LifecycleChanged, _ = changed(result)
			}
		} else if err == nil {
			var prior int
			err = tx.QueryRow(`
SELECT COUNT(*) FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) AS listening_history
WHERE session_id=? AND track_id=? AND action IN ('track_end','skip') AND event_id!=?`,
				ev.SessionID, ev.TrackID, ev.EventID).Scan(&prior)
			out.LifecycleChanged = err == nil && prior == 0
		}
		if err == nil && out.LifecycleChanged {
			finish, partial, early := 0, 0, 0
			switch ev.Outcome {
			case "finished":
				finish = 1
			case "partial":
				partial = 1
			case "early_skip":
				early = 1
			}
			_, err = tx.Exec(`
INSERT INTO track_stats(
  track_id, finishes, partial, early_skips, last_finished_at, updated_at
,profile_id) VALUES (?,?,?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET
  finishes=finishes+excluded.finishes,
  partial=partial+excluded.partial,
  early_skips=early_skips+excluded.early_skips,
  last_finished_at=CASE WHEN excluded.finishes=1 THEN excluded.last_finished_at
                        ELSE track_stats.last_finished_at END,
  updated_at=excluded.updated_at`,
				ev.TrackID, finish, partial, early,
				func() any {
					if finish == 1 {
						return nowText
					}
					return nil
				}(), nowText)
		}
	case "like", "dislike":
		var prior int
		if ev.ImpressionID != "" {
			err = tx.QueryRow(`
SELECT COUNT(*) FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) AS listening_history
WHERE impression_id=? AND action=? AND event_id!=?`,
				ev.ImpressionID, ev.Type, ev.EventID).Scan(&prior)
		} else {
			err = tx.QueryRow(`
SELECT COUNT(*) FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) AS listening_history
WHERE session_id=? AND track_id=? AND action=? AND event_id!=?`,
				ev.SessionID, ev.TrackID, ev.Type, ev.EventID).Scan(&prior)
		}
		if err == nil && prior == 0 {
			likes, dislikes := 0, 0
			if ev.Type == "like" {
				likes = 1
			} else {
				dislikes = 1
			}
			_, err = tx.Exec(`
INSERT INTO track_stats(track_id, likes, dislikes, updated_at,profile_id)
VALUES (?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,track_id) DO UPDATE SET
  likes=likes+excluded.likes, dislikes=dislikes+excluded.dislikes,
  updated_at=excluded.updated_at`, ev.TrackID, likes, dislikes, nowText)
			out.LifecycleChanged = true
		}
	}
	if err != nil {
		return LifecycleResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LifecycleResult{}, err
	}
	return out, nil
}

func changed(result sql.Result) (bool, error) {
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) ClosePendingImpressions(sessionID, outcome string, ids []string) error {
	if outcome != "superseded" && outcome != "abandoned" {
		return fmt.Errorf("invalid pending outcome %q", outcome)
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if _, err := tx.Exec(`
UPDATE recommendation_impressions SET outcome=?, closed_at=?
WHERE recommendation_impressions.profile_id=:musik_profile AND ( impression_id=? AND session_id=? AND outcome='pending'
  AND played_at IS NULL AND closed_at IS NULL) `, outcome, now, id, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AbandonSessionImpressions(sessionID string) error {
	_, err := s.DB.Exec(`
UPDATE recommendation_impressions
SET outcome='abandoned', closed_at=?
WHERE recommendation_impressions.profile_id=:musik_profile AND ( session_id=? AND outcome='pending' AND played_at IS NULL AND closed_at IS NULL) `,
		time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	return err
}
