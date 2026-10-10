package queue

import (
	"math"
	"math/rand"
	"time"
)

const (
	ArmAggregate        = "aggregate"
	MinSourceOutcomes   = 40
	ExploreDailyDecay   = 0.97
	ExplorePriorAlpha   = 2.0
	ExplorePriorBeta    = 8.0
	PolicySchemaVersion = 1
)

type Arm struct {
	Key       string
	Kind      string
	Alpha     float64
	Beta      float64
	Successes int
	Failures  int
	LastDecay time.Time
}

type ExploreDecision struct {
	Enabled      bool               `json:"enabled"`
	ExploreShare float64            `json:"explore_share"`
	SampledP     float64            `json:"sampled_p"`
	SourceP      map[string]float64 `json:"source_p,omitempty"`
	SourceShare  map[string]float64 `json:"source_share,omitempty"`
	Quotas       map[string]Quota   `json:"-"`
	Bounds       [2]float64         `json:"bounds"`
	Reason       string             `json:"reason"`
}

type ExploreInput struct {
	Arms     map[string]Arm
	Counts   map[string]int
	Size     int
	Lo       float64
	Hi       float64
	Discover bool
	Now      time.Time
	Rng      *rand.Rand
}

func ExploreSources() []string {
	return []string{SourceExploreAdjacent, SourceResurface, SourceNewInLibrary, SourceWildcard}
}

func IsExploreSource(source string) bool {
	switch source {
	case SourceExploreAdjacent, SourceResurface, SourceNewInLibrary, SourceWildcard, "explore":
		return true
	default:
		return false
	}
}

func IsExploitSource(source string) bool {
	switch source {
	case SourceExploit, SourceTransition, "radio_start", "refill":
		return true
	default:
		return false
	}
}

func AllowedBanditOutcome(outcome string) bool {
	switch outcome {
	case "finished", "early_skip", "like", "dislike":
		return true
	default:
		return false
	}
}

func BanditSuccess(outcome string) bool {
	return outcome == "finished" || outcome == "like"
}

func ClampExploreBounds(lo, hi float64) (float64, float64) {
	if lo < 0 {
		lo = 0
	}
	if hi > 0.8 {
		hi = 0.8
	}
	if hi < 0.05 {
		hi = 0.05
	}
	if lo >= hi {
		lo = math.Max(0, hi-0.05)
	}
	return lo, hi
}

func DefaultArm(key, kind string) Arm {
	alpha, beta := 1.0, 1.0
	if kind == "aggregate" {
		alpha, beta = ExplorePriorAlpha, ExplorePriorBeta
	}
	return Arm{Key: key, Kind: kind, Alpha: alpha, Beta: beta}
}

func DecayArm(arm Arm, now time.Time) Arm {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if arm.LastDecay.IsZero() {
		arm.LastDecay = now
		return arm
	}
	elapsed := now.Sub(arm.LastDecay)
	if elapsed < 24*time.Hour {
		return arm
	}
	days := elapsed.Hours() / 24
	factor := math.Pow(ExploreDailyDecay, days)
	priorA, priorB := 1.0, 1.0
	if arm.Kind == "aggregate" || arm.Key == ArmAggregate {
		priorA, priorB = ExplorePriorAlpha, ExplorePriorBeta
	}
	arm.Alpha = priorA + (arm.Alpha-priorA)*factor
	arm.Beta = priorB + (arm.Beta-priorB)*factor
	if arm.Alpha < 0.2 {
		arm.Alpha = 0.2
	}
	if arm.Beta < 0.2 {
		arm.Beta = 0.2
	}
	arm.LastDecay = now
	return arm
}

func ExploreReady(counts map[string]int) bool {
	for _, src := range ExploreSources() {
		if counts[src] < MinSourceOutcomes {
			return false
		}
	}
	return true
}

