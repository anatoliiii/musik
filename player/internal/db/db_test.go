package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/testdb"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "musik-test.db")
	if err := testdb.Create(path); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestPostgreSQLQueryAdapterPreservesBindPositions(t *testing.T) {
	got, args, err := adaptQuery("SELECT ?, datetime('now', ?), '?' -- ?\n", []any{7, "-3 days"}, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT $1, (CURRENT_TIMESTAMP + CAST($2 AS INTERVAL)), '?' -- ?\n"
	if got != want {
		t.Fatalf("adaptQuery = %q, want %q", got, want)
	}
	if len(args) != 2 || args[0] != 7 || args[1] != "-3 days" {
		t.Fatalf("bound arguments changed: %#v", args)
	}
}

func TestPostgreSQLIgnoreBecomesConflictDoNothing(t *testing.T) {
	got, _, err := adaptQuery("INSERT OR IGNORE INTO request_contexts(request_id) VALUES (?)", []any{"id"}, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if got != "INSERT INTO request_contexts(request_id) VALUES ($1) ON CONFLICT DO NOTHING" {
		t.Fatalf("adaptQuery = %q", got)
	}
}

func TestPostgreSQLMetricTimeExpressionsArePortable(t *testing.T) {
	got, _, err := adaptQuery(`SELECT CAST(strftime('%H', i.played_at) AS INTEGER), date(i.queued_at), julianday('now') - julianday(MIN(played_at))`, nil, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT CAST(EXTRACT(HOUR FROM CAST(i.played_at AS TIMESTAMPTZ)) AS INTEGER), CAST(CAST(i.queued_at AS TIMESTAMPTZ) AS DATE), EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - CAST(MIN(played_at) AS TIMESTAMPTZ))) / 86400.0`
	if got != want {
		t.Fatalf("adaptQuery = %q, want %q", got, want)
	}
}

func TestPostgreSQLAdapterBindsProfileFromStoreContext(t *testing.T) {
	got, args, err := adaptQueryProfile(
		"SELECT id FROM profiles WHERE owner_profile=:musik_profile AND id=? AND note='?' -- ?",
		[]any{17}, "postgres", "profile-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT id FROM profiles WHERE owner_profile=$1 AND id=$2 AND note='?' -- ?"
	if got != want {
		t.Fatalf("adaptQueryProfile = %q, want %q", got, want)
	}
	if len(args) != 2 || args[0] != "profile-a" || args[1] != 17 {
		t.Fatalf("profile bind order = %#v", args)
	}
}

func TestPostgreSQLDatetimeColumnExpressionIsPortable(t *testing.T) {
	got, _, err := adaptQuery("SELECT datetime(i.queued_at) FROM recommendation_impressions i", nil, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT CAST(i.queued_at AS TIMESTAMPTZ) FROM recommendation_impressions i"
	if got != want {
		t.Fatalf("adaptQuery = %q, want %q", got, want)
	}
}

func TestPostgreSQLStoreWhenConfigured(t *testing.T) {
	databaseURL := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("set MUSIK_TEST_POSTGRES_URL to run PostgreSQL conformance smoke test")
	}
	store, err := OpenDatabase(databaseURL, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.Dialect != "postgres" {
		t.Fatalf("database dialect = %q, want postgres", store.Dialect)
	}
	if _, err := store.OutcomeMetrics(7); err != nil {
		t.Fatalf("PostgreSQL metrics query failed: %v", err)
	}

	payload := `{"test":true}`
	id, err := store.EnqueueJob("gorm-smoke-"+NewID(), payload)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.GetJob(id)
	if err != nil || job == nil || job.Payload != payload {
		t.Fatalf("ORM job roundtrip = %#v, %v", job, err)
	}
	if _, err := store.DB.Exec(`DELETE FROM jobs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	playlist, err := store.CreateUserPlaylist(UserPlaylist{Name: "gorm-smoke-" + NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if playlist.ID == 0 {
		t.Fatal("GORM create did not return the PostgreSQL playlist ID")
	}
	if _, err := store.DB.Exec(`DELETE FROM playlists WHERE id = ?`, playlist.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLProfilesAreIsolatedWhenConfigured(t *testing.T) {
	databaseURL := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("set MUSIK_TEST_POSTGRES_URL to run PostgreSQL profile tests")
	}
	store, err := OpenDatabase(databaseURL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	userID, mainProfile, err := store.CreateUserWithDefaultProfile(ctx, "PostgreSQL isolation test")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := store.CreateProfile(ctx, userID, "Secondary")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := store.DB.Exec(`DELETE FROM playlists WHERE profile_id IN (?,?)`, mainProfile.ID, secondary.ID); err != nil {
			t.Errorf("clean test playlists: %v", err)
		}
		if _, err := store.DB.Exec(`DELETE FROM user_roles WHERE user_id=?`, userID); err != nil {
			t.Errorf("clean test roles: %v", err)
		}
		if _, err := store.DB.Exec(`DELETE FROM profiles WHERE id IN (?,?) AND owner_user_id=?`, mainProfile.ID, secondary.ID, userID); err != nil {
			t.Errorf("clean test profiles: %v", err)
		}
		if _, err := store.DB.Exec(`DELETE FROM users WHERE id=?`, userID); err != nil {
			t.Errorf("clean test user: %v", err)
		}
	}()
	mainStore := store.ForProfile(mainProfile.ID)
	secondaryStore := store.ForProfile(secondary.ID)
	if _, err := mainStore.CreateUserPlaylist(UserPlaylist{Name: "main-" + NewID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondaryStore.CreateUserPlaylist(UserPlaylist{Name: "secondary-" + NewID()}); err != nil {
		t.Fatal(err)
	}
	mainLists, err := mainStore.ListUserPlaylists(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	secondaryLists, err := secondaryStore.ListUserPlaylists(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(mainLists) != 1 || len(secondaryLists) != 1 || mainLists[0].Name == secondaryLists[0].Name {
		t.Fatalf("profile playlists crossed scopes: main=%#v secondary=%#v", mainLists, secondaryLists)
	}
}

func TestMondayZeroWeekday(t *testing.T) {
	tests := []struct {
		day  time.Weekday
		want int
	}{
		{time.Monday, 0},
		{time.Tuesday, 1},
		{time.Saturday, 5},
		{time.Sunday, 6},
	}
	for _, tt := range tests {
		if got := mondayZeroWeekday(tt.day); got != tt.want {
			t.Fatalf("mondayZeroWeekday(%s) = %d, want %d", tt.day, got, tt.want)
		}
	}
}

func TestOpenRequiresExactMigratedSchemaVersion(t *testing.T) {
	for _, version := range []int{0, SupportedSchemaVersion + 1} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("schema-%d.db", version))
		conn, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = Open(path)
		if err == nil {
			t.Fatalf("Open accepted schema version %d", version)
		}
		if !strings.Contains(err.Error(), "musik db migrate") {
			t.Fatalf("error %q does not explain how to migrate", err)
		}
	}
}

func TestLatestPlaylistSelectsNewestAndPreservesPositions(t *testing.T) {
	store, _ := openTestStore(t)
	for id := int64(1); id <= 3; id++ {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, duration) VALUES (?, ?, ?, ?, ?)`,
			id, fmt.Sprintf("/track-%d.flac", id), fmt.Sprintf("Track %d", id), "Artist", 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec(`
		INSERT INTO playlists(id, kind, name, created_at) VALUES
			(10, 'daily', 'Old', '2026-08-14T00:00:00Z'),
			(11, 'daily', 'New', '2026-08-15T00:00:00Z'),
			(12, 'weekly', 'Other kind', '2026-08-16T00:00:00Z');
		INSERT INTO playlist_tracks(playlist_id, position, track_id, explanation) VALUES
			(10, 0, 1, 'old'),
			(11, 7, 2, 'second'),
			(11, 3, 3, 'first')`); err != nil {
		t.Fatal(err)
	}

	playlist, err := store.LatestPlaylist("daily")
	if err != nil {
		t.Fatal(err)
	}
	if playlist == nil || playlist.ID != 11 || playlist.Name != "New" {
		t.Fatalf("got playlist %#v, want newest daily playlist 11", playlist)
	}
	if len(playlist.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(playlist.Tracks))
	}
	if got := []int{playlist.Tracks[0].Position, playlist.Tracks[1].Position}; got[0] != 3 || got[1] != 7 {
		t.Fatalf("positions = %v, want [3 7]", got)
	}
	if playlist.Tracks[0].TrackID != 3 || playlist.Tracks[1].TrackID != 2 {
		t.Fatalf("track order = [%d %d], want [3 2]", playlist.Tracks[0].TrackID, playlist.Tracks[1].TrackID)
	}
}

func TestConcurrentWorkerAndEventWrites(t *testing.T) {
	store, path := openTestStore(t)
	if _, err := store.DB.Exec(`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.EnqueueJob("mix_pack", `{}`)
	if err != nil {
		t.Fatal(err)
	}

	worker, err := sql.Open("sqlite",
		"file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	worker.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = worker.Close() })

	const writes = 40
	errs := make(chan error, writes*3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			_, err := worker.Exec(
				`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
				"running", time.Now().UTC().Format(time.RFC3339Nano), jobID,
			)
			if err != nil {
				errs <- fmt.Errorf("worker write %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if _, err := store.InsertListen(1, "progress", "test", "session", "",
				nil, nil, nil); err != nil {
				errs <- fmt.Errorf("history write %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if err := store.BumpRecStats(1, 1, 0, 0); err != nil {
				errs <- fmt.Errorf("rec stats write %d: %w", i, err)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	var histories, shown int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM listening_history`).Scan(&histories); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT shown FROM rec_stats WHERE track_id = 1`).Scan(&shown); err != nil {
		t.Fatal(err)
	}
	if histories != writes || shown != writes {
		t.Fatalf("history=%d shown=%d, want %d each", histories, shown, writes)
	}
}

func TestRecommendationImpressionsJoinOutcomes(t *testing.T) {
	store, _ := openTestStore(t)
	for id := int64(1); id <= 2; id++ {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title) VALUES (?, ?, ?)`,
			id, fmt.Sprintf("/track-%d.flac", id), fmt.Sprintf("Track %d", id),
		); err != nil {
			t.Fatal(err)
		}
	}
	impressions := []RecommendationImpression{
		{SessionID: "s1", TrackID: 1, Position: 0, Explore: true, Score: 0.2},
		{SessionID: "s1", TrackID: 2, Position: 1, Explore: false, Score: 0.8},
	}
	if err := store.InsertRecommendationImpressions(impressions); err != nil {
		t.Fatal(err)
	}
	duration := 100.0
	skipped := 10.0
	completed := 95.0
	for index, item := range impressions {
		if _, err := store.ApplyLifecycleEvent(LifecycleEvent{
			EventID: fmt.Sprintf("start-%d", index), Type: "track_start",
			TrackID: item.TrackID, SessionID: "s1", ImpressionID: item.ImpressionID,
			RequestID: item.RequestID, Source: item.Source,
		}); err != nil {
			t.Fatal(err)
		}
		outcome, listened := "early_skip", skipped
		if item.TrackID == 2 {
			outcome, listened = "finished", completed
		}
		if _, err := store.ApplyLifecycleEvent(LifecycleEvent{
			EventID: fmt.Sprintf("end-%d", index), Type: "track_end",
			TrackID: item.TrackID, SessionID: "s1", ImpressionID: item.ImpressionID,
			RequestID: item.RequestID, Source: item.Source, DurationSec: &duration,
			ListenedSec: &listened, ListenedRatio: listened / duration, Outcome: outcome,
		}); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := store.WeeklyMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ExploreShown != 1 || metrics.ExploitShown != 1 {
		t.Fatalf("shown explore/exploit=%d/%d, want 1/1", metrics.ExploreShown, metrics.ExploitShown)
	}
	if metrics.ExploreSkips != 1 || metrics.ExploitComplete != 1 {
		t.Fatalf("outcomes explore skips=%d exploit completes=%d, want 1/1",
			metrics.ExploreSkips, metrics.ExploitComplete)
	}
	if metrics.ExploreSkipRate != 1 || metrics.ExploitCompRate != 1 {
		t.Fatalf("rates explore skip=%f exploit complete=%f, want 1/1",
			metrics.ExploreSkipRate, metrics.ExploitCompRate)
	}
}

func TestLifecycleEventsAreIdempotent(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`,
	); err != nil {
		t.Fatal(err)
	}
	requestID, impressionID := NewID(), NewID()
	if err := store.CreateRecommendationRequest(RecommendationRequest{
		RequestID: requestID, SessionID: "s1", Reason: "radio_start",
		PolicyVersion: "test", CandidateCount: 1,
	}, []RecommendationImpression{{
		ImpressionID: impressionID, SessionID: "s1", TrackID: 1,
		Source: "radio_start",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	start := LifecycleEvent{
		EventID: "start-1", Type: "track_start", TrackID: 1,
		SessionID: "s1", ImpressionID: impressionID,
		RequestID: requestID, Source: "radio_start",
	}
	first, err := store.ApplyLifecycleEvent(start)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.ApplyLifecycleEvent(start)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Inserted || !first.LifecycleChanged || retry.Inserted {
		t.Fatalf("first=%+v retry=%+v", first, retry)
	}
	end := LifecycleEvent{
		EventID: "end-1", Type: "track_end", TrackID: 1,
		SessionID: "s1", ImpressionID: impressionID, RequestID: requestID,
		Source: "radio_start", Outcome: "finished", ListenedRatio: 0.95,
	}
	if result, err := store.ApplyLifecycleEvent(end); err != nil || !result.LifecycleChanged {
		t.Fatalf("end result=%+v err=%v", result, err)
	}
	end.EventID = "end-second-client-id"
	if result, err := store.ApplyLifecycleEvent(end); err != nil || result.LifecycleChanged {
		t.Fatalf("second close result=%+v err=%v", result, err)
	}
	var plays, finishes int
	if err := store.DB.QueryRow(
		`SELECT plays, finishes FROM track_stats WHERE track_id=1`,
	).Scan(&plays, &finishes); err != nil {
		t.Fatal(err)
	}
	if plays != 1 || finishes != 1 {
		t.Fatalf("track stats plays/finishes=%d/%d, want 1/1", plays, finishes)
	}
}

func TestLegacyFallbackRejectsAmbiguousPendingImpressions(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`,
	); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		requestID := NewID()
		if err := store.CreateRecommendationRequest(RecommendationRequest{
			RequestID: requestID, SessionID: "s1", Reason: "refill",
			PolicyVersion: "test", CandidateCount: 1,
		}, []RecommendationImpression{{
			ImpressionID: NewID(), SessionID: "s1", TrackID: 1, Source: "exploit",
		}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ResolveImpression("", "s1", 1); err != ErrAmbiguousImpression {
		t.Fatalf("fallback error=%v, want ErrAmbiguousImpression", err)
	}
}

func TestBaselineStatusAndMetricBreakdowns(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist) VALUES (1, '/track.flac', 'Track', 'Artist')`,
	); err != nil {
		t.Fatal(err)
	}
	requestID := NewID()
	if _, err := store.DB.Exec(`
INSERT INTO recommendation_requests(
  request_id, session_id, reason, policy_version, candidate_count, latency_ms, created_at
) VALUES (?, 's1', 'refill', 'test', 300, 1, datetime('now','-15 days'))`,
		requestID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < BaselineMinimumImpressions; i++ {
		outcome := "finished"
		if i%4 == 0 {
			outcome = "early_skip"
		}
		queued := "datetime('now','-15 days')"
		if i > 0 {
			queued = "datetime('now')"
		}
		query := fmt.Sprintf(`
INSERT INTO recommendation_impressions(
  session_id, track_id, position, score, cosine_taste, cosine_current,
  explore, new_boost, maturity, mode, shown_at, impression_id, request_id,
  source, queued_at, played_at, closed_at, outcome, listened_ratio, legacy
) VALUES (
  's1', 1, ?, 0, 0, 0, 0, 0, 'ready', 'radio', %s, ?, ?,
  'exploit', %s, %s, %s, ?, 0.9, 0
)`, queued, queued, queued, queued)
		if _, err := store.DB.Exec(query, i, NewID(), requestID, outcome); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := store.OutcomeMetrics(30)
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.Baseline.Ready ||
		metrics.Baseline.EligibleImpressions != BaselineMinimumImpressions {
		t.Fatalf("baseline=%+v", metrics.Baseline)
	}
	if metrics.Overall.Played != BaselineMinimumImpressions ||
		metrics.Overall.Finished == 0 || metrics.Overall.EarlySkips == 0 {
		t.Fatalf("overall=%+v", metrics.Overall)
	}
	seen := map[string]bool{}
	for _, slice := range metrics.Breakdowns {
		seen[slice.Dimension] = true
	}
	for _, dimension := range []string{"source", "daypart", "maturity", "date"} {
		if !seen[dimension] {
			t.Errorf("missing %s breakdown", dimension)
		}
	}
	if metrics.Overall.FinishInterval.Low <= 0 || metrics.Overall.FinishInterval.High < metrics.Overall.FinishRate {
		t.Fatalf("finish interval=%+v rate=%f", metrics.Overall.FinishInterval, metrics.Overall.FinishRate)
	}
}

func TestMetricsExcludeLegacyManualAndShare(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist) VALUES (1, '/track.flac', 'Track', 'Artist')`,
	); err != nil {
		t.Fatal(err)
	}
	requestID := NewID()
	if _, err := store.DB.Exec(`
INSERT INTO recommendation_requests(
  request_id, session_id, reason, policy_version, candidate_count, latency_ms, created_at
) VALUES (?, 's1', 'refill', 'test', 3, 1, datetime('now'))`, requestID); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		source, mode, outcome string
		legacy                int
	}{
		{"exploit", "radio", "finished", 0},
		{"manual", "radio", "finished", 0},
		{"exploit", "share", "finished", 0},
		{"legacy", "radio", "finished", 1},
	}
	for i, row := range rows {
		if _, err := store.DB.Exec(`
INSERT INTO recommendation_impressions(
  session_id, track_id, position, score, cosine_taste, cosine_current,
  explore, new_boost, maturity, mode, shown_at, impression_id, request_id,
  source, queued_at, played_at, closed_at, outcome, listened_ratio, legacy
) VALUES (
  's1', 1, ?, 0, 0, 0, 0, 0, 'ready', ?, datetime('now'), ?, ?,
  ?, datetime('now'), datetime('now'), datetime('now'), ?, 0.9, ?
)`, i, row.mode, NewID(), requestID, row.source, row.outcome, row.legacy); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := store.OutcomeMetrics(7)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Overall.Played != 1 || metrics.Overall.Finished != 1 {
		t.Fatalf("eligible metrics=%+v, want only the algorithmic radio play", metrics.Overall)
	}
}

func TestLikeStatsAreIdempotentWithoutSameEventID(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`,
	); err != nil {
		t.Fatal(err)
	}
	impressionID, requestID := NewID(), NewID()
	if err := store.CreateRecommendationRequest(RecommendationRequest{
		RequestID: requestID, SessionID: "s1", Reason: "radio_start",
		PolicyVersion: "test", CandidateCount: 1,
	}, []RecommendationImpression{{
		ImpressionID: impressionID, SessionID: "s1", TrackID: 1, Source: "radio_start",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	like := LifecycleEvent{
		Type: "like", TrackID: 1, SessionID: "s1",
		ImpressionID: impressionID, RequestID: requestID, Source: "radio_start",
	}
	like.EventID = "like-a"
	if _, err := store.ApplyLifecycleEvent(like); err != nil {
		t.Fatal(err)
	}
	like.EventID = "like-b"
	if result, err := store.ApplyLifecycleEvent(like); err != nil || result.LifecycleChanged {
		t.Fatalf("second like result=%+v err=%v", result, err)
	}
	var likes int
	if err := store.DB.QueryRow(`SELECT likes FROM track_stats WHERE track_id=1`).Scan(&likes); err != nil {
		t.Fatal(err)
	}
	if likes != 1 {
		t.Fatalf("likes=%d, want 1", likes)
	}
}

func TestLatestPoliciesAndTrainingRuns(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`,
	); err != nil {
		t.Fatal(err)
	}
	requestID := NewID()
	if err := store.CreateRecommendationRequest(RecommendationRequest{
		RequestID: requestID, SessionID: "s1", Reason: "radio_start",
		PolicyVersion: "queue-core-v1", ModelVersion: "default-v1",
		CandidateCount: 3, LatencyMS: 12.5,
		PolicyJSON: `{"reason":"static","explore_share":0.2}`,
	}, []RecommendationImpression{{
		ImpressionID: NewID(), SessionID: "s1", TrackID: 1, Source: "exploit",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO model_versions(model_version,model_type,feature_schema_version,artifact_path,artifact_hash,status,created_at) VALUES ('ranker-v2','ranker',1,'/test/ranker.json','hash','active',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`
INSERT INTO training_runs(
  run_id, model_version, model_type, feature_schema_version, train_from, train_until,
  positive_count, negative_count, metrics_schema_version, metrics_json, status, created_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,datetime('now'))`,
		NewID(), "ranker-v2", "linear", 1, "2026-01-01", "2026-02-01",
		12, 8, 1, `{"auc":0.71}`, "completed",
	); err != nil {
		t.Fatal(err)
	}
	policies, err := store.LatestRecommendationPolicies(3)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies=%v err=%v", policies, err)
	}
	if policies[0].PolicyVersion != "queue-core-v1" || policies[0].Policy["reason"] != "static" {
		t.Fatalf("policy=%+v", policies[0])
	}
	runs, err := store.ListTrainingRuns(3)
	if err != nil || len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	if runs[0].Metrics["auc"] != 0.71 {
		t.Fatalf("metrics=%v", runs[0].Metrics)
	}
}
