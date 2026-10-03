package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/static"
	"github.com/torwin-job/musik/player/internal/taste"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		runAdmin(os.Args[2:])
		return
	}
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
	if !cfg.AuthEnabled() && !cfg.AuthDisabled && !cfg.MultiUser {
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
	if err := srv.EnableMultiUser(context.Background()); err != nil {
		log.Fatalf("multi-user configuration: %v", err)
	}
	if err := srv.Reload(); err != nil {
		log.Fatalf("reload: %v", err)
	}

	srv.EnsureWorker()
	srv.WatchJobs()

	if cfg.MultiUser {
		log.Printf("multi-user OIDC authentication enabled")
	} else if cfg.AuthEnabled() {
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

func runAdmin(args []string) {
	if len(args) == 0 || args[0] != "bootstrap" {
		log.Fatal("usage: musik-player admin bootstrap [--db SQLITE_PATH] --issuer HTTPS_URL (or set MUSIK_DATABASE_URL)")
	}
	flags := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	path := flags.String("db", "", "migrated SQLite database path; defaults to MUSIK_DATABASE_URL or MUSIK_DB_PATH")
	issuer := flags.String("issuer", os.Getenv("MUSIK_OIDC_ISSUER"), "OIDC issuer")
	ttl := flags.Duration("ttl", time.Hour, "invitation lifetime (maximum 24h)")
	flags.Parse(args[1:])
	parsedIssuer, issuerErr := url.Parse(*issuer)
	if issuerErr != nil || parsedIssuer.Scheme != "https" || parsedIssuer.Host == "" || parsedIssuer.User != nil || parsedIssuer.RawQuery != "" || parsedIssuer.Fragment != "" || *ttl <= 0 || *ttl > 24*time.Hour {
		log.Fatal("bootstrap requires an HTTPS --issuer and a lifetime up to 24h")
	}
	cfg := config.Load()
	databaseURL := cfg.DatabaseURL
	databasePath := cfg.DBPath
	if *path != "" {
		if databaseURL != "" {
			log.Fatal("use either --db or MUSIK_DATABASE_URL, not both")
		}
		databasePath = *path
	} else {
		resolved, err := config.ResolveDatabase(databaseURL, os.Getenv("MUSIK_DB_PATH"), databasePath)
		if err != nil {
			log.Fatal(err)
		}
		if strings.HasPrefix(resolved, "postgresql://") {
			databaseURL = resolved
		} else {
			databaseURL = ""
			databasePath = resolved
		}
	}
	store, err := db.OpenDatabase(databaseURL, databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	secret, err := store.BootstrapInvitation(context.Background(), *issuer, *ttl)
	if err != nil {
		log.Fatalf("bootstrap invitation: %v", err)
	}
	fmt.Println(secret)
}
