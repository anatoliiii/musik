package queue

import (
	"sort"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/rules"
)

const (
	SourceExploit         = "exploit"
	SourceTransition      = "transition"
	SourceExploreAdjacent = "explore_adjacent"
	SourceResurface       = "resurface"
	SourceNewInLibrary    = "new_in_library"
	SourceWildcard        = "wildcard"
)

var sourcePriority = []string{
	SourceExploit,
	SourceTransition,
	SourceExploreAdjacent,
	SourceResurface,
	SourceNewInLibrary,
	SourceWildcard,
}

type Candidate struct {
	Row     int
	TrackID int64
	Sources []string
	Primary string
	Sims    CandidateSims
}

type CandidateSims struct {
	Taste       float32
	Current     float32
	Session     float32
	Daypart     float32
	Mood        float32
	Place       float32
	Activity    float32
	CentroidMax float32
}

type CandidateOpts struct {
	Taste       []float32
	Current     []float32
	Session     []float32
	Daypart     []float32
	Mood        []float32
	Place       []float32
	Activity    []float32
	Centroids   [][]float32
	Exclude     map[int64]bool
	Rules       *rules.Evaluator
	Now         time.Time
	Pool        int
	Transitions map[int64]float64
	Negative    func([]float32) float64
}

func CollectCandidates(idx *index.Index, currentID int64, opts CandidateOpts) []Candidate {
	n := idx.Size()
	if n == 0 {
		return nil
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	queries := make([][]float32, 0, 8)
	kind := make([]string, 0, 8)
	add := func(name string, vec []float32) {
		if len(vec) == idx.Dim() {
			queries = append(queries, vec)
			kind = append(kind, name)
		}
	}
	add("taste", opts.Taste)
	add("current", opts.Current)
	add("session", opts.Session)
	add("daypart", opts.Daypart)
	add("mood", opts.Mood)
	add("place", opts.Place)
	add("activity", opts.Activity)
	for i, centroid := range opts.Centroids {
		if i >= 6 {
			break
		}
		add("centroid", centroid)
	}
	far := farCentroid(opts.Centroids, opts.Taste)
	add("far", far)
	if len(queries) == 0 {
		queries = append(queries, idx.Centroid())
		kind = append(kind, "taste")
	}

	fused := idx.FusedSimsTo(queries...)
	byKind := map[string][]float32{}
	var centroidSims [][]float32
	for i, name := range kind {
		if name == "centroid" {
			centroidSims = append(centroidSims, fused[i])
			continue
		}
		byKind[name] = fused[i]
	}

	md5Cur := ""
	var currentMeta *index.Meta
	if r, ok := idx.RowOf(currentID); ok {
		meta := idx.MetaAt(r)
		md5Cur = meta.FileMD5
		currentMeta = &meta
	}
	allow := func(row int) bool {
		meta := idx.MetaAt(row)
		if meta.ID == currentID || opts.Exclude[meta.ID] {
			return false
		}
		if md5Cur != "" && meta.FileMD5 == md5Cur {
			return false
		}
		if opts.Rules != nil && opts.Rules.HardBlocked(meta.ID) {
			return false
		}
		return true
	}

	pool := opts.Pool
	if pool <= 0 {
		pool = ActiveSpec.Retrieval.SourcePool
	}
	tasteSim := byKind["taste"]
	currentSim := byKind["current"]
	exploitScore := make([]float32, n)
	for row := 0; row < n; row++ {
		exploitScore[row] = simAt(tasteSim, row)
		if c := maxAt(centroidSims, row); c > exploitScore[row] {
			exploitScore[row] = c
		}
	}
	seen := map[int]bool{}
	rows := make([]int, 0, pool*6)
	addRows := func(picked []int) {
		for _, row := range picked {
			if seen[row] {
				continue
			}
			seen[row] = true
			rows = append(rows, row)
		}
	}
	addRows(topK(exploitScore, pool, allow))
	addRows(transitionRows(idx, currentMeta, currentSim, opts.Transitions, pool, allow))
	addRows(resurfaceRows(idx, opts.Now, pool, allow))
	addRows(newInLibraryRows(idx, opts.Now, pool, allow))
	addRows(adjacentRows(tasteSim, pool, allow))
	addRows(wildcardRows(idx, tasteSim, byKind["far"], opts.Negative, pool, allow))

	out := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		meta := idx.MetaAt(row)
		sims := CandidateSims{
			Taste:       simAt(tasteSim, row),
			Current:     simAt(currentSim, row),
			Session:     simAt(byKind["session"], row),
			Daypart:     simAt(byKind["daypart"], row),
			Mood:        simAt(byKind["mood"], row),
			Place:       simAt(byKind["place"], row),
			Activity:    simAt(byKind["activity"], row),
			CentroidMax: maxAt(centroidSims, row),
		}
		sources := labelSources(meta, sims, opts.Now)
		out = append(out, Candidate{
			Row: row, TrackID: meta.ID, Sources: sources,
			Primary: primarySource(sources), Sims: sims,
		})
	}
	return out
}

