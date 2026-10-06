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
	var rows []struct {
		ID          string `db:"id"`
		DisplayName string `db:"display_name"`
		Status      string `db:"status"`
		Admin       int    `db:"admin"`
		Profiles    int    `db:"profiles"`
	}
	err := s.DB.Select(ctx, &rows, `
SELECT u.id, u.display_name, u.status,
       (SELECT COUNT(*) FROM user_roles r WHERE r.user_id = u.id AND r.role = 'admin') AS admin,
       (SELECT COUNT(*) FROM profiles p WHERE p.owner_user_id = u.id AND p.deleted_at IS NULL) AS profiles
FROM users u
ORDER BY u.created_at, u.id`)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, Account{
			ID: row.ID, DisplayName: row.DisplayName, Status: row.Status,
			Admin: row.Admin > 0, Profiles: row.Profiles,
		})
	}
	return out, nil
}

// activeAdminCount counts active administrators, or only userID when given.
func activeAdminCount(ctx context.Context, tx *Tx, userID string) (int, error) {
	query := `SELECT COUNT(*) FROM users u JOIN user_roles r ON r.user_id = u.id
WHERE u.status = 'active' AND r.role = 'admin'`
	var args []any
	if userID != "" {
		query += ` AND u.id = ?`
		args = append(args, userID)
	}
	var n int
	err := tx.Get(ctx, &n, query, args...)
	return n, err
}

func (s *Store) UpdateAccount(ctx context.Context, adminID, userID, status string, admin *bool) error {
	if status != "" && status != "active" && status != "disabled" {
		return errors.New("invalid user status")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.DB.InTx(ctx, func(tx *Tx) error {
		// Lock a shared invariant row before reading or changing administrator
		// roles. Locking either target user would allow two transactions that
		// update different administrators to pass the same count check.
		var guard string
		if err := tx.Get(ctx, &guard, `SELECT value FROM installation_state WHERE key = 'active_admin_guard'`+tx.Dialect.LockForUpdate()); err != nil {
			return err
		}
		if n, err := activeAdminCount(ctx, tx, adminID); err != nil {
			return err
		} else if n != 1 {
			return ErrUserNotFound
		}
		var exists string
		if err := tx.Get(ctx, &exists, `SELECT id FROM users WHERE id = ?`, userID); errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		if status != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET status = ?, updated_at = ? WHERE id = ?`, status, now, userID); err != nil {
				return err
			}
		}
		if admin != nil {
			if *admin {
				if _, err := tx.ExecContext(ctx, `INSERT INTO user_roles(user_id, role) VALUES (?, 'admin') ON CONFLICT DO NOTHING`, userID); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ? AND role = 'admin'`, userID); err != nil {
				return err
			}
		}
		if n, err := activeAdminCount(ctx, tx, ""); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
		if status == "disabled" {
			if err := revokeUserDeviceTokens(ctx, tx, userID, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) {
	var rows []InvitationRecord
	if err := s.DB.Select(ctx, &rows, `SELECT `+invitationColumns+` FROM invitations ORDER BY created_at DESC, id`); err != nil {
		return nil, err
	}
	out := make([]Invitation, 0, len(rows))
	for _, row := range rows {
		invitation := Invitation{ID: row.ID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
		if row.Email != nil {
			invitation.Email = *row.Email
		}
		if row.ConsumedAt != nil {
			invitation.ConsumedAt = *row.ConsumedAt
		}
		if row.RevokedAt != nil {
			invitation.RevokedAt = *row.RevokedAt
		}
		out = append(out, invitation)
	}
	return out, nil
}
