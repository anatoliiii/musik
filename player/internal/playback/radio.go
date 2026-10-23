package playback

import (
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
)

// A forgotten station anchors on tracks this profile finished often and then left alone:
// at least three finishes, last finished more than sixty days ago.
const (
	forgottenMinFinishes = 3
	forgottenStaleDays   = 60
)

// Proximity is how hard a radio station holds its anchor.
// near is the start. closer and wider each move one step:
// wide is ordinary taste radio, close keeps the anchor dominant.
const (
	ProximityWide  = "wide"
	ProximityNear  = "near"
	ProximityClose = "close"
)

const (
	anchorWeightNear  = float32(0.5)
	anchorWeightClose = float32(0.82)
)

// RadioAnchor is the track, artist, album, or forgotten set a radio session
// was started from. The queue keeps blending its vector until the mode changes.
type RadioAnchor struct {
	Kind      string `json:"kind,omitempty"`
	TrackID   int64  `json:"track_id,omitempty"`
	Artist    string `json:"artist,omitempty"`
	Album     string `json:"album,omitempty"`
	Label     string `json:"label,omitempty"`
	Proximity string `json:"proximity,omitempty"`
}

// RadioStart is the optional station anchor for Start.
// Session, when set, retunes that radio session instead of opening a new one.
// Station "forgotten" anchors on tracks with high finishes and an old last finish.
type RadioStart struct {
	Artist    string
	Album     string
	Proximity string
	Session   *Session
	Station   string
}

// StationName is the player label: "радио от: …" while the anchor is held,
// and "радио" once proximity is wide or there is no anchor.
func (s *Session) StationName() string {
	if s == nil || s.Mode != "radio" {
		if s == nil {
			return ""
		}
		return s.PlaylistName
	}
	if s.Anchor.Kind != "" && s.Anchor.Proximity != ProximityWide && s.Anchor.Label != "" {
		return "радио от: " + s.Anchor.Label
	}
	return "радио"
}

func (e *Engine) attachAnchor(sess *Session, seed *int64, opt RadioStart) {
	if sess == nil || sess.Mode != "radio" {
		return
	}
	artist := strings.TrimSpace(opt.Artist)
	album := strings.TrimSpace(opt.Album)
	var anchor RadioAnchor
	switch {
	case album != "":
		if e.Idx.CentroidOf(e.Idx.RowsForAlbum(artist, album)) == nil {
			return
		}
		anchor = RadioAnchor{Kind: "album", Artist: artist, Album: album, Label: album}
	case artist != "":
		if e.Idx.CentroidOf(e.Idx.RowsForArtist(artist)) == nil {
			return
		}
		anchor = RadioAnchor{Kind: "artist", Artist: artist, Label: artist}
	case opt.Station == "forgotten":
		if e.Idx.CentroidOf(e.forgottenRows()) == nil {
			return
		}
		anchor = RadioAnchor{Kind: "forgotten", Label: "забытое"}
	case seed != nil && *seed != 0:
		anchor = RadioAnchor{Kind: "track", TrackID: *seed, Label: e.trackLabel(*seed)}
	default:
		return
	}
	anchor.Proximity = initialProximity(opt.Proximity)
	sess.Anchor = anchor
}

func (e *Engine) trackLabel(id int64) string {
	row, ok := e.Idx.RowOf(id)
	if !ok {
		return "этот трек"
	}
	m := e.Idx.MetaAt(row)
	if title := strings.TrimSpace(m.Title); title != "" {
		return title
	}
	if artist := strings.TrimSpace(m.Artist); artist != "" {
		return artist
	}
	return "этот трек"
}

func initialProximity(request string) string {
	switch request {
	case "closer", ProximityClose:
		return ProximityClose
	case "wider", ProximityWide:
		return ProximityWide
	default:
		return ProximityNear
	}
}

// stepProximity moves one step. closer strengthens the anchor, wider loosens
// it, and the ends stay put.
func stepProximity(current, request string) string {
	switch request {
	case "closer":
		if current == ProximityWide || current == "" {
			return ProximityNear
		}
		return ProximityClose
	case "wider":
		if current == ProximityClose {
			return ProximityNear
		}
		return ProximityWide
	case ProximityClose, ProximityNear, ProximityWide:
		return request
	default:
		if current == "" {
			return ProximityNear
		}
		return current
	}
}

