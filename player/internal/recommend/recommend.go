// Package recommend scores similar tracks, artists, and albums from the
// in-memory index. HTTP mapping lives in internal/api.
package recommend

import (
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
)

type TrackHit struct {
	ID         int64
	Artist     string
	Title      string
	Album      string
	Duration   float64
	Cosine     float64
	HasArtwork bool
}

type ArtistHit struct {
	Artist       string
	Tracks       int
	Cosine       float64
	CoverTrackID int64
	HasArtwork   bool
	Explanation  string
}

type AlbumHit struct {
	Artist       string
	Album        string
	Tracks       int
	Cosine       float64
	CoverTrackID int64
	HasArtwork   bool
	Explanation  string
}

func TopTracks(idx *index.Index, vec []float32, exclude map[int64]bool, limit int) []TrackHit {
	if idx == nil || len(vec) == 0 || limit <= 0 {
		return nil
	}
	pairs := idx.TopK(vec, limit, exclude)
	out := make([]TrackHit, 0, len(pairs))
	for _, p := range pairs {
		m := idx.MetaAt(p.Row)
		out = append(out, TrackHit{
			ID: m.ID, Artist: m.Artist, Title: m.Title, Album: m.Album,
			Duration: m.Duration, Cosine: float64(p.Score),
			HasArtwork: m.ArtworkPath != "",
		})
	}
	return out
}

func SimilarTracks(idx *index.Index, trackID int64, limit int) []TrackHit {
	row, ok := idx.RowOf(trackID)
	if !ok {
		return nil
	}
	exclude := map[int64]bool{}
	for _, cloneID := range idx.CloneIDs(trackID) {
		exclude[cloneID] = true
	}
	return TopTracks(idx, idx.Vector(row), exclude, limit)
}

func FromTrack(idx *index.Index, trackID int64, limit int) []TrackHit {
	row, ok := idx.RowOf(trackID)
	if !ok {
		return nil
	}
	return TopTracks(idx, idx.Vector(row), map[int64]bool{trackID: true}, limit)
}

func FromArtist(idx *index.Index, artist string, limit int) []TrackHit {
	rows := idx.RowsForArtist(artist)
	exclude := map[int64]bool{}
	for _, ri := range rows {
		exclude[idx.MetaAt(ri).ID] = true
	}
	return TopTracks(idx, idx.CentroidOf(rows), exclude, limit)
}

func FromAlbum(idx *index.Index, artist, album string, limit int) []TrackHit {
	rows := idx.RowsForAlbum(artist, album)
	exclude := map[int64]bool{}
	for _, ri := range rows {
		exclude[idx.MetaAt(ri).ID] = true
	}
	return TopTracks(idx, idx.CentroidOf(rows), exclude, limit)
}

func SimilarArtists(idx *index.Index, seedArtist string, limit int) []ArtistHit {
	if idx == nil {
		return nil
	}
	seedRows := idx.RowsForArtist(seedArtist)
	seedVec := idx.CentroidOf(seedRows)
	if seedVec == nil {
		return nil
	}
	seedKey := strings.ToLower(strings.TrimSpace(seedArtist))
	type scored struct {
		artist string
		rows   []int
		sim    float32
	}
	var all []scored
	for _, group := range idx.ArtistCentroids() {
		if strings.ToLower(strings.TrimSpace(group.Artist)) == seedKey {
			continue
		}
		var sim float32
		for d := range group.Vector {
			sim += group.Vector[d] * seedVec[d]
		}
		all = append(all, scored{group.Artist, group.Rows, sim})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]ArtistHit, 0, len(all))
	for _, a := range all {
		m := idx.MetaAt(a.rows[0])
		out = append(out, ArtistHit{
			Artist: a.artist, Tracks: len(a.rows), Cosine: float64(a.sim),
			CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != "",
			Explanation: "похоже на «" + seedArtist + "»",
		})
	}
	return out
}

