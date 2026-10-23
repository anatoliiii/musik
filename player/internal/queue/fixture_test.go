package queue

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
)

type fixtureTrack struct {
	ID              int64     `json:"id"`
	Artist          string    `json:"artist"`
	Album           string    `json:"album"`
	Vector          []float64 `json:"vector"`
	BPM             *float64  `json:"bpm"`
	LUFS            *float64  `json:"lufs"`
	Key             string    `json:"key"`
	DurationSec     float64   `json:"duration_sec"`
	Plays           int       `json:"plays"`
	Finishes        int       `json:"finishes"`
	Partial         int       `json:"partial"`
	EarlySkips      int       `json:"early_skips"`
	Likes           int       `json:"likes"`
	Dislikes        int       `json:"dislikes"`
	Shown           int       `json:"shown"`
	SkipEarly       int       `json:"skip_early"`
	Completed       int       `json:"completed"`
	CreatedAt       string    `json:"created_at"`
	LastPlayedAt    string    `json:"last_played_at"`
	LastFinishedAt  string    `json:"last_finished_at"`
	NegativePenalty float64   `json:"negative_penalty"`
}

type fixtureDoc struct {
	Now         string             `json:"now"`
	Profile     string             `json:"profile"`
	Size        int                `json:"size"`
	Taste       []float64          `json:"taste"`
	Current     fixtureTrack       `json:"current"`
	Transitions map[string]float64 `json:"transitions"`
	Candidates  []fixtureTrack     `json:"candidates"`
	Expected    []fixtureExpect    `json:"expected"`
}

type fixtureExpect struct {
	TrackID  int64     `json:"track_id"`
	Source   string    `json:"source"`
	Sources  []string  `json:"sources"`
	Features []float64 `json:"features"`
}

type describedItem struct {
	TrackID  int64
	Source   string
	Sources  []string
	Score    float64
	Features []float64
}

func TestGoldenFixtureMatchesPythonContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "golden", "ranker_fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc fixtureDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Expected) == 0 {
		t.Fatal("golden fixture has no expected ranking")
	}
	got := rankFixture(doc)
	if len(got) != len(doc.Expected) {
		t.Fatalf("len=%d want %d", len(got), len(doc.Expected))
	}
	for i, item := range got {
		want := doc.Expected[i]
		if item.TrackID != want.TrackID || item.Source != want.Source || !slices.Equal(item.Sources, want.Sources) {
			t.Fatalf("item %d got id=%d source=%s sources=%v want id=%d source=%s sources=%v",
				i, item.TrackID, item.Source, item.Sources, want.TrackID, want.Source, want.Sources)
		}
		if len(item.Features) != len(want.Features) {
			t.Fatalf("track %d feature len %d", item.TrackID, len(item.Features))
		}
		for j, value := range item.Features {
			if math.Abs(value-want.Features[j]) > 1e-6 {
				t.Fatalf("track %d %s got %v want %v", item.TrackID, FeatureOrder[j], value, want.Features[j])
			}
		}
	}
}

func rankFixture(doc fixtureDoc) []describedItem {
	now := parseFixtureTime(doc.Now)
	profile := TransitionProfile(doc.Profile)
	if profile == "" {
		profile = ProfileSmooth
	}
	transitions := map[int64]float64{}
	for id, weight := range doc.Transitions {
		var n int64
		for _, r := range id {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int64(r-'0')
		}
		transitions[n] = weight
	}
	current := fixtureMeta(doc.Current, now)
	model := DefaultModel()
	items := make([]describedItem, 0, len(doc.Candidates))
	for _, track := range doc.Candidates {
		meta := fixtureMeta(track, now)
		simTaste := fixtureCosine(doc.Taste, track.Vector)
		simCurrent := 0.0
		if len(doc.Current.Vector) > 0 {
			simCurrent = fixtureCosine(doc.Current.Vector, track.Vector)
		}
		sims := CandidateSims{Taste: float32(simTaste), Current: float32(simCurrent)}
		sources := labelSources(meta, sims, now)
		artist, album, score := TransitionFeatures(&current, meta, simCurrent, transitions[meta.ID], profile, nil)
		values := VectorFromInputs(FeatureInputs{
			Meta: meta, Now: now,
			SimTaste: simTaste, SimCurrent: simCurrent,
			TransitionW: normTransition(transitions[meta.ID], transitions), TransitionScore: score,
			Current:  &current,
			NewBoost: fixtureBoost(meta, now), NegativePenalty: track.NegativePenalty,
			ArtistRepeat: artist, AlbumRepeat: album,
		})
		items = append(items, describedItem{
			TrackID: meta.ID, Source: primarySource(sources), Sources: sources,
			Score: model.Score(values), Features: values,
		})
	}
	return applyDescribedQuotas(items, doc.Size)
}

