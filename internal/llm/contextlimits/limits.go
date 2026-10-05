package contextlimits

import (
	llmollama "github.com/openvibely/openvibely/internal/llm/ollama"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

const (
	DefaultOpenAICompatibleContextWindow = 128000
	DefaultAnthropicContextWindow        = 200000
	DefaultOpenAIContextWindow             = 200000
	DefaultReservedOutputTokens            = 16384
)

// Resolved holds effective input/output windows for budgeting and provider requests.
type Resolved struct {
	ProviderInputWindow  int
	ProviderOutputWindow int
	EffectiveInputWindow int
	EffectiveOutputCap   int
	AutoLimit            int
	TriggerLimit         int
	EffectiveHardLimit   int
}

func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func openAIContextWindow(model string) int {
	if spec, ok := models.LookupModel(models.ProviderOpenAI, model); ok && spec.ContextWindow > 0 {
		return spec.ContextWindow
	}
	return DefaultOpenAIContextWindow
}

func providerInputWindow(agent models.LLMConfig) int {
	if agent.ProviderContextWindow > 0 {
		return agent.ProviderContextWindow
	}
	switch agent.Provider {
	case models.ProviderOpenAI:
		return openAIContextWindow(agent.Model)
	case models.ProviderAnthropic:
		if spec, ok := models.LookupModel(models.ProviderAnthropic, agent.Model); ok && spec.ContextWindow > 0 {
			return spec.ContextWindow
		}
		return DefaultAnthropicContextWindow
	case models.ProviderOpenAICompatible:
		return DefaultOpenAICompatibleContextWindow
	case models.ProviderOllama:
		return llmollama.DefaultContextWindow
	default:
		return DefaultOpenAIContextWindow
	}
}

func providerOutputWindow(agent models.LLMConfig) int {
	if agent.ProviderMaxOutputTokens > 0 {
		return agent.ProviderMaxOutputTokens
	}
	if spec, ok := models.LookupModel(agent.Provider, agent.Model); ok {
		if spec.MaxOutputTokens > 0 {
			return spec.MaxOutputTokens
		}
		if spec.DefaultOutputTokens > 0 {
			return spec.DefaultOutputTokens
		}
	}
	switch agent.Provider {
	case models.ProviderOllama:
		return 4096
	default:
		return DefaultReservedOutputTokens
	}
}

// Resolve applies cap-only user overrides on top of provider/catalog limits.
func Resolve(agent models.LLMConfig) Resolved {
	providerIn := providerInputWindow(agent)
	effectiveIn := providerIn
	if agent.ContextWindow > 0 {
		effectiveIn = minPositive(providerIn, agent.ContextWindow)
	}

	providerOut := providerOutputWindow(agent)
	effectiveOut := providerOut
	if agent.DefaultMaxTokens > 0 {
		effectiveOut = minPositive(providerOut, agent.DefaultMaxTokens)
	}

	autoLimit := (effectiveIn * 90) / 100
	effectiveHardLimit := (effectiveIn * 95) / 100
	if agent.Provider == models.ProviderAnthropic {
		autoLimit = anthropicclient.CompactionTriggerLimit(effectiveIn)
		effectiveHardLimit = anthropicclient.CompactionBlockingLimit(effectiveIn)
	}
	triggerLimit := autoLimit
	configuredThreshold := agent.CompactionThreshold
	if agent.Provider == models.ProviderAnthropic && configuredThreshold > 0 && configuredThreshold < anthropicclient.MinCompactionThreshold {
		configuredThreshold = anthropicclient.MinCompactionThreshold
	}
	if configuredThreshold > 0 && configuredThreshold < triggerLimit {
		triggerLimit = configuredThreshold
	}

	return Resolved{
		ProviderInputWindow:  providerIn,
		ProviderOutputWindow: providerOut,
		EffectiveInputWindow: effectiveIn,
		EffectiveOutputCap:   effectiveOut,
		AutoLimit:            autoLimit,
		TriggerLimit:         triggerLimit,
		EffectiveHardLimit:   effectiveHardLimit,
	}
}

// SyncProviderFromCatalog refreshes persisted provider limits from the built-in catalog.
func SyncProviderFromCatalog(agent *models.LLMConfig) {
	if agent == nil {
		return
	}
	spec, ok := models.LookupModel(agent.Provider, agent.Model)
	if !ok {
		return
	}
	if spec.ContextWindow > 0 {
		agent.ProviderContextWindow = spec.ContextWindow
	}
	if spec.MaxOutputTokens > 0 {
		agent.ProviderMaxOutputTokens = spec.MaxOutputTokens
	} else if spec.DefaultOutputTokens > 0 {
		agent.ProviderMaxOutputTokens = spec.DefaultOutputTokens
	}
}
