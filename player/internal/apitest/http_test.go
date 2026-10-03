package apitest

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCatalogArtistsAlbumsTrack(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("GET", "/api/artists", ""))
	var artists struct {
		Count   int `json:"count"`
		Artists []struct {
			Artist string `json:"artist"`
			Tracks int    `json:"tracks"`
		} `json:"artists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &artists); err != nil {
		t.Fatal(err)
	}
	if artists.Count != 1 || artists.Artists[0].Tracks != 3 {
		t.Fatalf("artists=%+v", artists)
	}

	rec = serve(server, jsonReq("GET", "/api/albums", ""))
	var albums struct {
		Count  int `json:"count"`
		Albums []struct {
			Album string `json:"album"`
		} `json:"albums"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &albums); err != nil {
		t.Fatal(err)
	}
	if albums.Count != 1 || albums.Albums[0].Album != "Album" {
		t.Fatalf("albums=%+v", albums)
	}

	rec = serve(server, jsonReq("GET", "/api/tracks/11", ""))
	if rec.Code != 200 {
		t.Fatalf("track status=%d", rec.Code)
	}
	var track map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &track); err != nil {
		t.Fatal(err)
	}
	if track["id"].(float64) != 11 {
		t.Fatalf("track=%v", track)
	}

	rec = serve(server, jsonReq("GET", "/api/tracks/999", ""))
	if rec.Code != 404 {
		t.Fatalf("missing track status=%d", rec.Code)
	}
}