func SimilarAlbums(idx *index.Index, seedArtist, seedAlbum string, limit int) []AlbumHit {
	if idx == nil {
		return nil
	}
	seedRows := idx.RowsForAlbum(seedArtist, seedAlbum)
	seedVec := idx.CentroidOf(seedRows)
	if seedVec == nil {
		return nil
	}
	seedAl := strings.ToLower(strings.TrimSpace(seedAlbum))
	seedAr := strings.ToLower(strings.TrimSpace(seedArtist))
	type scored struct {
		artist, album string
		rows          []int
		sim           float32
	}
	var all []scored
	for _, group := range idx.AlbumCentroids() {
		artistKey := strings.ToLower(strings.TrimSpace(group.Artist))
		albumKey := strings.ToLower(strings.TrimSpace(group.Album))
		if albumKey == seedAl && (seedAr == "" || artistKey == seedAr) {
			continue
		}
		var sim float32
		for d := range group.Vector {
			sim += group.Vector[d] * seedVec[d]
		}
		all = append(all, scored{group.Artist, group.Album, group.Rows, sim})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]AlbumHit, 0, len(all))
	for _, a := range all {
		m := idx.MetaAt(a.rows[0])
		out = append(out, AlbumHit{
			Artist: a.artist, Album: a.album, Tracks: len(a.rows),
			Cosine: float64(a.sim), CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != "",
			Explanation: "похоже на «" + seedAlbum + "»",
		})
	}
	return out
}

type FavoritesMix struct {
	Empty   bool
	BasedOn map[string]any
	Tracks  []TrackHit
	Artists []ArtistHit
	Albums  []AlbumHit
}

// A hearted track is one point in taste. An album centroid is a smaller cloud
// and an artist centroid is the broadest, so they do not share one slot in the
// average.
const (
	favoriteTrackWeight  = 1.0
	favoriteAlbumWeight  = 0.55
	favoriteArtistWeight = 0.30
	favoriteMixSize      = 24
	favoritePool         = 200
	// favoriteLikeArm is the liked tracks added when they miss the cosine top.
	// One like can cover a moderate drop; a track with no likes stays out.
	favoriteLikeArm     = 48
	favoriteArtistLimit = 2
	favoriteQueryNoise  = 0.05
)

type weightedVector struct {
	vec    []float32
	weight float64
}

func FromFavorites(store *db.Store, idx *index.Index, model queue.Model) FavoritesMix {
	return fromFavorites(store, idx, time.Now(), model)
}

func fromFavorites(store *db.Store, idx *index.Index, now time.Time, model queue.Model) FavoritesMix {
	if len(model.Weights) == 0 {
		model = queue.DefaultModel()
	}
	exclude := map[int64]bool{}
	if recent, err := store.RecentTrackIDs(24*7, 500); err == nil {
		for _, id := range recent {
			exclude[id] = true
		}
	}
	skipArtists := map[string]bool{}
	skipAlbums := map[string]bool{}
	var parts []weightedVector
	basedOn := map[string]any{}

	favTracks, _ := store.FavoritesList()
	trackTitles := make([]string, 0, len(favTracks))
	for _, t := range favTracks {
		exclude[t.TrackID] = true
		trackTitles = append(trackTitles, t.Title)
		row, ok := idx.RowOf(t.TrackID)
		if !ok {
			continue
		}
		meta := idx.MetaAt(row)
		skipArtists[creditKey(meta.Artist)] = true
		if meta.Album != "" {
			skipAlbums[creditKey(meta.Artist, meta.Album)] = true
		}
		if vec := idx.Vector(row); len(vec) > 0 {
			parts = append(parts, weightedVector{vec: vec, weight: favoriteTrackWeight})
		}
	}
	basedOn["tracks"] = trackTitles

	favArtists, _ := store.FavArtistsList()
	artistNames := make([]string, 0, len(favArtists))
	for _, a := range favArtists {
		artistNames = append(artistNames, a.Artist)
		skipArtists[creditKey(a.Artist)] = true
		rows := idx.RowsForArtist(a.Artist)
		for _, r := range rows {
			exclude[idx.MetaAt(r).ID] = true
		}
		if v := idx.CentroidOf(rows); v != nil {
			parts = append(parts, weightedVector{vec: v, weight: favoriteArtistWeight})
		}
	}
	basedOn["artists"] = artistNames

	favAlbums, _ := store.FavAlbumsList()
	albumNames := make([]string, 0, len(favAlbums))
	for _, a := range favAlbums {
		albumNames = append(albumNames, a.Artist+" — "+a.Album)
		skipArtists[creditKey(a.Artist)] = true
		skipAlbums[creditKey(a.Artist, a.Album)] = true
		rows := idx.RowsForAlbum(a.Artist, a.Album)
		for _, r := range rows {
			exclude[idx.MetaAt(r).ID] = true
		}
		if v := idx.CentroidOf(rows); v != nil {
			parts = append(parts, weightedVector{vec: v, weight: favoriteAlbumWeight})
		}
	}
	basedOn["albums"] = albumNames

	query := combineWeighted(parts)
	if query == nil {
		return FavoritesMix{Empty: true, BasedOn: basedOn}
	}
	query = perturbQuery(query, favoriteDaySeed(now), favoriteQueryNoise)

	return FavoritesMix{
		BasedOn: basedOn,
		Tracks:  scoreFavoriteTracks(idx, query, exclude, now, model),
		Artists: artistsLike(idx, query, skipArtists, 10),
		Albums:  albumsLike(idx, query, skipAlbums, 10),
	}
}

