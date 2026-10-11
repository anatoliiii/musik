package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/db"
)

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.MultiUser {
		s.handleLocalLogin(w, r)
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

// handleAuthToken signs a phone app in without a browser: it trades the
// credentials for the bearer token the app then sends as Authorization.
// Multi-user: {username, password, device_name, profile_id?} issues a device
// token for that profile (the default one when omitted). Single-user:
// {password} returns the configured MUSIK_API_TOKEN.
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.loginLimiter != nil && !s.loginLimiter.Allow(r) {
		writeErr(w, 429, "rate_limited", "too many login attempts")
		return
	}
	if !s.Cfg.MultiUser {
		var req struct {
			Password string `json:"password"`
		}
		if decodeSmall(w, r, &req) != nil {
			writeErr(w, 400, "bad_json", "bad json")
			return
		}
		if s.Auth == nil || !s.Auth.Cfg.Enabled() || s.Cfg.APIToken == "" {
			writeErr(w, 409, "no_api_token", "server has no API token; set MUSIK_API_TOKEN")
			return
		}
		if !s.Auth.CheckPassword(req.Password) {
			writeErr(w, 401, "bad_password", "неверный пароль")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"token": s.Cfg.APIToken})
		return
	}
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		DeviceName string `json:"device_name"`
		ProfileID  string `json:"profile_id"`
	}
	if decodeSmall(w, r, &req) != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	user, hash, err := s.Store.LocalPassword(r.Context(), strings.ToLower(strings.TrimSpace(req.Username)), "")
	if err != nil || !auth.PasswordMatches(hash, req.Password) {
		writeErr(w, 401, "bad_password", "неверное имя или пароль")
		return
	}
	if strings.TrimSpace(req.DeviceName) == "" {
		req.DeviceName = "Телефон"
	}
	device, secret, err := s.Store.CreateDeviceToken(r.Context(), user, req.ProfileID, req.DeviceName)
	if errors.Is(err, db.ErrProfileNotFound) {
		writeErr(w, 404, "profile_not_found", "profile not found")
		return
	} else if err != nil {
		writeErr(w, 400, "device_token", "device token could not be created")
		return
	}
	profiles, _ := s.Store.ListProfiles(r.Context(), user)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"token": secret, "device": device, "profiles": profiles})
}
