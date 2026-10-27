package yandex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
)

// Request asks for one Yandex item to be saved.
type Request struct {
	Kind string `json:"kind"` // track, album or artist
	ID   string `json:"id"`
	// Private puts the files in the profile's own folder instead of the
	// shared library.
	Private bool `json:"private"`
}

// Job is one download request and its progress.
type Job struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	ItemID  string `json:"item_id"`
	Title   string `json:"title"`
	Private bool   `json:"private"`
	Status  string `json:"status"` // queued, running, done, failed, cancelled
	Total   int    `json:"total"`
	Saved   int    `json:"saved"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	// Unavailable counts tracks Yandex does not offer (region, rights).
	Unavailable int       `json:"unavailable"`
	Error       string    `json:"error,omitempty"`
	Current     string    `json:"current,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	EndedAt     time.Time `json:"ended_at,omitzero"`

	userID string
	cancel context.CancelFunc
}

// Target is where one job writes and how it reports finished files.
type Target struct {
	UserID string
	Root   string
	Client *Client
}

// Manager runs download jobs in the background, a few at a time, and keeps
// each user's recent jobs for the status view.
type Manager struct {
	Downloader *Downloader
	Quality    string
	// Finished is called after a job saved at least one file, so the server
	// can start the usual library scan.
	Finished func(job Job)

	mu   sync.Mutex
	jobs map[string]*Job
	slot chan struct{}
}

const (
	maxRunningJobs  = 2
	keepJobsPerUser = 20
)

func NewManager(d *Downloader, quality string) *Manager {
	return &Manager{Downloader: d, Quality: quality, jobs: map[string]*Job{}, slot: make(chan struct{}, maxRunningJobs)}
}

