package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type RadioRule struct {
	ID         string  `json:"rule_id"`
	TargetType string  `json:"target_type"`
	Action     string  `json:"action"`
	Scope      string  `json:"scope"`
	TargetKey  string  `json:"target_key"`
	Strength   float64 `json:"strength"`
	SessionID  string  `json:"session_id,omitempty"`
	ContextID  string  `json:"context_id,omitempty"`
	ExpiresAt  string  `json:"expires_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
	ArchivedAt string  `json:"archived_at,omitempty"`
	Remaining  string  `json:"remaining,omitempty"`
}

func validRuleTarget(v string) bool {
	switch v {
	case "track", "song", "artist", "album", "genre", "cluster":
		return true
	default:
		return false
	}
}

func validRuleAction(v string) bool {
	switch v {
	case "block", "downrank", "cooldown":
		return true
	default:
		return false
	}
}

func validRuleScope(v string) bool {
	switch v {
	case "session", "context", "global":
		return true
	default:
		return false
	}
}

func (s *Store) CreateRadioRule(rule RadioRule) (RadioRule, error) {
	if rule.ID == "" {
		rule.ID = NewID()
	}
	if !validRuleTarget(rule.TargetType) || !validRuleAction(rule.Action) || !validRuleScope(rule.Scope) {
		return rule, fmt.Errorf("invalid target, action or scope")
	}
	rule.TargetKey = strings.TrimSpace(rule.TargetKey)
	if rule.TargetKey == "" {
		return rule, fmt.Errorf("target_key required")
	}
	if rule.Strength < 0 {
		return rule, fmt.Errorf("strength must be >= 0")
	}
	if rule.Strength == 0 {
		rule.Strength = 1
	}
	switch rule.Scope {
	case "session":
		if rule.SessionID == "" || rule.ContextID != "" {
			return rule, fmt.Errorf("session scope requires session_id")
		}
	case "context":
		if rule.ContextID == "" || rule.SessionID != "" {
			return rule, fmt.Errorf("context scope requires context_id")
		}
	default:
		rule.SessionID, rule.ContextID = "", ""
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rule.CreatedAt = now
	_, err := s.DB.Exec(`
INSERT INTO radio_rules(
  rule_id, target_type, action, scope, target_key, strength,
  session_id, context_id, expires_at, created_at
,profile_id) VALUES (?,?,?,?,?,?,?,?,?,?,:musik_profile)`,
		rule.ID, rule.TargetType, rule.Action, rule.Scope, rule.TargetKey, rule.Strength,
		nullStr(rule.SessionID), nullStr(rule.ContextID), nullStr(rule.ExpiresAt), rule.CreatedAt)
	return rule, err
}

func (s *Store) UpdateRadioRule(rule RadioRule) error {
	if rule.ID == "" {
		return fmt.Errorf("rule_id required")
	}
	now := time.Now().UTC()
	_, err := s.DB.Exec(`
UPDATE radio_rules SET
  strength = CASE WHEN ? > 0 THEN ? ELSE strength END,
  expires_at = CASE WHEN ? THEN ? ELSE expires_at END,
  archived_at = CASE WHEN ? THEN NULL ELSE archived_at END
WHERE radio_rules.profile_id=:musik_profile AND ( rule_id = ?) `,
		rule.Strength, rule.Strength,
		rule.ExpiresAt != "", nullStr(rule.ExpiresAt),
		rule.ArchivedAt == "undo",
		rule.ID)
	_ = now
	return err
}

func (s *Store) ArchiveRadioRule(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`UPDATE radio_rules SET archived_at = ? WHERE radio_rules.profile_id=:musik_profile AND ( rule_id = ? AND archived_at IS NULL) `, now, id)
	return err
}

func (s *Store) UndoLastRadioRule() (*RadioRule, error) {
	var id string
	err := s.DB.QueryRow(`
SELECT rule_id FROM (SELECT * FROM radio_rules WHERE profile_id=:musik_profile) AS radio_rules
WHERE archived_at IS NOT NULL
ORDER BY archived_at DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(`UPDATE radio_rules SET archived_at = NULL WHERE radio_rules.profile_id=:musik_profile AND ( rule_id = ?) `, id); err != nil {
		return nil, err
	}
	return s.GetRadioRule(id)
}

func (s *Store) GetRadioRule(id string) (*RadioRule, error) {
	row := s.DB.QueryRow(`
SELECT rule_id, target_type, action, scope, target_key, strength,
       COALESCE(session_id,''), COALESCE(context_id,''), COALESCE(expires_at,''),
       created_at, COALESCE(archived_at,'')
FROM (SELECT * FROM radio_rules WHERE profile_id=:musik_profile) AS radio_rules WHERE rule_id = ?`, id)
	return scanRadioRule(row)
}

func (s *Store) ListRadioRules(includeExpired bool) ([]RadioRule, error) {
	q := `
SELECT rule_id, target_type, action, scope, target_key, strength,
       COALESCE(session_id,''), COALESCE(context_id,''), COALESCE(expires_at,''),
       created_at, COALESCE(archived_at,'')
FROM (SELECT * FROM radio_rules WHERE profile_id=:musik_profile) AS radio_rules`
	if !includeExpired {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RadioRule
	now := time.Now().UTC()
	for rows.Next() {
		rule, err := scanRadioRule(rows)
		if err != nil {
			return nil, err
		}
		if rule == nil {
			continue
		}
		if !includeExpired && ruleExpired(*rule, now) {
			continue
		}
		rule.Remaining = remainingTime(*rule, now)
		out = append(out, *rule)
	}
	return out, rows.Err()
}

func (s *Store) ActiveRadioRules(sessionID string, contextIDs []string) ([]RadioRule, error) {
	all, err := s.ListRadioRules(false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	active := map[string]bool{}
	for _, id := range contextIDs {
		active[id] = true
	}
	var out []RadioRule
	for _, rule := range all {
		if ruleExpired(rule, now) {
			continue
		}
		switch rule.Scope {
		case "global":
			out = append(out, rule)
		case "session":
			if rule.SessionID == sessionID {
				out = append(out, rule)
			}
		case "context":
			if active[rule.ContextID] {
				out = append(out, rule)
			}
		}
	}
	return out, nil
}

type ruleScanner interface {
	Scan(dest ...any) error
}

func scanRadioRule(row ruleScanner) (*RadioRule, error) {
	var rule RadioRule
	if err := row.Scan(&rule.ID, &rule.TargetType, &rule.Action, &rule.Scope, &rule.TargetKey,
		&rule.Strength, &rule.SessionID, &rule.ContextID, &rule.ExpiresAt,
		&rule.CreatedAt, &rule.ArchivedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &rule, nil
}

func ruleExpired(rule RadioRule, now time.Time) bool {
	if rule.ArchivedAt != "" {
		return true
	}
	if rule.ExpiresAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339Nano, rule.ExpiresAt)
	if err != nil {
		exp, err = time.Parse(time.RFC3339, rule.ExpiresAt)
	}
	return err == nil && !exp.After(now)
}

func remainingTime(rule RadioRule, now time.Time) string {
	if rule.ExpiresAt == "" {
		if rule.ArchivedAt != "" {
			return "archived"
		}
		return "permanent"
	}
	exp, err := time.Parse(time.RFC3339Nano, rule.ExpiresAt)
	if err != nil {
		exp, err = time.Parse(time.RFC3339, rule.ExpiresAt)
	}
	if err != nil {
		return ""
	}
	if !exp.After(now) {
		return "expired"
	}
	return exp.Sub(now).Round(time.Second).String()
}
