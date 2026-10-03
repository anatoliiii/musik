package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type TasteContext struct {
	ID              string       `json:"context_id"`
	Kind            string       `json:"kind"`
	Name            string       `json:"name"`
	Icon            string       `json:"icon,omitempty"`
	Influence       float64      `json:"influence"`
	LearningEnabled bool         `json:"learning_enabled"`
	Seeds           ContextSeeds `json:"seeds"`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
	ArchivedAt      string       `json:"archived_at,omitempty"`
	PositiveSamples int          `json:"positive_samples"`
	HasVector       bool         `json:"has_vector"`
}

type ContextSeeds struct {
	SchemaVersion int                `json:"schema_version"`
	Tracks        []int64            `json:"tracks,omitempty"`
	Artists       []string           `json:"artists,omitempty"`
	Albums        []ContextAlbumSeed `json:"albums,omitempty"`
}

type ContextAlbumSeed struct {
	Artist string `json:"artist"`
	Album  string `json:"album"`
}

type TasteContextState struct {
	ContextID       string
	PositiveVector  []byte
	EmbeddingDim    int
	PositiveSamples int
	NegativeSamples int
	NegativesJSON   string
	ModelVersion    string
	UpdatedAt       string
}

func validContextKind(kind string) bool {
	switch kind {
	case "mood", "place", "activity":
		return true
	default:
		return false
	}
}

