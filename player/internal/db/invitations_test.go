package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestInvitationIsAtomicAndSingleUse(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	admin, _, err := s.CreateUserWithDefaultProfile(ctx, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, admin); err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.CreateInvitation(ctx, admin, "https://issuer.test", "invite@example.test", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claims := VerifiedIdentity{Issuer: "https://issuer.test", Subject: "subject", DisplayName: "User", Email: "invite@example.test"}
	if _, _, err = s.AcceptInvitation(ctx, secret, claims); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("unverified email accepted: %v", err)
	}
	claims.EmailVerified = true
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, e := s.AcceptInvitation(ctx, secret, claims); results <- e }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrInvitationInvalid) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("successful consumers=%d", successes)
	}
	var users int
	if err = s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 2 {
		t.Fatalf("failed consumer left partial user: count=%d", users)
	}
	var owner string
	if err = s.DB.QueryRow(`SELECT user_id FROM external_identities WHERE issuer=? AND subject=?`, claims.Issuer, claims.Subject).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	token, csrf, err := s.IssueUserSession(ctx, owner, time.Hour)
	if err != nil || token == "" || csrf == "" {
		t.Fatalf("session: %v", err)
	}
	if got, _, err := s.UserSession(ctx, token); err != nil || got != owner {
		t.Fatalf("session principal=%q err=%v", got, err)
	}
	if err = s.RevokeUserSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.UserSession(ctx, token); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("revoked session accepted: %v", err)
	}
}

func TestInvitationRestrictionsAndIdentityKeys(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	admin, _, err := s.CreateUserWithDefaultProfile(ctx, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateInvitation(ctx, admin, "", "", time.Now().Add(time.Hour)); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("ordinary user issued invite: %v", err)
	}
	if _, err = s.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, admin); err != nil {
		t.Fatal(err)
	}
	id, secret, err := s.CreateInvitation(ctx, admin, "https://issuer.test", "", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claims := VerifiedIdentity{Issuer: "https://wrong.test", Subject: "subject", Email: "same@test", EmailVerified: true}
	if _, _, err := s.AcceptInvitation(ctx, secret, claims); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("wrong issuer accepted: %v", err)
	}
	if err := s.RevokeInvitation(ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	claims.Issuer = "https://issuer.test"
	if _, _, err := s.AcceptInvitation(ctx, secret, claims); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("revoked invitation accepted: %v", err)
	}
	var owners []string
	for _, subject := range []string{"one", "two"} {
		_, secret, err = s.CreateInvitation(ctx, admin, claims.Issuer, "same@test", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		claims.Subject = subject
		owner, _, err := s.AcceptInvitation(ctx, secret, claims)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
		got, err := s.IdentityUser(ctx, claims.Issuer, subject)
		if err != nil || got != owner {
			t.Fatalf("identity resolution: %q %v", got, err)
		}
	}
	if owners[0] == owners[1] {
		t.Fatal("same email silently linked two subjects")
	}
	token, _, err := s.IssueUserSession(ctx, owners[0], time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE users SET status='disabled' WHERE id=?`, owners[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UserSession(ctx, token); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("disabled user session accepted: %v", err)
	}
}
