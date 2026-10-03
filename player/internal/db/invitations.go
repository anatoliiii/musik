package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvitationInvalid = errors.New("invitation invalid")
var ErrIdentityUnknown = errors.New("identity unknown")
var ErrIdentityAlreadyLinked = errors.New("identity already linked")
var ErrSessionInvalid = errors.New("session invalid")

type VerifiedIdentity struct {
	Issuer        string
	Subject       string
	DisplayName   string
	Email         string
	EmailVerified bool
}

func secretHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// SessionFingerprint binds a pending OIDC link flow to the exact authenticated
// browser session without retaining the raw session secret in OIDC state.
func SessionFingerprint(token string) string { return secretHash(token) }

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func invitationPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// CreateInvitation returns the secret once. Only its hash is persisted.
func (s *Store) CreateInvitation(ctx context.Context, adminID, issuer, email string, expires time.Time) (string, string, error) {
	if !expires.After(time.Now()) {
		return "", "", ErrInvitationInvalid
	}
	secret, err := randomSecret()
	if err != nil {
		return "", "", err
	}
	id := uuid.NewString()
	createdBy := adminID
	row := InvitationRecord{
		ID: id, SecretHash: secretHash(secret), CreatedBy: &createdBy,
		Issuer: invitationPointer(issuer), Email: invitationPointer(strings.ToLower(strings.TrimSpace(email))),
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: expires.UTC().Format(time.RFC3339),
	}
	err = s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admins int64
		if err := tx.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("users.id = ? AND users.status = ? AND user_roles.role = ?", adminID, "active", "admin").
			Count(&admins).Error; err != nil {
			return err
		}
		if admins != 1 {
			return ErrInvitationInvalid
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return "", "", err
	}
	return id, secret, nil
}

// AcceptInvitation provisions a user and consumes the invite in one transaction.
func (s *Store) AcceptInvitation(ctx context.Context, secret string, identity VerifiedIdentity) (string, Profile, error) {
	if identity.Issuer == "" || identity.Subject == "" {
		return "", Profile{}, ErrInvitationInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339)
	email := ""
	if identity.EmailVerified {
		email = strings.ToLower(strings.TrimSpace(identity.Email))
	}
	userID := uuid.NewString()
	profile := Profile{ID: uuid.NewString(), Name: "Main", IsDefault: true}
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where(
			"secret_hash = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ? AND (issuer IS NULL OR issuer = ?) AND (email IS NULL OR email = ?)",
			secretHash(secret), now, identity.Issuer, email,
		)
		if s.Dialect == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var invitation InvitationRecord
		if err := query.Take(&invitation).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvitationInvalid
		} else if err != nil {
			return err
		}

		if invitation.TargetUserID != nil {
			userID = *invitation.TargetUserID
			var user UserRecord
			if err := tx.Where("id = ? AND status = ?", userID, "disabled").Take(&user).Error; err != nil {
				return ErrInvitationInvalid
			}
			var identityCount int64
			if err := tx.Model(&ExternalIdentityRecord{}).Where("user_id = ?", userID).Count(&identityCount).Error; err != nil {
				return err
			}
			if identityCount != 0 {
				return ErrInvitationInvalid
			}
			if err := tx.Model(&UserRecord{}).Where("id = ?", userID).
				Updates(map[string]any{"status": "active", "display_name": identity.DisplayName, "updated_at": now}).Error; err != nil {
				return err
			}
			var defaultProfile ProfileRecord
			if err := tx.Where("owner_user_id = ? AND is_default = 1 AND deleted_at IS NULL", userID).Take(&defaultProfile).Error; err != nil {
				return ErrInvitationInvalid
			}
			profile = Profile{ID: defaultProfile.ID, Name: defaultProfile.Name, IsDefault: true}
		} else {
			user := UserRecord{ID: userID, Status: "active", DisplayName: identity.DisplayName, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			if err := tx.Create(&UserRoleRecord{UserID: userID, Role: "user"}).Error; err != nil {
				return err
			}
			newProfile := ProfileRecord{ID: profile.ID, OwnerUserID: userID, Name: profile.Name, IsDefault: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&newProfile).Error; err != nil {
				return err
			}
		}
		linkedIdentity := ExternalIdentityRecord{Issuer: identity.Issuer, Subject: identity.Subject, UserID: userID, CreatedAt: now}
		if err := tx.Create(&linkedIdentity).Error; err != nil {
			return ErrInvitationInvalid
		}
		result := tx.Model(&InvitationRecord{}).
			Where("id = ? AND consumed_at IS NULL AND revoked_at IS NULL", invitation.ID).
			Updates(map[string]any{"consumed_at": now, "consumed_by": userID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvitationInvalid
		}
		return nil
	})
	if err != nil {
		return "", Profile{}, err
	}
	return userID, profile, nil
}

