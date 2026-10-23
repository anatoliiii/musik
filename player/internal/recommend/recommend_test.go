package recommend

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func similarIndex(t *testing.T) *index.Index {
	t.Helper()
	idx := index.New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "Massive Attack", Album: "Mezzanine", Title: "Angel", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2, FileMD5: "md5-a"},
		{ID: 2, Artist: "Massive Attack", Album: "Mezzanine", Title: "Teardrop", Embedding: index.Float32Bytes([]float32{0.95, 0.05}), Dim: 2},
		{ID: 3, Artist: "Portishead", Album: "Dummy", Title: "Glory Box", Embedding: index.Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 4, Artist: "Portishead", Album: "Third", Title: "Machine Gun", Embedding: index.Float32Bytes([]float32{0.2, 0.8}), Dim: 2},
		{ID: 5, Artist: "Bjork", Album: "Homogenic", Title: "Joga", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 6, Artist: "Massive Attack", Album: "Mezzanine", Title: "Angel", Embedding: index.Float32Bytes([]float32{0.99, 0.01}), Dim: 2, FileMD5: "md5-a"},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestSimilarTracksExcludesClones(t *testing.T) {
	idx := similarIndex(t)
	hits := SimilarTracks(idx, 1, 10)
	if len(hits) == 0 {
		t.Fatal("expected similar tracks")
	}
	for _, h := range hits {
		if h.ID == 1 || h.ID == 6 {
			t.Fatalf("clone id %d should be excluded: %+v", h.ID, hits)
		}
	}
	if hits[0].ID != 2 && hits[0].ID != 3 {
		t.Fatalf("top similar = %d, want near Mezzanine/Portishead", hits[0].ID)
	}
}

func TestSimilarArtistsAndAlbums(t *testing.T) {
	idx := similarIndex(t)
	artists := SimilarArtists(idx, "Massive Attack", 12)
	if len(artists) == 0 {
		t.Fatal("expected similar artists")
	}
	for _, a := range artists {
		if a.Artist == "Massive Attack" {
			t.Fatal("seed artist must be excluded")
		}
	}
	if artists[0].Artist != "Portishead" {
		t.Fatalf("top artist=%q, want Portishead", artists[0].Artist)
	}

	albums := SimilarAlbums(idx, "Massive Attack", "Mezzanine", 12)
	if len(albums) == 0 {
		t.Fatal("expected similar albums")
	}
	for _, a := range albums {
		if a.Album == "Mezzanine" && a.Artist == "Massive Attack" {
			t.Fatal("seed album must be excluded")
		}
	}
}

func TestFromTrackAndArtistExcludeSeed(t *testing.T) {
	idx := similarIndex(t)
	tracks := FromTrack(idx, 1, 3)
	if len(tracks) == 0 {
		t.Fatal("expected recommendations from track")
	}
	for _, tr := range tracks {
		if tr.ID == 1 {
			t.Fatal("seed track must be excluded")
		}
	}

	tracks = FromArtist(idx, "Bjork", 20)
	for _, tr := range tracks {
		if tr.ID == 5 || tr.Artist == "Bjork" {
			t.Fatalf("seed artist tracks leaked: %+v", tr)
		}
	}
}

func TestFromFavoritesEmpty(t *testing.T) {
	store := openRecommendStore(t)
	mix := FromFavorites(store, similarIndex(t), queue.DefaultModel())
	if !mix.Empty {
		t.Fatalf("expected empty mix without hearts: %+v", mix)
	}
}

func TestFromFavoritesUsesHearts(t *testing.T) {
	store := openRecommendStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, album, duration, created_at, updated_at) VALUES (1,'/a','Angel','Massive Attack','Mezzanine',180, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	mix := FromFavorites(store, similarIndex(t), queue.DefaultModel())
	if mix.Empty {
		t.Fatal("expected mix from favorite track")
	}
	for _, tr := range mix.Tracks {
		if tr.ID == 1 {
			t.Fatal("favorite seed track must be excluded")
		}
	}
}

