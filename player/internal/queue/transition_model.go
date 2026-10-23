package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PairwiseFeatureOrder is the beam step the transition model learns.
// Hand weights in ranker_spec.json stay in force until a published model exists.
var PairwiseFeatureOrder = []string{"clap", "outcome", "bpm", "lufs", "key", "energy", "repeat"}

const TransitionSchemaVersion = 1

// TransitionModel is the learned probability of finishing the next track.
// The zero value means PairwiseScore keeps the hand-tuned profile weights.
type TransitionModel struct {
	SchemaVersion int       `json:"schema_version"`
	ModelVersion  string    `json:"model_version"`
	FeatureOrder  []string  `json:"feature_order"`
	Mean          []float64 `json:"mean"`
	Std           []float64 `json:"std"`
	Weights       []float64 `json:"weights"`
	Bias          float64   `json:"bias"`
	ScoreMin      float64   `json:"score_min"`
	ScoreMax      float64   `json:"score_max"`
}

func (m TransitionModel) Ready() bool {
	return len(m.Weights) == len(PairwiseFeatureOrder) && len(m.FeatureOrder) == len(PairwiseFeatureOrder)
}

func TransitionPath(rankerPath string) string {
	if rankerPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(rankerPath), "transition.json")
}

func LoadRuntimeTransition(path string) TransitionModel {
	if path == "" {
		return TransitionModel{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return TransitionModel{}
	}
	model, err := ParseTransitionModel(raw)
	if err != nil {
		return TransitionModel{}
	}
	return model
}

func ParseTransitionModel(raw []byte) (TransitionModel, error) {
	var model TransitionModel
	if err := json.Unmarshal(raw, &model); err != nil {
		return TransitionModel{}, fmt.Errorf("invalid transition model json")
	}
	if err := model.Validate(); err != nil {
		return TransitionModel{}, err
	}
	return model, nil
}

func (m TransitionModel) Validate() error {
	if m.SchemaVersion != TransitionSchemaVersion {
		return fmt.Errorf("transition schema_version %d", m.SchemaVersion)
	}
	if len(m.FeatureOrder) != len(PairwiseFeatureOrder) {
		return fmt.Errorf("transition feature_order length")
	}
	for i, name := range PairwiseFeatureOrder {
		if m.FeatureOrder[i] != name {
			return fmt.Errorf("transition feature_order mismatch at %d", i)
		}
	}
	n := len(PairwiseFeatureOrder)
	if len(m.Weights) != n || len(m.Mean) != n || len(m.Std) != n {
		return fmt.Errorf("transition vector lengths")
	}
	for i, std := range m.Std {
		if std == 0 {
			return fmt.Errorf("transition std[%s] must be non-zero", PairwiseFeatureOrder[i])
		}
	}
	if m.ModelVersion == "" {
		return fmt.Errorf("transition model_version required")
	}
	if m.ScoreMax <= m.ScoreMin {
		return fmt.Errorf("transition score bounds invalid")
	}
	return nil
}

func scoreLearnedTransition(model *TransitionModel, features map[string]float64) (float64, bool) {
	if model == nil || !model.Ready() {
		return 0, false
	}
	sum := model.Bias
	for i, name := range model.FeatureOrder {
		std := model.Std[i]
		if std == 0 {
			std = 1
		}
		sum += model.Weights[i] * (features[name] - model.Mean[i]) / std
	}
	if sum < model.ScoreMin {
		return model.ScoreMin, true
	}
	if sum > model.ScoreMax {
		return model.ScoreMax, true
	}
	return sum, true
}
