package media

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/playback"
)

type flightEntry struct {
	done chan struct{}
	err  error
}

type mobileFlight struct {
	mu   sync.Mutex
	wait map[int64]*flightEntry
}

// Every profile runtime has its own media.Service but they share one cache
// directory, so de-duplication and the ffmpeg budget are process-wide.
var (
	sharedMobileFlight mobileFlight
	transcodeSlots     = make(chan struct{}, max(2, runtime.NumCPU()/2))
)

func (f *mobileFlight) do(id int64, fn func() error) error {
	f.mu.Lock()
	if f.wait == nil {
		f.wait = map[int64]*flightEntry{}
	}
	if e, ok := f.wait[id]; ok {
		f.mu.Unlock()
		<-e.done
		return e.err
	}
	e := &flightEntry{done: make(chan struct{})}
	f.wait[id] = e
	f.mu.Unlock()

	e.err = fn()

	f.mu.Lock()
	delete(f.wait, id)
	close(e.done)
	f.mu.Unlock()
	return e.err
}

func (s *Service) mobileBitrate() string {
	bitrate := strings.TrimSpace(s.cfg.MobileBitrate)
	if bitrate == "" {
		return "160k"
	}
	return bitrate
}

func (s *Service) mobileFormat() string {
	format := strings.ToLower(strings.TrimSpace(s.cfg.MobileFormat))
	if format == "mp3" {
		return "mp3"
	}
	return "aac"
}

func (s *Service) mobileCachePath(id int64, format string) string {
	bitrate := strings.TrimSuffix(strings.ToLower(s.mobileBitrate()), "k")
	ext := ".m4a"
	if format == "mp3" {
		ext = ".mp3"
	}
	return filepath.Join(s.streamCacheDir(), fmt.Sprintf("%d_%sk%s", id, bitrate, ext))
}

func mobileContentType(format string) string {
	if format == "mp3" {
		return "audio/mpeg"
	}
	return "audio/mp4"
}

// Tiny already-lossy files need no re-encode.
func skipMobileTranscode(srcPath string) bool {
	switch strings.ToLower(filepath.Ext(srcPath)) {
	case ".mp3", ".m4a", ".aac":
		st, err := os.Stat(srcPath)
		if err != nil {
			return false
		}
		return st.Size() > 0 && st.Size() <= 4<<20
	default:
		return false
	}
}

func (s *Service) lookupMobileCache(id int64, srcPath string) (path, ctype string, ok bool) {
	if skipMobileTranscode(srcPath) {
		return srcPath, contentType(srcPath), true
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", "", false
	}
	format := s.mobileFormat()
	dst := s.mobileCachePath(id, format)
	if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
		return dst, mobileContentType(format), true
	}
	if format == "aac" {
		mp3Path := s.mobileCachePath(id, "mp3")
		if dstInfo, err := os.Stat(mp3Path); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
			return mp3Path, mobileContentType("mp3"), true
		}
	}
	return "", "", false
}

func (s *Service) ensureMobileFile(id int64, srcPath string) (path, ctype string, err error) {
	if path, ctype, ok := s.lookupMobileCache(id, srcPath); ok {
		return path, ctype, nil
	}

	format := s.mobileFormat()
	dst := s.mobileCachePath(id, format)
	ctype = mobileContentType(format)
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", "", err
	}

	ferr := sharedMobileFlight.do(id, func() error {
		if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
			return nil
		}
		return s.transcodeMobile(srcPath, dst, format)
	})
	if ferr != nil {
		if format == "aac" {
			mp3Path := s.mobileCachePath(id, "mp3")
			mp3Type := mobileContentType("mp3")
			if dstInfo, err := os.Stat(mp3Path); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
				return mp3Path, mp3Type, nil
			}
			if err := sharedMobileFlight.do(-id, func() error {
				return s.transcodeMobile(srcPath, mp3Path, "mp3")
			}); err != nil {
				return "", "", ferr
			}
			return mp3Path, mp3Type, nil
		}
		return "", "", ferr
	}
	return dst, ctype, nil
}

func (s *Service) transcodeMobile(src, dst, format string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	ffmpeg := s.cfg.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	transcodeSlots <- struct{}{}
	defer func() { <-transcodeSlots }()
	tmpFile, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	_ = tmpFile.Close()

	bitrate := s.mobileBitrate()
	var args []string
	if format == "mp3" {
		args = []string{
			"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-vn",
			"-acodec", "libmp3lame", "-b:a", bitrate, "-ar", "44100", "-ac", "2",
			"-f", "mp3", tmp,
		}
	} else {
		args = []string{
			"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-vn",
			"-c:a", "aac", "-b:a", bitrate, "-ar", "44100", "-ac", "2",
			"-movflags", "+faststart",
			"-f", "mp4", tmp,
		}
	}
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffmpeg mobile: %s", message)
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("ffmpeg mobile: empty output")
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// streamWarm keeps prepared mobile audio for the current and next tracks in
// memory. One cache serves every profile runtime (track bytes are not
// personal); it is bounded by total size so memory does not grow with the
// number of open profiles.
type streamWarm struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int
	order    []int64
	data     map[int64]warmEntry
}

