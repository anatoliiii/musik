package queue

import (
	"encoding/json"
	"slices"
	"testing"

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
			Embedding: index.Float32Bytes(vec), Dim: 2, Plays: int(id % 4),
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
			Embedding: index.Float32Bytes(vec), Dim: 2, Plays: int(id % 4),
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
	for _, item := range items {
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
