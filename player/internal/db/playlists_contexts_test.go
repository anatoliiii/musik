package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func insertTracks(t *testing.T, store *Store, n int) {
	t.Helper()
	for id := 1; id <= n; id++ {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, year, duration, created_at, updated_at) VALUES (?,?,?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, fmt.Sprintf("/t-%d.flac", id), "Title", "Artist", "Album", 2012, 180,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTasteContextCRUDAndSeeds(t *testing.T) {
	store, _ := openTestStore(t)
	ctx, err := store.CreateTasteContext(TasteContext{
		Kind: "mood", Name: "Ночная дорога", Influence: 1.2, LearningEnabled: true,
		Seeds: ContextSeeds{SchemaVersion: 1, Tracks: []int64{1}, Artists: []string{"Artist"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTasteContext(ctx.ID)
	if err != nil || got == nil || got.Name != "Ночная дорога" || got.Seeds.Artists[0] != "Artist" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if err := store.ArchiveTasteContext(ctx.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := store.ListTasteContexts(false)
	if len(list) != 0 {
		t.Fatalf("archived context still listed: %+v", list)
	}
}

func TestCommitTasteContextKeepsVectorWithRow(t *testing.T) {
	store, _ := openTestStore(t)
	created, err := store.CommitTasteContext(context.Background(), TasteContext{
		Kind: "mood", Name: "Вечер", Influence: 1, LearningEnabled: true,
	}, []byte{1, 2, 3, 4}, 1, true)
	if err != nil || created == nil || !created.HasVector {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if err := store.UpsertTasteContextState(TasteContextState{
		ContextID: created.ID, PositiveVector: []byte{1, 2, 3, 4}, EmbeddingDim: 1, PositiveSamples: 4,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.CommitTasteContext(context.Background(), *created, []byte{5, 6, 7, 8}, 1, false)
	if err != nil || updated == nil || !updated.HasVector {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	state, err := store.LoadTasteContextState(created.ID)
	if err != nil || state == nil || state.PositiveSamples != 4 || !bytes.Equal(state.PositiveVector, []byte{5, 6, 7, 8}) {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := store.CommitTasteContext(context.Background(), TasteContext{ID: "missing", Name: "нет"}, []byte{1}, 1, false); !errors.Is(err, ErrContextNotFound) {
		t.Fatalf("missing context: %v", err)
	}
}

func TestToggleFavoriteFlipsInOneStep(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 1)
	on, err := store.ToggleFavorite(context.Background(), "track", 1, "", "")
	if err != nil || !on.Favorited || on.Tracks != 1 {
		t.Fatalf("on=%+v err=%v", on, err)
	}
	off, err := store.ToggleFavorite(context.Background(), "track", 1, "", "")
	if err != nil || off.Favorited || off.Tracks != 0 {
		t.Fatalf("off=%+v err=%v", off, err)
	}
	artist, err := store.ToggleFavorite(context.Background(), "artist", 0, "Artist", "")
	if err != nil || !artist.Favorited || artist.Artists != 1 {
		t.Fatalf("artist=%+v err=%v", artist, err)
	}
}

func TestRadioRulesScopeAndUndo(t *testing.T) {
	store, _ := openTestStore(t)
	rule, err := store.CreateRadioRule(RadioRule{
		TargetType: "artist", Action: "block", Scope: "global", TargetKey: "artist", Strength: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveRadioRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := store.UndoLastRadioRule()
	if err != nil || restored == nil || restored.ID != rule.ID || restored.ArchivedAt != "" {
		t.Fatalf("undo failed %+v %v", restored, err)
	}
}

func TestManualPlaylistOrderSurvivesConcurrentAdds(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 8)
	pl, err := store.CreateUserPlaylist(UserPlaylist{Name: "Вечер", Type: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 6)
	for id := int64(1); id <= 6; id++ {
		wg.Add(1)
		go func(trackID int64) {
			defer wg.Done()
			_, err := store.AddPlaylistItem(pl.ID, UserPlaylistItem{TrackID: trackID, Source: "manual"})
			errCh <- err
		}(id)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetUserPlaylist(pl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 6 {
		t.Fatalf("tracks=%d", len(got.Tracks))
	}
	seenPos := map[int]bool{}
	itemIDs := make([]string, 0, 6)
	for _, item := range got.Tracks {
		if seenPos[item.Position] {
			t.Fatalf("duplicate position %d", item.Position)
		}
		seenPos[item.Position] = true
		itemIDs = append(itemIDs, item.ItemID)
	}
	rev := append([]string(nil), itemIDs...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if err := store.ReorderPlaylistItems(pl.ID, rev); err != nil {
		t.Fatal(err)
	}
	again, _ := store.GetUserPlaylist(pl.ID)
	if again.Tracks[0].ItemID != rev[0] || again.Tracks[len(again.Tracks)-1].ItemID != rev[len(rev)-1] {
		t.Fatalf("reorder failed %+v", again.Tracks)
	}
}

func TestUserPlaylistsVisibleAndRemovable(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 2)
	if _, err := store.DB.Exec(`
INSERT INTO playlists(id, kind, name, created_at, type, profile_id) VALUES
 (21, 'user', 'Старый', '2026-01-01T00:00:00Z', 'generated', ?),
 (22, 'daily', 'Микс', '2026-01-01T00:00:00Z', 'generated', ?)`, store.ProfileID, store.ProfileID); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListUserPlaylists(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Старый" || list[0].Type != "manual" {
		t.Fatalf("list=%+v", list)
	}
	if _, err := store.DB.Exec(`
INSERT INTO playlist_tracks(item_id, playlist_id, position, track_id, added_at, source, profile_id)
VALUES ('', 21, 0, 1, '2026-01-01T00:00:00Z', 'manual', ?)`, store.ProfileID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemovePlaylistItem(21, "1"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListPlaylistItems(21)
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := store.DeleteUserPlaylist(21); err != nil {
		t.Fatal(err)
	}
}

func TestListUserPlaylistsCountsAndBatchAddRollsBack(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 2)
	pl, err := store.CreateUserPlaylist(UserPlaylist{Name: "Счёт", Type: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	added, err := store.AddPlaylistItems(context.Background(), pl.ID, []UserPlaylistItem{
		{TrackID: 1, Source: "radio"},
		{TrackID: 2, Source: "radio"},
		{TrackID: 1, Source: "radio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Added != 2 || added.Skipped != 1 || added.Last.TrackID != 2 {
		t.Fatalf("batch=%+v", added)
	}
	list, err := store.ListUserPlaylists(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TrackCount != 2 {
		t.Fatalf("list=%+v", list)
	}
	if _, err := store.AddPlaylistItems(context.Background(), pl.ID, []UserPlaylistItem{
		{TrackID: 1, Source: "radio"},
		{TrackID: 99, Source: "radio"},
	}); err == nil {
		t.Fatal("expected foreign-key failure")
	}
	got, err := store.GetUserPlaylist(pl.ID)
	if err != nil || got == nil || got.TrackCount != 2 {
		t.Fatalf("partial batch stuck: %+v err=%v", got, err)
	}
	if _, err := store.AddPlaylistItems(context.Background(), 404, []UserPlaylistItem{{TrackID: 1, Source: "manual"}}); !errors.Is(err, ErrPlaylistNotFound) {
		t.Fatalf("missing playlist: %v", err)
	}
	if _, err := store.CreateUserPlaylistWithItems(context.Background(), UserPlaylist{Name: "Битый", Type: "manual"}, []UserPlaylistItem{
		{TrackID: 99, Source: "manual"},
	}); err == nil {
		t.Fatal("expected create+items rollback")
	}
	again, err := store.ListUserPlaylists([]string{"manual"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range again {
		if item.Name == "Битый" {
			t.Fatalf("rolled-back playlist is visible: %+v", item)
		}
	}
}

func TestLoadTransitionGraphPrefersDecayedWeight(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 6)
	now := time.Now().UTC()
	if err := store.BumpTransition(1, 2, 5); err != nil {
		t.Fatal(err)
	}
	if err := store.BumpTransition(1, 4, 9); err != nil {
		t.Fatal(err)
	}
	if err := store.BumpTransition(1, 5, 4); err != nil {
		t.Fatal(err)
	}
	insertDecay := func(to int64, weight float64, updated time.Time) {
		t.Helper()
		if _, err := store.DB.Exec(`
INSERT INTO transition_stats(
  from_id, to_id, manual_count, radio_count, finished_count, partial_count,
  skip_count, decayed_weight, updated_at, profile_id
) VALUES (1, ?, 0, 1, 0, 0, 0, ?, ?, ?)`, to, weight, updated.Format(time.RFC3339Nano), store.ProfileID); err != nil {
			t.Fatal(err)
		}
	}
	insertDecay(3, 0.4, now)
	insertDecay(4, 0.2, now)
	insertDecay(5, 0, now)
	insertDecay(6, -0.3, now)
	insertDecay(2, 2, now.Add(-90*24*time.Hour))

	graph, err := store.LoadTransitionGraph()
	if err != nil {
		t.Fatal(err)
	}
	edges := graph[1]
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-6 }
	if !near(edges[2], 1) {
		t.Fatalf("stale decay edge=%v, want about 1", edges[2])
	}
	if !near(edges[3], 0.4) {
		t.Fatalf("decay-only edge=%v", edges[3])
	}
	if !near(edges[4], 0.2) {
		t.Fatalf("decay should replace raw weight, got %v", edges[4])
	}
	if _, ok := edges[5]; ok {
		t.Fatalf("zero decay row fell back to raw weight: %v", edges[5])
	}
	if !near(edges[6], -0.3) {
		t.Fatalf("negative skip edge=%v", edges[6])
	}
}

func TestOutcomeTransitionDecay(t *testing.T) {
	store, _ := openTestStore(t)
	insertTracks(t, store, 2)
	if err := store.RecordOutcomeTransition(1, 2, "finished", "radio"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOutcomeTransition(1, 2, "early_skip", "manual"); err != nil {
		t.Fatal(err)
	}
	weights, err := store.LoadTransitionStatsFrom(1)
	if err != nil {
		t.Fatal(err)
	}
	if weights[2] <= 0 {
		t.Fatalf("weight=%v", weights)
	}
}
