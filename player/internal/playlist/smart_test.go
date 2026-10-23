package playlist

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestSmartRulesSeeOnlyTheCallersProfile(t *testing.T) {
	databaseURL, path := testdb.Target(t, filepath.Join(t.TempDir(), "smart.db"))
	owner, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	for id := 1; id <= 3; id++ {
		if _, err := owner.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, year, duration, created_at, updated_at)
VALUES (?,?,?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, fmt.Sprintf("/smart-%d.flac", id), "Title", "Artist", "Album", 2000+id, 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	_, otherProfile, err := owner.CreateUserWithDefaultProfile(context.Background(), "Other")
	if err != nil {
		t.Fatal(err)
	}
	other := owner.ForProfile(otherProfile.ID)

	// Personal signals exist only in the owner's profile.
	if err := owner.FavoritesAdd(1); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ApplyLifecycleEvent(db.LifecycleEvent{EventID: db.NewID(), Type: "like", TrackID: 2, SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DB.Exec(
		`INSERT INTO track_stats(track_id, plays, updated_at, profile_id) VALUES (3, 4, '2026-01-01T00:00:00Z', ?)`,
		owner.ProfileID,
	); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct {
		id int64
		at string
	}{{1, old}, {2, recent}} {
		if _, err := owner.DB.Exec(`
INSERT INTO track_stats(track_id, plays, last_played_at, updated_at, profile_id)
VALUES (?, 0, ?, ?, ?)
ON CONFLICT(profile_id, track_id) DO UPDATE SET last_played_at = excluded.last_played_at`,
			row.id, row.at, now, owner.ProfileID); err != nil {
			t.Fatal(err)
		}
	}
	tag, err := owner.CreateCustomTag("Night")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.TagTrack(tag.ID, 2); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		rule        Rule
		owner, peer []int64
	}{
		{"liked", Rule{All: []Clause{{Field: "liked", Op: "eq", Value: true}}}, []int64{1}, nil},
		{"plays above a fractional threshold", Rule{All: []Clause{{Field: "plays", Op: "gt", Value: 3.5}}}, []int64{3}, nil},
		{"tag", Rule{All: []Clause{{Field: "tag", Op: "in", Value: []any{"night"}}}}, []int64{2}, nil},
		{"empty in-list matches nothing", Rule{All: []Clause{{Field: "artist", Op: "in", Value: []any{}}}}, nil, nil},
		{"year between", Rule{All: []Clause{{Field: "year", Op: "between", Value: []any{2001.0, 2002.0}}}}, []int64{1, 2}, []int64{1, 2}},
		{"played more than a week ago", Rule{All: []Clause{{Field: "last_played", Op: "older_than_days", Value: 7}}}, []int64{1}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, side := range []struct {
				store *db.Store
				want  []int64
			}{{owner, tc.owner}, {other, tc.peer}} {
				got, err := Evaluate(context.Background(), side.store, tc.rule)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, side.want) {
					t.Fatalf("profile %s: got %v, want %v", side.store.ProfileID, got, side.want)
				}
			}
		})
	}
}

func TestSmartPlaylistOrdersFrequentTracksFirst(t *testing.T) {
	databaseURL, path := testdb.Target(t, filepath.Join(t.TempDir(), "smart-order.db"))
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, row := range []struct {
		id, plays int64
	}{{1, 2}, {2, 9}, {3, 4}} {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, created_at, updated_at) VALUES (?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			row.id, fmt.Sprintf("/o-%d.flac", row.id), "T",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.Exec(
			`INSERT INTO track_stats(profile_id, track_id, plays, updated_at) VALUES (?,?,?, '2026-01-01T00:00:00Z')`,
			store.ProfileID, row.id, row.plays,
		); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Evaluate(context.Background(), store, Rule{
		Sort: "plays_desc",
		All:  []Clause{{Field: "plays", Op: "gte", Value: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int64{2, 3}) {
		t.Fatalf("order = %v, want [2 3]", got)
	}
}
