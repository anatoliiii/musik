package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/static"
	"github.com/torwin-job/musik/player/internal/taste"
)

func main() {
	cfg := config.Load()
	resolvedDatabase, err := config.ResolveDatabase(cfg.DatabaseURL, os.Getenv("MUSIK_DB_PATH"), cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	if strings.HasPrefix(resolvedDatabase, "postgresql://") {
		cfg.DatabaseURL = resolvedDatabase
	} else {
		cfg.DBPath = resolvedDatabase
		cfg.DatabaseURL = ""
		if os.Getenv("MUSIK_DATA_DIR") == "" {
			cfg.DataDir = filepath.Dir(filepath.Dir(resolvedDatabase))
		}
	}
	if os.Getenv("MUSIK_THEMES") == "" {
		cfg.ThemesDir = filepath.Join(cfg.DataRoot(), "themes")
	}
	if !cfg.AuthEnabled() && !cfg.AuthDisabled {
		log.Fatal("auth required: set MUSIK_PASSWORD and/or MUSIK_API_TOKEN (or MUSIK_AUTH_DISABLED=1 for local open mode)")
	}
	dbBackend := "sqlite"
	if cfg.DatabaseURL != "" {
		dbBackend = "postgresql"
	}
	log.Printf("musik-player db_backend=%s addr=%s themes=%s", dbBackend, cfg.Addr, cfg.ThemesDir)
	if err := os.MkdirAll(cfg.ThemesDir, 0o755); err != nil {
		log.Printf("themes dir: %v", err)
	}

	store, err := db.OpenDatabase(cfg.DatabaseURL, cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	idx := index.New(cfg)
	tp := taste.New()

	staticFS, err := fs.Sub(static.FS, ".")
	if err != nil {
		log.Fatalf("static: %v", err)
	}
	var webFS http.FileSystem = http.FS(staticFS)
	// Development: serve the Web UI from disk so edits show up without a rebuild.
	if dir := os.Getenv("MUSIK_STATIC_DIR"); dir != "" {
		log.Printf("serving web UI from %s", dir)
		webFS = http.Dir(dir)
	}

	srv := api.New(cfg, store, idx, tp, webFS)
	if err := srv.Reload(); err != nil {
		log.Fatalf("reload: %v", err)
	}

	srv.EnsureWorker()
	srv.WatchJobs()

	if cfg.AuthEnabled() {
		log.Printf("auth enabled (password=%v token=%v)", cfg.Password != "", cfg.APIToken != "")
	} else {
		log.Printf("auth explicitly disabled (MUSIK_AUTH_DISABLED=1)")
	}
	addr := cfg.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "0.0.0.0" + addr
	}
	log.Printf("listening on http://%s  tracks=%d  sessions=multi", addr, idx.Size())
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
