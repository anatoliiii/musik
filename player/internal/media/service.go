// Package media serves and prepares stream and artwork media.
package media

import (
	"path/filepath"
	"strings"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/index"
)

type trackFiles struct {
	Path    string
	Artwork string
}

// Service owns media caches and transcoding state for the whole process.
// Paths come from the shared catalog, not from a profile's taste index.
type Service struct {
	cfg     config.Config
	filesOf func(id int64) (trackFiles, bool)

	warm *streamWarm
}

// New creates a media service that reads paths from idx.
func New(cfg config.Config, idx *index.Index) *Service {
	return newService(cfg, func(id int64) (trackFiles, bool) {
		if idx == nil {
			return trackFiles{}, false
		}
		row, ok := idx.RowOf(id)
		if !ok {
			return trackFiles{}, false
		}
		meta := idx.MetaAt(row)
		return trackFiles{Path: meta.Path, Artwork: meta.ArtworkPath}, true
	})
}

// NewCatalog creates a media service that reads paths from the shared catalog.
func NewCatalog(cfg config.Config, catalog *index.CatalogSource) *Service {
	return newService(cfg, func(id int64) (trackFiles, bool) {
		if catalog == nil {
			return trackFiles{}, false
		}
		cat, err := catalog.Current()
		if err != nil || cat == nil {
			return trackFiles{}, false
		}
		meta, ok := cat.Track(id)
		if !ok {
			return trackFiles{}, false
		}
		return trackFiles{Path: meta.Path, Artwork: meta.ArtworkPath}, true
	})
}

func newService(cfg config.Config, filesOf func(int64) (trackFiles, bool)) *Service {
	return &Service{cfg: cfg, filesOf: filesOf, warm: sharedWarm}
}

func (s *Service) files(id int64) (trackFiles, bool) {
	if s == nil || s.filesOf == nil {
		return trackFiles{}, false
	}
	return s.filesOf(id)
}

func (s *Service) streamCacheDir() string {
	return filepath.Join(s.cfg.DataRoot(), "cache", "stream")
}

func (s *Service) artworkCacheDir() string {
	return filepath.Join(s.cfg.DataRoot(), "cache", "art")
}

func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
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
