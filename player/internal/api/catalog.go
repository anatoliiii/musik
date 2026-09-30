package api

import (
	"net/http"
	"strconv"

	"github.com/torwin-job/musik/player/internal/library"
)

func artworkURL(id int64, has bool) string {
	if !has {
		return ""
	}
	return "/api/artwork/" + strconv.FormatInt(id, 10)
}

func (s *Server) handleArtists(w http.ResponseWriter, r *http.Request) {
	groups := library.GroupArtists(s.Idx)
	if limit := queryLimit(r, 0, 500); limit > 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	type row struct {
		Artist       string `json:"artist"`
		Tracks       int    `json:"tracks"`
		CoverTrackID int64  `json:"cover_track_id,omitempty"`
		Artwork      string `json:"artwork,omitempty"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		out = append(out, row{
			Artist: g.Artist, Tracks: g.Tracks, CoverTrackID: g.CoverTrackID,
			Artwork: artworkURL(g.CoverTrackID, g.HasArtwork),
		})
	}
	writeJSON(w, map[string]any{"artists": out, "count": len(out)})
}

func (s *Server) handleAlbums(w http.ResponseWriter, r *http.Request) {
	groups := library.GroupAlbums(s.Idx)
	if limit := queryLimit(r, 0, 500); limit > 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	type row struct {
		Artist       string `json:"artist"`
		Album        string `json:"album"`
		Tracks       int    `json:"tracks"`
		CoverTrackID int64  `json:"cover_track_id,omitempty"`
		Artwork      string `json:"artwork,omitempty"`
		Game         bool   `json:"game,omitempty"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		out = append(out, row{
			Artist: g.Artist, Album: g.Album, Tracks: g.Tracks, CoverTrackID: g.CoverTrackID,
			Artwork: artworkURL(g.CoverTrackID, g.HasArtwork),
			Game:    g.Game,
		})
	}
	writeJSON(w, map[string]any{"albums": out, "count": len(out)})
}

func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	tj := s.trackJSON(id)
	if tj == nil {
		writeErr(w, 404, "not_found", "track not found")
		return
	}
	if out, ok := tj.(map[string]any); ok {
		if p, err := s.Store.TrackPassport(id); err == nil && p != nil {
			out["passport"] = p
		}
	}
	writeJSON(w, tj)
}
