package apitest

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFavoritesTrackArtistAlbumLifecycle(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("POST", "/api/favorites", `{"type":"track","track_id":11}`))
	if rec.Code != 200 {
		t.Fatalf("add track status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("GET", "/api/favorites/status?type=track&track_id=11", ""))
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["favorited"] != true {
		t.Fatalf("status=%v", st)
	}

	rec = serve(server, jsonReq("POST", "/api/favorites/toggle", `{"type":"artist","artist":"Artist"}`))
	if rec.Code != 200 {
		t.Fatalf("toggle artist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("POST", "/api/favorites/toggle", `{"type":"album","artist":"Artist","album":"Album"}`))
	if rec.Code != 200 {
		t.Fatalf("toggle album status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("GET", "/api/favorites", ""))
	var body struct {
		Count  int `json:"count"`
		Counts struct {
			Tracks  int `json:"tracks"`
			Artists int `json:"artists"`
			Albums  int `json:"albums"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || body.Counts.Artists != 1 || body.Counts.Albums != 1 {
		t.Fatalf("list counts unexpected: %+v", body)
	}

	rec = serve(server, jsonReq("DELETE", "/api/favorites", `{"type":"track","track_id":11}`))
	if rec.Code != 200 {
		t.Fatalf("remove status=%d", rec.Code)
	}
	if server.Store.FavoritesHas(11) {
		t.Fatal("track should be removed")
	}

	rec = serve(server, jsonReq("POST", "/api/favorites/toggle", `{"type":"artist","artist":"Artist"}`))
	if server.Store.FavArtistHas("Artist") {
		t.Fatal("artist should be toggled off")
	}
}

func TestFavoritesValidationErrors(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("GET", "/api/favorites/status?type=track", ""))
	if rec.Code != 400 {
		t.Fatalf("missing track_id status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("POST", "/api/favorites/toggle", `{"type":"nope"}`))
	if rec.Code != 400 {
		t.Fatalf("bad type status=%d", rec.Code)
	}
}

func TestLaterAddListRemoveAndMixPlay(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("POST", "/api/later", `{"track_id":22}`))
	if rec.Code != 200 {
		t.Fatalf("later add status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("GET", "/api/later", ""))
	var body struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 {
		t.Fatalf("later count=%d", body.Count)
	}

	rec = serve(server, jsonReq("POST", "/api/mixes/later/play", ""))
	if rec.Code != 200 {
		t.Fatalf("later play status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("DELETE", "/api/later", `{"track_id":22}`))
	if server.Store.LaterCount() != 0 {
		t.Fatal("later should be empty")
	}
}

func TestMixesShelfAndFavoritesPlay(t *testing.T) {
	server := openTestServer(t)
	if err := server.Store.FavoritesAdd(11); err != nil {
		t.Fatal(err)
	}
	seedPlaylist(t, server, time.Now().UTC().Format(time.RFC3339Nano), 11, 22)

	rec := serve(server, jsonReq("GET", "/api/mixes", ""))
	if rec.Code != 200 {
		t.Fatalf("mixes status=%d", rec.Code)
	}
	var shelf struct {
		Mixes []map[string]any `json:"mixes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shelf); err != nil {
		t.Fatal(err)
	}
	if len(shelf.Mixes) < 5 {
		t.Fatalf("expected mix shelf, got %d", len(shelf.Mixes))
	}

	rec = serve(server, jsonReq("POST", "/api/mixes/favorites/play", ""))
	if rec.Code != 200 {
		t.Fatalf("favorites play status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("POST", "/api/mixes/daily/play", `{"start_track_id":22}`))
	if rec.Code != 200 {
		t.Fatalf("daily mix play status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Index int `json:"index"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Index != 1 || resp.Count != 2 {
		t.Fatalf("daily play index/count=%d/%d", resp.Index, resp.Count)
	}
}
