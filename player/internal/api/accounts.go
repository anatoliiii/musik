package api

import (
	"errors"
	"github.com/torwin-job/musik/player/internal/db"
	"net/http"
)

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil || !p.Admin {
		writeErr(w, 403, "admin_required", "administrator required")
		return
	}
	accounts, err := s.Store.ListAccounts(r.Context())
	if err != nil {
		writeErr(w, 500, "accounts", "accounts unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, accounts)
}
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil || !p.Admin {
		writeErr(w, 403, "admin_required", "administrator required")
		return
	}
	var req struct {
		Status string `json:"status"`
		Admin  *bool  `json:"admin"`
	}
	if decodeSmall(w, r, &req) != nil || (req.Status != "" && req.Status != "active" && req.Status != "disabled") {
		writeErr(w, 400, "account", "invalid account update")
		return
	}
	err := s.Store.UpdateAccount(r.Context(), p.UserID, r.PathValue("id"), req.Status, req.Admin)
	if errors.Is(err, db.ErrLastAdmin) {
		writeErr(w, 409, "admin_required", err.Error())
		return
	}
	if err != nil {
		writeErr(w, 404, "user_not_found", "user not found")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (s *Server) handleInvitations(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil || !p.Admin {
		writeErr(w, 403, "admin_required", "administrator required")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	invitations, err := s.Store.ListInvitations(r.Context())
	if err != nil {
		writeErr(w, 500, "invitations", "invitations unavailable")
		return
	}
	if id := r.PathValue("id"); id != "" {
		for _, invite := range invitations {
			if invite.ID == id {
				writeJSON(w, invite)
				return
			}
		}
		writeErr(w, 404, "invitation_not_found", "invitation not found")
		return
	}
	writeJSON(w, invitations)
}
