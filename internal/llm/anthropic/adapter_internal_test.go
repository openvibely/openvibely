package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestRuntimeAnthropicToolsAliasesSkillsListWireName(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "skill_view"},
			{Name: "skills_list"},
			{Name: "skill_manage"},
			{Name: "memory_view"},
		},
	}

	tools := runtimeAnthropicTools(rt)
	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Name)
	}
	want := []string{"skill_view", "skill_list", "skill_manage", "memory_view"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("runtimeAnthropicTools names = %v, want %v", got, want)
	}
}

func TestComposeRuntimeToolExecutorRemovesOptionalNulls(t *testing.T) {
	var got json.RawMessage
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name:       "list_alerts",
			Parameters: json.RawMessage(`{"type":"object","properties":{"processing_state":{"type":"string","x-openvibely-omit-value":"all"},"read":{"type":"string","x-openvibely-omit-value":"all"}}}`),
		}},
		Executor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, bool, error) {
			got = append(json.RawMessage(nil), input...)
			return `{}`, true, false, nil
		},
	}

	_, _, err := composeRuntimeToolExecutor(nil, rt)(context.Background(), "list_alerts", json.RawMessage(`{"processing_state":"all","read":"unread"}`))
	if err != nil {
		t.Fatalf("execute list_alerts: %v", err)
	}
	if string(got) != `{"read":"unread"}` {
		t.Fatalf("runtime input = %s, want explicit unread only", got)
	}
}

func TestComposeRuntimeToolExecutorCanonicalizesAnthropicSkillListAlias(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skills_list"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "skills_list" {
				return "", true, true, fmt.Errorf("unexpected tool name %q", name)
			}
			return "listed", true, false, nil
		},
	}

	exec := composeRuntimeToolExecutor(nil, rt)
	out, isError, err := exec(context.Background(), "skill_list", json.RawMessage(`{}`))
	if err != nil || isError || out != "listed" {
		t.Fatalf("aliased runtime executor = (%q, %v, %v), want listed false nil", out, isError, err)
	}
}

func TestComposeRuntimeToolFilterCanonicalizesAnthropicSkillListAlias(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skills_list"}},
		Filter: func(name string) (bool, bool) {
			if name != "skills_list" {
				return false, true
			}
			return true, true
		},
	}

	filter := llmcontracts.ComposeRuntimeToolFilter(nil, rt, runtimeToolPolicyOptions(false, models.ChatModeOrchestrate))
	if !filter("skill_list") {
		t.Fatalf("expected aliased skills_list tool to be allowed through canonical runtime filter")
	}
}

func TestAnthropicRuntimeToolHelperMappingFilteringAndExecution(t *testing.T) {
	if got := applyAgentToSystemPrompt("base", nil); got != "base" {
		t.Fatalf("nil agent prompt = %q", got)
	}
	combined := applyAgentToSystemPrompt("base", &models.Agent{SystemPrompt: "system", Skills: []models.SkillConfig{{Name: "Audit", Content: "check carefully"}, {Name: "Empty"}}})
	if !strings.Contains(combined, "system") || !strings.Contains(combined, "## Skill: Audit") || !strings.Contains(combined, "base") {
		t.Fatalf("combined agent prompt missing parts: %q", combined)
	}

	mapped := map[string]string{"read_file": "Read", "write_file": "Write", "edit_file": "Edit", "bash": "Bash", "list_files": "Glob", "grep_search": "Grep", "web_search_20260209": "WebSearch", "web_fetch_20260309": "WebFetch", "unknown": ""}
	for name, want := range mapped {
		if got := mapBuiltInToolName(" " + name + " "); got != want {
			t.Fatalf("mapBuiltInToolName(%q)=%q want %q", name, got, want)
		}
	}
	if !agentAllowsBuiltInTool(nil, "bash") || agentAllowsBuiltInTool(&models.Agent{ToolConfig: models.AgentToolConfig{SkipDefaultTools: true}}, "bash") {
		t.Fatal("agent built-in skip/default behavior changed")
	}
	if !agentAllowsBuiltInTool(&models.Agent{Tools: []string{" WebFetch "}}, "web_fetch") || agentAllowsBuiltInTool(&models.Agent{Tools: []string{"Read"}}, "bash") {
		t.Fatal("agent explicit built-in grant behavior changed")
	}
	if !planModeAllowsReadOnlyTool("web_fetch_20250910") || planModeAllowsReadOnlyTool("write_file") {
		t.Fatal("plan-mode Anthropic tool classification changed")
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: " skills_list ", Description: " list skills ", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)}, {Name: "write_task"}, {Name: " "}}}
	tools := runtimeAnthropicTools(rt)
	if len(tools) != 2 || tools[0].Name != "skill_list" || tools[0].Description != "list skills" {
		t.Fatalf("runtimeAnthropicTools = %#v", tools)
	}
	if runtimeAnthropicTools(nil) != nil {
		t.Fatal("nil runtime tools should not produce Anthropic tools")
	}

	baseExec := func(_ context.Context, name string, _ json.RawMessage) (string, bool, error) {
		return "base:" + name, false, nil
	}
	exec := composeRuntimeToolExecutor(baseExec, &llmcontracts.RuntimeTools{Executor: func(_ context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
		if name == "skills_list" {
			return "listed", true, false, nil
		}
		return "", false, false, nil
	}})
	out, isErr, err := exec(context.Background(), "skill_list", nil)
	if out != "listed" || isErr || err != nil {
		t.Fatalf("runtime executor out=%q isErr=%v err=%v", out, isErr, err)
	}
	out, isErr, err = exec(context.Background(), "bash", nil)
	if out != "base:bash" || isErr || err != nil {
		t.Fatalf("base executor out=%q isErr=%v err=%v", out, isErr, err)
	}
	missingExec := composeRuntimeToolExecutor(nil, &llmcontracts.RuntimeTools{Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
		return "", false, false, nil
	}})
	_, isErr, err = missingExec(context.Background(), "missing", nil)
	if !isErr || err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing executor isErr=%v err=%v", isErr, err)
	}

}

