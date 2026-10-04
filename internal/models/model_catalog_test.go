package models

import "testing"

func TestModelCatalogEntriesAreCompleteAndUnique(t *testing.T) {
	seen := map[string]bool{}
	defaults := map[LLMProvider]int{}
	visionDefaults := map[LLMProvider]int{}
	for _, provider := range []LLMProvider{ProviderOpenAI, ProviderAnthropic} {
		for _, spec := range ProviderModels(provider) {
			key := string(provider) + "/" + spec.ID
			if seen[key] {
				t.Errorf("duplicate model %s", key)
			}
			seen[key] = true
			if spec.Default {
				defaults[provider]++
			}
			if spec.VisionDefault {
				visionDefaults[provider]++
			}
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
		if defaults[provider] != 1 {
			t.Errorf("%s has %d default models, want 1", provider, defaults[provider])
		}
	}
	if visionDefaults[ProviderAnthropic] != 1 {
		t.Errorf("Anthropic has %d vision defaults, want 1", visionDefaults[ProviderAnthropic])
	}
}

func TestClaudeSonnet55CatalogPolicy(t *testing.T) {
	spec, ok := LookupModel(ProviderAnthropic, "claude-sonnet-5-5")
	if !ok {
		t.Fatal("claude-sonnet-5-5 missing from Anthropic catalog")
	}
	wantEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	if len(spec.ReasoningEfforts) != len(wantEfforts) {
		t.Fatalf("Sonnet 5.5 efforts = %v, want %v", spec.ReasoningEfforts, wantEfforts)
	}
	for i, want := range wantEfforts {
		if spec.ReasoningEfforts[i] != want {
			t.Fatalf("Sonnet 5.5 efforts = %v, want %v", spec.ReasoningEfforts, wantEfforts)
		}
	}
	if spec.ContextWindow != 200000 {
		t.Fatalf("Sonnet 5.5 context window = %d, want OpenVibely's 200000-token budget", spec.ContextWindow)
	}
	if !spec.RequiresAdaptiveThinking || !spec.UsesAdaptiveThinking {
		t.Fatalf("Sonnet 5.5 thinking capabilities = (required %v, adaptive %v), want both true", spec.RequiresAdaptiveThinking, spec.UsesAdaptiveThinking)
	}
	if spec.DefaultOutputTokens != 64000 || spec.MaxOutputTokens != 128000 {
		t.Fatalf("Sonnet 5.5 output tokens = (%d, %d), want default 64000 and max 128000", spec.DefaultOutputTokens, spec.MaxOutputTokens)
	}
	defaultModel, ok := DefaultModel(ProviderAnthropic)
	if !ok || defaultModel.ID != "claude-sonnet-4-5-20250929" {
		t.Fatalf("Anthropic default = %q, want unchanged default claude-sonnet-4-5-20250929", defaultModel.ID)
	}
	providerModels := ProviderModels(ProviderAnthropic)
	if len(providerModels) == 0 || providerModels[0].ID != "claude-opus-5-5" {
		t.Fatalf("first Anthropic model = %v, want existing first option claude-opus-5-5", providerModels)
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

func TestLookupModelRejectsSettingsEncodedInModelID(t *testing.T) {
	if _, found := LookupModel(ProviderAnthropic, "claude-opus-5-5[1m]"); found {
		t.Error("context setting encoded in model ID must not inherit support")
	}
	if _, found := LookupModel(ProviderAnthropic, "claude-opus-5-5-retired"); found {
		t.Error("uncataloged model suffix must not inherit support")
	}
}

func TestBuiltInModelSupportedForAuth(t *testing.T) {
	for _, spec := range ProviderModels(ProviderOpenAI) {
		if !BuiltInModelSupportedForAuth(spec.Provider, spec.ID, AuthMethodAPIKey) {
			t.Errorf("API key rejected for %s", spec.ID)
		}
		if got := BuiltInModelSupportedForAuth(spec.Provider, spec.ID, AuthMethodOAuth); got != spec.SupportsOAuth {
			t.Errorf("OAuth support for %s = %v", spec.ID, got)
		}
	}
	if BuiltInModelSupportedForAuth(ProviderOpenAI, "gpt-5.3-codex", AuthMethodOAuth) {
		t.Fatal("GPT-5.3-Codex must not be offered for ChatGPT sign-in")
	}
	if !BuiltInModelSupportedForAuth(ProviderOpenAICompatible, "custom", AuthMethodOAuth) {
		t.Fatal("custom provider OAuth was restricted")
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
