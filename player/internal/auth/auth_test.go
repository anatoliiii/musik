package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGatePasswordBearerAndCookie(t *testing.T) {
	gate := New(Config{Password: "pw", APIToken: "token-xyz", TTL: time.Hour})
	if gate.Authorized(httptest.NewRequest("GET", "/api/x", nil)) {
		t.Fatal("unauthenticated should fail")
	}
	if !gate.CheckPassword("pw") || gate.CheckPassword("no") {
		t.Fatal("password check")
	}

	rec := httptest.NewRecorder()
	if err := gate.IssueCookie(rec); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%v", cookies)
	}
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.AddCookie(cookies[0])
	if !gate.Authorized(req) {
		t.Fatal("cookie should authorize")
	}

	bearer := httptest.NewRequest("GET", "/api/x", nil)
	bearer.Header.Set("Authorization", "Bearer token-xyz")
	if !gate.Authorized(bearer) {
		t.Fatal("bearer should authorize")
	}

	gate.ClearCookie(rec)
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		// IssueCookie + ClearCookie both set cookies; ensure clear path works on fresh recorder.
		rec2 := httptest.NewRecorder()
		gate.ClearCookie(rec2)
		for _, c := range rec2.Result().Cookies() {
			if c.Name == CookieName && c.MaxAge < 0 {
				cleared = true
			}
		}
	}
	if !cleared {
		t.Fatal("expected cleared cookie")
	}
}

func TestMiddlewareBlocksAPIWithoutAuth(t *testing.T) {
	gate := New(Config{Password: "pw", APIToken: "tok"})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	})
	h := gate.Middleware(next, func(r *http.Request) bool { return r.URL.Path == "/api/health" })

	req := httptest.NewRequest("GET", "/api/library", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("status=%d, want 401", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("public health status=%d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/library", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("authorized status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginLimiterPerIP(t *testing.T) {
	lim := NewLoginLimiter(2, time.Minute)
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "1.2.3.4:9"
	if !lim.Allow(req) || !lim.Allow(req) {
		t.Fatal("first two should allow")
	}
	if lim.Allow(req) {
		t.Fatal("third should block")
	}
	other := httptest.NewRequest("POST", "/api/auth/login", nil)
	other.RemoteAddr = "5.6.7.8:9"
	if !lim.Allow(other) {
		t.Fatal("other IP should allow")
	}
}

func TestEnabledRequiresSecrets(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if (Config{Password: "x", Disabled: true}).Enabled() {
		t.Fatal("explicit disabled")
	}
	if !(Config{APIToken: "t"}).Enabled() {
		t.Fatal("token enables auth")
	}
}
