package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/playback"
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
		ids := make([]int64, 0)
		for i := range list {
			if generatedPlaylist(&list[i]) {
				ids = append(ids, list[i].ID)
			}
		}
		tracks, err := s.Store.ListPlaylistTrackIDs(r.Context(), ids)
		if err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
		for i := range list {
			if !generatedPlaylist(&list[i]) {
				continue
			}
			n := 0
			for _, trackID := range tracks[list[i].ID] {
				if trackID != 0 && blocks.HardBlocked(trackID) {
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
		out, err := playlist.CreateSmart(r.Context(), s.Store, req)
		if err != nil {
			s.writePlaylistErr(w, err)
			return
		}
		writeJSON(w, out)
		return
	}
	created, err := s.Store.CreateUserPlaylist(req)
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	out, err := s.Store.GetUserPlaylist(created.ID)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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
		out, err := playlist.UpdateSmart(r.Context(), s.Store, req)
		if err != nil {
			s.writePlaylistErr(w, err)
			return
		}
		writeJSON(w, out)
		return
	}
	if err := s.Store.UpdateUserPlaylist(req); err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	out, err := s.Store.GetUserPlaylist(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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
	items := make([]db.UserPlaylistItem, 0, len(ids))
	for _, trackID := range ids {
		items = append(items, db.UserPlaylistItem{TrackID: trackID, Source: source})
	}
	res, err := s.Store.AddPlaylistItems(r.Context(), id, items)
	if err != nil {
		if errors.Is(err, db.ErrPlaylistNotFound) {
			writeErr(w, 404, "not_found", "playlist not found")
			return
		}
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "added": res.Added, "skipped": res.Skipped, "item": res.Last, "item_id": res.Last.ItemID,
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
	out, err := s.Store.GetUserPlaylist(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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
	items := make([]db.UserPlaylistItem, 0, len(ids))
	for _, trackID := range ids {
		items = append(items, db.UserPlaylistItem{TrackID: trackID, Source: "radio"})
	}
	if _, err := s.Store.AddPlaylistItems(r.Context(), id, items); err != nil {
		if errors.Is(err, db.ErrPlaylistNotFound) {
			writeErr(w, 404, "not_found", "playlist not found")
			return
		}
		writeErr(w, 500, "db", err.Error())
		return
	}
	out, err := s.Store.GetUserPlaylist(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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
		if err := playlist.Materialize(r.Context(), s.Store, pl.ID, pl.RuleJSON); err != nil {
			writeErr(w, 500, "playlist", err.Error())
			return
		}
		pl, err = s.Store.GetUserPlaylist(id)
		if err != nil {
			writeErr(w, 500, "db", err.Error())
			return
		}
		if pl == nil {
			writeErr(w, 404, "not_found", "playlist not found")
			return
		}
	}
	ids := resolvedTrackIDs(pl)
	if generatedPlaylist(pl) {
		if blocks := s.globalBlocks(); blocks != nil {
			ids = blocks.FilterIDs(ids)
		}
	}
	if len(ids) == 0 {
		writeErr(w, 404, "play", playback.ErrNoTracks.Error())
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
	sess := s.Play.Start("radio", seed, nil)
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
	ids, err := playlist.Evaluate(r.Context(), s.Store, rule)
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
	items := playlist.ResolveImport(s.Store, doc)
	created, err := s.Store.CreateUserPlaylistWithItems(r.Context(), db.UserPlaylist{Name: doc.Name, Type: "manual", Kind: "user"}, items)
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	out, err := s.Store.GetUserPlaylist(created.ID)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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
	items := make([]db.UserPlaylistItem, 0, len(tracks))
	for _, tr := range tracks {
		items = append(items, db.UserPlaylistItem{TrackID: tr.TrackID, Source: "manual"})
	}
	created, err := s.Store.CreateUserPlaylistWithItems(r.Context(), db.UserPlaylist{Name: req.Name, Type: "manual", Kind: "user"}, items)
	if err != nil {
		writeErr(w, 400, "playlist", err.Error())
		return
	}
	out, err := s.Store.GetUserPlaylist(created.ID)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
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

func (s *Server) writePlaylistErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrPlaylistNotFound) {
		writeErr(w, 404, "not_found", "playlist not found")
		return
	}
	if playlist.IsRuleError(err) {
		writeErr(w, 400, "rule", err.Error())
		return
	}
	writeErr(w, 400, "playlist", err.Error())
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