// Start queues req for target and returns the job at once.
func (m *Manager) Start(target Target, req Request) (Job, error) {
	switch req.Kind {
	case "track", "album", "artist":
	default:
		return Job{}, errors.New("kind must be track, album or artist")
	}
	if req.ID == "" {
		return Job{}, errors.New("id required")
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID: hex.EncodeToString(raw[:]), Kind: req.Kind, ItemID: req.ID, Private: req.Private,
		Status: "queued", CreatedAt: time.Now().UTC(), userID: target.UserID, cancel: cancel,
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.pruneLocked(target.UserID)
	snapshot := *job
	m.mu.Unlock()
	go m.run(ctx, job, target)
	return snapshot, nil
}

// Jobs lists the user's jobs, newest first.
func (m *Manager) Jobs(userID string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.jobs {
		if j.userID == userID {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out
}

// Cancel stops one of the user's jobs.
func (m *Manager) Cancel(userID, jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[jobID]
	if j == nil || j.userID != userID {
		return false
	}
	j.cancel()
	return true
}

// CancelUser stops every job of the user, as on logout.
func (m *Manager) CancelUser(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.userID == userID {
			j.cancel()
		}
	}
}

func (m *Manager) pruneLocked(userID string) {
	var mine []*Job
	for _, j := range m.jobs {
		if j.userID == userID && (j.Status == "done" || j.Status == "failed" || j.Status == "cancelled") {
			mine = append(mine, j)
		}
	}
	if len(mine) <= keepJobsPerUser {
		return
	}
	sort.Slice(mine, func(a, b int) bool { return mine[a].CreatedAt.Before(mine[b].CreatedAt) })
	for _, j := range mine[:len(mine)-keepJobsPerUser] {
		delete(m.jobs, j.ID)
	}
}

func (m *Manager) update(job *Job, fn func(j *Job)) {
	m.mu.Lock()
	fn(job)
	m.mu.Unlock()
}

// item is one track to save and the album it is filed under.
type item struct {
	track Track
	place Placement
}

func (m *Manager) run(ctx context.Context, job *Job, target Target) {
	defer job.cancel()
	select {
	case m.slot <- struct{}{}:
		defer func() { <-m.slot }()
	case <-ctx.Done():
		m.finish(job, ctx.Err())
		return
	}
	m.update(job, func(j *Job) { j.Status = "running" })
	items, title, err := resolve(ctx, target.Client, job.Kind, job.ItemID)
	m.update(job, func(j *Job) { j.Title, j.Total = title, len(items) })
	if err != nil {
		m.finish(job, err)
		return
	}
	var lastErr error
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		m.update(job, func(j *Job) { j.Current = TrackTitle(it.track) })
		if it.track.Available != nil && !*it.track.Available {
			m.update(job, func(j *Job) { j.Unavailable++ })
			continue
		}
		_, err := m.Downloader.Save(ctx, target.Client, target.Root, it.track, it.place, m.Quality)
		switch {
		case err == nil:
			m.update(job, func(j *Job) { j.Saved++ })
		case errors.Is(err, ErrExists):
			m.update(job, func(j *Job) { j.Skipped++ })
		case ctx.Err() != nil:
		default:
			lastErr = err
			log.Printf("yandex: track %s: %v", it.track.ID, err)
			m.update(job, func(j *Job) { j.Failed++ })
			if errors.Is(err, ErrUnauthorized) {
				m.finish(job, err)
				return
			}
		}
	}
	if ctx.Err() != nil {
		m.finish(job, ctx.Err())
		return
	}
	m.mu.Lock()
	nothing := job.Saved == 0 && job.Skipped == 0
	m.mu.Unlock()
	if nothing && lastErr != nil {
		m.finish(job, lastErr)
		return
	}
	m.finish(job, nil)
}

func (m *Manager) finish(job *Job, err error) {
	m.update(job, func(j *Job) {
		j.Current = ""
		j.EndedAt = time.Now().UTC()
		switch {
		case errors.Is(err, context.Canceled):
			j.Status = "cancelled"
		case err != nil:
			j.Status, j.Error = "failed", err.Error()
		default:
			j.Status = "done"
		}
	})
	m.mu.Lock()
	snapshot := *job
	m.mu.Unlock()
	if snapshot.Saved > 0 && m.Finished != nil {
		m.Finished(snapshot)
	}
}

// resolve expands a request into tracks with their album placement.
func resolve(ctx context.Context, c *Client, kind, id string) ([]item, string, error) {
	switch kind {
	case "track":
		tracks, err := c.Tracks(ctx, []string{id})
		if err != nil {
			return nil, "", err
		}
		if len(tracks) == 0 {
			return nil, "", errors.New("track not found")
		}
		t := tracks[0]
		return []item{{track: t, place: PlacementFor(t, nil)}}, joinArtists(t.Artists) + " — " + TrackTitle(t), nil
	case "album":
		album, err := c.AlbumWithTracks(ctx, id)
		if err != nil {
			return nil, "", err
		}
		return albumItems(album), joinArtists(album.Artists) + " — " + AlbumTitle(album), nil
	case "artist":
		albums, err := c.ArtistAlbums(ctx, id)
		if err != nil {
			return nil, "", err
		}
		title := ""
		var out []item
		for _, a := range albums {
			if title == "" {
				title = joinArtists(a.Artists)
			}
			full, err := c.AlbumWithTracks(ctx, string(a.ID))
			if err != nil {
				if ctx.Err() != nil {
					return out, title, ctx.Err()
				}
				log.Printf("yandex: album %s: %v", a.ID, err)
				continue
			}
			out = append(out, albumItems(full)...)
		}
		if title == "" {
			if artist, err := c.Artist(ctx, id); err == nil {
				title = artist.Name
			}
		}
		return out, title, nil
	}
	return nil, "", errors.New("unknown kind")
}

func albumItems(album Album) []item {
	var out []item
	for disc, volume := range album.Volumes {
		for n, t := range volume {
			p := PlacementFor(t, &album)
			p.Disc, p.Number = disc+1, n+1
			out = append(out, item{track: t, place: p})
		}
	}
	return out
}
