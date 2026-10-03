package rules

import (
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestBlockAndDownrankDoNotMixWithMissingTarget(t *testing.T) {
	idx := index.New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "A", Title: "One", Album: "X", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "B", Title: "Two", Album: "Y", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	eval := New(idx, []db.RadioRule{
		{TargetType: "artist", Action: "block", Scope: "global", TargetKey: "a", Strength: 1},
		{TargetType: "track", Action: "downrank", Scope: "global", TargetKey: "2", Strength: 0.8},
	}, time.Now())
	if !eval.HardBlocked(1) {
		t.Fatal("artist A should be blocked")
	}
	if eval.HardBlocked(2) {
		t.Fatal("downrank is not a hard block")
	}
	if eval.Downrank(2) != 0.8 {
		t.Fatalf("downrank=%v", eval.Downrank(2))
	}
	if !eval.BlocksArtist("A") || eval.BlocksArtist("B") {
		t.Fatal("artist block key should be the normalized name")
	}
	if got := eval.FilterIDs([]int64{1, 2}); len(got) != 1 || got[0] != 2 {
		t.Fatalf("filter=%v", got)
	}
}
