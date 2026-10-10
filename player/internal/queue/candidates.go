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
	Taste     []float32
	Current   []float32
	Session   []float32
	Daypart   []float32
	Mood      []float32
	Place     []float32
	Activity  []float32
	Centroids [][]float32
	Exclude   map[int64]bool
	Rules     *rules.Evaluator
	Now       time.Time
	Pool      int
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

	pool := opts.Pool
	if pool <= 0 {
		pool = 200
	}
	type scored struct {
		row int
		sim float32
	}
	best := make([]scored, n)
	for row := 0; row < n; row++ {
		var maxSim float32 = -2
		for _, sims := range fused {
			if sims[row] > maxSim {
				maxSim = sims[row]
			}
		}
		best[row] = scored{row, maxSim}
	}
	sort.Slice(best, func(i, j int) bool { return best[i].sim > best[j].sim })
	if pool > len(best) {
		pool = len(best)
	}

	md5Cur := ""
	if r, ok := idx.RowOf(currentID); ok {
		md5Cur = idx.MetaAt(r).FileMD5
	}
	out := make([]Candidate, 0, pool)
	for _, item := range best[:pool] {
		meta := idx.MetaAt(item.row)
		if meta.ID == currentID || opts.Exclude[meta.ID] {
			continue
		}
		if md5Cur != "" && meta.FileMD5 == md5Cur {
			continue
		}
		if opts.Rules != nil && opts.Rules.HardBlocked(meta.ID) {
			continue
		}
		sims := CandidateSims{
			Taste:       simAt(byKind["taste"], item.row),
			Current:     simAt(byKind["current"], item.row),
			Session:     simAt(byKind["session"], item.row),
			Daypart:     simAt(byKind["daypart"], item.row),
			Mood:        simAt(byKind["mood"], item.row),
			Place:       simAt(byKind["place"], item.row),
			Activity:    simAt(byKind["activity"], item.row),
			CentroidMax: maxAt(centroidSims, item.row),
		}
		sources := labelSources(meta, sims, opts.Now)
		out = append(out, Candidate{
			Row: item.row, TrackID: meta.ID, Sources: sources,
			Primary: primarySource(sources), Sims: sims,
		})
	}
	return out
}

func labelSources(meta index.Meta, sims CandidateSims, now time.Time) []string {
	var out []string
	if sims.Taste >= 0.45 || sims.CentroidMax >= 0.5 {
		out = append(out, SourceExploit)
	}
	if sims.Current >= 0.55 {
		out = append(out, SourceTransition)
	}
	if sims.Taste >= 0.15 && sims.Taste < 0.45 {
		out = append(out, SourceExploreAdjacent)
	}
	if !meta.LastPlayedAt.IsZero() && now.Sub(meta.LastPlayedAt) >= 14*24*time.Hour && meta.Finishes > 0 {
		out = append(out, SourceResurface)
	}
	if !meta.CreatedAt.IsZero() && now.Sub(meta.CreatedAt) <= 30*24*time.Hour && meta.Plays < 2 {
		out = append(out, SourceNewInLibrary)
	}
	if sims.Taste < 0.15 {
		out = append(out, SourceWildcard)
	}
	if len(out) == 0 {
		out = append(out, SourceWildcard)
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
