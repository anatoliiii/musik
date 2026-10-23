package queue

import (
	"math"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/rules"
	"github.com/torwin-job/musik/player/internal/taste"
)

// CoreOpts is the single candidate → feature → score → select path.
type CoreOpts struct {
	CurrentID         int64
	Taste             []float32
	Session           []float32
	Daypart           []float32
	Centroids         [][]float32
	Contexts          []taste.ContextBlend
	Exclude           map[int64]bool
	Rules             *rules.Evaluator
	Transitions       map[int64]float64
	Profile           TransitionProfile
	Size              int
	Now               time.Time
	Model             *Model
	Negative          func([]float32) float64
	DaypartName       string
	ContextIDs        []string
	Discover          bool
	Allocation        *ExploreDecision
	LearnedTransition *TransitionModel
}

func (b *Builder) BuildCore(opts CoreOpts) []Item {
	if opts.Size < 1 {
		opts.Size = b.Cfg.QueueSize
	}
	if opts.Size < 1 {
		opts.Size = 6
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	model := DefaultModel()
	if opts.Model != nil {
		model = *opts.Model
	}
	if opts.Profile == "" {
		opts.Profile = ProfileSmooth
	}

	var mood, place, activity []float32
	for _, ctx := range opts.Contexts {
		switch ctx.Kind {
		case "mood":
			mood = ctx.Vector
		case "place":
			place = ctx.Vector
		case "activity":
			activity = ctx.Vector
		}
	}

	var currentVec []float32
	var currentMeta *index.Meta
	if row, ok := b.Idx.RowOf(opts.CurrentID); ok {
		currentVec = b.Idx.Vector(row)
		meta := b.Idx.MetaAt(row)
		currentMeta = &meta
	}

	cands := CollectCandidates(b.Idx, opts.CurrentID, CandidateOpts{
		Taste: opts.Taste, Current: currentVec, Session: opts.Session, Daypart: opts.Daypart,
		Mood: mood, Place: place, Activity: activity, Centroids: opts.Centroids,
		Exclude: opts.Exclude, Rules: opts.Rules, Now: opts.Now,
		Transitions: opts.Transitions, Negative: opts.Negative,
	})

	scored := make([]ScoredCandidate, 0, len(cands))
	for _, cand := range cands {
		meta := b.Idx.MetaAt(cand.Row)
		neg := 0.0
		if opts.Negative != nil {
			neg = opts.Negative(b.Idx.Vector(cand.Row))
		}
		ctxSims := taste.ContextSimilarities(b.Idx.Vector(cand.Row), opts.Contexts)
		downrank := 0.0
		if opts.Rules != nil {
			downrank = opts.Rules.Downrank(meta.ID)
		}
		artistRepeat, albumRepeat, transitionScore := TransitionFeatures(
			currentMeta, meta, float64(cand.Sims.Current), opts.Transitions[meta.ID], opts.Profile, opts.LearnedTransition,
		)
		values := VectorFromInputs(FeatureInputs{
			Meta: meta, Now: opts.Now,
			SimTaste: float64(cand.Sims.Taste), SimCurrent: float64(cand.Sims.Current),
			SimSession: float64(cand.Sims.Session), SimDaypart: float64(cand.Sims.Daypart),
			SimMood: ctxSims["mood"], SimPlace: ctxSims["place"], SimActivity: ctxSims["activity"],
			SimCentroidMax:  float64(cand.Sims.CentroidMax),
			TransitionW:     normTransition(opts.Transitions[meta.ID], opts.Transitions),
			TransitionScore: transitionScore,
			Current:         currentMeta,
			NewBoost:        float64(b.Idx.NewBoost(cand.Row, opts.Now)),
			NegativePenalty: neg,
			ArtistRepeat:    artistRepeat,
			AlbumRepeat:     albumRepeat,
		})
		score := model.Score(values) - 0.35*downrank
		cand.Primary = primarySource(cand.Sources)
		scored = append(scored, ScoredCandidate{
			Candidate: cand, Score: score, Features: values, Downrank: downrank,
		})
	}

	quotas := coreQuotas(opts)
	selected := Select(scored, SelectOpts{
		Size: opts.Size, Quotas: quotas,
		MMRLambda: ActiveSpec.MMRLambda, ArtistLimit: 2, Idx: b.Idx, CurrentID: opts.CurrentID,
	})
	seq := BeamSequence(b.Idx, opts.CurrentID, selected, opts.Size, 6, opts.Profile, opts.Transitions, 0.35, opts.LearnedTransition)
	chosen := seq.Items
	if len(chosen) == 0 {
		chosen = Select(scored, SelectOpts{
			Size: opts.Size, Quotas: quotas, MMRLambda: ActiveSpec.MMRLambda,
			ArtistLimit: 2, Idx: b.Idx, CurrentID: opts.CurrentID,
		})
	}

	out := make([]Item, 0, len(chosen))
	boostIdx := FeatureIndex("new_boost")
	positionIdx := FeatureIndex("queue_position")
	for i, c := range chosen {
		meta := b.Idx.MetaAt(c.Row)
		logged := append([]float64(nil), c.Features...)
		if positionIdx >= 0 && positionIdx < len(logged) {
			logged[positionIdx] = float64(i)
		}
		ctx := FeatureContext{
			Daypart: opts.DaypartName, ContextIDs: opts.ContextIDs, Profile: string(opts.Profile),
			Position: i, DurationSec: meta.Duration,
		}
		if opts.Allocation != nil && opts.Allocation.SampledP > 0 {
			ctx.SampledP = opts.Allocation.SampledP
		}
		if i < len(seq.Explain) {
			if feats, ok := seq.Explain[i]["features"].(map[string]float64); ok {
				ctx.Pairwise = copyFloatMap(feats)
			}
		}
		_, featuresJSON := Snapshot(model.ModelVersion, logged, ctx)
		why := c.Primary
		if i < len(seq.Explain) {
			why = c.Primary + " · sequence"
		}
		newBoost := false
		if boostIdx >= 0 && boostIdx < len(c.Features) {
			newBoost = c.Features[boostIdx] > 0.01
		}
		out = append(out, Item{
			TrackID: meta.ID, Artist: meta.Artist, Title: meta.Title, Album: meta.Album,
			Path: meta.Path, Duration: meta.Duration, Score: c.Score,
			CosineTaste: float64(c.Sims.Taste), CosineCur: float64(c.Sims.Current),
			Explanation: why, Explore: c.Primary != SourceExploit && c.Primary != SourceTransition,
			NewBoost: newBoost, ClusterID: meta.ClusterID,
			Source: c.Primary, FeaturesJSON: featuresJSON,
		})
	}
	return out
}

func copyFloatMap(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func coreQuotas(opts CoreOpts) map[string]Quota {
	if opts.Allocation != nil && opts.Allocation.Quotas != nil {
		return opts.Allocation.Quotas
	}
	if opts.Discover {
		return DiscoverQuotas
	}
	return DefaultQuotas
}

func normTransition(weight float64, all map[int64]float64) float64 {
	if weight <= 0 {
		return 0
	}
	maxW := 0.0
	for _, w := range all {
		if w > maxW {
			maxW = w
		}
	}
	if maxW <= 0 {
		return 0
	}
	return math.Log1p(weight) / math.Log1p(maxW)
}
