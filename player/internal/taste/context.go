package taste

import (
	"github.com/torwin-job/musik/player/internal/index"
)

type ContextBlend struct {
	ID        string
	Kind      string
	Influence float64
	Vector    []float32
	Samples   int
}

// BlendContexts mixes independent mood/place/activity vectors without creating
// a combinatorial profile. Missing or empty contexts contribute zero weight.
func BlendContexts(base []float32, contexts []ContextBlend) []float32 {
	if len(base) == 0 && len(contexts) == 0 {
		return nil
	}
	dim := len(base)
	for _, ctx := range contexts {
		if len(ctx.Vector) > dim {
			dim = len(ctx.Vector)
		}
	}
	if dim == 0 {
		return nil
	}
	out := make([]float32, dim)
	total := float32(0)
	if len(base) == dim {
		copy(out, base)
		total = 1
	}
	for _, ctx := range contexts {
		if len(ctx.Vector) != dim || ctx.Influence <= 0 {
			continue
		}
		w := float32(ctx.Influence)
		if w > 2 {
			w = 2
		}
		for i := range out {
			out[i] += w * ctx.Vector[i]
		}
		total += w
	}
	if total <= 0 {
		return normalizedCopy(base)
	}
	for i := range out {
		out[i] /= total
	}
	index.Normalize(out)
	return out
}

func ContextSimilarities(vector []float32, contexts []ContextBlend) map[string]float64 {
	out := map[string]float64{
		"mood":     0,
		"place":    0,
		"activity": 0,
	}
	for _, ctx := range contexts {
		if len(ctx.Vector) == 0 || ctx.Influence <= 0 {
			continue
		}
		sim := float64(dot(ctx.Vector, vector))
		if sim > out[ctx.Kind] {
			out[ctx.Kind] = sim
		}
	}
	return out
}

func SeedVector(idx *index.Index, tracks []int64, artists []string, albums [][2]string) []float32 {
	if idx == nil {
		return nil
	}
	var vectors [][]float32
	for _, id := range tracks {
		row, ok := idx.RowOf(id)
		if !ok {
			continue
		}
		vectors = append(vectors, idx.Vector(row))
	}
	for _, artist := range artists {
		rows := idx.RowsForArtist(artist)
		if len(rows) == 0 {
			continue
		}
		vectors = append(vectors, idx.CentroidOf(rows))
	}
	for _, album := range albums {
		rows := idx.RowsForAlbum(album[0], album[1])
		if len(rows) == 0 {
			continue
		}
		vectors = append(vectors, idx.CentroidOf(rows))
	}
	return meanDirection(vectors, idx.Dim())
}
