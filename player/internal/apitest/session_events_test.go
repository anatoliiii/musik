package apitest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestSessionStartNowAndEvents(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("POST", "/api/session/start", `{"seed_track_id":11}`))
	if rec.Code != 200 {
		t.Fatalf("session start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		SessionID string `json:"session_id"`
		Current   struct {
			ID int64 `json:"id"`
		} `json:"current"`
		Queue []any `json:"queue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.SessionID == "" || started.Current.ID != 11 {
		t.Fatalf("started=%+v", started)
	}
	flush(server)

	rec = serve(server, jsonReq("GET", "/api/now?session_id="+started.SessionID, ""))
	if rec.Code != 200 {
		t.Fatalf("now status=%d", rec.Code)
	}

	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"like","track_id":11,"session_id":"`+started.SessionID+`"}`))
	if rec.Code != 200 {
		t.Fatalf("like status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"track_end","track_id":11,"session_id":"`+started.SessionID+`","duration_sec":180,"listened_sec":170,"reason":"completed"}`))
	if rec.Code != 200 {
		t.Fatalf("track_end status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ended struct {
		OK     bool  `json:"ok"`
		NextID int64 `json:"next_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ended); err != nil {
		t.Fatal(err)
	}
	if !ended.OK || ended.NextID == 0 {
		t.Fatalf("expected next track after end: %+v", ended)
	}
	flush(server)

	var listens int
	if err := server.Store.DB.QueryRow(`SELECT COUNT(*) FROM listening_history`).Scan(&listens); err != nil {
		t.Fatal(err)
	}
	if listens < 1 {
		t.Fatal("events should write listening history")
	}
}

func TestSecondTrackEndKeepsCurrentAndReportsIgnored(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/radio/start", `{"seed_track_id":11}`))
	if rec.Code != 200 {
		t.Fatalf("radio start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		SessionID string `json:"session_id"`
		Current   struct {
			ID           int64  `json:"id"`
			ImpressionID string `json:"impression_id"`
		} `json:"current"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.SessionID == "" || started.Current.ID == 0 || started.Current.ImpressionID == "" {
		t.Fatalf("radio start: %v body=%s", err, rec.Body.String())
	}
	session := started.SessionID
	impression := started.Current.ImpressionID
	track := fmt.Sprintf("%d", started.Current.ID)

	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"track_start","event_id":"start-1","track_id":`+track+`,"session_id":"`+session+`","impression_id":"`+impression+`"}`))
	if rec.Code != 200 {
		t.Fatalf("track_start status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"track_end","event_id":"close-1","track_id":`+track+`,"session_id":"`+session+`","impression_id":"`+impression+`","duration_sec":180,"listened_sec":170,"reason":"completed"}`))
	if rec.Code != 200 {
		t.Fatalf("track_end status=%d body=%s", rec.Code, rec.Body.String())
	}
	var closed struct {
		OK      bool  `json:"ok"`
		Ignored bool  `json:"ignored"`
		NextID  int64 `json:"next_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &closed); err != nil {
		t.Fatal(err)
	}
	if !closed.OK || closed.Ignored || closed.NextID == 0 {
		t.Fatalf("first close=%+v", closed)
	}

	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"track_end","event_id":"close-2","track_id":`+track+`,"session_id":"`+session+`","impression_id":"`+impression+`","duration_sec":180,"listened_sec":20,"reason":"completed"}`))
	if rec.Code != 200 {
		t.Fatalf("second track_end status=%d body=%s", rec.Code, rec.Body.String())
	}
	var again struct {
		OK      bool   `json:"ok"`
		Ignored bool   `json:"ignored"`
		Reason  string `json:"reason"`
		NextID  int64  `json:"next_id"`
		Next    struct {
			ID int64 `json:"id"`
		} `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if !again.OK || !again.Ignored || again.Reason != "already_closed" || again.NextID != closed.NextID || again.Next.ID != closed.NextID {
		t.Fatalf("second close should keep %d and be ignored: %+v", closed.NextID, again)
	}
}

func TestEventsRequireSession(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/events", `{"type":"like","track_id":11}`))
	if rec.Code == 200 {
		t.Fatal("events without session should fail")
	}
}

func TestStatusIncludesSessionSnapshot(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/radio/start", `{}`))
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	rec = serve(server, jsonReq("GET", "/api/status?session_id="+started.SessionID, ""))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["session_id"] != started.SessionID {
		t.Fatalf("status session snapshot missing: %#v", body)
	}
}

func TestContextPatchStoresZeroInfluence(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/contexts", `{"kind":"mood","name":"night","influence":1.5}`))
	if rec.Code != 200 {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID        string  `json:"context_id"`
		Influence float64 `json:"influence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create: %v body=%s", err, rec.Body.String())
	}
	if created.Influence != 1.5 {
		t.Fatalf("influence=%v, want 1.5", created.Influence)
	}

	rec = serve(server, jsonReq("PATCH", "/api/contexts/"+created.ID, `{"name":"later"}`))
	if rec.Code != 200 {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renamed struct {
		Name      string  `json:"name"`
		Influence float64 `json:"influence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "later" || renamed.Influence != 1.5 {
		t.Fatalf("rename=%+v, want name later and influence 1.5", renamed)
	}

	rec = serve(server, jsonReq("PATCH", "/api/contexts/"+created.ID, `{"influence":0}`))
	if rec.Code != 200 {
		t.Fatalf("zero status=%d body=%s", rec.Code, rec.Body.String())
	}
	var zeroed struct {
		Name      string  `json:"name"`
		Influence float64 `json:"influence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &zeroed); err != nil {
		t.Fatal(err)
	}
	if zeroed.Name != "later" || zeroed.Influence != 0 {
		t.Fatalf("zeroed=%+v, want name later and influence 0", zeroed)
	}
}

