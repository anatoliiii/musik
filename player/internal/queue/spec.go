package queue

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed ranker_spec.json
var rankerSpecJSON []byte

// RankerSpec is the shared Go/Python contract (schema_version 3): feature order,
// source thresholds, quotas, MMR and transition profiles. Weights live in model_default.json.
type RankerSpec struct {
	SchemaVersion int      `json:"schema_version"`
	FeatureOrder  []string `json:"feature_order"`
	Thresholds    struct {
		ExploitTaste      float64 `json:"exploit_taste"`
		ExploitCentroid   float64 `json:"exploit_centroid"`
		TransitionCurrent float64 `json:"transition_current"`
		AdjacentTaste     float64 `json:"adjacent_taste"`
	} `json:"thresholds"`
	Resurface struct {
		MinFinishes int     `json:"min_finishes"`
		MinHours    float64 `json:"min_hours"`
	} `json:"resurface"`
	NewInLibrary struct {
		MaxAgeHours float64 `json:"max_age_hours"`
		MaxPlays    int     `json:"max_plays"`
	} `json:"new_in_library"`
	Quotas         map[string]specQuota   `json:"quotas"`
	DiscoverQuotas map[string]specQuota   `json:"discover_quotas"`
	MMRLambda      float64                `json:"mmr_lambda"`
	ArtistPenalty  float64                `json:"artist_penalty"`
	Retrieval      specRetrieval          `json:"retrieval"`
	NewBoost       specBoost              `json:"new_boost"`
	Profiles       map[string]specProfile `json:"transition_profiles"`
}

type specQuota struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type specRetrieval struct {
	SourcePool        int     `json:"source_pool"`
	BPMWindow         float64 `json:"bpm_window"`
	KeyMin            float64 `json:"key_min"`
	NegativeClearance float64 `json:"negative_clearance"`
	CentroidMix       float64 `json:"centroid_mix"`
	ClusterSkipSeries int     `json:"cluster_skip_series"`
}

type specBoost struct {
	Days         float64 `json:"days"`
	Beta         float64 `json:"beta"`
	TauDays      float64 `json:"tau_days"`
	Gamma        float64 `json:"gamma"`
	SkipEarlyCap int     `json:"skip_early_cap"`
}

type specProfile struct {
	CLAP       float64 `json:"clap"`
	Outcome    float64 `json:"outcome"`
	BPM        float64 `json:"bpm"`
	LUFS       float64 `json:"lufs"`
	Key        float64 `json:"key"`
	Energy     float64 `json:"energy"`
	Repeat     float64 `json:"repeat"`
	Similarity float64 `json:"similarity"`
}

func (p specProfile) weights() TransitionWeights {
	return TransitionWeights{
		CLAP: p.CLAP, Outcome: p.Outcome, BPM: p.BPM, LUFS: p.LUFS,
		Key: p.Key, Energy: p.Energy, Repeat: p.Repeat, Similarity: p.Similarity,
	}
}

// ActiveSpec is loaded from ranker_spec.json before any queue is built.
var ActiveSpec RankerSpec

func init() {
	spec, err := parseSpec(rankerSpecJSON)
	if err != nil {
		panic(err)
	}
	ActiveSpec = spec
	FeatureOrder = append([]string(nil), spec.FeatureOrder...)
	DefaultQuotas = quotaMap(spec.Quotas)
	DiscoverQuotas = quotaMap(spec.DiscoverQuotas)
}

func parseSpec(raw []byte) (RankerSpec, error) {
	var spec RankerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("invalid ranker spec json")
	}
	if spec.SchemaVersion != FeatureSchemaVersion {
		return spec, fmt.Errorf("ranker spec schema_version %d", spec.SchemaVersion)
	}
	if len(spec.FeatureOrder) == 0 {
		return spec, fmt.Errorf("ranker spec feature_order is empty")
	}
	for _, src := range sourcePriority {
		if _, ok := spec.Quotas[src]; !ok {
			return spec, fmt.Errorf("ranker spec missing quota %s", src)
		}
		if _, ok := spec.DiscoverQuotas[src]; !ok {
			return spec, fmt.Errorf("ranker spec missing discover quota %s", src)
		}
	}
	for _, name := range []string{"smooth", "diverse", "energetic", "calm"} {
		if _, ok := spec.Profiles[name]; !ok {
			return spec, fmt.Errorf("ranker spec missing profile %s", name)
		}
	}
	if spec.MMRLambda <= 0 || spec.MMRLambda > 1 {
		return spec, fmt.Errorf("ranker spec mmr_lambda")
	}
	if spec.Retrieval.SourcePool < 1 {
		return spec, fmt.Errorf("ranker spec source_pool")
	}
	if spec.Retrieval.ClusterSkipSeries < 1 {
		return spec, fmt.Errorf("ranker spec cluster_skip_series")
	}
	if spec.Resurface.MinHours <= 0 || spec.NewInLibrary.MaxAgeHours <= 0 {
		return spec, fmt.Errorf("ranker spec listening windows")
	}
	return spec, nil
}

func quotaMap(raw map[string]specQuota) map[string]Quota {
	out := make(map[string]Quota, len(raw))
	for src, q := range raw {
		out[src] = Quota{Min: q.Min, Max: q.Max}
	}
	return out
}
