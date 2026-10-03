package playback

import (
	"fmt"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/queue"
)

type PlaySpec struct {
	TrackID      int64
	TrackIDs     []int64
	Artist       string
	Album        string
	Name         string
	StartIndex   int
	StartTrackID int64
}

func IsFixedMode(mode string) bool {
	switch mode {
	case "playlist", "later", "daily", "listen", "favorites":
		return true
	default:
		return false
	}
}

// GeneratedMixKind is a shelf the worker builds. Manual and saved lists are not.
func GeneratedMixKind(kind string) bool {
	switch kind {
	case "for_you", "daily", "new_releases", "weekly",
		"weekday_mon", "weekday_tue", "weekday_wed", "weekday_thu",
		"weekday_fri", "weekday_sat", "weekday_sun":
		return true
	default:
		return false
	}
}

func (e *Engine) ResolvePlayIDs(spec PlaySpec) (ids []int64, name string, err error) {
	if len(spec.TrackIDs) > 0 {
		for _, id := range spec.TrackIDs {
			if _, ok := e.Idx.RowOf(id); ok {
				ids = append(ids, id)
			}
		}
		name = spec.Name
		if name == "" {
			name = "Плейлист"
		}
		if len(ids) == 0 {
			return nil, "", fmt.Errorf("нет доступных треков")
		}
		return ids, name, nil
	}

	artist := strings.TrimSpace(spec.Artist)
	album := strings.TrimSpace(spec.Album)
	if artist != "" || album != "" {
		n := e.Idx.Size()
		for i := 0; i < n; i++ {
			m := e.Idx.MetaAt(i)
			if artist != "" && !strings.EqualFold(strings.TrimSpace(m.Artist), artist) {
				continue
			}
			if album != "" && !strings.EqualFold(strings.TrimSpace(m.Album), album) {
				continue
			}
			ids = append(ids, m.ID)
		}
		if len(ids) == 0 {
			return nil, "", fmt.Errorf("ничего не найдено")
		}
		switch {
		case artist != "" && album != "":
			name = artist + " — " + album
		case album != "":
			name = album
		default:
			name = artist
		}
		if spec.Name != "" {
			name = spec.Name
		}
		return ids, name, nil
	}

	if spec.TrackID != 0 {
		if _, ok := e.Idx.RowOf(spec.TrackID); !ok {
			path, err := e.Store.TrackPath(spec.TrackID)
			if err != nil || path == "" {
				return nil, "", fmt.Errorf("track not found")
			}
		}
		name = spec.Name
		if name == "" {
			name = "Трек"
		}
		return []int64{spec.TrackID}, name, nil
	}
	return nil, "", fmt.Errorf("укажи track_id, track_ids, artist или album")
}

func (e *Engine) StartFixed(ids []int64, mode, name, kind string, startIndex int, startTrackID int64) *Session {
	pos := 0
	if startTrackID != 0 {
		for i, id := range ids {
			if id == startTrackID {
				pos = i
				break
			}
		}
	} else if startIndex >= 0 && startIndex < len(ids) {
		pos = startIndex
	}
	sess := e.NewSession(mode)
	sess.Lock()
	sess.DailyIDs = ids
	sess.DailyPos = pos
	sess.PlaylistName = name
	sess.PlaylistKind = kind
	sess.Current = ids[pos]
	sess.CurrentItem = queue.Item{}
	e.ExcludeTrack(sess, sess.Current)
	e.RebuildFixedQueue(sess)
	sess.Unlock()
	e.flush()
	return sess
}

func (e *Engine) Jump(sess *Session, index *int, trackID int64) error {
	if len(sess.DailyIDs) > 0 {
		pos := -1
		if trackID != 0 {
			for i, id := range sess.DailyIDs {
				if id == trackID {
					pos = i
					break
				}
			}
		}
		if pos < 0 && index != nil {
			pos = *index
		}
		if pos < 0 || pos >= len(sess.DailyIDs) {
			return fmt.Errorf("track not in playlist")
		}
		sess.DailyPos = pos
		sess.Current = sess.DailyIDs[pos]
		sess.CurrentItem = queue.Item{}
		e.ExcludeTrack(sess, sess.Current)
		e.RebuildFixedQueue(sess)
		return nil
	}
	if trackID == 0 && index != nil && *index >= 0 && *index < len(sess.Queue) {
		trackID = sess.Queue[*index].TrackID
	}
	pos := -1
	for i, item := range sess.Queue {
		if item.TrackID == trackID {
			pos = i
			break
		}
	}
	if trackID == 0 || pos < 0 {
		return fmt.Errorf("track not in queue")
	}
	item := sess.Queue[pos]
	if item.TrackID != sess.Current {
		e.rememberBack(sess)
	}
	sess.Prev = sess.Current
	sess.Current = item.TrackID
	sess.CurrentItem = item
	sess.Queue = append([]queue.Item(nil), sess.Queue[pos+1:]...)
	e.ExcludeTrack(sess, sess.Current)
	if len(sess.Queue) < e.Cfg.QueueSize/2 {
		e.RefreshQueue(sess, sess.Current, "jump")
		return nil
	}
	sess.UpdatedAt = time.Now()
	e.persistLocked(sess)
	e.warm(sess)
	return nil
}

