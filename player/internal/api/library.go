package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/library"
)

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	type row struct {
		ID      int64    `json:"id"`
		Artist  string   `json:"artist"`
		Artists []string `json:"artists,omitempty"`
		// AlbumArtist lists a compilation track under its album credit.
		AlbumArtist  string  `json:"album_artist,omitempty"`
		Title        string  `json:"title"`
		Album        string  `json:"album"`
		Duration     float64 `json:"duration"`
		Cluster      int     `json:"cluster_id"`
		Artwork      string  `json:"artwork,omitempty"`
		Ready        bool    `json:"ready"`
		Status       string  `json:"status,omitempty"`
		Plays        int     `json:"plays"`
		EarlySkips   int     `json:"early_skips"`
		LastPlayedAt string  `json:"last_played_at,omitempty"`
	}
	limit := queryLimit(r, 0, 2000)
	needAll := artist != "" || album != ""
	scanLimit := 0
	if !needAll {
		scanLimit = limit
	}
	catalog, err := s.Store.ListCatalogTracksLimit(scanLimit)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	out := make([]row, 0, len(catalog))
	if len(catalog) > 0 {
		for _, m := range catalog {
			if !library.MatchArtistAlbum(m.Artist, m.Artists, m.AlbumArtist, m.Album, artist, album) {
				continue
			}
			art := ""
			if m.Artwork != "" {
				art = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
			}
			out = append(out, row{
				ID: m.ID, Artist: m.Artist, Artists: m.Artists, AlbumArtist: m.AlbumArtist, Title: m.Title, Album: m.Album,
				Duration: m.Duration, Cluster: m.Cluster, Artwork: art,
				Ready: m.Status == "ready", Status: m.Status,
				Plays: m.Plays, EarlySkips: m.EarlySkips, LastPlayedAt: m.LastPlayedAt,
			})
			if needAll && limit > 0 && len(out) >= limit {
				break
			}
		}
		writeJSON(w, out)
		return
	}
	n := s.Idx.Size()
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		if !library.MatchArtistAlbum(m.Artist, m.Artists, m.AlbumArtist, m.Album, artist, album) {
			continue
		}
		art := ""
		if m.ArtworkPath != "" {
			art = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		last := ""
		if !m.LastPlayedAt.IsZero() {
			last = m.LastPlayedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, row{
			ID: m.ID, Artist: m.Artist, Artists: m.Artists, AlbumArtist: m.AlbumArtist,
			Title: m.Title, Album: m.Album, Duration: m.Duration, Cluster: m.ClusterID,
			Artwork: art, Ready: true, Status: "ready",
			Plays: m.Plays, EarlySkips: m.EarlySkips, LastPlayedAt: last,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	// Auth gate skips /api/reload so the worker can call it; still require
	// loopback or a valid session/Bearer when auth is enabled.
	if s.Auth != nil && s.Auth.Cfg.Enabled() && !s.Auth.Authorized(r) && !isLoopback(r) {
		writeErr(w, 401, "auth_required", "unauthorized")
		return
	}
	if err := s.Reload(); err != nil {
		writeErr(w, 500, "reload", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "tracks": s.TrackCount()})
}

func isLoopback(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (s *Server) Reload() error {
	if err := s.App.Reload(); err != nil {
		return err
	}
	return s.reloadProfiles()
}

// TrackCount is the shared catalog size. In single-user mode the playback
// index is that catalog plus this profile's counters, so its size matches.
func (s *Server) TrackCount() int {
	n, _ := s.catalogStats()
	return n
}

func (s *Server) catalogStats() (tracks, dim int) {
	if s.Idx != nil {
		return s.Idx.Size(), s.Idx.Dim()
	}
	if s.App == nil || s.App.Catalog == nil {
		return 0, 0
	}
	cat, err := s.App.Catalog.Current()
	if err != nil || cat == nil {
		return 0, 0
	}
	return cat.Size(), cat.Dim()
}
