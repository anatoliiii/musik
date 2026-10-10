package index

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
)

// Catalog is the immutable, profile-independent part of the index: track
// metadata, normalized embeddings and the artist/album lookups built from
// them. One Catalog is shared by every profile runtime of a process; a
// refresh builds a new one and the old one is dropped once nothing holds it.
type Catalog struct {
	ids          []int64
	meta         []Meta // catalog fields only; per-profile counters are zero
	matrix       []float32
	n, d         int
	idRow        map[int64]int
	md5ToIDs     map[string][]int64
	songKeyToIDs map[string][]int64
	artistRows   map[string][]int
	albumRows    map[string][]int
	artistAlbums map[string][]int
	artists      []GroupCentroid
	albums       []GroupCentroid
	// stamps[row] identifies the stored embedding (features.computed_at and
	// its dimension), so a refresh reuses vectors that did not change.
	stamps []string
}

func embeddingStamp(r db.TrackRow) string {
	if r.EmbeddingStamp == "" {
		return ""
	}
	return fmt.Sprintf("%s/%d", r.EmbeddingStamp, r.Dim)
}

// buildCatalog builds a catalog from rows ordered by id. vector returns the
// normalized embedding of row i (it may reuse a vector from a previous catalog).
func buildCatalog(rows []db.TrackRow, dim int, vector func(i int, r db.TrackRow) ([]float32, error)) (*Catalog, error) {
	n := len(rows)
	c := &Catalog{
		ids:          make([]int64, n),
		meta:         make([]Meta, n),
		matrix:       make([]float32, n*dim),
		n:            n,
		d:            dim,
		idRow:        make(map[int64]int, n),
		md5ToIDs:     make(map[string][]int64),
		songKeyToIDs: make(map[string][]int64),
		artistRows:   make(map[string][]int),
		albumRows:    make(map[string][]int),
		artistAlbums: make(map[string][]int),
		stamps:       make([]string, n),
	}
	if n == 0 {
		c.d = 0
		return c, nil
	}
	artistNames := make(map[string]string)
	albumNames := make(map[string][2]string)
	for i, r := range rows {
		vec, err := vector(i, r)
		if err != nil {
			return nil, err
		}
		copy(c.matrix[i*dim:(i+1)*dim], vec[:dim])
		c.ids[i] = r.ID
		c.stamps[i] = embeddingStamp(r)
		c.meta[i] = Meta{
			ID: r.ID, Path: r.Path, Title: r.Title, Artist: r.Artist,
			Artists: r.ArtistSegments, AlbumArtist: r.AlbumArtist, Album: r.Album,
			Duration: r.Duration, FileMD5: r.FileMD5, CreatedAt: parseTime(r.CreatedAt),
			ArtworkPath: r.ArtworkPath, ClusterID: r.ClusterID,
			Year: r.Year, BPM: r.BPM, LUFS: r.LUFS, HasBPM: r.HasBPM, HasLUFS: r.HasLUFS,
			KeyName: r.KeyName,
		}
		c.idRow[r.ID] = i
		if r.FileMD5 != "" {
			c.md5ToIDs[r.FileMD5] = append(c.md5ToIDs[r.FileMD5], r.ID)
		}
		if key := SongKey(r.Artist, r.Title); key != "" {
			c.songKeyToIDs[key] = append(c.songKeyToIDs[key], r.ID)
		}
		albumKey := normName(r.Album)
		// A credited album (a compilation) is one album of its album artist;
		// otherwise every credited track artist owns the album.
		if owner := strings.TrimSpace(r.AlbumArtist); owner != "" && albumKey != "" {
			aKey := normName(owner) + "\x00" + albumKey
			c.artistAlbums[aKey] = append(c.artistAlbums[aKey], i)
			if _, ok := albumNames[aKey]; !ok {
				albumNames[aKey] = [2]string{owner, strings.TrimSpace(r.Album)}
			}
		}
		for _, name := range trackArtistNames(r.Artist, r.ArtistSegments) {
			key := normName(name)
			if key == "" {
				continue
			}
			c.artistRows[key] = append(c.artistRows[key], i)
			if _, ok := artistNames[key]; !ok {
				artistNames[key] = name
			}
			if albumKey != "" && strings.TrimSpace(r.AlbumArtist) == "" {
				aKey := key + "\x00" + albumKey
				c.artistAlbums[aKey] = append(c.artistAlbums[aKey], i)
				if _, ok := albumNames[aKey]; !ok {
					albumNames[aKey] = [2]string{name, strings.TrimSpace(r.Album)}
				}
			}
		}
		if albumKey != "" {
			c.albumRows[albumKey] = append(c.albumRows[albumKey], i)
		}
	}
	c.artists = buildGroupCentroids(c.matrix, dim, c.artistRows, func(key string) (string, string) {
		return artistNames[key], ""
	})
	c.albums = buildGroupCentroids(c.matrix, dim, c.artistAlbums, func(key string) (string, string) {
		names := albumNames[key]
		return names[0], names[1]
	})
	return c, nil
}