func (e *Engine) tuneLocked(sess *Session, proximity string) {
	if sess == nil || sess.Mode != "radio" || sess.Anchor.Kind == "" {
		return
	}
	sess.Anchor.Proximity = stepProximity(sess.Anchor.Proximity, proximity)
	e.RefreshQueue(sess, sess.Current, "radio_tune")
}

func (e *Engine) pickAnchoredStart(sess *Session, seed *int64) int64 {
	if seed != nil && e.trackMatchesAnchor(*seed, sess.Anchor) {
		if id := e.pickAllowedStart(sess, seed); id != 0 {
			return id
		}
	}
	rows := e.anchorRows(sess.Anchor)
	eval := e.sessionRules(sess)
	var fallback int64
	for _, row := range rows {
		id := e.Idx.MetaAt(row).ID
		if fallback == 0 {
			fallback = id
		}
		if sess.Exclude[id] {
			continue
		}
		if eval != nil && eval.HardBlocked(id) {
			continue
		}
		return id
	}
	if fallback != 0 {
		return fallback
	}
	return e.pickAllowedStart(sess, nil)
}

func (e *Engine) forgottenRows() []int {
	if e == nil || e.Idx == nil {
		return nil
	}
	return e.Idx.ForgottenRows(forgottenMinFinishes, time.Now().Add(-forgottenStaleDays*24*time.Hour))
}

// ForgottenCount is how many tracks can anchor a forgotten station.
func (e *Engine) ForgottenCount() int {
	return len(e.forgottenRows())
}

func (e *Engine) anchorRows(a RadioAnchor) []int {
	switch a.Kind {
	case "artist":
		return e.Idx.RowsForArtist(a.Artist)
	case "album":
		return e.Idx.RowsForAlbum(a.Artist, a.Album)
	case "forgotten":
		return e.forgottenRows()
	default:
		return nil
	}
}

func (e *Engine) trackMatchesAnchor(id int64, a RadioAnchor) bool {
	row, ok := e.Idx.RowOf(id)
	if !ok {
		return false
	}
	m := e.Idx.MetaAt(row)
	switch a.Kind {
	case "artist":
		return strings.EqualFold(strings.TrimSpace(m.Artist), strings.TrimSpace(a.Artist))
	case "album":
		return strings.EqualFold(strings.TrimSpace(m.Album), strings.TrimSpace(a.Album)) &&
			(strings.TrimSpace(a.Artist) == "" || strings.EqualFold(strings.TrimSpace(m.Artist), strings.TrimSpace(a.Artist)))
	case "track":
		return id == a.TrackID
	case "forgotten":
		row, ok := e.Idx.RowOf(id)
		if !ok {
			return false
		}
		for _, candidate := range e.forgottenRows() {
			if candidate == row {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func (e *Engine) anchorVector(sess *Session) []float32 {
	if sess == nil || e.Idx == nil {
		return nil
	}
	switch sess.Anchor.Kind {
	case "track":
		row, ok := e.Idx.RowOf(sess.Anchor.TrackID)
		if !ok {
			return nil
		}
		return e.Idx.Vector(row)
	case "artist":
		return e.Idx.CentroidOf(e.Idx.RowsForArtist(sess.Anchor.Artist))
	case "album":
		return e.Idx.CentroidOf(e.Idx.RowsForAlbum(sess.Anchor.Artist, sess.Anchor.Album))
	case "forgotten":
		return e.Idx.CentroidOf(e.forgottenRows())
	default:
		return nil
	}
}

func anchorWeight(proximity string) float32 {
	switch proximity {
	case ProximityClose:
		return anchorWeightClose
	case ProximityNear:
		return anchorWeightNear
	default:
		return 0
	}
}

// blendAnchor mixes the station anchor into the taste vector. A zero weight
// leaves taste untouched, which is ordinary radio.
func blendAnchor(base, anchor []float32, weight float32) []float32 {
	if weight <= 0 || len(anchor) == 0 {
		return base
	}
	if weight > 1 {
		weight = 1
	}
	if len(base) == 0 {
		out := append([]float32(nil), anchor...)
		index.Normalize(out)
		return out
	}
	if len(base) != len(anchor) {
		return base
	}
	out := make([]float32, len(base))
	keep := 1 - weight
	for i := range out {
		out[i] = keep*base[i] + weight*anchor[i]
	}
	index.Normalize(out)
	return out
}
