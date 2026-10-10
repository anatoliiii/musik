package db

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

const (
	BaselineMinimumDays        = 14
	BaselineMinimumImpressions = 300
	MetricMinimumSample        = 30
)

type Interval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type MetricSlice struct {
	Dimension       string   `json:"dimension"`
	Value           string   `json:"value"`
	Impressions     int      `json:"impressions"`
	Played          int      `json:"played"`
	Finished        int      `json:"finished"`
	Partial         int      `json:"partial"`
	EarlySkips      int      `json:"early_skips"`
	Likes           int      `json:"likes"`
	Superseded      int      `json:"superseded"`
	Abandoned       int      `json:"abandoned"`
	UniqueArtists   int      `json:"unique_artists"`
	FinishRate      float64  `json:"finish_rate"`
	EarlySkipRate   float64  `json:"early_skip_rate"`
	LikesPer100     float64  `json:"likes_per_100_played"`
	ArtistDiversity float64  `json:"artist_diversity"`
	FinishInterval  Interval `json:"finish_interval_95"`
	SkipInterval    Interval `json:"early_skip_interval_95"`
	LikeInterval    Interval `json:"like_interval_95"`
	Sufficient      bool     `json:"sufficient_sample"`
}

type BaselineStatus struct {
	Ready               bool   `json:"ready"`
	StartedAt           string `json:"started_at,omitempty"`
	ObservedDays        int    `json:"observed_days"`
	EligibleImpressions int    `json:"eligible_algorithmic_impressions"`
	MinimumDays         int    `json:"minimum_days"`
	MinimumImpressions  int    `json:"minimum_impressions"`
}

type Metrics struct {
	WindowDays int            `json:"window_days"`
	Baseline   BaselineStatus `json:"baseline"`
	Overall    MetricSlice    `json:"overall"`
	Breakdowns []MetricSlice  `json:"breakdowns"`

	// Stable compatibility fields for existing dashboard clients.
	Listens7d       int     `json:"listens_7d"`
	Skips7d         int     `json:"skips_7d"`
	SkipRate7d      float64 `json:"skip_rate_7d"`
	Completes7d     int     `json:"completes_7d"`
	ExploreShown    int     `json:"explore_shown"`
	ExploitShown    int     `json:"exploit_shown"`
	ExploreSkips    int     `json:"explore_early_skips"`
	ExploitSkips    int     `json:"exploit_early_skips"`
	ExploreComplete int     `json:"explore_completes"`
	ExploitComplete int     `json:"exploit_completes"`
	ExploreSkipRate float64 `json:"explore_skip_rate"`
	ExploitSkipRate float64 `json:"exploit_skip_rate"`
	ExploreCompRate float64 `json:"explore_complete_rate"`
	ExploitCompRate float64 `json:"exploit_complete_rate"`
	UniqueArtists7d int     `json:"unique_artists_7d"`
}

func (s *Store) WeeklyMetrics() (Metrics, error) {
	return s.OutcomeMetrics(7)
}

func (s *Store) OutcomeMetrics(days int) (Metrics, error) {
	if days < 1 {
		days = 7
	}
	var metrics Metrics
	metrics.WindowDays = days
	overall, err := s.metricSlices(days, "overall", "'all'")
	if err != nil {
		return metrics, err
	}
	if len(overall) > 0 {
		metrics.Overall = overall[0]
	}
	hour := s.Dialect.HourUTC("i.played_at")
	dimensions := []struct {
		name string
		sql  string
	}{
		{"source", "i.source"},
		{"daypart", `CASE
			WHEN i.played_at IS NULL THEN 'unplayed'
			WHEN ` + hour + ` BETWEEN 5 AND 11 THEN 'morning'
			WHEN ` + hour + ` BETWEEN 12 AND 16 THEN 'afternoon'
			WHEN ` + hour + ` BETWEEN 17 AND 22 THEN 'evening'
			ELSE 'night' END`},
		{"maturity", "COALESCE(NULLIF(i.maturity,''), 'unknown')"},
		{"date", s.Dialect.DateUTC("i.queued_at")},
	}
	for _, dimension := range dimensions {
		slices, err := s.metricSlices(days, dimension.name, dimension.sql)
		if err != nil {
			return metrics, err
		}
		metrics.Breakdowns = append(metrics.Breakdowns, slices...)
	}
	metrics.Baseline, err = s.baselineStatus()
	if err != nil {
		return metrics, err
	}
	metrics.Listens7d = metrics.Overall.Played
	metrics.Skips7d = metrics.Overall.EarlySkips
	metrics.SkipRate7d = metrics.Overall.EarlySkipRate
	metrics.Completes7d = metrics.Overall.Finished
	metrics.UniqueArtists7d = metrics.Overall.UniqueArtists
	for _, slice := range metrics.Breakdowns {
		switch {
		case slice.Dimension == "source" && isExploreMetricSource(slice.Value):
			metrics.ExploreShown += slice.Played
			metrics.ExploreSkips += slice.EarlySkips
			metrics.ExploreComplete += slice.Finished
		case slice.Dimension == "source" && isExploitMetricSource(slice.Value):
			metrics.ExploitShown += slice.Played
			metrics.ExploitSkips += slice.EarlySkips
			metrics.ExploitComplete += slice.Finished
		}
	}
	if metrics.ExploreShown > 0 {
		metrics.ExploreSkipRate = float64(metrics.ExploreSkips) / float64(metrics.ExploreShown)
		metrics.ExploreCompRate = float64(metrics.ExploreComplete) / float64(metrics.ExploreShown)
	}
	if metrics.ExploitShown > 0 {
		metrics.ExploitSkipRate = float64(metrics.ExploitSkips) / float64(metrics.ExploitShown)
		metrics.ExploitCompRate = float64(metrics.ExploitComplete) / float64(metrics.ExploitShown)
	}
	return metrics, nil
}

