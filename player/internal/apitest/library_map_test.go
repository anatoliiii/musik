package apitest

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLibraryMapBeforeFirstBuild(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("GET", "/api/library/map", ""))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Ready  bool            `json:"ready"`
		Total  int             `json:"total"`
		Points [][4]float64    `json:"points"`
		Labels []any           `json:"labels"`
		Genres json.RawMessage `json:"genres"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ready || out.Total != 3 || len(out.Points) != 0 || len(out.Labels) != 0 || string(out.Genres) != "[]" {
		t.Fatalf("empty map = %s", rec.Body.String())
	}
}

func TestLibraryMapHidesOtherProfilesPrivateTracks(t *testing.T) {
	server := openTestServer(t)
	dir := t.TempDir()
	for id, path := range map[int64]string{
		44: filepath.Join(dir, "_private", "someone-else", "x.mp3"),
		55: filepath.Join(dir, "_private", server.Store.ProfileID, "y.mp3"),
	} {
		if _, err := server.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, created_at, updated_at) VALUES (?, ?, 'T', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, path,
		); err != nil {
			t.Fatal(err)
		}
	}
	doc := `{"version":1,"method":"umap","updated_at":"2026-10-27T00:00:00Z","genres":["Rock"],` +
		`"points":[[11,0.1,0.2,0],[22,0.3,0.4,-1],[44,0.5,0.5,0],[55,0.9,0.8,-1]],` +
		`"labels":[{"id":"chill","name":"Чилл","color":"#5fb3a8","x":0.2,"y":0.3,"track_ids":[44,11,55]},` +
		`{"id":"sad","name":"Грустное","color":"#5a9fd8","x":0.5,"y":0.5,"track_ids":[44]}]}`
	if _, err := server.Store.DB.Exec(`INSERT INTO scan_state(key, value) VALUES ('library_map', ?)`, doc); err != nil {
		t.Fatal(err)
	}

	rec := serve(server, jsonReq("GET", "/api/library/map", ""))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Ready  bool         `json:"ready"`
		Method string       `json:"method"`
		Total  int          `json:"total"`
		Points [][4]float64 `json:"points"`
		Labels []struct {
			ID       string  `json:"id"`
			TrackIDs []int64 `json:"track_ids"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Ready || out.Method != "umap" || out.Total != 4 {
		t.Fatalf("map header = %s", rec.Body.String())
	}
	var ids []float64
	for _, p := range out.Points {
		ids = append(ids, p[0])
	}
	if len(ids) != 3 || ids[0] != 11 || ids[1] != 22 || ids[2] != 55 {
		t.Fatalf("points = %v, want 11, 22 and own private 55", ids)
	}
	// A label left with only hidden tracks disappears; others lose them.
	if len(out.Labels) != 1 || out.Labels[0].ID != "chill" {
		t.Fatalf("labels = %+v", out.Labels)
	}
	if got := out.Labels[0].TrackIDs; len(got) != 2 || got[0] != 11 || got[1] != 55 {
		t.Fatalf("chill tracks = %v", got)
	}
}
