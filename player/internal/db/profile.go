package db

import (
	"database/sql"
	"time"
)

func (s *Store) SaveProfile(context string, emb []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(
		`INSERT INTO user_profile_snapshots(context, embedding, created_at,profile_id) VALUES (?,?,?,:musik_profile)`,
		context, emb, now,
	)
	return err
}

// PruneProfiles keeps the newest keep snapshots for a context.
func (s *Store) PruneProfiles(context string, keep int) error {
	if keep < 1 {
		keep = 50
	}
	_, err := s.DB.Exec(`
DELETE FROM user_profile_snapshots
WHERE user_profile_snapshots.profile_id=:musik_profile AND ( context = ? AND id NOT IN (
  SELECT id FROM (SELECT * FROM user_profile_snapshots WHERE profile_id=:musik_profile) AS user_profile_snapshots WHERE context = ?
  ORDER BY id DESC LIMIT ?
)) `, context, context, keep)
	return err
}

func (s *Store) LatestProfile(context string) ([]byte, error) {
	var b []byte
	err := s.DB.QueryRow(`
SELECT embedding FROM (SELECT * FROM user_profile_snapshots WHERE profile_id=:musik_profile) AS user_profile_snapshots WHERE context = ?
ORDER BY id DESC LIMIT 1`, context).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// ListenSignalCounts counts positive/negative signals for maturity.
func (s *Store) ListenSignalCounts() (pos, neg int, err error) {
	err = s.DB.QueryRow(`
SELECT
  COALESCE(SUM(CASE
    WHEN action IN ('like','finish') THEN 1
    WHEN action = 'track_end' AND (reason IN ('completed','next') OR COALESCE(listened_sec,0) >= 0.8 * COALESCE(duration_sec,1)) THEN 1
    ELSE 0 END), 0),
  COALESCE(SUM(CASE
    WHEN action IN ('dislike','skip') THEN 1
    WHEN action = 'track_end' AND reason = 'skipped' AND COALESCE(listened_sec,0) < 0.3 * COALESCE(duration_sec,1) THEN 1
    ELSE 0 END), 0)
FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) AS listening_history`).Scan(&pos, &neg)
	return
}

type ArtistCount struct {
	Artist string `json:"artist"`
	Count  int    `json:"count"`
}

func (s *Store) TopArtists(limit int) ([]ArtistCount, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT COALESCE(t.artist,'(unknown)'), COUNT(*) AS c
FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
JOIN tracks t ON t.id = h.track_id
WHERE h.action IN ('like','finish','track_end')
  AND (h.reason IS NULL OR h.reason != 'skipped')
GROUP BY 1
ORDER BY c DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistCount
	for rows.Next() {
		var a ArtistCount
		if err := rows.Scan(&a.Artist, &a.Count); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type ClusterCount struct {
	ClusterID int `json:"cluster_id"`
	Count     int `json:"count"`
}

type TasteStateRow struct {
	Key                    string
	PositiveVector         []byte
	EmbeddingDim           int
	PositiveSamples        int
	NegativeSamples        int
	NegativePrototypesJSON string
}

func (s *Store) UpsertTasteState(row TasteStateRow) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var vector any
	var dim any
	if len(row.PositiveVector) > 0 {
		vector, dim = row.PositiveVector, row.EmbeddingDim
	}
	var negativeVersion any
	var negatives any
	if row.NegativePrototypesJSON != "" {
		negativeVersion, negatives = 1, row.NegativePrototypesJSON
	}
	_, err := s.DB.Exec(`
INSERT INTO taste_states(
  state_key, positive_vector, embedding_dim, positive_samples,
  negative_samples, negative_schema_version, negative_prototypes_json,
  model_version, updated_at
,profile_id) VALUES (?,?,?,?,?,?,?,'clap-default',?,:musik_profile)
ON CONFLICT(profile_id,state_key) DO UPDATE SET
  positive_vector=excluded.positive_vector,
  embedding_dim=excluded.embedding_dim,
  positive_samples=excluded.positive_samples,
  negative_samples=excluded.negative_samples,
  negative_schema_version=excluded.negative_schema_version,
  negative_prototypes_json=excluded.negative_prototypes_json,
  model_version=excluded.model_version,
  updated_at=excluded.updated_at`,
		row.Key, vector, dim, row.PositiveSamples, row.NegativeSamples,
		negativeVersion, negatives, now)
	return err
}

func (s *Store) LoadTasteStates() ([]TasteStateRow, error) {
	rows, err := s.DB.Query(`
SELECT state_key, positive_vector, COALESCE(embedding_dim,0),
       positive_samples, negative_samples,
       COALESCE(negative_prototypes_json,'')
FROM (SELECT * FROM taste_states WHERE profile_id=:musik_profile) AS taste_states ORDER BY state_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteStateRow
	for rows.Next() {
		var row TasteStateRow
		if err := rows.Scan(
			&row.Key, &row.PositiveVector, &row.EmbeddingDim,
			&row.PositiveSamples, &row.NegativeSamples,
			&row.NegativePrototypesJSON,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type TasteCentroidRow struct {
	Index            int
	Vector           []byte
	EmbeddingDim     int
	Mass             float64
	Label            string
	SampleCount      int
	AlgorithmVersion string
}

func (s *Store) ReplaceTasteCentroids(rows []TasteCentroidRow) error {
	if len(rows) == 0 {
		return nil
	}
	version := rows[0].AlgorithmVersion
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM taste_centroids WHERE taste_centroids.profile_id=:musik_profile AND ( algorithm_version=?) `, version); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stmt, err := tx.Prepare(`
INSERT INTO taste_centroids(
  idx, vector, embedding_dim, mass, label, sample_count, updated_at, algorithm_version
,profile_id) VALUES (?,?,?,?,?,?,?,?,:musik_profile)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err = stmt.Exec(
			row.Index, row.Vector, row.EmbeddingDim, row.Mass, nullStr(row.Label),
			row.SampleCount, now, row.AlgorithmVersion,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadTasteCentroids(algorithmVersion string) ([]TasteCentroidRow, error) {
	rows, err := s.DB.Query(`
SELECT idx, vector, embedding_dim, mass, COALESCE(label,''), sample_count, algorithm_version
FROM (SELECT * FROM taste_centroids WHERE profile_id=:musik_profile) AS taste_centroids WHERE algorithm_version=? ORDER BY idx`, algorithmVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteCentroidRow
	for rows.Next() {
		var row TasteCentroidRow
		if err := rows.Scan(
			&row.Index, &row.Vector, &row.EmbeddingDim, &row.Mass,
			&row.Label, &row.SampleCount, &row.AlgorithmVersion,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type TasteSampleRow struct {
	TrackID int64
	Weight  float64
	At      time.Time
}

// PositiveTasteSamples returns algorithmic finished/liked impressions for
// centroid rebuild. Manual, share, legacy, partial, skip, and unplayed rows
// are excluded so the long-taste clusters stay on the training boundary.
func (s *Store) PositiveTasteSamples(limit int) ([]TasteSampleRow, error) {
	if limit < 1 {
		limit = 400
	}
	rows, err := s.DB.Query(`
SELECT i.track_id,
       COALESCE(i.closed_at, i.played_at, i.queued_at),
       CASE WHEN EXISTS(
         SELECT 1 FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
         WHERE h.impression_id=i.impression_id AND h.action='like'
       ) THEN 2.0 ELSE 1.0 END
FROM (SELECT * FROM recommendation_impressions WHERE profile_id=:musik_profile) i
WHERE i.legacy=0
  AND i.source NOT IN ('manual', 'legacy')
  AND COALESCE(i.mode, '') != 'share'
  AND (
    i.outcome='finished'
    OR EXISTS (
      SELECT 1 FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
      WHERE h.impression_id=i.impression_id AND h.action='like'
    )
  )
ORDER BY COALESCE(i.closed_at, i.played_at, i.queued_at) DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteSampleRow
	for rows.Next() {
		var row TasteSampleRow
		var at string
		if err := rows.Scan(&row.TrackID, &at, &row.Weight); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			row.At = t
		} else if t, err := time.Parse(time.RFC3339, at); err == nil {
			row.At = t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) TopClusters(limit int) ([]ClusterCount, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT COALESCE(f.cluster_id, -1), COUNT(*) AS c
FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
JOIN features f ON f.track_id = h.track_id
WHERE h.action IN ('like','finish','track_end')
  AND (h.reason IS NULL OR h.reason != 'skipped')
  AND f.cluster_id IS NOT NULL
GROUP BY 1
ORDER BY c DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterCount
	for rows.Next() {
		var c ClusterCount
		if err := rows.Scan(&c.ClusterID, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
