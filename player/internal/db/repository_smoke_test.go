package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRepositorySmoke calls repository methods that other tests do not reach,
// so SQL that only one backend accepts fails on the run for the other
// (MUSIK_TEST_BACKEND=postgres). Assertions check the basic contract only.
func TestRepositorySmoke(t *testing.T) {
	store, _, _ := openTestTarget(t)
	ctx := context.Background()
	insertTracks(t, store, 3)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("taste contexts", func(t *testing.T) {
		created, err := store.CreateTasteContext(TasteContext{Kind: "mood", Name: "Calm", Influence: 1, LearningEnabled: true})
		must(err)
		must(store.UpdateTasteContext(TasteContext{ID: created.ID, Name: "Calmer", Influence: 1.5, LearningEnabled: true,
			Seeds: ContextSeeds{SchemaVersion: 1, Tracks: []int64{1}}}))
		got, err := store.GetTasteContext(created.ID)
		must(err)
		if got == nil || got.Name != "Calmer" || got.Influence != 1.5 || len(got.Seeds.Tracks) != 1 {
			t.Fatalf("context=%+v", got)
		}
		must(store.UpsertTasteContextState(TasteContextState{ContextID: created.ID, PositiveVector: []byte{1, 2, 3, 4}, EmbeddingDim: 1, PositiveSamples: 2}))
		state, err := store.LoadTasteContextState(created.ID)
		must(err)
		if state == nil || state.PositiveSamples != 2 || state.EmbeddingDim != 1 {
			t.Fatalf("state=%+v", state)
		}
		must(store.UpsertPlaySession(PlaySessionRow{ID: "smoke-session", Mode: "radio", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}))
		must(store.SetSessionContexts("smoke-session", []string{created.ID}))
	})

	t.Run("sessions", func(t *testing.T) {
		must(store.UpsertPlaySession(PlaySessionRow{ID: "smoke-old", Mode: "radio", UpdatedAt: "2026-01-01T00:00:00Z"}))
		ids, err := store.OldestPlaySessionIDs(1)
		must(err)
		if len(ids) != 1 || ids[0] != "smoke-old" {
			t.Fatalf("oldest=%v", ids)
		}
		must(store.DeletePlaySession("smoke-old"))
		if _, ok, err := store.LoadPlaySession("smoke-old"); err != nil || ok {
			t.Fatalf("deleted session ok=%v err=%v", ok, err)
		}
	})

	t.Run("impressions", func(t *testing.T) {
		requestID := NewID()
		pending, abandoned := NewID(), NewID()
		must(store.CreateRecommendationRequest(RecommendationRequest{RequestID: requestID, SessionID: "s-imp", Reason: "refill", PolicyVersion: "test"},
			[]RecommendationImpression{
				{ImpressionID: pending, SessionID: "s-imp", TrackID: 1, Source: "exploit"},
				{ImpressionID: abandoned, SessionID: "s-imp", TrackID: 2, Source: "exploit"},
			}, nil))
		must(store.AbandonSessionImpressions("s-imp"))
		var outcomes []string
		must(store.DB.Select(ctx, &outcomes, `SELECT outcome FROM recommendation_impressions WHERE profile_id = ? AND session_id = 's-imp' ORDER BY track_id`, store.ProfileID))
		if len(outcomes) != 2 || outcomes[0] != "abandoned" || outcomes[1] != "abandoned" {
			t.Fatalf("outcomes=%v", outcomes)
		}
	})

	t.Run("accounts and invitations", func(t *testing.T) {
		userID, profile, err := store.CreateUserWithDefaultProfile(ctx, "Smoke")
		must(err)
		must(store.RenameProfile(ctx, userID, profile.ID, "Renamed"))
		name, roles, err := store.UserInfo(ctx, userID)
		must(err)
		if name != "Smoke" || len(roles) != 1 || roles[0] != "user" {
			t.Fatalf("user=%q roles=%v", name, roles)
		}
		if ready, err := store.BootstrapReady(ctx); err != nil || ready {
			t.Fatalf("bootstrap ready=%v err=%v (no admin, no owner invitation)", ready, err)
		}
		token, _, err := store.CreateDeviceToken(ctx, userID, profile.ID, "Phone")
		must(err)
		must(store.RevokeDeviceToken(ctx, userID, token.ID))
		if err := store.RevokeDeviceToken(ctx, userID, "missing"); err != ErrDeviceTokenNotFound {
			t.Fatalf("revoke missing=%v", err)
		}
		if SessionFingerprint("a") == SessionFingerprint("b") {
			t.Fatal("fingerprints collide")
		}
	})

	t.Run("radio shares and rules", func(t *testing.T) {
		_, err := store.CreateRadioShare("smoke-token", "Share")
		must(err)
		must(store.TouchRadioShareListen("smoke-token"))
		share, ok, err := store.GetActiveRadioShare("smoke-token")
		must(err)
		if !ok || share.ListenCount != 1 {
			t.Fatalf("share=%+v ok=%v", share, ok)
		}
		owner, err := store.RadioShareProfile(ctx, "smoke-token")
		if err != nil && err != ErrProfileNotFound {
			t.Fatal(err)
		}
		_ = owner // the fixture owner is disabled until bootstrap; both outcomes are valid
		rule, err := store.CreateRadioRule(RadioRule{TargetType: "artist", Action: "downrank", Scope: "global", TargetKey: "A"})
		must(err)
		must(store.UpdateRadioRule(RadioRule{ID: rule.ID, Strength: 0.25, ExpiresAt: "2099-01-01T00:00:00Z"}))
		got, err := store.GetRadioRule(rule.ID)
		must(err)
		if got == nil || got.Strength != 0.25 || got.ExpiresAt != "2099-01-01T00:00:00Z" {
			t.Fatalf("rule=%+v", got)
		}
	})

	t.Run("tags and preferences", func(t *testing.T) {
		tag, err := store.CreateCustomTag("Chill")
		must(err)
		must(store.TagTrack(tag.ID, 1))
		must(store.TagTrack(tag.ID, 1))
		tags, err := store.ListCustomTags()
		must(err)
		if len(tags) != 1 || tags[0].TrackCount != 1 {
			t.Fatalf("tags=%+v", tags)
		}
		must(store.UntagTrack(tag.ID, 1))
		must(store.FavAlbumAdd("Artist", "Album"))
		must(store.FavAlbumRemove("Artist", "Album"))
		if store.FavAlbumHas("Artist", "Album") {
			t.Fatal("album still favorite")
		}
	})

	t.Run("user playlists", func(t *testing.T) {
		pl, err := store.CreateUserPlaylist(UserPlaylist{Name: "Smoke", Type: "manual"})
		must(err)
		must(store.UpdateUserPlaylist(UserPlaylist{ID: pl.ID, Name: "Smoke 2", CoverTrackID: 1}))
		must(store.ReplacePlaylistItems(pl.ID, []UserPlaylistItem{
			{TrackID: 1},
			{UnresolvedArtist: "ARTIST", UnresolvedTitle: "title"},
			{UnresolvedPath: "/elsewhere/T-3.FLAC"},
		}))
		if id, ok, err := store.MatchTrack("", "", "/x/t_%.flac"); err != nil || ok {
			t.Fatalf("LIKE wildcards must match literally: id=%d ok=%v err=%v", id, ok, err)
		}
		dup, err := store.DuplicateUserPlaylist(pl.ID)
		must(err)
		if dup == nil || dup.Name != "Smoke 2 (копия)" || len(dup.Tracks) != 3 {
			t.Fatalf("duplicate=%+v", dup)
		}
		must(store.ArchiveUserPlaylist(pl.ID))
	})

	t.Run("jobs", func(t *testing.T) {
		id, err := store.EnqueueJob("embed", "")
		must(err)
		_, err = store.DB.Exec(`UPDATE jobs SET status = 'done', updated_at = ? WHERE id = ?`, "2099-01-01T00:00:00Z", id)
		must(err)
		done, err := store.ListDoneJobsAfter("2098-01-01T00:00:00Z", 5)
		must(err)
		if len(done) != 1 || done[0].ID != id {
			t.Fatalf("done=%+v", done)
		}
	})
}

