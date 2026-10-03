// Theme packages are directories on disk. The player reads them on each
// request, so installing a theme is copying a folder — no rebuild, no restart.
//
//	data/themes/<id>/theme.json
//	data/themes/<id>/theme.css
//	data/themes/<id>/fonts/*.(woff|woff2)   optional
package themes

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/torwin-job/musik/player/internal/static"
)

const (
	maxManifest = 8 << 10
	maxCSS      = 512 << 10
	maxFont     = 2 << 20
)

var (
	idRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	fontRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)
	hexRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// Info is one entry in the profile picker.
type Info struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Blurb   string   `json:"blurb"`
	Swatch  []string `json:"swatch"`
	Color   string   `json:"color"`
	Builtin bool     `json:"builtin"`
}

// Builtins are shipped inside the player binary.
func Builtins() []Info {
	return []Info{
		{ID: "ember", Name: "Ember", Blurb: "Warm", Swatch: []string{"#100c0a", "#e07a3a", "#3d8f7a"}, Color: "#1a120c", Builtin: true},
		{ID: "retro", Name: "Retro", Blurb: "Acid pixel", Swatch: []string{"#080a0c", "#d6ff3f", "#ff4f9a"}, Color: "#080a0c", Builtin: true},
	}
}

// List returns built-in themes plus packages in dir. A package whose id matches
// a built-in theme replaces that entry. A missing directory is not an error.
func List(dir string) []Info {
	out := Builtins()
	index := map[string]int{}
	for i, theme := range out {
		index[theme.ID] = i
	}
	extra := readDir(dir)
	slices.SortFunc(extra, func(a, b Info) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, theme := range extra {
		if i, ok := index[theme.ID]; ok {
			theme.Builtin = true
			out[i] = theme
			continue
		}
		out = append(out, theme)
	}
	return out
}

// Stylesheet returns theme CSS. A package on disk wins over the copy embedded
// in the binary. fromDisk reports which one was used so the caller can skip caching.
func Stylesheet(dir, id string) (body []byte, fromDisk, ok bool) {
	if !validID(id) {
		return nil, false, false
	}
	if dir != "" {
		path := filepath.Join(dir, id, "theme.css")
		if b, err := readRegular(path, maxCSS); err == nil && inside(filepath.Join(dir, id), path) {
			return b, true, true
		}
	}
	b, err := static.FS.ReadFile("themes/" + id + ".css")
	if err != nil || len(b) == 0 {
		return nil, false, false
	}
	return b, false, true
}

// FontPath returns a font file shipped inside a theme package.
func FontPath(dir, id, name string) (string, bool) {
	if dir == "" || !validID(id) || !fontRe.MatchString(name) {
		return "", false
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".woff" && ext != ".woff2" {
		return "", false
	}
	root := filepath.Join(dir, id, "fonts")
	path := filepath.Join(root, name)
	if !inside(root, path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxFont {
		return "", false
	}
	return path, true
}

func validID(id string) bool {
	return idRe.MatchString(id) && id != "base"
}

func readDir(dir string) []Info {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Info
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if theme, ok := readPackage(dir, entry.Name()); ok {
			out = append(out, theme)
		}
	}
	return out
}

func readPackage(dir, name string) (Info, bool) {
	if !validID(name) {
		return Info{}, false
	}
	root := filepath.Join(dir, name)
	manifestPath := filepath.Join(root, "theme.json")
	if !inside(root, manifestPath) {
		return Info{}, false
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return Info{}, false
	}
	defer f.Close()
	var raw struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Blurb  string   `json:"blurb"`
		Swatch []string `json:"swatch"`
		Color  string   `json:"color"`
	}
	dec := json.NewDecoder(io.LimitReader(f, maxManifest))
	if err := dec.Decode(&raw); err != nil {
		return Info{}, false
	}
	if raw.ID != name || !validID(raw.ID) {
		return Info{}, false
	}
	cssPath := filepath.Join(root, "theme.css")
	if _, err := readRegular(cssPath, maxCSS); err != nil || !inside(root, cssPath) {
		return Info{}, false
	}
	nameText := trimText(raw.Name, 40)
	if nameText == "" {
		return Info{}, false
	}
	swatch := make([]string, 0, 4)
	for _, color := range raw.Swatch {
		if !hexRe.MatchString(color) {
			return Info{}, false
		}
		swatch = append(swatch, strings.ToLower(color))
		if len(swatch) == 4 {
			break
		}
	}
	if len(swatch) == 0 {
		return Info{}, false
	}
	color := strings.ToLower(raw.Color)
	if color == "" {
		color = swatch[0]
	} else if !hexRe.MatchString(raw.Color) {
		return Info{}, false
	}
	return Info{
		ID:     raw.ID,
		Name:   nameText,
		Blurb:  trimText(raw.Blurb, 80),
		Swatch: swatch,
		Color:  color,
	}, true
}

func trimText(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > limit {
		return nil, os.ErrInvalid
	}
	return os.ReadFile(path)
}

func inside(root, path string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
