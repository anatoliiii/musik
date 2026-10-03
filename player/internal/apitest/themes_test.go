package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThemePackageWithoutRebuild(t *testing.T) {
	server := openTestServer(t)
	attachStatic(t, server)
	dir := t.TempDir()
	root := filepath.Join(dir, "ink")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{
		"id":"ink","name":"Ink","blurb":"Cool",
		"swatch":["#071018","#3ee0c5"],"color":"#071018"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme.css"), []byte(`html[data-theme="ink"]{--accent:#3ee0c5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	server.Cfg.ThemesDir = dir

	rec := serve(server, httptest.NewRequest(http.MethodGet, "/api/themes", nil))
	if rec.Code != 200 {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body)
	}
	var catalog struct {
		Themes []struct {
			ID      string `json:"id"`
			Builtin bool   `json:"builtin"`
		} `json:"themes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, theme := range catalog.Themes {
		if theme.ID == "ink" && !theme.Builtin {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog=%v", catalog.Themes)
	}

	rec = serve(server, httptest.NewRequest(http.MethodGet, "/themes/ink.css", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "#3ee0c5") {
		t.Fatalf("disk css status=%d cache=%q body=%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("disk theme must revalidate, cache=%q", rec.Header().Get("Cache-Control"))
	}

	rec = serve(server, httptest.NewRequest(http.MethodGet, "/themes/retro.css", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "d6ff3f") || !strings.Contains(rec.Body.String(), "retro-field") {
		t.Fatalf("builtin css status=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "omarchy") {
		t.Fatal("retro stylesheet still mentions omarchy")
	}
	var named struct {
		Themes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"themes"`
	}
	rec = serve(server, httptest.NewRequest(http.MethodGet, "/api/themes", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &named); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, theme := range named.Themes {
		names[theme.ID] = theme.Name
	}
	if names["ember"] != "Ember" || names["retro"] != "Retro" || names["ink"] != "Ink" {
		t.Fatalf("theme names=%v", names)
	}

	rec = serve(server, httptest.NewRequest(http.MethodGet, "/themes/../secret.css", nil))
	if rec.Code == 200 && strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("path traversal served a file")
	}
}
