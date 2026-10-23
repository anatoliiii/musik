package media

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
)

func TestExplicitMP3TranscodesSmallALACAndHonorsRange(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	src := filepath.Join(root, "small.m4a")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "alac", src).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s %v", out, err)
	}
	original, _ := os.ReadFile(src)
	if !skipMobileTranscode(src) {
		t.Fatal("fixture must exercise the small-original shortcut")
	}
	s := newService(config.Config{DBPath: filepath.Join(root, "db", "musik.db"), MobileFormat: "aac", FFmpegPath: ffmpeg}, nil)
	for _, query := range []string{"?q=mp3", "?fmt=mp3"} {
		req := httptest.NewRequest("GET", "/api/stream/1"+query, nil)
		req.Header.Set("Range", "bytes=0-1023")
		w := httptest.NewRecorder()
		s.ServeMobile(w, req, 1, src)
		if w.Code != 206 || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.Len() != 1024 {
			t.Fatalf("%s status=%d headers=%v size=%d", query, w.Code, w.Header(), w.Body.Len())
		}
		if bytes.Equal(w.Body.Bytes(), original[:1024]) {
			t.Fatal("returned ALAC original")
		}
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(original, after) {
		t.Fatal("original changed")
	}
}
