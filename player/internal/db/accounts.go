package db

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
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
	var users []UserRecord
	err := s.ORM.WithContext(ctx).Order("created_at, id").Find(&users).Error
	if err != nil {
		return nil, err
	}
	var adminRoles []UserRoleRecord
	if err := s.ORM.WithContext(ctx).Where("role = ?", "admin").Find(&adminRoles).Error; err != nil {
		return nil, err
	}
	admins := make(map[string]bool, len(adminRoles))
	for _, role := range adminRoles {
		admins[role.UserID] = true
	}
	var counts []struct {
		OwnerUserID string `gorm:"column:owner_user_id"`
		Count       int    `gorm:"column:profile_count"`
	}
	if err := s.ORM.WithContext(ctx).Model(&ProfileRecord{}).
		Select("owner_user_id, count(*) AS profile_count").
		Where("deleted_at IS NULL").Group("owner_user_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	profiles := make(map[string]int, len(counts))
	for _, count := range counts {
		profiles[count.OwnerUserID] = count.Count
	}
	out := make([]Account, 0, len(users))
	for _, user := range users {
		out = append(out, Account{
			ID: user.ID, DisplayName: user.DisplayName, Status: user.Status,
			Admin: admins[user.ID], Profiles: profiles[user.ID],
		})
	}
	return out, nil
}

func (s *Store) UpdateAccount(ctx context.Context, adminID, userID, status string, admin *bool) error {
	if status != "" && status != "active" && status != "disabled" {
		return errors.New("invalid user status")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var adminCount int64
		if err := tx.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("users.id = ? AND users.status = ? AND user_roles.role = ?", adminID, "active", "admin").
			Count(&adminCount).Error; err != nil {
			return err
		}
		if adminCount != 1 {
			return ErrUserNotFound
		}
		var user UserRecord
		if err := tx.Where("id = ?", userID).Take(&user).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		} else if err != nil {
			return err
		}
		if status != "" {
			if err := tx.Model(&UserRecord{}).Where("id = ?", userID).
				Updates(map[string]any{"status": status, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if admin != nil {
			role := UserRoleRecord{UserID: userID, Role: "admin"}
			if *admin {
				if err := tx.Where(&role).FirstOrCreate(&role).Error; err != nil {
					return err
				}
			} else if err := tx.Where(&role).Delete(&UserRoleRecord{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("users.status = ? AND user_roles.role = ?", "active", "admin").Count(&adminCount).Error; err != nil {
			return err
		}
		if adminCount == 0 {
			return ErrLastAdmin
		}
		if status == "disabled" {
			if err := tx.Model(&AuthSessionRecord{}).Where("user_id = ? AND revoked_at IS NULL", userID).
				Updates(map[string]any{"revoked_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListInvitations(ctx context.Context) ([]Invitation, error) {
	var rows []InvitationRecord
	if err := s.ORM.WithContext(ctx).Order("created_at DESC, id").Find(&rows).Error; err != nil {
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
