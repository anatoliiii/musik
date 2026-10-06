package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DeviceTokenLifetime = 180 * 24 * time.Hour
const deviceTokenTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func deviceTokenTimestamp(value time.Time) string {
	return value.UTC().Format(deviceTokenTimeLayout)
}

var ErrDeviceTokenInvalid = errors.New("device token is invalid or inactive")
var ErrDeviceTokenNotFound = errors.New("device token not found")
var ErrDeviceTokenNameRequired = errors.New("device name is required")

type DeviceTokenInfo struct {
	ID          string  `json:"id" db:"id"`
	UserID      string  `json:"user_id" db:"user_id"`
	ProfileID   string  `json:"profile_id" db:"profile_id"`
	ProfileName string  `json:"profile_name" db:"profile_name"`
	Name        string  `json:"name" db:"name"`
	CreatedAt   string  `json:"created_at" db:"created_at"`
	LastUsedAt  *string `json:"last_used_at,omitempty" db:"last_used_at"`
	ExpiresAt   string  `json:"expires_at" db:"expires_at"`
	RevokedAt   *string `json:"revoked_at,omitempty" db:"revoked_at"`
}

// CreateDeviceToken creates a fixed profile-bound token and returns its raw
// secret only to the caller that created it.
func (s *Store) CreateDeviceToken(ctx context.Context, userID, profileID, name string) (DeviceTokenInfo, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return DeviceTokenInfo{}, "", ErrDeviceTokenNameRequired
	}
	var info DeviceTokenInfo
	var secret string
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		var err error
		info, secret, err = createDeviceToken(ctx, tx, userID, profileID, name)
		return err
	})
	if err != nil {
		return DeviceTokenInfo{}, "", err
	}
	return info, secret, nil
}

// testHookBeforeDeviceTokenInsert lets tests pause token issue after the
// owner lock and profile check; it is always nil in production.
var testHookBeforeDeviceTokenInsert func(context.Context) error

