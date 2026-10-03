package api

import (
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/rules"
)

func (s *Server) globalBlocks() *rules.Evaluator {
	if s.Store == nil || s.Idx == nil {
		return nil
	}
	active, err := s.Store.ActiveRadioRules("", nil)
	if err != nil || len(active) == 0 {
		return nil
	}
	return rules.New(s.Idx, active, time.Now().UTC())
}

func generatedPlaylist(pl *db.UserPlaylist) bool {
	if pl == nil {
		return false
	}
	return pl.Type == "smart" || playback.GeneratedMixKind(pl.Kind)
}

func (s *Server) omitBlockedPlaylist(pl *db.UserPlaylist) {
	if !generatedPlaylist(pl) {
		return
	}
	blocks := s.globalBlocks()
	if blocks == nil || len(pl.Tracks) == 0 {
		return
	}
	kept := make([]db.UserPlaylistItem, 0, len(pl.Tracks))
	for _, item := range pl.Tracks {
		if item.TrackID != 0 && blocks.HardBlocked(item.TrackID) {
			continue
		}
		kept = append(kept, item)
	}
	pl.Tracks = kept
	pl.TrackCount = len(kept)
}