func TestPlayByArtistAndSessionJump(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("POST", "/api/play", `{"artist":"Artist","start_track_id":33}`))
	if rec.Code != 200 {
		t.Fatalf("play status=%d body=%s", rec.Code, rec.Body.String())
	}
	var play struct {
		SessionID string `json:"session_id"`
		Index     int    `json:"index"`
		Count     int    `json:"count"`
		Fixed     bool   `json:"fixed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatal(err)
	}
	if !play.Fixed || play.Count != 3 || play.Index != 2 {
		t.Fatalf("play=%+v", play)
	}

	rec = serve(server, jsonReq("POST", "/api/session/jump",
		fmt.Sprintf(`{"session_id":%q,"track_id":11}`, play.SessionID)))
	if rec.Code != 200 {
		t.Fatalf("jump status=%d body=%s", rec.Code, rec.Body.String())
	}
	var jumped struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &jumped); err != nil {
		t.Fatal(err)
	}
	if jumped.Index != 0 {
		t.Fatalf("jumped index=%d", jumped.Index)
	}
}

func TestLyricsAbsentAndPresent(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("GET", "/api/tracks/11/lyrics", ""))
	var absent map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &absent); err != nil {
		t.Fatal(err)
	}
	if absent["status"] != "absent" {
		t.Fatalf("absent=%v", absent)
	}

	if _, err := server.Store.DB.Exec(`
INSERT INTO lyrics(track_id, plain_lyrics, synced_lyrics, source, source_id, instrumental, status, updated_at)
VALUES (11,'hello','','local','',0,'ok',?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	rec = serve(server, jsonReq("GET", "/api/tracks/11/lyrics", ""))
	var ly struct {
		PlainLyrics string `json:"plain_lyrics"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ly); err != nil {
		t.Fatal(err)
	}
	if ly.PlainLyrics != "hello" || ly.Status != "ok" {
		t.Fatalf("lyrics=%+v", ly)
	}
}

func TestJobsLocalFallbackListGet(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("POST", "/api/jobs/mix_pack", ""))
	if rec.Code != 200 {
		t.Fatalf("enqueue status=%d body=%s", rec.Code, rec.Body.String())
	}
	var enq struct {
		OK     bool   `json:"ok"`
		ID     int64  `json:"id"`
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
		Via    string `json:"via"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &enq); err != nil {
		t.Fatal(err)
	}
	if !enq.OK || enq.ID == 0 || enq.JobID != enq.ID || enq.Via != "local_db" {
		t.Fatalf("enqueue=%+v", enq)
	}

	rec = serve(server, jsonReq("GET", "/api/jobs", ""))
	var listed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count < 1 {
		t.Fatal("expected listed jobs")
	}

	rec = serve(server, jsonReq("GET", fmt.Sprintf("/api/jobs/%d", enq.ID), ""))
	if rec.Code != 200 {
		t.Fatalf("get job status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("POST", "/api/library/rescan", ""))
	if rec.Code != 200 {
		t.Fatalf("rescan status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rescan struct {
		ID    int64 `json:"id"`
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rescan); err != nil {
		t.Fatal(err)
	}
	if rescan.ID == 0 || rescan.JobID != rescan.ID {
		t.Fatalf("rescan=%+v", rescan)
	}
}

func TestDiscoverTipsProfileHealthStatusMetrics(t *testing.T) {
	server := openTestServer(t)
	if _, err := server.Store.DB.Exec(`
INSERT INTO discover_tips(kind, artist, album, score, track_ids_json, explanation, created_at)
VALUES ('new_album','Artist','Album',1.5,'[11,22]','fresh',?),
       ('resurfaced','Artist','Album',0.5,'[33]','old',?)`,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}

	rec := serve(server, jsonReq("GET", "/api/discover/albums", ""))
	var tips struct {
		Tips []map[string]any `json:"tips"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tips); err != nil {
		t.Fatal(err)
	}
	if len(tips.Tips) != 1 {
		t.Fatalf("new album tips=%d", len(tips.Tips))
	}

	rec = serve(server, jsonReq("GET", "/api/discover/resurfaced", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &tips); err != nil {
		t.Fatal(err)
	}
	if len(tips.Tips) != 1 {
		t.Fatalf("resurfaced tips=%d", len(tips.Tips))
	}

	rec = serve(server, jsonReq("GET", "/api/profile", ""))
	if rec.Code != 200 {
		t.Fatalf("profile status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("GET", "/api/health", ""))
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["ok"] != true {
		t.Fatalf("health=%v", health)
	}

	rec = serve(server, jsonReq("GET", "/api/status", ""))
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["tracks"].(float64) != 3 {
		t.Fatalf("status tracks=%v", status["tracks"])
	}
	if status["db_backend"] != "sqlite" {
		t.Fatalf("status db_backend=%v, want sqlite", status["db_backend"])
	}

	rec = serve(server, jsonReq("GET", "/api/metrics/weekly", ""))
	if rec.Code != 200 {
		t.Fatalf("weekly metrics status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("GET", "/api/openapi.json", ""))
	if rec.Code != 200 || len(rec.Body.Bytes()) < 10 {
		t.Fatalf("openapi status=%d len=%d", rec.Code, rec.Body.Len())
	}

	rec = serve(server, jsonReq("GET", "/manifest.webmanifest", ""))
	if rec.Code != 200 || rec.Header().Get("Content-Type") == "" {
		t.Fatalf("manifest status=%d ct=%s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestRecommendFavoritesEmpty(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("GET", "/api/recommend/favorites", ""))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["empty"] != true {
		t.Fatalf("expected empty favorites recommend: %#v", body)
	}
}

func TestRecommendHTTPErrors(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("GET", "/api/similar/bad", ""))
	if rec.Code != 400 {
		t.Fatalf("bad similar id status=%d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("bad similar content-type=%q", got)
	}
	var apiErr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Error != "bad id" || apiErr.Code != "bad_id" {
		t.Fatalf("bad similar error=%+v", apiErr)
	}

	rec = serve(server, jsonReq("GET", "/api/similar/artists", ""))
	if rec.Code != 400 {
		t.Fatalf("missing artist status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("GET", "/api/recommend/seed", ""))
	if rec.Code != 400 {
		t.Fatalf("missing seed track status=%d", rec.Code)
	}
}

func TestRecommendationMetricsIncludesLatency(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("GET", "/api/similar/11", ""))
	if rec.Code != 200 {
		t.Fatalf("similar status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("GET", "/api/metrics/recommendations", ""))
	if rec.Code != 200 {
		t.Fatalf("metrics status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Window    string `json:"window"`
		LatencyMS map[string]struct {
			Count uint64 `json:"count"`
		} `json:"latency_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Window != "90d" {
		t.Fatalf("window=%q, want 90d", body.Window)
	}
	if body.LatencyMS["similar"].Count < 1 {
		t.Fatalf("expected similar latency samples, got %#v", body.LatencyMS)
	}
}
