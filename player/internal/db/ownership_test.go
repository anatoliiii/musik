package db

import (
	"context"
	"testing"
)

func TestPersonalRepositoriesIsolateProfiles(t *testing.T) {
	root, _ := openTestStore(t)
	ctx := context.Background()
	_, pa, err := root.CreateUserWithDefaultProfile(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	_, pb, err := root.CreateUserWithDefaultProfile(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	a, b := root.ForProfile(pa.ID), root.ForProfile(pb.ID)
	if _, err := root.DB.Exec(`INSERT INTO tracks(id,path,title) VALUES (1,'/a','A')`); err != nil {
		t.Fatal(err)
	}
	if err := a.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	if b.FavoritesHas(1) {
		t.Fatal("B sees A favorite")
	}
	if err := b.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	if err := a.FavoritesRemove(1); err != nil {
		t.Fatal(err)
	}
	if !b.FavoritesHas(1) {
		t.Fatal("A removed B favorite")
	}
	if _, err := a.InsertListen(1, "finish", "test", "", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ids, err := b.RecentTrackIDs(24, 10); err != nil || len(ids) != 0 {
		t.Fatalf("B sees A history: %v %v", ids, err)
	}
	playlist, err := a.CreateUserPlaylist(UserPlaylist{Name: "Private", Type: "manual", Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if foreign, err := b.GetUserPlaylist(playlist.ID); err != nil || foreign != nil {
		t.Fatalf("foreign playlist visible: %#v %v", foreign, err)
	}
	if _, err := b.AddPlaylistItem(playlist.ID, UserPlaylistItem{TrackID: 1}); err == nil {
		t.Fatal("B inserted into A playlist")
	}
	tag, err := a.CreateCustomTag("private")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.TagTrack(tag.ID, 1); err == nil {
		t.Fatal("B attached A tag")
	}
	if _, err := b.CreateCustomTag("private"); err != nil {
		t.Fatal("same tag name must be allowed in different profiles", err)
	}
	if err := a.SaveRadioPrefs(0.11, 0.32); err != nil {
		t.Fatal(err)
	}
	prefs, err := b.LoadRadioPrefs()
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ExploreLo == 0.11 {
		t.Fatal("B sees A radio preferences")
	}
}
