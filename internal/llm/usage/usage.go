package usage

import "github.com/openvibely/openvibely/internal/llm/contracts"

// FromIO builds canonical usage from explicit input/output tokens.
func FromIO(inputTokens, outputTokens int) contracts.Usage {
	total := inputTokens + outputTokens
	return contracts.Usage{InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: total}
}

// FromOpenAI builds canonical usage with OpenAI-specific optional fields.
func FromOpenAI(inputTokens, outputTokens, cachedInputTokens, reasoningTokens int) contracts.Usage {
	total := inputTokens + outputTokens
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
		TotalTokens:       total,
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
