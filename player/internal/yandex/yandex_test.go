package yandex

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileInfoSignMatchesReference(t *testing.T) {
	// Reference value from yandex_music.utils.sign_request.get_file_info_sign.
	got := FileInfoSign(1700000000, "12345", "lossless", fileInfoCodecs, "raw")
	if want := "fFQDLaOXGyTn5tPZev5DXs/xxMJCTEAGa/e9/RM6eWM"; got != want {
		t.Fatalf("sign = %q, want %q", got, want)
	}
}

func TestPlacementPaths(t *testing.T) {
	tr := Track{ID: "1", Title: "Song / Part", Version: "Live"}
	p := Placement{AlbumArtist: "AC/DC", Album: ".Hidden: Album", Year: 1980, Number: 3}
	got := p.RelPath(tr, ".flac")
	want := filepath.Join(LibraryFolder, "AC_DC", "Hidden_ Album (1980)", "03. Song _ Part (Live).flac")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	p.Disc, p.Discs = 2, 2
	if got := filepath.Base(p.RelPath(tr, ".mp3")); got != "2-03. Song _ Part (Live).mp3" {
		t.Fatalf("multi-disc name = %q", got)
	}
	if got := (Placement{}).RelPath(Track{}, ".m4a"); got != filepath.Join(LibraryFolder, "Unknown Artist", "Singles", "Unknown Track.m4a") {
		t.Fatalf("fallback path = %q", got)
	}
}

