package playback

import (
	"encoding/json"
	"math"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/rules"
	"github.com/torwin-job/musik/player/internal/taste"
)

func (e *Engine) ExcludeTrack(sess *Session, trackID int64) {
	if trackID == 0 || sess == nil {
		return
	}
	if sess.Exclude == nil {
		sess.Exclude = map[int64]bool{}
	}
	for _, id := range e.Idx.CloneIDs(trackID) {
		sess.Exclude[id] = true
	}
}

func (e *Engine) SeedRadioExclude(sess *Session) {
	ids, err := e.Store.RecentTrackIDs(48, 120)
	if err != nil {
		return
	}
	for _, id := range ids {
		e.ExcludeTrack(sess, id)
	}
}

func (e *Engine) PickStart(sess *Session, seed *int64) int64 {
	var startID int64
	if seed != nil {
		startID = *seed
	} else if e.Discovering() {
		startID = e.Builder.PickRandom(sess.Exclude)
	} else {
		startID = e.pickTasteStart(sess)
	}
	if _, ok := e.Idx.RowOf(startID); !ok && e.Idx.Size() > 0 {
		startID = e.Idx.MetaAt(0).ID
	}
	return startID
}

func (e *Engine) pickTasteStart(sess *Session) int64 {
	n := e.Idx.Size()
	if n == 0 {
		return 0
	}
	recent := map[int64]bool{}
	if ids, err := e.Store.RecentTrackIDs(36, 50); err == nil {
		for _, id := range ids {
			recent[id] = true
		}
	}
	for id := range sess.Exclude {
		recent[id] = true
	}

	type pair struct {
		row int
		sim float32
	}
	if e.Builder.RandomFloat64() < 0.12 && n > 8 {
		if id := e.Builder.PickRandom(recent); id != 0 {
			return id
		}
	}

	k := 20
	top := e.Idx.TopK(e.tasteForQueue(sess), k, nil)
	if len(top) == 0 {
		return e.Builder.PickRandom(recent)
	}
	candidates := make([]pair, 0, len(top))
	for _, p := range top {
		id := e.Idx.MetaAt(p.Row).ID
		sim := p.Score
		if recent[id] {
			sim -= 0.35
		}
		candidates = append(candidates, pair{p.Row, sim})
	}
	allRecent := true
	for _, p := range candidates {
		if !recent[e.Idx.MetaAt(p.row).ID] {
			allRecent = false
			break
		}
	}
	if allRecent {
		candidates = candidates[:0]
		for _, p := range top {
			candidates = append(candidates, pair{p.Row, p.Score})
		}
	}

	const temp = 0.08
	weights := make([]float64, len(candidates))
	var sum float64
	maxSim := candidates[0].sim
	for i, p := range candidates {
		w := math.Exp(float64(p.sim-maxSim) / temp)
		weights[i] = w
		sum += w
	}
	if sum <= 0 {
		return e.Idx.MetaAt(candidates[0].row).ID
	}
	r := e.Builder.RandomFloat64() * sum
	for i, w := range weights {
		r -= w
		if r <= 0 {
			return e.Idx.MetaAt(candidates[i].row).ID
		}
	}
	return e.Idx.MetaAt(candidates[len(candidates)-1].row).ID
}

func (e *Engine) tasteForQueue(sess *Session) []float32 {
	dp := db.DayPart(time.Now().Hour())
	if e.TasteState != nil {
		if sess == nil {
			return e.TasteState.Effective(dp, nil, taste.DefaultDaypartMinimum)
		}
		base := e.TasteState.Effective(dp, &sess.TasteState, taste.DefaultDaypartMinimum)
		return taste.BlendContexts(base, e.contextBlends(sess.ActiveContextIDs))
	}
	return e.Taste.Get()
}