func TestClaudeCodeMaxOutputTokens(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
	}{
		{"opus 5", "claude-opus-5", 64000},
		{"sonnet 5", "claude-sonnet-5", 64000},
		{"fable 5.1", "claude-fable-5-1", 64000},
		{"mythos 5.1", "claude-mythos-5-1", 64000},
		{"fable 5", "claude-fable-5", 64000},
		{"mythos 5", "claude-mythos-5", 64000},
		{"opus 4.8", "claude-opus-4-8", 64000},
		{"opus 4.7", "claude-opus-4-7-20260514", 64000},
		{"opus 4.6", "claude-opus-4-6-20260401", 64000},
		{"sonnet 4.6", "claude-sonnet-4-6-20260514", 32000},
		{"opus 4.5", "claude-opus-4-5-20251101", 32000},
		{"sonnet 4.5", "claude-sonnet-4-5-20250929", 32000},
		{"sonnet 4.0", "claude-sonnet-4-0-20250514", 32000},
		{"haiku 4.5", "claude-haiku-4-5-20251001", 32000},
		{"opus 4.1", "claude-opus-4-1-20250805", 32000},
		{"opus 4.0", "claude-opus-4-0-20250514", 32000},
		{"3.7 sonnet", "claude-3-7-sonnet-20250219", 32000},
		{"3.5 sonnet", "claude-3-5-sonnet-20241022", 8192},
		{"3.5 haiku", "claude-3-5-haiku-20241022", 8192},
		{"3 sonnet", "claude-3-sonnet-20240229", 8192},
		{"3 opus", "claude-3-opus-20240229", 4096},
		{"3 haiku", "claude-3-haiku-20240307", 4096},
		{"fallback", "claude-future-model", 32000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(claudeCodeMaxOutputTokensEnv, "")
			if got := claudeCodeMaxOutputTokens(tt.model); got != tt.want {
				t.Fatalf("claudeCodeMaxOutputTokens(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestClaudeCodeMaxOutputTokensOverrideClampsToUpperLimit(t *testing.T) {
	t.Setenv(claudeCodeMaxOutputTokensEnv, "999999")
	if got := claudeCodeMaxOutputTokens("claude-opus-5"); got != 128000 {
		t.Fatalf("opus 5 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-sonnet-5"); got != 128000 {
		t.Fatalf("sonnet 5 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-fable-5-1"); got != 128000 {
		t.Fatalf("fable 5.1 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-mythos-5-1"); got != 128000 {
		t.Fatalf("mythos 5.1 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-fable-5"); got != 128000 {
		t.Fatalf("fable 5 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-mythos-5"); got != 128000 {
		t.Fatalf("mythos 5 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-opus-4-7-20260514"); got != 128000 {
		t.Fatalf("opus 4.7 override = %d, want 128000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-sonnet-4-6-20260514"); got != 64000 {
		t.Fatalf("sonnet 4.6 override = %d, want 64000", got)
	}
	if got := claudeCodeMaxOutputTokens("claude-3-5-sonnet-20241022"); got != 8192 {
		t.Fatalf("3.5 sonnet override = %d, want 8192", got)
	}
}

func TestClaudeCodeMaxOutputTokensOverrideUsesParseIntSemantics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"positive value", "20000", 20000},
		{"numeric prefix", "20000extra", 20000},
		{"leading whitespace", " 20000", 20000},
		{"plus sign", "+20000", 20000},
		{"positive overflow caps", "999999999999999999999999999999999999999999", 64000},
		{"negative prefix falls back", "-1", 32000},
		{"invalid falls back", "not-a-number", 32000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(claudeCodeMaxOutputTokensEnv, tt.raw)
			if got := claudeCodeMaxOutputTokens("claude-sonnet-4-5-20250929"); got != tt.want {
				t.Fatalf("override %q = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

func TestClaudeCodeMaxOutputTokensInvalidOverrideUsesDefault(t *testing.T) {
	t.Setenv(claudeCodeMaxOutputTokensEnv, "not-a-number")
	if got := claudeCodeMaxOutputTokens("claude-opus-4-7-20260514"); got != 64000 {
		t.Fatalf("invalid override = %d, want default 64000", got)
	}
}
