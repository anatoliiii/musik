package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
)

type principalKey struct{}
type principal struct {
	UserID      string
	Profile     db.Profile
	Admin       bool
	Session     string
	DeviceToken bool
	DeviceID    string
}
type multiUserState struct {
	oidc     *auth.OIDCClient
	mu       sync.Mutex
	profiles map[string]*Server
}

func (s *Server) EnableMultiUser(ctx context.Context) error {
	if !s.Cfg.MultiUser {
		return nil
	}
	if s.Cfg.Password != "" || s.Cfg.APIToken != "" || s.Cfg.AuthDisabled {
		return errors.New("multi-user mode rejects MUSIK_PASSWORD, MUSIK_API_TOKEN and MUSIK_AUTH_DISABLED")
	}
	base, err := url.Parse(s.Cfg.PublicBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return errors.New("multi-user mode requires an HTTPS MUSIK_PUBLIC_BASE_URL without a path")
	}
	for _, origin := range s.Cfg.CORSOrigins {
		if origin != s.Cfg.PublicBaseURL {
			return errors.New("multi-user mode requires same-origin CORS")
		}
	}
	ready, err := s.Store.BootstrapReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("create the first administrator invitation with musik-player admin bootstrap before multi-user startup")
	}
	client, err := auth.NewOIDCClient(oidc.ClientContext(ctx, s.HTTP), auth.OIDCConfig{Issuer: s.Cfg.OIDCIssuer, ClientID: s.Cfg.OIDCClientID, ClientSecret: s.Cfg.OIDCClientSecret, RedirectURL: strings.TrimRight(s.Cfg.PublicBaseURL, "/") + "/api/auth/oidc/default/callback"})
	if err != nil {
		return err
	}
	s.multi = &multiUserState{oidc: client, profiles: make(map[string]*Server)}
	return nil
}

