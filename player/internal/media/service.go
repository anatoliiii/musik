// Package media serves and prepares stream and artwork media.
package media

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/index"
)

// Service owns media caches and transcoding state.
type Service struct {
	cfg config.Config
	idx *index.Index

	mobileFlight  mobileFlight
	mobileVariant sync.Map
	warm          *streamWarm
}

// New creates a media service.
func New(cfg config.Config, idx *index.Index) *Service {
	return &Service{
		cfg:  cfg,
		idx:  idx,
		warm: newStreamWarm(8),
	}
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
