package queue

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
)

// FeatureSchemaVersion is the active impression-feature contract (v3).
//
// History — training drops snapshots with a lower schema_version:
//   - v2: likes, finish/partial rates, impression fatigue, hours since finish, duration, queue position
//   - v3: impression_fatigue = log1p(shows − radio_completions); a library play does not clear
//     fatigue from a shown-but-not-finished radio impression
//
// Source one-hots stay on the impression as quota policy, not model features.
const FeatureSchemaVersion = 3

// FeatureOrder is filled from ranker_spec.json so Go and Python share one list.
var FeatureOrder []string

type FeatureContext struct {
	Daypart     string             `json:"daypart,omitempty"`
	ContextIDs  []string           `json:"context_ids,omitempty"`
	Profile     string             `json:"profile,omitempty"`
	Position    int                `json:"position"`
	DurationSec float64            `json:"duration_sec,omitempty"`
	SampledP    float64            `json:"sampled_p,omitempty"`
	Pairwise    map[string]float64 `json:"pairwise,omitempty"`
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
	NewBoost        float64
	NegativePenalty float64
	ArtistRepeat    float64
	AlbumRepeat     float64
	// QueuePosition is filled after a track is placed. Scoring leaves it at 0
	// so the slot itself is not a reason to pick the track.
	QueuePosition float64
}

func VectorFromInputs(in FeatureInputs) []float64 {
	bpmDist, bpmMissing := bpmDistance(in.Current, in.Meta)
	lufsDiff, lufsMissing := lufsDistance(in.Current, in.Meta)
	hours, never := recency(in.Meta, in.Now)
	earlyRate := 0.0
	if in.Meta.Plays > 0 {
		earlyRate = float64(in.Meta.EarlySkips) / float64(in.Meta.Plays)
	}
	finishedHours, _ := cappedHours(in.Meta.LastFinishedAt, in.Now)
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
		math.Log1p(float64(in.Meta.Plays)),
		earlyRate,
		hours,
		never,
		in.NegativePenalty,
		math.Log1p(float64(in.Meta.Likes)),
		math.Log1p(float64(in.Meta.Dislikes)),
		countRate(in.Meta.Finishes, in.Meta.Plays),
		countRate(in.Meta.Partial, in.Meta.Plays),
		impressionFatigue(in.Meta.Shown, in.Meta.Completed),
		finishedHours,
		in.Meta.Duration / 60,
		in.QueuePosition,
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
	hours, missing := cappedHours(meta.LastPlayedAt, now)
	if missing {
		return 0, 1
	}
	return hours, 0
}

// cappedHours scales an age into the same unit as hours_since_played:
// hours/720, capped at 4. A zero timestamp is missing.
func cappedHours(at, now time.Time) (float64, bool) {
	if at.IsZero() {
		return 0, true
	}
	hours := now.Sub(at).Hours()
	if hours < 0 {
		hours = 0
	}
	return math.Min(hours/720, 4), false
}

func countRate(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// impressionFatigue is how often a recommendation was shown without a radio
// completion. Plays from the library do not close the gap.
func impressionFatigue(shown, completed int) float64 {
	gap := shown - completed
	if gap <= 0 {
		return 0
	}
	return math.Log1p(float64(gap))
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
