package recommend

import (
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
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
	mix := FromFavorites(store, similarIndex(t))
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
	mix := FromFavorites(store, similarIndex(t))
	if mix.Empty {
		t.Fatal("expected mix from favorite track")
	}
	for _, tr := range mix.Tracks {
		if tr.ID == 1 {
			t.Fatal("favorite seed track must be excluded")
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
