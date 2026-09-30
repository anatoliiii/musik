package apitest

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/static"
)

func attachStatic(t *testing.T, server *api.Server) {
	t.Helper()
	sub, err := fs.Sub(static.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	server.Static = http.FS(sub)
}

func TestStaticCacheHeaders(t *testing.T) {
	server := openTestServer(t)
	attachStatic(t, server)

	cases := []struct {
		path, want string
	}{
		{"/", "no-cache"},
		{"/app.js", "public, max-age=3600"},
		{"/style.css", "public, max-age=3600"},
		{"/fonts.css", "public, max-age=3600"},
		{"/fonts/manrope-latin.woff2", "public, max-age=31536000, immutable"},
	}
	for _, tc := range cases {
		rec := serve(server, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s status=%d", tc.path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.want {
			t.Fatalf("%s Cache-Control=%q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestGzipJSONAndStaticNotImages(t *testing.T) {
	server := openTestServer(t)
	attachStatic(t, server)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(server, req)
	if rec.Code != 200 {
		t.Fatalf("health status=%d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("health encoding=%q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	raw := gunzipBody(t, rec)
	var health map[string]any
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatalf("health json: %v body=%s", err, raw)
	}
	if health["ok"] != true {
		t.Fatalf("health=%v", health)
	}

	req = httptest.NewRequest("GET", "/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = serve(server, req)
	if rec.Code != 200 {
		t.Fatalf("app.js status=%d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("app.js encoding=%q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	js := gunzipBody(t, rec)
	if len(js) < 100 {
		t.Fatal("expected gunzipped app.js")
	}

	req = httptest.NewRequest("GET", "/fonts/manrope-latin.woff2", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = serve(server, req)
	if rec.Code != 200 {
		t.Fatalf("font status=%d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("woff2 must not be gzipped")
	}
}

func gunzipBody(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
