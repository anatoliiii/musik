package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	ID          string  `json:"id"`
	UserID      string  `json:"user_id"`
	ProfileID   string  `json:"profile_id"`
	ProfileName string  `json:"profile_name"`
	Name        string  `json:"name"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at,omitempty"`
	ExpiresAt   string  `json:"expires_at"`
	RevokedAt   *string `json:"revoked_at,omitempty"`
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
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		info, secret, err = s.createDeviceToken(tx, userID, profileID, name)
		return err
	})
	if err != nil {
		return DeviceTokenInfo{}, "", err
	}
	return info, secret, nil
}

func (s *Store) lockDeviceTokenOwner(tx *gorm.DB, userID string) error {
	query := tx.Where("id = ? AND status = ?", userID, "active")
	if s.Dialect == "postgres" {
		// Account disable updates this row before revoking its tokens.
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var user UserRecord
	if err := query.Take(&user).Error; err != nil {
		return ErrProfileNotFound
	}
	return nil
}

func (s *Store) createDeviceToken(tx *gorm.DB, userID, profileID, name string) (DeviceTokenInfo, string, error) {
	if err := s.lockDeviceTokenOwner(tx, userID); err != nil {
		return DeviceTokenInfo{}, "", ErrProfileNotFound
	}
	query := tx.Model(&ProfileRecord{}).Where("owner_user_id = ? AND deleted_at IS NULL", userID)
	if profileID != "" {
		query = query.Where("id = ?", profileID)
	} else {
		query = query.Order("is_default DESC, created_at, id")
	}
	var profile ProfileRecord
	if err := query.Take(&profile).Error; err != nil {
		return DeviceTokenInfo{}, "", ErrProfileNotFound
	}
	secret, err := randomSecret()
	if err != nil {
		return DeviceTokenInfo{}, "", err
	}
	now := time.Now().UTC()
	createdAt := deviceTokenTimestamp(now)
	info := DeviceTokenInfo{
		ID: uuid.NewString(), UserID: userID, ProfileID: profile.ID,
		ProfileName: profile.Name, Name: name, CreatedAt: createdAt,
		ExpiresAt: deviceTokenTimestamp(now.Add(DeviceTokenLifetime)),
	}
	row := DeviceTokenRecord{
		ID: info.ID, SecretHash: secretHash(secret), UserID: userID,
		ProfileID: profile.ID, Name: name, CreatedAt: info.CreatedAt,
		ExpiresAt: info.ExpiresAt,
	}
	if err := tx.Create(&row).Error; err != nil {
		return DeviceTokenInfo{}, "", err
	}
	return info, secret, nil
}

func (s *Store) ListDeviceTokens(ctx context.Context, userID string) ([]DeviceTokenInfo, error) {
	var tokens []DeviceTokenInfo
	err := s.ORM.WithContext(ctx).Table("device_tokens AS d").
		Select("d.id, d.user_id, d.profile_id, p.name AS profile_name, d.name, d.created_at, d.last_used_at, d.expires_at, d.revoked_at").
		Joins("JOIN profiles AS p ON p.id = d.profile_id AND p.owner_user_id = d.user_id").
		Where("d.user_id = ?", userID).
		Order("d.created_at DESC, d.id").Scan(&tokens).Error
	if tokens == nil && err == nil {
		tokens = []DeviceTokenInfo{}
	}
	return tokens, err
}

func (s *Store) RevokeDeviceToken(ctx context.Context, userID, tokenID string) error {
	now := deviceTokenTimestamp(time.Now())
	result := s.ORM.WithContext(ctx).Model(&DeviceTokenRecord{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", tokenID, userID).
		Update("revoked_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 0 {
		return nil
	}
	var count int64
	if err := s.ORM.WithContext(ctx).Model(&DeviceTokenRecord{}).
		Where("id = ? AND user_id = ?", tokenID, userID).Count(&count).Error; err != nil {
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
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Use the same owner-before-token lock order as account disable.
		if err := s.lockDeviceTokenOwner(tx, userID); err != nil {
			return err
		}
		query := tx.Where("id = ? AND user_id = ? AND revoked_at IS NULL", tokenID, userID)
		if s.Dialect == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var old DeviceTokenRecord
		if err := query.Take(&old).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeviceTokenNotFound
		} else if err != nil {
			return err
		}
		var err error
		info, secret, err = s.createDeviceToken(tx, userID, old.ProfileID, old.Name)
		if err != nil {
			return err
		}
		now := deviceTokenTimestamp(time.Now())
		result := tx.Model(&DeviceTokenRecord{}).
			Where("id = ? AND user_id = ? AND revoked_at IS NULL", tokenID, userID).
			Update("revoked_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
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
	var userID, profileID, profileName, tokenID string
	var isDefault int
	now := deviceTokenTimestamp(time.Now())
	hash := secretHash(secret)
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID        string `gorm:"column:id"`
			UserID    string `gorm:"column:user_id"`
			ProfileID string `gorm:"column:profile_id"`
			Name      string `gorm:"column:profile_name"`
			IsDefault int    `gorm:"column:is_default"`
		}
		queryErr := tx.Table("device_tokens AS d").
			Select("d.id, d.user_id, d.profile_id, p.name AS profile_name, p.is_default").
			Joins("JOIN users AS u ON u.id = d.user_id").
			Joins("JOIN profiles AS p ON p.id = d.profile_id AND p.owner_user_id = d.user_id").
			Where("d.secret_hash = ? AND d.revoked_at IS NULL AND d.expires_at > ? AND u.status = ? AND p.deleted_at IS NULL", hash, now, "active").
			Take(&row).Error
		if errors.Is(queryErr, gorm.ErrRecordNotFound) {
			return ErrDeviceTokenInvalid
		}
		if queryErr != nil {
			return queryErr
		}
		result := tx.Model(&DeviceTokenRecord{}).
			Where("id = ? AND secret_hash = ? AND revoked_at IS NULL AND expires_at > ?", row.ID, hash, now).
			Update("last_used_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDeviceTokenInvalid
		}
		tokenID, userID, profileID, profileName, isDefault = row.ID, row.UserID, row.ProfileID, row.Name, row.IsDefault
		return nil
	})
	if err != nil {
		return "", Profile{}, "", err
	}
	return userID, Profile{ID: profileID, Name: profileName, IsDefault: isDefault != 0}, tokenID, nil
}

func (s *Store) RevokeUserDeviceTokens(ctx context.Context, tx *gorm.DB, userID, now string) error {
	return tx.WithContext(ctx).Model(&DeviceTokenRecord{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}