func labelSources(meta index.Meta, sims CandidateSims, now time.Time) []string {
	exploit := float32(ActiveSpec.Thresholds.ExploitTaste)
	centroid := float32(ActiveSpec.Thresholds.ExploitCentroid)
	transition := float32(ActiveSpec.Thresholds.TransitionCurrent)
	adjacent := float32(ActiveSpec.Thresholds.AdjacentTaste)
	var out []string
	if sims.Taste >= exploit || sims.CentroidMax >= centroid {
		out = append(out, SourceExploit)
	}
	if sims.Current >= transition {
		out = append(out, SourceTransition)
	}
	if sims.Taste >= adjacent && sims.Taste < exploit {
		out = append(out, SourceExploreAdjacent)
	}
	if resurfaceEligible(meta, now) {
		out = append(out, SourceResurface)
	}
	if newInLibraryEligible(meta, now) {
		out = append(out, SourceNewInLibrary)
	}
	if sims.Taste < adjacent {
		out = append(out, SourceWildcard)
	}
	if len(out) == 0 {
		out = append(out, SourceWildcard)
	}
	return out
}

func resurfaceEligible(meta index.Meta, now time.Time) bool {
	if meta.Finishes < ActiveSpec.Resurface.MinFinishes || meta.LastFinishedAt.IsZero() {
		return false
	}
	return now.Sub(meta.LastFinishedAt).Hours() >= ActiveSpec.Resurface.MinHours
}

func newInLibraryEligible(meta index.Meta, now time.Time) bool {
	if meta.CreatedAt.IsZero() || meta.Plays >= ActiveSpec.NewInLibrary.MaxPlays {
		return false
	}
	return now.Sub(meta.CreatedAt).Hours() <= ActiveSpec.NewInLibrary.MaxAgeHours
}

func transitionRows(idx *index.Index, current *index.Meta, currentSim []float32, edges map[int64]float64, pool int, allow func(int) bool) []int {
	rows := topK(currentSim, pool, allow)
	seen := map[int]bool{}
	for _, row := range rows {
		seen[row] = true
	}
	if current != nil {
		window := make([]float32, idx.Size())
		for row := 0; row < idx.Size(); row++ {
			if !allow(row) || seen[row] {
				window[row] = -2
				continue
			}
			meta := idx.MetaAt(row)
			bpm, missing := bpmDistance(current, meta)
			key := keyCompat(current, meta)
			if (missing == 0 && bpm <= ActiveSpec.Retrieval.BPMWindow) || key >= ActiveSpec.Retrieval.KeyMin {
				window[row] = simAt(currentSim, row)
				continue
			}
			window[row] = -2
		}
		for _, row := range topK(window, pool, func(row int) bool { return window[row] > -1.5 }) {
			if !seen[row] {
				seen[row] = true
				rows = append(rows, row)
			}
		}
	}
	type edge struct {
		row int
		w   float64
	}
	ranked := make([]edge, 0, len(edges))
	for id, w := range edges {
		if w <= 0 {
			continue
		}
		row, ok := idx.RowOf(id)
		if !ok || !allow(row) || seen[row] {
			continue
		}
		ranked = append(ranked, edge{row, w})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].w == ranked[j].w {
			return ranked[i].row < ranked[j].row
		}
		return ranked[i].w > ranked[j].w
	})
	if len(ranked) > pool {
		ranked = ranked[:pool]
	}
	for _, item := range ranked {
		rows = append(rows, item.row)
	}
	return rows
}

