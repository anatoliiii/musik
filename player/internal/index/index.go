package index

import (
	"container/heap"
	"encoding/binary"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

type Meta struct {
	ID     int64
	Path   string
	Title  string
	Artist string
	// Artists holds the collaborator segments from the database (row.artists).
	Artists []string
	// AlbumArtist is the album-level credit; empty when the tags have none.
	AlbumArtist  string
	Album        string
	Duration     float64
	FileMD5      string
	CreatedAt    time.Time
	ArtworkPath  string
	ClusterID    int
	Shown        int
	SkipEarly    int
	Completed    int
	Year         int
	BPM          float64
	LUFS         float64
	HasBPM       bool
	HasLUFS      bool
	KeyName      string
	Plays        int
	Finishes     int
	EarlySkips   int
	LastPlayedAt time.Time
}

type Index struct {
	mu sync.RWMutex
	// IDs, Meta and Matrix belong to the shared catalog and are read-only.
	// Meta carries catalog fields only; MetaAt adds this profile's counters.
	IDs    []int64
	Meta   []Meta
	Matrix []float32 // row-major N*D
	N      int
	D      int
	idRow  map[int64]int
	// Clone lookups rebuilt on Load — excludeTrack uses these, not a full scan.
	md5ToIDs     map[string][]int64
	songKeyToIDs map[string][]int64
	artistRows   map[string][]int
	albumRows    map[string][]int
	artistAlbums map[string][]int
	artists      []GroupCentroid
	albums       []GroupCentroid
	cfg          config.Config
	catalog      *Catalog
	stats        []Stats // this profile's counters, by row
}

type ScoredRow struct {
	Row   int
	Score float32
}

type GroupCentroid struct {
	Artist string
	Album  string
	Rows   []int
	Vector []float32
}

func New(cfg config.Config) *Index {
	return &Index{
		cfg:          cfg,
		idRow:        map[int64]int{},
		md5ToIDs:     map[string][]int64{},
		songKeyToIDs: map[string][]int64{},
		artistRows:   map[string][]int{},
		albumRows:    map[string][]int{},
		artistAlbums: map[string][]int{},
	}
}

// SongKey normalizes artist|title for clone grouping.
func SongKey(artist, title string) string {
	a := strings.ToLower(strings.TrimSpace(artist))
	t := strings.ToLower(strings.TrimSpace(title))
	if a == "" || t == "" {
		return ""
	}
	return a + "|" + t
}

// Load builds a private catalog from rows that carry embeddings and the
// counters of one profile. Servers share a catalog through Attach instead.
func (idx *Index) Load(rows []db.TrackRow) error {
	cat, err := BuildCatalog(rows)
	if err != nil {
		return err
	}
	stats := make(map[int64]db.TrackStats, len(rows))
	for _, r := range rows {
		stats[r.ID] = db.TrackStats{
			Shown: r.Shown, SkipEarly: r.SkipEarly, Completed: r.Completed,
			Plays: r.Plays, Finishes: r.Finishes, EarlySkips: r.EarlySkips,
			LastPlayedAt: r.LastPlayedAt,
		}
	}
	idx.Attach(cat, stats)
	return nil
}

// Attach points the index at a shared catalog and sets this profile's
// counters. Only the counters are allocated per profile.
func (idx *Index) Attach(cat *Catalog, stats map[int64]db.TrackStats) {
	perRow := make([]Stats, cat.n)
	for id, st := range stats {
		if row, ok := cat.idRow[id]; ok {
			perRow[row] = statsOf(st)
		}
	}
	idx.mu.Lock()
	idx.catalog = cat
	idx.IDs, idx.Meta, idx.Matrix = cat.ids, cat.meta, cat.matrix
	idx.N, idx.D = cat.n, cat.d
	idx.idRow = cat.idRow
	idx.md5ToIDs = cat.md5ToIDs
	idx.songKeyToIDs = cat.songKeyToIDs
	idx.artistRows = cat.artistRows
	idx.albumRows = cat.albumRows
	idx.artistAlbums = cat.artistAlbums
	idx.artists = cat.artists
	idx.albums = cat.albums
	idx.stats = perRow
	idx.mu.Unlock()
}

func (idx *Index) statsLocked(row int) Stats {
	if row < 0 || row >= len(idx.stats) {
		return Stats{}
	}
	return idx.stats[row]
}

// CloneIDs returns trackID plus all MD5 / artist+title duplicates (deduped).
func (idx *Index) CloneIDs(trackID int64) []int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	row, ok := idx.idRow[trackID]
	if !ok {
		return []int64{trackID}
	}
	m := idx.Meta[row]
	seen := map[int64]bool{trackID: true}
	out := []int64{trackID}
	add := func(ids []int64) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if m.FileMD5 != "" {
		add(idx.md5ToIDs[m.FileMD5])
	}
	if key := SongKey(m.Artist, m.Title); key != "" {
		add(idx.songKeyToIDs[key])
	}
	return out
}

