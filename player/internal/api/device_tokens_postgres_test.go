package api

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
)

func TestPostgresDeviceTokenPreservesLegacyMobileRequestsWhenConfigured(t *testing.T) {
	databaseURL := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("set MUSIK_TEST_POSTGRES_URL to run PostgreSQL mobile token contract test")
	}
	store, err := db.OpenDatabase(databaseURL, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	userID, profile, err := store.CreateUserWithDefaultProfile(ctx, "PostgreSQL mobile token test")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf, err := store.IssueUserSession(ctx, userID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(audioPath, []byte("0123456789abcdefghijklmnopqrstuvwxyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	artworkPath := filepath.Join(dir, "cover.jpg")
	cover := image.NewRGBA(image.Rect(0, 0, 300, 200))
	cover.Set(0, 0, color.RGBA{R: 200, A: 255})
	artworkFile, err := os.Create(artworkPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(artworkFile, cover, nil); err != nil {
		t.Fatal(err)
	}
	if err := artworkFile.Close(); err != nil {
		t.Fatal(err)
	}
	var trackID int64
	if err := store.DB.QueryRow(`SELECT nextval(pg_get_serial_sequence('tracks','id'))`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB.Exec(
		`INSERT INTO tracks(id,path,title,artist,album,duration,artwork_path,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		trackID, audioPath, "PostgreSQL test track", "Test artist", "Test album", 30, artworkPath, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(
		`INSERT INTO features(track_id,embedding,embedding_dim,status,cluster_id) VALUES (?,?,2,'ready',0)`,
		trackID, index.Float32Bytes([]float32{1, 0}),
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM device_tokens WHERE user_id=?`,
			`DELETE FROM auth_sessions WHERE user_id=?`,
		} {
			if _, err := store.DB.Exec(query, userID); err != nil {
				t.Errorf("revoke PostgreSQL mobile fixture credentials: %v", err)
			}
		}
	})

	cfg := config.Config{
		DataDir: dir, QueueSize: 6, MultiUser: true,
		PublicBaseURL: "https://musik.test", WorkerURL: "http://127.0.0.1:1",
		WorkerAutostart: false,
	}
	server := New(cfg, store, index.New(cfg), taste.New(), nil)
	server.multi = &multiUserState{profiles: make(map[string]*Server)}
	handler := server.Handler()
	call := func(method, path, body, browserCookie, csrfToken, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://musik.test"+path, strings.NewReader(body))
		if browserCookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: browserCookie})
		}
		if csrfToken != "" {
			req.Header.Set("X-CSRF-Token", csrfToken)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if method == http.MethodPost || method == http.MethodDelete || method == http.MethodPatch {
			req.Header.Set("Origin", "https://musik.test")
			req.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check := func(rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
		}
	}

	created := call(http.MethodPost, "/api/account/device-tokens", `{"name":"PostgreSQL phone"}`, cookie, csrf, "")
	check(created, http.StatusOK)
	var tokenResponse struct {
		Device db.DeviceTokenInfo `json:"device"`
		Token  string             `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &tokenResponse); err != nil || tokenResponse.Token == "" {
		t.Fatalf("create response=%s err=%v", created.Body.String(), err)
	}
	me := call(http.MethodGet, "/api/auth/me", "", "", "", tokenResponse.Token)
	check(me, http.StatusOK)
	var meBody map[string]any
	if err := json.Unmarshal(me.Body.Bytes(), &meBody); err != nil || len(meBody) != 2 || meBody["ok"] != true || meBody["auth_enabled"] != true {
		t.Fatalf("legacy auth/me body=%s err=%v", me.Body.String(), err)
	}
	check(call(http.MethodGet, "/api/favorites", "", "", "", tokenResponse.Token), http.StatusOK)

	streamRequest := httptest.NewRequest(http.MethodGet, "https://musik.test/api/stream/"+strconv.FormatInt(trackID, 10), nil)
	streamRequest.Header.Set("Authorization", "Bearer "+tokenResponse.Token)
	streamRequest.Header.Set("Range", "bytes=2-5")
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, streamRequest)
	check(stream, http.StatusPartialContent)
	if stream.Body.String() != "2345" {
		t.Fatalf("Range response body=%q", stream.Body.String())
	}

	artwork := call(http.MethodGet, "/api/artwork/"+strconv.FormatInt(trackID, 10)+"?w=96", "", "", "", tokenResponse.Token)
	check(artwork, http.StatusOK)
	if artwork.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("artwork content-type=%q", artwork.Header().Get("Content-Type"))
	}
	started := call(http.MethodPost, "/api/session/start", `{"seed_track_id":`+strconv.FormatInt(trackID, 10)+`}`, "", "", tokenResponse.Token)
	check(started, http.StatusOK)
	var playbackSession struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &playbackSession); err != nil || playbackSession.SessionID == "" {
		t.Fatalf("session response=%s err=%v", started.Body.String(), err)
	}
	legacyEvent := call(http.MethodPost, "/api/events", `{"type":"like","session_id":"`+playbackSession.SessionID+`","track_id":`+strconv.FormatInt(trackID, 10)+`}`, "", "", tokenResponse.Token)
	check(legacyEvent, http.StatusOK)
	var likeCount int
	if err := store.DB.QueryRow(`SELECT likes FROM track_stats WHERE track_id=? AND profile_id=?`, trackID, profile.ID).Scan(&likeCount); err != nil || likeCount != 1 {
		t.Fatalf("legacy event track stats likes=%d err=%v", likeCount, err)
	}
	createdTokenID := tokenResponse.Device.ID
	revoked := call(http.MethodDelete, "/api/account/device-tokens/"+createdTokenID, "", cookie, csrf, "")
	check(revoked, http.StatusOK)
	check(call(http.MethodGet, "/api/favorites", "", "", "", tokenResponse.Token), http.StatusUnauthorized)
}
