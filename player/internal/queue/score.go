package queue

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed model_default.json
var defaultModelJSON []byte

const DefaultModelVersion = "default-v1"

type Model struct {
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

func DefaultModel() Model {
	model, err := ParseModel(defaultModelJSON)
	if err != nil {
		panic(err)
	}
	return model
}

func ParseModel(raw []byte) (Model, error) {
	var model Model
	if err := json.Unmarshal(raw, &model); err != nil {
		return model, fmt.Errorf("invalid model json")
	}
	if err := model.Validate(); err != nil {
		return model, err
	}
	return model, nil
}

func LoadRuntimeModel(path string) Model {
	if path == "" {
		return DefaultModel()
	}
	model, err := LoadModelFile(path)
	if err != nil {
		return DefaultModel()
	}
	return model
}

func LoadModelFile(path string) (Model, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Model{}, err
	}
	return ParseModel(raw)
}

func (m Model) Validate() error {
	if m.SchemaVersion != FeatureSchemaVersion {
		return fmt.Errorf("model schema_version %d does not match feature schema %d", m.SchemaVersion, FeatureSchemaVersion)
	}
	if len(m.FeatureOrder) != len(FeatureOrder) {
		return fmt.Errorf("feature_order length mismatch")
	}
	for i, name := range FeatureOrder {
		if m.FeatureOrder[i] != name {
			return fmt.Errorf("feature_order mismatch at %d: %s != %s", i, m.FeatureOrder[i], name)
		}
	}
	n := len(FeatureOrder)
	if len(m.Weights) != n || len(m.Mean) != n || len(m.Std) != n {
		return fmt.Errorf("model vector lengths must match feature_order")
	}
	for i, std := range m.Std {
		if std == 0 {
			return fmt.Errorf("std[%s] must be non-zero", FeatureOrder[i])
		}
	}
	if m.ModelVersion == "" {
		return fmt.Errorf("model_version required")
	}
	if m.ScoreMax <= m.ScoreMin {
		return fmt.Errorf("score bounds invalid")
	}
	return nil
}

func (m Model) Score(values []float64) float64 {
	if len(values) != len(m.Weights) {
		return m.Bias
	}
	sum := m.Bias
	for i, value := range values {
		sum += m.Weights[i] * (value - m.Mean[i]) / m.Std[i]
	}
	if sum < m.ScoreMin {
		return m.ScoreMin
	}
	if sum > m.ScoreMax {
		return m.ScoreMax
	}
	return sum
}

func FeatureIndex(name string) int {
	for i, n := range FeatureOrder {
		if n == name {
			return i
		}
	}
	return -1
}