func TestRadioStartAndNowReturnContextIDs(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/contexts", `{"kind":"mood","name":"night"}`))
	if rec.Code != 200 {
		t.Fatalf("create context status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"context_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create context: %v body=%s", err, rec.Body.String())
	}

	rec = serve(server, jsonReq("POST", "/api/radio/start", `{"context_ids":["`+created.ID+`"]}`))
	if rec.Code != 200 {
		t.Fatalf("radio start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		SessionID  string   `json:"session_id"`
		ContextIDs []string `json:"context_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.SessionID == "" || len(started.ContextIDs) != 1 || started.ContextIDs[0] != created.ID {
		t.Fatalf("start context_ids=%v, want [%s]", started.ContextIDs, created.ID)
	}

	rec = serve(server, jsonReq("GET", "/api/now?session_id="+started.SessionID, ""))
	if rec.Code != 200 {
		t.Fatalf("now status=%d body=%s", rec.Code, rec.Body.String())
	}
	var now struct {
		ContextIDs []string `json:"context_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &now); err != nil {
		t.Fatal(err)
	}
	if len(now.ContextIDs) != 1 || now.ContextIDs[0] != created.ID {
		t.Fatalf("now context_ids=%v, want [%s]", now.ContextIDs, created.ID)
	}
}

func TestRadioStartRecordsImpressions(t *testing.T) {
	server := openTestServer(t)
	loadIndex(t, server, []db.TrackRow{
		{ID: 1, Artist: "Massive Attack", Album: "Mezzanine", Title: "Angel", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "Portishead", Album: "Dummy", Title: "Glory Box", Embedding: index.Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 3, Artist: "Bjork", Album: "Homogenic", Title: "Joga", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
	})
	for _, id := range []int64{1, 2, 3} {
		if _, err := server.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, created_at, updated_at) VALUES (?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z') ON CONFLICT DO NOTHING`,
			id, fmt.Sprintf("/radio-%d.flac", id), "T",
		); err != nil {
			t.Fatal(err)
		}
	}

	rec := serve(server, jsonReq("POST", "/api/radio/start", `{}`))
	if rec.Code != 200 {
		t.Fatalf("radio status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		SessionID string `json:"session_id"`
		Current   struct {
			ID           int64  `json:"id"`
			ImpressionID string `json:"impression_id"`
			Source       string `json:"source"`
		} `json:"current"`
		Queue []struct {
			ImpressionID string `json:"impression_id"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Current.ImpressionID == "" || started.Current.Source == "" {
		t.Fatalf("current missing impression: %+v", started.Current)
	}
	if len(started.Queue) == 0 || started.Queue[0].ImpressionID == "" {
		t.Fatalf("queue missing impression: %+v", started.Queue)
	}
	flush(server)

	var n int
	if err := server.Store.DB.QueryRow(`SELECT COUNT(*) FROM recommendation_impressions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("radio queue refresh should persist recommendation impressions")
	}
}

func TestAmbiguousImpressionFallbackConflicts(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("POST", "/api/radio/start", `{"seed_track_id":11}`))
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	flush(server)
	for i := 0; i < 2; i++ {
		if err := server.Store.CreateRecommendationRequest(db.RecommendationRequest{
			RequestID: db.NewID(), SessionID: started.SessionID, Reason: "refill",
			PolicyVersion: "test", CandidateCount: 1,
		}, []db.RecommendationImpression{{
			ImpressionID: db.NewID(), SessionID: started.SessionID, TrackID: 11,
			Source: "exploit",
		}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	rec = serve(server, jsonReq("POST", "/api/events",
		`{"type":"track_start","event_id":"amb-1","track_id":11,"session_id":"`+started.SessionID+`"}`))
	if rec.Code != 409 {
		t.Fatalf("ambiguous fallback status=%d body=%s", rec.Code, rec.Body.String())
	}
}
