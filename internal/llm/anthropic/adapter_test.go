package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestToolSecondaryInfo_LongBashPreservesLaterContext(t *testing.T) {
	input := map[string]any{
		"command": "cd /Users/dubee/go/src/github.com/openvibely/openvibely/.worktrees/task_6a40e9f8fefa53ac8d203aa3fd3a70be && rg -n \"toolSecondaryInfo|truncateToolSecondary|task thread\" internal pkg web/templates/components/chat_shared.templ",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("Bash", raw)
	if !strings.HasPrefix(got, "$ cd ") {
		t.Fatalf("expected bash detail prefix, got %q", got)
	}
	if !strings.Contains(got, "chat_shared.templ") {
		t.Fatalf("expected later command context to survive truncation, got %q", got)
	}
}

func TestToolSecondaryInfo_LongGrepPreservesLaterPatternContext(t *testing.T) {
	input := map[string]any{
		"pattern": "len\\(cmd\\) >|len\\(p\\) >|toolSecondaryInfo|truncateToolSecondary|task thread|chat_shared\\.templ|stream/events\\.go",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("Grep", raw)
	if !strings.Contains(got, "chat_shared") {
		t.Fatalf("expected later grep context to survive truncation, got %q", got)
	}
}

func TestWrapToolFilterForPlanMode_ReadOnlyAllowlist(t *testing.T) {
	base := func(name string) bool { return true }
	filter := wrapToolFilterForPlanMode(base, false, models.ChatModePlan)

	if !filter("read_file") || !filter("list_files") || !filter("grep_search") {
		t.Fatalf("expected read-only tool allowlist to pass")
	}
	if filter("write_file") || filter("edit_file") || filter("bash") {
		t.Fatalf("expected mutating tools to be blocked in plan mode")
	}
}

func TestComposeRuntimeToolFilter_OrchestrateAllowsOnlyActionTools(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "create_task"},
		},
	}
	base := func(name string) bool { return true }

	filter := composeRuntimeToolFilter(base, rt, false, models.ChatModeOrchestrate)
	if !filter("create_task") {
		t.Fatalf("expected action tool to be allowed in orchestrate mode")
	}
	if filter("read_file") {
		t.Fatalf("expected filesystem tool to be blocked in orchestrate mode")
	}
}

func TestComposeRuntimeToolFilter_PlanBlocksActionToolsAndMutations(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "create_task"},
		},
	}
	base := func(name string) bool { return true }

	filter := composeRuntimeToolFilter(base, rt, false, models.ChatModePlan)
	if filter("create_task") {
		t.Fatalf("expected action tool to be blocked in plan mode")
	}
	if !filter("read_file") || !filter("list_files") || !filter("grep_search") {
		t.Fatalf("expected read-only tools to remain allowed in plan mode")
	}
	if filter("write_file") || filter("bash") {
		t.Fatalf("expected mutating tools to be blocked in plan mode")
	}
}

func TestShouldSkipDefaultToolsForChatMode(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "create_task"},
		},
	}

	if !shouldSkipDefaultToolsForChatMode(false, models.ChatModeOrchestrate, rt) {
		t.Fatalf("expected default tools to be skipped for orchestrate chat with runtime action tools")
	}
	if shouldSkipDefaultToolsForChatMode(true, models.ChatModeOrchestrate, rt) {
		t.Fatalf("did not expect skip for task follow-up mode")
	}
	if shouldSkipDefaultToolsForChatMode(false, models.ChatModePlan, rt) {
		t.Fatalf("did not expect skip for plan mode")
	}
	if shouldSkipDefaultToolsForChatMode(false, models.ChatModeOrchestrate, nil) {
		t.Fatalf("did not expect skip without runtime tools")
	}
}

func TestShouldPreferAnthropicProviderWeb(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{
			name:   "url summarize prompt",
			prompt: "summarize this blog https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts",
			want:   true,
		},
		{
			name:   "url coding prompt",
			prompt: "implement oauth flow from https://example.com/spec and update internal handler code",
			want:   true,
		},
		{
			name:   "no url",
			prompt: "summarize what we changed in this repository",
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPreferAnthropicProviderWeb(tc.prompt); got != tc.want {
				t.Fatalf("shouldPreferAnthropicProviderWeb(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}
