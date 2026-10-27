package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/privacy"
	"github.com/torwin-job/musik/player/internal/yandex"
)

// yandexPlugin is the optional Yandex Music integration (MUSIK_YANDEX=1).
// Each musik user signs in to Yandex separately; downloads go into the
// shared library or, on request, into the active profile's private folder,
// and then through the usual rescan pipeline.
type yandexPlugin struct {
	tokens  *yandex.TokenStore
	jobs    *yandex.Manager
	apiURL  string
	oauth   string
	mu      sync.Mutex
	pending map[string]pendingLogin
}

type pendingLogin struct {
	code    yandex.DeviceCode
	expires time.Time
}

func (s *Server) enableYandex() {
	if !s.Cfg.Yandex {
		return
	}
	dir := filepath.Join(s.Cfg.DataRoot(), "yandex")
	d := &yandex.Downloader{HTTP: &http.Client{Timeout: 10 * time.Minute}, FFmpeg: s.Cfg.FFmpegPath}
	p := &yandexPlugin{
		tokens:  &yandex.TokenStore{Dir: filepath.Join(dir, "tokens")},
		jobs:    yandex.NewManager(d, s.Cfg.YandexQuality),
		apiURL:  s.Cfg.YandexAPIURL,
		oauth:   s.Cfg.YandexOAuthURL,
		pending: map[string]pendingLogin{},
	}
	p.jobs.Finished = func(job yandex.Job) {
		if _, _, err := s.enqueueRescan(); err != nil {
			log.Printf("yandex: rescan after job %s: %v", job.ID, err)
		}
	}
	s.yandex = p
}

// yandexUser is the musik account a Yandex sign-in belongs to. Single-user
// mode has one account.
func yandexUser(r *http.Request) string {
	if p := caller(r); p != nil && p.UserID != "" {
		return p.UserID
	}
	return "local"
}

func (s *Server) yandexClient(token string) *yandex.Client {
	c := &yandex.Client{HTTP: s.HTTP, Token: token}
	if s.yandex != nil {
		c.APIURL, c.OAuthURL = s.yandex.apiURL, s.yandex.oauth
	}
	return c
}

// yandexSession returns the caller's signed-in client, or writes an error.
func (s *Server) yandexSession(w http.ResponseWriter, r *http.Request) (*yandex.Client, bool) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return nil, false
	}
	sess, ok := s.yandex.tokens.Load(yandexUser(r))
	if !ok {
		writeErr(w, 403, "yandex_login_required", "войдите в Яндекс Музыку")
		return nil, false
	}
	return s.yandexClient(sess.Token), true
}

func writeYandexErr(w http.ResponseWriter, err error) {
	if errors.Is(err, yandex.ErrUnauthorized) {
		writeErr(w, 403, "yandex_login_required", "сессия Яндекса истекла, войдите снова")
		return
	}
	var api *yandex.APIError
	if errors.As(err, &api) && api.Status == 404 {
		writeErr(w, 404, "not_found", "не найдено в Яндекс Музыке")
		return
	}
	writeErr(w, 502, "yandex_error", err.Error())
}

func (s *Server) handleYandexStatus(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	out := map[string]any{"enabled": true, "connected": false}
	if sess, ok := s.yandex.tokens.Load(yandexUser(r)); ok {
		out["connected"] = true
		out["login"] = sess.Login
		out["display_name"] = sess.DisplayName
		out["has_plus"] = sess.HasPlus
	}
	writeJSON(w, out)
}

// handleYandexLogin starts the device flow: the user opens the link, enters
// the code, and the page polls handleYandexLoginPoll.
func (s *Server) handleYandexLogin(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	code, err := s.yandexClient("").RequestDeviceCode(r.Context(), "musik")
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	expires := time.Duration(code.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 5 * time.Minute
	}
	s.yandex.mu.Lock()
	s.yandex.pending[yandexUser(r)] = pendingLogin{code: code, expires: time.Now().Add(expires)}
	s.yandex.mu.Unlock()
	interval := code.Interval
	if interval <= 0 {
		interval = 5
	}
	writeJSON(w, map[string]any{
		"user_code": code.UserCode, "verification_url": code.VerificationURL,
		"interval": interval, "expires_in": int(expires.Seconds()),
	})
}