func (s *Server) multiUserMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p *principal
		cookie, cookieErr := r.Cookie(auth.CookieName)
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if authorization != "" && cookieErr == nil {
			writeErr(w, 400, "credential_conflict", "send either a browser session or a bearer token")
			return
		}
		if authorization != "" {
			const bearerPrefix = "Bearer "
			if strings.HasPrefix(authorization, bearerPrefix) && strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix)) != "" {
				secret := strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix))
				user, profile, tokenID, err := s.Store.AuthenticateDeviceToken(r.Context(), secret)
				if err == nil {
					p = &principal{UserID: user, Profile: profile, DeviceToken: true, DeviceID: tokenID}
					r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
				}
			}
		} else if cookieErr == nil {
			user, profile, err := s.Store.UserSession(r.Context(), cookie.Value)
			if err == nil {
				admin, e := s.Store.UserIsAdmin(r.Context(), user)
				if e == nil {
					p = &principal{UserID: user, Profile: profile, Admin: admin, Session: cookie.Value}
					r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
				}
			}
		}
		path := r.URL.Path
		public := r.Method == "GET" && (path == "/api/auth/me" || path == "/api/health" || path == "/api/themes" || path == "/api/openapi.json" || path == "/manifest.webmanifest" || strings.HasPrefix(path, "/api/auth/oidc/default/") || strings.HasPrefix(path, "/listen/"))
		if public || !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if path == "/api/auth/logout" && r.Method == http.MethodPost && p == nil {
			next.ServeHTTP(w, r)
			return
		}
		if path == "/api/reload" && isLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		if p == nil {
			writeErr(w, 401, "auth_required", "sign in required")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		if p.DeviceToken && !deviceRouteAllowed(r.Method, path) {
			writeErr(w, 403, "device_token_scope", "operation is not available to device tokens")
			return
		}
		adminOnly := strings.HasPrefix(path, "/api/admin/") || strings.HasPrefix(path, "/api/jobs") || path == "/api/library/upload" || path == "/api/library/rescan" || path == "/api/reload"
		if p.DeviceToken && strings.HasPrefix(path, "/api/jobs/") && (path == "/api/jobs/mix_pack" || deviceJobIDPath(r.Method, path)) {
			adminOnly = false
		}
		if adminOnly && !p.Admin {
			writeErr(w, 403, "admin_required", "administrator required")
			return
		}
		if !p.DeviceToken && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != strings.TrimRight(s.Cfg.PublicBaseURL, "/") {
				writeErr(w, 403, "csrf", "invalid origin")
				return
			}
			if !s.Store.ValidUserCSRF(r.Context(), p.Session, r.Header.Get("X-CSRF-Token")) {
				writeErr(w, 403, "csrf", "invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func deviceRouteAllowed(method, path string) bool {
	if strings.HasPrefix(path, "/api/admin/") || strings.HasPrefix(path, "/api/profiles") ||
		strings.HasPrefix(path, "/api/account/device-tokens") || strings.HasPrefix(path, "/api/auth/oidc/") {
		return false
	}
	if strings.HasPrefix(path, "/api/jobs") {
		return (method == http.MethodPost && path == "/api/jobs/mix_pack") || deviceJobIDPath(method, path)
	}
	return true
}

func deviceJobIDPath(method, path string) bool {
	if method != http.MethodGet || !strings.HasPrefix(path, "/api/jobs/") {
		return false
	}
	id := strings.TrimPrefix(path, "/api/jobs/")
	if id == "" || strings.Contains(id, "/") {
		return false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (s *Server) requestServer(r *http.Request) (*Server, error) {
	if !s.Cfg.MultiUser {
		return s, nil
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/profiles") || strings.HasPrefix(path, "/api/admin/") || path == "/api/health" || path == "/api/themes" || path == "/api/openapi.json" || path == "/api/reload" || path == "/manifest.webmanifest" {
		return s, nil
	}
	p, _ := r.Context().Value(principalKey{}).(*principal)
	profileID := ""
	if strings.HasPrefix(path, "/listen/") {
		var err error
		profileID, err = s.Store.RadioShareProfile(r.Context(), r.PathValue("token"))
		if err != nil {
			return nil, err
		}
	} else if p != nil {
		profileID = p.Profile.ID
	}
	if profileID == "" || s.multi == nil {
		return nil, db.ErrProfileNotFound
	}
	s.multi.mu.Lock()
	defer s.multi.mu.Unlock()
	if runtime := s.multi.profiles[profileID]; runtime != nil {
		return runtime, nil
	}
	cfg := s.Cfg
	cfg.MultiUser = false
	cfg.WorkerAutostart = false
	cfg.RankerPath = filepath.Join(filepath.Dir(cfg.RankerPath), "profiles", profileID, "ranker.json")
	runtime := newServer(cfg, s.Store.ForProfile(profileID), index.New(cfg), taste.New(), s.Static, s.backgroundIO)
	if err := runtime.Reload(); err != nil {
		return nil, err
	}
	s.multi.profiles[profileID] = runtime
	return runtime, nil
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.MultiUser || s.multi == nil {
		writeErr(w, 503, "provider_unavailable", "OIDC unavailable")
		return
	}
	redirect, binding, err := s.multi.oidc.Begin(r.URL.Query().Get("invitation"))
	if err != nil {
		writeErr(w, 503, "provider_unavailable", "OIDC unavailable")
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: "musik_oidc_binding", Value: binding, Path: "/api/auth/oidc/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, redirect, http.StatusFound)
}

type oidcLinkState struct {
	UserID      string `json:"user_id"`
	SessionHash string `json:"session_hash"`
}

func (s *Server) handleOIDCLinkStart(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if !s.Cfg.MultiUser || s.multi == nil || s.multi.oidc == nil {
		writeErr(w, 503, "provider_unavailable", "OIDC unavailable")
		return
	}
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	state, err := json.Marshal(oidcLinkState{UserID: p.UserID, SessionHash: db.SessionFingerprint(p.Session)})
	if err != nil {
		writeErr(w, 500, "oidc_link", "link could not be started")
		return
	}
	redirect, binding, err := s.multi.oidc.BeginWithData("", string(state))
	if err != nil {
		writeErr(w, 503, "provider_unavailable", "OIDC unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, &http.Cookie{Name: "musik_oidc_binding", Value: binding, Path: "/api/auth/oidc/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	writeJSON(w, map[string]string{"authorization_url": redirect})
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.MultiUser || s.multi == nil {
		writeErr(w, 503, "provider_unavailable", "OIDC unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	cookie, err := r.Cookie("musik_oidc_binding")
	if err != nil {
		writeErr(w, 403, "oidc_invalid", "sign-in failed")
		return
	}
	identity, err := s.multi.oidc.Complete(r.Context(), r.URL.Query().Get("state"), cookie.Value, r.URL.Query().Get("code"))
	http.SetCookie(w, &http.Cookie{Name: "musik_oidc_binding", Path: "/api/auth/oidc/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if err != nil {
		writeErr(w, 403, "oidc_invalid", "sign-in failed")
		return
	}
	if identity.FlowData != "" {
		var link oidcLinkState
		p := caller(r)
		session, sessionErr := r.Cookie(auth.CookieName)
		if json.Unmarshal([]byte(identity.FlowData), &link) != nil || p == nil || sessionErr != nil ||
			p.UserID != link.UserID || subtle.ConstantTimeCompare([]byte(db.SessionFingerprint(session.Value)), []byte(link.SessionHash)) != 1 {
			writeErr(w, 403, "oidc_link_invalid", "identity link failed")
			return
		}
		if err := s.Store.LinkExternalIdentity(r.Context(), session.Value, link.UserID, identity.Issuer, identity.Subject); err != nil {
			if errors.Is(err, db.ErrIdentityAlreadyLinked) {
				writeErr(w, 409, "identity_already_linked", "identity is already linked to an account")
			} else {
				writeErr(w, 403, "oidc_link_invalid", "identity link failed")
			}
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	user, err := s.Store.IdentityUser(r.Context(), identity.Issuer, identity.Subject)
	if errors.Is(err, db.ErrIdentityUnknown) {
		user, _, err = s.Store.AcceptInvitation(r.Context(), identity.Invitation, db.VerifiedIdentity{Issuer: identity.Issuer, Subject: identity.Subject, DisplayName: identity.Name, Email: identity.Email, EmailVerified: identity.EmailVerified})
	}
	if err != nil {
		writeErr(w, 403, "invitation_invalid", "sign-in requires a valid invitation")
		return
	}
	if previous, e := r.Cookie(auth.CookieName); e == nil {
		if e := s.Store.RevokeUserSession(r.Context(), previous.Value); e != nil {
			writeErr(w, 500, "session", "sign-in failed")
			return
		}
	}
	token, csrf, err := s.Store.IssueUserSession(r.Context(), user, 14*24*time.Hour)
	if err != nil {
		writeErr(w, 500, "session", "sign-in failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 14 * 86400})
	http.SetCookie(w, &http.Cookie{Name: "musik_csrf", Value: csrf, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 14 * 86400})
	http.Redirect(w, r, "/", http.StatusFound)
}

func clearUserCookies(w http.ResponseWriter) {
	for _, name := range []string{auth.CookieName, "musik_csrf"} {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", HttpOnly: name == auth.CookieName, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}

func (s *Server) handleUserAuthMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, _ := r.Context().Value(principalKey{}).(*principal)
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		writeJSON(w, map[string]any{"ok": p != nil, "auth_enabled": true})
		return
	}
	if p == nil {
		writeJSON(w, map[string]any{"ok": false, "authenticated": false, "auth_enabled": true, "multi_user": true, "oidc_login_url": "/api/auth/oidc/default/start"})
		return
	}
	name, roles, err := s.Store.UserInfo(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, 500, "account", "account unavailable")
		return
	}
	profiles, err := s.Store.ListProfiles(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, 500, "profile", "profiles unavailable")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "authenticated": true, "auth_enabled": true, "multi_user": true, "user": map[string]any{"id": p.UserID, "display_name": name, "roles": roles}, "active_profile": p.Profile, "profiles": profiles})
}

func caller(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	profiles, err := s.Store.ListProfiles(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, 500, "profile", "profiles unavailable")
		return
	}
	writeJSON(w, profiles)
}
func decodeSmall(w http.ResponseWriter, r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(dst)
}
func profileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var request struct {
		Name string `json:"name"`
	}
	if decodeSmall(w, r, &request) != nil || strings.TrimSpace(request.Name) == "" || utf8.RuneCountInString(request.Name) > 128 {
		writeErr(w, 400, "profile_name", "profile name must contain 1 to 128 characters")
		return "", false
	}
	return request.Name, true
}
func (s *Server) handleProfileCreate(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	name, ok := profileName(w, r)
	if !ok {
		return
	}
	profile, err := s.Store.CreateProfile(r.Context(), p.UserID, name)
	if err != nil {
		writeErr(w, 500, "profile", "profile creation failed")
		return
	}
	writeJSON(w, profile)
}
func (s *Server) handleProfileRename(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	name, ok := profileName(w, r)
	if !ok {
		return
	}
	if err := s.Store.RenameProfile(r.Context(), p.UserID, r.PathValue("profile_id"), name); err != nil {
		writeErr(w, 404, "profile_not_found", "profile not found")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	err := s.Store.DeleteProfile(r.Context(), p.UserID, r.PathValue("profile_id"))
	if errors.Is(err, db.ErrProfileRequired) {
		writeErr(w, 409, "profile_required", err.Error())
		return
	}
	if err != nil {
		writeErr(w, 404, "profile_not_found", "profile not found")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
func (s *Server) handleProfileActivate(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil {
		writeErr(w, 401, "auth_required", "sign in required")
		return
	}
	if err := s.Store.ActivateUserProfile(r.Context(), p.Session, p.UserID, r.PathValue("profile_id")); err != nil {
		writeErr(w, 404, "profile_not_found", "profile not found")
		return
	}
	_, profile, err := s.Store.UserSession(r.Context(), p.Session)
	if err != nil {
		writeErr(w, 500, "session", "session unavailable")
		return
	}
	writeJSON(w, profile)
}
func (s *Server) handleInvitationCreate(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil || !p.Admin {
		writeErr(w, 403, "admin_required", "administrator required")
		return
	}
	var req struct {
		Email    string `json:"email"`
		TTLHours int    `json:"ttl_hours"`
	}
	if decodeSmall(w, r, &req) != nil {
		writeErr(w, 400, "bad_json", "invalid invitation")
		return
	}
	if req.TTLHours == 0 {
		req.TTLHours = 24
	}
	if req.TTLHours < 1 || req.TTLHours > 168 || len(req.Email) > 254 {
		writeErr(w, 400, "invitation", "invalid invitation constraints")
		return
	}
	id, secret, err := s.Store.CreateInvitation(r.Context(), p.UserID, s.Cfg.OIDCIssuer, req.Email, time.Now().Add(time.Duration(req.TTLHours)*time.Hour))
	if err != nil {
		writeErr(w, 500, "invitation", "invitation creation failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"id": id, "secret": secret, "url": s.Cfg.PublicBaseURL + "/api/auth/oidc/default/start?invitation=" + url.QueryEscape(secret)})
}
func (s *Server) handleInvitationRevoke(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	if p == nil || !p.Admin {
		writeErr(w, 403, "admin_required", "administrator required")
		return
	}
	if err := s.Store.RevokeInvitation(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeErr(w, 404, "invitation_not_found", "invitation not found")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