func (e *Engine) rememberBack(sess *Session) {
	if sess.Current == 0 {
		return
	}
	item := sess.CurrentItem
	if item.TrackID != sess.Current {
		item = e.trackItem(sess.Current)
	}
	if n := len(sess.BackStack); n > 0 && sess.BackStack[n-1].TrackID == item.TrackID {
		return
	}
	sess.BackStack = append(sess.BackStack, item)
	if len(sess.BackStack) > 40 {
		sess.BackStack = append([]queue.Item(nil), sess.BackStack[len(sess.BackStack)-40:]...)
	}
}

func (e *Engine) trackItem(id int64) queue.Item {
	row, ok := e.Idx.RowOf(id)
	if !ok {
		return queue.Item{TrackID: id}
	}
	m := e.Idx.MetaAt(row)
	return queue.Item{
		TrackID: m.ID, Artist: m.Artist, Title: m.Title, Album: m.Album,
		Path: m.Path, Duration: m.Duration,
	}
}

// Back restarts the previous track. In a fixed list that is the previous
// position. In radio it restores the track we just left and puts the current
// one back at the front of the queue.
func (e *Engine) Back(sess *Session) error {
	if len(sess.DailyIDs) > 0 {
		if sess.DailyPos <= 0 {
			return fmt.Errorf("нет предыдущего трека")
		}
		sess.DailyPos--
		sess.Current = sess.DailyIDs[sess.DailyPos]
		sess.Prev = 0
		if sess.DailyPos > 0 {
			sess.Prev = sess.DailyIDs[sess.DailyPos-1]
		}
		sess.CurrentItem = queue.Item{}
		e.ExcludeTrack(sess, sess.Current)
		e.RebuildFixedQueue(sess)
		e.createCurrentImpression(sess, "back", "back")
		return nil
	}
	if len(sess.BackStack) == 0 {
		return fmt.Errorf("нет предыдущего трека")
	}
	prev := sess.BackStack[len(sess.BackStack)-1]
	sess.BackStack = sess.BackStack[:len(sess.BackStack)-1]
	cur := sess.CurrentItem
	if cur.TrackID != sess.Current {
		cur = e.trackItem(sess.Current)
	}
	if cur.TrackID != 0 && (len(sess.Queue) == 0 || sess.Queue[0].TrackID != cur.TrackID) {
		sess.Queue = append([]queue.Item{cur}, sess.Queue...)
	}
	sess.Prev = 0
	if n := len(sess.BackStack); n > 0 {
		sess.Prev = sess.BackStack[n-1].TrackID
	}
	sess.Current = prev.TrackID
	sess.CurrentItem = prev
	e.createCurrentImpression(sess, "back", "back")
	sess.UpdatedAt = time.Now()
	e.persistLocked(sess)
	e.warm(sess)
	return nil
}

func (e *Engine) pickAllowedStart(sess *Session, seed *int64) int64 {
	startID := e.PickStart(sess, seed)
	eval := e.sessionRules(sess)
	if eval == nil || !eval.HardBlocked(startID) {
		return startID
	}
	e.applyHardBlocks(sess)
	if id := e.Builder.PickRandom(sess.Exclude); id != 0 {
		return id
	}
	return 0
}

func (e *Engine) StartRadio(seed *int64) *Session {
	sess := e.NewSession("radio")
	sess.Lock()
	e.SeedRadioExclude(sess)
	startID := e.pickAllowedStart(sess, seed)
	sess.Current = startID
	sess.Prev = 0
	e.ExcludeTrack(sess, startID)
	source := "radio_start"
	if seed != nil {
		source = "manual"
	}
	e.createCurrentImpression(sess, source, "radio_start")
	e.RefreshQueue(sess, startID, "radio_start")
	sess.Unlock()
	return sess
}

func (e *Engine) StartSession(seed *int64) *Session {
	sess := e.NewSession("session")
	sess.Lock()
	startID := e.pickAllowedStart(sess, seed)
	sess.Current = startID
	sess.Prev = 0
	source := "radio_start"
	if seed != nil {
		source = "manual"
	}
	e.createCurrentImpression(sess, source, "radio_start")
	e.RefreshQueue(sess, startID, "radio_start")
	sess.Unlock()
	return sess
}
