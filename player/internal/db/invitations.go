package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	err = tx.QueryRowContext(ctx, `SELECT id FROM invitations WHERE secret_hash=? AND consumed_at IS NULL
 AND revoked_at IS NULL AND expires_at>? AND (issuer IS NULL OR issuer=?) AND (email IS NULL OR email=?)`,
		secretHash(secret), now, identity.Issuer, email).Scan(&inviteID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Profile{}, ErrInvitationInvalid
	}
	if err != nil {
		return "", Profile{}, err
	}
	userID := uuid.NewString()
	p := Profile{ID: uuid.NewString(), Name: "Main", IsDefault: true}
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,status,display_name,created_at,updated_at) VALUES (?,'active',?,?,?)`, userID, identity.DisplayName, now, now); err != nil {
		return "", Profile{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role) VALUES (?,'user')`, userID); err != nil {
		return "", Profile{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) VALUES (?,?,?,1,?,?)`, p.ID, userID, p.Name, now, now); err != nil {
		return "", Profile{}, err
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