func (s *Server) handleYandexLoginPoll(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	user := yandexUser(r)
	s.yandex.mu.Lock()
	login, ok := s.yandex.pending[user]
	s.yandex.mu.Unlock()
	if !ok || time.Now().After(login.expires) {
		writeErr(w, 410, "login_expired", "код устарел, начните вход заново")
		return
	}
	token, err := s.yandexClient("").PollDeviceToken(r.Context(), login.code.DeviceCode)
	if errors.Is(err, yandex.ErrPending) {
		writeJSON(w, map[string]any{"status": "pending"})
		return
	}
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	s.yandex.mu.Lock()
	delete(s.yandex.pending, user)
	s.yandex.mu.Unlock()
	s.saveYandexToken(w, r, token)
}

// handleYandexLoginToken accepts a token the user obtained elsewhere, for
// when the device flow is not available.
func (s *Server) handleYandexLoginToken(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeErr(w, 400, "token_required", "нужен токен")
		return
	}
	s.saveYandexToken(w, r, strings.TrimSpace(body.Token))
}

func (s *Server) saveYandexToken(w http.ResponseWriter, r *http.Request, token string) {
	account, err := s.yandexClient(token).Account(r.Context())
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	if account.UID == "" && account.Login == "" {
		writeErr(w, 403, "yandex_login_required", "Яндекс не принял токен")
		return
	}
	sess := yandex.Session{
		Token: token, Login: account.Login, DisplayName: account.DisplayName,
		HasPlus: account.HasPlus, SavedAt: time.Now().UTC(),
	}
	if err := s.yandex.tokens.Save(yandexUser(r), sess); err != nil {
		writeErr(w, 500, "token_store", "не удалось сохранить вход")
		return
	}
	writeJSON(w, map[string]any{
		"status": "ok", "connected": true, "login": sess.Login,
		"display_name": sess.DisplayName, "has_plus": sess.HasPlus,
	})
}

// handleYandexLogout forgets the user's token, stops their downloads and
// asks Yandex to revoke the token. Files already saved stay in the library.
func (s *Server) handleYandexLogout(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	user := yandexUser(r)
	sess, had := s.yandex.tokens.Load(user)
	s.yandex.jobs.CancelUser(user)
	s.yandex.mu.Lock()
	delete(s.yandex.pending, user)
	s.yandex.mu.Unlock()
	if err := s.yandex.tokens.Delete(user); err != nil {
		writeErr(w, 500, "token_store", "не удалось удалить вход")
		return
	}
	if had {
		c := s.yandexClient(sess.Token)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.RevokeToken(ctx); err != nil {
				log.Printf("yandex: revoke token: %v", err)
			}
		}()
	}
	writeJSON(w, map[string]any{"ok": true, "connected": false})
}

type yandexArtistDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Cover string `json:"cover,omitempty"`
}

type yandexAlbumDTO struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	Artists    []string `json:"artists"`
	Cover      string   `json:"cover,omitempty"`
	TrackCount int      `json:"track_count,omitempty"`
	Genre      string   `json:"genre,omitempty"`
}

type yandexTrackDTO struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Artists   []string `json:"artists"`
	Album     string   `json:"album,omitempty"`
	AlbumID   string   `json:"album_id,omitempty"`
	Duration  float64  `json:"duration"`
	Cover     string   `json:"cover,omitempty"`
	Available bool     `json:"available"`
}

func artistNames(as []yandex.Artist) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func yandexArtist(a yandex.Artist) yandexArtistDTO {
	out := yandexArtistDTO{ID: string(a.ID), Name: a.Name}
	if a.Cover != nil {
		out.Cover = yandex.CoverURL(a.Cover.URI, "200x200")
	}
	return out
}

func yandexAlbum(a yandex.Album) yandexAlbumDTO {
	return yandexAlbumDTO{
		ID: string(a.ID), Title: yandex.AlbumTitle(a), Year: a.Year, Artists: artistNames(a.Artists),
		Cover: yandex.CoverURL(a.CoverURI, "200x200"), TrackCount: a.TrackCount, Genre: a.Genre,
	}
}

