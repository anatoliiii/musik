package api

import (
	"net/http"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
)

func (s *Server) handleWeeklyMetrics(w http.ResponseWriter, _ *http.Request) {
	m, err := s.Store.WeeklyMetrics()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, m)
}

func (s *Server) handleListeningMemory(w http.ResponseWriter, _ *http.Request) {
	mem, err := s.Store.ListeningMemory(time.Now().UTC())
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, mem)
}

func (s *Server) handleRecommendationMetrics(w http.ResponseWriter, _ *http.Request) {
	m, err := s.Store.OutcomeMetrics(90)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	policies, _ := s.Store.LatestRecommendationPolicies(8)
	if policies == nil {
		policies = []db.RecommendationPolicyRow{}
	}
	runs, _ := s.Store.ListTrainingRuns(5)
	if runs == nil {
		runs = []db.TrainingRunRow{}
	}
	writeJSON(w, map[string]any{
		"window":          "90d",
		"outcomes":        m,
		"latency_ms":      s.latency.Snapshot(),
		"last_policy":     firstPolicy(policies),
		"recent_requests": policies,
		"training_runs":   runs,
		"explore":         s.exploreState(),
	})
}

func firstPolicy(rows []db.RecommendationPolicyRow) any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
