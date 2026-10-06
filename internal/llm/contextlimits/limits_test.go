package contextlimits

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestResolveCapOnlyContextWindow(t *testing.T) {
	agent := models.LLMConfig{
		Provider:              models.ProviderOpenAICompatible,
		ProviderContextWindow: 32768,
		ContextWindow:         8192,
	}
	resolved := Resolve(agent)
	if resolved.EffectiveInputWindow != 8192 {
		t.Fatalf("effective input = %d, want 8192 cap", resolved.EffectiveInputWindow)
	}
	if resolved.ProviderInputWindow != 32768 {
		t.Fatalf("provider input = %d, want 32768", resolved.ProviderInputWindow)
	}
}

func TestResolveZeroCapUsesProviderWindow(t *testing.T) {
	agent := models.LLMConfig{
		Provider:              models.ProviderOpenAICompatible,
		ProviderContextWindow: 8192,
	}
	resolved := Resolve(agent)
	if resolved.EffectiveInputWindow != 8192 {
		t.Fatalf("effective input = %d, want 8192", resolved.EffectiveInputWindow)
	}
	if resolved.TriggerLimit != (8192*90)/100 {
		t.Fatalf("trigger = %d, want ~90%% of 8192", resolved.TriggerLimit)
	}
}

func TestResolveOutputCap(t *testing.T) {
	agent := models.LLMConfig{
		Provider:                models.ProviderOpenAICompatible,
		ProviderMaxOutputTokens: 8192,
		DefaultMaxTokens:        4096,
	}
	resolved := Resolve(agent)
	if resolved.EffectiveOutputCap != 4096 {
		t.Fatalf("effective output = %d, want 4096", resolved.EffectiveOutputCap)
	}
}

func TestSyncProviderFromCatalog(t *testing.T) {
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol"}
	SyncProviderFromCatalog(&agent)
	if agent.ProviderContextWindow <= 0 {
		t.Fatal("expected catalog context window on agent")
	}
}

func TestResolveCompatibleManualWindowWithoutDiscovery(t *testing.T) {
	for _, window := range []int{8192, 262144, 1000000} {
		got := Resolve(models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: window})
		if got.EffectiveInputWindow != window {
			t.Fatalf("manual window %d resolved to %d", window, got.EffectiveInputWindow)
		}
	}
	got := Resolve(models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 262144, ProviderContextWindow: 32768})
	if got.EffectiveInputWindow != 32768 {
		t.Fatalf("reported server limit must win: %+v", got)
	}
}

func TestResolveCompatibleOutputLeavesPromptRoom(t *testing.T) {
	for _, output := range []int{0, 16384, 1024} {
		got := Resolve(models.LLMConfig{Provider: models.ProviderOpenAICompatible, ProviderContextWindow: 8192, DefaultMaxTokens: output})
		want := 2048
		if output == 1024 {
			want = 1024
		}
		if got.EffectiveOutputCap != want {
			t.Fatalf("output %d: got %d, want %d", output, got.EffectiveOutputCap, want)
		}
	}
}