func (idx *Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.N
}

func (idx *Index) Dim() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.D
}

func (idx *Index) RowOf(id int64) (int, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	r, ok := idx.idRow[id]
	return r, ok
}

func (idx *Index) Vector(row int) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return nil
	}
	out := make([]float32, idx.D)
	copy(out, idx.Matrix[row*idx.D:(row+1)*idx.D])
	return out
}

func (idx *Index) Centroid() []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.N == 0 {
		return nil
	}
	rows := make([]int, idx.N)
	for i := range rows {
		rows[i] = i
	}
	return idx.centroidOfLocked(rows)
}

// CentroidOf returns the L2-normalized mean embedding of the given rows.
func (idx *Index) CentroidOf(rows []int) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.centroidOfLocked(rows)
}

func (idx *Index) centroidOfLocked(rows []int) []float32 {
	if idx.N == 0 || idx.D == 0 || len(rows) == 0 {
		return nil
	}
	sum := make([]float64, idx.D)
	n := 0
	for _, i := range rows {
		if i < 0 || i >= idx.N {
			continue
		}
		off := i * idx.D
		for d := 0; d < idx.D; d++ {
			sum[d] += float64(idx.Matrix[off+d])
		}
		n++
	}
	if n == 0 {
		return nil
	}
	out := make([]float32, idx.D)
	inv := 1.0 / float64(n)
	for d := 0; d < idx.D; d++ {
		out[d] = float32(sum[d] * inv)
	}
	Normalize(out)
	return out
}

// RowsForArtist returns index rows whose artist matches (case-insensitive trim).
func (idx *Index) RowsForArtist(artist string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	want := normName(artist)
	if want == "" {
		return nil
	}
	return append([]int(nil), idx.artistRows[want]...)
}

// RowsForAlbum returns index rows for artist+album (album alone if artist empty).
func (idx *Index) RowsForAlbum(artist, album string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	wantA := normName(artist)
	wantAl := normName(album)
	if wantAl == "" {
		return nil
	}
	if wantA == "" {
		return append([]int(nil), idx.albumRows[wantAl]...)
	}
	return append([]int(nil), idx.artistAlbums[wantA+"\x00"+wantAl]...)
}

// normName lowercases and trims an artist/album key.
func normName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		return unicode.ToLower(r)
	}, s)
}

func buildGroupCentroids(
	matrix []float32,
	dim int,
	groups map[string][]int,
	names func(string) (string, string),
) []GroupCentroid {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]GroupCentroid, 0, len(keys))
	for _, key := range keys {
		rows := groups[key]
		vec := make([]float32, dim)
		for _, row := range rows {
			off := row * dim
			for d := 0; d < dim; d++ {
				vec[d] += matrix[off+d]
			}
		}
		if len(rows) > 0 {
			inv := float32(1) / float32(len(rows))
			for d := range vec {
				vec[d] *= inv
			}
		}
		Normalize(vec)
		artist, album := names(key)
		out = append(out, GroupCentroid{
			Artist: artist,
			Album:  album,
			Rows:   append([]int(nil), rows...),
			Vector: vec,
		})
	}
	return out
}

func (idx *Index) ArtistCentroids() []GroupCentroid {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return cloneGroups(idx.artists)
}

func (idx *Index) AlbumCentroids() []GroupCentroid {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return cloneGroups(idx.albums)
}

func cloneGroups(groups []GroupCentroid) []GroupCentroid {
	out := make([]GroupCentroid, len(groups))
	for i, group := range groups {
		out[i] = GroupCentroid{
			Artist: group.Artist,
			Album:  group.Album,
			Rows:   append([]int(nil), group.Rows...),
			Vector: append([]float32(nil), group.Vector...),
		}
	}
	return out
}

type scoreHeap []ScoredRow

