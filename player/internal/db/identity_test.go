package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProfilesStayWithTheirOwner(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	userA, defaultA, err := store.CreateUserWithDefaultProfile(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	userB, defaultB, err := store.CreateUserWithDefaultProfile(ctx, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	secondA, err := store.CreateProfile(ctx, userA, "Quiet")
	if err != nil {
		t.Fatal(err)
	}
	profilesA, err := store.ListProfiles(ctx, userA)
	if err != nil || len(profilesA) != 2 || profilesA[0].ID != defaultA.ID || profilesA[1].ID != secondA.ID {
		t.Fatalf("A profiles = %#v, %v", profilesA, err)
	}
	profilesB, err := store.ListProfiles(ctx, userB)
	if err != nil || len(profilesB) != 1 || profilesB[0].ID != defaultB.ID {
		t.Fatalf("B profiles = %#v, %v", profilesB, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := store.DB.Exec(`INSERT INTO auth_sessions
		(token_hash,user_id,active_profile_id,csrf_hash,created_at,expires_at)
		VALUES (?,?,?,?,?,?)`, "session-a", userA, defaultA.ID, "csrf", now, expires); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateProfile(ctx, "session-a", userA, defaultB.ID); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("foreign profile activation: %v", err)
	}
	if err := store.ActivateProfile(ctx, "session-a", userA, secondA.ID); err != nil {
		t.Fatal(err)
	}
	actualUser, active, err := store.ProfileForSession(ctx, "session-a")
	if err != nil || actualUser != userA || active.ID != secondA.ID {
		t.Fatalf("active = %q, %#v, %v", actualUser, active, err)
	}
	if _, err := store.DB.Exec(`UPDATE auth_sessions SET revoked_at=? WHERE token_hash=?`, now, "session-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ProfileForSession(ctx, "session-a"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("revoked session accepted: %v", err)
	}
}
