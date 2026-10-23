package queue

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/rules"
	"github.com/torwin-job/musik/player/internal/taste"
)

func coreIndex(t *testing.T) *index.Index {
	t.Helper()
	idx := index.New(config.Config{NewTrackDays: 30, NewBoostTauDays: 14})
	rows := make([]db.TrackRow, 0, 18)
	for id := int64(1); id <= 18; id++ {
		artist := "Near"
		if id > 8 {
			artist = "Far"
		}
		vec := []float32{1, 0}
		if id > 8 {
			vec = []float32{0, 1}
		}
		rows = append(rows, db.TrackRow{
			ID: id, Artist: artist, Title: "Track", Album: "Album",
			Path: "/x.flac", Duration: 180, Year: 2010,
			BPM: 100 + float64(id), HasBPM: true, LUFS: -12, HasLUFS: true,
			Embedding: index.Float32Bytes(vec), Dim: 2,
		})
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestDefaultModelMatchesFeatureOrder(t *testing.T) {
	model := DefaultModel()
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(model.FeatureOrder, FeatureOrder) {
		t.Fatal("embedded model feature_order drifted")
	}
	if model.ModelVersion != DefaultModelVersion || model.SchemaVersion != FeatureSchemaVersion {
		t.Fatalf("model %s schema %d", model.ModelVersion, model.SchemaVersion)
	}
}

func TestModelRejectsSchemaMismatch(t *testing.T) {
	model := DefaultModel()
	model.SchemaVersion = 99
	if err := model.Validate(); err == nil {
		t.Fatal("expected schema mismatch")
	}
}

func TestScaleQuotasQueueSizeSix(t *testing.T) {
	q := ScaleQuotas(6, nil)
	if q[SourceExploit].Min != 2 {
		t.Fatalf("exploit min=%d", q[SourceExploit].Min)
	}
	if q[SourceExploreAdjacent].Min != 1 {
		t.Fatalf("explore min=%d", q[SourceExploreAdjacent].Min)
	}
	if q[SourceResurface].Min != 1 || q[SourceResurface].Max != 1 {
		t.Fatalf("resurface quota %+v", q[SourceResurface])
	}
	if DiscoverQuotas[SourceResurface].Min != 0 {
		t.Fatalf("discover resurface min=%d", DiscoverQuotas[SourceResurface].Min)
	}
	sum := 0
	for _, src := range sourcePriority {
		sum += q[src].Min
	}
	if sum > 6 {
		t.Fatalf("min quotas %d exceed size 6", sum)
	}
}

func TestMMRPrefersDifferentArtists(t *testing.T) {
	idx := coreIndex(t)
	cands := []ScoredCandidate{
		{Candidate: Candidate{Row: 0, TrackID: 1, Primary: SourceExploit}, Score: 1},
		{Candidate: Candidate{Row: 1, TrackID: 2, Primary: SourceExploit}, Score: 0.99},
		{Candidate: Candidate{Row: 10, TrackID: 11, Primary: SourceWildcard}, Score: 0.4},
	}
	got := MMRSelect(cands, idx, 2, 0.6, nil)
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[1].TrackID == 2 {
		t.Fatal("MMR should diversify away from the same artist")
	}
}

func TestHardBlockRemovedBeforeSelect(t *testing.T) {
	idx := coreIndex(t)
	eval := rules.New(idx, []db.RadioRule{{
		TargetType: "artist", Action: "block", Scope: "global", TargetKey: "far", Strength: 1,
	}}, idx.MetaAt(0).CreatedAt)
	cands := CollectCandidates(idx, 1, CandidateOpts{
		Taste: []float32{1, 0}, Current: []float32{1, 0}, Rules: eval, Pool: 20,
	})
	for _, c := range cands {
		if idx.MetaAt(c.Row).Artist == "Far" {
			t.Fatalf("blocked artist leaked: %+v", c)
		}
	}
}

func uniqueArtistIndex(t *testing.T) *index.Index {
	t.Helper()
	idx := index.New(config.Config{NewTrackDays: 30, NewBoostTauDays: 14})
	rows := make([]db.TrackRow, 0, 24)
	for id := int64(1); id <= 24; id++ {
		vec := []float32{1, 0}
		if id > 12 {
			vec = []float32{0, 1}
		}
		rows = append(rows, db.TrackRow{
			ID: id, Artist: "Artist" + string(rune('A'+id-1)), Title: "Track",
			Album: "Album", Path: "/x.flac", Duration: 180, Year: 2010,
			BPM: 100 + float64(id), HasBPM: true, LUFS: -12, HasLUFS: true,
			Embedding: index.Float32Bytes(vec), Dim: 2,
		})
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestBuildCoreKeepsSizeAndMix(t *testing.T) {
	idx := uniqueArtistIndex(t)
	b := NewBuilder(idx, config.Config{QueueSize: 6})
	items := b.BuildCore(CoreOpts{CurrentID: 1, Taste: []float32{1, 0}, Size: 6})
	if len(items) != 6 {
		t.Fatalf("len=%d", len(items))
	}
	seen := map[int64]bool{1: true}
	exploit := 0
	for _, item := range items {
		if seen[item.TrackID] {
			t.Fatalf("duplicate %d", item.TrackID)
		}
		seen[item.TrackID] = true
		if item.Source == SourceExploit || item.Source == SourceTransition {
			exploit++
		}
	}
	if exploit == 6 {
		t.Fatal("final queue collapsed to only close tracks")
	}
}

func TestBuildCoreAssignsSourcesAndFeatures(t *testing.T) {
	idx := coreIndex(t)
	b := NewBuilder(idx, config.Config{QueueSize: 6})
	items := b.BuildCore(CoreOpts{
		CurrentID: 1, Taste: []float32{1, 0}, Size: 6,
		Contexts: []taste.ContextBlend{{
			Kind: "mood", Influence: 1, Vector: []float32{1, 0},
		}},
	})
	if len(items) == 0 {
		t.Fatal("expected core queue")
	}
	seen := map[int64]bool{1: true}
	for i, item := range items {
		if seen[item.TrackID] {
			t.Fatalf("duplicate %d", item.TrackID)
		}
		seen[item.TrackID] = true
		if item.Source == "" || item.FeaturesJSON == "" {
			t.Fatalf("missing source/features: %+v", item)
		}
		var snap FeatureSnapshot
		if err := json.Unmarshal([]byte(item.FeaturesJSON), &snap); err != nil {
			t.Fatal(err)
		}
		if snap.SchemaVersion != FeatureSchemaVersion || len(snap.Values) != len(FeatureOrder) {
			t.Fatalf("bad snapshot %+v", snap)
		}
		if snap.Context.Position != i {
			t.Fatalf("position %d, snapshot %d", i, snap.Context.Position)
		}
		if snap.Context.DurationSec != item.Duration {
			t.Fatalf("duration %v, snapshot %v", item.Duration, snap.Context.DurationSec)
		}
		if pos := FeatureIndex("queue_position"); pos < 0 || snap.Values[pos] != float64(i) {
			t.Fatalf("queue_position feature = %v", snap.Values)
		}
		if FeatureIndex("source_exploit") >= 0 {
			t.Fatal("source one-hot must stay out of the model vector")
		}
		if _, ok := snap.Context.Pairwise["clap"]; !ok {
			t.Fatalf("track %d snapshot missing pairwise features: %+v", item.TrackID, snap.Context)
		}
		if _, ok := snap.Context.Pairwise["repeat"]; !ok {
			t.Fatalf("track %d snapshot missing repeat: %+v", item.TrackID, snap.Context.Pairwise)
		}
	}
}

func TestCollectCandidatesFindsResurfaceOutsideCosine(t *testing.T) {
	idx := index.New(config.Config{NewTrackDays: 30, NewBoostTauDays: 14})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	rows := make([]db.TrackRow, 0, 12)
	for id := int64(1); id <= 10; id++ {
		rows = append(rows, db.TrackRow{
			ID: id, Artist: "Near", Title: "Track", Album: "Album",
			Path: "/x.flac", Duration: 180,
			Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2,
		})
	}
	rows = append(rows, db.TrackRow{
		ID: 11, Artist: "Old", Title: "Forgotten", Album: "Other",
		Path: "/y.flac", Duration: 200,
		Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2,
		CreatedAt: now.Add(-400 * 24 * time.Hour).Format(time.RFC3339),
	})
	rows = append(rows, db.TrackRow{
		ID: 12, Artist: "Edge", Title: "Linked", Album: "Other",
		Path: "/z.flac", Duration: 200, BPM: 180, HasBPM: true,
		Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2,
	})
	cat, err := index.BuildCatalog(rows)
	if err != nil {
		t.Fatal(err)
	}
	idx.Attach(cat, map[int64]db.TrackStats{
		11: {Finishes: 4, Plays: 6, LastFinishedAt: now.Add(-40 * 24 * time.Hour).Format(time.RFC3339)},
	})
	cands := CollectCandidates(idx, 1, CandidateOpts{
		Taste: []float32{1, 0}, Current: []float32{1, 0}, Now: now, Pool: 3,
		Transitions: map[int64]float64{12: 3},
	})
	found := map[int64]Candidate{}
	for _, cand := range cands {
		found[cand.TrackID] = cand
	}
	forgotten, ok := found[11]
	if !ok || !slices.Contains(forgotten.Sources, SourceResurface) {
		t.Fatalf("forgotten hit missing from arm retrieval: %+v", found)
	}
	if _, ok := found[12]; !ok {
		t.Fatal("transition edge outside the cosine pool was dropped")
	}
}

func TestResurfaceQuotaSurvivesExploitLabel(t *testing.T) {
	cands := []ScoredCandidate{
		{Candidate: Candidate{Row: 1, TrackID: 1, Primary: SourceExploit, Sources: []string{SourceExploit, SourceResurface}}, Score: 0.9},
		{Candidate: Candidate{Row: 2, TrackID: 2, Primary: SourceExploit, Sources: []string{SourceExploit}}, Score: 0.8},
		{Candidate: Candidate{Row: 3, TrackID: 3, Primary: SourceExploreAdjacent, Sources: []string{SourceExploreAdjacent}}, Score: 0.2},
	}
	quotas := map[string]Quota{
		SourceExploit:         {Min: 1, Max: 4},
		SourceTransition:      {Min: 0, Max: 1},
		SourceExploreAdjacent: {Min: 0, Max: 1},
		SourceResurface:       {Min: 1, Max: 1},
		SourceNewInLibrary:    {Min: 0, Max: 1},
		SourceWildcard:        {Min: 0, Max: 1},
	}
	got := Select(cands, SelectOpts{Size: 6, Quotas: quotas})
	for _, cand := range got {
		if cand.TrackID == 1 && cand.Primary == SourceResurface {
			return
		}
	}
	t.Fatalf("resurface quota hid inside exploit: %+v", got)
}

func TestBestDualTrackTakesResurfaceSlot(t *testing.T) {
	cands := []ScoredCandidate{
		{Candidate: Candidate{Row: 1, TrackID: 1, Primary: SourceExploit, Sources: []string{SourceExploit, SourceResurface}}, Score: 0.95},
		{Candidate: Candidate{Row: 8, TrackID: 8, Primary: SourceExploit, Sources: []string{SourceExploit, SourceResurface}}, Score: 0.5},
		{Candidate: Candidate{Row: 2, TrackID: 2, Primary: SourceExploit, Sources: []string{SourceExploit}}, Score: 0.9},
		{Candidate: Candidate{Row: 3, TrackID: 3, Primary: SourceExploit, Sources: []string{SourceExploit}}, Score: 0.85},
		{Candidate: Candidate{Row: 4, TrackID: 4, Primary: SourceExploit, Sources: []string{SourceExploit}}, Score: 0.8},
		{Candidate: Candidate{Row: 5, TrackID: 5, Primary: SourceExploreAdjacent, Sources: []string{SourceExploreAdjacent}}, Score: 0.4},
		{Candidate: Candidate{Row: 6, TrackID: 6, Primary: SourceExploreAdjacent, Sources: []string{SourceExploreAdjacent}}, Score: 0.3},
		{Candidate: Candidate{Row: 7, TrackID: 7, Primary: SourceWildcard, Sources: []string{SourceWildcard}}, Score: 0.1},
	}
	got := Select(cands, SelectOpts{Size: 6})
	best, other := "", ""
	for _, cand := range got {
		switch cand.TrackID {
		case 1:
			best = cand.Primary
		case 8:
			other = cand.Primary
		}
	}
	if best != SourceResurface {
		t.Fatalf("best exploit+resurface took %q, want resurface: %+v", best, got)
	}
	if other == SourceResurface {
		t.Fatalf("lower resurface took the only slot: %+v", got)
	}

	closed := map[string]Quota{}
	for src, quota := range DefaultQuotas {
		closed[src] = quota
	}
	closed[SourceResurface] = Quota{}
	hidden := Select(cands, SelectOpts{Size: 6, Quotas: closed})
	for _, cand := range hidden {
		if cand.TrackID == 1 && cand.Primary != SourceExploit {
			t.Fatalf("zeroed resurface share took %s", cand.Primary)
		}
	}
}

func TestResurfaceUsesLastFinish(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)
	sources := labelSources(index.Meta{
		Finishes: 3, LastFinishedAt: old, LastPlayedAt: recent,
	}, CandidateSims{Taste: 0.9}, now)
	if !slices.Contains(sources, SourceResurface) {
		t.Fatalf("sources=%v, want resurface despite a recent play", sources)
	}
	fresh := labelSources(index.Meta{
		Finishes: 3, LastFinishedAt: recent, LastPlayedAt: old,
	}, CandidateSims{Taste: 0.9}, now)
	if slices.Contains(fresh, SourceResurface) {
		t.Fatalf("sources=%v, recent finish should not resurface", fresh)
	}
}

func TestBuildCoreFillsRepeatAndTransition(t *testing.T) {
	idx := coreIndex(t)
	b := NewBuilder(idx, config.Config{QueueSize: 6})
	items := b.BuildCore(CoreOpts{
		CurrentID: 1, Taste: []float32{1, 0}, Size: 6,
		Transitions: map[int64]float64{2: 4},
	})
	if len(items) == 0 {
		t.Fatal("expected core queue")
	}
	artistIdx := FeatureIndex("artist_repeat")
	albumIdx := FeatureIndex("album_repeat")
	scoreIdx := FeatureIndex("transition_score")
	for _, item := range items {
		var snap FeatureSnapshot
		if err := json.Unmarshal([]byte(item.FeaturesJSON), &snap); err != nil {
			t.Fatal(err)
		}
		wantArtist := 0.0
		if item.Artist == "Near" {
			wantArtist = 1
		}
		if snap.Values[artistIdx] != wantArtist || snap.Values[albumIdx] != 1 {
			t.Fatalf("track %d artist=%s repeats=%v/%v", item.TrackID, item.Artist, snap.Values[artistIdx], snap.Values[albumIdx])
		}
		if snap.Values[scoreIdx] == 0 {
			t.Fatalf("track %d transition_score is still zero", item.TrackID)
		}
	}
}

func TestVectorSchemaV3ListeningFeatures(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	values := VectorFromInputs(FeatureInputs{
		Meta: index.Meta{
			Plays: 4, Finishes: 3, Partial: 1, Likes: 2, Dislikes: 1, Shown: 7, Completed: 1,
			LastPlayedAt: now.Add(-48 * time.Hour), LastFinishedAt: now.Add(-30 * 24 * time.Hour),
			Duration: 180,
		},
		Now: now,
	})
	if len(values) != len(FeatureOrder) {
		t.Fatalf("vector len=%d, feature order=%d", len(values), len(FeatureOrder))
	}
	at := func(name string) float64 { return values[FeatureIndex(name)] }
	if got := at("likes_log"); got != math.Log1p(2) {
		t.Fatalf("likes_log=%v", got)
	}
	if got := at("dislikes_log"); got != math.Log1p(1) {
		t.Fatalf("dislikes_log=%v", got)
	}
	if got := at("finish_rate"); got != 0.75 {
		t.Fatalf("finish_rate=%v", got)
	}
	if got := at("partial_rate"); got != 0.25 {
		t.Fatalf("partial_rate=%v", got)
	}
	if got := at("impression_fatigue"); got != math.Log1p(6) {
		t.Fatalf("impression_fatigue=%v", got)
	}
	if got := at("hours_since_finished"); got != 1 {
		t.Fatalf("hours_since_finished=%v", got)
	}
	if got := at("duration_min"); got != 3 {
		t.Fatalf("duration_min=%v", got)
	}
	if at("hours_since_played") == 0 || at("never_played") != 0 {
		t.Fatal("last play should stay on hours_since_played")
	}
	cold := VectorFromInputs(FeatureInputs{Meta: index.Meta{Plays: 0, Shown: 2}, Now: now})
	if cold[FeatureIndex("finish_rate")] != 0 || cold[FeatureIndex("hours_since_finished")] != 0 {
		t.Fatal("a track with no finish should not look recently finished")
	}
	if cold[FeatureIndex("impression_fatigue")] != math.Log1p(2) {
		t.Fatal("shown without a radio completion should fatigue the recommendation")
	}
	library := VectorFromInputs(FeatureInputs{
		Meta: index.Meta{Plays: 40, Shown: 5, Completed: 1}, Now: now,
	})
	if got := library[FeatureIndex("impression_fatigue")]; got != math.Log1p(4) {
		t.Fatalf("library plays must not clear fatigue, got %v", got)
	}
}

func TestPairwiseEnergyProfile(t *testing.T) {
	from := index.Meta{Artist: "A", Album: "X", LUFS: -16, HasLUFS: true, BPM: 90, HasBPM: true, KeyName: "8A"}
	to := index.Meta{Artist: "B", Album: "Y", LUFS: -8, HasLUFS: true, BPM: 130, HasBPM: true, KeyName: "8A"}
	calm, _ := PairwiseScore(TransitionInputs{From: from, To: to, CLAP: 0.4}, ProfileCalm)
	energy, _ := PairwiseScore(TransitionInputs{From: from, To: to, CLAP: 0.4}, ProfileEnergetic)
	if energy <= calm {
		t.Fatalf("energetic=%f calm=%f", energy, calm)
	}
}

func TestLearnedTransitionReplacesManualWeights(t *testing.T) {
	from := index.Meta{ID: 1, Artist: "A", Album: "X", LUFS: -16, HasLUFS: true, BPM: 90, HasBPM: true, KeyName: "8A"}
	to := index.Meta{Artist: "B", Album: "Y", LUFS: -8, HasLUFS: true, BPM: 130, HasBPM: true, KeyName: "8A"}
	manual, feats := PairwiseScore(TransitionInputs{From: from, To: to, CLAP: 0.4}, ProfileCalm)
	learned := TransitionModel{
		SchemaVersion: TransitionSchemaVersion,
		ModelVersion:  "test",
		FeatureOrder:  append([]string(nil), PairwiseFeatureOrder...),
		Mean:          []float64{0, 0, 0, 0, 0, 0, 0},
		Std:           []float64{1, 1, 1, 1, 1, 1, 1},
		Weights:       []float64{0, 0, 0, 0, 0, 4, 0},
		Bias:          0,
		ScoreMin:      -5,
		ScoreMax:      5,
	}
	if err := learned.Validate(); err != nil {
		t.Fatal(err)
	}
	got, _ := PairwiseScore(TransitionInputs{From: from, To: to, CLAP: 0.4, Learned: &learned}, ProfileCalm)
	if got == manual {
		t.Fatalf("learned score %v matched manual %v features %v", got, manual, feats)
	}
	if _, err := ParseTransitionModel([]byte(`{"schema_version":1}`)); err == nil {
		t.Fatal("incomplete transition model should be rejected")
	}
	if LoadRuntimeTransition(t.TempDir() + "/missing.json").Ready() {
		t.Fatal("missing transition model must keep manual weights")
	}
}

func TestOutcomeTransitionDelta(t *testing.T) {
	if db.OutcomeTransitionDelta("finished", false) != 1 {
		t.Fatal("finished")
	}
	if db.OutcomeTransitionDelta("partial", false) != 0.3 {
		t.Fatal("partial")
	}
	if got := db.OutcomeTransitionDelta("early_skip", true); got < -0.151 || got > -0.149 {
		t.Fatalf("manual skip %v", got)
	}
}
