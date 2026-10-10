package db

import (
	"database/sql"
	"time"
)

type RadioPrefs struct {
	ExploreLo float64
	ExploreHi float64
}

type ExploreArmRow struct {
	ArmKey      string
	ArmKind     string
	Alpha       float64
	Beta        float64
	Successes   int
	Failures    int
	LastDecayAt time.Time
}

func (s *Store) LoadRadioPrefs() (RadioPrefs, error) {
	prefs := RadioPrefs{ExploreLo: 0.10, ExploreHi: 0.40}
	var lo, hi float64
	err := s.DB.QueryRow(`
SELECT explore_lo, explore_hi FROM radio_prefs WHERE profile_id = ? AND owner_scope='local'`, s.ProfileID).Scan(&lo, &hi)
	if err == sql.ErrNoRows {
		return prefs, nil
	}
	if err != nil {
		return prefs, err
	}
	prefs.ExploreLo, prefs.ExploreHi = lo, hi
	return prefs, nil
}

func (s *Store) SaveRadioPrefs(lo, hi float64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO radio_prefs(owner_scope, explore_lo, explore_hi, updated_at, profile_id)
VALUES ('local', ?, ?, ?, ?)
ON CONFLICT(profile_id,owner_scope) DO UPDATE SET
  explore_lo=excluded.explore_lo, explore_hi=excluded.explore_hi, updated_at=excluded.updated_at`,
		lo, hi, now, s.ProfileID)
	return err
}

func (s *Store) LoadExploreArms() ([]ExploreArmRow, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, row *ExploreArmRow) error {
		var decay string
		if err := rows.Scan(&row.ArmKey, &row.ArmKind, &row.Alpha, &row.Beta, &row.Successes, &row.Failures, &decay); err != nil {
			return err
		}
		if decay != "" {
			if ts, err := time.Parse(time.RFC3339Nano, decay); err == nil {
				row.LastDecayAt = ts
			} else if ts, err := time.Parse(time.RFC3339, decay); err == nil {
				row.LastDecayAt = ts
			}
		}
		return nil
	}, `
SELECT arm_key, arm_kind, alpha, beta, successes, failures, COALESCE(last_decay_at, '')
FROM explore_arms WHERE profile_id = ?`, s.ProfileID)
}

func (s *Store) UpdateExploreArm(key, kind string, success bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	alphaInc, betaInc := 0.0, 0.0
	succInc, failInc := 0, 0
	if success {
		alphaInc, succInc = 1, 1
	} else {
		betaInc, failInc = 1, 1
	}
	priorA, priorB := 1.0, 1.0
	if kind == "aggregate" {
		priorA, priorB = 2.0, 8.0
	}
	_, err := s.DB.Exec(`
INSERT INTO explore_arms(
  arm_key, arm_kind, alpha, beta, successes, failures, last_decay_at, updated_at, owner_scope, profile_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'local', ?)
ON CONFLICT(profile_id,arm_key) DO UPDATE SET
  alpha=explore_arms.alpha + ?,
  beta=explore_arms.beta + ?,
  successes=explore_arms.successes + ?,
  failures=explore_arms.failures + ?,
  updated_at=excluded.updated_at`,
		key, kind, priorA+alphaInc, priorB+betaInc, succInc, failInc, now, now, s.ProfileID,
		alphaInc, betaInc, succInc, failInc)
	return err
}

func (s *Store) ExploreSourceOutcomeCounts() (map[string]int, error) {
	rows, err := s.DB.Query(`
SELECT source, COUNT(*)
FROM recommendation_impressions
WHERE profile_id = ? AND legacy=0
  AND played_at IS NOT NULL
  AND outcome IN ('finished', 'early_skip')
  AND source IN ('explore_adjacent', 'resurface', 'new_in_library', 'wildcard')
GROUP BY source`, s.ProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var source string
		var n int
		if err := rows.Scan(&source, &n); err != nil {
			return nil, err
		}
		out[source] = n
	}
	return out, rows.Err()
}