func TestCatalogRowsEmbeddingsAndProfileStats(t *testing.T) {
	store, _, _ := openTestTarget(t)
	ctx := context.Background()
	insertTracks(t, store, 3)
	for id := 1; id <= 3; id++ {
		status := "ready"
		if id == 3 {
			status = "pending"
		}
		if _, err := store.DB.Exec(`INSERT INTO features(track_id, embedding, embedding_dim, status, computed_at) VALUES (?, ?, 1, ?, ?)`,
			id, []byte{0, 0, 128, 63}, status, fmt.Sprintf("2026-01-0%dT00:00:00Z", id)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.LoadCatalogRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].EmbeddingStamp != "2026-01-02T00:00:00Z" || rows[0].Embedding != nil {
		t.Fatalf("catalog rows=%+v", rows)
	}
	blobs, err := store.LoadEmbeddings([]int64{2, 3})
	if err != nil || len(blobs) != 2 || len(blobs[2]) != 4 {
		t.Fatalf("embeddings=%v err=%v", blobs, err)
	}

	_, other, err := store.CreateUserWithDefaultProfile(ctx, "Other listener")
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-02-01T00:00:00Z"
	if _, err := store.DB.Exec(`INSERT INTO rec_stats(profile_id, track_id, shown, skipped_early, completed, updated_at) VALUES (?, 1, 4, 1, 2, ?)`, store.ProfileID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO track_stats(profile_id, track_id, plays, finishes, early_skips, last_played_at, updated_at) VALUES (?, 1, 5, 3, 1, ?, ?), (?, 2, 9, 0, 0, ?, ?)`,
		store.ProfileID, now, now, other.ID, now, now); err != nil {
		t.Fatal(err)
	}
	stats, err := store.LoadTrackStats()
	if err != nil {
		t.Fatal(err)
	}
	if got := stats[1]; got.Shown != 4 || got.SkipEarly != 1 || got.Completed != 2 || got.Plays != 5 || got.Finishes != 3 || got.LastPlayedAt != now {
		t.Fatalf("stats[1]=%+v", got)
	}
	if _, leaked := stats[2]; leaked {
		t.Fatal("another profile's stats were loaded")
	}
}
