package queue

import (
	"math"
	"strings"

	"github.com/torwin-job/musik/player/internal/index"
)

type Quota struct {
	Min int
	Max int
}

var DefaultQuotas = map[string]Quota{
	SourceExploit:         {Min: 2, Max: 4},
	SourceTransition:      {Min: 0, Max: 2},
	SourceExploreAdjacent: {Min: 1, Max: 2},
	SourceResurface:       {Min: 0, Max: 1},
	SourceNewInLibrary:    {Min: 0, Max: 1},
	SourceWildcard:        {Min: 0, Max: 1},
}

var DiscoverQuotas = map[string]Quota{
	SourceExploit:         {Min: 1, Max: 2},
	SourceTransition:      {Min: 0, Max: 1},
	SourceExploreAdjacent: {Min: 1, Max: 2},
	SourceResurface:       {Min: 0, Max: 1},
	SourceNewInLibrary:    {Min: 1, Max: 2},
	SourceWildcard:        {Min: 1, Max: 2},
}

type ScoredCandidate struct {
	Candidate
	Score    float64
	Features []float64
	Downrank float64
}

type SelectOpts struct {
	Size        int
	Quotas      map[string]Quota
	MMRLambda   float64
	ArtistLimit int
	Idx         *index.Index
	CurrentID   int64
}

func ScaleQuotas(size int, base map[string]Quota) map[string]Quota {
	if size < 1 {
		size = 6
	}
	if base == nil {
		base = DefaultQuotas
	}
	scale := float64(size) / 6
	out := map[string]Quota{}
	mins, maxs := 0, 0
	for _, src := range sourcePriority {
		q := base[src]
		minN := int(math.Floor(float64(q.Min)*scale + 1e-9))
		maxN := int(math.Round(float64(q.Max) * scale))
		if q.Min > 0 && minN < 1 && size >= 3 {
			minN = 1
		}
		if maxN < minN {
			maxN = minN
		}
		out[src] = Quota{Min: minN, Max: maxN}
		mins += minN
		maxs += maxN
	}
	if mins > size {
		// Drop optional mins from the tail first.
		for i := len(sourcePriority) - 1; i >= 0 && mins > size; i-- {
			src := sourcePriority[i]
			q := out[src]
			if q.Min > 0 {
				q.Min--
				mins--
				out[src] = q
			}
		}
	}
	_ = maxs
	return out
}

func Select(cands []ScoredCandidate, opts SelectOpts) []ScoredCandidate {
	if opts.Size < 1 {
		opts.Size = 6
	}
	if opts.ArtistLimit < 1 {
		opts.ArtistLimit = 2
	}
	if opts.MMRLambda <= 0 {
		opts.MMRLambda = 0.7
	}
	quotas := ScaleQuotas(opts.Size, opts.Quotas)
	used := map[int]bool{}
	artistCount := map[string]int{}
	usedMD5 := map[string]bool{}
	usedSong := map[string]bool{}
	if opts.Idx != nil {
		if row, ok := opts.Idx.RowOf(opts.CurrentID); ok {
			meta := opts.Idx.MetaAt(row)
			if meta.FileMD5 != "" {
				usedMD5[meta.FileMD5] = true
			}
			if key := index.SongKey(meta.Artist, meta.Title); key != "" {
				usedSong[key] = true
			}
		}
	}

	canTake := func(c ScoredCandidate) bool {
		if used[c.Row] || opts.Idx == nil {
			return !used[c.Row]
		}
		meta := opts.Idx.MetaAt(c.Row)
		if artistCount[strings.ToLower(meta.Artist)] >= opts.ArtistLimit {
			return false
		}
		if meta.FileMD5 != "" && usedMD5[meta.FileMD5] {
			return false
		}
		if key := index.SongKey(meta.Artist, meta.Title); key != "" && usedSong[key] {
			return false
		}
		return true
	}
	mark := func(c ScoredCandidate) {
		used[c.Row] = true
		if opts.Idx == nil {
			return
		}
		meta := opts.Idx.MetaAt(c.Row)
		artistCount[strings.ToLower(meta.Artist)]++
		if meta.FileMD5 != "" {
			usedMD5[meta.FileMD5] = true
		}
		if key := index.SongKey(meta.Artist, meta.Title); key != "" {
			usedSong[key] = true
		}
	}

	bySource := map[string][]ScoredCandidate{}
	for _, c := range cands {
		bySource[c.Primary] = append(bySource[c.Primary], c)
	}
	for src := range bySource {
		sortByScore(bySource[src])
	}

	selected := make([]ScoredCandidate, 0, opts.Size)
	counts := map[string]int{}
	for _, src := range sourcePriority {
		want := quotas[src].Min
		for _, c := range bySource[src] {
			if len(selected) >= opts.Size || counts[src] >= want {
				break
			}
			if !canTake(c) {
				continue
			}
			selected = append(selected, c)
			mark(c)
			counts[src]++
		}
	}

	remaining := remainingCandidates(cands, used)
	for len(selected) < opts.Size {
		next, ok := mmrPick(remaining, selected, opts)
		if !ok {
			break
		}
		if counts[next.Primary] >= quotas[next.Primary].Max && hasOpenQuota(remaining, counts, quotas, used) {
			// skip this source if another source still has room
			used[next.Row] = true
			remaining = remainingCandidates(cands, used)
			continue
		}
		if !canTake(next) {
			used[next.Row] = true
			remaining = remainingCandidates(cands, used)
			continue
		}
		selected = append(selected, next)
		mark(next)
		counts[next.Primary]++
		remaining = remainingCandidates(cands, used)
	}

	if len(selected) < opts.Size {
		for _, c := range cands {
			if len(selected) >= opts.Size {
				break
			}
			if !canTake(c) {
				continue
			}
			selected = append(selected, c)
			mark(c)
		}
	}
	return selected
}