func TestDecryptFileUndoesEncraw(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	plain := []byte(strings.Repeat("fLaC audio bytes ", 100))
	block, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCTR(block, make([]byte, 16)).XORKeyStream(enc, plain)
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := decryptFile(path, hex.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(plain) {
		t.Fatal("decrypted bytes differ")
	}
}

func TestTokenStoreIsPerUser(t *testing.T) {
	store := &TokenStore{Dir: t.TempDir()}
	if err := store.Save("alice", Session{Token: "a", Login: "alice@ya"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Load("bob"); ok {
		t.Fatal("bob sees alice's token")
	}
	got, ok := store.Load("alice")
	if !ok || got.Token != "a" {
		t.Fatalf("load = %+v %v", got, ok)
	}
	entries, _ := os.ReadDir(store.Dir)
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v", info.Mode().Perm())
		}
	}
	if err := store.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Load("alice"); ok {
		t.Fatal("token survived delete")
	}
	if err := store.Delete("alice"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// fakeYandex serves the API, OAuth and file endpoints the plugin uses.
type fakeYandex struct {
	t      *testing.T
	srv    *httptest.Server
	audio  []byte
	codec  string
	key    string // hex key: serve encraw
	cover  []byte
	mu     sync.Mutex
	signed []string
	polls  int
}

func newFakeYandex(t *testing.T, audio []byte, codec string) *fakeYandex {
	f := &fakeYandex{t: t, audio: audio, codec: codec}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": v})
	}
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "OAuth good" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"name":"session-expired"}}`))
			return false
		}
		return true
	}
	albumOf := func() map[string]any {
		return map[string]any{
			"id": 10, "title": "Album", "year": 2001, "genre": "rock", "coverUri": f.srv.URL + "/cover/%%",
			"artists": []any{map[string]any{"id": 7, "name": "Band"}}, "labels": []any{"Label"},
		}
	}
	track := func(id string, n int) map[string]any {
		a := albumOf()
		a["trackPosition"] = map[string]any{"volume": 1, "index": n}
		return map[string]any{"id": id, "title": "Song " + id, "durationMs": 1000,
			"artists": []any{map[string]any{"id": 7, "name": "Band"}}, "albums": []any{a}}
	}
	mux.HandleFunc("GET /account/status", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			ok(w, map[string]any{"account": map[string]any{"uid": 99, "login": "user", "displayName": "User"}, "plus": map[string]any{"hasPlus": true}})
		}
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			ok(w, map[string]any{"tracks": map[string]any{"results": []any{track("1", 1)}}, "albums": map[string]any{"results": []any{albumOf()}}})
		}
	})
	mux.HandleFunc("POST /tracks", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			_ = r.ParseForm()
			ok(w, []any{track(r.Form.Get("track-ids"), 1)})
		}
	})
	mux.HandleFunc("GET /albums/10/with-tracks", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			a := albumOf()
			a["volumes"] = []any{[]any{track("1", 1), track("2", 2)}}
			ok(w, a)
		}
	})
	mux.HandleFunc("GET /artists/7/direct-albums", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			ok(w, map[string]any{"albums": []any{albumOf()}, "pager": map[string]any{"total": 1, "perPage": 100}})
		}
	})
	mux.HandleFunc("GET /get-file-info", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		q := r.URL.Query()
		var ts int64
		fmt.Sscan(q.Get("ts"), &ts)
		want := FileInfoSign(ts, q.Get("trackId"), q.Get("quality"), strings.Split(q.Get("codecs"), ","), q.Get("transports"))
		if q.Get("sign") != want || r.Header.Get("X-Yandex-Music-Client") != desktopClient {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		f.signed = append(f.signed, q.Get("trackId"))
		f.mu.Unlock()
		info := map[string]any{"trackId": q.Get("trackId"), "quality": "lossless", "codec": f.codec,
			"transport": "raw", "url": f.srv.URL + "/file/" + q.Get("trackId")}
		if f.key != "" {
			info["transport"], info["key"] = "encraw", f.key
		}
		ok(w, map[string]any{"downloadInfo": info})
	})
	mux.HandleFunc("GET /file/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(f.audio)
	})
	mux.HandleFunc("GET /cover/{size}", func(w http.ResponseWriter, r *http.Request) {
		if f.cover == nil {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(f.cover)
	})
	mux.HandleFunc("POST /device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dev", "user_code": "ABC", "verification_url": "https://ya.ru/device", "interval": 1, "expires_in": 300})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.polls++
		n := f.polls
		f.mu.Unlock()
		if n == 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "good"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeYandex) client() *Client {
	return &Client{APIURL: f.srv.URL, OAuthURL: f.srv.URL, Token: "good"}
}

func sineFLAC(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "s.flac")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	raw, _ := os.ReadFile(path)
	return raw
}

func jpegCover(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.jpg")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	raw, _ := os.ReadFile(path)
	return raw
}

func hasPicture(t *testing.T, path string) bool {
	t.Helper()
	out, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	return strings.TrimSpace(string(out)) != ""
}

func probeTags(t *testing.T, path string) map[string]string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var doc struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	_ = json.Unmarshal(out, &doc)
	tags := map[string]string{}
	for k, v := range doc.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	return tags
}

func TestDeviceLoginAndAccount(t *testing.T) {
	f := newFakeYandex(t, nil, "mp3")
	c := &Client{APIURL: f.srv.URL, OAuthURL: f.srv.URL}
	code, err := c.RequestDeviceCode(context.Background(), "musik")
	if err != nil || code.UserCode != "ABC" {
		t.Fatalf("device code = %+v %v", code, err)
	}
	if _, err := c.PollDeviceToken(context.Background(), code.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("first poll err = %v, want pending", err)
	}
	token, err := c.PollDeviceToken(context.Background(), code.DeviceCode)
	if err != nil || token != "good" {
		t.Fatalf("token = %q %v", token, err)
	}
	c.Token = token
	acc, err := c.Account(context.Background())
	if err != nil || acc.Login != "user" || !acc.HasPlus || acc.UID != "99" {
		t.Fatalf("account = %+v %v", acc, err)
	}
	c.Token = "bad"
	if _, err := c.Account(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token err = %v", err)
	}
}

func TestManagerDownloadsAlbumAndSkipsExisting(t *testing.T) {
	audio := sineFLAC(t)
	key := hex.EncodeToString([]byte("0123456789abcdef"))
	block, _ := aes.NewCipher([]byte("0123456789abcdef"))
	enc := make([]byte, len(audio))
	cipher.NewCTR(block, make([]byte, 16)).XORKeyStream(enc, audio)
	f := newFakeYandex(t, enc, "flac")
	f.key = key
	f.cover = jpegCover(t)

	root := t.TempDir()
	finished := make(chan Job, 4)
	m := NewManager(&Downloader{}, "lossless")
	m.Finished = func(j Job) { finished <- j }
	target := Target{UserID: "alice", Root: root, Client: f.client()}

	job, err := m.Start(target, Request{Kind: "album", ID: "10"})
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, m, "alice", job.ID)
	if done.Status != "done" || done.Saved != 2 || done.Total != 2 || done.Title != "Band — Album" {
		t.Fatalf("job = %+v", done)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Finished not called")
	}
	song := filepath.Join(root, LibraryFolder, "Band", "Album (2001)", "01. Song 1.flac")
	tags := probeTags(t, song)
	if tags["title"] != "Song 1" || tags["artist"] != "Band" || tags["album"] != "Album" || tags["track"] != "1" {
		t.Fatalf("tags = %v", tags)
	}
	if !hasPicture(t, song) {
		t.Fatal("cover not embedded")
	}

	again, _ := m.Start(target, Request{Kind: "track", ID: "1"})
	again = waitJob(t, m, "alice", again.ID)
	if again.Status != "done" || again.Skipped != 1 || again.Saved != 0 {
		t.Fatalf("repeat job = %+v", again)
	}
	if len(m.Jobs("bob")) != 0 {
		t.Fatal("bob sees alice's jobs")
	}
}

func TestManagerReportsExpiredToken(t *testing.T) {
	f := newFakeYandex(t, nil, "mp3")
	m := NewManager(&Downloader{}, "")
	c := f.client()
	c.Token = "expired"
	job, _ := m.Start(Target{UserID: "u", Root: t.TempDir(), Client: c}, Request{Kind: "artist", ID: "7"})
	got := waitJob(t, m, "u", job.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "unauthorized") {
		t.Fatalf("job = %+v", got)
	}
	if _, err := m.Start(Target{}, Request{Kind: "playlist", ID: "1"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func waitJob(t *testing.T, m *Manager, user, id string) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range m.Jobs(user) {
			if j.ID == id && j.Status != "queued" && j.Status != "running" {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return Job{}
}

func TestSaveRemuxesEveryCodec(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cover := jpegCover(t)
	cases := []struct {
		codec, ext string
		args       []string
	}{
		{"flac-mp4", ".flac", []string{"-c:a", "flac", "-strict", "-2", "-f", "mp4"}},
		{"aac-mp4", ".m4a", []string{"-c:a", "aac", "-f", "mp4"}},
		{"aac", ".m4a", []string{"-c:a", "aac", "-f", "adts"}},
		{"mp3", ".mp3", []string{"-c:a", "libmp3lame", "-f", "mp3"}},
	}
	for _, tc := range cases {
		t.Run(tc.codec, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "src")
			args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=1"}, tc.args...)
			if out, err := exec.Command("ffmpeg", append(args, src)...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg cannot make %s: %s", tc.codec, out)
			}
			audio, _ := os.ReadFile(src)
			f := newFakeYandex(t, audio, tc.codec)
			f.cover = cover
			root := t.TempDir()
			tr := Track{ID: "5", Title: "Five", Artists: []Artist{{Name: "Band"}},
				Albums: []Album{{ID: "10", Title: "Album", CoverURI: f.srv.URL + "/cover/%%"}}}
			rel, err := (&Downloader{}).Save(context.Background(), f.client(), root, tr, PlacementFor(tr, nil), "")
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(rel) != tc.ext {
				t.Fatalf("saved %s, want %s", rel, tc.ext)
			}
			path := filepath.Join(root, rel)
			if got := probeTags(t, path)["title"]; got != "Five" {
				t.Fatalf("title tag = %q", got)
			}
			if !hasPicture(t, path) {
				t.Fatal("cover not embedded")
			}
		})
	}
}
