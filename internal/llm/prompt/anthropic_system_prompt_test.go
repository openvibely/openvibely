package prompt

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAnthropicAgentSystemPrompt(t *testing.T) {
	got := BuildAnthropicAgentSystemPrompt("claude-sonnet-5", "PROJECT_CONTEXT", "/tmp/.worktrees/task_123")
	for _, want := range []string{
		AnthropicAgentSystemPrompt,
		"check with the user before proceeding",
		"If you notice that you wrote insecure code, immediately fix it.",
		"Don't implement until the user agrees.",
		"start the dev server and use the feature in a browser",
		"read_file, edit_file, write_file, list_files, grep_search",
		"PROJECT_CONTEXT",
		BuildWorktreeContextSentence("/tmp/.worktrees/task_123"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing instruction %q", want)
		}
	}
	for _, unwanted := range []string{"After editing ANY code file", "If you notice insecure code, fix it immediately.", "/help", "TodoWrite", "Claude Code is available"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected legacy or CLI-only instruction %q", unwanted)
		}
	}
	if got := BuildAgentSystemPrompt(""); got != AgentSystemPrompt {
		t.Fatal("provider-neutral base prompt changed")
	}
}

func TestEveryCatalogAnthropicPromptProfileIsEmbedded(t *testing.T) {
	for _, spec := range models.ProviderModels(models.ProviderAnthropic) {
		data, err := anthropicPrompts.ReadFile(string(spec.PromptProfile))
		if err != nil {
			t.Errorf("%s prompt profile %q: %v", spec.ID, spec.PromptProfile, err)
			continue
		}
		got := BuildAnthropicAgentSystemPrompt(spec.ID, "")
		if !strings.Contains(got, string(data)) {
			t.Errorf("%s does not use its catalog prompt", spec.ID)
		}
	}
}

func TestAnthropicChatSystemPrompt(t *testing.T) {
	got := BuildAnthropicChatSystemPrompt("claude-sonnet-5", true, models.ChatModeOrchestrate, "FOLLOWUP_CONTEXT", false)
	want := AnthropicAgentSystemPrompt + taskFollowupConstraints + "\nFOLLOWUP_CONTEXT"
	if got != want {
		t.Fatal("follow-up must use Anthropic base and preserve application constraints and context")
	}
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		for _, restrict := range []bool{false, true} {
			got := BuildAnthropicChatSystemPrompt("claude-sonnet-5", false, mode, "CHAT_CONTEXT", restrict)
			want := BuildChatSystemPrompt(false, mode, "CHAT_CONTEXT", restrict)
			if got != want {
				t.Fatalf("non-coding chat changed for mode %s", mode)
			}
		}
	}
}
