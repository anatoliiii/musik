package library

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/torwin-job/musik/player/internal/index"
)

type ArtistGroup struct {
	Artist       string
	Tracks       int
	CoverTrackID int64
	HasArtwork   bool
}

type AlbumGroup struct {
	Artist       string
	Album        string
	Tracks       int
	CoverTrackID int64
	HasArtwork   bool
	// Game is true when the album's files live under a game-soundtrack folder
	// ("Steam OST", "GOG OST"): album titles like "Cyberpunk 2077" say nothing.
	Game bool
}

// gameSoundtrackDirs are library folders that hold game soundtracks (see the Steam/GOG importer).
var gameSoundtrackDirs = []string{"steam ost", "gog ost"}

// IsGameSoundtrackPath reports whether a track path lies in a game-soundtrack folder.
func IsGameSoundtrackPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		part = strings.ToLower(strings.TrimSpace(part))
		for _, dir := range gameSoundtrackDirs {
			if part == dir {
				return true
			}
		}
	}
	return false
}

func MatchArtistAlbum(gotArtist, gotAlbum, wantArtist, wantAlbum string) bool {
	if wantArtist != "" && !strings.EqualFold(strings.TrimSpace(gotArtist), strings.TrimSpace(wantArtist)) {
		return false
	}
	if wantAlbum != "" && !strings.EqualFold(strings.TrimSpace(gotAlbum), strings.TrimSpace(wantAlbum)) {
		return false
	}
	return true
}

func GroupArtists(idx *index.Index) []ArtistGroup {
	if idx == nil {
		return nil
	}
	by := map[string]*ArtistGroup{}
	n := idx.Size()
	for i := 0; i < n; i++ {
		m := idx.MetaAt(i)
		name := strings.TrimSpace(m.Artist)
		if name == "" {
			name = "Unknown"
		}
		key := strings.ToLower(name)
		g := by[key]
		if g == nil {
			g = &ArtistGroup{Artist: name, CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != ""}
			by[key] = g
		}
		g.Tracks++
	}
	out := make([]ArtistGroup, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Artist < out[j].Artist
	})
	return out
}

func GroupAlbums(idx *index.Index) []AlbumGroup {
	if idx == nil {
		return nil
	}
	by := map[string]*AlbumGroup{}
	n := idx.Size()
	for i := 0; i < n; i++ {
		m := idx.MetaAt(i)
		album := strings.TrimSpace(m.Album)
		if album == "" {
			continue
		}
		artist := strings.TrimSpace(m.Artist)
		key := strings.ToLower(artist) + "\x00" + strings.ToLower(album)
		g := by[key]
		if g == nil {
			g = &AlbumGroup{
				Artist: artist, Album: album,
				CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != "",
			}
			by[key] = g
		}
		g.Tracks++
		if IsGameSoundtrackPath(m.Path) {
			g.Game = true
		}
	}
	out := make([]AlbumGroup, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Album < out[j].Album
	})
	return out
}
