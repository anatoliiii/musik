package playlist

import (
	"bufio"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/torwin-job/musik/player/internal/db"
)

const ExportSchemaVersion = 1

type ExportDoc struct {
	SchemaVersion int           `json:"schema_version"`
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	Description   string        `json:"description,omitempty"`
	RuleJSON      string        `json:"rule_json,omitempty"`
	Tracks        []ExportTrack `json:"tracks"`
}

type ExportTrack struct {
	Artist   string  `json:"artist,omitempty"`
	Title    string  `json:"title,omitempty"`
	Album    string  `json:"album,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Note     string  `json:"note,omitempty"`
	Path     string  `json:"path,omitempty"`
}

func ExportJSON(pl *db.UserPlaylist, includePaths bool) ([]byte, error) {
	doc := ExportDoc{
		SchemaVersion: ExportSchemaVersion,
		Name:          pl.Name,
		Type:          pl.Type,
		Description:   pl.Description,
		RuleJSON:      pl.RuleJSON,
	}
	for _, item := range pl.Tracks {
		tr := ExportTrack{
			Artist: item.Artist, Title: item.Title, Album: item.Album,
			Duration: item.Duration, Note: item.Note,
		}
		if item.Unresolved {
			tr.Artist = firstNonEmpty(item.UnresolvedArtist, tr.Artist)
			tr.Title = firstNonEmpty(item.UnresolvedTitle, tr.Title)
		}
		if includePaths {
			tr.Path = item.UnresolvedPath
		}
		doc.Tracks = append(doc.Tracks, tr)
	}
	return json.MarshalIndent(doc, "", "  ")
}

func ExportM3U(pl *db.UserPlaylist, includePaths bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, item := range pl.Tracks {
		artist := firstNonEmpty(item.Artist, item.UnresolvedArtist)
		title := firstNonEmpty(item.Title, item.UnresolvedTitle)
		dur := int(item.Duration)
		if dur <= 0 {
			dur = -1
		}
		fmt.Fprintf(&b, "#EXTINF:%d,%s - %s\n", dur, artist, title)
		if includePaths && item.UnresolvedPath != "" {
			b.WriteString(filepath.Base(item.UnresolvedPath))
		} else {
			b.WriteString(artist + " - " + title)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func ParseImport(name, body, contentType string) (ExportDoc, error) {
	trimmed := strings.TrimSpace(body)
	if strings.Contains(contentType, "json") || strings.HasPrefix(trimmed, "{") {
		var doc ExportDoc
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return doc, fmt.Errorf("invalid playlist json")
		}
		if doc.SchemaVersion == 0 {
			doc.SchemaVersion = ExportSchemaVersion
		}
		if doc.SchemaVersion != ExportSchemaVersion {
			return doc, fmt.Errorf("unsupported export schema_version")
		}
		if doc.Name == "" {
			doc.Name = name
		}
		if doc.Type == "" {
			doc.Type = "manual"
		}
		return doc, nil
	}
	return parseM3U(name, body), nil
}

func parseM3U(name, body string) ExportDoc {
	doc := ExportDoc{SchemaVersion: ExportSchemaVersion, Name: name, Type: "manual"}
	if doc.Name == "" {
		doc.Name = "Импорт"
	}
	scanner := bufio.NewScanner(strings.NewReader(body))
	var pending ExportTrack
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "#EXTM3U" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			rest := strings.TrimPrefix(line, "#EXTINF:")
			label := rest
			if i := strings.Index(rest, ","); i >= 0 {
				fmt.Sscanf(rest[:i], "%f", &pending.Duration)
				label = rest[i+1:]
			}
			artist, title, _ := strings.Cut(label, " - ")
			if title == "" {
				pending.Title = strings.TrimSpace(label)
			} else {
				pending.Artist = strings.TrimSpace(artist)
				pending.Title = strings.TrimSpace(title)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		pending.Path = line
		if pending.Title == "" {
			pending.Title = strings.TrimSuffix(filepath.Base(line), filepath.Ext(line))
		}
		doc.Tracks = append(doc.Tracks, pending)
		pending = ExportTrack{}
	}
	if pending.Title != "" || pending.Path != "" {
		doc.Tracks = append(doc.Tracks, pending)
	}
	return doc
}

func ResolveImport(store *db.Store, doc ExportDoc) []db.UserPlaylistItem {
	items := make([]db.UserPlaylistItem, 0, len(doc.Tracks))
	for _, tr := range doc.Tracks {
		item := db.UserPlaylistItem{
			Source:           "import",
			Note:             tr.Note,
			UnresolvedArtist: tr.Artist,
			UnresolvedTitle:  tr.Title,
			UnresolvedPath:   tr.Path,
		}
		if id, ok, err := store.MatchTrack(tr.Artist, tr.Title, tr.Path); err == nil && ok {
			item.TrackID = id
		} else {
			item.Unresolved = true
		}
		items = append(items, item)
	}
	return items
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
