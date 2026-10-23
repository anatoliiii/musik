package playback

import (
	"fmt"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
)

func TestRadioAnchorHoldsAndSteps(t *testing.T) {
	engine := anchoredEngine(t)
	seed := int64(11)
	session := engine.Start("radio", &seed, nil)
	if session.ID == "" {
		t.Fatal("missing session id")
	}
	session.Lock()
	if session.Anchor.Kind != "track" || session.Anchor.TrackID != 11 || session.Anchor.Label != "North" {
		t.Fatalf("anchor=%+v", session.Anchor)
	}
	if session.Anchor.Proximity != ProximityNear {
		t.Fatalf("proximity=%q, want near", session.Anchor.Proximity)
	}
	if session.StationName() != "радио от: North" {
		t.Fatalf("name=%q", session.StationName())
	}
	if session.Current != 11 {
		t.Fatalf("current=%d, want seed", session.Current)
	}
	near := cosine(engine.tasteForQueue(session), []float32{1, 0})
	session.Anchor.Proximity = ProximityClose
	closeSim := cosine(engine.tasteForQueue(session), []float32{1, 0})
	session.Anchor.Proximity = ProximityWide
	wideSim := cosine(engine.tasteForQueue(session), []float32{1, 0})
	session.Anchor.Proximity = ProximityNear
	if !(closeSim > near && near > wideSim) {
		t.Fatalf("anchor pull close=%v near=%v wide=%v", closeSim, near, wideSim)
	}
	if wideSim > 0.2 {
		t.Fatalf("wide radio still hugs the anchor: %v", wideSim)
	}
	for i := 0; i < 2; i++ {
		if engine.Advance(session) == 0 {
			t.Fatal("radio ended before the anchor could be checked")
		}
	}
	if session.Anchor.TrackID != 11 || session.Anchor.Kind != "track" {
		t.Fatalf("anchor dissolved: %+v", session.Anchor)
	}
	session.Unlock()

	row, ok, err := engine.Store.LoadPlaySession(session.ID)
	if err != nil || !ok {
		t.Fatalf("load session ok=%v err=%v", ok, err)
	}
	restored := hydrate(row)
	if restored.Anchor.TrackID != 11 || restored.Anchor.Kind != "track" || restored.Anchor.Label != "North" {
		t.Fatalf("restored anchor=%+v", restored.Anchor)
	}

	same := engine.Start("radio", nil, nil, RadioStart{Session: session, Proximity: "closer"})
	if same.ID != session.ID {
		t.Fatalf("tune opened session %s, want %s", same.ID, session.ID)
	}
	same.Lock()
	if same.Anchor.Proximity != ProximityClose {
		t.Fatalf("after closer proximity=%q", same.Anchor.Proximity)
	}
	current := same.Current
	same.Unlock()

	engine.Start("radio", nil, nil, RadioStart{Session: session, Proximity: "wider"})
	session.Lock()
	if session.Anchor.Proximity != ProximityNear || session.Current != current {
		t.Fatalf("wider from close: proximity=%q current=%d", session.Anchor.Proximity, session.Current)
	}
	session.Unlock()

	engine.Start("radio", nil, nil, RadioStart{Session: session, Proximity: "wider"})
	session.Lock()
	defer session.Unlock()
	if session.Anchor.Proximity != ProximityWide || session.Anchor.Kind != "track" {
		t.Fatalf("wide anchor=%+v", session.Anchor)
	}
	if session.StationName() != "радио" {
		t.Fatalf("wide name=%q, want ordinary radio", session.StationName())
	}
}

func TestRadioAnchorForArtistAndAlbum(t *testing.T) {
	engine := anchoredEngine(t)
	artist := engine.Start("radio", nil, nil, RadioStart{Artist: "North"})
	artist.Lock()
	if artist.Anchor.Kind != "artist" || artist.Anchor.Artist != "North" || artist.Anchor.Proximity != ProximityNear {
		t.Fatalf("artist anchor=%+v", artist.Anchor)
	}
	if artist.StationName() != "радио от: North" {
		t.Fatalf("artist name=%q", artist.StationName())
	}
	meta := engine.Idx.MetaAt(mustRow(t, engine, artist.Current))
	if meta.Artist != "North" {
		t.Fatalf("artist radio started on %s — %s", meta.Artist, meta.Title)
	}
	artist.Unlock()

	album := engine.Start("radio", nil, nil, RadioStart{Artist: "South", Album: "Warm"})
	album.Lock()
	defer album.Unlock()
	if album.Anchor.Kind != "album" || album.Anchor.Album != "Warm" || album.Anchor.Label != "Warm" {
		t.Fatalf("album anchor=%+v", album.Anchor)
	}
	meta = engine.Idx.MetaAt(mustRow(t, engine, album.Current))
	if meta.Artist != "South" || meta.Album != "Warm" {
		t.Fatalf("album radio started on %s — %s (%s)", meta.Artist, meta.Title, meta.Album)
	}
	if album.StationName() != "радио от: Warm" {
		t.Fatalf("album name=%q", album.StationName())
	}
}