func MMRSelect(cands []ScoredCandidate, idx *index.Index, size int, lambda float64, forbidden map[int]bool) []ScoredCandidate {
	if lambda <= 0 {
		lambda = 0.7
	}
	selected := make([]ScoredCandidate, 0, size)
	remaining := make([]ScoredCandidate, 0, len(cands))
	for _, c := range cands {
		if forbidden[c.Row] {
			continue
		}
		remaining = append(remaining, c)
	}
	artistCount := map[string]int{}
	for len(selected) < size && len(remaining) > 0 {
		bestI, bestScore := -1, -1e9
		for i, c := range remaining {
			rel := c.Score
			div := 0.0
			if idx != nil && len(selected) > 0 {
				vec := idx.Vector(c.Row)
				for _, s := range selected {
					sim := float64(dot32(vec, idx.Vector(s.Row)))
					if sim > div {
						div = sim
					}
				}
			}
			artist := ""
			if idx != nil {
				artist = strings.ToLower(idx.MetaAt(c.Row).Artist)
			}
			score := lambda*rel - (1-lambda)*div - 0.15*float64(artistCount[artist])
			if score > bestScore {
				bestScore = score
				bestI = i
			}
		}
		if bestI < 0 {
			break
		}
		chosen := remaining[bestI]
		selected = append(selected, chosen)
		if idx != nil {
			artistCount[strings.ToLower(idx.MetaAt(chosen.Row).Artist)]++
		}
		remaining = append(remaining[:bestI], remaining[bestI+1:]...)
	}
	return selected
}

func mmrPick(remaining, selected []ScoredCandidate, opts SelectOpts) (ScoredCandidate, bool) {
	picked := MMRSelect(remaining, opts.Idx, 1, opts.MMRLambda, nil)
	if len(picked) == 0 {
		return ScoredCandidate{}, false
	}
	return picked[0], true
}

func remainingCandidates(cands []ScoredCandidate, used map[int]bool) []ScoredCandidate {
	out := make([]ScoredCandidate, 0, len(cands))
	for _, c := range cands {
		if !used[c.Row] {
			out = append(out, c)
		}
	}
	return out
}

func hasOpenQuota(remaining []ScoredCandidate, counts map[string]int, quotas map[string]Quota, used map[int]bool) bool {
	for _, c := range remaining {
		if used[c.Row] {
			continue
		}
		if counts[c.Primary] < quotas[c.Primary].Max {
			return true
		}
	}
	return false
}

func sortByScore(cands []ScoredCandidate) {
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].Score > cands[i].Score || (cands[j].Score == cands[i].Score && cands[j].TrackID < cands[i].TrackID) {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
}

func dot32(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
