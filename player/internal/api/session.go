package api

import (
	"encoding/json"
	"net/http"

	"github.com/torwin-job/musik/player/internal/playback"
)

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request, bodySessionID string) *playback.Session {
	id := bodySessionID
	if id == "" {
		id = r.URL.Query().Get("session_id")
	}
	if id == "" {
		id = r.Header.Get("X-Session-Id")
	}
	sess := s.Play.Get(id)
	if sess == nil {
		writeErr(w, 404, "session_not_found", "unknown or missing session_id — call /api/radio/start or /api/session/start")
		return nil
	}
	return sess
}

func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SeedTrackID *int64 `json:"seed_track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sess := s.Play.Start("session", req.SeedTrackID, nil)
	sess.Lock()
	defer sess.Unlock()
	writeJSON(w, s.sessionStartResponse(sess))
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r, "")
	if sess == nil {
		return
	}
	sess.Lock()
	defer sess.Unlock()
	out := map[string]any{
		"session_id":  sess.ID,
		"mode":        sess.Mode,
		"maturity":    s.Play.Maturity(),
		"current":     s.sessionCurrentJSON(sess),
		"queue":       sess.Queue,
		"name":        sess.PlaylistName,
		"kind":        sess.PlaylistKind,
		"index":       sess.DailyPos,
		"count":       len(sess.DailyIDs),
		"fixed":       playback.IsFixedMode(sess.Mode),
		"context_ids": sessionContextIDs(sess),
	}
	annotateRadio(out, sess)
	if playback.IsFixedMode(sess.Mode) {
		out["tracks"] = s.sessionTracks(sess)
	}
	writeJSON(w, out)
}

func (s *Server) handleSessionJump(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Index     *int   `json:"index"`
		TrackID   int64  `json:"track_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	sess := s.requireSession(w, r, req.SessionID)
	if sess == nil {
		return
	}
	sess.Lock()
	defer sess.Unlock()
	if err := s.Play.Jump(sess, req.Index, req.TrackID); err != nil {
		writeErr(w, playHTTPStatus(err), "play", err.Error())
		return
	}
	out := s.playResponse(sess)
	out["ok"] = true
	writeJSON(w, out)
}

func (s *Server) handleSessionBack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	sess := s.requireSession(w, r, req.SessionID)
	if sess == nil {
		return
	}
	sess.Lock()
	defer sess.Unlock()
	if err := s.Play.Back(sess); err != nil {
		writeErr(w, playHTTPStatus(err), "play", err.Error())
		return
	}
	out := s.playResponse(sess)
	out["ok"] = true
	writeJSON(w, out)
}