func (h scoreHeap) Len() int { return len(h) }
func (h scoreHeap) Less(i, j int) bool {
	if h[i].Score == h[j].Score {
		return h[i].Row > h[j].Row
	}
	return h[i].Score < h[j].Score
}
func (h scoreHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *scoreHeap) Push(x any)   { *h = append(*h, x.(ScoredRow)) }
func (h *scoreHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// TopK returns exact cosine neighbors without materializing or sorting all N scores.
func (idx *Index) TopK(vec []float32, limit int, exclude map[int64]bool) []ScoredRow {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if limit <= 0 || idx.N == 0 || len(vec) != idx.D {
		return nil
	}
	if limit > idx.N {
		limit = idx.N
	}
	h := make(scoreHeap, 0, limit)
	for row := 0; row < idx.N; row++ {
		if exclude != nil && exclude[idx.Meta[row].ID] {
			continue
		}
		off := row * idx.D
		var score float32
		for d := 0; d < idx.D; d++ {
			score += idx.Matrix[off+d] * vec[d]
		}
		item := ScoredRow{Row: row, Score: score}
		if len(h) < limit {
			heap.Push(&h, item)
			continue
		}
		worst := h[0]
		if score > worst.Score || (score == worst.Score && row < worst.Row) {
			h[0] = item
			heap.Fix(&h, 0)
		}
	}
	out := append([]ScoredRow(nil), h...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Row < out[j].Row
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func (idx *Index) SimsTo(vec []float32) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]float32, idx.N)
	if idx.N == 0 || len(vec) != idx.D {
		return out
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	// Parallel only pays off on larger libraries.
	if idx.N < 1500 || workers < 2 {
		for i := 0; i < idx.N; i++ {
			off := i * idx.D
			var s float32
			for d := 0; d < idx.D; d++ {
				s += idx.Matrix[off+d] * vec[d]
			}
			out[i] = s
		}
		return out
	}
	var wg sync.WaitGroup
	chunk := (idx.N + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo := w * chunk
		hi := lo + chunk
		if hi > idx.N {
			hi = idx.N
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				off := i * idx.D
				var s float32
				for d := 0; d < idx.D; d++ {
					s += idx.Matrix[off+d] * vec[d]
				}
				out[i] = s
			}
		}(lo, hi)
	}
	wg.Wait()
	return out
}

// FusedSimsTo computes exact cosine scores for several queries while each
// matrix row is hot. Results are query-major: result[q][row].
func (idx *Index) FusedSimsTo(vectors ...[]float32) [][]float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([][]float32, len(vectors))
	for q, vec := range vectors {
		out[q] = make([]float32, idx.N)
		if len(vec) != idx.D {
			return out
		}
	}
	for row := 0; row < idx.N; row++ {
		off := row * idx.D
		for d := 0; d < idx.D; d++ {
			value := idx.Matrix[off+d]
			for q, vec := range vectors {
				out[q][row] += value * vec[d]
			}
		}
	}
	return out
}

// Dot returns cosine/dot of row i with vec (vectors assumed L2-normalized).
func (idx *Index) Dot(row int, vec []float32) float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N || len(vec) != idx.D {
		return 0
	}
	off := row * idx.D
	var s float32
	for d := 0; d < idx.D; d++ {
		s += idx.Matrix[off+d] * vec[d]
	}
	return s
}

func (idx *Index) MetaAt(row int) Meta {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return Meta{}
	}
	m := idx.Meta[row]
	st := idx.statsLocked(row)
	m.Shown, m.SkipEarly, m.Completed = st.Shown, st.SkipEarly, st.Completed
	m.Plays, m.Finishes, m.EarlySkips, m.LastPlayedAt = st.Plays, st.Finishes, st.EarlySkips, st.LastPlayedAt
	return m
}

func (idx *Index) NewBoost(row int, now time.Time) float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return 0
	}
	m := idx.Meta[row]
	st := idx.statsLocked(row)
	if st.SkipEarly >= 3 {
		return 0
	}
	if m.CreatedAt.IsZero() {
		return 0
	}
	ageDays := now.Sub(m.CreatedAt).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	if ageDays > float64(idx.cfg.NewTrackDays) {
		return 0
	}
	tau := idx.cfg.NewBoostTauDays
	if tau < 1 {
		tau = 14
	}
	gamma := idx.cfg.NewBoostGamma
	if gamma < 1 {
		gamma = 5
	}
	boost := idx.cfg.NewBoostBeta * math.Exp(-ageDays/tau) * math.Exp(-float64(st.Shown)/gamma)
	return float32(boost)
}

func (idx *Index) BumpShownLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok && r < len(idx.stats) {
		idx.stats[r].Shown++
	}
}

func (idx *Index) BumpSkipEarlyLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok && r < len(idx.stats) {
		idx.stats[r].SkipEarly++
	}
}

func (idx *Index) BumpCompletedLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok && r < len(idx.stats) {
		idx.stats[r].Completed++
	}
}

func BytesToFloat32(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(b[i*4 : (i+1)*4])
		out[i] = math.Float32frombits(bits)
	}
	return out
}

func Float32Bytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:(i+1)*4], math.Float32bits(f))
	}
	return b
}

func Normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	if s < 1e-12 {
		return
	}
	inv := float32(1 / s)
	for i := range v {
		v[i] *= inv
	}
}

// AlbumOwner is who the track's album is listed under: the album-level
// credit when the tags carry one, else the primary credited artist.
func (m Meta) AlbumOwner() string {
	if owner := strings.TrimSpace(m.AlbumArtist); owner != "" {
		return owner
	}
	if len(m.Artists) > 0 {
		return strings.TrimSpace(m.Artists[0])
	}
	return strings.TrimSpace(m.Artist)
}
