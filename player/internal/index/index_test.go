package index

import (
	"sort"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

func testIndex(t *testing.T) *Index {
	t.Helper()
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 10, Artist: "Artist A", Album: "First", Title: "A1", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 11, Artist: "Artist A", Album: "First", Title: "A2", Embedding: Float32Bytes([]float32{0.8, 0.2}), Dim: 2},
		{ID: 12, Artist: "Artist B", Album: "Second", Title: "B1", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 13, Artist: "Artist C", Album: "Second", Title: "C1", Embedding: Float32Bytes([]float32{-1, 0}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestTopKMatchesFullSortAndExcludes(t *testing.T) {
	idx := testIndex(t)
	query := []float32{1, 0}
	sims := idx.SimsTo(query)
	type pair struct {
		row   int
		score float32
	}
	var want []pair
	for row, score := range sims {
		if idx.MetaAt(row).ID != 11 {
			want = append(want, pair{row, score})
		}
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].score == want[j].score {
			return want[i].row < want[j].row
		}
		return want[i].score > want[j].score
	})
	want = want[:2]

	got := idx.TopK(query, 2, map[int64]bool{11: true})
	if len(got) != len(want) {
		t.Fatalf("TopK len=%d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Row != want[i].row || got[i].Score != want[i].score {
			t.Fatalf("TopK[%d]=%+v, want row=%d score=%f", i, got[i], want[i].row, want[i].score)
		}
	}
}

func TestTopKUsesStableRowTieBreak(t *testing.T) {
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 3, Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	got := idx.TopK([]float32{1, 0}, 2, nil)
	if len(got) != 2 || got[0].Row != 0 || got[1].Row != 1 {
		t.Fatalf("stable ties = %+v, want rows 0,1", got)
	}
}

func TestFusedSimsMatchesIndependentScans(t *testing.T) {
	idx := testIndex(t)
	queries := [][]float32{{1, 0}, {0, 1}, {-1, 0}}
	got := idx.FusedSimsTo(queries...)
	if len(got) != len(queries) {
		t.Fatalf("query results=%d, want %d", len(got), len(queries))
	}
	for q, query := range queries {
		want := idx.SimsTo(query)
		for row := range want {
			if got[q][row] != want[row] {
				t.Fatalf("query %d row %d = %f, want %f", q, row, got[q][row], want[row])
			}
		}
	}
}

func TestArtistAndAlbumIndexes(t *testing.T) {
	idx := testIndex(t)
	if got := idx.RowsForArtist(" artist a "); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("artist rows=%v, want [0 1]", got)
	}
	if got := idx.RowsForAlbum("", "second"); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("album rows=%v, want [2 3]", got)
	}
	if got := idx.RowsForAlbum("artist b", "second"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("artist album rows=%v, want [2]", got)
	}
	artists := idx.ArtistCentroids()
	if len(artists) != 3 {
		t.Fatalf("artist centroids=%d, want 3", len(artists))
	}
	for _, group := range artists {
		if len(group.Vector) != 2 {
			t.Fatalf("bad centroid for %q: %v", group.Artist, group.Vector)
		}
	}
	albums := idx.AlbumCentroids()
	if len(albums) != 3 {
		t.Fatalf("album centroids=%d, want 3", len(albums))
	}
}

func TestIDsForTargetUsesRuleKeys(t *testing.T) {
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "Gone", Title: "Hidden", Album: "Al", ClusterID: 7, FileMD5: "abc", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "Kept", Title: "Stay", Album: "Other", FileMD5: "abc", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	if got := idx.IDsForTarget("artist", ArtistKey("Gone")); len(got) != 1 || got[0] != 1 {
		t.Fatalf("artist=%v", got)
	}
	if got := idx.IDsForTarget("artist", "kept-segment-only"); got != nil {
		t.Fatalf("unknown artist=%v", got)
	}
	if got := idx.IDsForTarget("album", AlbumKey("Gone", "Al")); len(got) != 1 || got[0] != 1 {
		t.Fatalf("album=%v", got)
	}
	if got := idx.IDsForTarget("track", TrackKey(2)); len(got) != 1 || got[0] != 2 {
		t.Fatalf("track=%v", got)
	}
	if got := idx.IDsForTarget("cluster", ClusterKey(7)); len(got) != 1 || got[0] != 1 {
		t.Fatalf("cluster=%v", got)
	}
	got := idx.IDsForTarget("song", "abc")
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("md5=%v", got)
	}
	if got := idx.IDsForTarget("song", SongKey("Gone", "Hidden")); len(got) != 1 || got[0] != 1 {
		t.Fatalf("song key=%v", got)
	}
}

func TestForgottenRowsUsesLastFinish(t *testing.T) {
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "A", Title: "Old", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "B", Title: "Recent", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 3, Artist: "C", Title: "Unstamped", Embedding: Float32Bytes([]float32{-1, 0}), Dim: 2},
	}
	cat, err := BuildCatalog(rows)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Add(-2 * 24 * time.Hour).Format(time.RFC3339Nano)
	idx.Attach(cat, map[int64]db.TrackStats{
		1: {Finishes: 4, Likes: 1, LastFinishedAt: old, LastPlayedAt: recent},
		2: {Finishes: 4, LastFinishedAt: recent, LastPlayedAt: old},
		3: {Finishes: 4, LastPlayedAt: old},
	})
	got := idx.ForgottenRows(3, time.Now().UTC().Add(-60*24*time.Hour))
	if len(got) != 1 || idx.MetaAt(got[0]).ID != 1 {
		ids := make([]int64, len(got))
		for i, row := range got {
			ids[i] = idx.MetaAt(row).ID
		}
		t.Fatalf("forgotten=%v, want [1]", ids)
	}
	if meta := idx.MetaAt(got[0]); meta.Likes != 1 || meta.LastPlayedAt.IsZero() {
		t.Fatalf("counters=%+v", meta)
	}
}

func TestCloneIDsIncludesMD5AndSongKeyDuplicates(t *testing.T) {
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "Same", Title: "Song", FileMD5: "abc", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "Same", Title: "Song", FileMD5: "other", Embedding: Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 3, Artist: "Other", Title: "Track", FileMD5: "abc", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 4, Artist: "Solo", Title: "Alone", FileMD5: "zzz", Embedding: Float32Bytes([]float32{-1, 0}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	got := idx.CloneIDs(1)
	seen := map[int64]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, want := range []int64{1, 2, 3} {
		if !seen[want] {
			t.Fatalf("CloneIDs(1)=%v, missing %d", got, want)
		}
	}
	if seen[4] {
		t.Fatalf("CloneIDs(1)=%v unexpectedly includes unrelated id 4", got)
	}
	if got := idx.CloneIDs(99); len(got) != 1 || got[0] != 99 {
		t.Fatalf("missing track CloneIDs=%v, want [99]", got)
	}
}
