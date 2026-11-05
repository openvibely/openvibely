package usage

import "github.com/openvibely/openvibely/internal/llm/contracts"

// FromOpenAI builds canonical usage with OpenAI-specific optional fields.
func FromOpenAI(inputTokens, outputTokens, cachedInputTokens, reasoningTokens int) contracts.Usage {
	return FromOpenAIWithTotal(inputTokens, outputTokens, cachedInputTokens, reasoningTokens, 0)
}

// FromOpenAIWithTotal builds canonical usage with an optional provider-reported total.
func FromOpenAIWithTotal(inputTokens, outputTokens, cachedInputTokens, reasoningTokens, totalTokens int) contracts.Usage {
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}
	raw := map[string]int{}
	if cachedInputTokens > 0 {
		raw["cached_input_tokens"] = cachedInputTokens
	}
	if reasoningTokens > 0 {
		raw["reasoning_tokens"] = reasoningTokens
	}
	return contracts.Usage{
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		TotalTokens:       totalTokens,
		CachedInputTokens: cachedInputTokens,
		ReasoningTokens:   reasoningTokens,
		ProviderRaw:       raw,
	}
}

// FromAnthropic builds canonical usage with Anthropic cache-token details.
func FromAnthropic(inputTokens, outputTokens, cacheCreationInputTokens, cacheReadInputTokens int) contracts.Usage {
	total := inputTokens + outputTokens
	raw := map[string]int{}
	if cacheCreationInputTokens > 0 {
		raw["cache_creation_input_tokens"] = cacheCreationInputTokens
	}
	if cacheReadInputTokens > 0 {
		raw["cache_read_input_tokens"] = cacheReadInputTokens
	}
	return contracts.Usage{
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		TotalTokens:       total,
		CachedInputTokens: cacheCreationInputTokens + cacheReadInputTokens,
		ProviderRaw:       raw,
	}
}

// FromTotal builds canonical usage when only total tokens are available.
func FromTotal(totalTokens int) contracts.Usage {
	return contracts.Usage{TotalTokens: totalTokens}
}

// AddBilled adds a prior attempt's charges, preserving the current response's
// context size, identifiers, and continuation state.
func AddBilled(current, prior contracts.Usage) contracts.Usage {
	current.InputTokens += prior.InputTokens
	current.OutputTokens += prior.OutputTokens
	current.TotalTokens += prior.TotalTokens
	current.CachedInputTokens += prior.CachedInputTokens
	current.ReasoningTokens += prior.ReasoningTokens
	if len(prior.ProviderRaw) > 0 {
		raw := make(map[string]int, len(current.ProviderRaw)+len(prior.ProviderRaw))
		for k, v := range current.ProviderRaw {
			raw[k] = v
		}
		for k, v := range prior.ProviderRaw {
			raw[k] += v
		}
		current.ProviderRaw = raw
	}
	return current
}
