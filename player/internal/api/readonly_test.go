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
