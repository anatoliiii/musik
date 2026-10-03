package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrLastAdmin = errors.New("at least one active administrator is required")
var ErrUserNotFound = errors.New("user not found")

type Account struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Admin       bool   `json:"admin"`
	Profiles    int    `json:"profiles"`
}
type Invitation struct {
	ID         string `json:"id"`
	Email      string `json:"email,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	ConsumedAt string `json:"consumed_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id,u.display_name,u.status,
 (SELECT count(*) FROM user_roles r WHERE r.user_id=u.id AND r.role='admin'),
 (SELECT count(*) FROM profiles p WHERE p.owner_user_id=u.id AND p.deleted_at IS NULL)
 FROM users u ORDER BY u.created_at,u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var admin int
		if err := rows.Scan(&a.ID, &a.DisplayName, &a.Status, &admin, &a.Profiles); err != nil {
			return nil, err
		}
		a.Admin = admin > 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAccount(ctx context.Context, adminID, userID, status string, admin *bool) error {
	if status != "" && status != "active" && status != "disabled" {
		return errors.New("invalid user status")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var permitted int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=? AND u.status='active' AND r.role='admin'`, adminID).Scan(&permitted); err != nil {
		return err
	}
	if permitted != 1 {
		return ErrUserNotFound
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, userID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	} else if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if status != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status=?,updated_at=? WHERE id=?`, status, now, userID); err != nil {
			return err
		}
	}
	if admin != nil {
		if *admin {
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role) VALUES (?,'admin') ON CONFLICT(user_id,role) DO NOTHING`, userID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id=? AND role='admin'`, userID); err != nil {
				return err
			}
		}
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.status='active' AND r.role='admin'`).Scan(&active); err != nil {
		return err
	}
	if active == 0 {
		return ErrLastAdmin
	}
	if status == "disabled" {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,COALESCE(email,''),created_at,expires_at,COALESCE(consumed_at,''),COALESCE(revoked_at,'') FROM invitations ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if err := rows.Scan(&i.ID, &i.Email, &i.CreatedAt, &i.ExpiresAt, &i.ConsumedAt, &i.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