func combineWeighted(parts []weightedVector) []float32 {
	dim := 0
	for _, part := range parts {
		if part.weight > 0 && len(part.vec) > dim {
			dim = len(part.vec)
		}
	}
	if dim == 0 {
		return nil
	}
	sum := make([]float64, dim)
	var total float64
	for _, part := range parts {
		if part.weight <= 0 || len(part.vec) != dim {
			continue
		}
		total += part.weight
		for d := 0; d < dim; d++ {
			sum[d] += part.weight * float64(part.vec[d])
		}
	}
	if total <= 0 {
		return nil
	}
	out := make([]float32, dim)
	inv := 1 / total
	for d := range out {
		out[d] = float32(sum[d] * inv)
	}
	index.Normalize(out)
	return out
}

func favoriteDaySeed(now time.Time) int64 {
	year, month, day := now.Date()
	return int64(year)*10000 + int64(month)*100 + int64(day)
}

func perturbQuery(q []float32, seed int64, sigma float64) []float32 {
	if len(q) == 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed))
	out := append([]float32(nil), q...)
	for i := range out {
		out[i] += float32(rng.NormFloat64() * sigma)
	}
	index.Normalize(out)
	return out
}

type mixCandidate struct {
	row    int
	id     int64
	score  float64
	cosine float64
	vec    []float32
	artist string
	meta   index.Meta
}

// scoreFavoriteTracks unions the cosine top with liked tracks that missed it.
// The linear score, MMR and artist cap still decide the shelf.
func scoreFavoriteTracks(idx *index.Index, query []float32, exclude map[int64]bool, now time.Time, model queue.Model) []TrackHit {
	if idx == nil || len(query) == 0 {
		return nil
	}
	pool := idx.TopK(query, favoritePool, exclude)
	seen := make(map[int64]bool, len(pool))
	type rowSim struct {
		row int
		sim float64
	}
	rows := make([]rowSim, 0, len(pool)+favoriteLikeArm)
	for _, hit := range pool {
		seen[idx.MetaAt(hit.Row).ID] = true
		rows = append(rows, rowSim{row: hit.Row, sim: float64(hit.Score)})
	}
	for _, row := range likedOutsideTop(idx, exclude, seen, favoriteLikeArm) {
		rows = append(rows, rowSim{row: row, sim: float64(idx.Dot(row, query))})
	}
	cands := make([]mixCandidate, 0, len(rows))
	for _, item := range rows {
		meta := idx.MetaAt(item.row)
		values := queue.VectorFromInputs(queue.FeatureInputs{
			Meta: meta, Now: now, SimTaste: item.sim,
		})
		cands = append(cands, mixCandidate{
			row: item.row, id: meta.ID, score: model.Score(values), cosine: item.sim,
			vec: idx.Vector(item.row), artist: strings.ToLower(strings.TrimSpace(meta.Artist)),
			meta: meta,
		})
	}
	picked := selectFavoriteTracks(cands, favoriteMixSize, favoriteArtistLimit)
	out := make([]TrackHit, 0, len(picked))
	for _, cand := range picked {
		out = append(out, TrackHit{
			ID: cand.meta.ID, Artist: cand.meta.Artist, Title: cand.meta.Title, Album: cand.meta.Album,
			Duration: cand.meta.Duration, Cosine: cand.cosine, HasArtwork: cand.meta.ArtworkPath != "",
		})
	}
	return out
}

