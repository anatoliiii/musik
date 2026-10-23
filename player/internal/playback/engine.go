package playback

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
)

type Engine struct {
	Cfg             config.Config
	Store           *db.Store
	Idx             *index.Index
	Taste           *taste.Profile
	TasteState      *taste.State
	Builder         *queue.Builder
	Ranker          queue.Model
	TransitionModel queue.TransitionModel

	Enqueue func(func())
	Flush   func()
	Observe func(string, time.Duration)
	Warm    func([]int64)

	sessionsMu  sync.RWMutex
	sessions    map[string]*Session
	transMu     sync.RWMutex
	transitions map[int64]map[int64]float64
}

func New(cfg config.Config, store *db.Store, idx *index.Index, tp *taste.Profile, builder *queue.Builder) *Engine {
	engine := &Engine{
		Cfg: cfg, Store: store, Idx: idx, Taste: tp, TasteState: taste.NewState(tp), Builder: builder,
		Ranker:          queue.LoadRuntimeModel(cfg.RankerPath),
		TransitionModel: queue.LoadRuntimeTransition(queue.TransitionPath(cfg.RankerPath)),
		sessions:        map[string]*Session{},
	}
	if rows, err := store.LoadTasteStates(); err == nil {
		for _, row := range rows {
			switch {
			case row.Key == "persistent_negative":
				var prototypes []taste.NegativePrototype
				if json.Unmarshal([]byte(row.NegativePrototypesJSON), &prototypes) == nil {
					engine.TasteState.RestorePersistentNegatives(prototypes)
				}
			case len(row.Key) > len("daypart:") && row.Key[:len("daypart:")] == "daypart:":
				engine.TasteState.RestoreDaypart(row.Key[len("daypart:"):], taste.VectorState{
					Vector: index.BytesToFloat32(row.PositiveVector), Samples: row.PositiveSamples,
				})
			}
		}
	}
	return engine
}

func (e *Engine) enqueue(work func()) {
	if work == nil {
		return
	}
	if e.Enqueue != nil {
		e.Enqueue(work)
		return
	}
	work()
}

func (e *Engine) flush() {
	if e.Flush != nil {
		e.Flush()
	}
}

func (e *Engine) observe(op string, d time.Duration) {
	if e.Observe != nil {
		e.Observe(op, d)
	}
}

func (e *Engine) warm(sess *Session) {
	if e.Warm == nil {
		return
	}
	ids := warmTrackIDs(sess)
	if len(ids) == 0 {
		return
	}
	e.Warm(ids)
}

func warmTrackIDs(sess *Session) []int64 {
	if sess == nil {
		return nil
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(sess.Current)
	for i, item := range sess.Queue {
		if i >= 6 {
			break
		}
		add(item.TrackID)
	}
	if len(sess.DailyIDs) > 0 {
		for i := sess.DailyPos; i < len(sess.DailyIDs) && len(ids) < 7; i++ {
			add(sess.DailyIDs[i])
		}
	}
	return ids
}

func (e *Engine) Maturity() string {
	return e.Taste.Maturity(e.Cfg.ProfileFormingAt, e.Cfg.ProfileReadyAt)
}

func (e *Engine) Discovering() bool {
	return e.Maturity() == taste.StatusDiscovering
}

func (e *Engine) Explore() float64 {
	return e.Taste.EffectiveExplore(e.Cfg.ExploreRatio, e.Cfg.DiscoverExploreRatio,
		e.Cfg.ProfileFormingAt, e.Cfg.ProfileReadyAt)
}

func (e *Engine) SessionCount() int {
	e.sessionsMu.RLock()
	defer e.sessionsMu.RUnlock()
	return len(e.sessions)
}

func (e *Engine) ReloadTransitions() {
	g, err := e.Store.LoadTransitionGraph()
	if err != nil {
		return
	}
	e.transMu.Lock()
	e.transitions = g
	e.transMu.Unlock()
}

func (e *Engine) ReloadRanker() {
	e.Ranker = queue.LoadRuntimeModel(e.Cfg.RankerPath)
	e.TransitionModel = queue.LoadRuntimeTransition(queue.TransitionPath(e.Cfg.RankerPath))
}

func (e *Engine) learnedTransition() *queue.TransitionModel {
	if !e.TransitionModel.Ready() {
		return nil
	}
	model := e.TransitionModel
	return &model
}

func (e *Engine) radioPrefs() db.RadioPrefs {
	prefs, err := e.Store.LoadRadioPrefs()
	if err != nil {
		lo, hi := queue.ClampExploreBounds(e.Cfg.ExploreLo, e.Cfg.ExploreHi)
		return db.RadioPrefs{ExploreLo: lo, ExploreHi: hi}
	}
	prefs.ExploreLo, prefs.ExploreHi = queue.ClampExploreBounds(prefs.ExploreLo, prefs.ExploreHi)
	return prefs
}

func (e *Engine) exploreArms() map[string]queue.Arm {
	rows, err := e.Store.LoadExploreArms()
	if err != nil {
		return nil
	}
	out := map[string]queue.Arm{}
	for _, row := range rows {
		out[row.ArmKey] = queue.Arm{
			Key: row.ArmKey, Kind: row.ArmKind, Alpha: row.Alpha, Beta: row.Beta,
			Successes: row.Successes, Failures: row.Failures, LastDecay: row.LastDecayAt,
		}
	}
	return out
}

func (e *Engine) exploreCounts() map[string]int {
	counts, err := e.Store.ExploreSourceOutcomeCounts()
	if err != nil {
		return nil
	}
	return counts
}

func (e *Engine) daypartVector(daypart string) []float32 {
	if e.TasteState == nil {
		return nil
	}
	state := e.TasteState.Daypart(daypart)
	if state.Samples < taste.DefaultDaypartMinimum {
		return nil
	}
	return state.Vector
}

func (e *Engine) tasteCentroidVecs() [][]float32 {
	rows, err := e.Store.LoadTasteCentroids(taste.CentroidAlgorithmVersion)
	if err != nil {
		return nil
	}
	out := make([][]float32, 0, len(rows))
	for _, row := range rows {
		vec := index.BytesToFloat32(row.Vector)
		if len(vec) == 0 {
			continue
		}
		out = append(out, vec)
	}
	return out
}

func (e *Engine) ObserveExploreOutcome(source, outcome string, played bool) {
	if !played || !queue.AllowedBanditOutcome(outcome) || source == "manual" || source == "" {
		return
	}
	success := queue.BanditSuccess(outcome)
	if queue.IsExploreSource(source) {
		_ = e.Store.UpdateExploreArm(source, "source", success)
		_ = e.Store.UpdateExploreArm(queue.ArmAggregate, "aggregate", success)
		return
	}
	if queue.IsExploitSource(source) {
		_ = e.Store.UpdateExploreArm(queue.ArmAggregate, "aggregate", !success)
	}
}

func (e *Engine) BumpTransitionMem(from, to int64, w float64) {
	if from == 0 || to == 0 {
		return
	}
	e.transMu.Lock()
	defer e.transMu.Unlock()
	if e.transitions == nil {
		e.transitions = map[int64]map[int64]float64{}
	}
	m := e.transitions[from]
	if m == nil {
		m = map[int64]float64{}
		e.transitions[from] = m
	}
	m[to] += w
}
