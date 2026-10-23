package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestReadOnlyReplicaRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.ReadOnly = true
	cfg := config.Config{DBPath: path, QueueSize: 6, AuthDisabled: true}
	handler := New(cfg, store, index.New(cfg), taste.New(), nil).Handler()

	write := httptest.NewRecorder()
	handler.ServeHTTP(write, httptest.NewRequest(http.MethodPost, "/api/favorites", strings.NewReader(`{"track_id":1}`)))
	if write.Code != http.StatusServiceUnavailable || !strings.Contains(write.Body.String(), "read_only_replica") {
		t.Fatalf("write on replica: %d %s", write.Code, write.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("read on replica: %d %s", read.Code, read.Body.String())
	}
}

func TestReadOnlyReplicaAllowsOnlyLoopbackReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly-reload.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ReadOnly = true
	cfg := config.Config{DBPath: path, QueueSize: 6, AuthDisabled: true}
	handler := New(cfg, store, index.New(cfg), taste.New(), nil).Handler()
	for _, tc := range []struct {
		name, remote, path string
		want               int
	}{
		{"local IPv4 reload", "127.0.0.1:1234", "/api/reload", http.StatusOK},
		{"local IPv6 reload", "[::1]:1234", "/api/reload", http.StatusOK},
		{"remote reload", "192.0.2.1:1234", "/api/reload", http.StatusServiceUnavailable},
		{"local write", "127.0.0.1:1234", "/api/favorites", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.RemoteAddr = tc.remote
			// A forwarded address cannot turn an external request into loopback.
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			reply := httptest.NewRecorder()
			handler.ServeHTTP(reply, req)
			if reply.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", reply.Code, reply.Body.String(), tc.want)
			}
		})
	}
}