func rowDim(r db.TrackRow) int {
	if r.Dim > 0 {
		return r.Dim
	}
	return len(r.Embedding) / 4
}

// decodeEmbedding returns the normalized embedding stored in blob.
func decodeEmbedding(id int64, blob []byte, dim int) ([]float32, error) {
	vec := BytesToFloat32(blob)
	if len(vec) < dim {
		return nil, fmt.Errorf("short embedding track %d", id)
	}
	vec = vec[:dim]
	Normalize(vec)
	return vec, nil
}

// BuildCatalog builds a catalog from rows that carry their embeddings.
func BuildCatalog(rows []db.TrackRow) (*Catalog, error) {
	if len(rows) == 0 {
		return buildCatalog(nil, 0, nil)
	}
	dim := rowDim(rows[0])
	return buildCatalog(rows, dim, func(_ int, r db.TrackRow) ([]float32, error) {
		if d := rowDim(r); d != dim {
			return nil, fmt.Errorf("inconsistent embedding dim: %d vs %d", d, dim)
		}
		return decodeEmbedding(r.ID, r.Embedding, dim)
	})
}

// RefreshCatalog builds the next catalog from rows without embeddings. A
// track whose embedding stamp matches prev keeps prev's vector; the rest are
// read through fetch, so a reload after a scan reads only the new vectors.
func RefreshCatalog(prev *Catalog, rows []db.TrackRow, fetch func(ids []int64) (map[int64][]byte, error)) (*Catalog, error) {
	if len(rows) == 0 {
		return buildCatalog(nil, 0, nil)
	}
	dim := rows[0].Dim
	reuse := func(r db.TrackRow) []float32 {
		if prev == nil || prev.d != dim {
			return nil
		}
		row, ok := prev.idRow[r.ID]
		if !ok || prev.stamps[row] == "" || prev.stamps[row] != embeddingStamp(r) {
			return nil
		}
		return prev.matrix[row*dim : (row+1)*dim]
	}
	var missing []int64
	for _, r := range rows {
		if r.Dim != dim {
			return nil, fmt.Errorf("inconsistent embedding dim: %d vs %d", r.Dim, dim)
		}
		if reuse(r) == nil {
			missing = append(missing, r.ID)
		}
	}
	blobs := map[int64][]byte{}
	if len(missing) > 0 {
		var err error
		if blobs, err = fetch(missing); err != nil {
			return nil, err
		}
	}
	return buildCatalog(rows, dim, func(_ int, r db.TrackRow) ([]float32, error) {
		if vec := reuse(r); vec != nil {
			return vec, nil
		}
		blob, ok := blobs[r.ID]
		if !ok {
			return nil, fmt.Errorf("embedding of track %d disappeared during reload", r.ID)
		}
		return decodeEmbedding(r.ID, blob, dim)
	})
}

// CatalogSource keeps the process-wide catalog of one database.
type CatalogSource struct {
	store *db.Store
	mu    sync.Mutex
	cur   *Catalog
}

func NewCatalogSource(store *db.Store) *CatalogSource {
	return &CatalogSource{store: store}
}

// Refresh rereads the catalog from the database, reusing unchanged vectors.
func (s *CatalogSource) Refresh() (*Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked()
}

func (s *CatalogSource) refreshLocked() (*Catalog, error) {
	rows, err := s.store.LoadCatalogRows()
	if err != nil {
		return nil, err
	}
	next, err := RefreshCatalog(s.cur, rows, s.store.LoadEmbeddings)
	if err != nil {
		return nil, err
	}
	s.cur = next
	return next, nil
}

// Current returns the loaded catalog, reading it on first use.
func (s *CatalogSource) Current() (*Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		return s.cur, nil
	}
	return s.refreshLocked()
}

// Stats holds one profile's counters for a track.
type Stats struct {
	Shown        int
	SkipEarly    int
	Completed    int
	Plays        int
	Finishes     int
	EarlySkips   int
	LastPlayedAt time.Time
}

func statsOf(r db.TrackStats) Stats {
	return Stats{
		Shown: r.Shown, SkipEarly: r.SkipEarly, Completed: r.Completed,
		Plays: r.Plays, Finishes: r.Finishes, EarlySkips: r.EarlySkips,
		LastPlayedAt: parseTime(r.LastPlayedAt),
	}
}

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	if t.IsZero() {
		t, _ = time.Parse(time.RFC3339, value)
	}
	return t
}
