package playback

import (
	"context"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/rules"
	"github.com/torwin-job/musik/player/internal/taste"
)

// TasteContextPatch is the fields a context update may change. An empty name
// or icon leaves that field unchanged. A nil Influence leaves the weight
// unchanged; a pointer to zero stores zero.
type TasteContextPatch struct {
	Name            string
	Icon            string
	Influence       *float64
	LearningEnabled *bool
	Seeds           db.ContextSeeds
}

// CreateTasteContext stores the context and, when the seeds resolve to a
// vector, its state row in one transaction.
func CreateTasteContext(ctx context.Context, store *db.Store, idx *index.Index, taste db.TasteContext) (*db.TasteContext, error) {
	raw, dim := contextSeedBytes(idx, taste.Seeds)
	return store.CommitTasteContext(ctx, taste, raw, dim, true)
}

// UpdateTasteContext merges patch into the stored context and rewrites the
// seed vector in the same transaction as the row.
func UpdateTasteContext(ctx context.Context, store *db.Store, idx *index.Index, id string, patch TasteContextPatch) (*db.TasteContext, error) {
	existing, err := store.GetTasteContext(id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, db.ErrContextNotFound
	}
	if patch.Name != "" {
		existing.Name = patch.Name
	}
	if patch.Icon != "" {
		existing.Icon = patch.Icon
	}
	if patch.Influence != nil {
		existing.Influence = *patch.Influence
	}
	if patch.LearningEnabled != nil {
		existing.LearningEnabled = *patch.LearningEnabled
	}
	if patch.Seeds.SchemaVersion > 0 || len(patch.Seeds.Tracks)+len(patch.Seeds.Artists)+len(patch.Seeds.Albums) > 0 {
		existing.Seeds = patch.Seeds
		if existing.Seeds.SchemaVersion == 0 {
			existing.Seeds.SchemaVersion = 1
		}
	}
	raw, dim := contextSeedBytes(idx, existing.Seeds)
	return store.CommitTasteContext(ctx, *existing, raw, dim, false)
}

func contextSeedVector(idx *index.Index, seeds db.ContextSeeds) []float32 {
	albums := make([][2]string, 0, len(seeds.Albums))
	for _, album := range seeds.Albums {
		albums = append(albums, [2]string{album.Artist, album.Album})
	}
	return taste.SeedVector(idx, seeds.Tracks, seeds.Artists, albums)
}

func contextSeedBytes(idx *index.Index, seeds db.ContextSeeds) ([]byte, int) {
	vec := contextSeedVector(idx, seeds)
	if len(vec) == 0 {
		return nil, 0
	}
	return index.Float32Bytes(vec), len(vec)
}

func (e *Engine) SetSessionContexts(sess *Session, ids []string) error {
	sess.ActiveContextIDs = append([]string(nil), ids...)
	if err := e.Store.SetSessionContexts(sess.ID, ids); err != nil {
		return err
	}
	e.persistLocked(sess)
	return nil
}

func (e *Engine) contextBlends(ids []string) []taste.ContextBlend {
	var out []taste.ContextBlend
	for _, id := range ids {
		ctx, err := e.Store.GetTasteContext(id)
		if err != nil || ctx == nil || ctx.ArchivedAt != "" {
			continue
		}
		state, _ := e.Store.LoadTasteContextState(id)
		var vector []float32
		if state != nil && len(state.PositiveVector) > 0 {
			vector = index.BytesToFloat32(state.PositiveVector)
		} else {
			vector = contextSeedVector(e.Idx, ctx.Seeds)
		}
		if len(vector) == 0 {
			continue
		}
		samples := 0
		if state != nil {
			samples = state.PositiveSamples
		}
		out = append(out, taste.ContextBlend{
			ID: ctx.ID, Kind: ctx.Kind, Influence: ctx.Influence,
			Vector: vector, Samples: samples,
		})
	}
	return out
}

func (e *Engine) sessionRules(sess *Session) *rules.Evaluator {
	active, err := e.Store.ActiveRadioRules(sess.ID, sess.ActiveContextIDs)
	if err != nil || len(active) == 0 {
		return nil
	}
	return rules.New(e.Idx, active, time.Now().UTC())
}

// excludeRuleTargets adds hard-block matches to the session exclude by each
// rule's target key. Cooldown still checks last-played, but only for the
// tracks that key names. Candidate filtering stays in BuildCore.
func (e *Engine) excludeRuleTargets(sess *Session) {
	eval := e.sessionRules(sess)
	if eval == nil {
		return
	}
	for _, rule := range eval.Rules {
		switch rule.Action {
		case "block", "cooldown":
		default:
			continue
		}
		for _, id := range e.Idx.IDsForTarget(rule.TargetType, rule.TargetKey) {
			if rule.Action == "cooldown" && !eval.HardBlocked(id) {
				continue
			}
			e.ExcludeTrack(sess, id)
		}
	}
}

func (e *Engine) learnActiveContexts(sess *Session, vector []float32, weight float64) {
	if weight <= 0 || len(vector) == 0 {
		return
	}
	for _, id := range sess.ActiveContextIDs {
		ctx, err := e.Store.GetTasteContext(id)
		if err != nil || ctx == nil || !ctx.LearningEnabled {
			continue
		}
		state, _ := e.Store.LoadTasteContextState(id)
		current := taste.VectorState{}
		if state != nil {
			current.Vector = index.BytesToFloat32(state.PositiveVector)
			current.Samples = state.PositiveSamples
		}
		updated := taste.ApplyPositive(current, vector, weight, e.Cfg.TasteAlpha)
		_ = e.Store.UpsertTasteContextState(db.TasteContextState{
			ContextID: id, PositiveVector: index.Float32Bytes(updated.Vector),
			EmbeddingDim: len(updated.Vector), PositiveSamples: updated.Samples,
		})
	}
}