func createDeviceToken(ctx context.Context, tx *Tx, userID, profileID, name string) (DeviceTokenInfo, string, error) {
	// Account disable updates the owner row before revoking its tokens.
	if err := lockActiveUser(ctx, tx, userID); err != nil {
		return DeviceTokenInfo{}, "", ErrProfileNotFound
	}
	query := `SELECT ` + profileColumns + ` FROM profiles WHERE owner_user_id = ? AND deleted_at IS NULL`
	args := []any{userID}
	if profileID != "" {
		query += ` AND id = ?`
		args = append(args, profileID)
	} else {
		query += ` ORDER BY is_default DESC, created_at, id LIMIT 1`
	}
	var profile ProfileRecord
	if err := tx.Get(ctx, &profile, query, args...); err != nil {
		return DeviceTokenInfo{}, "", ErrProfileNotFound
	}
	secret, err := randomSecret()
	if err != nil {
		return DeviceTokenInfo{}, "", err
	}
	now := time.Now().UTC()
	info := DeviceTokenInfo{
		ID: uuid.NewString(), UserID: userID, ProfileID: profile.ID,
		ProfileName: profile.Name, Name: name, CreatedAt: deviceTokenTimestamp(now),
		ExpiresAt: deviceTokenTimestamp(now.Add(DeviceTokenLifetime)),
	}
	if testHookBeforeDeviceTokenInsert != nil {
		if err := testHookBeforeDeviceTokenInsert(ctx); err != nil {
			return DeviceTokenInfo{}, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO device_tokens(id, secret_hash, user_id, profile_id, name, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		info.ID, secretHash(secret), userID, profile.ID, name, info.CreatedAt, info.ExpiresAt); err != nil {
		return DeviceTokenInfo{}, "", err
	}
	return info, secret, nil
}

func (s *Store) ListDeviceTokens(ctx context.Context, userID string) ([]DeviceTokenInfo, error) {
	tokens := []DeviceTokenInfo{}
	err := s.DB.Select(ctx, &tokens, `
SELECT d.id, d.user_id, d.profile_id, p.name AS profile_name, d.name, d.created_at,
       d.last_used_at, d.expires_at, d.revoked_at
FROM device_tokens d
JOIN profiles p ON p.id = d.profile_id AND p.owner_user_id = d.user_id
WHERE d.user_id = ?
ORDER BY d.created_at DESC, d.id`, userID)
	return tokens, err
}

func (s *Store) RevokeDeviceToken(ctx context.Context, userID, tokenID string) error {
	now := deviceTokenTimestamp(time.Now())
	result, err := s.DB.ExecContext(ctx, `
UPDATE device_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		now, tokenID, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 0 {
		return nil
	}
	var count int
	if err := s.DB.Get(ctx, &count, `SELECT COUNT(*) FROM device_tokens WHERE id = ? AND user_id = ?`, tokenID, userID); err != nil {
		return err
	}
	if count == 0 {
		return ErrDeviceTokenNotFound
	}
	return nil
}

// ReplaceDeviceToken revokes the selected token and creates its replacement
// in the same transaction. The replacement secret is returned once.
func (s *Store) ReplaceDeviceToken(ctx context.Context, userID, tokenID string) (DeviceTokenInfo, string, error) {
	var info DeviceTokenInfo
	var secret string
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		// Use the same owner-before-token lock order as account disable.
		if err := lockActiveUser(ctx, tx, userID); err != nil {
			return err
		}
		var old DeviceTokenRecord
		err := tx.Get(ctx, &old, `
SELECT id, secret_hash, user_id, profile_id, name, created_at, last_used_at, expires_at, revoked_at
FROM device_tokens WHERE id = ? AND user_id = ? AND revoked_at IS NULL`+tx.Dialect.LockForUpdate(),
			tokenID, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeviceTokenNotFound
		} else if err != nil {
			return err
		}
		info, secret, err = createDeviceToken(ctx, tx, userID, old.ProfileID, old.Name)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
UPDATE device_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
			deviceTokenTimestamp(time.Now()), tokenID, userID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrDeviceTokenNotFound
		}
		return nil
	})
	if err != nil {
		return DeviceTokenInfo{}, "", err
	}
	return info, secret, nil
}

func (s *Store) AuthenticateDeviceToken(ctx context.Context, secret string) (string, Profile, string, error) {
	if secret == "" {
		return "", Profile{}, "", ErrDeviceTokenInvalid
	}
	var row struct {
		ID        string `db:"id"`
		UserID    string `db:"user_id"`
		ProfileID string `db:"profile_id"`
		Name      string `db:"profile_name"`
		IsDefault int    `db:"is_default"`
	}
	now := deviceTokenTimestamp(time.Now())
	hash := secretHash(secret)
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		err := tx.Get(ctx, &row, `
SELECT d.id, d.user_id, d.profile_id, p.name AS profile_name, p.is_default
FROM device_tokens d
JOIN users u ON u.id = d.user_id
JOIN profiles p ON p.id = d.profile_id AND p.owner_user_id = d.user_id
WHERE d.secret_hash = ? AND d.revoked_at IS NULL AND d.expires_at > ?
  AND u.status = 'active' AND p.deleted_at IS NULL`, hash, now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeviceTokenInvalid
		}
		if err != nil {
			return err
		}
		if s.ReadOnly {
			// Validate replicated token/account/profile without writing usage
			// metadata. Primary authentication retains the conditional update.
			return nil
		}
		result, err := tx.ExecContext(ctx, `
UPDATE device_tokens SET last_used_at = ?
WHERE id = ? AND secret_hash = ? AND revoked_at IS NULL AND expires_at > ?`, now, row.ID, hash, now)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrDeviceTokenInvalid
		}
		return nil
	})
	if err != nil {
		return "", Profile{}, "", err
	}
	return row.UserID, Profile{ID: row.ProfileID, Name: row.Name, IsDefault: row.IsDefault != 0}, row.ID, nil
}

func revokeUserDeviceTokens(ctx context.Context, tx *Tx, userID, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE device_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	return err
}
