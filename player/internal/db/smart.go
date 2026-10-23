package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SmartRule is the evaluated form of a smart-playlist rule. Parsing and
// schema checks stay with the playlist package; this is only the SQL input.
type SmartRule struct {
	Limit int
	Sort  string
	All   []SmartClause
	Any   []SmartClause
}

// SmartClause is one comparison inside a smart rule.
type SmartClause struct {
	Field string
	Op    string
	Value any
}

// DaysAgoUTC is the UTC timestamp `days` before now, in the RFC3339Nano text
// both engines store in timestamp columns.
func DaysAgoUTC(days int) string {
	if days < 0 {
		days = 0
	}
	if days > 36500 {
		days = 36500
	}
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
}

// SmartTrackIDs returns active catalog tracks that match rule, scoped to this
// profile. Number and timestamp predicates go through Dialect.
func (s *Store) SmartTrackIDs(ctx context.Context, rule SmartRule) ([]int64, error) {
	q := `
SELECT t.id
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
LEFT JOIN track_stats ts ON ts.track_id = t.id AND ts.profile_id = ?
LEFT JOIN track_preferences pref ON pref.track_id = t.id AND pref.profile_id = ?
LEFT JOIN favorites fav ON fav.track_id = t.id AND fav.profile_id = ?
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL`
	profile := s.ProfileID
	args := []any{profile, profile, profile}
	allSQL, allArgs, err := compileSmartGroup(s.Dialect, rule.All, "AND", profile)
	if err != nil {
		return nil, err
	}
	anySQL, anyArgs, err := compileSmartGroup(s.Dialect, rule.Any, "OR", profile)
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
		q += " ORDER BY " + s.Dialect.NullsFirst("ts.last_played_at") + ", t.id"
	case "plays":
		q += " ORDER BY COALESCE(ts.plays,0), t.id"
	case "plays_desc":
		q += " ORDER BY COALESCE(ts.plays,0) DESC, t.id"
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
	var ids []int64
	if err := s.DB.Select(ctx, &ids, q, args...); err != nil {
		return nil, err
	}
	return ids, nil
}

func compileSmartGroup(d Dialect, clauses []SmartClause, join, profileID string) (string, []any, error) {
	if len(clauses) == 0 {
		return "", nil, nil
	}
	parts := make([]string, 0, len(clauses))
	var args []any
	for _, clause := range clauses {
		sql, extra, err := compileSmartClause(d, clause, profileID)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "("+sql+")")
		args = append(args, extra...)
	}
	return strings.Join(parts, " "+join+" "), args, nil
}

func compileSmartClause(d Dialect, c SmartClause, profileID string) (string, []any, error) {
	switch c.Field {
	case "artist":
		return compareSmartText("lower(trim(t.artist))", c)
	case "album":
		return compareSmartText("lower(trim(t.album))", c)
	case "liked":
		want := smartBool(c.Value)
		expr := "(fav.track_id IS NOT NULL OR pref.rating = 'like')"
		if c.Op == "neq" || !want {
			return "NOT " + expr, nil, nil
		}
		return expr, nil, nil
	case "plays":
		return compareSmartNumber(d, "COALESCE(ts.plays,0)", c)
	case "year":
		return compareSmartNumber(d, "COALESCE(t.year,0)", c)
	case "bpm":
		return compareSmartNumber(d, "COALESCE(f.bpm,0)", c)
	case "lufs":
		return compareSmartNumber(d, "COALESCE(f.lufs, t.lufs, 0)", c)
	case "duration":
		return compareSmartNumber(d, "COALESCE(t.duration,0)", c)
	case "last_played":
		return compareSmartLastPlayed(d, c)
	case "tag":
		return compareSmartTag(c, profileID)
	default:
		return "", nil, fmt.Errorf("unsupported field")
	}
}

func compareSmartText(expr string, c SmartClause) (string, []any, error) {
	switch c.Op {
	case "eq":
		return expr + " = lower(?)", []any{fmt.Sprint(c.Value)}, nil
	case "neq":
		return expr + " <> lower(?)", []any{fmt.Sprint(c.Value)}, nil
	case "in", "not_in":
		values := smartStrings(c.Value)
		if len(values) == 0 {
			return "1=0", nil, nil
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

func compareSmartNumber(d Dialect, expr string, c SmartClause) (string, []any, error) {
	value := d.Real()
	switch c.Op {
	case "eq":
		return expr + " = " + value, []any{smartFloat(c.Value)}, nil
	case "neq":
		return expr + " <> " + value, []any{smartFloat(c.Value)}, nil
	case "gt":
		return expr + " > " + value, []any{smartFloat(c.Value)}, nil
	case "gte":
		return expr + " >= " + value, []any{smartFloat(c.Value)}, nil
	case "lt":
		return expr + " < " + value, []any{smartFloat(c.Value)}, nil
	case "lte":
		return expr + " <= " + value, []any{smartFloat(c.Value)}, nil
	case "between":
		a, b := smartPair(c.Value)
		return expr + " BETWEEN " + value + " AND " + value, []any{a, b}, nil
	default:
		return "", nil, fmt.Errorf("op %s not valid for number", c.Op)
	}
}

func compareSmartLastPlayed(d Dialect, c SmartClause) (string, []any, error) {
	switch c.Op {
	case "older_than_days":
		return d.NotAfter("ts.last_played_at"), []any{DaysAgoUTC(int(smartFloat(c.Value)))}, nil
	case "newer_than_days":
		return d.NotBefore("ts.last_played_at"), []any{DaysAgoUTC(int(smartFloat(c.Value)))}, nil
	case "eq":
		if !smartBool(c.Value) {
			return "ts.last_played_at IS NULL", nil, nil
		}
		return "ts.last_played_at IS NOT NULL", nil, nil
	default:
		return "", nil, fmt.Errorf("op %s not valid for last_played", c.Op)
	}
}

func compareSmartTag(c SmartClause, profileID string) (string, []any, error) {
	names := smartStrings(c.Value)
	if c.Op == "eq" {
		names = []string{fmt.Sprint(c.Value)}
	}
	if len(names) == 0 {
		return "1=0", nil, nil
	}
	ph := make([]string, len(names))
	args := []any{profileID}
	for i, name := range names {
		ph[i] = "lower(?)"
		args = append(args, name)
	}
	sql := `t.id IN (
SELECT tt.track_id FROM track_tags tt
JOIN custom_tags ct ON ct.profile_id = tt.profile_id AND ct.tag_id = tt.tag_id
WHERE tt.profile_id = ? AND ct.archived_at IS NULL AND lower(ct.name) IN (` + strings.Join(ph, ",") + `))`
	if c.Op == "not_in" || c.Op == "neq" {
		sql = "NOT " + sql
	}
	return sql, args, nil
}

func smartBool(v any) bool {
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

func smartFloat(v any) float64 {
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

func smartPair(v any) (float64, float64) {
	switch x := v.(type) {
	case []any:
		if len(x) >= 2 {
			return smartFloat(x[0]), smartFloat(x[1])
		}
	case []float64:
		if len(x) >= 2 {
			return x[0], x[1]
		}
	}
	return 0, 0
}

func smartStrings(v any) []string {
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
