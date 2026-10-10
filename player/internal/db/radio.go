package db

import (
	"database/sql"
	"fmt"
	"time"
)

type RadioShare struct {
	Token        string  `json:"token"`
	Name         string  `json:"name"`
	CreatedAt    string  `json:"created_at"`
	RevokedAt    *string `json:"revoked_at,omitempty"`
	LastListenAt *string `json:"last_listen_at,omitempty"`
	ListenCount  int     `json:"listen_count"`
	Active       bool    `json:"active"`
}

func (s *Store) CreateRadioShare(token, name string) (RadioShare, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO radio_shares(token, name, created_at, listen_count, profile_id)
VALUES(?,?,?,0,?)`, token, name, now, s.ProfileID)
	if err != nil {
		return RadioShare{}, err
	}
	return RadioShare{Token: token, Name: name, CreatedAt: now, Active: true}, nil
}

func (s *Store) ListRadioShares(includeRevoked bool) ([]RadioShare, error) {
	q := `SELECT token, name, created_at, revoked_at, last_listen_at, listen_count
FROM radio_shares WHERE profile_id = ?`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	q += ` ORDER BY created_at DESC`
	return QueryAll(s.DB, func(rows *sql.Rows, sh *RadioShare) error {
		var revoked, last sql.NullString
		if err := rows.Scan(&sh.Token, &sh.Name, &sh.CreatedAt, &revoked, &last, &sh.ListenCount); err != nil {
			return err
		}
		if revoked.Valid {
			sh.RevokedAt = &revoked.String
		}
		if last.Valid {
			sh.LastListenAt = &last.String
		}
		sh.Active = !revoked.Valid
		return nil
	}, q, s.ProfileID)
}

func (s *Store) GetActiveRadioShare(token string) (RadioShare, bool, error) {
	var sh RadioShare
	var revoked, last sql.NullString
	err := s.DB.QueryRow(`
SELECT token, name, created_at, revoked_at, last_listen_at, listen_count
FROM radio_shares WHERE profile_id = ? AND token = ?`, s.ProfileID, token).Scan(
		&sh.Token, &sh.Name, &sh.CreatedAt, &revoked, &last, &sh.ListenCount)
	if err == sql.ErrNoRows {
		return RadioShare{}, false, nil
	}
	if err != nil {
		return RadioShare{}, false, err
	}
	if revoked.Valid {
		sh.RevokedAt = &revoked.String
		return sh, false, nil
	}
	if last.Valid {
		sh.LastListenAt = &last.String
	}
	sh.Active = true
	return sh, true, nil
}

func (s *Store) RevokeRadioShare(token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(`UPDATE radio_shares SET revoked_at = ? WHERE profile_id = ? AND token = ? AND revoked_at IS NULL`, now, s.ProfileID, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("not found")
	}
	return nil
}

func (s *Store) TouchRadioShareListen(token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
UPDATE radio_shares SET listen_count = listen_count + 1, last_listen_at = ?
WHERE profile_id = ? AND token = ? AND revoked_at IS NULL`, now, s.ProfileID, token)
	return err
}
