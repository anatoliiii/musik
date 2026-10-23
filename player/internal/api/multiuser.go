package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/torwin-job/musik/player/internal/app"
	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/queue"
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

// A profile is listening state: its store, taste index, and engine.
// Routes, auth, and media stay on the one Server.
type profileState struct {
	store *db.Store
	idx   *index.Index
	taste *taste.Profile
	play  *playback.Engine
	used  atomic.Int64
	refs  atomic.Int32
}

type multiUserState struct {
	oidc     *auth.OIDCClient
	mu       sync.Mutex
	profiles map[string]*profileState
	sweep    sync.Once
}

// Idle profiles are dropped so a long-running process does not keep an
// engine for every account that ever pressed play. A profile with a request
// in flight (refs > 0) stays.
var (
	profileIdleAfter = 30 * time.Minute
	profileCacheMax  = 32
)

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
	s.multi = &multiUserState{profiles: make(map[string]*profileState)}
	s.multi.sweep.Do(func() { go s.profileJanitor() })
	if s.Cfg.OIDCIssuer == "" {
		return nil // local accounts only
	}
	client, err := auth.NewOIDCClient(oidc.ClientContext(ctx, s.HTTP), auth.OIDCConfig{Issuer: s.Cfg.OIDCIssuer, ClientID: s.Cfg.OIDCClientID, ClientSecret: s.Cfg.OIDCClientSecret, RedirectURL: strings.TrimRight(s.Cfg.PublicBaseURL, "/") + "/api/auth/oidc/default/callback"})
	if err != nil {
		return err
	}
	s.multi.oidc = client
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
		route, routed := matchRoute(r)
		if openWithoutSession(route, routed, path, p != nil) {
			if routed && credentialPost(route) {
				if origin := r.Header.Get("Origin"); origin != "" && !s.allowedBrowserOrigin(origin) {
					writeErr(w, 403, "csrf", "invalid origin")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		// The worker calls reload on loopback; the handler still checks the peer.
		if routed && route.path == "/api/reload" && isLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		if p == nil {
			writeErr(w, 401, "auth_required", "sign in required")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		if p.DeviceToken && routed && !deviceMayCall(route, r) {
			writeErr(w, 403, "device_token_scope", "operation is not available to device tokens")
			return
		}
		if routed && route.scope == scopeAdmin && !p.Admin && !(p.DeviceToken && jobDeviceException(route, r)) {
			writeErr(w, 403, "admin_required", "administrator required")
			return
		}
		if !p.DeviceToken && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			origin := r.Header.Get("Origin")
			if origin != "" && !s.allowedBrowserOrigin(origin) {
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

// openWithoutSession is true for static files and for public routes.
// A signed-in logout still falls through so the CSRF check applies.
func openWithoutSession(route routeDescriptor, routed bool, path string, signedIn bool) bool {
	if !routed {
		return !strings.HasPrefix(path, "/api/")
	}
	if route.scope != scopePublic {
		return false
	}
	return !(route.path == "/api/auth/logout" && signedIn)
}

func credentialPost(route routeDescriptor) bool {
	return route.scope == scopePublic && route.method == http.MethodPost && route.path != "/api/auth/logout"
}

// deviceMayCall blocks admin routes and account management. A phone can
// still enqueue its own mix_pack and read that job; profile routes stay open.
func deviceMayCall(route routeDescriptor, r *http.Request) bool {
	if jobDeviceException(route, r) {
		return true
	}
	return route.scope != scopeAdmin && route.scope != scopeUser
}

func jobDeviceException(route routeDescriptor, r *http.Request) bool {
	switch route.path {
	case "/api/jobs/{kind}":
		return r.URL.Path == "/api/jobs/mix_pack"
	case "/api/jobs/{id}":
		return deviceJobIDPath(r.Method, r.URL.Path)
	default:
		return false
	}
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

// serveTarget returns the server that runs this request. Profile routes get a
// view of the same server whose store, taste index, and engine are that
// profile's. The view is not cached and does not own routes, auth, or media.
func (s *Server) serveTarget(r *http.Request, route routeDescriptor) (*Server, func(), error) {
	if !s.Cfg.MultiUser || !route.profileRuntime() {
		return s, nil, nil
	}
	profileID, err := s.requestProfileID(r, route)
	if err != nil {
		return nil, nil, err
	}
	p, err := s.acquireProfile(profileID)
	if err != nil {
		return nil, nil, err
	}
	return s.bind(p), func() { s.releaseProfile(p) }, nil
}

func (s *Server) requestProfileID(r *http.Request, route routeDescriptor) (string, error) {
	if route.path == "/listen/{token}" {
		return s.Store.RadioShareProfile(r.Context(), r.PathValue("token"))
	}
	if p := caller(r); p != nil {
		return p.Profile.ID, nil
	}
	return "", db.ErrProfileNotFound
}

// bind points a request at one profile. Copying the server shares the
// pointer fields (auth, media, catalog, listener counter); only the
// profile's store, index, taste, and engine change.
func (s *Server) bind(p *profileState) *Server {
	view := *s
	view.Store = p.store
	view.Idx = p.idx
	view.Taste = p.taste
	view.Play = p.play
	return &view
}

func (s *Server) acquireProfile(profileID string) (*profileState, error) {
	if profileID == "" || s.multi == nil {
		return nil, db.ErrProfileNotFound
	}
	s.multi.mu.Lock()
	if p := s.multi.profiles[profileID]; p != nil {
		p.refs.Add(1)
		p.used.Store(time.Now().UnixNano())
		s.evictLocked(time.Now())
		s.multi.mu.Unlock()
		return p, nil
	}
	s.multi.mu.Unlock()

	p, err := s.openProfile(profileID)
	if err != nil {
		return nil, err
	}
	s.multi.mu.Lock()
	defer s.multi.mu.Unlock()
	if cur := s.multi.profiles[profileID]; cur != nil {
		cur.refs.Add(1)
		cur.used.Store(time.Now().UnixNano())
		s.evictLocked(time.Now())
		return cur, nil
	}
	p.refs.Store(1)
	p.used.Store(time.Now().UnixNano())
	s.multi.profiles[profileID] = p
	s.evictLocked(time.Now())
	return p, nil
}

func (s *Server) releaseProfile(p *profileState) {
	if p == nil {
		return
	}
	p.used.Store(time.Now().UnixNano())
	p.refs.Add(-1)
}

func (s *Server) openProfile(profileID string) (*profileState, error) {
	store := s.Store.ForProfile(profileID)
	idx := index.New(s.Cfg)
	tp := taste.New()
	cfg := s.Cfg
	cfg.RankerPath = filepath.Join(filepath.Dir(cfg.RankerPath), "profiles", profileID, "ranker.json")
	play := playback.New(cfg, store, idx, tp, queue.NewBuilder(idx, cfg))
	s.wire(play)
	cat, err := s.App.Catalog.Current()
	if err != nil {
		return nil, err
	}
	if err := app.ReloadProfile(store, idx, tp, play, cat); err != nil {
		return nil, err
	}
	log.Printf("profile %s ready tracks=%d", profileID, idx.Size())
	return &profileState{store: store, idx: idx, taste: tp, play: play}, nil
}

func (s *Server) reloadProfiles() error {
	if s.multi == nil {
		return nil
	}
	cat, err := s.App.Catalog.Current()
	if err != nil {
		return err
	}
	s.multi.mu.Lock()
	list := make([]*profileState, 0, len(s.multi.profiles))
	for _, p := range s.multi.profiles {
		list = append(list, p)
	}
	s.multi.mu.Unlock()
	for _, p := range list {
		if err := app.ReloadProfile(p.store, p.idx, p.taste, p.play, cat); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) dropProfile(profileID string) {
	if s.multi == nil || profileID == "" {
		return
	}
	s.multi.mu.Lock()
	delete(s.multi.profiles, profileID)
	s.multi.mu.Unlock()
}

func (s *Server) profileJanitor() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		s.sweepProfiles()
	}
}

func (s *Server) sweepProfiles() {
	if s.multi == nil {
		return
	}
	s.multi.mu.Lock()
	defer s.multi.mu.Unlock()
	s.evictLocked(time.Now())
}

func (s *Server) evictLocked(now time.Time) {
	for id, p := range s.multi.profiles {
		if p.refs.Load() > 0 {
			continue
		}
		if now.Sub(time.Unix(0, p.used.Load())) >= profileIdleAfter {
			delete(s.multi.profiles, id)
		}
	}
	extra := len(s.multi.profiles) - profileCacheMax
	if extra <= 0 {
		return
	}
	type idle struct {
		id   string
		used int64
	}
	quiet := make([]idle, 0, len(s.multi.profiles))
	for id, p := range s.multi.profiles {
		if p.refs.Load() == 0 {
			quiet = append(quiet, idle{id, p.used.Load()})
		}
	}
	sort.Slice(quiet, func(i, j int) bool { return quiet[i].used < quiet[j].used })
	for i := 0; i < extra && i < len(quiet); i++ {
		delete(s.multi.profiles, quiet[i].id)
	}
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.MultiUser || s.multi == nil || s.multi.oidc == nil {
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
	if !s.Cfg.MultiUser || s.multi == nil || s.multi.oidc == nil {
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
	if s.startUserSession(w, r, user) {
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// startUserSession replaces the caller's session with a new one for user.
func (s *Server) startUserSession(w http.ResponseWriter, r *http.Request, user string) bool {
	if previous, e := r.Cookie(auth.CookieName); e == nil {
		if e := s.Store.RevokeUserSession(r.Context(), previous.Value); e != nil {
			writeErr(w, 500, "session", "sign-in failed")
			return false
		}
	}
	token, csrf, err := s.Store.IssueUserSession(r.Context(), user, 14*24*time.Hour)
	if err != nil {
		writeErr(w, 500, "session", "sign-in failed")
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 14 * 86400})
	http.SetCookie(w, &http.Cookie{Name: "musik_csrf", Value: csrf, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 14 * 86400})
	return true
}

type localCredentials struct {
	Invitation string `json:"invitation"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

// readCredentials decodes and checks a username (1–64 characters, compared
// case-insensitively) and a password of 8–256 characters.
func readCredentials(w http.ResponseWriter, r *http.Request) (localCredentials, bool) {
	var c localCredentials
	if decodeSmall(w, r, &c) != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return c, false
	}
	c.Username = strings.ToLower(strings.TrimSpace(c.Username))
	if n := utf8.RuneCountInString(c.Username); n < 1 || n > 64 || len(c.Password) < 8 || len(c.Password) > 256 {
		writeErr(w, 400, "credentials", "имя от 1 до 64 символов, пароль от 8 до 256")
		return c, false
	}
	return c, true
}

func (s *Server) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if s.loginLimiter != nil && !s.loginLimiter.Allow(r) {
		writeErr(w, 429, "rate_limited", "too many login attempts")
		return
	}
	c, ok := readCredentials(w, r)
	if !ok {
		return
	}
	user, hash, err := s.Store.LocalPassword(r.Context(), c.Username, "")
	if err != nil || !auth.PasswordMatches(hash, c.Password) {
		writeErr(w, 401, "bad_password", "неверное имя или пароль")
		return
	}
	if s.startUserSession(w, r, user) {
		writeJSON(w, map[string]any{"ok": true})
	}
}

// handleLocalRegister accepts an invitation with a new local account.
func (s *Server) handleLocalRegister(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.MultiUser {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if s.loginLimiter != nil && !s.loginLimiter.Allow(r) {
		writeErr(w, 429, "rate_limited", "too many attempts")
		return
	}
	c, ok := readCredentials(w, r)
	if !ok {
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		writeErr(w, 500, "password", "password could not be stored")
		return
	}
	user, _, err := s.Store.AcceptInvitation(r.Context(), c.Invitation, db.VerifiedIdentity{Issuer: db.LocalIssuer, Subject: c.Username, DisplayName: c.Username, PasswordHash: hash})
	if err != nil {
		writeErr(w, 403, "invitation_invalid", "приглашение недействительно или имя занято")
		return
	}
	if s.startUserSession(w, r, user) {
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if p == nil || p.DeviceToken || decodeSmall(w, r, &req) != nil || len(req.New) < 8 || len(req.New) > 256 {
		writeErr(w, 400, "password", "новый пароль от 8 до 256 символов")
		return
	}
	if _, hash, err := s.Store.LocalPassword(r.Context(), "", p.UserID); err != nil || !auth.PasswordMatches(hash, req.Current) {
		writeErr(w, 403, "bad_password", "текущий пароль неверный")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil || s.Store.SetLocalPassword(r.Context(), p.UserID, hash) != nil {
		writeErr(w, 500, "password", "password could not be stored")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
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
		writeJSON(w, map[string]any{"ok": false, "authenticated": false, "auth_enabled": true, "multi_user": true, "oidc": s.multi != nil && s.multi.oidc != nil, "oidc_login_url": "/api/auth/oidc/default/start"})
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
	_, _, noLocal := s.Store.LocalPassword(r.Context(), "", p.UserID)
	writeJSON(w, map[string]any{"ok": true, "authenticated": true, "auth_enabled": true, "multi_user": true, "oidc": s.multi != nil && s.multi.oidc != nil, "local_account": noLocal == nil, "user": map[string]any{"id": p.UserID, "display_name": name, "roles": roles}, "active_profile": p.Profile, "profiles": profiles})
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
	s.dropProfile(r.PathValue("profile_id"))
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
	// Any sign-in method may accept the invitation: a local account or OIDC.
	id, secret, err := s.Store.CreateInvitation(r.Context(), p.UserID, "", req.Email, time.Now().Add(time.Duration(req.TTLHours)*time.Hour))
	if err != nil {
		writeErr(w, 500, "invitation", "invitation creation failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"id": id, "secret": secret, "url": strings.TrimRight(s.Cfg.PublicBaseURL, "/") + "/?invitation=" + url.QueryEscape(secret)})
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

// Additional deployment names are explicitly trusted for authentication.
// They do not grant CORS access or remove the CSRF check for cookie sessions.
func (s *Server) allowedBrowserOrigin(origin string) bool {
	if origin == strings.TrimRight(s.Cfg.PublicBaseURL, "/") {
		return true
	}
	for _, candidate := range s.Cfg.AuthAllowedOrigins {
		if candidate != origin {
			continue
		}
		u, err := url.Parse(candidate)
		if err == nil && u.Scheme == "https" && u.Hostname() != "" &&
			u.User == nil && u.Path == "" && u.RawPath == "" &&
			!strings.ContainsAny(candidate, "?#*") {
			return true
		}
	}
	return false
}
