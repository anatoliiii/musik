package playback

import (
	_ "embed"
	"encoding/json"
)

const (
	MixSourceGenerated = "generated"
	MixSourceLater     = "later"
	MixSourceFavorites = "favorites"
)

// Mix is one shelf. Mixes is the only registry: the HTTP shelf, GeneratedMixKind
// and the mix_pack payload the worker builds all come from mixes.json.
type Mix struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Source   string `json:"source"`
	// Weekday is Monday=0 … Sunday=6 for weekday shelves.
	Weekday *int `json:"weekday,omitempty"`
}

// MixContract is one generated shelf in a mix_pack job. The worker builds
// these kinds in order and rejects a kind it has no generator for.
type MixContract struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Weekday *int   `json:"weekday,omitempty"`
}

//go:embed mixes.json
var mixContractJSON []byte

// Mixes is the shelf, in display order.
var Mixes []Mix

func init() {
	if err := json.Unmarshal(mixContractJSON, &Mixes); err != nil {
		panic("playback mix contract: " + err.Error())
	}
}

func MixByKind(kind string) (Mix, bool) {
	for _, mix := range Mixes {
		if mix.Kind == kind {
			return mix, true
		}
	}
	return Mix{}, false
}

// GeneratedMixKind is a shelf the worker builds. Manual and saved lists are not.
func GeneratedMixKind(kind string) bool {
	mix, ok := MixByKind(kind)
	return ok && mix.Source == MixSourceGenerated
}

// GeneratedMixContract is the mix list a mix_pack job sends the worker.
func GeneratedMixContract() []MixContract {
	out := make([]MixContract, 0, len(Mixes))
	for _, mix := range Mixes {
		if mix.Source != MixSourceGenerated {
			continue
		}
		item := MixContract{Kind: mix.Kind, Title: mix.Title}
		if mix.Weekday != nil {
			day := *mix.Weekday
			item.Weekday = &day
		}
		out = append(out, item)
	}
	return out
}
