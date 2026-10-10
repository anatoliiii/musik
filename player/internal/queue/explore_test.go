package queue

import (
	"math/rand"
	"testing"
)

func TestExploreBoundsClampAndStayOrdered(t *testing.T) {
	lo, hi := ClampExploreBounds(0.9, 0.2)
	if lo >= hi || hi > 0.8 || lo < 0 {
		t.Fatalf("bounds %f %f", lo, hi)
	}
}

func TestDecideExploreStaysStaticUntilSourceMinimum(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	decision := DecideExplore(ExploreInput{
		Counts: map[string]int{SourceExploreAdjacent: 3},
		Size:   6, Lo: 0.1, Hi: 0.4, Rng: rng,
	})
	if decision.Enabled || decision.Reason != "insufficient_source_outcomes" {
		t.Fatalf("expected gated static quotas, got %+v", decision)
	}
}

func TestDecideExploreRespectsBoundsWhenReady(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	counts := map[string]int{}
	for _, src := range ExploreSources() {
		counts[src] = MinSourceOutcomes
	}
	decision := DecideExplore(ExploreInput{
		Counts: counts, Size: 6, Lo: 0.1, Hi: 0.25, Rng: rng,
		Arms: map[string]Arm{
			ArmAggregate: {Key: ArmAggregate, Kind: "aggregate", Alpha: 20, Beta: 4},
		},
	})
	if !decision.Enabled {
		t.Fatalf("expected thompson, got %+v", decision)
	}
	if decision.ExploreShare < 0.1 || decision.ExploreShare > 0.25 {
		t.Fatalf("share %f outside bounds", decision.ExploreShare)
	}
	if decision.Quotas == nil {
		t.Fatal("missing quotas")
	}
}
