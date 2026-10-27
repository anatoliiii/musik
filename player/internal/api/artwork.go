package api

import (
	"net/http"
	"strconv"
)

func (s *Server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	if id, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil && s.trackHidden(id) {
		writeErr(w, 404, "not_found", "not found")
		return
	}
	s.Media.ServeArtwork(w, r)
}
