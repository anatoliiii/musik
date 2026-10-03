package themes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinNamesAreEnglish(t *testing.T) {
	list := Builtins()
	if len(list) != 2 || list[0].ID != "ember" || list[0].Name != "Ember" || list[0].Blurb != "Warm" {
		t.Fatalf("ember=%#v", list)
	}
	if list[1].ID != "retro" || list[1].Name != "Retro" || list[1].Blurb != "Acid pixel" {
		t.Fatalf("retro=%#v", list[1])
	}
}

func TestListReadsPackageAndOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	writePackage(t, dir, "ink", `{
		"id": "ink",
		"name": "Ink",
		"blurb": "Cool",
		"swatch": ["#071018", "#3EE0C5", "#7aa2ff"],
		"color": "#071018"
	}`, "html[data-theme=\"ink\"]{--accent:#3ee0c5}")
	writePackage(t, dir, "retro", `{
		"id": "retro",
		"name": "Retro",
		"blurb": "From disk",
		"swatch": ["#111111", "#222222"],
		"color": "#111111"
	}`, "html[data-theme=\"retro\"]{--accent:#222}")
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644)
	os.Mkdir(filepath.Join(dir, "Broken"), 0o755)

	list := List(dir)
	if len(list) != 3 {
		t.Fatalf("themes=%d %#v", len(list), list)
	}
	if list[0].ID != "ember" || !list[0].Builtin {
		t.Fatalf("ember=%#v", list[0])
	}
	if list[1].ID != "retro" || list[1].Blurb != "From disk" || !list[1].Builtin {
		t.Fatalf("override=%#v", list[1])
	}
	if list[2].ID != "ink" || list[2].Builtin || list[2].Swatch[1] != "#3ee0c5" {
		t.Fatalf("ink=%#v", list[2])
	}

	body, disk, ok := Stylesheet(dir, "ink")
	if !ok || !disk || !strings.Contains(string(body), "#3ee0c5") {
		t.Fatalf("ink css disk=%v ok=%v %q", disk, ok, body)
	}
	body, disk, ok = Stylesheet(dir, "retro")
	if !ok || !disk || !strings.Contains(string(body), "#222") {
		t.Fatalf("override css disk=%v %q", disk, body)
	}
	body, disk, ok = Stylesheet("", "retro")
	if !ok || disk || !strings.Contains(string(body), "d6ff3f") {
		t.Fatalf("embedded retro disk=%v %q", disk, body)
	}
	if _, _, ok := Stylesheet(dir, "../secret"); ok {
		t.Fatal("path traversal stylesheet")
	}
	if _, ok := FontPath(dir, "ink", "../theme.css"); ok {
		t.Fatal("path traversal font")
	}
}

func TestRejectsIncompletePackage(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ink")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"id":"other","name":"X","swatch":["#000000"]}`), 0o644)
	os.WriteFile(filepath.Join(root, "theme.css"), []byte("x"), 0o644)
	if len(List(dir)) != 2 {
		t.Fatalf("id mismatch should be ignored: %#v", List(dir))
	}
}

func writePackage(t *testing.T, dir, id, manifest, css string) {
	t.Helper()
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "theme.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
}
