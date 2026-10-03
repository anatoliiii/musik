package api

import (
	"errors"
	"net/http"

	"github.com/torwin-job/musik/player/internal/db"
)

func deviceTokenManager(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	p := caller(r)
	if p == nil {
		writeErr(w, http.StatusUnauthorized, "auth_required", "sign in required")
		return nil, false
	}
	if p.DeviceToken {
		writeErr(w, http.StatusForbidden, "device_token_scope", "manage device tokens from the browser account settings")
		return nil, false
	}
	return p, true
}

func (s *Server) handleDeviceTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := deviceTokenManager(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	tokens, err := s.Store.ListDeviceTokens(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device_tokens", "device tokens unavailable")
		return
	}
	writeJSON(w, map[string]any{"tokens": tokens})
}

func (s *Server) handleDeviceTokenCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := deviceTokenManager(w, r)
	if !ok {
		return
	}
	var request struct {
		Name      string `json:"name"`
		ProfileID string `json:"profile_id"`
	}
	if decodeSmall(w, r, &request) != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "bad json")
		return
	}
	token, secret, err := s.Store.CreateDeviceToken(r.Context(), p.UserID, request.ProfileID, request.Name)
	if errors.Is(err, db.ErrDeviceTokenNameRequired) {
		writeErr(w, http.StatusBadRequest, "device_name_required", "device name must contain 1 to 128 characters")
		return
	}
	if errors.Is(err, db.ErrProfileNotFound) {
		writeErr(w, http.StatusNotFound, "profile_not_found", "profile not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device_token", "device token could not be created")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, map[string]any{"device": token, "token": secret})
}

func (s *Server) handleDeviceTokenRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := deviceTokenManager(w, r)
	if !ok {
		return
	}
	if err := s.Store.RevokeDeviceToken(r.Context(), p.UserID, r.PathValue("id")); errors.Is(err, db.ErrDeviceTokenNotFound) {
		writeErr(w, http.StatusNotFound, "device_token_not_found", "device token not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "device_token", "device token could not be revoked")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleDeviceTokenReplace(w http.ResponseWriter, r *http.Request) {
	p, ok := deviceTokenManager(w, r)
	if !ok {
		return
	}
	token, secret, err := s.Store.ReplaceDeviceToken(r.Context(), p.UserID, r.PathValue("id"))
	if errors.Is(err, db.ErrDeviceTokenNotFound) || errors.Is(err, db.ErrProfileNotFound) {
		writeErr(w, http.StatusNotFound, "device_token_not_found", "device token not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "device_token", "device token could not be replaced")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, map[string]any{"device": token, "token": secret})
}
