package mixture

import (
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestNormalizeConfigDefaultsAndClamps(t *testing.T) {
	input := DefaultConfig()
	input.Enabled = true
	input.ReferenceModels = []ModelSlot{
		{AgentConfigID: "ref-1", Provider: "openai"},
		{AgentConfigID: "ref-2", Provider: "anthropic"},
	}
	input.Aggregator = ModelSlot{AgentConfigID: "agg", Provider: "openai"}
	input.MaxReferenceWorkers = 99
	cfg, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.MaxReferenceWorkers != 8 {
		t.Fatalf("MaxReferenceWorkers = %d, want 8", cfg.MaxReferenceWorkers)
	}
	if cfg.ReferenceTimeoutSeconds != 90 {
		t.Fatalf("ReferenceTimeoutSeconds = %d, want 90", cfg.ReferenceTimeoutSeconds)
	}
	if cfg.ReferenceTemperature != 0.6 || cfg.AggregatorTemperature != 0.4 {
		t.Fatalf("temperatures = %.1f/%.1f", cfg.ReferenceTemperature, cfg.AggregatorTemperature)
	}
}

func TestParseConfigPreservesExplicitZeroTemperatures(t *testing.T) {
	cfg, err := ParseConfig(`{
		"enabled": true,
		"reference_temperature": 0,
		"aggregator_temperature": 0,
		"aggregator": {"provider": "openai"}
	}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.ReferenceTemperature != 0 || cfg.AggregatorTemperature != 0 {
		t.Fatalf("temperatures = %v/%v, want 0/0", cfg.ReferenceTemperature, cfg.AggregatorTemperature)
	}
}

func TestParseConfigDefaultsMissingTemperatures(t *testing.T) {
	cfg, err := ParseConfig(`{"enabled":true,"aggregator":{"provider":"openai"}}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.ReferenceTemperature != DefaultReferenceTemperature || cfg.AggregatorTemperature != DefaultAggregatorTemperature {
		t.Fatalf("temperatures = %v/%v, want defaults", cfg.ReferenceTemperature, cfg.AggregatorTemperature)
	}
}

func TestNormalizeConfigRejectsRecursiveMixtureSlots(t *testing.T) {
	_, err := NormalizeConfig(Config{Aggregator: ModelSlot{AgentConfigID: "agg", Provider: "mixture"}})
	if err == nil || !strings.Contains(err.Error(), "aggregator") {
		t.Fatalf("expected recursive aggregator error, got %v", err)
	}
	_, err = NormalizeConfig(Config{ReferenceModels: []ModelSlot{{AgentConfigID: "ref", Provider: "mixture"}}, Aggregator: ModelSlot{AgentConfigID: "agg", Provider: "openai"}})
	if err == nil || !strings.Contains(err.Error(), "reference") {
		t.Fatalf("expected recursive reference error, got %v", err)
	}
}

func TestPrivateContextPreservesConfiguredOrder(t *testing.T) {
	block := PrivateContext([]ReferenceResult{
		{Index: 0, Label: "First", Provider: "openai", Model: "gpt", Output: "first output"},
		{Index: 1, Label: "Second", Provider: "anthropic", Model: "claude", Err: "timeout"},
	})
	first := strings.Index(block, "First")
	second := strings.Index(block, "Second")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("reference order not preserved:\n%s", block)
	}
	if !strings.Contains(block, "[failed: timeout]") {
		t.Fatalf("failure note missing:\n%s", block)
	}
}

func TestAppendPrivateContextUsesPrivateInstructionSurfaces(t *testing.T) {
	req := AppendPrivateContext(llmcontracts.AgentRequest{ChatSystemContext: "chat private", Message: "user"}, "ctx")
	if req.ChatSystemContext != "chat private\n\nctx" || req.Message != "user" {
		t.Fatalf("chat context append failed: %+v", req)
	}
	req = AppendPrivateContext(llmcontracts.AgentRequest{ProjectInstructions: "project private", Message: "task"}, "ctx")
	if req.ProjectInstructions != "project private\n\nctx" || req.Message != "task" {
		t.Fatalf("project context append failed: %+v", req)
	}
	req = AppendPrivateContext(llmcontracts.AgentRequest{Message: "user"}, "ctx")
	if !strings.Contains(req.Message, "user\n\nctx") {
		t.Fatalf("message append failed: %+v", req)
	}
}

func TestReferencePromptOmitsMarkersAndToolOutput(t *testing.T) {
	prompt := ReferencePrompt("current", []models.Execution{{
		PromptSent: "prior user",
		Output:     "assistant\n[STATUS: SUCCESS]\ntool_result: hidden\nvisible",
	}})
	if !strings.Contains(prompt, "User request:\ncurrent") || !strings.Contains(prompt, "User: prior user") || !strings.Contains(prompt, "Assistant: assistant\nvisible") {
		t.Fatalf("expected prompt context missing:\n%s", prompt)
	}
	if strings.Contains(prompt, "STATUS") || strings.Contains(prompt, "tool_result") {
		t.Fatalf("prompt included marker/tool output:\n%s", prompt)
	}
}