func TestFavoriteQueryWeightsTrackOverArtist(t *testing.T) {
	track := combineWeighted([]weightedVector{
		{vec: []float32{0, 1}, weight: favoriteTrackWeight},
		{vec: []float32{1, 0}, weight: favoriteArtistWeight},
	})
	equal := combineWeighted([]weightedVector{
		{vec: []float32{0, 1}, weight: 1},
		{vec: []float32{1, 0}, weight: 1},
	})
	if track[1] <= track[0] {
		t.Fatalf("track should outweigh the artist centroid: %v", track)
	}
	if track[1] <= equal[1] {
		t.Fatalf("heavier track weight should move the query: weighted %v equal %v", track, equal)
	}
	album := combineWeighted([]weightedVector{
		{vec: []float32{0, 1}, weight: favoriteAlbumWeight},
		{vec: []float32{1, 0}, weight: favoriteArtistWeight},
	})
	if !(track[1] > album[1] && album[1] > equal[1]) {
		t.Fatalf("weights should rank track > album > equal artist slot: track %v album %v", track, album)
	}
}

func TestPerturbQueryIsStableForADay(t *testing.T) {
	query := []float32{1, 0}
	same := perturbQuery(query, favoriteDaySeed(time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)), favoriteQueryNoise)
	again := perturbQuery(query, favoriteDaySeed(time.Date(2026, 10, 19, 23, 0, 0, 0, time.UTC)), favoriteQueryNoise)
	other := perturbQuery(query, favoriteDaySeed(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)), favoriteQueryNoise)
	if same[0] != again[0] || same[1] != again[1] {
		t.Fatalf("one calendar day must keep the query: %v vs %v", same, again)
	}
	if same[0] == other[0] && same[1] == other[1] {
		t.Fatal("a different day should move the query")
	}
}

