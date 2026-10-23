package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/torwin-job/musik/player/internal/db"
)

type favReq struct {
	Type    string `json:"type"` // track|artist|album
	TrackID int64  `json:"track_id"`
	Artist  string `json:"artist"`
	Album   string `json:"album"`
}

func (s *Server) enrichArtistCard(artist string) map[string]any {
	rows := s.Idx.RowsForArtist(artist)
	card := map[string]any{
		"type": "artist", "artist": artist, "tracks": len(rows), "favorited": true,
	}
	if len(rows) > 0 {
		m := s.Idx.MetaAt(rows[0])
		card["cover_track_id"] = m.ID
		if m.ArtworkPath != "" {
			card["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
	}
	return card
}

func (s *Server) enrichAlbumCard(artist, album string) map[string]any {
	rows := s.Idx.RowsForAlbum(artist, album)
	card := map[string]any{
		"type": "album", "artist": artist, "album": album, "tracks": len(rows), "favorited": true,
	}
	if len(rows) > 0 {
		m := s.Idx.MetaAt(rows[0])
		card["cover_track_id"] = m.ID
		if artist == "" {
			card["artist"] = m.Artist
		}
		if m.ArtworkPath != "" {
			card["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
	}
	return card
}

func (s *Server) handleFavoritesStatus(w http.ResponseWriter, r *http.Request) {
	typ := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if typ == "" {
		typ = "track"
	}
	switch typ {
	case "track", "song":
		id, _ := strconv.ParseInt(r.URL.Query().Get("track_id"), 10, 64)
		if id == 0 {
			writeErr(w, 400, "track_id", "track_id required")
			return
		}
		writeJSON(w, map[string]any{"type": "track", "track_id": id, "favorited": s.Store.FavoritesHas(id)})
	case "artist":
		artist := strings.TrimSpace(r.URL.Query().Get("artist"))
		if artist == "" {
			writeErr(w, 400, "artist", "artist required")
			return
		}
		writeJSON(w, map[string]any{"type": "artist", "artist": artist, "favorited": s.Store.FavArtistHas(artist)})
	case "album":
		artist := strings.TrimSpace(r.URL.Query().Get("artist"))
		album := strings.TrimSpace(r.URL.Query().Get("album"))
		if album == "" {
			writeErr(w, 400, "album", "album required")
			return
		}
		writeJSON(w, map[string]any{
			"type": "album", "artist": artist, "album": album,
			"favorited": s.Store.FavAlbumHas(artist, album),
		})
	default:
		writeErr(w, 400, "type", "type must be track|artist|album")
	}
}

func (s *Server) handleFavoritesList(w http.ResponseWriter, r *http.Request) {
	tracks, err := s.Store.FavoritesList()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	artists, err := s.Store.FavArtistsList()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	albums, err := s.Store.FavAlbumsList()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}

	enriched := make([]any, 0, len(tracks))
	ids := make([]int64, 0, len(tracks))
	for _, t := range tracks {
		ids = append(ids, t.TrackID)
		enriched = append(enriched, map[string]any{
			"type": "track", "track_id": t.TrackID, "artist": t.Artist, "title": t.Title,
			"duration": t.Duration, "position": t.Position,
			"track": s.trackJSON(t.TrackID),
		})
	}
	artOut := make([]any, 0, len(artists))
	for _, a := range artists {
		artOut = append(artOut, s.enrichArtistCard(a.Artist))
	}
	albOut := make([]any, 0, len(albums))
	for _, a := range albums {
		albOut = append(albOut, s.enrichAlbumCard(a.Artist, a.Album))
	}

	only := r.URL.Query().Get("type")
	out := map[string]any{
		"tracks":  enriched,
		"artists": artOut,
		"albums":  albOut,
		"ids":     ids,
		"count":   len(tracks),
		"counts": map[string]int{
			"tracks":  len(tracks),
			"artists": len(artists),
			"albums":  len(albums),
		},
	}
	switch only {
	case "track", "tracks":
		writeJSON(w, map[string]any{"tracks": enriched, "ids": ids, "count": len(tracks)})
	case "artist", "artists":
		writeJSON(w, map[string]any{"artists": artOut, "count": len(artOut)})
	case "album", "albums":
		writeJSON(w, map[string]any{"albums": albOut, "count": len(albOut)})
	default:
		writeJSON(w, out)
	}
}

func (s *Server) handleFavoritesAdd(w http.ResponseWriter, r *http.Request) {
	var req favReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	out, code, errMsg := s.favMutate(req, true)
	if code != 0 {
		writeErr(w, code, "favorite", errMsg)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleFavoritesRemove(w http.ResponseWriter, r *http.Request) {
	var req favReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	out, code, errMsg := s.favMutate(req, false)
	if code != 0 {
		writeErr(w, code, "favorite", errMsg)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleFavoritesToggle(w http.ResponseWriter, r *http.Request) {
	var req favReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if typ == "" {
		typ = "track"
	}
	switch typ {
	case "track", "song":
		if req.TrackID == 0 {
			writeErr(w, 400, "track_id_required", "track_id required")
			return
		}
		typ = "track"
	case "artist":
		req.Artist = strings.TrimSpace(req.Artist)
		if req.Artist == "" && req.TrackID != 0 {
			if tj, ok := s.trackJSON(req.TrackID).(map[string]any); ok {
				req.Artist, _ = tj["artist"].(string)
			}
		}
		if req.Artist == "" {
			writeErr(w, 400, "artist_required", "artist required")
			return
		}
	case "album":
		req.Artist = strings.TrimSpace(req.Artist)
		req.Album = strings.TrimSpace(req.Album)
		if (req.Artist == "" || req.Album == "") && req.TrackID != 0 {
			if tj, ok := s.trackJSON(req.TrackID).(map[string]any); ok {
				if req.Artist == "" {
					req.Artist, _ = tj["artist"].(string)
				}
				if req.Album == "" {
					req.Album, _ = tj["album"].(string)
				}
			}
		}
		if req.Album == "" {
			writeErr(w, 400, "album_required", "album required")
			return
		}
	default:
		writeErr(w, 400, "invalid_type", "type must be track|artist|album")
		return
	}
	out, err := s.Store.ToggleFavorite(r.Context(), typ, req.TrackID, req.Artist, req.Album)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, favoriteToggleJSON(out))
}

func favoriteToggleJSON(out db.FavoriteToggle) map[string]any {
	counts := map[string]int{
		"tracks": out.Tracks, "artists": out.Artists, "albums": out.Albums,
	}
	switch out.Type {
	case "artist":
		return map[string]any{
			"ok": true, "type": "artist", "favorited": out.Favorited, "artist": out.Artist, "counts": counts,
		}
	case "album":
		return map[string]any{
			"ok": true, "type": "album", "favorited": out.Favorited,
			"artist": out.Artist, "album": out.Album, "counts": counts,
		}
	default:
		return map[string]any{
			"ok": true, "type": "track", "favorited": out.Favorited, "track_id": out.TrackID,
			"count": out.Tracks, "counts": counts,
		}
	}
}

func (s *Server) favCounts() map[string]int {
	return map[string]int{
		"tracks":  s.Store.FavoritesCount(),
		"artists": s.Store.FavArtistCount(),
		"albums":  s.Store.FavAlbumCount(),
	}
}

func (s *Server) favMutate(req favReq, add bool) (map[string]any, int, string) {
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if typ == "" {
		typ = "track"
	}
	switch typ {
	case "track", "song":
		if req.TrackID == 0 {
			return nil, 400, "track_id required"
		}
		var err error
		if add {
			err = s.Store.FavoritesAdd(req.TrackID)
		} else {
			err = s.Store.FavoritesRemove(req.TrackID)
		}
		if err != nil {
			return nil, 500, err.Error()
		}
		return map[string]any{
			"ok": true, "type": "track", "favorited": add, "track_id": req.TrackID,
			"count": s.Store.FavoritesCount(), "counts": s.favCounts(),
		}, 0, ""
	case "artist":
		artist := strings.TrimSpace(req.Artist)
		if artist == "" {
			return nil, 400, "artist required"
		}
		var err error
		if add {
			err = s.Store.FavArtistAdd(artist)
		} else {
			err = s.Store.FavArtistRemove(artist)
		}
		if err != nil {
			return nil, 500, err.Error()
		}
		return map[string]any{
			"ok": true, "type": "artist", "favorited": add, "artist": artist, "counts": s.favCounts(),
		}, 0, ""
	case "album":
		artist := strings.TrimSpace(req.Artist)
		album := strings.TrimSpace(req.Album)
		if album == "" {
			return nil, 400, "album required"
		}
		var err error
		if add {
			err = s.Store.FavAlbumAdd(artist, album)
		} else {
			err = s.Store.FavAlbumRemove(artist, album)
		}
		if err != nil {
			return nil, 500, err.Error()
		}
		return map[string]any{
			"ok": true, "type": "album", "favorited": add, "artist": artist, "album": album, "counts": s.favCounts(),
		}, 0, ""
	default:
		return nil, 400, "type must be track|artist|album"
	}
}
