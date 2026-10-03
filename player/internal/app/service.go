// Package app coordinates application lifecycle concerns that span domain packages.
package app

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/taste"
)

var reloadKinds = map[string]bool{
	"embed": true, "full_rescan": true, "clusters": true,
	"daily": true, "album_tips": true, "mix_pack": true, "train_ranker": true,
}

type Service struct {
	Cfg   config.Config
	Store *db.Store
	Idx   *index.Index
	Taste *taste.Profile
	Play  *playback.Engine
	HTTP  *http.Client
}

func New(cfg config.Config, store *db.Store, idx *index.Index, tp *taste.Profile, play *playback.Engine, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{Cfg: cfg, Store: store, Idx: idx, Taste: tp, Play: play, HTTP: client}
}

func (s *Service) Reload() error {
	rows, err := s.Store.LoadReadyTracks()
	if err != nil {
		return err
	}
	if err := s.Idx.Load(rows); err != nil {
		return err
	}
	pos, neg, _ := s.Store.ListenSignalCounts()
	if blob, err := s.Store.LatestProfile("global"); err == nil && len(blob) > 0 {
		v := index.BytesToFloat32(blob)
		if len(v) == s.Idx.Dim() {
			s.Taste.SetWithMeta(v, pos, neg, "online_ema")
		}
	} else if !s.Taste.Ready() {
		s.Taste.SetWithMeta(s.Idx.Centroid(), pos, neg, "centroid")
	} else {
		s.Taste.SetCounts(pos, neg)
	}
	log.Printf("reloaded index n=%d dim=%d maturity=%s", s.Idx.Size(), s.Idx.Dim(),
		s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt))
	s.Play.ReloadTransitions()
	s.Play.ReloadRanker()
	entityRows := s.Idx.EntityVectorRows(db.EntityModelVersion)
	go func() {
		if err := s.Store.ReplaceEntityVectors(db.EntityModelVersion, entityRows); err != nil {
			log.Printf("entity vectors refresh: %v", err)
		}
	}()
	return nil
}

func (s *Service) WorkerHealthy() bool {
	res, err := s.HTTP.Get(strings.TrimRight(s.Cfg.WorkerURL, "/") + "/jobs")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode < 500
}

func (s *Service) EnsureWorker() {
	if !s.Cfg.WorkerAutostart {
		return
	}
	if s.WorkerHealthy() {
		log.Printf("worker already up at %s", s.Cfg.WorkerURL)
		return
	}
	dataDir := filepath.Dir(filepath.Dir(s.Cfg.DBPath))
	projectRoot := filepath.Dir(dataDir)
	if root := os.Getenv("MUSIK_ROOT"); root != "" {
		projectRoot = root
	}
	python := findPython()
	if python == "" {
		venvPython := filepath.Join(projectRoot, ".venv", "bin", "python")
		if _, err := os.Stat(venvPython); err == nil {
			python = venvPython
		}
	}
	if python == "" {
		log.Printf("worker autostart: no python found; start `musik worker` manually")
		return
	}
	musikBin := filepath.Join(filepath.Dir(python), "musik")
	var cmd *exec.Cmd
	if _, err := os.Stat(musikBin); err == nil {
		cmd = exec.Command(musikBin, "worker")
	} else {
		cmd = exec.Command(python, "-m", "musik", "worker")
	}
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(),
		"MUSIK_ROOT="+projectRoot,
		"MUSIK_DB_PATH="+s.Cfg.DBPath,
		"MUSIK_MULTI_USER="+strconv.FormatBool(s.Cfg.MultiUser),
		"MUSIK_LIBRARY="+s.Cfg.Library,
		"MUSIK_PLAYER_RELOAD_URL=http://127.0.0.1"+normalizeAddr(s.Cfg.Addr)+"/api/reload",
	)
	if s.Cfg.APIToken != "" {
		cmd.Env = append(cmd.Env, "MUSIK_API_TOKEN="+s.Cfg.APIToken)
	}
	logPath := filepath.Join(dataDir, "worker.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer file.Close()
		cmd.Stdout = file
		cmd.Stderr = file
	}
	if err := cmd.Start(); err != nil {
		log.Printf("worker autostart failed: %v", err)
		return
	}
	log.Printf("worker autostarted pid=%d log=%s", cmd.Process.Pid, logPath)
	go func() { _ = cmd.Wait() }()
	for range 20 {
		time.Sleep(250 * time.Millisecond)
		if s.WorkerHealthy() {
			log.Printf("worker ready at %s", s.Cfg.WorkerURL)
			return
		}
	}
	log.Printf("worker autostart: still not healthy at %s (check %s)", s.Cfg.WorkerURL, logPath)
}

func (s *Service) WatchJobs() {
	after := time.Now().UTC().Format(time.RFC3339Nano)
	ticker := time.NewTicker(3 * time.Second)
	go func() {
		for range ticker.C {
			jobs, err := s.Store.ListDoneJobsAfter(after, 50)
			if err != nil || len(jobs) == 0 {
				continue
			}
			needReload := false
			for _, job := range jobs {
				after = job.UpdatedAt
				if reloadKinds[job.Kind] {
					needReload = true
					log.Printf("job #%d kind=%s done → reload index", job.ID, job.Kind)
				}
			}
			if needReload {
				if err := s.Reload(); err != nil {
					log.Printf("auto-reload after job: %v", err)
				}
			}
		}
	}()
}

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return ":" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}

func findPython() string {
	candidates := []string{os.Getenv("MUSIK_PYTHON")}
	if root := os.Getenv("MUSIK_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, ".venv", "bin", "python"))
	}
	cwd, _ := os.Getwd()
	candidates = append(candidates,
		filepath.Join(cwd, ".venv", "bin", "python"),
		filepath.Join(cwd, "..", ".venv", "bin", "python"),
		".venv/bin/python", "python3", "python",
	)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
		if _, err := os.Stat(candidate); err == nil {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute
			}
			return candidate
		}
	}
	return ""
}
