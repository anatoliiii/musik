package api

import (
	"encoding/json"
	"github.com/torwin-job/musik/player/internal/auth"
	"net/http"
)

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.MultiUser {
		writeErr(w, 403, "oidc_required", "use OIDC sign-in")
		return
	}
	if s.Auth == nil || !s.Auth.Cfg.Enabled() {
		writeJSON(w, map[string]any{"ok": true, "auth": false, "hint": "auth disabled"})
		return
	}
	if s.loginLimiter != nil && !s.loginLimiter.Allow(r) {
		writeErr(w, 429, "rate_limited", "too many login attempts")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	if !s.Auth.CheckPassword(req.Password) {
		writeErr(w, 401, "bad_password", "неверный пароль")
		return
	}
	if err := s.Auth.IssueCookie(w); err != nil {
		writeErr(w, 500, "session", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.MultiUser {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			if err := s.Store.RevokeUserSession(r.Context(), c.Value); err != nil {
				writeErr(w, 500, "session", "logout failed")
				return
			}
		}
		clearUserCookies(w)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if s.Auth != nil {
		s.Auth.ClearCookie(w)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.MultiUser {
		s.handleUserAuthMe(w, r)
		return
	}
	enabled := s.Auth != nil && s.Auth.Cfg.Enabled()
	ok := !enabled || (s.Auth != nil && s.Auth.Authorized(r))
	writeJSON(w, map[string]any{
		"ok": ok, "auth_enabled": enabled,
	})
}