// likedOutsideTop is the liked arm: up to limit tracks with Likes > 0 that
// missed the cosine pool and are not already excluded, highest likes first.
func likedOutsideTop(idx *index.Index, exclude, seen map[int64]bool, limit int) []int {
	if idx == nil || limit <= 0 {
		return nil
	}
	skip := make(map[int64]bool, len(exclude)+len(seen))
	for id := range exclude {
		skip[id] = true
	}
	for id := range seen {
		skip[id] = true
	}
	return idx.LikedRows(limit, skip)
}

// selectFavoriteTracks is MMR with a hard artist cap. Relevance is the shared
// linear score; diversity is cosine to tracks already chosen.
func selectFavoriteTracks(cands []mixCandidate, size, artistLimit int) []mixCandidate {
	if size < 1 || len(cands) == 0 {
		return nil
	}
	lambda := queue.ActiveSpec.MMRLambda
	if lambda <= 0 || lambda > 1 {
		lambda = 0.7
	}
	penalty := queue.ActiveSpec.ArtistPenalty
	remaining := append([]mixCandidate(nil), cands...)
	selected := make([]mixCandidate, 0, size)
	artistCount := map[string]int{}
	for len(selected) < size && len(remaining) > 0 {
		bestI, bestScore := -1, -1e9
		for i, cand := range remaining {
			if artistLimit > 0 && artistCount[cand.artist] >= artistLimit {
				continue
			}
			div := 0.0
			for _, prev := range selected {
				sim := dot32(cand.vec, prev.vec)
				if sim > div {
					div = sim
				}
			}
			score := lambda*cand.score - (1-lambda)*div - penalty*float64(artistCount[cand.artist])
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
		artistCount[chosen.artist]++
		remaining = append(remaining[:bestI], remaining[bestI+1:]...)
	}
	return selected
}

func artistsLike(idx *index.Index, query []float32, skip map[string]bool, limit int) []ArtistHit {
	if idx == nil || len(query) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		artist string
		rows   []int
		sim    float32
	}
	var all []scored
	for _, group := range idx.ArtistCentroids() {
		if skip[creditKey(group.Artist)] || len(group.Vector) == 0 || len(group.Rows) == 0 {
			continue
		}
		all = append(all, scored{group.Artist, group.Rows, dotQuery(group.Vector, query)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sim == all[j].sim {
			return all[i].artist < all[j].artist
		}
		return all[i].sim > all[j].sim
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]ArtistHit, 0, len(all))
	for _, item := range all {
		meta := idx.MetaAt(item.rows[0])
		out = append(out, ArtistHit{
			Artist: item.artist, Tracks: len(item.rows), Cosine: float64(item.sim),
			CoverTrackID: meta.ID, HasArtwork: meta.ArtworkPath != "",
			Explanation: "похоже на избранное",
		})
	}
	return out
}

func albumsLike(idx *index.Index, query []float32, skip map[string]bool, limit int) []AlbumHit {
	if idx == nil || len(query) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		artist, album string
		rows          []int
		sim           float32
	}
	var all []scored
	for _, group := range idx.AlbumCentroids() {
		if group.Album == "" || skip[creditKey(group.Artist, group.Album)] || len(group.Vector) == 0 || len(group.Rows) == 0 {
			continue
		}
		all = append(all, scored{group.Artist, group.Album, group.Rows, dotQuery(group.Vector, query)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sim == all[j].sim {
			if all[i].artist == all[j].artist {
				return all[i].album < all[j].album
			}
			return all[i].artist < all[j].artist
		}
		return all[i].sim > all[j].sim
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]AlbumHit, 0, len(all))
	for _, item := range all {
		meta := idx.MetaAt(item.rows[0])
		out = append(out, AlbumHit{
			Artist: item.artist, Album: item.album, Tracks: len(item.rows),
			Cosine: float64(item.sim), CoverTrackID: meta.ID, HasArtwork: meta.ArtworkPath != "",
			Explanation: "похоже на избранное",
		})
	}
	return out
}

func creditKey(parts ...string) string {
	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(out, "\x00")
}

func dotQuery(vec, query []float32) float32 {
	n := len(vec)
	if len(query) < n {
		n = len(query)
	}
	var sum float32
	for i := 0; i < n; i++ {
		sum += vec[i] * query[i]
	}
	return sum
}

func dot32(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += float64(a[i] * b[i])
	}
	return sum
}
