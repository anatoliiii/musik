package playlist

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestSmartPlaylistRoundTripAndImportExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pl.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for id := 1; id <= 3; id++ {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, year, duration, created_at, updated_at) VALUES (?,?,?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, fmt.Sprintf("/t-%d.flac", id), "Title", "Artist", "Album", 2012, 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec(`INSERT INTO favorites(track_id, added_at, position, profile_id) VALUES (1,'2026-01-01T00:00:00Z',0,?)`, store.ProfileID); err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		SchemaVersion: 1,
		All:           []Clause{{Field: "liked", Op: "eq", Value: true}},
	}
	raw, err := rule.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRule(raw)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := Evaluate(store, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("smart ids=%v", ids)
	}
	pl, err := store.CreateUserPlaylist(db.UserPlaylist{Name: "Лайки", Type: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddPlaylistItem(pl.ID, db.UserPlaylistItem{TrackID: 1, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	full, _ := store.GetUserPlaylist(pl.ID)
	exported, err := ExportJSON(full, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) == 0 || containsPath(string(exported), "/t-1.flac") {
		t.Fatalf("export leaked a server path: %s", exported)
	}
	doc, err := ParseImport("copy", string(exported), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Name != "Лайки" || len(doc.Tracks) != 1 {
		t.Fatalf("doc %+v", doc)
	}
	m3u := ExportM3U(full, false)
	if !containsAll(m3u, "#EXTM3U", "Artist - Title") {
		t.Fatalf("m3u=%s", m3u)
	}
	round, err := ParseImport("m3u", m3u, "audio/x-mpegurl")
	if err != nil || len(round.Tracks) != 1 {
		t.Fatalf("m3u import %+v %v", round, err)
	}
}

func containsPath(s, path string) bool {
	return len(s) >= len(path) && (s == path || indexOf(s, path) >= 0)
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if indexOf(s, part) < 0 {
			return false
		}
	}
	return true
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
