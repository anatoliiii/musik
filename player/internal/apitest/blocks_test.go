package apitest

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestBlockArtistLeavesGeneratedMusic(t *testing.T) {
	server := openTestServer(t)
	rows := []db.TrackRow{
		{ID: 11, Title: "Hidden", Artist: "Gone", Album: "Hide", Duration: 180, Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 12, Title: "Hidden too", Artist: "Gone", Album: "Hide", Duration: 180, Embedding: index.Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 22, Title: "Stay", Artist: "Kept", Album: "Keep", Duration: 180, Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 33, Title: "Stay too", Artist: "Kept", Album: "Keep", Duration: 180, Embedding: index.Float32Bytes([]float32{0.1, 0.9}), Dim: 2},
	}
	for _, row := range rows {
		path := filepath.Join(t.TempDir(), row.Title+".flac")
		if _, err := server.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, duration) VALUES (?,?,?,?,?,?)
			 ON CONFLICT(id) DO UPDATE SET path=excluded.path, title=excluded.title, artist=excluded.artist, album=excluded.album`,
			row.ID, path, row.Title, row.Artist, row.Album, row.Duration,
		); err != nil {
			t.Fatal(err)
		}
		row.Path = path
		rows[indexOfRow(rows, row.ID)].Path = path
	}
	loadIndex(t, server, rows)

	if _, err := server.Store.DB.Exec(`
INSERT INTO playlists(id, kind, name, created_at) VALUES (1, 'daily', 'Daily', '2026-10-01T00:00:00Z');
INSERT INTO playlist_tracks(playlist_id, position, track_id, explanation) VALUES
 (1,0,11,''),(1,1,22,''),(1,2,12,''),(1,3,33,'')`); err != nil {
		t.Fatal(err)
	}
	smart, err := server.Store.CreateUserPlaylist(db.UserPlaylist{Name: "Smart", Type: "smart", Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := server.Store.CreateUserPlaylist(db.UserPlaylist{Name: "Mine", Type: "manual", Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{11, 22, 12, 33} {
		if _, err := server.Store.AddPlaylistItem(smart.ID, db.UserPlaylistItem{TrackID: id, Source: "rule"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := server.Store.AddPlaylistItem(manual.ID, db.UserPlaylistItem{TrackID: 11, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := server.Store.FavoritesAdd(22); err != nil {
		t.Fatal(err)
	}
	if err := server.Store.LaterAdd(11); err != nil {
		t.Fatal(err)
	}

	if n := mixTrackCount(t, server, "daily"); n != 4 {
		t.Fatalf("daily tracks before block = %d", n)
	}

	rec := serve(server, jsonReq("POST", "/api/radio/start", `{"seed_track_id":11}`))
	if rec.Code != 200 {
		t.Fatalf("radio start status=%d body=%s", rec.Code, rec.Body)
	}
	var started struct {
		SessionID string `json:"session_id"`
		Current   struct {
			ID int64 `json:"id"`
		} `json:"current"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Current.ID != 11 {
		t.Fatalf("seeded radio current=%d", started.Current.ID)
	}

	rec = serve(server, jsonReq("POST", "/api/rules", `{
		"target_type":"artist","action":"block","scope":"global","preset":"week",
		"artist":"Gone","track_id":11,"session_id":"`+started.SessionID+`"
	}`))
	if rec.Code != 200 {
		t.Fatalf("rule status=%d body=%s", rec.Code, rec.Body)
	}
	var created struct {
		TargetKey string `json:"target_key"`
		Playback  struct {
			Current struct {
				ID int64 `json:"id"`
			} `json:"current"`
			Queue []struct {
				TrackID int64 `json:"track_id"`
			} `json:"queue"`
		} `json:"playback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.TargetKey != "gone" {
		t.Fatalf("target_key=%q", created.TargetKey)
	}
	live := []int64{created.Playback.Current.ID}
	for _, item := range created.Playback.Queue {
		live = append(live, item.TrackID)
	}
	assertKeptOnly(t, "live radio", live)

	if n := mixTrackCount(t, server, "daily"); n != 2 {
		t.Fatalf("daily tracks after block = %d", n)
	}
	rec = serve(server, jsonReq("POST", "/api/mixes/daily/play", ""))
	if rec.Code != 200 {
		t.Fatalf("daily play status=%d body=%s", rec.Code, rec.Body)
	}
	var played struct {
		Count  int `json:"count"`
		Tracks []struct {
			ID int64 `json:"id"`
		} `json:"tracks"`
		Queue []struct {
			TrackID int64 `json:"track_id"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &played); err != nil {
		t.Fatal(err)
	}
	if played.Count != 2 {
		t.Fatalf("daily play count=%d", played.Count)
	}
	got := make([]int64, 0, len(played.Tracks)+len(played.Queue))
	for _, track := range played.Tracks {
		got = append(got, track.ID)
	}
	for _, item := range played.Queue {
		got = append(got, item.TrackID)
	}
	assertKeptOnly(t, "daily mix", got)

	rec = serve(server, jsonReq("GET", "/api/playlists/"+strconv.FormatInt(smart.ID, 10), ""))
	assertPlaylistKeepsGone(t, "smart", rec, false)
	rec = serve(server, jsonReq("POST", "/api/playlists/"+strconv.FormatInt(smart.ID, 10)+"/play", ""))
	if rec.Code != 200 {
		t.Fatalf("smart play status=%d body=%s", rec.Code, rec.Body)
	}
	var smartPlay struct {
		Tracks []struct {
			ID int64 `json:"id"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &smartPlay); err != nil {
		t.Fatal(err)
	}
	smartIDs := make([]int64, len(smartPlay.Tracks))
	for i, track := range smartPlay.Tracks {
		smartIDs[i] = track.ID
	}
	assertKeptOnly(t, "smart play", smartIDs)

	rec = serve(server, jsonReq("GET", "/api/playlists", ""))
	var listed struct {
		Playlists []struct {
			ID         int64 `json:"id"`
			TrackCount int   `json:"track_count"`
		} `json:"playlists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, pl := range listed.Playlists {
		switch pl.ID {
		case smart.ID:
			if pl.TrackCount != 2 {
				t.Fatalf("smart count=%d", pl.TrackCount)
			}
		case manual.ID:
			if pl.TrackCount != 1 {
				t.Fatalf("manual count=%d", pl.TrackCount)
			}
		}
	}
	rec = serve(server, jsonReq("GET", "/api/playlists/"+strconv.FormatInt(manual.ID, 10), ""))
	assertPlaylistKeepsGone(t, "manual", rec, true)

	rec = serve(server, jsonReq("POST", "/api/mixes/later/play", ""))
	if rec.Code != 200 {
		t.Fatalf("later play status=%d body=%s", rec.Code, rec.Body)
	}
	var laterPlay struct {
		Tracks []struct {
			ID int64 `json:"id"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &laterPlay); err != nil {
		t.Fatal(err)
	}
	if len(laterPlay.Tracks) != 1 || laterPlay.Tracks[0].ID != 11 {
		t.Fatalf("later should keep the saved track, got %+v", laterPlay.Tracks)
	}

	rec = serve(server, jsonReq("GET", "/api/recommend/favorites", ""))
	if rec.Code != 200 {
		t.Fatalf("recommend status=%d body=%s", rec.Code, rec.Body)
	}
	var recs struct {
		Tracks []struct {
			ID     int64  `json:"id"`
			Artist string `json:"artist"`
		} `json:"tracks"`
		Artists []struct {
			Artist string `json:"artist"`
		} `json:"artists"`
		Albums []struct {
			Artist string `json:"artist"`
		} `json:"albums"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &recs); err != nil {
		t.Fatal(err)
	}
	seenKept := false
	for _, track := range recs.Tracks {
		if track.ID == 11 || track.ID == 12 || track.Artist == "Gone" {
			t.Fatalf("recommendation track %+v", track)
		}
		if track.ID == 33 {
			seenKept = true
		}
	}
	if !seenKept {
		t.Fatalf("expected Kept track in recommendations, got %+v", recs.Tracks)
	}
	for _, artist := range recs.Artists {
		if artist.Artist == "Gone" {
			t.Fatalf("recommendation artist %+v", artist)
		}
	}
	for _, album := range recs.Albums {
		if album.Artist == "Gone" {
			t.Fatalf("recommendation album %+v", album)
		}
	}

	rec = serve(server, jsonReq("GET", "/api/similar/22", ""))
	if rec.Code != 200 {
		t.Fatalf("similar status=%d body=%s", rec.Code, rec.Body)
	}
	var similar []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &similar); err != nil {
		t.Fatal(err)
	}
	for _, hit := range similar {
		if hit.ID == 11 || hit.ID == 12 {
			t.Fatalf("similar still has %d", hit.ID)
		}
	}

	rec = serve(server, jsonReq("POST", "/api/radio/start", `{}`))
	if rec.Code != 200 {
		t.Fatalf("second radio status=%d body=%s", rec.Code, rec.Body)
	}
	var again struct {
		Current struct {
			ID int64 `json:"id"`
		} `json:"current"`
		Queue []struct {
			TrackID int64 `json:"track_id"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	ids := []int64{again.Current.ID}
	for _, item := range again.Queue {
		ids = append(ids, item.TrackID)
	}
	assertKeptOnly(t, "new radio", ids)
}

func indexOfRow(rows []db.TrackRow, id int64) int {
	for i, row := range rows {
		if row.ID == id {
			return i
		}
	}
	return 0
}

func mixTrackCount(t *testing.T, server *api.Server, kind string) int {
	t.Helper()
	rec := serve(server, jsonReq("GET", "/api/mixes", ""))
	if rec.Code != 200 {
		t.Fatalf("mixes status=%d body=%s", rec.Code, rec.Body)
	}
	var shelf struct {
		Mixes []map[string]any `json:"mixes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shelf); err != nil {
		t.Fatal(err)
	}
	for _, mix := range shelf.Mixes {
		if mix["kind"] == kind {
			return int(mix["tracks"].(float64))
		}
	}
	t.Fatalf("mix %s missing", kind)
	return 0
}

func assertPlaylistKeepsGone(t *testing.T, name string, rec *httptest.ResponseRecorder, keep bool) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("%s status=%d body=%s", name, rec.Code, rec.Body)
	}
	var pl struct {
		Tracks []struct {
			TrackID int64 `json:"track_id"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pl); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, track := range pl.Tracks {
		if track.TrackID == 11 || track.TrackID == 12 {
			found = true
		}
	}
	if keep && !found {
		t.Fatalf("%s dropped a manually saved track: %+v", name, pl.Tracks)
	}
	if !keep && found {
		t.Fatalf("%s still has a blocked track: %+v", name, pl.Tracks)
	}
}

func assertKeptOnly(t *testing.T, name string, ids []int64) {
	t.Helper()
	if len(ids) == 0 {
		t.Fatalf("%s is empty", name)
	}
	for _, id := range ids {
		if id == 11 || id == 12 || id == 0 {
			t.Fatalf("%s contains %d in %v", name, id, ids)
		}
	}
}
