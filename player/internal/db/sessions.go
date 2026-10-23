package db

import (
	"database/sql"
	"time"
)

// LoadTransitionGraph loads from→to weights for radio.
// transition_stats.decayed_weight wins and is decayed again to now, including
// a negative edge left by a skip. transitions.weight of at least 1 fills only
// the pairs that do not have a decay row yet.
func (s *Store) LoadTransitionGraph() (map[int64]map[int64]float64, error) {
	out := map[int64]map[int64]float64{}
	covered := map[int64]map[int64]struct{}{}
	note := func(from, to int64) {
		edges := covered[from]
		if edges == nil {
			edges = map[int64]struct{}{}
			covered[from] = edges
		}
		edges[to] = struct{}{}
	}
	put := func(from, to int64, weight float64) {
		edges := out[from]
		if edges == nil {
			edges = map[int64]float64{}
			out[from] = edges
		}
		edges[to] = weight
	}

	rows, err := s.DB.Query(`
SELECT from_id, to_id, decayed_weight, COALESCE(updated_at,'')
FROM transition_stats WHERE profile_id = ?
ORDER BY ABS(decayed_weight) DESC LIMIT 50000`, s.ProfileID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for rows.Next() {
		var from, to int64
		var weight float64
		var updated string
		if err := rows.Scan(&from, &to, &weight, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		note(from, to)
		if w := DecayTransitionWeight(weight, updated, now); w != 0 {
			put(from, to, w)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.DB.Query(`
SELECT from_id, to_id, weight FROM transitions
WHERE profile_id = ? AND weight >= 1
ORDER BY weight DESC LIMIT 50000`, s.ProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var from, to int64
		var weight float64
		if err := rows.Scan(&from, &to, &weight); err != nil {
			return nil, err
		}
		if _, ok := covered[from][to]; ok {
			continue
		}
		put(from, to, weight)
	}
	return out, rows.Err()
}

type PlaySessionRow struct {
	ID                 string
	Mode               string
	CurrentID          int64
	QueueJSON          string
	ExcludeJSON        string
	RatedJSON          string
	DailyIDsJSON       string
	DailyPos           int
	PlaylistName       string
	PlaylistKind       string
	CurrentItemJSON    string
	TasteStateJSON     string
	ActiveContextsJSON string
	TransitionProfile  string
	UpdatedAt          string
}

func (s *Store) UpsertPlaySession(row PlaySessionRow) error {
	_, err := s.DB.Exec(`
INSERT INTO play_sessions(
  id, mode, current_id, queue_json, exclude_json, rated_json,
  daily_ids_json, daily_pos, playlist_name, playlist_kind, current_item_json,
  taste_state_schema_version, taste_state_json, active_contexts_json,
  transition_profile, updated_at, profile_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  mode=excluded.mode,
  current_id=excluded.current_id,
  queue_json=excluded.queue_json,
  exclude_json=excluded.exclude_json,
  rated_json=excluded.rated_json,
  daily_ids_json=excluded.daily_ids_json,
  daily_pos=excluded.daily_pos,
  playlist_name=excluded.playlist_name,
  playlist_kind=excluded.playlist_kind,
  current_item_json=excluded.current_item_json,
  taste_state_schema_version=excluded.taste_state_schema_version,
  taste_state_json=excluded.taste_state_json,
  active_contexts_json=excluded.active_contexts_json,
  transition_profile=excluded.transition_profile,
  updated_at=excluded.updated_at WHERE play_sessions.profile_id = excluded.profile_id`,
		row.ID, row.Mode, row.CurrentID, nullStr(row.QueueJSON), nullStr(row.ExcludeJSON),
		nullStr(row.RatedJSON), nullStr(row.DailyIDsJSON), row.DailyPos,
		row.PlaylistName, row.PlaylistKind, nullStr(row.CurrentItemJSON),
		nullStr(row.TasteStateJSON), nullStr(row.ActiveContextsJSON),
		func() string {
			if row.TransitionProfile == "" {
				return "smooth"
			}
			return row.TransitionProfile
		}(), row.UpdatedAt, s.ProfileID)
	return err
}

func (s *Store) LoadPlaySession(id string) (PlaySessionRow, bool, error) {
	var row PlaySessionRow
	var q, ex, rated, daily, currentItem, tasteState, contexts sql.NullString
	var profile sql.NullString
	err := s.DB.QueryRow(`
SELECT id, mode, current_id, queue_json, exclude_json, rated_json,
       daily_ids_json, daily_pos, playlist_name, playlist_kind,
       current_item_json, taste_state_json, active_contexts_json,
       transition_profile, updated_at
FROM play_sessions WHERE profile_id = ? AND id = ?`, s.ProfileID, id).Scan(
		&row.ID, &row.Mode, &row.CurrentID, &q, &ex, &rated, &daily,
		&row.DailyPos, &row.PlaylistName, &row.PlaylistKind, &currentItem,
		&tasteState, &contexts, &profile, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return PlaySessionRow{}, false, nil
	}
	if err != nil {
		return PlaySessionRow{}, false, err
	}
	if q.Valid {
		row.QueueJSON = q.String
	}
	if ex.Valid {
		row.ExcludeJSON = ex.String
	}
	if rated.Valid {
		row.RatedJSON = rated.String
	}
	if daily.Valid {
		row.DailyIDsJSON = daily.String
	}
	if currentItem.Valid {
		row.CurrentItemJSON = currentItem.String
	}
	if tasteState.Valid {
		row.TasteStateJSON = tasteState.String
	}
	if contexts.Valid {
		row.ActiveContextsJSON = contexts.String
	}
	if profile.Valid {
		row.TransitionProfile = profile.String
	}
	return row, true, nil
}

func (s *Store) DeletePlaySession(id string) error {
	_, err := s.DB.Exec(`DELETE FROM play_sessions WHERE profile_id = ? AND id = ?`, s.ProfileID, id)
	return err
}

func (s *Store) DeleteStalePlaySessions(olderThan time.Time) error {
	_, err := s.DB.Exec(`DELETE FROM play_sessions WHERE profile_id = ? AND updated_at < ?`, s.ProfileID, olderThan.UTC().Format(time.RFC3339Nano))
	return err
}

// CountPlaySessions counts the owner's sessions. Listener rows (mode=share)
// are not part of the limit, including leftovers from older builds.
func (s *Store) CountPlaySessions() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM play_sessions WHERE profile_id = ? AND mode <> 'share'`, s.ProfileID).Scan(&n)
	return n, err
}

func (s *Store) OldestPlaySessionIDs(limit int) ([]string, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, id *string) error {
		return rows.Scan(id)
	}, `SELECT id FROM play_sessions WHERE profile_id = ? AND mode <> 'share' ORDER BY updated_at ASC LIMIT ?`, s.ProfileID, limit)
}