func resurfaceRows(idx *index.Index, now time.Time, pool int, allow func(int) bool) []int {
	type hit struct {
		row, finishes int
		last          time.Time
	}
	hits := make([]hit, 0)
	for row := 0; row < idx.Size(); row++ {
		if !allow(row) {
			continue
		}
		meta := idx.MetaAt(row)
		if !resurfaceEligible(meta, now) {
			continue
		}
		hits = append(hits, hit{row: row, finishes: meta.Finishes, last: meta.LastFinishedAt})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].finishes != hits[b].finishes {
			return hits[a].finishes > hits[b].finishes
		}
		if !hits[a].last.Equal(hits[b].last) {
			return hits[a].last.Before(hits[b].last)
		}
		return hits[a].row < hits[b].row
	})
	if len(hits) > pool {
		hits = hits[:pool]
	}
	rows := make([]int, len(hits))
	for i, h := range hits {
		rows[i] = h.row
	}
	return rows
}

func newInLibraryRows(idx *index.Index, now time.Time, pool int, allow func(int) bool) []int {
	type hit struct {
		row     int
		created time.Time
	}
	hits := make([]hit, 0)
	for row := 0; row < idx.Size(); row++ {
		if !allow(row) {
			continue
		}
		meta := idx.MetaAt(row)
		if !newInLibraryEligible(meta, now) {
			continue
		}
		hits = append(hits, hit{row: row, created: meta.CreatedAt})
	}
	sort.Slice(hits, func(a, b int) bool {
		if !hits[a].created.Equal(hits[b].created) {
			return hits[a].created.After(hits[b].created)
		}
		return hits[a].row < hits[b].row
	})
	if len(hits) > pool {
		hits = hits[:pool]
	}
	rows := make([]int, len(hits))
	for i, h := range hits {
		rows[i] = h.row
	}
	return rows
}

func adjacentRows(taste []float32, pool int, allow func(int) bool) []int {
	lo := float32(ActiveSpec.Thresholds.AdjacentTaste)
	hi := float32(ActiveSpec.Thresholds.ExploitTaste)
	score := make([]float32, len(taste))
	for row, sim := range taste {
		if allow(row) && sim >= lo && sim < hi {
			score[row] = sim
			continue
		}
		score[row] = -2
	}
	return topK(score, pool, func(row int) bool { return score[row] > -1.5 })
}

func wildcardRows(idx *index.Index, taste, far []float32, negative func([]float32) float64, pool int, allow func(int) bool) []int {
	limit := float32(ActiveSpec.Thresholds.AdjacentTaste)
	clearance := ActiveSpec.Retrieval.NegativeClearance
	score := make([]float32, idx.Size())
	for row := 0; row < idx.Size(); row++ {
		score[row] = -2
		if !allow(row) || simAt(taste, row) >= limit {
			continue
		}
		if negative != nil && negative(idx.Vector(row)) >= clearance {
			continue
		}
		score[row] = simAt(far, row) - simAt(taste, row)
	}
	return topK(score, pool, func(row int) bool { return score[row] > -1.5 })
}

func farCentroid(centroids [][]float32, taste []float32) []float32 {
	if len(centroids) == 0 {
		return nil
	}
	bestI := len(centroids) - 1
	best := float32(2)
	for i, centroid := range centroids {
		sim := float32(0)
		if len(taste) > 0 && len(taste) == len(centroid) {
			sim = dot32(centroid, taste)
		}
		if sim < best {
			best = sim
			bestI = i
		}
	}
	return centroids[bestI]
}

func topK(score []float32, k int, allow func(int) bool) []int {
	if k < 1 {
		return nil
	}
	type pair struct {
		row int
		sim float32
	}
	all := make([]pair, 0, len(score))
	for row, sim := range score {
		if allow != nil && !allow(row) {
			continue
		}
		all = append(all, pair{row, sim})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sim == all[j].sim {
			return all[i].row < all[j].row
		}
		return all[i].sim > all[j].sim
	})
	if k > len(all) {
		k = len(all)
	}
	out := make([]int, k)
	for i := 0; i < k; i++ {
		out[i] = all[i].row
	}
	return out
}

func primarySource(sources []string) string {
	have := map[string]bool{}
	for _, src := range sources {
		have[src] = true
	}
	for _, src := range sourcePriority {
		if have[src] {
			return src
		}
	}
	return SourceWildcard
}

func simAt(values []float32, row int) float32 {
	if row < 0 || row >= len(values) {
		return 0
	}
	return values[row]
}

func maxAt(groups [][]float32, row int) float32 {
	var best float32 = 0
	for _, values := range groups {
		if row >= 0 && row < len(values) && values[row] > best {
			best = values[row]
		}
	}
	return best
}
