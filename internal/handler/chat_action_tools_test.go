package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestSupportsChatActionTools(t *testing.T) {
	tests := []struct {
		name  string
		agent models.LLMConfig
		want  bool
	}{
		{
			name: "openai oauth supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodOAuth,
			},
			want: true,
		},
		{
			name: "openai cli does not support runtime action tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodCLI,
			},
			want: false,
		},
		{
			name: "anthropic api key supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderAnthropic,
				AuthMethod: models.AuthMethodAPIKey,
			},
			want: true,
		},
		{
			name: "ollama does not support runtime action tools",
			agent: models.LLMConfig{
				Provider: models.ProviderOllama,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsChatActionTools(tt.agent); got != tt.want {
				t.Fatalf("supportsChatActionTools()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestBuildToolMarker_WithBody(t *testing.T) {
	input := json.RawMessage(`{"title":"Fix login","prompt":"Investigate auth flow"}`)
	got, err := buildToolMarker("CREATE_TASK", input, true)
	if err != nil {
		t.Fatalf("buildToolMarker error: %v", err)
	}
	if !strings.Contains(got, "[CREATE_TASK]") || !strings.Contains(got, "[/CREATE_TASK]") {
		t.Fatalf("expected create task marker wrapper, got %q", got)
	}
	if !strings.Contains(got, `"title":"Fix login"`) {
		t.Fatalf("expected normalized JSON body, got %q", got)
	}
}

func TestBuildToolMarker_ChainConfigPreserved(t *testing.T) {
	// Simulate the exact JSON a model sends when using create_task tool with chain config
	input := json.RawMessage(`{"title":"Compute 1+1","prompt":"Compute 1+1 and save to file","category":"active","chain":{"enabled":true,"trigger":"on_completion","child_title":"Compute x+1 from parent output","child_prompt_prefix":"Read x from result.txt and compute x+1","child_category":"active"}}`)

	marker, err := buildToolMarker("CREATE_TASK", input, true)
	if err != nil {
		t.Fatalf("buildToolMarker error: %v", err)
	}

	// Verify marker wrapping
	if !strings.Contains(marker, "[CREATE_TASK]") || !strings.Contains(marker, "[/CREATE_TASK]") {
		t.Fatalf("missing marker wrapper: %q", marker)
	}

	// Parse it back via the same path as processChatTaskCreations
	tasks := service.ParseTaskCreations(marker)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task from roundtrip, got %d", len(tasks))
	}

	req := tasks[0]
	if req.Chain == nil {
		t.Fatal("chain config lost in buildToolMarker → ParseTaskCreations roundtrip")
	}
	if !req.Chain.Enabled {
		t.Error("chain.enabled should be true after roundtrip")
	}
	if req.Chain.Trigger != "on_completion" {
		t.Errorf("chain.trigger = %q after roundtrip", req.Chain.Trigger)
	}
	if req.Chain.ChildTitle != "Compute x+1 from parent output" {
		t.Errorf("chain.child_title = %q after roundtrip", req.Chain.ChildTitle)
	}
	if req.Chain.ChildPromptPrefix != "Read x from result.txt and compute x+1" {
		t.Errorf("chain.child_prompt_prefix lost in roundtrip")
	}
	if req.Chain.ChildCategory != "active" {
		t.Errorf("chain.child_category = %q after roundtrip", req.Chain.ChildCategory)
	}
}

func TestToolSummaryFromMarker(t *testing.T) {
	marker := "[LIST_PROJECTS]"
	updated := marker + "\n\n---\nAvailable Projects:\n- **Default**"
	got := toolSummaryFromMarker(marker, updated)
	if !strings.Contains(got, "Available Projects") {
		t.Fatalf("expected tool summary to keep appended output, got %q", got)
	}
}

func TestChatActionSummaryCollector_AppendsCreatedAndEdited(t *testing.T) {
	collector := newChatActionSummaryCollector()
	collector.addCreated("\n---\nCreated 1 task(s):\n- \"Fix login\" (active) [TASK_ID:abc123]")
	collector.addEdited("\n---\nEdited 1 task(s):\n- \"Fix login\" (backlog, updated: category) [TASK_EDITED:abc123]")

	out := collector.appendToOutput("Done.")
	if !strings.Contains(out, "Created 1 task(s):") {
		t.Fatalf("expected created summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_ID:abc123]") {
		t.Fatalf("expected task id marker, got %q", out)
	}
	if !strings.Contains(out, "Edited 1 task(s):") {
		t.Fatalf("expected edited summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_EDITED:abc123]") {
		t.Fatalf("expected task edited marker, got %q", out)
	}
}

func TestChatActionHandlers_CoverageWebAndAPI(t *testing.T) {
	h := &Handler{}
	params := streamingResponseParams{ExecID: "e", ProjectID: "p"}

	webHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true, webHandlers); err != nil {
		t.Fatalf("web handler coverage mismatch: %v", err)
	}

	apiHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceAPI, true, apiHandlers); err != nil {
		t.Fatalf("api handler coverage mismatch: %v", err)
	}
}