func DecideExplore(in ExploreInput) ExploreDecision {
	lo, hi := ClampExploreBounds(in.Lo, in.Hi)
	size := in.Size
	if size < 1 {
		size = 6
	}
	decision := ExploreDecision{
		Bounds: [2]float64{lo, hi},
		Quotas: ScaleQuotas(size, DefaultQuotas),
	}
	if in.Discover {
		decision.Quotas = ScaleQuotas(size, DiscoverQuotas)
		decision.Reason = "static_discover"
		decision.ExploreShare = exploreShareFromQuotas(decision.Quotas, size)
		return decision
	}
	if !ExploreReady(in.Counts) {
		decision.Reason = "insufficient_source_outcomes"
		decision.ExploreShare = exploreShareFromQuotas(decision.Quotas, size)
		return decision
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rng := in.Rng
	if rng == nil {
		rng = rand.New(rand.NewSource(now.UnixNano()))
	}
	arms := in.Arms
	if arms == nil {
		arms = map[string]Arm{}
	}
	agg := arms[ArmAggregate]
	if agg.Alpha <= 0 || agg.Beta <= 0 {
		agg = DefaultArm(ArmAggregate, "aggregate")
	}
	agg = DecayArm(agg, now)
	sampled := sampleBeta(agg.Alpha, agg.Beta, rng)
	share := clamp(sampled, lo, hi)
	sourceP := map[string]float64{}
	var sumP float64
	for _, src := range ExploreSources() {
		arm := arms[src]
		if arm.Alpha <= 0 || arm.Beta <= 0 {
			arm = DefaultArm(src, "source")
		}
		arm = DecayArm(arm, now)
		p := sampleBeta(arm.Alpha, arm.Beta, rng)
		sourceP[src] = p
		sumP += p
	}
	if sumP <= 0 {
		for _, src := range ExploreSources() {
			sourceP[src] = 1
			sumP++
		}
	}
	sourceShare := map[string]float64{}
	for src, p := range sourceP {
		sourceShare[src] = p / sumP
	}
	decision.Enabled = true
	decision.ExploreShare = share
	decision.SampledP = sampled
	decision.SourceP = sourceP
	decision.SourceShare = sourceShare
	decision.Quotas = QuotasFromExploreShare(size, share, sourceShare)
	decision.Reason = "thompson"
	return decision
}

func QuotasFromExploreShare(size int, share float64, sourceShare map[string]float64) map[string]Quota {
	if size < 1 {
		size = 6
	}
	nExplore := int(math.Round(float64(size) * share))
	if size >= 3 && share > 0 && nExplore < 1 {
		nExplore = 1
	}
	if nExplore > size-1 {
		nExplore = size - 1
	}
	if nExplore < 0 {
		nExplore = 0
	}
	nExploit := size - nExplore
	out := map[string]Quota{
		SourceExploit:         {Min: max(0, nExploit-1), Max: nExploit},
		SourceTransition:      {Min: 0, Max: min(2, nExploit)},
		SourceExploreAdjacent: {Min: 0, Max: nExplore},
		SourceResurface:       {Min: 0, Max: nExplore},
		SourceNewInLibrary:    {Min: 0, Max: nExplore},
		SourceWildcard:        {Min: 0, Max: nExplore},
	}
	remaining := nExplore
	for i, src := range ExploreSources() {
		weight := 0.0
		if sourceShare != nil {
			weight = sourceShare[src]
		}
		want := int(math.Round(float64(nExplore) * weight))
		if i == len(ExploreSources())-1 {
			want = remaining
		}
		if want < 0 {
			want = 0
		}
		if want > remaining {
			want = remaining
		}
		out[src] = Quota{Min: min(1, want), Max: max(want, min(1, nExplore))}
		if nExplore == 0 {
			out[src] = Quota{Min: 0, Max: 0}
		}
		remaining -= want
	}
	if nExplore > 0 && out[SourceExploreAdjacent].Min == 0 && size >= 3 {
		out[SourceExploreAdjacent] = Quota{Min: 1, Max: max(1, out[SourceExploreAdjacent].Max)}
	}
	return ScaleQuotas(size, out)
}

func exploreShareFromQuotas(quotas map[string]Quota, size int) float64 {
	if size < 1 {
		return 0
	}
	n := 0
	for _, src := range ExploreSources() {
		n += quotas[src].Min
	}
	return float64(n) / float64(size)
}

func sampleBeta(alpha, beta float64, rng *rand.Rand) float64 {
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}
	a := sampleGamma(alpha, rng)
	b := sampleGamma(beta, rng)
	if a+b <= 0 {
		return 0.5
	}
	return a / (a + b)
}

func sampleGamma(shape float64, rng *rand.Rand) float64 {
	if shape < 1 {
		return sampleGamma(shape+1, rng) * math.Pow(rng.Float64(), 1/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