func (s *Store) metricSlices(days int, dimension, expression string) ([]MetricSlice, error) {
	query := fmt.Sprintf(`
SELECT %s AS slice_key,
       COUNT(*),
		SUM(CASE WHEN i.played_at IS NOT NULL THEN 1 ELSE 0 END),
		SUM(CASE WHEN i.outcome='finished' THEN 1 ELSE 0 END),
		SUM(CASE WHEN i.outcome='partial' THEN 1 ELSE 0 END),
		SUM(CASE WHEN i.outcome='early_skip' THEN 1 ELSE 0 END),
		SUM(CASE WHEN i.outcome='superseded' THEN 1 ELSE 0 END),
		SUM(CASE WHEN i.outcome='abandoned' THEN 1 ELSE 0 END),
		SUM(CASE WHEN EXISTS(
           SELECT 1 FROM listening_history h
           WHERE h.profile_id=i.profile_id AND h.impression_id=i.impression_id AND h.action='like'
       ) THEN 1 ELSE 0 END),
       COUNT(DISTINCT CASE WHEN i.played_at IS NOT NULL THEN NULLIF(t.artist,'') END)
FROM recommendation_impressions i
JOIN tracks t ON t.id=i.track_id
WHERE i.profile_id = ?
  AND i.legacy=0
  AND i.source NOT IN ('manual', 'legacy')
  AND COALESCE(i.mode, '') != 'share'
  AND i.queued_at >= ?
GROUP BY slice_key
ORDER BY slice_key`, expression)
	// Stored instants are UTC ISO-8601 text; a whole-second cutoff without a
	// zone suffix orders correctly against both "Z" and "+00:00" forms.
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02T15:04:05")
	return QueryAll(s.DB, func(rows *sql.Rows, slice *MetricSlice) error {
		slice.Dimension = dimension
		if err := rows.Scan(
			&slice.Value, &slice.Impressions, &slice.Played, &slice.Finished,
			&slice.Partial, &slice.EarlySkips, &slice.Superseded,
			&slice.Abandoned, &slice.Likes, &slice.UniqueArtists,
		); err != nil {
			return err
		}
		slice.finish()
		return nil
	}, query, s.ProfileID, cutoff)
}

func (m *MetricSlice) finish() {
	if m.Played > 0 {
		denominator := float64(m.Played)
		m.FinishRate = float64(m.Finished) / denominator
		m.EarlySkipRate = float64(m.EarlySkips) / denominator
		m.LikesPer100 = 100 * float64(m.Likes) / denominator
		m.ArtistDiversity = float64(m.UniqueArtists) / denominator
		m.FinishInterval = wilson(m.Finished, m.Played)
		m.SkipInterval = wilson(m.EarlySkips, m.Played)
		m.LikeInterval = wilson(m.Likes, m.Played)
	}
	m.Sufficient = m.Played >= MetricMinimumSample
}

func (s *Store) baselineStatus() (BaselineStatus, error) {
	status := BaselineStatus{
		MinimumDays:        BaselineMinimumDays,
		MinimumImpressions: BaselineMinimumImpressions,
	}
	var started string
	err := s.DB.QueryRow(`
SELECT COALESCE(MIN(played_at), ''), COUNT(*)
FROM recommendation_impressions
WHERE profile_id = ? AND legacy=0 AND source NOT IN ('manual','legacy')
  AND COALESCE(mode, '') != 'share' AND played_at IS NOT NULL`,
		s.ProfileID,
	).Scan(&started, &status.EligibleImpressions)
	if err != nil {
		return status, err
	}
	status.StartedAt = started
	var elapsed float64
	if at, ok := parseTimestamp(started); ok {
		elapsed = time.Since(at).Hours() / 24
		status.ObservedDays = int(math.Floor(elapsed)) + 1
	}
	status.Ready = elapsed >= BaselineMinimumDays &&
		status.EligibleImpressions >= BaselineMinimumImpressions
	return status, nil
}

func wilson(successes, total int) Interval {
	if total <= 0 {
		return Interval{}
	}
	const z = 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denominator
	return Interval{Low: math.Max(0, center-margin), High: math.Min(1, center+margin)}
}

func dayPart(h int) string {
	switch {
	case h >= 5 && h < 12:
		return "morning"
	case h >= 12 && h < 17:
		return "afternoon"
	case h >= 17 && h < 23:
		return "evening"
	default:
		return "night"
	}
}

// DayPart is the exported name for API/queue blending.
func DayPart(h int) string { return dayPart(h) }

func isExploreMetricSource(source string) bool {
	switch source {
	case "explore", "explore_adjacent", "resurface", "new_in_library", "wildcard":
		return true
	default:
		return false
	}
}

func isExploitMetricSource(source string) bool {
	switch source {
	case "exploit", "transition", "radio_start", "refill":
		return true
	default:
		return false
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// parseTimestamp reads a stored UTC instant: RFC 3339 as written by Go and
// Python, or SQLite's legacy "YYYY-MM-DD HH:MM:SS".
func parseTimestamp(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
