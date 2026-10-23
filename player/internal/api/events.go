package api

import (
	"encoding/json"
	"net/http"

	"github.com/torwin-job/musik/player/internal/playback"
)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var ev playback.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	sess := s.requireSession(w, r, ev.SessionID)
	if sess == nil {
		return
	}
	sess.Lock()
	defer sess.Unlock()
	res := s.Play.ApplyEvent(sess, ev)
	if res.Error != "" {
		status := http.StatusInternalServerError
		code := "event"
		if res.Conflict {
			status = http.StatusConflict
			code = "ambiguous_impression"
		}
		writeErr(w, status, code, res.Error)
		return
	}
	if res.Unknown {
		writeErr(w, 400, "unknown_event", "unknown event type")
		return
	}
	if res.IsEnd {
		var next any
		if res.NextID != 0 {
			next = s.sessionCurrentJSON(sess)
		}
		out := map[string]any{
			"ok": true, "signed_weight": res.SignedWeight,
			"session_id": sess.ID,
			"maturity":   s.Play.Maturity(),
			"next":       next,
			"queue":      sess.Queue,
			"mode":       sess.Mode,
			"next_id":    res.NextID,
			"ended":      res.Ended,
			"ignored":    res.Ignored,
			"index":      sess.DailyPos,
			"name":       sess.PlaylistName,
			"fixed":      playback.IsFixedMode(sess.Mode),
		}
		if res.Ignored {
			out["reason"] = res.IgnoreReason
		}
		annotateRadio(out, sess)
		if playback.IsFixedMode(sess.Mode) {
			out["tracks"] = s.sessionTracks(sess)
		}
		writeJSON(w, out)
		return
	}
	if res.IsRate {
		if res.Ignored {
			writeJSON(w, map[string]any{
				"ok": true, "ignored": true, "reason": res.IgnoreReason,
				"session_id": sess.ID, "current": sess.Current, "queue": sess.Queue,
				"maturity": s.Play.Maturity(), "rating": res.Rating,
			})
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "ignored": false, "rating": res.Rating, "flipped": res.Flipped,
			"session_id": sess.ID, "current": sess.Current, "queue": sess.Queue,
			"maturity": s.Play.Maturity(),
		})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "session_id": sess.ID, "current": sess.Current, "queue": sess.Queue, "maturity": s.Play.Maturity(),
	})
}