func TestFromFavoritesCapsArtistAndFollowsQuery(t *testing.T) {
	store := openRecommendStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, album, duration, created_at, updated_at) VALUES (1,'/bjork','Joga','Bjork','Homogenic',180, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	if err := store.FavArtistAdd("Massive"); err != nil {
		t.Fatal(err)
	}
	idx := index.New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "Bjork", Album: "Homogenic", Title: "Joga", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 2, Artist: "Massive", Album: "Mezzanine", Title: "Angel", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 3, Artist: "Same", Album: "A", Title: "One", Embedding: index.Float32Bytes([]float32{0.05, 1}), Dim: 2},
		{ID: 4, Artist: "Same", Album: "A", Title: "Two", Embedding: index.Float32Bytes([]float32{0.04, 1}), Dim: 2},
		{ID: 5, Artist: "Same", Album: "A", Title: "Three", Embedding: index.Float32Bytes([]float32{0.03, 1}), Dim: 2},
		{ID: 6, Artist: "Same", Album: "B", Title: "Four", Embedding: index.Float32Bytes([]float32{0.02, 1}), Dim: 2},
		{ID: 7, Artist: "Same", Album: "B", Title: "Five", Embedding: index.Float32Bytes([]float32{0.01, 1}), Dim: 2},
		{ID: 8, Artist: "NearBjork", Album: "Near", Title: "Close", Embedding: index.Float32Bytes([]float32{0.1, 1}), Dim: 2},
		{ID: 9, Artist: "Other", Album: "Far", Title: "Side", Embedding: index.Float32Bytes([]float32{0.2, 0.9}), Dim: 2},
		{ID: 10, Artist: "NearMassive", Album: "West", Title: "Edge", Embedding: index.Float32Bytes([]float32{1, 0.05}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	model := queue.DefaultModel()
	mix := fromFavorites(store, idx, now, model)
	again := fromFavorites(store, idx, now, model)
	if mix.Empty {
		t.Fatal("expected a mix")
	}
	same := 0
	ids := make([]int64, 0, len(mix.Tracks))
	for _, tr := range mix.Tracks {
		ids = append(ids, tr.ID)
		if tr.ID == 1 || tr.Artist == "Massive" {
			t.Fatalf("seed leaked into the mix: %+v", tr)
		}
		if tr.Artist == "Same" {
			same++
		}
	}
	if same > favoriteArtistLimit {
		t.Fatalf("artist Same appeared %d times, limit %d: %+v", same, favoriteArtistLimit, mix.Tracks)
	}
	againIDs := make([]int64, 0, len(again.Tracks))
	for _, tr := range again.Tracks {
		againIDs = append(againIDs, tr.ID)
	}
	if len(ids) == 0 || len(ids) != len(againIDs) {
		t.Fatalf("mix ids %v vs %v", ids, againIDs)
	}
	for i := range ids {
		if ids[i] != againIDs[i] {
			t.Fatalf("same day changed order: %v vs %v", ids, againIDs)
		}
	}
	samePos, massivePos := -1, -1
	for i, artist := range mix.Artists {
		if artist.Artist == "Bjork" || artist.Artist == "Massive" {
			t.Fatalf("favorite artist returned as similar: %+v", artist)
		}
		if artist.Artist == "Same" {
			samePos = i
		}
		if artist.Artist == "NearMassive" {
			massivePos = i
		}
	}
	if samePos < 0 || massivePos < 0 || samePos > massivePos {
		t.Fatalf("query should follow the heavier track, not the artist centroid: %+v", mix.Artists)
	}
}

func TestLikedTrackOutsideCosineTopEntersFavorites(t *testing.T) {
	store := openRecommendStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, album, duration, created_at, updated_at) VALUES (1,'/seed','Seed','Seed','Album',180, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	const fillers = 210
	const lovedID int64 = 900
	const coldID int64 = 901
	rows := make([]db.TrackRow, 0, fillers+3)
	rows = append(rows, db.TrackRow{
		ID: 1, Artist: "Seed", Album: "Album", Title: "Seed",
		Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2,
	})
	for i := 0; i < fillers; i++ {
		id := int64(i + 2)
		rows = append(rows, db.TrackRow{
			ID: id, Artist: fmt.Sprintf("Near %d", id), Title: "Close",
			Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2,
		})
	}
	rows = append(rows,
		db.TrackRow{ID: lovedID, Artist: "Loved", Title: "Miss", Embedding: index.Float32Bytes([]float32{0.85, 0.53}), Dim: 2},
		db.TrackRow{ID: coldID, Artist: "Cold", Title: "Also", Embedding: index.Float32Bytes([]float32{0.84, 0.54}), Dim: 2},
	)
	cat, err := index.BuildCatalog(rows)
	if err != nil {
		t.Fatal(err)
	}
	idx := index.New(config.Config{})
	idx.Attach(cat, map[int64]db.TrackStats{lovedID: {Likes: 1}})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	mix := fromFavorites(store, idx, now, queue.DefaultModel())
	var loved, near *TrackHit
	for i := range mix.Tracks {
		switch mix.Tracks[i].ID {
		case lovedID:
			loved = &mix.Tracks[i]
		case coldID:
			t.Fatal("unliked track outside the cosine top must stay out")
		default:
			if near == nil {
				near = &mix.Tracks[i]
			}
		}
	}
	if loved == nil {
		t.Fatal("one like should bring a track in past the cosine top")
	}
	if near == nil || loved.Cosine >= near.Cosine {
		t.Fatalf("liked cosine should be the miss, loved=%+v near=%+v", loved, near)
	}
}

func TestLikedArmKeepsHighestOutsideTop(t *testing.T) {
	rows := make([]db.TrackRow, 0, 60)
	stats := map[int64]db.TrackStats{}
	for i := 0; i < 60; i++ {
		id := int64(i + 1)
		rows = append(rows, db.TrackRow{
			ID: id, Artist: "A", Title: fmt.Sprintf("T%d", id),
			Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2,
		})
		stats[id] = db.TrackStats{Likes: int(id)}
	}
	cat, err := index.BuildCatalog(rows)
	if err != nil {
		t.Fatal(err)
	}
	idx := index.New(config.Config{})
	idx.Attach(cat, stats)
	got := likedOutsideTop(idx, map[int64]bool{1: true}, map[int64]bool{60: true}, favoriteLikeArm)
	if len(got) != favoriteLikeArm {
		t.Fatalf("arm=%d, want %d", len(got), favoriteLikeArm)
	}
	first := idx.MetaAt(got[0])
	last := idx.MetaAt(got[len(got)-1])
	if first.ID != 59 || first.Likes != 59 {
		t.Fatalf("first=%+v, want id 59", first)
	}
	if last.ID != 12 || last.Likes != 12 {
		t.Fatalf("last=%+v, want id 12", last)
	}
	for _, row := range got {
		id := idx.MetaAt(row).ID
		if id == 1 || id == 60 || id < 12 {
			t.Fatalf("arm included %d", id)
		}
	}
}

func openRecommendStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recommend-test.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
