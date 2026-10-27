package index

import (
	"sort"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

func catalogRows(stamp map[int64]string, vectors map[int64][]float32) []db.TrackRow {
	ids := make([]int64, 0, len(vectors))
	for id := range vectors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows := make([]db.TrackRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, db.TrackRow{ID: id, Artist: "Artist", Album: "Album", Title: "T", Dim: 2, EmbeddingStamp: stamp[id]})
	}
	return rows
}

func TestProfilesShareCatalogVectorsAndKeepTheirOwnCounters(t *testing.T) {
	cat, err := BuildCatalog([]db.TrackRow{
		{ID: 1, Artist: "A", Title: "One", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "B", Title: "Two", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := New(config.Config{}), New(config.Config{})
	alice.Attach(cat, map[int64]db.TrackStats{1: {Plays: 7, Shown: 2}})
	bob.Attach(cat, nil)
	if &alice.Matrix[0] != &bob.Matrix[0] {
		t.Fatal("profiles hold separate copies of the embedding matrix")
	}
	alice.BumpShownLocal(1)
	if got := alice.MetaAt(0); got.Plays != 7 || got.Shown != 3 {
		t.Fatalf("alice meta=%+v", got)
	}
	if got := bob.MetaAt(0); got.Plays != 0 || got.Shown != 0 {
		t.Fatalf("bob sees alice's counters: %+v", got)
	}
	if cat.meta[0].Shown != 0 {
		t.Fatal("a profile counter leaked into the shared catalog")
	}
}

func TestRefreshCatalogFetchesOnlyChangedEmbeddings(t *testing.T) {
	stored := map[int64][]float32{1: {1, 0}, 2: {0, 1}, 3: {1, 1}}
	stamps := map[int64]string{1: "s1", 2: "s1", 3: "s1"}
	var fetched [][]int64
	fetch := func(ids []int64) (map[int64][]byte, error) {
		fetched = append(fetched, append([]int64(nil), ids...))
		out := map[int64][]byte{}
		for _, id := range ids {
			out[id] = Float32Bytes(stored[id])
		}
		return out, nil
	}
	first, err := RefreshCatalog(nil, catalogRows(stamps, stored), fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 || len(fetched[0]) != 3 {
		t.Fatalf("first load fetched %v", fetched)
	}

	// Track 2 is re-embedded, track 3 removed, track 4 added.
	stored[2], stamps[2] = []float32{-1, 0}, "s2"
	delete(stored, 3)
	stored[4], stamps[4] = []float32{0, -1}, "s1"
	fetched = nil
	next, err := RefreshCatalog(first, catalogRows(stamps, stored), fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 || len(fetched[0]) != 2 || fetched[0][0] != 2 || fetched[0][1] != 4 {
		t.Fatalf("refresh fetched %v, want [2 4]", fetched)
	}
	idx := New(config.Config{})
	idx.Attach(next, nil)
	for id, want := range map[int64][]float32{1: {1, 0}, 2: {-1, 0}, 4: {0, -1}} {
		row, ok := idx.RowOf(id)
		if !ok {
			t.Fatalf("track %d missing", id)
		}
		if got := idx.Vector(row); got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("track %d vector=%v want %v", id, got, want)
		}
	}
	if _, ok := idx.RowOf(3); ok {
		t.Fatal("removed track still indexed")
	}
	if rows := idx.RowsForArtist("Artist"); len(rows) != 3 {
		t.Fatalf("artist rows=%v", rows)
	}
}

func TestCatalogForProfileHidesOtherProfilesPrivateTracks(t *testing.T) {
	vec := func(a, b float32) []byte { return Float32Bytes([]float32{a, b}) }
	cat, err := BuildCatalog([]db.TrackRow{
		{ID: 1, Path: "/music/Band/One.flac", Artist: "Band", Album: "Shared", Title: "One", Embedding: vec(1, 0), Dim: 2},
		{ID: 2, Path: "/music/_private/alice/Yandex Music/Solo/Two.flac", Artist: "Solo", Album: "Mine", Title: "Two", Embedding: vec(0, 1), Dim: 2},
		{ID: 3, Path: "/music/_private/bob/Three.flac", Artist: "Other", Title: "Three", Embedding: vec(1, 1), Dim: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(c *Catalog) []int64 { return append([]int64{}, c.ids...) }
	if got := ids(cat.ForProfile("alice")); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("alice sees %v", got)
	}
	if got := ids(cat.ForProfile("carol")); len(got) != 1 || got[0] != 1 {
		t.Fatalf("carol sees %v", got)
	}
	if cat.ForProfile("carol") != cat.ForProfile("") {
		t.Fatal("profiles without private music should share one view")
	}
	if cat.ForProfile("alice") != cat.ForProfile("alice") {
		t.Fatal("view rebuilt on every call")
	}
	alice := New(config.Config{})
	alice.Attach(cat.ForProfile("alice"), nil)
	if _, ok := alice.RowOf(3); ok {
		t.Fatal("bob's private track is in alice's index")
	}
	if rows := alice.RowsForArtist("Other"); len(rows) != 0 {
		t.Fatalf("artist lookup leaks bob's track: %v", rows)
	}
	row, ok := alice.RowOf(2)
	if !ok || alice.Vector(row)[1] < 0.99 {
		t.Fatal("alice's private vector lost")
	}

	shared, err := BuildCatalog([]db.TrackRow{{ID: 1, Path: "/music/a.flac", Embedding: vec(1, 0), Dim: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if shared.ForProfile("alice") != shared {
		t.Fatal("catalog without private tracks should be used as is")
	}
}