func yandexTrack(t yandex.Track) yandexTrackDTO {
	out := yandexTrackDTO{
		ID: string(t.ID), Title: yandex.TrackTitle(t), Artists: artistNames(t.Artists),
		Duration: float64(t.DurationMs) / 1000, Cover: yandex.CoverURL(t.CoverURI, "200x200"),
		Available: t.Available == nil || *t.Available,
	}
	if len(t.Albums) > 0 {
		out.Album, out.AlbumID = yandex.AlbumTitle(t.Albums[0]), string(t.Albums[0].ID)
		if out.Cover == "" {
			out.Cover = yandex.CoverURL(t.Albums[0].CoverURI, "200x200")
		}
	}
	return out
}

func (s *Server) handleYandexSearch(w http.ResponseWriter, r *http.Request) {
	c, ok := s.yandexSession(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, 400, "query_required", "введите запрос")
		return
	}
	kind := r.URL.Query().Get("type")
	switch kind {
	case "", "all", "artist", "album", "track":
	default:
		writeErr(w, 400, "bad_type", "type: all, artist, album или track")
		return
	}
	res, err := c.Search(r.Context(), q, kind, 0)
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	out := map[string]any{"artists": []yandexArtistDTO{}, "albums": []yandexAlbumDTO{}, "tracks": []yandexTrackDTO{}}
	artists := make([]yandexArtistDTO, 0, len(res.Artists))
	for _, a := range res.Artists {
		artists = append(artists, yandexArtist(a))
	}
	albums := make([]yandexAlbumDTO, 0, len(res.Albums))
	for _, a := range res.Albums {
		albums = append(albums, yandexAlbum(a))
	}
	tracks := make([]yandexTrackDTO, 0, len(res.Tracks))
	for _, t := range res.Tracks {
		tracks = append(tracks, yandexTrack(t))
	}
	out["artists"], out["albums"], out["tracks"] = artists, albums, tracks
	writeJSON(w, out)
}

func (s *Server) handleYandexArtist(w http.ResponseWriter, r *http.Request) {
	c, ok := s.yandexSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	artist, err := c.Artist(r.Context(), id)
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	albums, err := c.ArtistAlbums(r.Context(), id)
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	tracks, err := c.ArtistTracks(r.Context(), id, 20)
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	albumOut := make([]yandexAlbumDTO, 0, len(albums))
	for _, a := range albums {
		albumOut = append(albumOut, yandexAlbum(a))
	}
	trackOut := make([]yandexTrackDTO, 0, len(tracks))
	for _, t := range tracks {
		trackOut = append(trackOut, yandexTrack(t))
	}
	writeJSON(w, map[string]any{"artist": yandexArtist(artist), "albums": albumOut, "tracks": trackOut})
}

func (s *Server) handleYandexAlbum(w http.ResponseWriter, r *http.Request) {
	c, ok := s.yandexSession(w, r)
	if !ok {
		return
	}
	album, err := c.AlbumWithTracks(r.Context(), r.PathValue("id"))
	if err != nil {
		writeYandexErr(w, err)
		return
	}
	tracks := []yandexTrackDTO{}
	for _, volume := range album.Volumes {
		for _, t := range volume {
			tracks = append(tracks, yandexTrack(t))
		}
	}
	writeJSON(w, map[string]any{"album": yandexAlbum(album), "tracks": tracks})
}

// handleYandexDownload queues a track, album or artist. Shared is the
// default; private:true saves into the active profile's own folder, which
// no other profile sees.
func (s *Server) handleYandexDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.yandexSession(w, r)
	if !ok {
		return
	}
	root := strings.TrimSpace(s.Cfg.Library)
	if root == "" {
		writeErr(w, 500, "no_library", "MUSIK_LIBRARY не задан")
		return
	}
	var req yandex.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.Private {
		if s.Store.ProfileID == "" {
			writeErr(w, 409, "no_profile", "нет активного профиля")
			return
		}
		root = privacy.PrivateRoot(root, s.Store.ProfileID)
	}
	job, err := s.yandex.jobs.Start(yandex.Target{UserID: yandexUser(r), Root: root, Client: c}, req)
	if err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	writeJSON(w, job)
}

func (s *Server) handleYandexDownloads(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	writeJSON(w, map[string]any{"jobs": s.yandex.jobs.Jobs(yandexUser(r))})
}

func (s *Server) handleYandexDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if s.yandex == nil {
		writeErr(w, 404, "plugin_disabled", "Яндекс Музыка выключена (MUSIK_YANDEX)")
		return
	}
	if !s.yandex.jobs.Cancel(yandexUser(r), r.PathValue("id")) {
		writeErr(w, 404, "not_found", "нет такой загрузки")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
