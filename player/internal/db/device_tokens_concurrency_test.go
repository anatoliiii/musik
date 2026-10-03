package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestPostgresDeviceTokenIssueDuringDisableDoesNotSurviveReenable(t *testing.T) {
	databaseURL := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("requires a disposable PostgreSQL database")
	}
	for _, replace := range []bool{false, true} {
		name := "create"
		if replace {
			name = "replace"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			issuer, err := OpenDatabase(databaseURL, "")
			if err != nil {
				t.Fatal(err)
			}
			defer issuer.Close()
			adminStore, err := OpenDatabase(databaseURL, "")
			if err != nil {
				t.Fatal(err)
			}
			defer adminStore.Close()
			adminID, _, err := issuer.CreateUserWithDefaultProfile(ctx, "Token race admin")
			if err != nil {
				t.Fatal(err)
			}
			if err := issuer.ORM.Create(&UserRoleRecord{UserID: adminID, Role: "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			userID, profile, err := issuer.CreateUserWithDefaultProfile(ctx, "Token race owner")
			if err != nil {
				t.Fatal(err)
			}
			var old DeviceTokenInfo
			if replace {
				old, _, err = issuer.CreateDeviceToken(ctx, userID, profile.ID, "Phone")
				if err != nil {
					t.Fatal(err)
				}
			}

			// Pause the real repository after its owner check, just before INSERT.
			ready, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			const callback = "test:pause_device_token_insert"
			if err := issuer.ORM.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "device_tokens" {
					close(ready)
					select {
					case <-release:
					case <-ctx.Done():
						tx.AddError(ctx.Err())
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer issuer.ORM.Callback().Create().Remove(callback)
			type issued struct {
				secret string
				err    error
			}
			issueDone := make(chan issued, 1)
			go func() {
				var secret string
				var err error
				if replace {
					_, secret, err = issuer.ReplaceDeviceToken(ctx, userID, old.ID)
				} else {
					_, secret, err = issuer.CreateDeviceToken(ctx, userID, profile.ID, "Phone")
				}
				issueDone <- issued{secret, err}
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			disableDone := make(chan error, 1)
			go func() { disableDone <- adminStore.UpdateAccount(ctx, adminID, userID, "disabled", nil) }()

			// Wait until disable commits (the old create bug) or blocks on the
			// issuer transaction. No assumption about goroutine scheduling.
			disableFinished := false
			for {
				select {
				case err := <-disableDone:
					if err != nil {
						t.Fatal(err)
					}
					disableFinished = true
				default:
				}
				var blocked int
				if err := issuer.ORM.WithContext(ctx).Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%UPDATE "users"%'`).Scan(&blocked).Error; err != nil {
					t.Fatal(err)
				}
				// Replacement can also expose the old lock order by blocking
				// disable's token revocation after it updated the owner.
				if replace && blocked == 0 {
					if err := issuer.ORM.WithContext(ctx).Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%UPDATE "device_tokens"%'`).Scan(&blocked).Error; err != nil {
						t.Fatal(err)
					}
				}
				if disableFinished || blocked > 0 {
					break
				}
				select {
				case <-time.After(10 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			unblock()
			result := <-issueDone
			if result.err != nil {
				t.Fatal(result.err)
			}
			if !disableFinished {
				if err := <-disableDone; err != nil {
					t.Fatal(err)
				}
			}
			if err := adminStore.UpdateAccount(ctx, adminID, userID, "active", nil); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := issuer.AuthenticateDeviceToken(ctx, result.secret); !errors.Is(err, ErrDeviceTokenInvalid) {
				t.Fatalf("token issued concurrently with disable survived reenable: %v", err)
			}
		})
	}
}
