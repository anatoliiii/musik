package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/torwin-job/musik/player/internal/playback"
)

func (s *Server) handleRadioStart(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { s.latency.Observe("radio_start", time.Since(started)) }()
	var req struct {
		SeedTrackID *int64   `json:"seed_track_id"`
		SeedArtist  string   `json:"seed_artist"`
		SeedAlbum   string   `json:"seed_album"`
		ContextIDs  []string `json:"context_ids"`
		Profile     string   `json:"transition_profile"`
		Proximity   string   `json:"proximity"`
		SessionID   string   `json:"session_id"`
		Station     string   `json:"station"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var tune *playback.Session
	if req.SessionID != "" && (req.Proximity == "closer" || req.Proximity == "wider") {
		if existing := s.Play.Get(req.SessionID); existing != nil && existing.Mode == "radio" && existing.Anchor.Kind != "" {
			tune = existing
		}
	}
	if req.Station == "forgotten" && s.Play.ForgottenCount() == 0 {
		writeErr(w, 409, "empty_station", "nothing forgotten yet")
		return
	}
	sess := s.Play.Start("radio", req.SeedTrackID, req.ContextIDs, playback.RadioStart{
		Artist: req.SeedArtist, Album: req.SeedAlbum, Proximity: req.Proximity, Session: tune,
		Station: req.Station,
	})
	sess.Lock()
	defer sess.Unlock()
	if tune == nil && req.Profile != "" {
		sess.TransitionProfile = req.Profile
	}
	writeJSON(w, s.sessionStartResponse(sess))
}
