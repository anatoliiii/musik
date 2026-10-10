package queue

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
)

const FeatureSchemaVersion = 1

var FeatureOrder = []string{
	"sim_taste",
	"sim_current",
	"sim_session",
	"sim_daypart",
	"sim_mood",
	"sim_place",
	"sim_activity",
	"sim_centroid_max",
	"transition_weight",
	"transition_score",
	"bpm_distance",
	"bpm_missing",
	"lufs_diff",
	"lufs_missing",
	"key_compat",
	"energy_diff",
	"artist_repeat",
	"album_repeat",
	"new_boost",
	"source_exploit",
	"source_transition",
	"source_explore_adjacent",
	"source_resurface",
	"source_new_in_library",
	"source_wildcard",
	"plays_log",
	"early_skip_rate",
	"hours_since_played",
	"never_played",
	"negative_penalty",
}

type FeatureContext struct {
	Daypart    string   `json:"daypart,omitempty"`
	ContextIDs []string `json:"context_ids,omitempty"`
	Profile    string   `json:"profile,omitempty"`
}

type FeatureSnapshot struct {
	SchemaVersion int            `json:"schema_version"`
	ModelVersion  string         `json:"model_version"`
	Names         []string       `json:"names"`
	Values        []float64      `json:"values"`
	Context       FeatureContext `json:"context"`
}

type FeatureInputs struct {
	Meta            index.Meta
	Now             time.Time
	SimTaste        float64
	SimCurrent      float64
	SimSession      float64
	SimDaypart      float64
	SimMood         float64
	SimPlace        float64
	SimActivity     float64
	SimCentroidMax  float64
	TransitionW     float64
	TransitionScore float64
	Current         *index.Meta
	Sources         []string
	NewBoost        float64
	NegativePenalty float64
	ArtistRepeat    float64
	AlbumRepeat     float64
}

func VectorFromInputs(in FeatureInputs) []float64 {
	bpmDist, bpmMissing := bpmDistance(in.Current, in.Meta)
	lufsDiff, lufsMissing := lufsDistance(in.Current, in.Meta)
	hours, never := recency(in.Meta, in.Now)
	earlyRate := 0.0
	if in.Meta.Plays > 0 {
		earlyRate = float64(in.Meta.EarlySkips) / float64(in.Meta.Plays)
	}
	src := sourceFlags(in.Sources)
	return []float64{
		in.SimTaste,
		in.SimCurrent,
		in.SimSession,
		in.SimDaypart,
		in.SimMood,
		in.SimPlace,
		in.SimActivity,
		in.SimCentroidMax,
		in.TransitionW,
		in.TransitionScore,
		bpmDist,
		bpmMissing,
		lufsDiff,
		lufsMissing,
		keyCompat(in.Current, in.Meta),
		energyDiff(in.Current, in.Meta),
		in.ArtistRepeat,
		in.AlbumRepeat,
		in.NewBoost,
		src["exploit"],
		src["transition"],
		src["explore_adjacent"],
		src["resurface"],
		src["new_in_library"],
		src["wildcard"],
		math.Log1p(float64(in.Meta.Plays)),
		earlyRate,
		hours,
		never,
		in.NegativePenalty,
	}
}

func Snapshot(modelVersion string, values []float64, ctx FeatureContext) (FeatureSnapshot, string) {
	snap := FeatureSnapshot{
		SchemaVersion: FeatureSchemaVersion,
		ModelVersion:  modelVersion,
		Names:         append([]string(nil), FeatureOrder...),
		Values:        append([]float64(nil), values...),
		Context:       ctx,
	}
	raw, _ := json.Marshal(snap)
	return snap, string(raw)
}

func sourceFlags(sources []string) map[string]float64 {
	out := map[string]float64{
		"exploit": 0, "transition": 0, "explore_adjacent": 0,
		"resurface": 0, "new_in_library": 0, "wildcard": 0,
	}
	for _, src := range sources {
		if _, ok := out[src]; ok {
			out[src] = 1
		}
	}
	return out
}

func bpmDistance(cur *index.Meta, next index.Meta) (float64, float64) {
	if cur == nil || !cur.HasBPM || !next.HasBPM || cur.BPM <= 0 || next.BPM <= 0 {
		return 0, 1
	}
	a, b := cur.BPM, next.BPM
	if a > b {
		a, b = b, a
	}
	rel := math.Abs(b-a) / b
	if almostMultiple(a, b, 2) {
		rel = math.Min(rel, math.Abs(b-2*a)/(2*a))
	}
	return clamp01(rel), 0
}

func almostMultiple(slow, fast, factor float64) bool {
	return math.Abs(fast-factor*slow)/math.Max(fast, 1) < 0.08
}

func lufsDistance(cur *index.Meta, next index.Meta) (float64, float64) {
	if cur == nil || !cur.HasLUFS || !next.HasLUFS {
		return 0, 1
	}
	return math.Abs(cur.LUFS-next.LUFS) / 20, 0
}

func energyDiff(cur *index.Meta, next index.Meta) float64 {
	if cur == nil || !cur.HasLUFS || !next.HasLUFS {
		return 0
	}
	return (next.LUFS - cur.LUFS) / 10
}

func keyCompat(cur *index.Meta, next index.Meta) float64 {
	if cur == nil {
		return 0
	}
	a, b := camelot(cur.KeyName), camelot(next.KeyName)
	if a == 0 || b == 0 {
		return 0
	}
	if a == b {
		return 1
	}
	if neighbors(a, b) {
		return 0.7
	}
	return 0.15
}

func camelot(key string) int {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return 0
	}
	// Accept "8A", "8B", or "C# minor"-style leftovers by hashing the first number.
	n := 0
	for _, r := range key {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		} else if n > 0 {
			break
		}
	}
	if n < 1 || n > 12 {
		return 0
	}
	mode := 0
	if strings.Contains(key, "B") || strings.Contains(strings.ToLower(key), "maj") {
		mode = 12
	}
	return n + mode
}

func neighbors(a, b int) bool {
	modeA, modeB := a > 12, b > 12
	na, nb := a, b
	if na > 12 {
		na -= 12
	}
	if nb > 12 {
		nb -= 12
	}
	if modeA == modeB {
		return absInt(na-nb)%12 == 1 || absInt(na-nb) == 11
	}
	return na == nb
}

func recency(meta index.Meta, now time.Time) (float64, float64) {
	if meta.LastPlayedAt.IsZero() {
		return 0, 1
	}
	hours := now.Sub(meta.LastPlayedAt).Hours()
	if hours < 0 {
		hours = 0
	}
	return math.Min(hours/720, 4), 0
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
