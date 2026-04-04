package usage

import "testing"

func TestFromOpenAI(t *testing.T) {
	u := FromOpenAI(10, 5, 2, 3)
	if u.InputTokens != 10 || u.OutputTokens != 5 || u.TotalTokens != 15 {
		t.Fatalf("unexpected tokens: %+v", u)
	}
	if u.CachedInputTokens != 2 || u.ReasoningTokens != 3 {
		t.Fatalf("unexpected cached/reasoning: %+v", u)
	}
	if u.ProviderRaw["cached_input_tokens"] != 2 || u.ProviderRaw["reasoning_tokens"] != 3 {
		t.Fatalf("unexpected provider raw: %+v", u.ProviderRaw)
	}
}

func TestFromAnthropic(t *testing.T) {
	u := FromAnthropic(11, 7, 4, 5)
	if u.TotalTokens != 18 {
		t.Fatalf("expected total 18, got %d", u.TotalTokens)
	}
	if u.CachedInputTokens != 9 {
		t.Fatalf("expected cached total 9, got %d", u.CachedInputTokens)
	}
	if u.ProviderRaw["cache_creation_input_tokens"] != 4 || u.ProviderRaw["cache_read_input_tokens"] != 5 {
		t.Fatalf("unexpected provider raw: %+v", u.ProviderRaw)
	}
}
