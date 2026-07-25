package mixture

import (
	"encoding/json"
	"fmt"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

const (
	DefaultReferenceTemperature    = 0.6
	DefaultAggregatorTemperature   = 0.4
	DefaultReferenceTimeoutSeconds = 90
	MaxReferenceWorkersLimit       = 8
)

type Config struct {
	Enabled                 bool        `json:"enabled"`
	ReferenceModels         []ModelSlot `json:"reference_models"`
	Aggregator              ModelSlot   `json:"aggregator"`
	ReferenceTemperature    float64     `json:"reference_temperature"`
	AggregatorTemperature   float64     `json:"aggregator_temperature"`
	ReferenceTimeoutSeconds int         `json:"reference_timeout_seconds"`
	MaxReferenceWorkers     int         `json:"max_reference_workers"`
}

type ModelSlot struct {
	AgentConfigID string `json:"agent_config_id"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Label         string `json:"label,omitempty"`
}

type ReferenceResult struct {
	Index    int
	Label    string
	Provider string
	Model    string
	Output   string
	Err      string
	Usage    llmcontracts.Usage
}

func DefaultConfig() Config {
	return Config{
		ReferenceTemperature:  DefaultReferenceTemperature,
		AggregatorTemperature: DefaultAggregatorTemperature,
	}
}

func ParseConfig(raw string) (Config, error) {
	cfg := DefaultConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg, fmt.Errorf("mixture config is empty")
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("parse mixture config: %w", err)
	}
	return NormalizeConfig(cfg)
}

func NormalizeConfig(cfg Config) (Config, error) {
	if cfg.ReferenceTimeoutSeconds <= 0 {
		cfg.ReferenceTimeoutSeconds = DefaultReferenceTimeoutSeconds
	}
	if cfg.MaxReferenceWorkers <= 0 {
		cfg.MaxReferenceWorkers = len(cfg.ReferenceModels)
		if cfg.MaxReferenceWorkers <= 0 {
			cfg.MaxReferenceWorkers = 1
		}
	}
	if cfg.MaxReferenceWorkers > MaxReferenceWorkersLimit {
		cfg.MaxReferenceWorkers = MaxReferenceWorkersLimit
	}
	if cfg.MaxReferenceWorkers < 1 {
		cfg.MaxReferenceWorkers = 1
	}
	if isMixtureSlot(cfg.Aggregator) {
		return Config{}, fmt.Errorf("mixture aggregator cannot use provider %q", models.ProviderMixture)
	}
	for i, slot := range cfg.ReferenceModels {
		if isMixtureSlot(slot) {
			return Config{}, fmt.Errorf("mixture reference %d cannot use provider %q", i+1, models.ProviderMixture)
		}
	}
	return cfg, nil
}

func isMixtureSlot(slot ModelSlot) bool {
	return strings.EqualFold(strings.TrimSpace(slot.Provider), string(models.ProviderMixture))
}

func SlotLabel(slot ModelSlot, fallback string) string {
	if label := strings.TrimSpace(slot.Label); label != "" {
		return label
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	parts := []string{strings.TrimSpace(slot.Provider), strings.TrimSpace(slot.Model)}
	joined := strings.Trim(strings.Join(parts, "/"), "/")
	if joined != "" {
		return joined
	}
	return strings.TrimSpace(slot.AgentConfigID)
}
