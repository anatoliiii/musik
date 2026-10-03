package rules

import (
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

type Decision struct {
	Blocked  bool
	Cooldown bool
	Downrank float64
	Reasons  []string
}

type Evaluator struct {
	Idx   *index.Index
	Rules []db.RadioRule
	Now   time.Time
}

func New(idx *index.Index, rules []db.RadioRule, now time.Time) *Evaluator {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &Evaluator{Idx: idx, Rules: rules, Now: now}
}

func DurationForPreset(preset string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "hour", "1h":
		return time.Hour
	case "day", "1d":
		return 24 * time.Hour
	case "week", "1w":
		return 7 * 24 * time.Hour
	case "session":
		return 0
	case "forever", "permanent":
		return -1
	default:
		return 0
	}
}

func (e *Evaluator) Decide(trackID int64) Decision {
	if e.Idx == nil {
		return Decision{}
	}
	row, ok := e.Idx.RowOf(trackID)
	if !ok {
		return Decision{}
	}
	meta := e.Idx.MetaAt(row)
	keys := e.keysFor(meta)
	var out Decision
	for _, rule := range e.Rules {
		matched, ok := keys[rule.TargetType+"\x00"+rule.TargetKey]
		if !ok || !matched {
			continue
		}
		switch rule.Action {
		case "block":
			out.Blocked = true
			out.Reasons = append(out.Reasons, "block:"+rule.TargetType)
		case "cooldown":
			if e.onCooldown(meta, rule) {
				out.Cooldown = true
				out.Reasons = append(out.Reasons, "cooldown:"+rule.TargetType)
			}
		case "downrank":
			if rule.Strength > out.Downrank {
				out.Downrank = rule.Strength
			}
			out.Reasons = append(out.Reasons, "downrank:"+rule.TargetType)
		}
	}
	return out
}

func (e *Evaluator) HardBlocked(trackID int64) bool {
	d := e.Decide(trackID)
	return d.Blocked || d.Cooldown
}

func (e *Evaluator) Downrank(trackID int64) float64 {
	return e.Decide(trackID).Downrank
}

func (e *Evaluator) FilterIDs(ids []int64) []int64 {
	if e == nil {
		return ids
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !e.HardBlocked(id) {
			out = append(out, id)
		}
	}
	return out
}

// BlocksArtist reports a hard artist block. Recommendations use it for artist
// cards that are not themselves tracks.
func (e *Evaluator) BlocksArtist(artist string) bool {
	if e == nil {
		return false
	}
	key := index.ArtistKey(artist)
	if key == "" {
		return false
	}
	for _, rule := range e.Rules {
		if rule.Action == "block" && rule.TargetType == "artist" && rule.TargetKey == key {
			return true
		}
	}
	return false
}

// BlocksAlbum reports an artist block or an album block.
func (e *Evaluator) BlocksAlbum(artist, album string) bool {
	if e == nil {
		return false
	}
	if e.BlocksArtist(artist) {
		return true
	}
	key := index.AlbumKey(artist, album)
	if key == "" {
		return false
	}
	for _, rule := range e.Rules {
		if rule.Action == "block" && rule.TargetType == "album" && rule.TargetKey == key {
			return true
		}
	}
	return false
}

func (e *Evaluator) keysFor(meta index.Meta) map[string]bool {
	out := map[string]bool{
		"track\x00" + index.TrackKey(meta.ID):                 true,
		"artist\x00" + index.ArtistKey(meta.Artist):           true,
		"album\x00" + index.AlbumKey(meta.Artist, meta.Album): true,
		"cluster\x00" + index.ClusterKey(meta.ClusterID):      true,
	}
	if meta.FileMD5 != "" {
		out["song\x00"+meta.FileMD5] = true
	}
	if key := index.SongKey(meta.Artist, meta.Title); key != "" {
		out["song\x00"+key] = true
	}
	return out
}

func (e *Evaluator) onCooldown(meta index.Meta, rule db.RadioRule) bool {
	if meta.LastPlayedAt.IsZero() {
		return false
	}
	window := time.Duration(rule.Strength * float64(time.Hour))
	if window <= 0 {
		window = 6 * time.Hour
	}
	return e.Now.Sub(meta.LastPlayedAt) < window
}

func ExpiresAt(preset string, now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d := DurationForPreset(preset)
	if d < 0 {
		return ""
	}
	if d == 0 {
		return ""
	}
	return now.Add(d).Format(time.RFC3339Nano)
}

func ParseTrackKey(key string) (int64, bool) {
	id, err := strconv.ParseInt(key, 10, 64)
	return id, err == nil && id > 0
}
