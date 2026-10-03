package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountAdministrationPreservesAnActiveAdmin(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	adminID, _, err := store.CreateUserWithDefaultProfile(ctx, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	userID, _, err := store.CreateUserWithDefaultProfile(ctx, "Member")
	if err != nil {
		t.Fatal(err)
	}
	backupAdminID, _, err := store.CreateUserWithDefaultProfile(ctx, "Backup admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, adminID); err != nil {
		t.Fatal(err)
	}
	userToken, _, err := store.IssueUserSession(ctx, userID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccount(ctx, adminID, adminID, "disabled", nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin disable error=%v", err)
	}
	if _, _, err := store.UserSession(ctx, userToken); err != nil {
		t.Fatalf("failed admin change modified another account: %v", err)
	}
	grant := true
	if err := store.UpdateAccount(ctx, adminID, userID, "", &grant); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccount(ctx, adminID, backupAdminID, "", &grant); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccount(ctx, adminID, userID, "disabled", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UserSession(ctx, userToken); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("disabled user's session survived: %v", err)
	}
	if err := store.UpdateAccount(ctx, adminID, adminID, "disabled", nil); err != nil {
		t.Fatalf("backup active admin should permit disable: %v", err)
	}
	if err := store.UpdateAccount(ctx, backupAdminID, backupAdminID, "disabled", nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last active admin disable error=%v", err)
	}
	accounts, err := store.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) < 3 || accounts[0].ID == "" || accounts[1].Profiles != 1 || accounts[2].Profiles != 1 {
		t.Fatalf("unexpected account list: %#v", accounts)
	}
}
