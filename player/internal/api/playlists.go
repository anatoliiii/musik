package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/playlist"
)

func (s *Server) handlePlaylistsList(w http.ResponseWriter, r *http.Request) {
	var types []string
	if raw := r.URL.Query().Get("type"); raw != "" {
		types = strings.Split(raw, ",")
	}
	list, err := s.Store.ListUserPlaylists(types, r.URL.Query().Get("all") == "1")
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if list == nil {
		list = []db.UserPlaylist{}
	}
	if blocks := s.globalBlocks(); blocks != nil {
		for i := range list {
			if !generatedPlaylist(&list[i]) {
				continue
			}
			items, err := s.Store.ListPlaylistItems(list[i].ID)
			if err != nil {
				writeErr(w, 500, "db", err.Error())
				return
			}
			n := 0
			for _, item := range items {
				if item.TrackID != 0 && blocks.HardBlocked(item.TrackID) {
					continue
				}
				n++
			}
			list[i].TrackCount = n
		}
	}
	writeJSON(w, map[string]any{"playlists": list})
}

func (s *Server) handlePlaylistsCreate(w http.ResponseWriter, r *http.Request) {
	var req db.UserPlaylist
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	if req.Type == "smart" && req.RuleJSON != "" {
		if _, err := playlist.ParseRule(req.RuleJSON); err != nil {
			writeErr(w, 400, "rule", err.Error())
			return
		}
		req.RuleSchema = playlist.RuleSchemaVersion
	}
	created, err := s.Store.CreateUserPlaylist(req)
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	if created.Type == "smart" && created.RuleJSON != "" {
		_ = s.materializeSmart(created.ID, created.RuleJSON)
	}
	out, _ := s.Store.GetUserPlaylist(created.ID)
	writeJSON(w, out)
}

func (s *Server) handlePlaylistGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	pl, err := s.Store.GetUserPlaylist(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if pl == nil {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	s.omitBlockedPlaylist(pl)
	writeJSON(w, pl)
}

func (s *Server) handlePlaylistPatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	var req db.UserPlaylist
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	req.ID = id
	if req.RuleJSON != "" {
		if _, err := playlist.ParseRule(req.RuleJSON); err != nil {
			writeErr(w, 400, "rule", err.Error())
			return
		}
		req.RuleSchema = playlist.RuleSchemaVersion
	}
	if err := s.Store.UpdateUserPlaylist(req); err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	if req.RuleJSON != "" {
		_ = s.materializeSmart(id, req.RuleJSON)
	}
	out, _ := s.Store.GetUserPlaylist(id)
	writeJSON(w, out)
}

func (s *Server) handlePlaylistDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	if r.URL.Query().Get("hard") == "1" {
		if err := s.Store.DeleteUserPlaylist(id); err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
	} else if err := s.Store.ArchiveUserPlaylist(id); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePlaylistAddTrack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	var req struct {
		TrackID  int64   `json:"track_id"`
		TrackIDs []int64 `json:"track_ids"`
		Source   string  `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	ids := append([]int64{}, req.TrackIDs...)
	if req.TrackID != 0 {
		ids = append([]int64{req.TrackID}, ids...)
	}
	if len(ids) == 0 {
		writeErr(w, 400, "track_id", "track_id required")
		return
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}
	added, skipped := 0, 0
	var last db.UserPlaylistItem
	for _, trackID := range ids {
		item, err := s.Store.AddPlaylistItem(id, db.UserPlaylistItem{TrackID: trackID, Source: source})
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") {
				skipped++
				continue
			}
			writeErr(w, 400, "playlist", err.Error())
			return
		}
		added++
		last = item
	}
	writeJSON(w, map[string]any{
		"ok": true, "added": added, "skipped": skipped, "item": last, "item_id": last.ItemID,
	})
}

func (s *Server) handlePlaylistRemoveTrack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	if err := s.Store.RemovePlaylistItem(id, r.PathValue("item_id")); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handlePlaylistReorder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	var req struct {
		ItemIDs []string `json:"item_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	if err := s.Store.ReorderPlaylistItems(id, req.ItemIDs); err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	out, _ := s.Store.GetUserPlaylist(id)
	writeJSON(w, out)
}

func (s *Server) handlePlaylistDuplicate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	out, err := s.Store.DuplicateUserPlaylist(id)
	if err != nil || out == nil {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handlePlaylistFromQueue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sess := s.Play.Get(req.SessionID)
	if sess == nil {
		writeErr(w, 404, "not_found", "session not found")
		return
	}
	sess.Lock()
	ids := make([]int64, 0, len(sess.Queue)+1)
	if sess.Current != 0 {
		ids = append(ids, sess.Current)
	}
	for _, item := range sess.Queue {
		ids = append(ids, item.TrackID)
	}
	sess.Unlock()
	for _, trackID := range ids {
		_, _ = s.Store.AddPlaylistItem(id, db.UserPlaylistItem{TrackID: trackID, Source: "radio"})
	}
	out, _ := s.Store.GetUserPlaylist(id)
	writeJSON(w, out)
}