func (s *Store) CreateTasteContext(ctx TasteContext) (TasteContext, error) {
	if ctx.ID == "" {
		ctx.ID = NewID()
	}
	if !validContextKind(ctx.Kind) {
		return ctx, fmt.Errorf("kind must be mood, place or activity")
	}
	ctx.Name = strings.TrimSpace(ctx.Name)
	if ctx.Name == "" {
		return ctx, fmt.Errorf("name required")
	}
	if ctx.Influence <= 0 {
		ctx.Influence = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ctx.CreatedAt, ctx.UpdatedAt = now, now
	if ctx.Seeds.SchemaVersion == 0 && (len(ctx.Seeds.Tracks) > 0 || len(ctx.Seeds.Artists) > 0 || len(ctx.Seeds.Albums) > 0) {
		ctx.Seeds.SchemaVersion = 1
	}
	var seedsJSON sql.NullString
	var seedsVer sql.NullInt64
	if ctx.Seeds.SchemaVersion > 0 {
		payload, err := json.Marshal(ctx.Seeds)
		if err != nil {
			return ctx, err
		}
		seedsJSON = sql.NullString{String: string(payload), Valid: true}
		seedsVer = sql.NullInt64{Int64: int64(ctx.Seeds.SchemaVersion), Valid: true}
	}
	learning := 1
	if !ctx.LearningEnabled && ctx.ID != "" {
		// default is enabled unless caller set the field explicitly via API
	}
	if !ctx.LearningEnabled {
		learning = 0
	}
	_, err := s.DB.Exec(`
INSERT INTO taste_contexts(
  context_id, kind, name, icon, influence, learning_enabled,
  seeds_schema_version, seeds_json, created_at, updated_at
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,:musik_profile)`,
		ctx.ID, ctx.Kind, ctx.Name, nullStr(ctx.Icon), ctx.Influence, learning,
		seedsVer, seedsJSON, ctx.CreatedAt, ctx.UpdatedAt)
	return ctx, err
}

func (s *Store) UpdateTasteContext(ctx TasteContext) error {
	if ctx.ID == "" {
		return fmt.Errorf("context_id required")
	}
	if ctx.Name != "" {
		ctx.Name = strings.TrimSpace(ctx.Name)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var seedsJSON sql.NullString
	var seedsVer sql.NullInt64
	if ctx.Seeds.SchemaVersion > 0 {
		payload, err := json.Marshal(ctx.Seeds)
		if err != nil {
			return err
		}
		seedsJSON = sql.NullString{String: string(payload), Valid: true}
		seedsVer = sql.NullInt64{Int64: int64(ctx.Seeds.SchemaVersion), Valid: true}
	}
	learning := 1
	if !ctx.LearningEnabled {
		learning = 0
	}
	_, err := s.DB.Exec(`
UPDATE taste_contexts SET
  name = COALESCE(NULLIF(?, ''), name),
  icon = COALESCE(NULLIF(?, ''), icon),
  influence = CASE WHEN ? > 0 THEN ? ELSE influence END,
  learning_enabled = ?,
  seeds_schema_version = CASE WHEN ? THEN ? ELSE seeds_schema_version END,
  seeds_json = CASE WHEN ? THEN ? ELSE seeds_json END,
  updated_at = ?
WHERE taste_contexts.profile_id=:musik_profile AND ( context_id = ? AND archived_at IS NULL) `,
		ctx.Name, ctx.Icon, ctx.Influence, ctx.Influence, learning,
		seedsJSON.Valid, seedsVer, seedsJSON.Valid, seedsJSON, now, ctx.ID)
	return err
}

func (s *Store) ArchiveTasteContext(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
UPDATE taste_contexts SET archived_at = ?, updated_at = ?
WHERE taste_contexts.profile_id=:musik_profile AND ( context_id = ? AND archived_at IS NULL) `, now, now, id)
	return err
}

func (s *Store) GetTasteContext(id string) (*TasteContext, error) {
	row := s.DB.QueryRow(`
SELECT c.context_id, c.kind, c.name, COALESCE(c.icon,''), c.influence, c.learning_enabled,
       c.seeds_schema_version, c.seeds_json, c.created_at, c.updated_at, COALESCE(c.archived_at,''),
       COALESCE(s.positive_samples,0), s.positive_vector
FROM (SELECT * FROM taste_contexts WHERE profile_id=:musik_profile) c
LEFT JOIN (SELECT * FROM taste_context_states WHERE profile_id=:musik_profile) s ON s.context_id = c.context_id
WHERE c.context_id = ?`, id)
	return scanTasteContext(row)
}

func (s *Store) ListTasteContexts(includeArchived bool) ([]TasteContext, error) {
	q := `
SELECT c.context_id, c.kind, c.name, COALESCE(c.icon,''), c.influence, c.learning_enabled,
       c.seeds_schema_version, c.seeds_json, c.created_at, c.updated_at, COALESCE(c.archived_at,''),
       COALESCE(s.positive_samples,0), s.positive_vector
FROM (SELECT * FROM taste_contexts WHERE profile_id=:musik_profile) c
LEFT JOIN (SELECT * FROM taste_context_states WHERE profile_id=:musik_profile) s ON s.context_id = c.context_id`
	if !includeArchived {
		q += ` WHERE c.archived_at IS NULL`
	}
	q += ` ORDER BY c.kind, c.name`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TasteContext
	for rows.Next() {
		ctx, err := scanTasteContext(rows)
		if err != nil {
			return nil, err
		}
		if ctx != nil {
			out = append(out, *ctx)
		}
	}
	return out, rows.Err()
}

type contextScanner interface {
	Scan(dest ...any) error
}

func scanTasteContext(row contextScanner) (*TasteContext, error) {
	var ctx TasteContext
	var learning int
	var seedsVer sql.NullInt64
	var seedsJSON sql.NullString
	var vector []byte
	if err := row.Scan(&ctx.ID, &ctx.Kind, &ctx.Name, &ctx.Icon, &ctx.Influence, &learning,
		&seedsVer, &seedsJSON, &ctx.CreatedAt, &ctx.UpdatedAt, &ctx.ArchivedAt,
		&ctx.PositiveSamples, &vector); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	ctx.LearningEnabled = learning == 1
	ctx.HasVector = len(vector) > 0
	if seedsJSON.Valid {
		_ = json.Unmarshal([]byte(seedsJSON.String), &ctx.Seeds)
		if seedsVer.Valid {
			ctx.Seeds.SchemaVersion = int(seedsVer.Int64)
		}
	}
	return &ctx, nil
}

func (s *Store) UpsertTasteContextState(state TasteContextState) error {
	if state.ModelVersion == "" {
		state.ModelVersion = "clap-default"
	}
	if state.UpdatedAt == "" {
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var dim sql.NullInt64
	if state.EmbeddingDim > 0 {
		dim = sql.NullInt64{Int64: int64(state.EmbeddingDim), Valid: true}
	}
	_, err := s.DB.Exec(`
INSERT INTO taste_context_states(
  context_id, positive_vector, embedding_dim, negative_prototypes_json,
  positive_samples, negative_samples, model_version, updated_at
,profile_id) VALUES (?,?,?,?,?,?,?,?,:musik_profile)
ON CONFLICT(profile_id,context_id) DO UPDATE SET
  positive_vector=excluded.positive_vector,
  embedding_dim=excluded.embedding_dim,
  negative_prototypes_json=excluded.negative_prototypes_json,
  positive_samples=excluded.positive_samples,
  negative_samples=excluded.negative_samples,
  model_version=excluded.model_version,
  updated_at=excluded.updated_at`,
		state.ContextID, nullBytes(state.PositiveVector), dim, nullStr(state.NegativesJSON),
		state.PositiveSamples, state.NegativeSamples, state.ModelVersion, state.UpdatedAt)
	return err
}

func (s *Store) LoadTasteContextState(id string) (*TasteContextState, error) {
	var state TasteContextState
	var dim sql.NullInt64
	var vector []byte
	err := s.DB.QueryRow(`
SELECT context_id, positive_vector, embedding_dim, COALESCE(negative_prototypes_json,''),
       positive_samples, negative_samples, model_version, updated_at
FROM (SELECT * FROM taste_context_states WHERE profile_id=:musik_profile) AS taste_context_states WHERE context_id = ?`, id).Scan(
		&state.ContextID, &vector, &dim, &state.NegativesJSON,
		&state.PositiveSamples, &state.NegativeSamples, &state.ModelVersion, &state.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.PositiveVector = vector
	if dim.Valid {
		state.EmbeddingDim = int(dim.Int64)
	}
	return &state, nil
}

func (s *Store) SetSessionContexts(sessionID string, contextIDs []string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
UPDATE session_contexts SET deactivated_at = ?
WHERE session_contexts.profile_id=:musik_profile AND ( session_id = ? AND deactivated_at IS NULL) `, now, sessionID); err != nil {
		return err
	}
	for _, id := range contextIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.Exec(`
INSERT INTO session_contexts(session_id, context_id, activated_at,profile_id)
VALUES (?,?,?,:musik_profile)`, sessionID, id, now); err != nil {
			return err
		}
	}
	payload, _ := json.Marshal(contextIDs)
	if _, err := tx.Exec(`
UPDATE play_sessions SET active_contexts_json = ? WHERE play_sessions.profile_id=:musik_profile AND ( id = ?) `, string(payload), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActiveSessionContexts(sessionID string) ([]string, error) {
	rows, err := s.DB.Query(`
SELECT context_id FROM (SELECT * FROM session_contexts WHERE profile_id=:musik_profile) AS session_contexts
WHERE session_id = ? AND deactivated_at IS NULL
ORDER BY activated_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) AttachRequestContexts(requestID string, contextIDs []string) error {
	for _, id := range contextIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, err := s.DB.Exec(`
INSERT OR IGNORE INTO request_contexts(request_id, context_id,profile_id) VALUES (?,?,:musik_profile)`,
			requestID, id); err != nil {
			return err
		}
	}
	return nil
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
