package prompt

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

var legacyMutationMarkers = []string{
	"[CREATE_TASK]",
	"[EDIT_TASK]",
	"[EXECUTE_TASKS]",
	"[SEND_TO_TASK]",
	"[SCHEDULE_TASK]",
	"[DELETE_SCHEDULE]",
	"[MODIFY_SCHEDULE]",
	"[SET_PERSONALITY]",
	"[CREATE_ALERT]",
	"[DELETE_ALERT]",
	"[TOGGLE_ALERT]",
	"[SWITCH_PROJECT]",
}

func TestApplyTaskCreationToolMode(t *testing.T) {
	base := "Task objective"
	toolPrompt := ApplyTaskCreationToolMode(base, []string{"Read", "create_task"})
	if !strings.Contains(toolPrompt, TaskCreationToolModeInstructions) || !strings.Contains(toolPrompt, "Available runtime task tools: Read, create_task") {
		t.Fatalf("tool-mode task prompt missing runtime guidance: %q", toolPrompt)
	}
	for _, marker := range legacyMutationMarkers {
		if strings.Contains(toolPrompt, marker) {
			t.Fatalf("tool-mode task prompt advertised legacy marker %s: %q", marker, toolPrompt)
		}
	}
	if got := ApplyTaskCreationToolMode(base, []string{"Read"}); got != base {
		t.Fatalf("unrelated runtime tool changed task prompt: %q", got)
	}
	noToolPrompt := ApplyTaskCreationToolMode(base, nil)
	if !strings.Contains(noToolPrompt, ChatActionUnavailableInstructions) {
		t.Fatalf("no-tool task prompt missing capability limitation: %q", noToolPrompt)
	}
	for _, marker := range legacyMutationMarkers {
		if strings.Contains(noToolPrompt, marker) {
			t.Fatalf("no-tool task prompt advertised legacy marker %s: %q", marker, noToolPrompt)
		}
	}
}

func TestApplyChatActionToolModeReportsConcreteCapability(t *testing.T) {
	base := BuildChatSystemPrompt(false, models.ChatModeOrchestrate, "", false)
	capable := ApplyChatActionToolMode(base, []string{"create_task", "edit_task"})
	if !strings.Contains(capable, ChatActionToolModeInstructions) || !strings.Contains(capable, "Available action tools: create_task, edit_task") {
		t.Fatalf("capable prompt missing runtime action guidance: %q", capable)
	}
	incapable := ApplyChatActionToolMode(base, nil)
	if !strings.Contains(incapable, ChatActionUnavailableInstructions) {
		t.Fatalf("incapable prompt missing capability limitation: %q", incapable)
	}
	for _, prompt := range []string{capable, incapable} {
		for _, marker := range legacyMutationMarkers {
			if strings.Contains(prompt, marker) {
				t.Fatalf("chat prompt advertised legacy marker %s: %q", marker, prompt)
			}
		}
	}
}

func TestBuildChatSystemPromptModes(t *testing.T) {
	followup := BuildChatSystemPrompt(true, models.ChatModeOrchestrate, "selected context", false)
	if strings.Contains(followup, "task management assistant") || !strings.Contains(followup, "coding agent") || !strings.Contains(followup, "selected context") {
		t.Fatalf("unexpected follow-up prompt: %q", followup)
	}

	orchestrate := BuildChatSystemPrompt(false, models.ChatModeOrchestrate, "task context", false)
	if !strings.Contains(orchestrate, "task management assistant") || !strings.Contains(orchestrate, "task context") {
		t.Fatalf("unexpected orchestrate prompt: %q", orchestrate)
	}

	plan := BuildChatSystemPrompt(false, models.ChatModePlan, "", false)
	for _, want := range []string{"PLAN MODE (read-only)", "read_file, list_files, grep_search", "<proposed_plan>", "Do not default to numbered lists", "Use numbered steps only when strict ordering is essential"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan prompt missing %q: %q", want, plan)
		}
	}

	for _, prompt := range []string{followup, orchestrate, plan} {
		for _, marker := range legacyMutationMarkers {
			if strings.Contains(prompt, marker) {
				t.Fatalf("base chat prompt advertised legacy marker %s: %q", marker, prompt)
			}
		}
	}
}

func TestBuildChatSystemPrompt_TaskFollowupIncludesSelectedMemoryContext(t *testing.T) {
	selectedMemoryContext := "## Selected Memories For This Task\n\n<selected_memories>\n- `chat_memory.md`\n</selected_memories>"
	prompt := BuildChatSystemPrompt(true, models.ChatModeOrchestrate, selectedMemoryContext, false)
	for _, want := range []string{"coding agent", "## Selected Memories For This Task", "<selected_memories>", "`chat_memory.md`"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("task follow-up provider prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestChatTaskAwarenessInstructions_ContainsRequiredElements(t *testing.T) {
	for _, want := range []string{"Current tasks in this project", "answer questions", "explain a task"} {
		if !strings.Contains(ChatTaskAwarenessInstructions, want) {
			t.Errorf("ChatTaskAwarenessInstructions missing %q", want)
		}
	}
}