const warmCacheBytes = 64 << 20

var sharedWarm = newStreamWarm(warmCacheBytes)

type warmEntry struct {
	bytes []byte
	ctype string
	mtime time.Time
}

func newStreamWarm(maxBytes int) *streamWarm {
	return &streamWarm{maxBytes: maxBytes, data: map[int64]warmEntry{}}
}

func (cache *streamWarm) get(id int64) (warmEntry, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.data[id]
	if !ok {
		return warmEntry{}, false
	}
	for i, itemID := range cache.order {
		if itemID == id {
			cache.order = append(cache.order[:i], cache.order[i+1:]...)
			break
		}
	}
	cache.order = append(cache.order, id)
	return entry, true
}

func (cache *streamWarm) put(id int64, entry warmEntry) {
	if len(entry.bytes) > cache.maxBytes {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if old, ok := cache.data[id]; ok {
		cache.bytes -= len(old.bytes)
		for i, itemID := range cache.order {
			if itemID == id {
				cache.order = append(cache.order[:i], cache.order[i+1:]...)
				break
			}
		}
	}
	cache.data[id] = entry
	cache.bytes += len(entry.bytes)
	cache.order = append(cache.order, id)
	for cache.bytes > cache.maxBytes && len(cache.order) > 1 {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		cache.bytes -= len(cache.data[oldest].bytes)
		delete(cache.data, oldest)
	}
}

func (s *Service) loadWarmFromFile(id int64, path, ctype string) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	st, _ := os.Stat(path)
	mtime := time.Now()
	if st != nil {
		mtime = st.ModTime()
	}
	s.warm.put(id, warmEntry{bytes: raw, ctype: ctype, mtime: mtime})
}

func (s *Service) ensureWarm(id int64) {
	if id == 0 {
		return
	}
	if _, ok := s.warm.get(id); ok {
		return
	}
	row, ok := s.idx.RowOf(id)
	if !ok {
		return
	}
	src := s.idx.MetaAt(row).Path
	path, ctype, err := s.ensureMobileFile(id, src)
	if err != nil {
		log.Printf("warm track %d: %v", id, err)
		return
	}
	s.loadWarmFromFile(id, path, ctype)
}

func collectWarmIDs(sess *playback.Session) []int64 {
	if sess == nil {
		return nil
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(sess.Current)
	for i, item := range sess.Queue {
		if i >= 6 {
			break
		}
		add(item.TrackID)
	}
	if len(sess.DailyIDs) > 0 {
		for i := sess.DailyPos; i < len(sess.DailyIDs) && len(ids) < 7; i++ {
			add(sess.DailyIDs[i])
		}
	}
	return ids
}

// Warm asynchronously prepares the current and upcoming session tracks.
func (s *Service) Warm(sess *playback.Session) {
	ids := collectWarmIDs(sess)
	if len(ids) == 0 {
		return
	}
	go func(ids []int64) {
		for _, id := range ids {
			s.ensureWarm(id)
		}
	}(append([]int64(nil), ids...))
}

func serveFile(w http.ResponseWriter, r *http.Request, path, ctype string) {
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "file missing", http.StatusNotFound)
		return
	}
	defer file.Close()
	stat, _ := file.Stat()
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	} else {
		w.Header().Set("Content-Type", contentType(path))
	}
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), file)
}

func (s *Service) serveMobileCached(w http.ResponseWriter, r *http.Request, id int64, srcPath string) bool {
	if entry, ok := s.warm.get(id); ok && len(entry.bytes) > 0 {
		w.Header().Set("Content-Type", entry.ctype)
		http.ServeContent(w, r, "track"+strconv.FormatInt(id, 10), entry.mtime, bytes.NewReader(entry.bytes))
		return true
	}
	if path, ctype, ok := s.lookupMobileCache(id, srcPath); ok {
		go s.loadWarmFromFile(id, path, ctype)
		serveFile(w, r, path, ctype)
		return true
	}
	return false
}

// ServeMobile prepares one compatible representation before serving any bytes.
// Android players commonly start with Range; that must not select the original
// FLAC/Opus file or change the representation between successive range requests.
func (s *Service) ServeMobile(w http.ResponseWriter, r *http.Request, id int64, srcPath string) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=86400")

	if s.serveMobileCached(w, r, id, srcPath) {
		return
	}
	path, ctype, err := s.ensureMobileFile(id, srcPath)
	if err != nil {
		log.Printf("mobile transcode %d: %v", id, err)
		if os.IsNotExist(err) {
			http.Error(w, "file missing", http.StatusNotFound)
		} else {
			http.Error(w, "mobile audio preparation failed", http.StatusBadGateway)
		}
		return
	}
	serveFile(w, r, path, ctype)
}
