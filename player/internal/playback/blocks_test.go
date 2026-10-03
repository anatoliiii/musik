package playback

import (
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestHideBlockedDropsArtistFromRadioAndMix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocks.db")
	if err := testdb.Create(path); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{QueueSize: 6, ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0}
	idx := index.New(cfg)
	rows := []db.TrackRow{
		{ID: 11, Title: "Hidden", Artist: "Gone", Album: "A", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 12, Title: "Also hidden", Artist: "Gone", Album: "A", Embedding: index.Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 22, Title: "Stay", Artist: "Kept", Album: "B", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 33, Title: "Stay too", Artist: "Kept", Album: "B", Embedding: index.Float32Bytes([]float32{0.1, 0.9}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	engine := New(cfg, store, idx, taste.New(), queue.NewBuilder(idx, cfg))

	seed := int64(11)
	radio := engine.StartRadio(&seed)
	if _, err := store.CreateRadioRule(db.RadioRule{
		TargetType: "artist", Action: "block", Scope: "global", TargetKey: "gone", Strength: 1,
	}); err != nil {
		t.Fatal(err)
	}
	engine.HideBlocked()

	radio.Lock()
	if radio.Current == 11 || radio.Current == 12 || radio.Current == 0 {
		t.Fatalf("radio current=%d, want a Kept track", radio.Current)
	}
	for _, item := range radio.Queue {
		if item.TrackID == 11 || item.TrackID == 12 {
			t.Fatalf("radio queue still has %d", item.TrackID)
		}
	}
	radio.Unlock()

	mix := engine.StartFixed([]int64{11, 22, 12, 33}, "playlist", "Для вас", "for_you", 0, 0)
	engine.HideBlocked()
	mix.Lock()
	defer mix.Unlock()
	if mix.Current == 11 || mix.Current == 12 {
		t.Fatalf("mix current=%d", mix.Current)
	}
	for _, id := range mix.DailyIDs {
		if id == 11 || id == 12 {
			t.Fatalf("mix still contains %d in %v", id, mix.DailyIDs)
		}
	}
	for _, item := range mix.Queue {
		if item.TrackID == 11 || item.TrackID == 12 {
			t.Fatalf("mix queue still has %d", item.TrackID)
		}
	}
}
