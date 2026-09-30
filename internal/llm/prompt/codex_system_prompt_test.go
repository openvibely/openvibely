package prompt

import (
	"crypto/sha256"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestCodexPromptMatchesSnapshot(t *testing.T) {
	// Hashes of the actual cached templates, excluding surrounding whitespace.
	for model, want := range map[string]string{
		"gpt-6-astra":       "8e5e8f9a863bb360482e4d9fc000f1e4f1a85b1fdab7301dba58b36d1185b707",
		"gpt-6-sol":         "6e61d417b2c660f88f59b8be32933df8ed5e21240058ded907e8e3ecfbf2ea5a",
		"gpt-6-luna":        "49c4958e931333ee6a703ecb258a44ffb46f07f40e0a167a663de04dfdfa6ef1",
		"gpt-reserve":       "0972574f9d43b6c30321624b9ba5eec3a47db32251b7ba6d94807bf5295170e3",
		"gpt-5.6-sol":       "0972574f9d43b6c30321624b9ba5eec3a47db32251b7ba6d94807bf5295170e3",
		"gpt-5.6-terra":     "0972574f9d43b6c30321624b9ba5eec3a47db32251b7ba6d94807bf5295170e3",
		"gpt-5.6-luna":      "0972574f9d43b6c30321624b9ba5eec3a47db32251b7ba6d94807bf5295170e3",
		"gpt-5.5":           "3d7984a1671ad1c65f6bd7732f03b69eb96b616cd7fa7390eb5e5ca04d06cf49",
		"codex-auto-review": "0972574f9d43b6c30321624b9ba5eec3a47db32251b7ba6d94807bf5295170e3",
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(codexSnapshot(model)))); got != want {
			t.Errorf("%s: prompt differs from Codex snapshot: %s", model, got)
		}
	}
	if CodexSystemPrompt("unknown-model") != CodexSystemPrompt("gpt-reserve") {
		t.Fatal("unknown model must use reserve template")
	}
}

func TestCodexPromptContextAndModes(t *testing.T) {
	base := CodexSystemPrompt("gpt-6-astra")
	got := BuildCodexAgentSystemPrompt("gpt-6-astra", "PROJECT_CONTEXT", "/tmp/.worktrees/task_123")
	for _, want := range []string{base, "PROJECT_CONTEXT", BuildWorktreeContextSentence("/tmp/.worktrees/task_123")} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
	followup := BuildCodexChatSystemPrompt("gpt-6-astra", true, models.ChatModeOrchestrate, "FOLLOWUP_CONTEXT", false)
	if followup != base+taskFollowupConstraints+"\nFOLLOWUP_CONTEXT" {
		t.Fatal("follow-up lost base or application context")
	}
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		if BuildCodexChatSystemPrompt("gpt-6-astra", false, mode, "CONTEXT", false) != BuildChatSystemPrompt(false, mode, "CONTEXT", false) {
			t.Fatalf("ordinary chat changed for %s", mode)
		}
	}
	if BuildAgentSystemPrompt("") != AgentSystemPrompt {
		t.Fatal("other providers changed")
	}
}

func TestCodexPromptUsesOpenVibelyTools(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.5", "gpt-5.6-sol"} {
		prompt := CodexSystemPrompt(model)
		for _, unavailable := range []string{"apply_patch", "functions.exec", "exec_command", "request_user_input_async", "send_user_message_async", "multi_tool_use.parallel"} {
			if strings.Contains(prompt, unavailable) {
				t.Errorf("%s references unavailable %s", model, unavailable)
			}
		}
	}
}
