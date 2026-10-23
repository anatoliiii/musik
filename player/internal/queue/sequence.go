package queue

import (
	"math"
	"strings"

	"github.com/torwin-job/musik/player/internal/index"
)

type TransitionProfile string

const (
	ProfileSmooth    TransitionProfile = "smooth"
	ProfileDiverse   TransitionProfile = "diverse"
	ProfileEnergetic TransitionProfile = "energetic"
	ProfileCalm      TransitionProfile = "calm"
)

type TransitionWeights struct {
	CLAP       float64
	Outcome    float64
	BPM        float64
	LUFS       float64
	Key        float64
	Energy     float64
	Repeat     float64
	Similarity float64
}

func WeightsForProfile(profile TransitionProfile) TransitionWeights {
	raw, ok := ActiveSpec.Profiles[string(profile)]
	if !ok {
		raw = ActiveSpec.Profiles[string(ProfileSmooth)]
	}
	return raw.weights()
}

type TransitionInputs struct {
	From          index.Meta
	To            index.Meta
	CLAP          float64
	OutcomeWeight float64
	Manual        bool
	Learned       *TransitionModel
}

func PairwiseScore(in TransitionInputs, profile TransitionProfile) (float64, map[string]float64) {
	w := WeightsForProfile(profile)
	bpm, _ := bpmDistance(&in.From, in.To)
	lufs, _ := lufsDistance(&in.From, in.To)
	key := keyCompat(&in.From, in.To)
	energy := energyDiff(&in.From, in.To)
	repeat := 0.0
	if sameArtist(in.From, in.To) {
		repeat += 1
	}
	if sameAlbum(in.From, in.To) {
		repeat += 0.5
	}
	outcome := in.OutcomeWeight
	if in.Manual {
		outcome *= 1.5
	}
	features := map[string]float64{
		"clap": in.CLAP, "outcome": outcome, "bpm": bpm, "lufs": lufs,
		"key": key, "energy": energy, "repeat": repeat,
	}
	if score, ok := scoreLearnedTransition(in.Learned, features); ok {
		return score, features
	}
	score := w.CLAP*in.CLAP + w.Outcome*outcome + w.Key*key + w.Energy*energy -
		w.BPM*bpm - w.LUFS*lufs - w.Repeat*repeat
	return score, features
}

func sameArtist(from, to index.Meta) bool {
	return from.Artist != "" && strings.EqualFold(from.Artist, to.Artist)
}

func sameAlbum(from, to index.Meta) bool {
	return from.Album != "" && strings.EqualFold(from.Album, to.Album)
}

// TransitionFeatures is the current→candidate step that beam already scores.
// Artist and album repeats are 1 when that credit matches. Score is PairwiseScore.
// A missing current track leaves all three at zero.
func TransitionFeatures(from *index.Meta, to index.Meta, clap, outcome float64, profile TransitionProfile, learned *TransitionModel) (artist, album, score float64) {
	if from == nil || from.ID == 0 {
		return 0, 0, 0
	}
	if sameArtist(*from, to) {
		artist = 1
	}
	if sameAlbum(*from, to) {
		album = 1
	}
	score, _ = PairwiseScore(TransitionInputs{
		From: *from, To: to, CLAP: clap, OutcomeWeight: outcome, Learned: learned,
	}, profile)
	return artist, album, score
}

type SequenceChoice struct {
	Items   []ScoredCandidate
	Score   float64
	Explain []map[string]any
}

type sequenceNode struct {
	path  []int
	used  map[int]bool
	score float64
	expl  []map[string]any
}

type sequenceOption struct {
	idx   int
	total float64
	pair  float64
	feats map[string]float64
}

func BeamSequence(
	idx *index.Index,
	currentID int64,
	cands []ScoredCandidate,
	size, width int,
	profile TransitionProfile,
	transitions map[int64]float64,
	lambda float64,
	learned *TransitionModel,
) SequenceChoice {
	if size < 1 {
		size = 6
	}
	if width < 1 {
		width = 6
	}
	if lambda <= 0 {
		lambda = 0.35
	}
	if len(cands) == 0 {
		return SequenceChoice{}
	}

	startMeta := index.Meta{}
	if row, ok := idx.RowOf(currentID); ok {
		startMeta = idx.MetaAt(row)
	}
	beams := []sequenceNode{{used: map[int]bool{}, score: 0}}

	for step := 0; step < size; step++ {
		var next []sequenceNode
		for _, beam := range beams {
			from := startMeta
			if len(beam.path) > 0 {
				from = idx.MetaAt(cands[beam.path[len(beam.path)-1]].Row)
			}
			seenArtist := map[string]int{}
			for _, p := range beam.path {
				seenArtist[strings.ToLower(idx.MetaAt(cands[p].Row).Artist)]++
			}
			options := make([]sequenceOption, 0, len(cands))
			for i, cand := range cands {
				if beam.used[i] {
					continue
				}
				to := idx.MetaAt(cand.Row)
				clap := 0.0
				if from.ID != 0 {
					if row, ok := idx.RowOf(from.ID); ok {
						clap = float64(idx.Dot(cand.Row, idx.Vector(row)))
					}
				}
				pair, feats := PairwiseScore(TransitionInputs{
					From: from, To: to, CLAP: clap, OutcomeWeight: transitions[to.ID], Learned: learned,
				}, profile)
				rep := float64(seenArtist[strings.ToLower(to.Artist)])
				simPen := 0.0
				if len(beam.path) > 0 {
					prev := idx.Vector(cands[beam.path[len(beam.path)-1]].Row)
					simPen = math.Max(0, float64(dot32(prev, idx.Vector(cand.Row))))
				}
				w := WeightsForProfile(profile)
				total := beam.score + cand.Score + lambda*pair - 0.2*rep - w.Similarity*simPen
				options = append(options, sequenceOption{idx: i, total: total, pair: pair, feats: feats})
			}
			sortSequenceOptions(options)
			limit := width
			if limit > len(options) {
				limit = len(options)
			}
			for _, opt := range options[:limit] {
				used := map[int]bool{}
				for k, v := range beam.used {
					used[k] = v
				}
				used[opt.idx] = true
				expl := append([]map[string]any{}, beam.expl...)
				expl = append(expl, map[string]any{
					"track_id": cands[opt.idx].TrackID,
					"unary":    cands[opt.idx].Score,
					"pairwise": opt.pair,
					"features": opt.feats,
					"source":   cands[opt.idx].Primary,
				})
				next = append(next, sequenceNode{
					path: append(append([]int{}, beam.path...), opt.idx),
					used: used, score: opt.total, expl: expl,
				})
			}
		}
		if len(next) == 0 {
			break
		}
		sortSequenceNodes(next)
		if len(next) > width {
			next = next[:width]
		}
		beams = next
	}
	if len(beams) == 0 {
		return SequenceChoice{}
	}
	best := beams[0]
	items := make([]ScoredCandidate, 0, len(best.path))
	for _, i := range best.path {
		items = append(items, cands[i])
	}
	return SequenceChoice{Items: items, Score: best.score, Explain: best.expl}
}

func sortSequenceOptions(opts []sequenceOption) {
	for i := 0; i < len(opts); i++ {
		for j := i + 1; j < len(opts); j++ {
			if opts[j].total > opts[i].total {
				opts[i], opts[j] = opts[j], opts[i]
			}
		}
	}
}

func sortSequenceNodes(nodes []sequenceNode) {
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[j].score > nodes[i].score {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
}
