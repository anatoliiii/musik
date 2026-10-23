package playback

import "time"

// StartShare builds an in-memory radio session for one listener.
// The session is not registered and is not written to play_sessions, so a
// listener cannot occupy or evict the owner's sessions.
func (e *Engine) StartShare() *Session {
	sess := &Session{
		ID:        newSessionID(),
		Mode:      "share",
		Exclude:   map[int64]bool{},
		Rated:     map[int64]string{},
		UpdatedAt: time.Now(),
		ephemeral: true,
	}
	sess.Lock()
	startID := e.PickStart(sess, nil)
	sess.Current = startID
	e.ExcludeTrack(sess, startID)
	e.RefreshQueue(sess, startID, "")
	sess.Unlock()
	return sess
}

// ShareCurrentTrackID returns the current track while preserving session locking.
func (e *Engine) ShareCurrentTrackID(sess *Session) int64 {
	if sess == nil {
		return 0
	}
	sess.Lock()
	defer sess.Unlock()
	return sess.Current
}

// AdvanceShare advances a shared radio session and restarts its queue when needed.
func (e *Engine) AdvanceShare(sess *Session) int64 {
	if sess == nil {
		return 0
	}
	sess.Lock()
	defer sess.Unlock()

	if next := e.Advance(sess); next != 0 {
		return next
	}
	next := e.PickStart(sess, nil)
	if next == 0 {
		return 0
	}
	sess.Mode = "share"
	sess.Current = next
	e.ExcludeTrack(sess, next)
	e.RefreshQueue(sess, next, "")
	return next
}
