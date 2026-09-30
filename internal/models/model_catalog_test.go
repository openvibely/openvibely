package models

import "testing"

func TestModelCatalogEntriesAreCompleteAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range []LLMProvider{ProviderOpenAI, ProviderAnthropic} {
		for _, spec := range ProviderModels(provider) {
			key := string(provider) + "/" + spec.ID
			if seen[key] {
				t.Errorf("duplicate model %s", key)
			}
			seen[key] = true
			if spec.Label == "" {
				t.Errorf("%s has no label", key)
			}
			if spec.ContextWindow <= 0 {
				t.Errorf("%s has invalid context window %d", key, spec.ContextWindow)
			}
			if spec.PromptProfile == "" {
				t.Errorf("%s has no prompt profile", key)
			}
			for _, effort := range spec.ReasoningEfforts {
				if got := NormalizeModelEffort(provider, spec.ID, effort); got != effort {
					t.Errorf("NormalizeModelEffort(%s, %q) = %q", key, effort, got)
				}
			}
		}
	}
}

func TestRetiredBuiltInModelsAreUnsupported(t *testing.T) {
	for _, tc := range []struct {
		provider LLMProvider
		model    string
	}{
		{ProviderOpenAI, "gpt-5.2-codex"},
		{ProviderAnthropic, "claude-opus-4-5"},
	} {
		if BuiltInModelSupported(tc.provider, tc.model) {
			t.Errorf("%s/%s remains supported", tc.provider, tc.model)
		}
	}
	if !BuiltInModelSupported(ProviderOpenAICompatible, "gpt-5.2-codex") {
		t.Error("OpenAI-compatible custom model IDs must remain unrestricted")
	}
}

func TestLookupModelAcceptsAnthropicContextSuffixOnly(t *testing.T) {
	want, ok := LookupModel(ProviderAnthropic, "claude-opus-5-5")
	if !ok {
		t.Fatal("base model missing")
	}
	got, found := LookupModel(ProviderAnthropic, "claude-opus-5-5[1m]")
	if !found || got.ID != want.ID {
		t.Errorf("1m lookup = (%q, %v), want %q", got.ID, found, want.ID)
	}
	if got.ContextWindow != 200000 {
		t.Errorf("1m lookup context = %d, want standard 200000", got.ContextWindow)
	}
	if _, found := LookupModel(ProviderAnthropic, "claude-opus-5-5-retired"); found {
		t.Error("uncataloged model suffix must not inherit support")
	}
}

func TestProviderModelsReturnsDefensiveEffortCopies(t *testing.T) {
	models := ProviderModels(ProviderOpenAI)
	if len(models) == 0 || len(models[0].ReasoningEfforts) == 0 {
		t.Fatal("OpenAI catalog unexpectedly empty")
	}
	models[0].ReasoningEfforts[0] = "mutated"
	again := ProviderModels(ProviderOpenAI)
	if again[0].ReasoningEfforts[0] == "mutated" {
		t.Fatal("ProviderModels exposed mutable catalog storage")
	}
}
