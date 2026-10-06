package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrProfileNotFound = errors.New("profile not found")
var ErrProfileRequired = errors.New("last profile cannot be deleted")

type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

func insertUserWithProfile(ctx context.Context, tx *Tx, userID, displayName, profileID, profileName, now string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO users(id, status, display_name, created_at, updated_at) VALUES (?, 'active', ?, ?, ?)`,
		userID, displayName, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_roles(user_id, role) VALUES (?, 'user')`, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		profileID, userID, profileName, now, now)
	return err
}

// CreateUserWithDefaultProfile is used by trusted provisioning code. The user
// and its first profile become visible together, never as partial records.
func (s *Store) CreateUserWithDefaultProfile(ctx context.Context, displayName string) (string, Profile, error) {
	userID := uuid.NewString()
	profile := Profile{ID: uuid.NewString(), Name: "Main", IsDefault: true}
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		return insertUserWithProfile(ctx, tx, userID, strings.TrimSpace(displayName), profile.ID, profile.Name, now)
	})
	if err != nil {
		return "", Profile{}, err
	}
	return userID, profile, nil
}

// lockActiveUser fails with ErrProfileNotFound unless userID is active. On
// PostgreSQL the row stays locked, ordering profile changes per user and
// against account disable.
func lockActiveUser(ctx context.Context, tx *Tx, userID string) error {
	var id string
	err := tx.Get(ctx, &id, `SELECT id FROM users WHERE id = ? AND status = 'active'`+tx.Dialect.LockForUpdate(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProfileNotFound
	}
	return err
}

func (s *Store) CreateProfile(ctx context.Context, userID, name string) (Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Profile{}, errors.New("profile name is required")
	}
	p := Profile{ID: uuid.NewString(), Name: name}
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		if err := lockActiveUser(ctx, tx, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
			p.ID, userID, p.Name, now, now)
		return err
	})
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (s *Store) ListProfiles(ctx context.Context, userID string) ([]Profile, error) {
	var rows []ProfileRecord
	err := s.DB.Select(ctx, &rows, `SELECT `+profileColumns+` FROM profiles
WHERE owner_user_id = ? AND deleted_at IS NULL ORDER BY is_default DESC, created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(rows))
	for _, row := range rows {
		profiles = append(profiles, Profile{ID: row.ID, Name: row.Name, IsDefault: row.IsDefault != 0})
	}
	return profiles, nil
}

// ActivateProfile changes only the caller's unexpired, unrevoked session.
// Foreign and missing profile IDs produce the same error.
func (s *Store) ActivateProfile(ctx context.Context, tokenHash, userID, profileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.DB.InTx(ctx, func(tx *Tx) error {
		var n int
		if err := tx.Get(ctx, &n, `
SELECT COUNT(*) FROM profiles p JOIN users u ON u.id = p.owner_user_id
WHERE p.id = ? AND p.owner_user_id = ? AND p.deleted_at IS NULL AND u.status = 'active'`,
			profileID, userID); err != nil {
			return err
		}
		if n != 1 {
			return ErrProfileNotFound
		}
		result, err := tx.ExecContext(ctx, `
UPDATE auth_sessions SET active_profile_id = ?
WHERE token_hash = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?`,
			profileID, tokenHash, userID, now)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrProfileNotFound
		}
		return nil
	})
}

// ProfileForSession reads a profile only after checking the session owner.
func (s *Store) ProfileForSession(ctx context.Context, tokenHash string) (string, Profile, error) {
	var row struct {
		UserID    string `db:"user_id"`
		ID        string `db:"profile_id"`
		Name      string `db:"name"`
		IsDefault int    `db:"is_default"`
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB.Get(ctx, &row, `
SELECT sessions.user_id, profiles.id AS profile_id, profiles.name, profiles.is_default
FROM auth_sessions sessions
JOIN profiles ON profiles.id = sessions.active_profile_id AND profiles.owner_user_id = sessions.user_id
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = ? AND sessions.revoked_at IS NULL AND sessions.expires_at > ?
  AND users.status = 'active' AND profiles.deleted_at IS NULL`, tokenHash, now)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Profile{}, ErrProfileNotFound
	}
	if err != nil {
		return "", Profile{}, err
	}
	return row.UserID, Profile{ID: row.ID, Name: row.Name, IsDefault: row.IsDefault != 0}, nil
}

func (s *Store) RenameProfile(ctx context.Context, userID, profileID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("profile name is required")
	}
	result, err := s.DB.ExecContext(ctx, `
UPDATE profiles SET name = ?, updated_at = ?
WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`,
		name, time.Now().UTC().Format(time.RFC3339), profileID, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrProfileNotFound
	}
	return nil
}

func (s *Store) DeleteProfile(ctx context.Context, userID, profileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.DB.InTx(ctx, func(tx *Tx) error {
		// Serialize profile deletion per owner so two concurrent deletes cannot
		// both pick the other profile as the surviving replacement.
		var owner string
		if err := tx.Get(ctx, &owner, `SELECT id FROM users WHERE id = ?`+tx.Dialect.LockForUpdate(), userID); errors.Is(err, sql.ErrNoRows) {
			return ErrProfileNotFound
		} else if err != nil {
			return err
		}
		var profile ProfileRecord
		if err := tx.Get(ctx, &profile, `SELECT `+profileColumns+` FROM profiles
WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, profileID, userID); errors.Is(err, sql.ErrNoRows) {
			return ErrProfileNotFound
		} else if err != nil {
			return err
		}
		var replacement ProfileRecord
		err := tx.Get(ctx, &replacement, `SELECT `+profileColumns+` FROM profiles
WHERE owner_user_id = ? AND id <> ? AND deleted_at IS NULL
ORDER BY is_default DESC, created_at, id LIMIT 1`, userID, profileID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProfileRequired
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE device_tokens SET revoked_at = ? WHERE user_id = ? AND profile_id = ? AND revoked_at IS NULL`,
			now, userID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE profiles SET deleted_at = ?, is_default = 0, updated_at = ? WHERE id = ? AND owner_user_id = ?`,
			now, now, profileID, userID); err != nil {
			return err
		}
		if profile.IsDefault != 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE profiles SET is_default = 1, updated_at = ? WHERE id = ? AND owner_user_id = ?`,
				now, replacement.ID, userID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `
UPDATE auth_sessions SET active_profile_id = ? WHERE user_id = ? AND active_profile_id = ?`,
			replacement.ID, userID, profileID)
		return err
	})
}

func (s *Store) UserIsAdmin(ctx context.Context, userID string) (bool, error) {
	var n int
	err := s.DB.Get(ctx, &n, `
SELECT COUNT(*) FROM users u JOIN user_roles r ON r.user_id = u.id
WHERE u.id = ? AND u.status = 'active' AND r.role = 'admin'`, userID)
	return n > 0, err
}

func (s *Store) UserInfo(ctx context.Context, userID string) (string, []string, error) {
	var displayName string
	if err := s.DB.Get(ctx, &displayName, `SELECT display_name FROM users WHERE id = ? AND status = 'active'`, userID); err != nil {
		return "", nil, err
	}
	roles := []string{}
	err := s.DB.Select(ctx, &roles, `SELECT role FROM user_roles WHERE user_id = ? ORDER BY role`, userID)
	return displayName, roles, err
}
