package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/rules"
)

// Mix shelf definition (VK-style).
var mixShelf = []struct {
	Kind     string
	Title    string
	Subtitle string
}{
	{"for_you", "Для вас", "Персональный микс под вкус"},
	{"daily", "На сегодня", "Свежий микс на день"},
	{"favorites", "Избранное", "Треки с сердечком"},
	{"later", "Потом", "Отложенные треки"},
	{"new_releases", "Новинки", "Недавно в библиотеке"},
	{"weekly", "Недельный микс", "Разнообразие по кластерам"},
	{"weekday_mon", "Понедельник", "Микс дня недели"},
	{"weekday_tue", "Вторник", "Микс дня недели"},
	{"weekday_wed", "Среда", "Микс дня недели"},
	{"weekday_thu", "Четверг", "Микс дня недели"},
	{"weekday_fri", "Пятница", "Микс дня недели"},
	{"weekday_sat", "Суббота", "Микс дня недели"},
	{"weekday_sun", "Воскресенье", "Микс дня недели"},
}

func (s *Server) handleMixes(w http.ResponseWriter, _ *http.Request) {
	today := playback.TodayWeekdayKind()
	blocks := s.globalBlocks()
	out := make([]map[string]any, 0, len(mixShelf))
	for _, m := range mixShelf {
		card := map[string]any{
			"kind": m.Kind, "title": m.Title, "subtitle": m.Subtitle,
			"highlight": m.Kind == "for_you" || m.Kind == "daily" || m.Kind == today,
			"today":     m.Kind == today,
		}
		if m.Kind == "later" {
			tracks, _ := s.Store.LaterList()
			card["tracks"] = len(tracks)
			card["ready"] = len(tracks) > 0
			card["playlist_id"] = nil
			card["special"] = "later"
			if len(tracks) > 0 {
				card["cover_track_id"] = tracks[0].TrackID
			}
		} else if m.Kind == "favorites" {
			tracks, _ := s.Store.FavoritesList()
			card["tracks"] = len(tracks)
			card["ready"] = len(tracks) > 0
			card["playlist_id"] = nil
			card["special"] = "favorites"
			if len(tracks) > 0 {
				card["cover_track_id"] = tracks[0].TrackID
			}
		} else {
			id, name, n, coverID, createdAt, err := s.mixMeta(m.Kind, blocks)
			if err != nil {
				writeErr(w, 500, "db", err.Error())
				return
			}
			card["playlist_id"] = nil
			card["name"] = name
			card["tracks"] = n
			card["ready"] = id != 0 && n > 0
			if id != 0 {
				card["playlist_id"] = id
				if coverID != 0 {
					card["cover_track_id"] = coverID
				}
				if createdAt != "" {
					card["generated_at"] = createdAt
					if generatedAt, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
						card["age_seconds"] = int64(time.Since(generatedAt).Seconds())
						card["stale"] = time.Since(generatedAt) > 36*time.Hour
					}
				}
			}
		}
		out = append(out, card)
	}
	writeJSON(w, map[string]any{
		"mixes":         out,
		"today_weekday": today,
		"hint":          "Полки пересобираются ночью; POST /api/jobs/mix_pack запускает обновление вручную",
	})
}

func (s *Server) handleMixPlay(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		writeErr(w, 400, "kind_required", "kind required")
		return
	}

	var req struct {
		StartIndex   *int  `json:"start_index"`
		StartTrackID int64 `json:"start_track_id"`
		TrackID      int64 `json:"track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.StartTrackID == 0 && req.TrackID != 0 {
		req.StartTrackID = req.TrackID
	}

	var ids []int64
	var name string
	mode := "playlist"

	if kind == "later" {
		tracks, err := s.Store.LaterList()
		if err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
		if len(tracks) == 0 {
			writeErr(w, 404, "empty", "Потом пусто — добавь треки кнопкой «Потом»")
			return
		}
		for _, t := range tracks {
			ids = append(ids, t.TrackID)
		}
		name = "Потом"
		mode = "later"
	} else if kind == "favorites" {
		tracks, err := s.Store.FavoritesList()
		if err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
		if len(tracks) == 0 {
			writeErr(w, 404, "empty", "Избранное пусто — жми ♥ на треке")
			return
		}
		for _, t := range tracks {
			ids = append(ids, t.TrackID)
		}
		name = "Избранное"
		mode = "favorites"
	} else {
		pl, err := s.Store.LatestPlaylist(kind)
		if err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
		if pl == nil || len(pl.Tracks) == 0 {
			writeErr(w, 404, "empty", "плейлист не собран — нажми «Обновить миксы»")
			return
		}
		for _, t := range pl.Tracks {
			ids = append(ids, t.TrackID)
		}
		if blocks := s.globalBlocks(); blocks != nil {
			ids = blocks.FilterIDs(ids)
		}
		name = pl.Name
	}

	if len(ids) == 0 {
		writeErr(w, 404, "empty", "в этом миксе не осталось треков")
		return
	}

	startIdx := 0
	if req.StartIndex != nil {
		startIdx = *req.StartIndex
	}
	sess := s.Play.StartFixed(ids, mode, name, kind, startIdx, req.StartTrackID)
	sess.Lock()
	defer sess.Unlock()
	writeJSON(w, s.playResponse(sess))
}

func (s *Server) mixMeta(kind string, blocks *rules.Evaluator) (id int64, name string, n int, coverID int64, createdAt string, err error) {
	if blocks == nil || !playback.GeneratedMixKind(kind) {
		return s.Store.PlaylistMeta(kind)
	}
	pl, err := s.Store.LatestPlaylist(kind)
	if err != nil || pl == nil {
		return 0, "", 0, 0, "", err
	}
	for _, track := range pl.Tracks {
		if blocks.HardBlocked(track.TrackID) {
			continue
		}
		if coverID == 0 {
			coverID = track.TrackID
		}
		n++
	}
	return pl.ID, pl.Name, n, coverID, pl.CreatedAt, nil
}
