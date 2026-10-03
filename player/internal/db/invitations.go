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
	result, err := s.DB.ExecContext(ctx, `INSERT INTO invitations(id,secret_hash,created_by,issuer,email,created_at,expires_at)
 SELECT ?,?,?,NULLIF(?,''),NULLIF(?,''),?,? WHERE EXISTS
 (SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=? AND u.status='active' AND r.role='admin')`,
		id, secretHash(secret), adminID, issuer, strings.ToLower(strings.TrimSpace(email)), time.Now().UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339), adminID)
	if err != nil {
		return "", "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if n != 1 {
		return "", "", ErrInvitationInvalid
	}
	return id, secret, nil
}

// AcceptInvitation must receive claims from the OIDC verifier, never browser
// payloads. Provisioning and consuming the invite share one transaction.
func (s *Store) AcceptInvitation(ctx context.Context, secret string, identity VerifiedIdentity) (string, Profile, error) {
	if identity.Issuer == "" || identity.Subject == "" {
		return "", Profile{}, ErrInvitationInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", Profile{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	email := ""
	if identity.EmailVerified {
		email = strings.ToLower(strings.TrimSpace(identity.Email))
	}
	var inviteID string
	var targetUser sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,target_user_id FROM invitations WHERE secret_hash=? AND consumed_at IS NULL
 AND revoked_at IS NULL AND expires_at>? AND (issuer IS NULL OR issuer=?) AND (email IS NULL OR email=?)`,
		secretHash(secret), now, identity.Issuer, email).Scan(&inviteID, &targetUser)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Profile{}, ErrInvitationInvalid
	}
	if err != nil {
		return "", Profile{}, err
	}
	userID := uuid.NewString()
	p := Profile{ID: uuid.NewString(), Name: "Main", IsDefault: true}
	if targetUser.Valid {
		userID = targetUser.String
		result, err := tx.ExecContext(ctx, `UPDATE users SET status='active',display_name=?,updated_at=?
		 WHERE id=? AND status='disabled' AND id=(SELECT value FROM installation_state WHERE key='legacy_user_id')
		 AND NOT EXISTS(SELECT 1 FROM external_identities WHERE user_id=?)`, identity.DisplayName, now, userID, userID)
		if err != nil {
			return "", Profile{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", Profile{}, err
		}
		if n != 1 {
			return "", Profile{}, ErrInvitationInvalid
		}
		if err := tx.QueryRowContext(ctx, `SELECT id,name FROM profiles WHERE owner_user_id=? AND is_default=1 AND deleted_at IS NULL`, userID).Scan(&p.ID, &p.Name); err != nil {
			return "", Profile{}, err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,status,display_name,created_at,updated_at) VALUES (?,'active',?,?,?)`, userID, identity.DisplayName, now, now); err != nil {
			return "", Profile{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role) VALUES (?,'user')`, userID); err != nil {
			return "", Profile{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) VALUES (?,?,?,1,?,?)`, p.ID, userID, p.Name, now, now); err != nil {
			return "", Profile{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO external_identities(issuer,subject,user_id,created_at) VALUES (?,?,?,?)`, identity.Issuer, identity.Subject, userID, now); err != nil {
		return "", Profile{}, ErrInvitationInvalid
	}
	result, err := tx.ExecContext(ctx, `UPDATE invitations SET consumed_at=?,consumed_by=? WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL`, now, userID, inviteID)
	if err != nil {
		return "", Profile{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", Profile{}, err
	}
	if n != 1 {
		return "", Profile{}, ErrInvitationInvalid
	}
	if err = tx.Commit(); err != nil {
		return "", Profile{}, err
	}
	return userID, p, nil
}

// BootstrapInvitation is CLI-only. It activates the pending installation owner
// after OIDC verification; its existing profile retains all legacy data.
func (s *Store) BootstrapInvitation(ctx context.Context, issuer string, ttl time.Duration) (string, error) {
	if issuer == "" || ttl <= 0 {
		return "", ErrInvitationInvalid
	}
	secret, err := randomSecret()
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT u.id FROM users u JOIN installation_state i ON i.value=u.id AND i.key='legacy_user_id'
	 WHERE u.status='disabled' AND NOT EXISTS(SELECT 1 FROM external_identities WHERE user_id=u.id)
	 AND NOT EXISTS(SELECT 1 FROM users a JOIN user_roles r ON r.user_id=a.id WHERE a.status='active' AND r.role='admin')`).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvitationInvalid
	}
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE target_user_id=? AND consumed_at IS NULL AND revoked_at IS NULL`, now.Format(time.RFC3339), owner); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO invitations(id,secret_hash,issuer,created_at,expires_at,target_user_id) VALUES (?,?,?,?,?,?)`, uuid.NewString(), secretHash(secret), issuer, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339), owner); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
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
	result, err := s.DB.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash,user_id,active_profile_id,csrf_hash,created_at,expires_at)
 SELECT ?,u.id,p.id,?,?,? FROM users u JOIN profiles p ON p.owner_user_id=u.id
 WHERE u.id=? AND u.status='active' AND p.is_default=1 AND p.deleted_at IS NULL`,
		secretHash(token), secretHash(csrf), now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339), userID)
	if err != nil {
		return "", "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if n != 1 {
		return "", "", ErrProfileNotFound
	}
	return token, csrf, nil
}

func (s *Store) RevokeUserSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339), secretHash(token))
	return err
}

func (s *Store) UserSession(ctx context.Context, token string) (string, Profile, error) {
	return s.ProfileForSession(ctx, secretHash(token))
}

// IdentityUser never looks up by email: a different issuer/subject is a
// different identity even when the provider reports the same email address.
func (s *Store) IdentityUser(ctx context.Context, issuer, subject string) (string, error) {
	var userID string
	err := s.DB.QueryRowContext(ctx, `SELECT u.id FROM external_identities i
	 JOIN users u ON u.id=i.user_id WHERE i.issuer=? AND i.subject=? AND u.status='active'`, issuer, subject).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrIdentityUnknown
	}
	return userID, err
}

// LinkExternalIdentity adds a verified OIDC identity only to the user whose
// still-live session started the flow. The identity is never merged by email.
func (s *Store) LinkExternalIdentity(ctx context.Context, sessionToken, userID, issuer, subject string) error {
	if sessionToken == "" || userID == "" || issuer == "" || subject == "" {
		return ErrSessionInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM auth_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.user_id=? AND s.revoked_at IS NULL AND s.expires_at>? AND u.status='active'`,
		secretHash(sessionToken), userID, now).Scan(&active); err != nil {
		return err
	}
	if active != 1 {
		return ErrSessionInvalid
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM external_identities WHERE issuer=? AND subject=?`, issuer, subject).Scan(&existing)
	if err == nil {
		if existing == userID {
			return tx.Commit()
		}
		return ErrIdentityAlreadyLinked
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO external_identities(issuer,subject,user_id,created_at) VALUES (?,?,?,?)`, issuer, subject, userID, now); err != nil {
		// A concurrent link may have claimed the unique (issuer, subject) key.
		var claimedBy string
		if lookupErr := tx.QueryRowContext(ctx, `SELECT user_id FROM external_identities WHERE issuer=? AND subject=?`, issuer, subject).Scan(&claimedBy); lookupErr == nil {
			return ErrIdentityAlreadyLinked
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeInvitation(ctx context.Context, adminID, invitationID string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE invitations SET revoked_at=?
	 WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL AND EXISTS
	 (SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=? AND u.status='active' AND r.role='admin')`, time.Now().UTC().Format(time.RFC3339), invitationID, adminID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvitationInvalid
	}
	return nil
}

func (s *Store) BootstrapReady(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.status='active' AND r.role='admin')
	 + (SELECT count(*) FROM invitations WHERE target_user_id IS NOT NULL AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?)`, time.Now().UTC().Format(time.RFC3339)).Scan(&n)
	return n > 0, err
}

func (s *Store) ValidUserCSRF(ctx context.Context, token, csrf string) bool {
	if token == "" || csrf == "" {
		return false
	}
	var stored string
	err := s.DB.QueryRowContext(ctx, `SELECT csrf_hash FROM auth_sessions WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, secretHash(token), time.Now().UTC().Format(time.RFC3339)).Scan(&stored)
	return err == nil && subtle.ConstantTimeCompare([]byte(stored), []byte(secretHash(csrf))) == 1
}

func (s *Store) ActivateUserProfile(ctx context.Context, token, userID, profileID string) error {
	return s.ActivateProfile(ctx, secretHash(token), userID, profileID)
}

func (s *Store) RadioShareProfile(ctx context.Context, token string) (string, error) {
	var profileID string
	err := s.DB.QueryRowContext(ctx, `SELECT s.profile_id FROM radio_shares s JOIN profiles p ON p.id=s.profile_id JOIN users u ON u.id=p.owner_user_id WHERE s.token=? AND s.revoked_at IS NULL AND p.deleted_at IS NULL AND u.status='active'`, token).Scan(&profileID)
	return profileID, err
}