func anchoredEngine(t *testing.T) *Engine {
	t.Helper()
	engine := testEngine(t)
	tp := taste.New()
	tp.SetWithMeta([]float32{0, 1}, 20, 0, "test")
	cfg := config.Config{QueueSize: 4, ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0}
	idx := index.New(cfg)
	rows := []db.TrackRow{
		{ID: 11, Title: "North", Artist: "North", Album: "Cold", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 12, Title: "North two", Artist: "North", Album: "Cold", Embedding: index.Float32Bytes([]float32{0.95, 0.05}), Dim: 2},
		{ID: 22, Title: "South", Artist: "South", Album: "Warm", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 33, Title: "South two", Artist: "South", Album: "Warm", Embedding: index.Float32Bytes([]float32{0.05, 0.95}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	built := New(cfg, engine.Store, idx, tp, queue.NewBuilder(idx, cfg))
	for _, id := range []int64{11, 12, 22, 33} {
		if _, err := built.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, duration, created_at, updated_at) VALUES (?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, fmt.Sprintf("/anchor-%d.flac", id), "T", "Artist", 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	return built
}

func TestForgottenRadioAnchorsStaleFinishes(t *testing.T) {
	engine := forgottenEngine(t)
	if engine.ForgottenCount() != 2 {
		t.Fatalf("forgotten count = %d, want 2", engine.ForgottenCount())
	}
	session := engine.Start("radio", nil, nil, RadioStart{Station: "forgotten"})
	session.Lock()
	defer session.Unlock()
	if session.Anchor.Kind != "forgotten" || session.Anchor.Label != "забытое" || session.Anchor.Proximity != ProximityNear {
		t.Fatalf("anchor=%+v", session.Anchor)
	}
	if session.StationName() != "радио от: забытое" {
		t.Fatalf("name=%q", session.StationName())
	}
	if session.Current != 11 {
		t.Fatalf("current=%d, want the most-finished stale track", session.Current)
	}
	plain := tasteVector(engine)
	pulled := cosine(engine.tasteForQueue(session), []float32{1, 0})
	if pulled <= cosine(plain, []float32{1, 0}) {
		t.Fatalf("forgotten anchor did not pull taste toward the stale tracks: pulled=%v plain=%v", pulled, cosine(plain, []float32{1, 0}))
	}
}

func TestForgottenRadioWithoutCandidates(t *testing.T) {
	engine := anchoredEngine(t)
	if engine.ForgottenCount() != 0 {
		t.Fatalf("count=%d", engine.ForgottenCount())
	}
	session := engine.Start("radio", nil, nil, RadioStart{Station: "forgotten"})
	session.Lock()
	defer session.Unlock()
	if session.Anchor.Kind != "" {
		t.Fatalf("anchor=%+v, want none", session.Anchor)
	}
}

func forgottenEngine(t *testing.T) *Engine {
	t.Helper()
	engine := testEngine(t)
	tp := taste.New()
	tp.SetWithMeta([]float32{0, 1}, 20, 0, "test")
	cfg := config.Config{QueueSize: 4, ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0}
	rows := []db.TrackRow{
		{ID: 11, Title: "Old hit", Artist: "North", Album: "Cold", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 12, Title: "Old two", Artist: "North", Album: "Cold", Embedding: index.Float32Bytes([]float32{0.9, 0.1}), Dim: 2},
		{ID: 22, Title: "Recent", Artist: "South", Album: "Warm", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 33, Title: "Barely", Artist: "South", Album: "Warm", Embedding: index.Float32Bytes([]float32{0.1, 0.9}), Dim: 2},
	}
	cat, err := index.BuildCatalog(rows)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -120).Format(time.RFC3339Nano)
	recent := time.Now().UTC().AddDate(0, 0, -2).Format(time.RFC3339Nano)
	idx := index.New(cfg)
	idx.Attach(cat, map[int64]db.TrackStats{
		11: {Finishes: 5, Plays: 6, LastFinishedAt: old, LastPlayedAt: recent},
		12: {Finishes: 4, Plays: 4, LastFinishedAt: old},
		22: {Finishes: 8, Plays: 9, LastFinishedAt: recent},
		33: {Finishes: 1, Plays: 2, LastFinishedAt: old},
	})
	built := New(cfg, engine.Store, idx, tp, queue.NewBuilder(idx, cfg))
	for _, row := range rows {
		if _, err := built.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, duration, created_at, updated_at) VALUES (?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			row.ID, fmt.Sprintf("/forgotten-%d.flac", row.ID), row.Title, row.Artist, 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	return built
}

func tasteVector(engine *Engine) []float32 {
	return engine.Taste.Get()
}

func mustRow(t *testing.T, engine *Engine, id int64) int {
	t.Helper()
	row, ok := engine.Idx.RowOf(id)
	if !ok {
		t.Fatalf("track %d is not in the index", id)
	}
	return row
}

func cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}
