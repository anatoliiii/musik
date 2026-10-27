package api

import (
	"net/http"
	"strconv"
)

func (s *Server) handleTrackLyrics(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "bad track id")
		return
	}
	if s.trackHidden(id) {
		writeErr(w, 404, "not_found", "track not found")
		return
	}
	ly, ok, err := s.Store.GetLyrics(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if !ok {
		writeJSON(w, map[string]any{
			"track_id":      id,
			"status":        "absent",
			"plain_lyrics":  "",
			"synced_lyrics": "",
		})
		return
	}
	writeJSON(w, ly)
}