func (s *Server) handlePlaylistPlay(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	var req struct {
		StartIndex   int   `json:"start_index"`
		StartTrackID int64 `json:"start_track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	pl, err := s.Store.GetUserPlaylist(id)
	if err != nil || pl == nil {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	if pl.Type == "smart" && pl.RuleJSON != "" {
		_ = s.materializeSmart(pl.ID, pl.RuleJSON)
		pl, _ = s.Store.GetUserPlaylist(id)
	}
	ids := resolvedTrackIDs(pl)
	if generatedPlaylist(pl) {
		if blocks := s.globalBlocks(); blocks != nil {
			ids = blocks.FilterIDs(ids)
		}
	}
	if len(ids) == 0 {
		writeErr(w, 404, "play", "нет доступных треков")
		return
	}
	sess := s.Play.StartFixed(ids, "playlist", pl.Name, "user", req.StartIndex, req.StartTrackID)
	sess.Lock()
	defer sess.Unlock()
	writeJSON(w, s.playResponse(sess))
}

func (s *Server) handlePlaylistRadio(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	pl, err := s.Store.GetUserPlaylist(id)
	if err != nil || pl == nil {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	ids := resolvedTrackIDs(pl)
	var seed *int64
	if len(ids) > 0 {
		seed = &ids[0]
	}
	sess := s.Play.StartRadio(seed)
	sess.Lock()
	defer sess.Unlock()
	writeJSON(w, s.sessionStartResponse(sess))
}

func (s *Server) handlePlaylistPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleJSON string `json:"rule_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	rule, err := playlist.ParseRule(req.RuleJSON)
	if err != nil {
		writeErr(w, 400, "rule", err.Error())
		return
	}
	ids, err := playlist.Evaluate(s.Store, rule)
	if err != nil {
		writeErr(w, 400, "rule", err.Error())
		return
	}
	tracks := make([]any, 0, len(ids))
	for _, id := range ids {
		tracks = append(tracks, s.trackJSON(id))
	}
	writeJSON(w, map[string]any{"track_ids": ids, "tracks": tracks, "count": len(ids)})
}

func (s *Server) handlePlaylistImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "bad_body", "cannot read body")
		return
	}
	name := r.URL.Query().Get("name")
	doc, err := playlist.ParseImport(name, string(body), r.Header.Get("Content-Type"))
	if err != nil {
		writeErr(w, 400, "import", err.Error())
		return
	}
	created, err := s.Store.CreateUserPlaylist(db.UserPlaylist{Name: doc.Name, Type: "manual", Kind: "user"})
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	items := playlist.ResolveImport(s.Store, doc)
	if err := s.Store.ReplacePlaylistItems(created.ID, items); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	out, _ := s.Store.GetUserPlaylist(created.ID)
	writeJSON(w, out)
}

func (s *Server) handlePlaylistExport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "invalid playlist id")
		return
	}
	pl, err := s.Store.GetUserPlaylist(id)
	if err != nil || pl == nil {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	includePaths := r.URL.Query().Get("paths") == "1"
	if r.URL.Query().Get("format") == "m3u" {
		w.Header().Set("Content-Type", "audio/x-mpegurl")
		_, _ = w.Write([]byte(playlist.ExportM3U(pl, includePaths)))
		return
	}
	raw, err := playlist.ExportJSON(pl, includePaths)
	if err != nil {
		writeErr(w, 500, "export", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) handlePlaylistFromFavorites(w http.ResponseWriter, r *http.Request) {
	s.copySpecialList(w, r, "Избранное", func() ([]db.PlaylistTrack, error) { return s.Store.FavoritesList() })
}

func (s *Server) handlePlaylistFromLater(w http.ResponseWriter, r *http.Request) {
	s.copySpecialList(w, r, "Потом", func() ([]db.PlaylistTrack, error) { return s.Store.LaterList() })
}

func (s *Server) copySpecialList(w http.ResponseWriter, r *http.Request, defaultName string, load func() ([]db.PlaylistTrack, error)) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Name == "" {
		req.Name = defaultName
	}
	tracks, err := load()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	created, err := s.Store.CreateUserPlaylist(db.UserPlaylist{Name: req.Name, Type: "manual", Kind: "user"})
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	items := make([]db.UserPlaylistItem, 0, len(tracks))
	for _, tr := range tracks {
		items = append(items, db.UserPlaylistItem{TrackID: tr.TrackID, Source: "manual"})
	}
	if err := s.Store.ReplacePlaylistItems(created.ID, items); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	out, _ := s.Store.GetUserPlaylist(created.ID)
	writeJSON(w, out)
}

func (s *Server) handleTagsList(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.ListCustomTags()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if list == nil {
		list = []db.CustomTag{}
	}
	writeJSON(w, map[string]any{"tags": list})
}

func (s *Server) handleTagsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	tag, err := s.Store.CreateCustomTag(req.Name)
	if err != nil {
		writeErr(w, 400, "tag", err.Error())
		return
	}
	writeJSON(w, tag)
}

func (s *Server) handleTagAddTrack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackID int64 `json:"track_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TrackID == 0 {
		writeErr(w, 400, "bad_json", "track_id required")
		return
	}
	if err := s.Store.TagTrack(r.PathValue("id"), req.TrackID); err != nil {
		writeErr(w, 400, "tag", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleTagRemoveTrack(w http.ResponseWriter, r *http.Request) {
	trackID, _ := strconv.ParseInt(r.URL.Query().Get("track_id"), 10, 64)
	if trackID == 0 {
		writeErr(w, 400, "bad_id", "track_id required")
		return
	}
	if err := s.Store.UntagTrack(r.PathValue("id"), trackID); err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) materializeSmart(id int64, raw string) error {
	rule, err := playlist.ParseRule(raw)
	if err != nil {
		return err
	}
	ids, err := playlist.Evaluate(s.Store, rule)
	if err != nil {
		return err
	}
	items := make([]db.UserPlaylistItem, 0, len(ids))
	for _, trackID := range ids {
		items = append(items, db.UserPlaylistItem{TrackID: trackID, Source: "rule"})
	}
	return s.Store.ReplacePlaylistItems(id, items)
}

func resolvedTrackIDs(pl *db.UserPlaylist) []int64 {
	var ids []int64
	if pl == nil {
		return nil
	}
	for _, item := range pl.Tracks {
		if item.TrackID != 0 {
			ids = append(ids, item.TrackID)
		}
	}
	return ids
}
