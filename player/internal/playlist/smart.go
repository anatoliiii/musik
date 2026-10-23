package playlist

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/torwin-job/musik/player/internal/db"
)

const RuleSchemaVersion = 1

type Rule struct {
	SchemaVersion int      `json:"schema_version"`
	Limit         int      `json:"limit,omitempty"`
	Sort          string   `json:"sort,omitempty"`
	All           []Clause `json:"all,omitempty"`
	Any           []Clause `json:"any,omitempty"`
}

type Clause struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

func ParseRule(raw string) (Rule, error) {
	var rule Rule
	if strings.TrimSpace(raw) == "" {
		return rule, fmt.Errorf("rule_json required")
	}
	if err := json.Unmarshal([]byte(raw), &rule); err != nil {
		return rule, fmt.Errorf("invalid rule json")
	}
	if rule.SchemaVersion != RuleSchemaVersion {
		return rule, fmt.Errorf("unsupported rule schema_version")
	}
	for _, clause := range append(append([]Clause{}, rule.All...), rule.Any...) {
		if err := validateClause(clause); err != nil {
			return rule, err
		}
	}
	if rule.Limit < 0 {
		rule.Limit = 0
	}
	return rule, nil
}

func validateClause(c Clause) error {
	switch c.Field {
	case "tag", "artist", "album", "liked", "plays", "last_played", "year", "bpm", "lufs", "duration":
	default:
		return fmt.Errorf("unsupported field %q", c.Field)
	}
	switch c.Op {
	case "eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "between",
		"older_than_days", "newer_than_days":
	default:
		return fmt.Errorf("unsupported op %q", c.Op)
	}
	return nil
}

func (r Rule) JSON() (string, error) {
	r.SchemaVersion = RuleSchemaVersion
	b, err := json.Marshal(r)
	return string(b), err
}

// Evaluate runs the rule. The SQL, including number and timestamp predicates,
// lives in db so both engines share one dialect.
func Evaluate(ctx context.Context, store *db.Store, rule Rule) ([]int64, error) {
	return store.SmartTrackIDs(ctx, db.SmartRule{
		Limit: rule.Limit,
		Sort:  rule.Sort,
		All:   smartClauses(rule.All),
		Any:   smartClauses(rule.Any),
	})
}

func smartClauses(in []Clause) []db.SmartClause {
	if len(in) == 0 {
		return nil
	}
	out := make([]db.SmartClause, len(in))
	for i, clause := range in {
		out[i] = db.SmartClause{Field: clause.Field, Op: clause.Op, Value: clause.Value}
	}
	return out
}
