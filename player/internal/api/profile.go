package api

import (
	"net/http"

	"github.com/torwin-job/musik/player/internal/taste"
)

func (s *Server) handleProfile(w http.ResponseWriter, _ *http.Request) {
	pos, neg := s.Taste.Counts()
	if dbPos, dbNeg, err := s.Store.ListenSignalCounts(); err == nil {
		if dbPos > pos {
			pos = dbPos
		}
		if dbNeg > neg {
			neg = dbNeg
		}
	}
	mat := taste.MaturityOf(pos, s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
	explore := taste.ExploreFor(mat, s.Cfg.ExploreRatio, s.Cfg.DiscoverExploreRatio)
	artists, _ := s.Store.TopArtists(5)
	clusters, _ := s.Store.TopClusters(5)
	confidence := float64(pos) / float64(s.Cfg.ProfileReadyAt)
	if confidence > 1 {
		confidence = 1
	}
	out := map[string]any{
		"ready":         mat == taste.StatusReady,
		"maturity":      mat,
		"n_positive":    pos,
		"n_negative":    neg,
		"ready_at":      s.Cfg.ProfileReadyAt,
		"forming_at":    s.Cfg.ProfileFormingAt,
		"confidence":    confidence,
		"explore_ratio": explore,
		"source":        s.Taste.SourceName(),
		"taste_vector":  s.Taste.Ready(),
		"top_artists":   artists,
		"top_clusters":  clusters,
		"model_version": s.Play.Ranker.ModelVersion,
	}
	for key, value := range s.exploreState() {
		out[key] = value
	}
	writeJSON(w, out)
}
