package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
	"github.com/torwin-job/musik/player/internal/yandex"
)

// fakeYandexAPI answers the few calls a track download makes. The file is
// not real audio, so ffmpeg fails and the mp3 is saved untagged.
func fakeYandexAPI(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	result := func(w http.ResponseWriter, r *http.Request, v any) {
		if r.Header.Get("Authorization") != "OAuth good" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": v})
	}
	track := map[string]any{"id": "5", "title": "Five", "artists": []any{map[string]any{"id": 1, "name": "Band"}},
		"albums": []any{map[string]any{"id": 9, "title": "Album", "year": 2020, "artists": []any{map[string]any{"id": 1, "name": "Band"}}}}}
	mux.HandleFunc("GET /account/status", func(w http.ResponseWriter, r *http.Request) {
		result(w, r, map[string]any{"account": map[string]any{"uid": 1, "login": "me"}})
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		result(w, r, map[string]any{"tracks": map[string]any{"results": []any{track}}})
	})
	mux.HandleFunc("POST /tracks", func(w http.ResponseWriter, r *http.Request) {
		result(w, r, []any{track})
	})
	mux.HandleFunc("GET /get-file-info", func(w http.ResponseWriter, r *http.Request) {
		result(w, r, map[string]any{"downloadInfo": map[string]any{"codec": "mp3", "transport": "raw", "url": srv.URL + "/file"}})
	})
	mux.HandleFunc("GET /file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ID3 not really audio"))
	})
	mux.HandleFunc("POST /revoke_token", func(w http.ResponseWriter, r *http.Request) {})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func openYandexServer(t *testing.T, enabled bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "musik.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lib := filepath.Join(dir, "music")
	cfg := config.Config{
		Root: dir, DBPath: path, Library: lib, QueueSize: 6, AuthDisabled: true,
		ProfileFormingAt: 3, ProfileReadyAt: 8, WorkerURL: "http://127.0.0.1:1",
		Yandex: enabled, FFmpegPath: "/nonexistent/ffmpeg",
	}
	idx := index.New(cfg)
	if err := idx.Load(nil); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, store, idx, taste.New(), nil)
	s.Play.Warm = nil
	return s, lib
}

func call(t *testing.T, s *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestYandexPluginIsOffByDefault(t *testing.T) {
	s, _ := openYandexServer(t, false)
	if code, out := call(t, s, "GET", "/api/yandex/status", ""); code != 200 || out["enabled"] != false {
		t.Fatalf("status = %d %v", code, out)
	}
	if code, _ := call(t, s, "GET", "/api/yandex/search?q=x", ""); code != 404 {
		t.Fatalf("search on disabled plugin = %d", code)
	}
}

func TestYandexLoginDownloadPrivateAndLogout(t *testing.T) {
	s, lib := openYandexServer(t, true)
	fake := fakeYandexAPI(t)
	s.yandex.apiURL, s.yandex.oauth = fake.URL, fake.URL
	s.yandex.jobs.Finished = nil // no worker in tests

	// 403, not 401: the web client treats 401 as "sign in to musik".
	if code, _ := call(t, s, "GET", "/api/yandex/search?q=five", ""); code != 403 {
		t.Fatalf("search before login = %d", code)
	}
	if code, out := call(t, s, "POST", "/api/yandex/login/token", `{"token":"bad"}`); code != 403 {
		t.Fatalf("bad token = %d %v", code, out)
	}
	if code, out := call(t, s, "POST", "/api/yandex/login/token", `{"token":"good"}`); code != 200 || out["login"] != "me" {
		t.Fatalf("login = %d %v", code, out)
	}
	if code, out := call(t, s, "GET", "/api/yandex/status", ""); code != 200 || out["connected"] != true {
		t.Fatalf("status = %d %v", code, out)
	}
	code, out := call(t, s, "GET", "/api/yandex/search?q=five&type=track", "")
	if code != 200 || len(out["tracks"].([]any)) != 1 {
		t.Fatalf("search = %d %v", code, out)
	}

	wait := func(id string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			_, out := call(t, s, "GET", "/api/yandex/downloads", "")
			for _, j := range out["jobs"].([]any) {
				job := j.(map[string]any)
				if job["id"] == id && job["status"] != "queued" && job["status"] != "running" {
					return job
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("job %s did not finish", id)
		return nil
	}
	rel := filepath.Join(yandex.LibraryFolder, "Band", "Album (2020)", "Five.mp3")

	_, job := call(t, s, "POST", "/api/yandex/downloads", `{"kind":"track","id":"5","private":true}`)
	if done := wait(job["id"].(string)); done["status"] != "done" || done["saved"] != float64(1) {
		t.Fatalf("private job = %v", done)
	}
	private := filepath.Join(lib, "_private", s.Store.ProfileID, rel)
	if _, err := os.Stat(private); err != nil {
		t.Fatalf("private file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, rel)); err == nil {
		t.Fatal("private download landed in the shared library")
	}

	_, job = call(t, s, "POST", "/api/yandex/downloads", `{"kind":"track","id":"5"}`)
	if done := wait(job["id"].(string)); done["status"] != "done" {
		t.Fatalf("shared job = %v", done)
	}
	if _, err := os.Stat(filepath.Join(lib, rel)); err != nil {
		t.Fatalf("shared file missing: %v", err)
	}

	if code, _ := call(t, s, "POST", "/api/yandex/logout", ""); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	if _, out := call(t, s, "GET", "/api/yandex/status", ""); out["connected"] != false {
		t.Fatalf("still connected after logout: %v", out)
	}
	if code, _ := call(t, s, "GET", "/api/yandex/search?q=five", ""); code != 403 {
		t.Fatalf("search after logout = %d", code)
	}
}

func TestPrivateTracksOfOtherProfilesAreNotServed(t *testing.T) {
	s, lib := openYandexServer(t, false)
	other := filepath.Join(lib, "_private", "someone-else", "x.mp3")
	mine := filepath.Join(lib, "_private", s.Store.ProfileID, "y.mp3")
	for _, p := range []string{other, mine} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("ID3"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for id, p := range map[int]string{1: other, 2: mine} {
		if _, err := s.Store.DB.Exec(`INSERT INTO tracks(id, path, title, created_at, updated_at) VALUES (?, ?, 'T', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/stream/1", "/api/tracks/1", "/api/tracks/1/lyrics", "/api/artwork/1"} {
		if code, _ := call(t, s, "GET", path, ""); code != 404 {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
	if code, _ := call(t, s, "GET", "/api/stream/2", ""); code != 200 {
		t.Errorf("own private stream = %d", code)
	}
}
