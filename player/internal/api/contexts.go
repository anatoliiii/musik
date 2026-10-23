package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/playback"
)

type contextWrite struct {
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Icon            string          `json:"icon"`
	Influence       *float64        `json:"influence"`
	LearningEnabled *bool           `json:"learning_enabled"`
	Seeds           db.ContextSeeds `json:"seeds"`
}

func (s *Server) handleContextsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListTasteContexts(r.URL.Query().Get("all") == "1")
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if list == nil {
		list = []db.TasteContext{}
	}
	writeJSON(w, map[string]any{"contexts": list})
}

func (s *Server) handleContextsCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeContextWrite(r)
	if err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	ctx := db.TasteContext{
		Kind: req.Kind, Name: req.Name, Icon: req.Icon,
		Seeds: req.Seeds, LearningEnabled: true,
	}
	if req.Influence != nil {
		ctx.Influence = *req.Influence
	}
	if req.LearningEnabled != nil {
		ctx.LearningEnabled = *req.LearningEnabled
	}
	out, err := playback.CreateTasteContext(r.Context(), s.Store, s.Idx, ctx)
	if err != nil {
		writeContextErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleContextGet(w http.ResponseWriter, r *http.Request) {
	ctx, err := s.Store.GetTasteContext(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if ctx == nil {
		writeErr(w, 404, "not_found", "context not found")
		return
	}
	writeJSON(w, ctx)
}

func (s *Server) handleContextPatch(w http.ResponseWriter, r *http.Request) {
	req, err := decodeContextWrite(r)
	if err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	out, err := playback.UpdateTasteContext(r.Context(), s.Store, s.Idx, r.PathValue("id"), playback.TasteContextPatch{
		Name: req.Name, Icon: req.Icon, Influence: req.Influence,
		LearningEnabled: req.LearningEnabled, Seeds: req.Seeds,
	})
	if err != nil {
		writeContextErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleContextDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ArchiveTasteContext(r.PathValue("id")); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleContextActivate(w http.ResponseWriter, r *http.Request) {
	s.toggleContext(w, r, true)
}

func (s *Server) handleContextDeactivate(w http.ResponseWriter, r *http.Request) {
	s.toggleContext(w, r, false)
}

func (s *Server) toggleContext(w http.ResponseWriter, r *http.Request, on bool) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sess := s.Play.Get(req.SessionID)
	if sess == nil {
		writeErr(w, 404, "not_found", "session not found")
		return
	}
	id := r.PathValue("id")
	sess.Lock()
	defer sess.Unlock()
	next := make([]string, 0, len(sess.ActiveContextIDs)+1)
	have := false
	for _, existing := range sess.ActiveContextIDs {
		if existing == id {
			have = true
			if on {
				next = append(next, existing)
			}
			continue
		}
		next = append(next, existing)
	}
	if on && !have {
		next = append(next, id)
	}
	if err := s.Play.SetSessionContexts(sess, next); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "context_ids": next})
}

func (s *Server) handleSessionContexts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID  string   `json:"session_id"`
		ContextIDs []string `json:"context_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	sess := s.Play.Get(req.SessionID)
	if sess == nil {
		writeErr(w, 404, "not_found", "session not found")
		return
	}
	sess.Lock()
	defer sess.Unlock()
	if err := s.Play.SetSessionContexts(sess, req.ContextIDs); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "context_ids": req.ContextIDs})
}

func writeContextErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrContextNotFound) {
		writeErr(w, 404, "not_found", "context not found")
		return
	}
	writeErr(w, 400, "context", err.Error())
}

func decodeContextWrite(r *http.Request) (contextWrite, error) {
	var req contextWrite
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return req, err
	}
	if len(body) == 0 {
		return req, nil
	}
	return req, json.Unmarshal(body, &req)
}
