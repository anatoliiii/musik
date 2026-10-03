package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeviceTokensBindProfilesExpireAndRevoke(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	userA, mainProfile, err := store.CreateUserWithDefaultProfile(ctx, "Device owner")
	if err != nil {
		t.Fatal(err)
	}
	secondProfile, err := store.CreateProfile(ctx, userA, "Second")
	if err != nil {
		t.Fatal(err)
	}
	userB, _, err := store.CreateUserWithDefaultProfile(ctx, "Other owner")
	if err != nil {
		t.Fatal(err)
	}

	phone, phoneSecret, err := store.CreateDeviceToken(ctx, userA, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if phone.ProfileID != mainProfile.ID {
		t.Fatalf("new token metadata=%+v, want default profile", phone)
	}
	tablet, tabletSecret, err := store.CreateDeviceToken(ctx, userA, secondProfile.ID, "Tablet")
	if err != nil {
		t.Fatal(err)
	}
	if tablet.ProfileID != secondProfile.ID || tabletSecret == phoneSecret {
		t.Fatalf("second token=%+v secret_equal=%v", tablet, tabletSecret == phoneSecret)
	}
	if _, _, err := store.CreateDeviceToken(ctx, userA, "foreign-profile", "Foreign"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("foreign profile creation error=%v", err)
	}
	if _, _, err := store.CreateDeviceToken(ctx, userB, mainProfile.ID, "Foreign"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("other user's profile creation error=%v", err)
	}

	user, profile, tokenID, err := store.AuthenticateDeviceToken(ctx, phoneSecret)
	if err != nil || user != userA || profile.ID != mainProfile.ID || profile.Name != mainProfile.Name || tokenID != phone.ID {
		t.Fatalf("phone auth=%q/%+v/%q err=%v", user, profile, tokenID, err)
	}
	var lastUsed string
	if err := store.ORM.Model(&DeviceTokenRecord{}).Select("last_used_at").Where("id = ?", phone.ID).Scan(&lastUsed).Error; err != nil || lastUsed == "" {
		t.Fatalf("last_used_at=%q err=%v", lastUsed, err)
	}

	replacement, replacementSecret, err := store.ReplaceDeviceToken(ctx, userA, phone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == phone.ID || replacement.ProfileID != mainProfile.ID || replacementSecret == phoneSecret {
		t.Fatalf("replacement=%+v reused_id=%v reused_secret=%v", replacement, replacement.ID == phone.ID, replacementSecret == phoneSecret)
	}
	if _, _, _, err := store.AuthenticateDeviceToken(ctx, phoneSecret); !errors.Is(err, ErrDeviceTokenInvalid) {
		t.Fatalf("replaced token error=%v", err)
	}
	if _, profile, _, err := store.AuthenticateDeviceToken(ctx, replacementSecret); err != nil || profile.ID != mainProfile.ID {
		t.Fatalf("replacement auth profile=%+v err=%v", profile, err)
	}

	if err := store.ORM.Model(&DeviceTokenRecord{}).Where("id = ?", tablet.ID).
		Update("expires_at", deviceTokenTimestamp(time.Now().Add(-time.Minute))).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.AuthenticateDeviceToken(ctx, tabletSecret); !errors.Is(err, ErrDeviceTokenInvalid) {
		t.Fatalf("expired token error=%v", err)
	}

	if err := store.DeleteProfile(ctx, userA, secondProfile.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.AuthenticateDeviceToken(ctx, tabletSecret); !errors.Is(err, ErrDeviceTokenInvalid) {
		t.Fatalf("deleted-profile token error=%v", err)
	}

	if _, err := store.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, userB); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccount(ctx, userB, userA, "disabled", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.AuthenticateDeviceToken(ctx, replacementSecret); !errors.Is(err, ErrDeviceTokenInvalid) {
		t.Fatalf("disabled-user token error=%v", err)
	}

	tokens, err := store.ListDeviceTokens(ctx, userA)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 {
		t.Fatalf("listed device tokens=%+v, want original, replacement, and deleted-profile token", tokens)
	}
	for _, token := range tokens {
		if token.ID == "" || token.ProfileName == "" {
			t.Fatalf("incomplete or secret-bearing list result: %+v", token)
		}
	}
}
