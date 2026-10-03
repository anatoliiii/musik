package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/rules"
)

func (s *Server) handleRulesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListRadioRules(r.URL.Query().Get("all") == "1")
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if list == nil {
		list = []db.RadioRule{}
	}
	writeJSON(w, map[string]any{"rules": list})
}

func (s *Server) handleRulesCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		db.RadioRule
		Preset  string `json:"preset"`
		TrackID int64  `json:"track_id"`
		Artist  string `json:"artist"`
		Album   string `json:"album"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	rule := req.RadioRule
	if rule.TargetKey == "" {
		switch rule.TargetType {
		case "track":
			if req.TrackID != 0 {
				rule.TargetKey = index.TrackKey(req.TrackID)
			}
		case "song":
			if req.TrackID != 0 {
				if row, ok := s.Idx.RowOf(req.TrackID); ok {
					meta := s.Idx.MetaAt(row)
					if meta.FileMD5 != "" {
						rule.TargetKey = meta.FileMD5
					} else {
						rule.TargetKey = index.SongKey(meta.Artist, meta.Title)
					}
				}
			}
		case "artist":
			rule.TargetKey = index.ArtistKey(req.Artist)
		case "album":
			rule.TargetKey = index.AlbumKey(req.Artist, req.Album)
		}
	}
	if req.Preset != "" && rule.ExpiresAt == "" && req.Preset != "session" && req.Preset != "forever" {
		rule.ExpiresAt = rules.ExpiresAt(req.Preset, time.Now().UTC())
	}
	if req.Preset == "session" {
		rule.Scope = "session"
	}
	created, err := s.Store.CreateRadioRule(rule)
	if err != nil {
		writeErr(w, 400, "rule", err.Error())
		return
	}
	if created.Action == "block" && s.Play != nil {
		s.Play.HideBlocked()
	}
	var playback any
	if req.SessionID != "" && s.Play != nil {
		if sess := s.Play.Get(req.SessionID); sess != nil {
			sess.Lock()
			playback = s.playResponse(sess)
			sess.Unlock()
		}
	}
	writeJSON(w, struct {
		db.RadioRule
		Playback any `json:"playback,omitempty"`
	}{RadioRule: created, Playback: playback})
}

func (s *Server) handleRulePatch(w http.ResponseWriter, r *http.Request) {
	var req db.RadioRule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	req.ID = r.PathValue("id")
	if err := s.Store.UpdateRadioRule(req); err != nil {
		writeErr(w, 400, "rule", err.Error())
		return
	}
	out, err := s.Store.GetRadioRule(req.ID)
	if err != nil || out == nil {
		writeErr(w, 404, "not_found", "rule not found")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ArchiveRadioRule(r.PathValue("id")); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRulesUndo(w http.ResponseWriter, _ *http.Request) {
	rule, err := s.Store.UndoLastRadioRule()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if rule == nil {
		writeErr(w, 404, "not_found", "nothing to undo")
		return
	}
	writeJSON(w, rule)
}
