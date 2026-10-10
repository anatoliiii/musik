package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvitationInvalid = errors.New("invitation invalid")
var ErrIdentityUnknown = errors.New("identity unknown")
var ErrIdentityAlreadyLinked = errors.New("identity already linked")
var ErrSessionInvalid = errors.New("session invalid")

// LocalIssuer marks a local account: the subject is its username.
const LocalIssuer = "local"

type VerifiedIdentity struct {
	Issuer        string
	PasswordHash  string // local accounts only
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
	err = s.DB.InTx(ctx, func(tx *Tx) error {
		if n, err := activeAdminCount(ctx, tx, adminID); err != nil {
			return err
		} else if n != 1 {
			return ErrInvitationInvalid
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO invitations(id, secret_hash, created_by, issuer, email, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, secretHash(secret), adminID, nullStr(issuer), nullStr(strings.ToLower(strings.TrimSpace(email))),
			time.Now().UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
		return err
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
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		var invitation InvitationRecord
		err := tx.Get(ctx, &invitation, `SELECT `+invitationColumns+` FROM invitations
WHERE secret_hash = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?
  AND (issuer IS NULL OR issuer = ?) AND (email IS NULL OR email = ?)`+tx.Dialect.LockForUpdate(),
			secretHash(secret), now, identity.Issuer, email)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvitationInvalid
		} else if err != nil {
			return err
		}

		if invitation.TargetUserID != nil {
			userID = *invitation.TargetUserID
			var disabled, identities int
			if err := tx.Get(ctx, &disabled, `SELECT COUNT(*) FROM users WHERE id = ? AND status = 'disabled'`, userID); err != nil {
				return err
			}
			if err := tx.Get(ctx, &identities, `SELECT COUNT(*) FROM external_identities WHERE user_id = ?`, userID); err != nil {
				return err
			}
			if disabled != 1 || identities != 0 {
				return ErrInvitationInvalid
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET status = 'active', display_name = ?, updated_at = ? WHERE id = ?`,
				identity.DisplayName, now, userID); err != nil {
				return err
			}
			var defaultProfile ProfileRecord
			if err := tx.Get(ctx, &defaultProfile, `SELECT `+profileColumns+` FROM profiles
WHERE owner_user_id = ? AND is_default = 1 AND deleted_at IS NULL`, userID); err != nil {
				return ErrInvitationInvalid
			}
			profile = Profile{ID: defaultProfile.ID, Name: defaultProfile.Name, IsDefault: true}
		} else if err := insertUserWithProfile(ctx, tx, userID, identity.DisplayName, profile.ID, profile.Name, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO external_identities(issuer, subject, user_id, created_at, password_hash) VALUES (?, ?, ?, ?, ?)`,
			identity.Issuer, identity.Subject, userID, now, nullStr(identity.PasswordHash)); err != nil {
			return ErrInvitationInvalid
		}
		result, err := tx.ExecContext(ctx, `
UPDATE invitations SET consumed_at = ?, consumed_by = ?
WHERE id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, now, userID, invitation.ID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrInvitationInvalid
		}
		return nil
	})
	if err != nil {
		return "", Profile{}, err
	}
	return userID, profile, nil
}

// BootstrapInvitation activates the pending installation owner when it signs
// in (OIDC, or a local account when issuer is empty); its existing profile
// retains all legacy data.
func (s *Store) BootstrapInvitation(ctx context.Context, issuer string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", ErrInvitationInvalid
	}
	secret, err := randomSecret()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339)
	err = s.DB.InTx(ctx, func(tx *Tx) error {
		var ownerID string
		if err := tx.Get(ctx, &ownerID, `SELECT value FROM installation_state WHERE key = 'legacy_user_id'`); err != nil {
			return ErrInvitationInvalid
		}
		var owner string
		if err := tx.Get(ctx, &owner, `SELECT id FROM users WHERE id = ? AND status = 'disabled'`+tx.Dialect.LockForUpdate(), ownerID); err != nil {
			return ErrInvitationInvalid
		}
		var identities int
		if err := tx.Get(ctx, &identities, `SELECT COUNT(*) FROM external_identities WHERE user_id = ?`, ownerID); err != nil {
			return err
		}
		admins, err := activeAdminCount(ctx, tx, "")
		if err != nil {
			return err
		}
		if identities != 0 || admins != 0 {
			return ErrInvitationInvalid
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE invitations SET revoked_at = ?
WHERE target_user_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, createdAt, ownerID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO invitations(id, secret_hash, issuer, created_at, expires_at, target_user_id)
VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), secretHash(secret), nullStr(issuer), createdAt, now.Add(ttl).Format(time.RFC3339), ownerID)
		return err
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
	var profileIDs []string
	if err := s.DB.Select(ctx, &profileIDs, `
SELECT profiles.id FROM users
JOIN profiles ON profiles.owner_user_id = users.id
WHERE users.id = ? AND users.status = 'active' AND profiles.is_default = 1 AND profiles.deleted_at IS NULL`,
		userID); err != nil {
		return "", "", err
	}
	if len(profileIDs) != 1 {
		return "", "", ErrProfileNotFound
	}
	if _, err := s.DB.ExecContext(ctx, `
INSERT INTO auth_sessions(token_hash, user_id, active_profile_id, csrf_hash, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		secretHash(token), userID, profileIDs[0], secretHash(csrf),
		now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339)); err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

func (s *Store) RevokeUserSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), secretHash(token))
	return err
}

func (s *Store) UserSession(ctx context.Context, token string) (string, Profile, error) {
	return s.ProfileForSession(ctx, secretHash(token))
}

// IdentityUser never looks up by email: a different issuer/subject is a
// different identity even when the provider reports the same email address.
func (s *Store) IdentityUser(ctx context.Context, issuer, subject string) (string, error) {
	var userID string
	err := s.DB.Get(ctx, &userID, `
SELECT external_identities.user_id FROM external_identities
JOIN users ON users.id = external_identities.user_id
WHERE external_identities.issuer = ? AND external_identities.subject = ? AND users.status = 'active'`,
		issuer, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrIdentityUnknown
	}
	return userID, err
}

// LinkExternalIdentity links a provider identity only to the user whose
// still-live session started the flow.
func (s *Store) LinkExternalIdentity(ctx context.Context, sessionToken, userID, issuer, subject string) error {
	if sessionToken == "" || userID == "" || issuer == "" || subject == "" {
		return ErrSessionInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB.InTx(ctx, func(tx *Tx) error {
		var sessions int
		if err := tx.Get(ctx, &sessions, `
SELECT COUNT(*) FROM auth_sessions JOIN users ON users.id = auth_sessions.user_id
WHERE auth_sessions.token_hash = ? AND auth_sessions.user_id = ? AND auth_sessions.revoked_at IS NULL
  AND auth_sessions.expires_at > ? AND users.status = 'active'`,
			secretHash(sessionToken), userID, now); err != nil {
			return err
		}
		if sessions != 1 {
			return ErrSessionInvalid
		}
		var existing string
		err := tx.Get(ctx, &existing, `SELECT user_id FROM external_identities WHERE issuer = ? AND subject = ?`, issuer, subject)
		if err == nil {
			if existing == userID {
				return nil
			}
			return ErrIdentityAlreadyLinked
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO external_identities(issuer, subject, user_id, created_at) VALUES (?, ?, ?, ?)`,
			issuer, subject, userID, now)
		return err
	})
	if err != nil && !errors.Is(err, ErrSessionInvalid) && !errors.Is(err, ErrIdentityAlreadyLinked) {
		// A concurrent link may have won the unique key; report it as such.
		var claimed string
		lookupErr := s.DB.Get(ctx, &claimed, `SELECT user_id FROM external_identities WHERE issuer = ? AND subject = ?`, issuer, subject)
		if lookupErr == nil && claimed != userID {
			return ErrIdentityAlreadyLinked
		}
	}
	return err
}

func (s *Store) RevokeInvitation(ctx context.Context, adminID, invitationID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.DB.InTx(ctx, func(tx *Tx) error {
		if n, err := activeAdminCount(ctx, tx, adminID); err != nil {
			return err
		} else if n != 1 {
			return ErrInvitationInvalid
		}
		result, err := tx.ExecContext(ctx, `
UPDATE invitations SET revoked_at = ? WHERE id = ? AND consumed_at IS NULL AND revoked_at IS NULL`,
			now, invitationID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrInvitationInvalid
		}
		return nil
	})
}

func (s *Store) BootstrapReady(ctx context.Context) (bool, error) {
	var ready int
	err := s.DB.Get(ctx, &ready, `
SELECT
  (SELECT COUNT(*) FROM users u JOIN user_roles r ON r.user_id = u.id
   WHERE u.status = 'active' AND r.role = 'admin')
+ (SELECT COUNT(*) FROM invitations
   WHERE target_user_id IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?)`,
		time.Now().UTC().Format(time.RFC3339))
	return ready > 0, err
}

func (s *Store) ValidUserCSRF(ctx context.Context, token, csrf string) bool {
	if token == "" || csrf == "" {
		return false
	}
	var csrfHash string
	err := s.DB.Get(ctx, &csrfHash, `
SELECT csrf_hash FROM auth_sessions WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?`,
		secretHash(token), time.Now().UTC().Format(time.RFC3339))
	return err == nil && subtle.ConstantTimeCompare([]byte(csrfHash), []byte(secretHash(csrf))) == 1
}

func (s *Store) ActivateUserProfile(ctx context.Context, token, userID, profileID string) error {
	return s.ActivateProfile(ctx, secretHash(token), userID, profileID)
}

func (s *Store) RadioShareProfile(ctx context.Context, token string) (string, error) {
	var profileID string
	err := s.DB.Get(ctx, &profileID, `
SELECT shares.profile_id FROM radio_shares shares
JOIN profiles ON profiles.id = shares.profile_id
JOIN users ON users.id = profiles.owner_user_id
WHERE shares.token = ? AND shares.revoked_at IS NULL AND profiles.deleted_at IS NULL AND users.status = 'active'`,
		token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProfileNotFound
	}
	return profileID, err
}

// LocalPassword returns the active user and password hash of a local
// account, found by username or, when username is empty, by userID.
func (s *Store) LocalPassword(ctx context.Context, username, userID string) (string, string, error) {
	var row struct {
		UserID string         `db:"user_id"`
		Hash   sql.NullString `db:"password_hash"`
	}
	err := s.DB.Get(ctx, &row, `
SELECT external_identities.user_id, external_identities.password_hash FROM external_identities
JOIN users ON users.id = external_identities.user_id
WHERE external_identities.issuer = ? AND (external_identities.subject = ? OR (? = '' AND external_identities.user_id = ?))
  AND users.status = 'active'`, LocalIssuer, username, username, userID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !row.Hash.Valid) {
		return "", "", ErrIdentityUnknown
	}
	return row.UserID, row.Hash.String, err
}

// SetLocalPassword replaces the password of the user's local account.
func (s *Store) SetLocalPassword(ctx context.Context, userID, hash string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE external_identities SET password_hash = ? WHERE issuer = ? AND user_id = ?`, hash, LocalIssuer, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrIdentityUnknown
	}
	return nil
}
