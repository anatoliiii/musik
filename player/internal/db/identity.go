package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user := UserRecord{ID: userID, Status: "active", DisplayName: strings.TrimSpace(displayName), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := tx.Create(&UserRoleRecord{UserID: userID, Role: "user"}).Error; err != nil {
			return err
		}
		row := ProfileRecord{ID: profile.ID, OwnerUserID: userID, Name: profile.Name, IsDefault: 1, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&row).Error
	})
	if err != nil {
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
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user UserRecord
		err := tx.Where("id = ? AND status = ?", userID, "active").Take(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProfileNotFound
		}
		if err != nil {
			return err
		}
		row := ProfileRecord{ID: p.ID, OwnerUserID: userID, Name: p.Name, IsDefault: 0, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&row).Error
	})
	if errors.Is(err, ErrProfileNotFound) {
		return Profile{}, ErrProfileNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (s *Store) ListProfiles(ctx context.Context, userID string) ([]Profile, error) {
	var rows []ProfileRecord
	err := s.ORM.WithContext(ctx).Where("owner_user_id = ? AND deleted_at IS NULL", userID).
		Order("is_default DESC, created_at, id").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0)
	for _, row := range rows {
		profiles = append(profiles, Profile{ID: row.ID, Name: row.Name, IsDefault: row.IsDefault != 0})
	}
	return profiles, nil
}

// ActivateProfile changes only the caller's unexpired, unrevoked session.
// Foreign and missing profile IDs produce the same error.
func (s *Store) ActivateProfile(ctx context.Context, tokenHash, userID, profileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var profile ProfileRecord
		if err := tx.Where("id = ? AND owner_user_id = ? AND deleted_at IS NULL", profileID, userID).Take(&profile).Error; err != nil {
			return ErrProfileNotFound
		}
		var user UserRecord
		if err := tx.Where("id = ? AND status = ?", userID, "active").Take(&user).Error; err != nil {
			return ErrProfileNotFound
		}
		result := tx.Model(&AuthSessionRecord{}).
			Where("token_hash = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", tokenHash, userID, now).
			Updates(map[string]any{"active_profile_id": profileID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProfileNotFound
		}
		return nil
	})
}

// ProfileForSession reads a profile only after checking the session owner.
func (s *Store) ProfileForSession(ctx context.Context, tokenHash string) (string, Profile, error) {
	var result struct {
		UserID    string `gorm:"column:user_id"`
		ID        string `gorm:"column:profile_id"`
		Name      string `gorm:"column:name"`
		IsDefault int    `gorm:"column:is_default"`
	}
	now := time.Now().UTC().Format(time.RFC3339)
	query := s.ORM.WithContext(ctx).Table("auth_sessions AS sessions").
		Select("sessions.user_id, profiles.id AS profile_id, profiles.name, profiles.is_default").
		Joins("JOIN profiles ON profiles.id = sessions.active_profile_id AND profiles.owner_user_id = sessions.user_id").
		Joins("JOIN users ON users.id = sessions.user_id").
		Where("sessions.token_hash = ? AND sessions.revoked_at IS NULL AND sessions.expires_at > ? AND users.status = ? AND profiles.deleted_at IS NULL", tokenHash, now, "active").
		Scan(&result)
	if query.Error != nil {
		return "", Profile{}, query.Error
	}
	if query.RowsAffected != 1 {
		return "", Profile{}, ErrProfileNotFound
	}
	p := Profile{ID: result.ID, Name: result.Name, IsDefault: result.IsDefault != 0}
	return result.UserID, p, nil
}

func (s *Store) RenameProfile(ctx context.Context, userID, profileID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("profile name is required")
	}
	result := s.ORM.WithContext(ctx).Model(&ProfileRecord{}).
		Where("id = ? AND owner_user_id = ? AND deleted_at IS NULL", profileID, userID).
		Updates(map[string]any{"name": name, "updated_at": time.Now().UTC().Format(time.RFC3339)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrProfileNotFound
	}
	return nil
}

func (s *Store) DeleteProfile(ctx context.Context, userID, profileID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var profile ProfileRecord
		if err := tx.Where("id = ? AND owner_user_id = ? AND deleted_at IS NULL", profileID, userID).Take(&profile).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProfileNotFound
		} else if err != nil {
			return err
		}
		var replacement ProfileRecord
		err := tx.Where("owner_user_id = ? AND id <> ? AND deleted_at IS NULL", userID, profileID).
			Order("is_default DESC, created_at, id").Take(&replacement).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProfileRequired
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&ProfileRecord{}).Where("id = ? AND owner_user_id = ?", profileID, userID).
			Updates(map[string]any{"deleted_at": now, "is_default": 0, "updated_at": now}).Error; err != nil {
			return err
		}
		if profile.IsDefault != 0 {
			if err := tx.Model(&ProfileRecord{}).Where("id = ? AND owner_user_id = ?", replacement.ID, userID).
				Updates(map[string]any{"is_default": 1, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&AuthSessionRecord{}).Where("user_id = ? AND active_profile_id = ?", userID, profileID).
			Updates(map[string]any{"active_profile_id": replacement.ID}).Error
	})
}

func (s *Store) UserIsAdmin(ctx context.Context, userID string) (bool, error) {
	var n int64
	err := s.ORM.WithContext(ctx).Model(&UserRecord{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Where("users.id = ? AND users.status = ? AND user_roles.role = ?", userID, "active", "admin").
		Count(&n).Error
	return n > 0, err
}

func (s *Store) UserInfo(ctx context.Context, userID string) (string, []string, error) {
	var user UserRecord
	if err := s.ORM.WithContext(ctx).Where("id = ? AND status = ?", userID, "active").Take(&user).Error; err != nil {
		return "", nil, err
	}
	roles := []string{}
	err := s.ORM.WithContext(ctx).Model(&UserRoleRecord{}).
		Where("user_id = ?", user.ID).Order("role").Pluck("role", &roles).Error
	return user.DisplayName, roles, err
}
