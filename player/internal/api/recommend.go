package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/recommend"
)

func trackHitJSON(h recommend.TrackHit) map[string]any {
	item := map[string]any{
		"id": h.ID, "artist": h.Artist, "title": h.Title, "album": h.Album,
		"duration": h.Duration, "cosine": h.Cosine,
		"stream":      "/api/stream/" + strconv.FormatInt(h.ID, 10),
		"explanation": "похоже по звучанию",
	}
	if h.HasArtwork {
		item["artwork"] = "/api/artwork/" + strconv.FormatInt(h.ID, 10)
	}
	return item
}

func artistHitJSON(h recommend.ArtistHit) map[string]any {
	item := map[string]any{
		"type": "artist", "artist": h.Artist, "tracks": h.Tracks,
		"cosine": h.Cosine, "cover_track_id": h.CoverTrackID,
		"explanation": h.Explanation,
	}
	if h.HasArtwork {
		item["artwork"] = "/api/artwork/" + strconv.FormatInt(h.CoverTrackID, 10)
	}
	return item
}

func albumHitJSON(h recommend.AlbumHit) map[string]any {
	item := map[string]any{
		"type": "album", "artist": h.Artist, "album": h.Album, "tracks": h.Tracks,
		"cosine": h.Cosine, "cover_track_id": h.CoverTrackID,
		"explanation": h.Explanation,
	}
	if h.HasArtwork {
		item["artwork"] = "/api/artwork/" + strconv.FormatInt(h.CoverTrackID, 10)
	}
	return item
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("similar", time.Since(started)) }()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	if _, ok := s.Idx.RowOf(id); !ok {
		writeErr(w, 404, "not_found", "not in index")
		return
	}
	hits := recommend.SimilarTracks(s.Idx, id, 10)
	blocks := s.globalBlocks()
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if blocks != nil && blocks.HardBlocked(h.ID) {
			continue
		}
		out = append(out, map[string]any{
			"id": h.ID, "artist": h.Artist, "title": h.Title, "cosine": h.Cosine,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleSimilarArtists(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("similar", time.Since(started)) }()
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	if artist == "" {
		writeErr(w, 400, "artist_required", "artist required")
		return
	}
	hits := recommend.SimilarArtists(s.Idx, artist, 12)
	blocks := s.globalBlocks()
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if blocks != nil && blocks.BlocksArtist(h.Artist) {
			continue
		}
		out = append(out, artistHitJSON(h))
	}
	writeJSON(w, map[string]any{"seed": artist, "artists": out})
}

func (s *Server) handleSimilarAlbums(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("similar", time.Since(started)) }()
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	if album == "" {
		writeErr(w, 400, "album_required", "album required")
		return
	}
	hits := recommend.SimilarAlbums(s.Idx, artist, album, 12)
	blocks := s.globalBlocks()
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if blocks != nil && blocks.BlocksAlbum(h.Artist, h.Album) {
			continue
		}
		out = append(out, albumHitJSON(h))
	}
	writeJSON(w, map[string]any{
		"seed":   map[string]string{"artist": artist, "album": album},
		"albums": out,
	})
}

func (s *Server) handleRecommendSeed(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("recommend_seed", time.Since(started)) }()
	q := r.URL.Query()
	typ := strings.ToLower(strings.TrimSpace(q.Get("type")))
	limit := 20
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	seed := map[string]any{"type": typ}
	var hits []recommend.TrackHit

	switch typ {
	case "track", "song", "":
		id, _ := strconv.ParseInt(q.Get("track_id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(q.Get("id"), 10, 64)
		}
		if id == 0 {
			writeErr(w, 400, "track_id", "track_id required")
			return
		}
		if _, ok := s.Idx.RowOf(id); !ok {
			writeErr(w, 404, "not_found", "track not in index")
			return
		}
		seed["track_id"] = id
		seed["track"] = s.trackJSON(id)
		hits = recommend.FromTrack(s.Idx, id, limit)
	case "artist":
		artist := strings.TrimSpace(q.Get("artist"))
		if artist == "" {
			writeErr(w, 400, "artist", "artist required")
			return
		}
		if s.Idx.CentroidOf(s.Idx.RowsForArtist(artist)) == nil {
			writeErr(w, 404, "empty", "no embeddings for seed")
			return
		}
		seed["artist"] = artist
		hits = recommend.FromArtist(s.Idx, artist, limit)
	case "album":
		artist := strings.TrimSpace(q.Get("artist"))
		album := strings.TrimSpace(q.Get("album"))
		if album == "" {
			writeErr(w, 400, "album", "album required")
			return
		}
		if s.Idx.CentroidOf(s.Idx.RowsForAlbum(artist, album)) == nil {
			writeErr(w, 404, "empty", "no embeddings for seed")
			return
		}
		seed["artist"] = artist
		seed["album"] = album
		hits = recommend.FromAlbum(s.Idx, artist, album, limit)
	default:
		writeErr(w, 400, "type", "type must be track|artist|album")
		return
	}
	blocks := s.globalBlocks()
	tracks := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if blocks != nil && blocks.HardBlocked(h.ID) {
			continue
		}
		tracks = append(tracks, trackHitJSON(h))
	}
	writeJSON(w, map[string]any{"ok": true, "seed": seed, "tracks": tracks})
}

func (s *Server) handleRecommendFavorites(w http.ResponseWriter, _ *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("recommend_favorites", time.Since(started)) }()
	mix := recommend.FromFavorites(s.Store, s.Idx)
	if mix.Empty {
		writeJSON(w, map[string]any{
			"ok": false, "empty": true,
			"hint":     "Добавь любимые песни, артистов или альбомы (♥)",
			"tracks":   []any{},
			"artists":  []any{},
			"albums":   []any{},
			"based_on": mix.BasedOn,
		})
		return
	}
	blocks := s.globalBlocks()
	tracks := make([]map[string]any, 0, len(mix.Tracks))
	for _, h := range mix.Tracks {
		if blocks != nil && blocks.HardBlocked(h.ID) {
			continue
		}
		tracks = append(tracks, trackHitJSON(h))
	}
	artists := make([]map[string]any, 0, len(mix.Artists))
	for _, h := range mix.Artists {
		if blocks != nil && blocks.BlocksArtist(h.Artist) {
			continue
		}
		artists = append(artists, artistHitJSON(h))
	}
	albums := make([]map[string]any, 0, len(mix.Albums))
	for _, h := range mix.Albums {
		if blocks != nil && blocks.BlocksAlbum(h.Artist, h.Album) {
			continue
		}
		albums = append(albums, albumHitJSON(h))
	}
	writeJSON(w, map[string]any{
		"ok": true, "based_on": mix.BasedOn,
		"tracks": tracks, "artists": artists, "albums": albums,
		"explanation": "на основе твоих любимых — по звучанию (CLAP)",
	})
}
