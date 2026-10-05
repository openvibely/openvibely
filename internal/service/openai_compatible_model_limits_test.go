package service

import "testing"

func TestParseOpenAICompatibleModelEntry_VLLM(t *testing.T) {
	id, limits := ParseOpenAICompatibleModelEntry(map[string]any{
		"id":            "meta-llama/Llama-3-8B",
		"max_model_len": float64(8192),
	})
	if id != "meta-llama/Llama-3-8B" {
		t.Fatalf("id = %q", id)
	}
	if limits.ContextLength != 8192 {
		t.Fatalf("context = %d, want 8192", limits.ContextLength)
	}
}

func TestParseOpenAICompatibleModelEntry_RootMaxModelLen(t *testing.T) {
	_, limits := ParseOpenAICompatibleModelEntry(map[string]any{
		"model": "local",
		"root":  map[string]any{"max_model_len": float64(16384)},
	})
	if limits.ContextLength != 16384 {
		t.Fatalf("context = %d, want 16384", limits.ContextLength)
	}
}
