package db

import (
	"testing"
	"time"
)

func TestWeeklyListeningCountsTimeAndTops(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, duration, created_at, updated_at)
		 VALUES (1, '/a.flac', 'Alpha', 'North', 180, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		        (2, '/b.flac', 'Beta', 'South', 200, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows := []struct {
		track              int64
		action, reason     string
		listened, duration float64
	}{
		{1, "track_start", "", 0, 180},
		{1, "track_start", "", 0, 180},
		{1, "track_end", "completed", 170, 180},
		{2, "track_start", "", 0, 200},
		{2, "skip", "skipped", 20, 200},
	}
	for i, row := range rows {
		if _, err := store.DB.Exec(`
INSERT INTO listening_history(profile_id, track_id, ts, action, reason, duration_sec, listened_sec)
VALUES (?,?,?,?,?,?,?)`,
			store.ProfileID, row.track, now, row.action, row.reason, row.duration, row.listened,
		); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	metrics, err := store.WeeklyMetrics()
	if err != nil {
		t.Fatal(err)
	}
	got := metrics.Listening
	if got.Plays != 3 || got.Finishes != 1 || got.ListenedSec != 190 {
		t.Fatalf("listening = %+v", got)
	}
	if got.FinishRate < 0.33 || got.FinishRate > 0.34 {
		t.Fatalf("finish rate = %v", got.FinishRate)
	}
	if len(got.TopTracks) != 2 || got.TopTracks[0].TrackID != 1 || got.TopTracks[0].Plays != 2 || got.TopTracks[0].Title != "Alpha" {
		t.Fatalf("top tracks = %+v", got.TopTracks)
	}
	if len(got.TopArtists) != 2 || got.TopArtists[0].Artist != "North" || got.TopArtists[0].Count != 2 {
		t.Fatalf("top artists = %+v", got.TopArtists)
	}
}

func TestListeningMemoryDayAndYear(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, duration, created_at, updated_at)
		 VALUES (1, '/a.flac', 'Alpha', 'North', 180, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		        (2, '/b.flac', 'Beta', 'South', 200, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	lastYear := now.AddDate(-1, 0, 0)
	if lastYear.Month() != now.Month() || lastYear.Day() != now.Day() {
		t.Skip("this calendar day did not exist last year")
	}
	other := lastYear.AddDate(0, 0, -3)
	insert := func(track int64, ts time.Time, action, reason string, listened float64) {
		t.Helper()
		if _, err := store.DB.Exec(`
INSERT INTO listening_history(profile_id, track_id, ts, action, reason, duration_sec, listened_sec)
VALUES (?,?,?,?,?,?,?)`,
			store.ProfileID, track, ts.Format(time.RFC3339Nano), action, reason, 180, listened,
		); err != nil {
			t.Fatal(err)
		}
	}
	insert(1, lastYear, "track_start", "", 0)
	insert(1, lastYear.Add(time.Hour), "track_start", "", 0)
	insert(2, other, "track_start", "", 0)
	insert(1, now, "track_start", "", 0)
	insert(1, now, "track_end", "completed", 180)
	insert(2, now.AddDate(0, 0, -10), "track_start", "", 0)

	mem, err := store.ListeningMemory(now)
	if err != nil {
		t.Fatal(err)
	}
	if mem.OnThisDay.Month != int(now.Month()) || mem.OnThisDay.Day != now.Day() {
		t.Fatalf("day = %+v", mem.OnThisDay)
	}
	if len(mem.OnThisDay.Tracks) != 1 || mem.OnThisDay.Tracks[0].TrackID != 1 || mem.OnThisDay.Tracks[0].Plays != 2 {
		t.Fatalf("on this day = %+v", mem.OnThisDay.Tracks)
	}
	if mem.Year.Year != now.Year() || mem.Year.Plays != 2 || mem.Year.Finishes != 1 || mem.Year.ListenedSec != 180 {
		t.Fatalf("year = %+v", mem.Year)
	}
	if len(mem.Year.TopTracks) != 2 || mem.Year.TopTracks[0].TrackID != 1 {
		t.Fatalf("year tracks = %+v", mem.Year.TopTracks)
	}
}