// BootstrapInvitation activates the pending installation owner after OIDC
// verification; its existing profile retains all legacy data.
func (s *Store) BootstrapInvitation(ctx context.Context, issuer string, ttl time.Duration) (string, error) {
	if issuer == "" || ttl <= 0 {
		return "", ErrInvitationInvalid
	}
	secret, err := randomSecret()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339)
	var ownerID string
	err = s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var installation InstallationStateRecord
		if err := tx.Where("key = ?", "legacy_user_id").Take(&installation).Error; err != nil {
			return ErrInvitationInvalid
		}
		ownerID = installation.Value
		var owner UserRecord
		ownerQuery := tx.Where("id = ? AND status = ?", ownerID, "disabled")
		if s.Dialect == "postgres" {
			ownerQuery = ownerQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := ownerQuery.Take(&owner).Error; err != nil {
			return ErrInvitationInvalid
		}
		var identities, admins int64
		if err := tx.Model(&ExternalIdentityRecord{}).Where("user_id = ?", ownerID).Count(&identities).Error; err != nil {
			return err
		}
		if err := tx.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("users.status = ? AND user_roles.role = ?", "active", "admin").Count(&admins).Error; err != nil {
			return err
		}
		if identities != 0 || admins != 0 {
			return ErrInvitationInvalid
		}
		if err := tx.Model(&InvitationRecord{}).
			Where("target_user_id = ? AND consumed_at IS NULL AND revoked_at IS NULL", ownerID).
			Updates(map[string]any{"revoked_at": createdAt}).Error; err != nil {
			return err
		}
		targetUserID := ownerID
		row := InvitationRecord{
			ID: uuid.NewString(), SecretHash: secretHash(secret), Issuer: &issuer,
			CreatedAt: createdAt, ExpiresAt: now.Add(ttl).Format(time.RFC3339),
			TargetUserID: &targetUserID,
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Store) IssueUserSession(ctx context.Context, userID string, ttl time.Duration) (string, string, error) {
	if ttl <= 0 {
		return "", "", errors.New("session TTL must be positive")
	}
	token, err := randomSecret()
	if err != nil {
		return "", "", err
	}
	csrf, err := randomSecret()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	timestr := now.Format(time.RFC3339)
	var principal struct {
		UserID    string `gorm:"column:user_id"`
		ProfileID string `gorm:"column:profile_id"`
	}
	query := s.ORM.WithContext(ctx).Table("users").
		Select("users.id AS user_id, profiles.id AS profile_id").
		Joins("JOIN profiles ON profiles.owner_user_id = users.id").
		Where("users.id = ? AND users.status = ? AND profiles.is_default = 1 AND profiles.deleted_at IS NULL", userID, "active").
		Scan(&principal)
	if query.Error != nil {
		return "", "", query.Error
	}
	if query.RowsAffected != 1 {
		return "", "", ErrProfileNotFound
	}
	row := AuthSessionRecord{
		TokenHash: secretHash(token), UserID: userID, ActiveProfileID: principal.ProfileID,
		CSRFHash: secretHash(csrf), CreatedAt: timestr, ExpiresAt: now.Add(ttl).Format(time.RFC3339),
	}
	if err := s.ORM.WithContext(ctx).Create(&row).Error; err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

func (s *Store) RevokeUserSession(ctx context.Context, token string) error {
	return s.ORM.WithContext(ctx).Model(&AuthSessionRecord{}).
		Where("token_hash = ? AND revoked_at IS NULL", secretHash(token)).
		Updates(map[string]any{"revoked_at": time.Now().UTC().Format(time.RFC3339)}).Error
}

func (s *Store) UserSession(ctx context.Context, token string) (string, Profile, error) {
	return s.ProfileForSession(ctx, secretHash(token))
}

// IdentityUser never looks up by email: a different issuer/subject is a
// different identity even when the provider reports the same email address.
func (s *Store) IdentityUser(ctx context.Context, issuer, subject string) (string, error) {
	var identity ExternalIdentityRecord
	err := s.ORM.WithContext(ctx).Table("external_identities").
		Select("external_identities.user_id").
		Joins("JOIN users ON users.id = external_identities.user_id").
		Where("external_identities.issuer = ? AND external_identities.subject = ? AND users.status = ?", issuer, subject, "active").
		Take(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrIdentityUnknown
	}
	return identity.UserID, err
}

// LinkExternalIdentity links a provider identity only to the user whose
// still-live session started the flow.
func (s *Store) LinkExternalIdentity(ctx context.Context, sessionToken, userID, issuer, subject string) error {
	if sessionToken == "" || userID == "" || issuer == "" || subject == "" {
		return ErrSessionInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sessions int64
		if err := tx.Model(&AuthSessionRecord{}).Joins("JOIN users ON users.id = auth_sessions.user_id").
			Where("auth_sessions.token_hash = ? AND auth_sessions.user_id = ? AND auth_sessions.revoked_at IS NULL AND auth_sessions.expires_at > ? AND users.status = ?", secretHash(sessionToken), userID, now, "active").
			Count(&sessions).Error; err != nil {
			return err
		}
		if sessions != 1 {
			return ErrSessionInvalid
		}
		var existing ExternalIdentityRecord
		err := tx.Where("issuer = ? AND subject = ?", issuer, subject).Take(&existing).Error
		if err == nil {
			if existing.UserID == userID {
				return nil
			}
			return ErrIdentityAlreadyLinked
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&ExternalIdentityRecord{Issuer: issuer, Subject: subject, UserID: userID, CreatedAt: now}).Error
	})
	if err != nil && !errors.Is(err, ErrSessionInvalid) && !errors.Is(err, ErrIdentityAlreadyLinked) {
		var claimed ExternalIdentityRecord
		lookupErr := s.ORM.WithContext(ctx).Where("issuer = ? AND subject = ?", issuer, subject).Take(&claimed).Error
		if lookupErr == nil && claimed.UserID != userID {
			return ErrIdentityAlreadyLinked
		}
	}
	return err
}

func (s *Store) RevokeInvitation(ctx context.Context, adminID, invitationID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.ORM.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admins int64
		if err := tx.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("users.id = ? AND users.status = ? AND user_roles.role = ?", adminID, "active", "admin").
			Count(&admins).Error; err != nil {
			return err
		}
		if admins != 1 {
			return ErrInvitationInvalid
		}
		result := tx.Model(&InvitationRecord{}).
			Where("id = ? AND consumed_at IS NULL AND revoked_at IS NULL", invitationID).
			Updates(map[string]any{"revoked_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvitationInvalid
		}
		return nil
	})
}

