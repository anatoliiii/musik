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

// CreateUserWithDefaultProfile is used by trusted provisioning code. The user
// and its first profile become visible together, never as partial records.
func (s *Store) CreateUserWithDefaultProfile(ctx context.Context, displayName string) (string, Profile, error) {
	userID := uuid.NewString()
	profile := Profile{ID: uuid.NewString(), Name: "Main", IsDefault: true}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", Profile{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,status,display_name,created_at,updated_at)
		VALUES (?,'active',?,?,?)`, userID, strings.TrimSpace(displayName), now, now); err != nil {
		return "", Profile{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role) VALUES (?,'user')`, userID); err != nil {
		return "", Profile{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at)
		VALUES (?,?,?,1,?,?)`, profile.ID, userID, profile.Name, now, now); err != nil {
		return "", Profile{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", Profile{}, err
	}
	return userID, profile, nil
}

func (s *Store) CreateProfile(ctx context.Context, userID, name string) (Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Profile{}, errors.New("profile name is required")
	}
	p := Profile{ID: uuid.NewString(), Name: name}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.DB.ExecContext(ctx, `INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at)
		SELECT ?,id,?,0,?,? FROM users WHERE id=? AND status='active'`, p.ID, p.Name, now, now, userID)
	if err != nil {
		return Profile{}, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return Profile{}, err
	} else if n != 1 {
		return Profile{}, ErrProfileNotFound
	}
	return p, nil
}

func (s *Store) ListProfiles(ctx context.Context, userID string) ([]Profile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,is_default FROM profiles
		WHERE owner_user_id=? AND deleted_at IS NULL ORDER BY is_default DESC, created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		var p Profile
		var isDefault int
		if err := rows.Scan(&p.ID, &p.Name, &isDefault); err != nil {
			return nil, err
		}
		p.IsDefault = isDefault != 0
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// ActivateProfile changes only the caller's unexpired, unrevoked session.
// Foreign and missing profile IDs produce the same error.
func (s *Store) ActivateProfile(ctx context.Context, tokenHash, userID, profileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.DB.ExecContext(ctx, `UPDATE auth_sessions SET active_profile_id=?
		WHERE token_hash=? AND user_id=? AND revoked_at IS NULL AND expires_at>?
		AND EXISTS (SELECT 1 FROM profiles p JOIN users u ON u.id=p.owner_user_id
		WHERE p.id=? AND p.owner_user_id=? AND p.deleted_at IS NULL AND u.status='active')`,
		profileID, tokenHash, userID, now, profileID, userID)
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

// ProfileForSession reads a profile only after checking the session owner.
func (s *Store) ProfileForSession(ctx context.Context, tokenHash string) (string, Profile, error) {
	var userID string
	var p Profile
	var isDefault int
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB.QueryRowContext(ctx, `SELECT s.user_id,p.id,p.name,p.is_default
		FROM auth_sessions s JOIN profiles p ON p.id=s.active_profile_id AND p.owner_user_id=s.user_id
		JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.revoked_at IS NULL AND s.expires_at>?
		AND u.status='active' AND p.deleted_at IS NULL`, tokenHash, now).Scan(&userID, &p.ID, &p.Name, &isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Profile{}, ErrProfileNotFound
	}
	if err != nil {
		return "", Profile{}, err
	}
	p.IsDefault = isDefault != 0
	return userID, p, nil
}

func (s *Store) RenameProfile(ctx context.Context, userID, profileID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("profile name is required")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE profiles SET name=?,updated_at=? WHERE id=? AND owner_user_id=? AND deleted_at IS NULL`, name, time.Now().UTC().Format(time.RFC3339), profileID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrProfileNotFound
	}
	return nil
}

func (s *Store) DeleteProfile(ctx context.Context, userID, profileID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var isDefault int
	err = tx.QueryRowContext(ctx, `SELECT is_default FROM profiles WHERE id=? AND owner_user_id=? AND deleted_at IS NULL`, profileID, userID).Scan(&isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProfileNotFound
	}
	if err != nil {
		return err
	}
	var replacement string
	err = tx.QueryRowContext(ctx, `SELECT id FROM profiles WHERE owner_user_id=? AND id<>? AND deleted_at IS NULL ORDER BY is_default DESC,created_at,id LIMIT 1`, userID, profileID).Scan(&replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProfileRequired
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx, `UPDATE profiles SET deleted_at=?,is_default=0,updated_at=? WHERE id=? AND owner_user_id=?`, now, now, profileID, userID); err != nil {
		return err
	}
	if isDefault == 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE profiles SET is_default=1,updated_at=? WHERE id=? AND owner_user_id=?`, now, replacement, userID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET active_profile_id=? WHERE user_id=? AND active_profile_id=?`, replacement, userID, profileID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UserIsAdmin(ctx context.Context, userID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=? AND u.status='active' AND r.role='admin'`, userID).Scan(&n)
	return n > 0, err
}

func (s *Store) UserInfo(ctx context.Context, userID string) (string, []string, error) {
	var name string
	if err := s.DB.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id=? AND status='active'`, userID).Scan(&name); err != nil {
		return "", nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT role FROM user_roles WHERE user_id=? ORDER BY role`, userID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	roles := []string{}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return "", nil, err
		}
		roles = append(roles, role)
	}
	return name, roles, rows.Err()
}