func applyDescribedQuotas(items []describedItem, size int) []describedItem {
	if size < 1 {
		size = 6
	}
	quotas := ScaleQuotas(size, nil)
	sorted := append([]describedItem(nil), items...)
	sortDescribed(sorted)
	by := map[string][]describedItem{}
	for _, item := range sorted {
		for _, src := range describedLabels(item) {
			by[src] = append(by[src], item)
		}
	}
	chosen := make([]describedItem, 0, size)
	used := map[int64]bool{}
	counts := map[string]int{}
	for i := len(sourcePriority) - 1; i >= 0; i-- {
		src := sourcePriority[i]
		want := quotas[src].Min
		for _, item := range by[src] {
			if len(chosen) >= size || counts[src] >= want {
				break
			}
			if used[item.TrackID] {
				continue
			}
			taken := item
			taken.Source = src
			chosen = append(chosen, taken)
			used[item.TrackID] = true
			counts[src]++
		}
	}
	for _, item := range sorted {
		if len(chosen) >= size {
			break
		}
		if used[item.TrackID] {
			continue
		}
		slot, open := quotaSlot(scoredFromDescribed(item), counts, quotas)
		if !open {
			continue
		}
		item.Source = slot
		chosen = append(chosen, item)
		used[item.TrackID] = true
		counts[slot]++
	}
	return chosen
}

func describedLabels(item describedItem) []string {
	if len(item.Sources) > 0 {
		return item.Sources
	}
	if item.Source != "" {
		return []string{item.Source}
	}
	return []string{SourceWildcard}
}

func scoredFromDescribed(item describedItem) ScoredCandidate {
	return ScoredCandidate{Candidate: Candidate{
		Sources: append([]string(nil), item.Sources...),
		Primary: item.Source,
	}}
}

func sortDescribed(items []describedItem) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].Score > items[i].Score || (items[j].Score == items[i].Score && items[j].TrackID < items[i].TrackID) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

func fixtureMeta(track fixtureTrack, now time.Time) index.Meta {
	meta := index.Meta{
		ID: track.ID, Artist: track.Artist, Album: track.Album,
		Duration: track.DurationSec, KeyName: track.Key,
		Plays: track.Plays, Finishes: track.Finishes, Partial: track.Partial,
		EarlySkips: track.EarlySkips, Likes: track.Likes, Dislikes: track.Dislikes,
		Shown: track.Shown, SkipEarly: track.SkipEarly, Completed: track.Completed,
		CreatedAt:      parseFixtureTime(track.CreatedAt),
		LastPlayedAt:   parseFixtureTime(track.LastPlayedAt),
		LastFinishedAt: parseFixtureTime(track.LastFinishedAt),
	}
	if track.BPM != nil && *track.BPM > 0 {
		meta.BPM, meta.HasBPM = *track.BPM, true
	}
	if track.LUFS != nil {
		meta.LUFS, meta.HasLUFS = *track.LUFS, true
	}
	_ = now
	return meta
}

func fixtureBoost(meta index.Meta, now time.Time) float64 {
	boost := ActiveSpec.NewBoost
	if meta.SkipEarly >= boost.SkipEarlyCap || meta.CreatedAt.IsZero() {
		return 0
	}
	ageDays := now.Sub(meta.CreatedAt).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	if ageDays > boost.Days || boost.TauDays == 0 || boost.Gamma == 0 {
		return 0
	}
	return boost.Beta * math.Exp(-ageDays/boost.TauDays) * math.Exp(-float64(meta.Shown)/boost.Gamma)
}

func fixtureCosine(a, b []float64) float64 {
	left, right := unit64(a), unit64(b)
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var sum float64
	for i := range left {
		sum += left[i] * right[i]
	}
	return sum
}

func unit64(v []float64) []float64 {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := append([]float64(nil), v...)
	if norm <= 1e-12 {
		return out
	}
	for i := range out {
		out[i] /= norm
	}
	return out
}

func parseFixtureTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, value)
	return t
}
