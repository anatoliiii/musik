package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func withCORS(next http.Handler, allowed []string) http.Handler {
	allowSet := map[string]bool{}
	for _, o := range allowed {
		allowSet[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			ok := false
			if len(allowSet) == 0 {
				// same-origin only: reflect if Origin host matches request Host
				if u, err := parseOriginHost(origin); err == nil && u == r.Host {
					ok = true
				}
			} else if allowSet[origin] || allowSet["*"] {
				ok = true
			}
			if ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseOriginHost(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", errors.New("bad origin")
	}
	return u.Host, nil
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "code": errCode})
}

func playHTTPStatus(err error) int {
	if err == nil {
		return 0
	}
	switch err.Error() {
	case "нет доступных треков", "ничего не найдено", "track not found", "track not in playlist":
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) staticHandler() http.Handler {
	if s.Static == nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setStaticCacheControl(w, r.URL.Path)
		if r.URL.Path == "/" || r.URL.Path == "" {
			f, err := s.Static.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			stat, err := f.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			rs, ok := f.(io.ReadSeeker)
			if !ok {
				r.URL.Path = "/index.html"
				fileServer.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", modTime(stat), rs)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func setStaticCacheControl(w http.ResponseWriter, path string) {
	if cc := staticCacheControl(path); cc != "" {
		w.Header().Set("Cache-Control", cc)
	}
}

func staticCacheControl(path string) string {
	if strings.HasPrefix(path, "/fonts/") && strings.HasSuffix(strings.ToLower(path), ".woff2") {
		return "public, max-age=31536000, immutable"
	}
	switch path {
	case "/", "", "/index.html":
		// The page is tiny and names its assets with ?v=<content hash>, so always revalidate
		// it: a new deploy then reaches browsers at once instead of after an hour.
		return "no-cache"
	case "/app.js", "/style.css", "/redesign.css", "/fonts.css":
		return "public, max-age=3600"
	default:
		return ""
	}
}

func modTime(info fs.FileInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"ok": true, "version": Version, "api_version": APIVersion,
		"auth": s.Auth != nil && s.Auth.Cfg.Enabled(),
	}
	if s.Auth == nil || !s.Auth.Cfg.Enabled() || s.Auth.Authorized(r) {
		out["tracks"] = s.Idx.Size()
		out["dim"] = s.Idx.Dim()
	}
	writeJSON(w, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	mat := s.Play.Maturity()
	explore := s.Play.Explore()
	sessionCount := s.Play.SessionCount()
	out := map[string]any{
		"tracks": s.Idx.Size(), "dim": s.Idx.Dim(),
		"taste_ready":      s.Taste.Ready(),
		"maturity":         mat,
		"sessions":         sessionCount,
		"explore":          explore,
		"explore_lo":       s.exploreState()["explore_lo"],
		"explore_hi":       s.exploreState()["explore_hi"],
		"model_version":    s.Play.Ranker.ModelVersion,
		"db":               s.Cfg.DBPath,
		"worker_url":       s.Cfg.WorkerURL,
		"worker_autostart": s.Cfg.WorkerAutostart,
	}
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		if sess := s.Play.Get(sid); sess != nil {
			sess.Lock()
			out["session_id"] = sess.ID
			out["mode"] = sess.Mode
			out["current"] = sess.Current
			out["queue_len"] = len(sess.Queue)
			sess.Unlock()
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	_, _ = w.Write([]byte(`{
  "name": "musik",
  "short_name": "musik",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#141210",
  "theme_color": "#c45c26",
  "description": "Local smart music player"
}`))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func contentType(path string) string {
	switch filepath.Ext(path) {
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}

func queryLimit(r *http.Request, fallback, max int) int {
	if r == nil {
		return fallback
	}
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if max > 0 && n > max {
		return max
	}
	return n
}