func (s *Store) BootstrapReady(ctx context.Context) (bool, error) {
	var admins, invitations int64
	db := s.ORM.WithContext(ctx)
	if err := db.Model(&UserRecord{}).Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Where("users.status = ? AND user_roles.role = ?", "active", "admin").Count(&admins).Error; err != nil {
		return false, err
	}
	if err := db.Model(&InvitationRecord{}).
		Where("target_user_id IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?", time.Now().UTC().Format(time.RFC3339)).
		Count(&invitations).Error; err != nil {
		return false, err
	}
	return admins+invitations > 0, nil
}

func (s *Store) ValidUserCSRF(ctx context.Context, token, csrf string) bool {
	if token == "" || csrf == "" {
		return false
	}
	var session AuthSessionRecord
	err := s.ORM.WithContext(ctx).Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", secretHash(token), time.Now().UTC().Format(time.RFC3339)).Take(&session).Error
	return err == nil && subtle.ConstantTimeCompare([]byte(session.CSRFHash), []byte(secretHash(csrf))) == 1
}

func (s *Store) ActivateUserProfile(ctx context.Context, token, userID, profileID string) error {
	return s.ActivateProfile(ctx, secretHash(token), userID, profileID)
}

func (s *Store) RadioShareProfile(ctx context.Context, token string) (string, error) {
	var result struct {
		ProfileID string `gorm:"column:profile_id"`
	}
	err := s.ORM.WithContext(ctx).Table("radio_shares AS shares").
		Select("shares.profile_id").
		Joins("JOIN profiles ON profiles.id = shares.profile_id").
		Joins("JOIN users ON users.id = profiles.owner_user_id").
		Where("shares.token = ? AND shares.revoked_at IS NULL AND profiles.deleted_at IS NULL AND users.status = ?", token, "active").
		Take(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrProfileNotFound
	}
	return result.ProfileID, err
}
