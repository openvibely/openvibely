package models

import "testing"

func TestModelCatalogVisibleEntriesAreCompleteAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, provider := range []LLMProvider{ProviderOpenAI, ProviderAnthropic} {
		for _, spec := range ProviderModels(provider) {
			key := string(provider) + "/" + spec.ID
			if seen[key] {
				t.Errorf("duplicate visible model %s", key)
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

func TestLookupModelInheritsAnthropicVersionAndContextSuffix(t *testing.T) {
	want, ok := LookupModel(ProviderAnthropic, "claude-opus-5-5")
	if !ok {
		t.Fatal("base model missing")
	}
	for _, id := range []string{"claude-opus-5-5-20260901", "claude-opus-5-5[1m]"} {
		got, found := LookupModel(ProviderAnthropic, id)
		if !found || got.ID != want.ID {
			t.Errorf("LookupModel(%q) = (%q, %v), want %q", id, got.ID, found, want.ID)
		}
		if got.ContextWindow != 200000 {
			t.Errorf("LookupModel(%q) context = %d, want standard 200000", id, got.ContextWindow)
		}
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
