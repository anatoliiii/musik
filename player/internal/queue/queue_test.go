package queue

import (
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestTransitionNormMonotonic(t *testing.T) {
	weights := map[int64]float64{1: 1, 2: 5, 3: 10}
	a := normTransition(1, weights)
	b := normTransition(5, weights)
	c := normTransition(10, weights)
	if !(a < b && b <= c) {
		t.Fatalf("expected monotonic norms got %v %v %v", a, b, c)
	}
	if c > 1.01 {
		t.Fatalf("norm at max should be ~1 got %v", c)
	}
	if normTransition(0, weights) != 0 {
		t.Fatal("zero weight should stay zero")
	}
}

func testBuilder(t *testing.T) *Builder {
	t.Helper()
	idx := index.New(config.Config{})
	rows := make([]db.TrackRow, 0, 12)
	for id := int64(1); id <= 12; id++ {
		artist := "Artist A"
		if id > 6 {
			artist = "Artist B"
		}
		rows = append(rows, db.TrackRow{
			ID: id, Artist: artist, Title: "Track", Album: "Album",
			Path: "/x.flac", Duration: 180,
			Embedding: index.Float32Bytes([]float32{float32(id), 1}), Dim: 2,
		})
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return NewBuilder(idx, config.Config{QueueSize: 5, ExploreRatio: 0.2})
}

func TestBuildReturnsDistinctQueue(t *testing.T) {
	b := testBuilder(t)
	taste := []float32{1, 0}
	index.Normalize(taste)
	items := b.BuildCore(CoreOpts{CurrentID: 1, Taste: taste, Exclude: map[int64]bool{1: true}, Size: 5})
	if len(items) == 0 {
		t.Fatal("expected queue items")
	}
	if len(items) > 5 {
		t.Fatalf("queue len=%d, want <=5", len(items))
	}
	seen := map[int64]bool{1: true}
	for _, item := range items {
		if item.TrackID == 0 {
			t.Fatal("empty track id in queue")
		}
		if seen[item.TrackID] {
			t.Fatalf("duplicate track %d in queue", item.TrackID)
		}
		seen[item.TrackID] = true
		if item.Artist == "" || item.Title == "" || item.Source == "" || item.FeaturesJSON == "" {
			t.Fatalf("incomplete item: %+v", item)
		}
	}
}

func TestBuildHonorsExcludeAndTransitions(t *testing.T) {
	b := testBuilder(t)
	exclude := map[int64]bool{1: true, 2: true, 3: true}
	items := b.BuildCore(CoreOpts{
		CurrentID: 1, Taste: []float32{1, 0}, Exclude: exclude, Size: 4,
		Transitions: map[int64]float64{10: 20, 11: 5},
	})
	for _, item := range items {
		if exclude[item.TrackID] {
			t.Fatalf("excluded track %d leaked into queue", item.TrackID)
		}
	}
}

func TestPickRandomSkipsExcluded(t *testing.T) {
	b := testBuilder(t)
	exclude := map[int64]bool{}
	for id := int64(1); id <= 11; id++ {
		exclude[id] = true
	}
	got := b.PickRandom(exclude)
	if got != 12 {
		t.Fatalf("PickRandom=%d, want only remaining id 12", got)
	}
	if b.PickRandom(map[int64]bool{}) == 0 {
		t.Fatal("PickRandom on non-empty index returned 0")
	}
	empty := NewBuilder(index.New(config.Config{}), config.Config{})
	if empty.PickRandom(nil) != 0 {
		t.Fatal("PickRandom on empty index should return 0")
	}
}

func TestPickRandomFullExcludeReturnsZero(t *testing.T) {
	b := testBuilder(t)
	exclude := map[int64]bool{}
	for id := int64(1); id <= 12; id++ {
		exclude[id] = true
	}
	if got := b.PickRandom(exclude); got != 0 {
		t.Fatalf("PickRandom=%d, want 0 when every track is excluded", got)
	}
}

func TestRandomFloat64IsBounded(t *testing.T) {
	b := testBuilder(t)
	for i := 0; i < 20; i++ {
		v := b.RandomFloat64()
		if v < 0 || v >= 1 {
			t.Fatalf("RandomFloat64=%f out of [0,1)", v)
		}
	}
}
