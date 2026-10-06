package db

import (
	"fmt"
	"sync"
	"testing"
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
