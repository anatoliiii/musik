package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestMultiUserHTTPIsolationAndCSRF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{DBPath: path, MultiUser: true, PublicBaseURL: "https://musik.test", QueueSize: 6}
	server := New(cfg, store, index.New(cfg), taste.New(), nil)
	server.multi = &multiUserState{profiles: make(map[string]*profileState)}
	ctx := context.Background()
	userA, profileA, err := store.CreateUserWithDefaultProfile(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	userB, profileB, err := store.CreateUserWithDefaultProfile(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, userA); err != nil {
		t.Fatal(err)
	}
	tokenA, csrfA, err := store.IssueUserSession(ctx, userA, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, csrfB, err := store.IssueUserSession(ctx, userB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO tracks(id,path,title, created_at, updated_at) VALUES (1,'/a','A', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	call := func(method, path, body, token, csrf, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://musik.test"+path, strings.NewReader(body))
		if token != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check := func(out *httptest.ResponseRecorder, want int) {
		t.Helper()
		if out.Code != want {
			t.Fatalf("status=%d want=%d body=%s", out.Code, want, out.Body.String())
		}
	}
	check(call("GET", "/api/favorites", "", "", "", ""), 401)
	check(call("POST", "/api/favorites", `{"track_id":1}`, tokenA, "", ""), 403)
	check(call("POST", "/api/favorites", `{"track_id":1}`, tokenA, csrfA, "https://evil.test"), 403)
	check(call("POST", "/api/favorites", `{"track_id":1}`, tokenA, csrfB, "https://musik.test"), 403)
	check(call("POST", "/api/favorites", `{"track_id":1,"profile_id":"`+profileB.ID+`"}`, tokenA, csrfA, "https://musik.test"), 200)
	check(call("POST", "/api/auth/oidc/default/link", "{}", tokenA, "", "https://musik.test"), 403)
	check(call("POST", "/api/auth/oidc/default/link", "{}", tokenA, csrfA, "https://musik.test"), 503)
	check(call("GET", "/api/admin/users", "", tokenA, "", ""), 200)
	check(call("GET", "/api/admin/users", "", tokenB, "", ""), 403)
	// Behind a same-host reverse proxy every request arrives from loopback;
	// server settings (env file, directory browser, restart) stay admin-only.
	viaProxy := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://musik.test"+path, nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check(viaProxy("/api/settings", tokenB), 403)
	check(viaProxy("/api/settings/dirs", tokenB), 403)
	check(viaProxy("/api/settings", tokenA), 200)
	createdInvite := call("POST", "/api/admin/invitations", `{"ttl_hours":1}`, tokenA, csrfA, "https://musik.test")
	check(createdInvite, 200)
	var invitation struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(createdInvite.Body.Bytes(), &invitation); err != nil || invitation.Secret == "" {
		t.Fatalf("invitation response=%s err=%v", createdInvite.Body.String(), err)
	}
	listedInvites := call("GET", "/api/admin/invitations", "", tokenA, "", "")
	check(listedInvites, 200)
	if strings.Contains(listedInvites.Body.String(), invitation.Secret) {
		t.Fatal("invitation metadata exposed its one-time secret")
	}
	check(call("DELETE", "/api/admin/invitations/"+invitation.ID, "", tokenA, csrfA, "https://musik.test"), 200)
	if store.ForProfile(profileB.ID).FavoritesHas(1) {
		t.Fatal("request body chose a foreign profile")
	}
	if !store.ForProfile(profileA.ID).FavoritesHas(1) {
		t.Fatal("A favorite missing")
	}
	check(call("POST", "/api/profiles/"+profileB.ID+"/activate", "{}", tokenA, csrfA, ""), 404)
	check(call("POST", "/api/admin/invitations", "{}", tokenB, csrfB, ""), 403)
	check(call("DELETE", "/api/profiles/"+profileA.ID, "", tokenA, csrfA, ""), 409)
	created := call("POST", "/api/profiles", `{"name":"Quiet"}`, tokenA, csrfA, "")
	check(created, 200)
	var second db.Profile
	if err := json.Unmarshal(created.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	started := call("POST", "/api/session/start", "{}", tokenA, csrfA, "")
	check(started, 200)
	var play struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &play); err != nil {
		t.Fatal(err)
	}
	check(call("GET", "/api/now?session_id="+play.SessionID, "", tokenB, "", ""), 404)
	check(call("POST", "/api/profiles/"+second.ID+"/activate", "{}", tokenA, csrfA, ""), 200)
	check(call("GET", "/api/now?session_id="+play.SessionID, "", tokenA, "", ""), 404)
	activeUser, active, err := store.UserSession(ctx, tokenA)
	if err != nil || activeUser != userA || active.ID != second.ID {
		t.Fatalf("active profile %#v %v", active, err)
	}
	check(call("DELETE", "/api/profiles/"+second.ID, "", tokenA, csrfA, ""), 200)
	_, active, err = store.UserSession(ctx, tokenA)
	if err != nil || active.ID != profileA.ID {
		t.Fatal("deleted profile left a dangling session")
	}
	check(call("PATCH", "/api/admin/users/"+userA, `{"admin":false}`, tokenA, csrfA, "https://musik.test"), 409)
	// An alias is rejected by default, then accepted only when explicitly configured.
	check(call("PATCH", "/api/profiles/"+profileA.ID, `{"name":"Home"}`, tokenA, csrfA, "https://home.test"), 403)
	server.Cfg.AuthAllowedOrigins = []string{"https://home.test"}
	check(call("PATCH", "/api/profiles/"+profileA.ID, `{"name":"Home"}`, tokenA, csrfA, "https://home.test"), 200)
	check(call("PATCH", "/api/profiles/"+profileA.ID, `{"name":"No CSRF"}`, tokenA, "", "https://home.test"), 403)
	check(call("PATCH", "/api/profiles/"+profileA.ID, `{"name":"Wrong CSRF"}`, tokenA, csrfB, "https://home.test"), 403)
	check(call("PATCH", "/api/profiles/"+profileA.ID, `{"name":"Foreign"}`, tokenA, csrfA, "https://evil.test"), 403)

	check(call("POST", "/api/auth/logout", "{}", tokenA, csrfA, ""), 200)
	check(call("GET", "/api/favorites", "", tokenA, "", ""), 401)
}

func TestLocalAccountsRegisterSignInAndChangePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{DBPath: path, MultiUser: true, PublicBaseURL: "https://musik.test", QueueSize: 6}
	server := New(cfg, store, index.New(cfg), taste.New(), nil)
	ctx := context.Background()
	secret, err := store.BootstrapInvitation(ctx, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableMultiUser(ctx); err != nil { // no OIDC issuer: local accounts only
		t.Fatal(err)
	}
	server.loginLimiter = auth.NewLoginLimiter(100, time.Minute)
	handler := server.Handler()
	call := func(path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://musik.test"+path, strings.NewReader(body))
		req.Header.Set("Origin", "https://musik.test")
		for _, c := range cookies {
			req.AddCookie(c)
			if c.Name == "musik_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check := func(out *httptest.ResponseRecorder, want int) []*http.Cookie {
		t.Helper()
		if out.Code != want {
			t.Fatalf("status=%d want=%d body=%s", out.Code, want, out.Body.String())
		}
		return out.Result().Cookies()
	}
	register := `{"invitation":"` + secret + `","username":" Owner ","password":"first-secret"}`
	check(call("/api/auth/register", `{"invitation":"wrong","username":"owner","password":"first-secret"}`), 403)
	check(call("/api/auth/register", `{"invitation":"`+secret+`","username":"owner","password":"short"}`), 400)
	session := check(call("/api/auth/register", register), 200)
	check(call("/api/auth/register", register), 403) // invitation consumed
	if admin, err := store.UserIsAdmin(ctx, func() string { u, _, _ := store.LocalPassword(ctx, "owner", ""); return u }()); err != nil || !admin {
		t.Fatalf("bootstrap owner is not admin: %v %v", admin, err)
	}
	check(call("/api/auth/login", `{"username":"OWNER","password":"wrong-secret"}`), 401)
	check(call("/api/auth/login", `{"username":"owner","password":"first-secret"}`), 200)
	check(call("/api/account/password", `{"current":"wrong-secret","new":"second-secret"}`, session...), 403)
	check(call("/api/account/password", `{"current":"first-secret","new":"second-secret"}`, session...), 200)
	check(call("/api/auth/login", `{"username":"owner","password":"first-secret"}`), 401)
	check(call("/api/auth/login", `{"username":"owner","password":"second-secret"}`), 200)

	// A phone app signs in without cookies and uses the device token as Bearer.
	check(call("/api/auth/token", `{"username":"owner","password":"first-secret","device_name":"Phone"}`), 401)
	check(call("/api/auth/token", `{"username":"owner","password":"second-secret","profile_id":"foreign"}`), 404)
	out := call("/api/auth/token", `{"username":"Owner","password":"second-secret","device_name":"Phone"}`)
	check(out, 200)
	var phone struct {
		Token    string       `json:"token"`
		Profiles []db.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &phone); err != nil || phone.Token == "" || len(phone.Profiles) != 1 {
		t.Fatalf("token response=%s err=%v", out.Body.String(), err)
	}
	req := httptest.NewRequest("GET", "https://musik.test/api/favorites", nil)
	req.Header.Set("Authorization", "Bearer "+phone.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	check(rec, 200)
}

func TestSingleUserPasswordReturnsAPITokenToPhones(t *testing.T) {
	databaseURL, path := testdb.Target(t, filepath.Join(t.TempDir(), "test.db"))
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{DBPath: path, Password: "owner-secret", APIToken: "api-token-value", SessionSecret: "s", QueueSize: 6}
	server := New(cfg, store, index.New(cfg), taste.New(), nil)
	handler := server.Handler()
	call := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/token", strings.NewReader(body)))
		return rec
	}
	if rec := call(`{"password":"wrong"}`); rec.Code != 401 {
		t.Fatalf("wrong password status=%d", rec.Code)
	}
	if rec := call(`{"password":"owner-secret"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"api-token-value"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMultiUserRootSkipsPlaybackStack(t *testing.T) {
	cfg := config.Config{MultiUser: true, PublicBaseURL: "https://musik.test"}
	server := New(cfg, &db.Store{}, index.New(cfg), taste.New(), nil)
	if server.Play != nil || server.Idx != nil || server.Taste != nil {
		t.Fatal("multi-user root started the installation playback stack")
	}
	if server.Media == nil || server.Auth == nil || server.App == nil || server.App.Catalog == nil {
		t.Fatal("routes server is missing auth, media, or the shared catalog")
	}
}

func TestInactiveProfilesAreEvicted(t *testing.T) {
	prevIdle, prevMax := profileIdleAfter, profileCacheMax
	profileIdleAfter = time.Minute
	profileCacheMax = 2
	t.Cleanup(func() {
		profileIdleAfter = prevIdle
		profileCacheMax = prevMax
	})
	server := &Server{multi: &multiUserState{profiles: map[string]*profileState{}}}
	idle := &profileState{}
	idle.used.Store(time.Now().Add(-time.Hour).UnixNano())
	busy := &profileState{}
	busy.used.Store(time.Now().Add(-time.Hour).UnixNano())
	busy.refs.Store(1)
	fresh := &profileState{}
	fresh.used.Store(time.Now().UnixNano())
	server.multi.profiles["idle"] = idle
	server.multi.profiles["busy"] = busy
	server.multi.profiles["fresh"] = fresh
	server.sweepProfiles()
	if _, ok := server.multi.profiles["idle"]; ok {
		t.Fatal("idle profile stayed cached")
	}
	if server.multi.profiles["busy"] != busy || server.multi.profiles["fresh"] != fresh {
		t.Fatal("in-use or fresh profile was evicted")
	}

	older := &profileState{}
	older.used.Store(time.Now().Add(-time.Second).UnixNano())
	newest := &profileState{}
	newest.used.Store(time.Now().UnixNano())
	server.multi.profiles = map[string]*profileState{"older": older, "fresh": fresh, "newest": newest}
	server.sweepProfiles()
	if _, ok := server.multi.profiles["older"]; ok || len(server.multi.profiles) != 2 {
		t.Fatalf("cache = %#v", server.multi.profiles)
	}
}

func TestExplicitBrowserOriginAliases(t *testing.T) {
	aliases := []string{"https://home.test", "https://vpn.test:8443", "http://insecure.test", "https://bad.test/path", "https://user@bad.test", "https://query.test?", "https://fragment.test#", "https://*.wildcard.test", "https://:8443"}
	server := &Server{Cfg: config.Config{PublicBaseURL: "https://public.test/", AuthAllowedOrigins: aliases}}
	for _, origin := range []string{"https://public.test", "https://home.test", "https://vpn.test:8443"} {
		if !server.allowedBrowserOrigin(origin) {
			t.Errorf("trusted origin rejected: %s", origin)
		}
	}
	for _, origin := range []string{"https://evil.test", "null", "https://home.test.evil", "http://home.test", "https://home.test/", "https://home.test/path", "https://vpn.test", "https://vpn.test:9443", "http://insecure.test", "https://bad.test/path", "https://user@bad.test", "https://query.test?", "https://fragment.test#", "https://*.wildcard.test", "https://:8443"} {
		if server.allowedBrowserOrigin(origin) {
			t.Errorf("untrusted or invalid origin admitted: %s", origin)
		}
	}
}

func TestAuthenticationOriginAliases(t *testing.T) {
	server := &Server{Cfg: config.Config{MultiUser: true, PublicBaseURL: "https://public.test", AuthAllowedOrigins: []string{"https://home.test"}}}
	// Test the guard without invoking credential handlers or requiring a database.
	handler := server.multiUserMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/token"} {
		for _, tc := range []struct {
			origin string
			want   int
		}{
			{"https://public.test", 204}, {"https://home.test", 204}, {"", 204},
			{"https://evil.test", 403}, {"null", 403}, {"http://home.test", 403},
		} {
			t.Run(path+"/"+tc.origin, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "https://home.test"+path, strings.NewReader("{}"))
				req.Header.Set("Origin", tc.origin)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("status=%d want=%d", rec.Code, tc.want)
				}
			})
		}
	}
}
