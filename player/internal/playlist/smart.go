package playlist

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

func Evaluate(store *db.Store, rule Rule) ([]int64, error) {
	q := `
SELECT t.id
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
LEFT JOIN track_stats ts ON ts.track_id = t.id
LEFT JOIN track_preferences pref ON pref.track_id = t.id
LEFT JOIN favorites fav ON fav.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL`
	args := []any{}
	allSQL, allArgs, err := compileGroup(rule.All, "AND")
	if err != nil {
		return nil, err
	}
	anySQL, anyArgs, err := compileGroup(rule.Any, "OR")
	if err != nil {
		return nil, err
	}
	if allSQL != "" {
		q += " AND (" + allSQL + ")"
		args = append(args, allArgs...)
	}
	if anySQL != "" {
		q += " AND (" + anySQL + ")"
		args = append(args, anyArgs...)
	}
	switch rule.Sort {
	case "year_desc":
		q += " ORDER BY COALESCE(t.year,0) DESC, t.id"
	case "bpm":
		q += " ORDER BY COALESCE(f.bpm,0), t.id"
	case "last_played":
		q += " ORDER BY ts.last_played_at IS NULL DESC, ts.last_played_at, t.id"
	case "plays":
		q += " ORDER BY COALESCE(ts.plays,0), t.id"
	default:
		q += " ORDER BY t.id"
	}
	limit := rule.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	q += " LIMIT ?"
	args = append(args, limit)
	rows, err := store.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func compileGroup(clauses []Clause, join string) (string, []any, error) {
	if len(clauses) == 0 {
		return "", nil, nil
	}
	parts := make([]string, 0, len(clauses))
	var args []any
	for _, clause := range clauses {
		sql, extra, err := compileClause(clause)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "("+sql+")")
		args = append(args, extra...)
	}
	return strings.Join(parts, " "+join+" "), args, nil
}

func compileClause(c Clause) (string, []any, error) {
	switch c.Field {
	case "artist":
		return compareText("lower(trim(t.artist))", c)
	case "album":
		return compareText("lower(trim(t.album))", c)
	case "liked":
		want := asBool(c.Value)
		expr := "(fav.track_id IS NOT NULL OR pref.rating = 'like')"
		if c.Op == "neq" || !want {
			return "NOT " + expr, nil, nil
		}
		return expr, nil, nil
	case "plays":
		return compareNumber("COALESCE(ts.plays,0)", c)
	case "year":
		return compareNumber("COALESCE(t.year,0)", c)
	case "bpm":
		return compareNumber("COALESCE(f.bpm,0)", c)
	case "lufs":
		return compareNumber("COALESCE(f.lufs, t.lufs, 0)", c)
	case "duration":
		return compareNumber("COALESCE(t.duration,0)", c)
	case "last_played":
		return compareLastPlayed(c)
	case "tag":
		return compareTag(c)
	default:
		return "", nil, fmt.Errorf("unsupported field")
	}
}

func compareText(expr string, c Clause) (string, []any, error) {
	switch c.Op {
	case "eq":
		return expr + " = lower(?)", []any{fmt.Sprint(c.Value)}, nil
	case "neq":
		return expr + " <> lower(?)", []any{fmt.Sprint(c.Value)}, nil
	case "in", "not_in":
		values := asStringSlice(c.Value)
		if len(values) == 0 {
			return "0", nil, nil
		}
		ph := make([]string, len(values))
		args := make([]any, len(values))
		for i, v := range values {
			ph[i] = "lower(?)"
			args[i] = v
		}
		sql := expr + " IN (" + strings.Join(ph, ",") + ")"
		if c.Op == "not_in" {
			sql = "NOT " + sql
		}
		return sql, args, nil
	default:
		return "", nil, fmt.Errorf("op %s not valid for text", c.Op)
	}
}

func compareNumber(expr string, c Clause) (string, []any, error) {
	switch c.Op {
	case "eq":
		return expr + " = ?", []any{asFloat(c.Value)}, nil
	case "neq":
		return expr + " <> ?", []any{asFloat(c.Value)}, nil
	case "gt":
		return expr + " > ?", []any{asFloat(c.Value)}, nil
	case "gte":
		return expr + " >= ?", []any{asFloat(c.Value)}, nil
	case "lt":
		return expr + " < ?", []any{asFloat(c.Value)}, nil
	case "lte":
		return expr + " <= ?", []any{asFloat(c.Value)}, nil
	case "between":
		a, b := asPair(c.Value)
		return expr + " BETWEEN ? AND ?", []any{a, b}, nil
	default:
		return "", nil, fmt.Errorf("op %s not valid for number", c.Op)
	}
}

func compareLastPlayed(c Clause) (string, []any, error) {
	days := int(asFloat(c.Value))
	if days < 0 {
		days = 0
	}
	if days > 36500 {
		days = 36500
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	switch c.Op {
	case "older_than_days":
		return "ts.last_played_at IS NOT NULL AND ts.last_played_at <= ?", []any{cutoff}, nil
	case "newer_than_days":
		return "ts.last_played_at IS NOT NULL AND ts.last_played_at >= ?", []any{cutoff}, nil
	case "eq":
		if !asBool(c.Value) {
			return "ts.last_played_at IS NULL", nil, nil
		}
		return "ts.last_played_at IS NOT NULL", nil, nil
	default:
		return "", nil, fmt.Errorf("op %s not valid for last_played", c.Op)
	}
}

func compareTag(c Clause) (string, []any, error) {
	names := asStringSlice(c.Value)
	if c.Op == "eq" {
		names = []string{fmt.Sprint(c.Value)}
	}
	if len(names) == 0 {
		return "0", nil, nil
	}
	ph := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		ph[i] = "lower(?)"
		args[i] = name
	}
	sql := `t.id IN (
SELECT tt.track_id FROM track_tags tt
JOIN custom_tags ct ON ct.tag_id = tt.tag_id
WHERE ct.archived_at IS NULL AND lower(ct.name) IN (` + strings.Join(ph, ",") + `))`
	if c.Op == "not_in" || c.Op == "neq" {
		sql = "NOT " + sql
	}
	return sql, args, nil
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case float64:
		return x != 0
	default:
		return false
	}
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		var f float64
		fmt.Sscanf(x, "%f", &f)
		return f
	default:
		return 0
	}
}

func asPair(v any) (float64, float64) {
	switch x := v.(type) {
	case []any:
		if len(x) >= 2 {
			return asFloat(x[0]), asFloat(x[1])
		}
	case []float64:
		if len(x) >= 2 {
			return x[0], x[1]
		}
	}
	return 0, 0
}

func asStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, strings.TrimSpace(fmt.Sprint(item)))
		}
		return out
	case []string:
		return x
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	default:
		return nil
	}
}