func (e *Engine) transitionsFrom(currentID int64) map[int64]float64 {
	e.transMu.RLock()
	defer e.transMu.RUnlock()
	if e.transitions == nil {
		return nil
	}
	src := e.transitions[currentID]
	if len(src) == 0 {
		return nil
	}
	out := make(map[int64]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// HideBlocked drops hard-blocked tracks from live radio and generated mixes.
func (e *Engine) HideBlocked() {
	e.sessionsMu.RLock()
	list := make([]*Session, 0, len(e.sessions))
	for _, sess := range e.sessions {
		list = append(list, sess)
	}
	e.sessionsMu.RUnlock()
	for _, sess := range list {
		sess.Lock()
		e.dropBlockedLocked(sess)
		sess.Unlock()
	}
}

func (e *Engine) dropBlockedLocked(sess *Session) {
	if sess == nil {
		return
	}
	eval := e.sessionRules(sess)
	if eval == nil {
		return
	}
	switch sess.Mode {
	case "radio", "session", "share":
		e.dropBlockedRadioLocked(sess, eval)
	default:
		if GeneratedMixKind(sess.PlaylistKind) && len(sess.DailyIDs) > 0 {
			e.dropBlockedFixedLocked(sess, eval)
		}
	}
}

func (e *Engine) dropBlockedRadioLocked(sess *Session, eval *rules.Evaluator) {
	blockedNow := eval.HardBlocked(sess.Current)
	e.RefreshQueue(sess, sess.Current, "block")
	if !blockedNow {
		return
	}
	if len(sess.Queue) == 0 {
		sess.Current = 0
		sess.CurrentItem = queue.Item{}
		e.persistLocked(sess)
		return
	}
	next := sess.Queue[0]
	sess.Queue = append([]queue.Item(nil), sess.Queue[1:]...)
	sess.Prev = sess.Current
	sess.Current = next.TrackID
	sess.CurrentItem = next
	e.ExcludeTrack(sess, next.TrackID)
	e.createCurrentImpression(sess, "block", "block")
	e.persistLocked(sess)
	e.warm(sess)
}

func (e *Engine) dropBlockedFixedLocked(sess *Session, eval *rules.Evaluator) {
	filtered := eval.FilterIDs(sess.DailyIDs)
	if len(filtered) == len(sess.DailyIDs) {
		return
	}
	cur := sess.Current
	sess.DailyIDs = filtered
	if len(filtered) == 0 {
		sess.DailyPos = 0
		sess.Current = 0
		sess.CurrentItem = queue.Item{}
		sess.Queue = nil
		e.persistLocked(sess)
		return
	}
	pos := 0
	found := false
	for i, id := range filtered {
		if id == cur {
			pos = i
			found = true
			break
		}
	}
	if !found {
		if sess.DailyPos >= len(filtered) {
			pos = len(filtered) - 1
		} else if sess.DailyPos > 0 {
			pos = sess.DailyPos
			if pos >= len(filtered) {
				pos = len(filtered) - 1
			}
		}
		sess.Current = filtered[pos]
		sess.CurrentItem = queue.Item{}
	}
	sess.DailyPos = pos
	e.RebuildFixedQueue(sess)
}

func (e *Engine) RefreshQueue(sess *Session, currentID int64, reason string) {
	started := time.Now()
	defer func() { e.observe("queue_build", time.Since(started)) }()
	previous := append([]queue.Item(nil), sess.Queue...)
	e.applyHardBlocks(sess)
	size := e.Cfg.QueueSize
	if size < 1 {
		size = 6
	}
	dp := db.DayPart(time.Now().Hour())
	prefs := e.radioPrefs()
	decision := queue.DecideExplore(queue.ExploreInput{
		Arms: e.exploreArms(), Counts: e.exploreCounts(), Size: size,
		Lo: prefs.ExploreLo, Hi: prefs.ExploreHi, Discover: e.Discovering(),
		Now: time.Now().UTC(), Rng: e.Builder.ForkRNG(),
	})
	model := e.Ranker
	built := e.Builder.BuildCore(queue.CoreOpts{
		CurrentID: currentID, Taste: e.tasteForQueue(sess),
		Session: sess.TasteState.Positive.Vector, Daypart: e.daypartVector(dp),
		Centroids: e.tasteCentroidVecs(), Contexts: e.contextBlends(sess.ActiveContextIDs),
		Exclude: sess.Exclude, Rules: e.sessionRules(sess),
		Transitions: e.transitionsFrom(currentID),
		Profile:     queue.TransitionProfile(sess.TransitionProfile),
		Size:        size, Model: &model,
		Negative: func(vector []float32) float64 {
			return e.TasteState.NegativePenalty(vector, &sess.TasteState)
		},
		DaypartName: dp, ContextIDs: sess.ActiveContextIDs,
		Discover: e.Discovering(), Allocation: &decision,
	})
	if reason != "" && sess.Mode != "share" {
		requestID := db.NewID()
		previousByTrack := make(map[int64]queue.Item, len(previous))
		for _, item := range previous {
			if item.ImpressionID != "" {
				previousByTrack[item.TrackID] = item
			}
		}
		kept := map[string]bool{}
		newImpressions := make([]db.RecommendationImpression, 0, len(built))
		for position := range built {
			item := &built[position]
			if existing, ok := previousByTrack[item.TrackID]; ok {
				item.ImpressionID = existing.ImpressionID
				item.RequestID = existing.RequestID
				item.Source = existing.Source
				item.FeaturesJSON = existing.FeaturesJSON
				kept[existing.ImpressionID] = true
				continue
			}
			item.ImpressionID = db.NewID()
			item.RequestID = requestID
			newImpressions = append(newImpressions, db.RecommendationImpression{
				ImpressionID: item.ImpressionID, RequestID: requestID,
				SessionID: sess.ID, TrackID: item.TrackID, Position: position,
				Score: item.Score, CosineTaste: item.CosineTaste,
				CosineCurrent: item.CosineCur, Explore: item.Explore,
				NewBoost: item.NewBoost, Maturity: e.Maturity(), Mode: sess.Mode,
				Source: item.Source, FeaturesJSON: item.FeaturesJSON,
			})
		}
		var superseded []string
		for _, item := range previous {
			if item.ImpressionID != "" && !kept[item.ImpressionID] {
				superseded = append(superseded, item.ImpressionID)
			}
		}
		policy, _ := json.Marshal(map[string]any{
			"schema_version":  queue.PolicySchemaVersion,
			"explore_enabled": decision.Enabled,
			"explore_share":   decision.ExploreShare,
			"sampled_p":       decision.SampledP,
			"source_p":        decision.SourceP,
			"allocation":      decision.SourceShare,
			"bounds":          []float64{decision.Bounds[0], decision.Bounds[1]},
			"reason":          decision.Reason,
		})
		_ = e.Store.CreateRecommendationRequest(db.RecommendationRequest{
			RequestID: requestID, SessionID: sess.ID, Reason: reason,
			PolicyVersion: "queue-core-v1", ModelVersion: model.ModelVersion,
			CandidateCount: e.Idx.Size(),
			LatencyMS:      float64(time.Since(started).Microseconds()) / 1000,
			PolicyJSON:     string(policy),
		}, newImpressions, superseded)
		_ = e.Store.AttachRequestContexts(requestID, sess.ActiveContextIDs)
	}
	sess.Queue = built
	sess.LastQueueAt = time.Now()
	sess.UpdatedAt = time.Now()
	e.persistLocked(sess)
	e.warm(sess)
}

func (e *Engine) createCurrentImpression(sess *Session, source, reason string) {
	if sess == nil || sess.Current == 0 || source == "" || sess.Mode == "share" {
		return
	}
	item := queue.Item{TrackID: sess.Current, ImpressionID: db.NewID(), RequestID: db.NewID(), Source: source}
	if row, ok := e.Idx.RowOf(sess.Current); ok {
		meta := e.Idx.MetaAt(row)
		item.Artist, item.Title, item.Album = meta.Artist, meta.Title, meta.Album
		item.Path, item.Duration, item.ClusterID = meta.Path, meta.Duration, meta.ClusterID
	}
	item.FeaturesJSON = `{"schema_version":1,"start":true}`
	err := e.Store.CreateRecommendationRequest(db.RecommendationRequest{
		RequestID: item.RequestID, SessionID: sess.ID, Reason: reason,
		PolicyVersion: "queue-core-v1", ModelVersion: e.Ranker.ModelVersion,
		CandidateCount: e.Idx.Size(),
	}, []db.RecommendationImpression{{
		ImpressionID: item.ImpressionID, RequestID: item.RequestID,
		SessionID: sess.ID, TrackID: sess.Current, Position: 0,
		Maturity: e.Maturity(), Mode: sess.Mode, Source: source,
		FeaturesJSON: item.FeaturesJSON,
	}}, nil)
	if err == nil {
		sess.CurrentItem = item
	}
}

func (e *Engine) RebuildFixedQueue(sess *Session) {
	sess.Queue = nil
	limit := e.Cfg.QueueSize
	if IsFixedMode(sess.Mode) {
		limit = 50
	}
	for i := sess.DailyPos + 1; i < len(sess.DailyIDs) && len(sess.Queue) < limit; i++ {
		id := sess.DailyIDs[i]
		row, ok := e.Idx.RowOf(id)
		if !ok {
			continue
		}
		m := e.Idx.MetaAt(row)
		label := sess.PlaylistName
		if label == "" {
			label = "плейлист"
		}
		sess.Queue = append(sess.Queue, queue.Item{
			TrackID: m.ID, Artist: m.Artist, Title: m.Title, Album: m.Album,
			Path: m.Path, Duration: m.Duration, Explanation: label,
		})
	}
	e.persistLocked(sess)
	e.warm(sess)
}

func (e *Engine) Advance(sess *Session) int64 {
	nextID := int64(0)
	if len(sess.DailyIDs) > 0 {
		sess.DailyPos++
		if sess.DailyPos < len(sess.DailyIDs) {
			nextID = sess.DailyIDs[sess.DailyPos]
			if nextID != sess.Current {
				e.rememberBack(sess)
			}
			sess.Prev = sess.Current
			sess.Current = nextID
			sess.CurrentItem = queue.Item{}
			e.ExcludeTrack(sess, nextID)
			e.RebuildFixedQueue(sess)
			sess.UpdatedAt = time.Now()
			e.persistLocked(sess)
			e.warm(sess)
			return nextID
		}
		if IsFixedMode(sess.Mode) {
			sess.DailyPos = len(sess.DailyIDs) - 1
			sess.Queue = nil
			sess.UpdatedAt = time.Now()
			e.persistLocked(sess)
			return 0
		}
		sess.DailyIDs = nil
		sess.Mode = "radio"
	}
	if len(sess.Queue) > 0 {
		sess.CurrentItem = sess.Queue[0]
		nextID = sess.CurrentItem.TrackID
		sess.Queue = sess.Queue[1:]
	} else if sess.Mode == "radio" || sess.Mode == "session" || sess.Mode == "share" {
		nextID = e.Builder.PickRandom(sess.Exclude)
	}
	if nextID != 0 && nextID != sess.Current {
		e.rememberBack(sess)
	}
	sess.Prev = sess.Current
	if nextID != 0 {
		sess.Current = nextID
		e.ExcludeTrack(sess, nextID)
		if sess.CurrentItem.TrackID != nextID {
			e.createCurrentImpression(sess, "refill", "refill")
		}
		if len(sess.Queue) < e.Cfg.QueueSize/2 {
			e.RefreshQueue(sess, nextID, "refill")
		} else {
			sess.UpdatedAt = time.Now()
			e.persistLocked(sess)
			e.warm(sess)
		}
	} else {
		sess.UpdatedAt = time.Now()
		e.persistLocked(sess)
	}
	return nextID
}

func (e *Engine) PersistTaste(trackVec []float32, signed, alpha float64) {
	if signed <= 0 {
		return
	}
	blob := index.Float32Bytes(e.Taste.Get())
	_ = e.Store.SaveProfile("global", blob)
	_ = e.Store.PruneProfiles("global", 50)

	dp := db.DayPart(time.Now().Hour())
	defer e.persistTasteState(dp)
	prev, err := e.Store.LatestProfile(dp)
	if err != nil || len(prev) == 0 || len(trackVec) == 0 {
		_ = e.Store.SaveProfile(dp, blob)
		_ = e.Store.PruneProfiles(dp, 50)
		return
	}
	v := index.BytesToFloat32(prev)
	if len(v) != len(trackVec) {
		_ = e.Store.SaveProfile(dp, blob)
		_ = e.Store.PruneProfiles(dp, 50)
		return
	}
	a := float32(alpha)
	if a <= 0 {
		a = 0.1
	}
	w := float32(signed)
	for i := range v {
		v[i] = (1-a)*v[i] + a*w*trackVec[i]
	}
	index.Normalize(v)
	_ = e.Store.SaveProfile(dp, index.Float32Bytes(v))
	_ = e.Store.PruneProfiles(dp, 50)
}

func (e *Engine) persistTasteState(daypart string) {
	if e.TasteState == nil {
		return
	}
	state := e.TasteState.Daypart(daypart)
	if len(state.Vector) > 0 {
		_ = e.Store.UpsertTasteState(db.TasteStateRow{
			Key:            "daypart:" + daypart,
			PositiveVector: index.Float32Bytes(state.Vector),
			EmbeddingDim:   len(state.Vector), PositiveSamples: state.Samples,
		})
	}
	negatives := e.TasteState.PersistentNegatives()
	if len(negatives) > 0 {
		payload, _ := json.Marshal(negatives)
		_ = e.Store.UpsertTasteState(db.TasteStateRow{
			Key: "persistent_negative", NegativeSamples: len(negatives),
			NegativePrototypesJSON: string(payload),
		})
	}
	e.rebuildTasteCentroids()
}

func (e *Engine) rebuildTasteCentroids() {
	rows, err := e.Store.PositiveTasteSamples(400)
	if err != nil || len(rows) == 0 {
		return
	}
	samples := make([]taste.Sample, 0, len(rows))
	for _, row := range rows {
		indexRow, ok := e.Idx.RowOf(row.TrackID)
		if !ok {
			continue
		}
		samples = append(samples, taste.Sample{
			TrackID: row.TrackID, Vector: e.Idx.Vector(indexRow),
			Weight: row.Weight, At: row.At,
		})
	}
	if len(samples) == 0 {
		return
	}
	var warm []taste.Centroid
	if stored, err := e.Store.LoadTasteCentroids(taste.CentroidAlgorithmVersion); err == nil {
		for _, row := range stored {
			warm = append(warm, taste.Centroid{
				Vector:      index.BytesToFloat32(row.Vector),
				Mass:        row.Mass,
				SampleCount: row.SampleCount,
			})
		}
	}
	centroids := taste.WeightedSphericalCentroids(samples, taste.CentroidOptions{
		WarmStart: warm, Seed: 1,
	})
	out := make([]db.TasteCentroidRow, 0, len(centroids))
	for i, centroid := range centroids {
		out = append(out, db.TasteCentroidRow{
			Index: i, Vector: index.Float32Bytes(centroid.Vector),
			EmbeddingDim: len(centroid.Vector), Mass: centroid.Mass,
			SampleCount: centroid.SampleCount, AlgorithmVersion: taste.CentroidAlgorithmVersion,
		})
	}
	_ = e.Store.ReplaceTasteCentroids(out)
}
