package library

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipArchive(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func tarGzArchive(t *testing.T, headers []*tar.Header, bodies []string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for i, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if bodies[i] != "" {
			if _, err := w.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func noStageLeft(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".archive-") {
			t.Fatalf("staging directory left behind: %s", e.Name())
		}
	}
}

func TestSaveArchiveZipKeepsColonNamesAndSkipsOtherFiles(t *testing.T) {
	root := t.TempDir()
	src := zipArchive(t, map[string]string{
		"Band/LP/01 Intro: Part I.flac": "audio-1",
		"Band/LP/cover.jpg":             "image",
	})
	saved, skipped, err := SaveArchive(root, "album.zip", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || len(skipped) != 1 {
		t.Fatalf("saved=%v skipped=%v", saved, skipped)
	}
	got, err := os.ReadFile(filepath.Join(root, "Band", "LP", "01 Intro: Part I.flac"))
	if err != nil || string(got) != "audio-1" {
		t.Fatalf("published file: %q %v", got, err)
	}
	noStageLeft(t, root)
}

func TestSaveArchiveRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../escape.mp3", "/abs.mp3", "C:/win.mp3", `a\..\..\x.mp3`} {
		root := t.TempDir()
		src := zipArchive(t, map[string]string{"ok.mp3": "fine", name: "evil"})
		if _, _, err := SaveArchive(root, "x.zip", src); err == nil {
			t.Fatalf("%q: expected rejection", name)
		}
		if _, err := os.Stat(filepath.Join(root, "ok.mp3")); !os.IsNotExist(err) {
			t.Fatalf("%q: nothing may be published from a rejected archive", name)
		}
		noStageLeft(t, root)
	}
}

func TestSaveArchiveTarGzRejectsLinks(t *testing.T) {
	root := t.TempDir()
	buf := tarGzArchive(t, []*tar.Header{
		{Name: "a.mp3", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "b.mp3", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}, []string{"data", ""})
	if _, _, err := SaveArchive(root, "x.tar.gz", buf); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if _, err := os.Stat(filepath.Join(root, "a.mp3")); !os.IsNotExist(err) {
		t.Fatal("nothing may be published from a rejected archive")
	}
	noStageLeft(t, root)
}

func TestSaveArchiveTarGzPublishes(t *testing.T) {
	root := t.TempDir()
	buf := tarGzArchive(t, []*tar.Header{
		{Name: "Artist/Album/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "Artist/Album/01.opus", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5},
	}, []string{"", "opus!"})
	saved, _, err := SaveArchive(root, "x.tgz", buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Bytes != 5 {
		t.Fatalf("saved=%v", saved)
	}
	if _, err := os.Stat(filepath.Join(root, "Artist", "Album", "01.opus")); err != nil {
		t.Fatal(err)
	}
	noStageLeft(t, root)
}
