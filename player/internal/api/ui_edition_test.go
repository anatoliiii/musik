package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticEditionCookieSelectsPage(t *testing.T) {
	server := &Server{Static: http.FS(fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("original interface")},
		"mozgolika.html": &fstest.MapFile{Data: []byte("mozgolika interface")},
	})}
	for _, tc := range []struct{ cookie, want string }{
		{"", "original interface"},
		{"mozgolika", "mozgolika interface"},
		{"original", "original interface"},
		{"../../etc/passwd", "original interface"},
	} {
		t.Run(tc.cookie, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			if tc.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "musik_ui", Value: tc.cookie})
			}
			response := httptest.NewRecorder()
			server.staticHandler().ServeHTTP(response, request)
			if response.Code != 200 || response.Body.String() != tc.want {
				t.Fatalf("status=%d body=%q want=%q", response.Code, response.Body.String(), tc.want)
			}
			if !strings.Contains(response.Header().Get("Vary"), "Cookie") {
				t.Fatal("edition responses must vary on Cookie")
			}
			if response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("edition must revalidate")
			}
		})
	}
}

func TestStaticEditionEnvironmentDefault(t *testing.T) {
	server := &Server{Static: http.FS(fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("original")},
		"mozgolika.html": &fstest.MapFile{Data: []byte("mozgolika")},
	})}
	server.Cfg.UI = "mozgolika"
	response := httptest.NewRecorder()
	server.staticHandler().ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Body.String() != "mozgolika" {
		t.Fatalf("default body=%q", response.Body.String())
	}
}
