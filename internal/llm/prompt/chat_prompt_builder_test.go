package prompt

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskFollowupSystemPrompt(t *testing.T) {
	if strings.Contains(TaskFollowupSystemPrompt, "task management assistant") {
		t.Error("TaskFollowupSystemPrompt should NOT contain 'task management assistant' — it should be a coding agent prompt")
	}
	if !strings.Contains(TaskFollowupSystemPrompt, "coding agent") {
		t.Error("TaskFollowupSystemPrompt should identify as a coding agent")
	}
	if strings.Contains(TaskFollowupSystemPrompt, "Your PRIMARY job is to create tasks") {
		t.Error("TaskFollowupSystemPrompt should NOT instruct to create tasks — follow-ups should execute code changes")
	}
	if !strings.Contains(TaskFollowupSystemPrompt, "execute") {
		t.Error("TaskFollowupSystemPrompt should instruct the model to execute instructions")
	}
}

func TestBuildChatSystemPrompt_TaskFollowupUsesCodingAgentPrompt(t *testing.T) {
	followupPrompt := BuildChatSystemPrompt(true, models.ChatModeOrchestrate, "", false)
	orchestrationPrompt := BuildChatSystemPrompt(false, models.ChatModeOrchestrate, "", false)

	if strings.Contains(followupPrompt, "task management assistant") {
		t.Error("task follow-up prompt should NOT contain 'task management assistant'")
	}
	if !strings.Contains(followupPrompt, "coding agent") {
		t.Error("task follow-up prompt should identify as a coding agent")
	}

	if !strings.Contains(orchestrationPrompt, "task management assistant") {
		t.Error("orchestration prompt should contain 'task management assistant'")
	}

	if strings.Contains(followupPrompt, "Your PRIMARY job is to create tasks") {
		t.Error("task follow-up prompt should NOT include orchestration task creation instructions")
	}
	if strings.Contains(followupPrompt, ChatTaskCreationInstructions) {
		t.Error("task follow-up prompt should NOT include ChatTaskCreationInstructions")
	}
}

func TestBuildChatSystemPrompt_IncludesSystemContext(t *testing.T) {
	ctx := "You are continuing work on task \"Fix login bug\"."
	followupPrompt := BuildChatSystemPrompt(true, models.ChatModeOrchestrate, ctx, false)
	orchestrationPrompt := BuildChatSystemPrompt(false, models.ChatModeOrchestrate, ctx, false)

	if !strings.Contains(followupPrompt, ctx) {
		t.Error("task follow-up prompt should include system context")
	}
	if !strings.Contains(orchestrationPrompt, ctx) {
		t.Error("orchestration prompt should include system context")
	}
}

func TestBuildChatSystemPrompt_IncludesMarkerReinforcement(t *testing.T) {
	prompt := BuildChatSystemPrompt(false, models.ChatModeOrchestrate, "", false)

	if !strings.Contains(prompt, "CRITICAL REMINDER — ACTION MARKERS:") {
		t.Error("orchestration prompt should include marker reinforcement section")
	}
	if !strings.Contains(prompt, "NEVER say \"I'll do X\" without actually outputting the marker") {
		t.Error("orchestration prompt should include anti-description reinforcement")
	}

	if !strings.Contains(prompt, "thinking about it or describing what you will do is NOT enough") {
		t.Error("Thread view instructions should include anti-description language")
	}

	if !strings.Contains(prompt, "Every follow-up request about a task") {
		t.Error("SEND_TO_TASK instructions should emphasize each request needs its own marker")
	}

	if !strings.Contains(prompt, "TASK SCHEDULING:") {
		t.Error("orchestration prompt should include SCHEDULE_TASK instructions")
	}
	if !strings.Contains(prompt, "[SCHEDULE_TASK]") {
		t.Error("orchestration prompt should reference [SCHEDULE_TASK] marker")
	}

	if !strings.Contains(prompt, "To schedule a task: output [SCHEDULE_TASK]...[/SCHEDULE_TASK]") {
		t.Error("marker reinforcement should include SCHEDULE_TASK")
	}

	followupPrompt := BuildChatSystemPrompt(true, models.ChatModeOrchestrate, "", false)
	if strings.Contains(followupPrompt, "CRITICAL REMINDER — ACTION MARKERS:") {
		t.Error("task followup prompt should NOT include marker reinforcement (only orchestration chat)")
	}
}

func TestBuildChatSystemPrompt_PlanModeUsesReadOnlyPlanPrompt(t *testing.T) {
	prompt := BuildChatSystemPrompt(false, models.ChatModePlan, "", false)

	if !strings.Contains(prompt, "PLAN MODE (read-only)") {
		t.Error("plan mode prompt should include explicit plan mode header")
	}
	if !strings.Contains(prompt, "read_file, list_files, grep_search") {
		t.Error("plan mode prompt should include read-only tool allowlist")
	}
	if strings.Contains(prompt, ChatTaskCreationInstructions) {
		t.Error("plan mode prompt should not include task creation instructions")
	}
	if !strings.Contains(prompt, "<proposed_plan>") {
		t.Error("plan mode prompt should require proposed plan wrapper")
	}
	if !strings.Contains(prompt, "Do not default to numbered lists") {
		t.Error("plan mode prompt should discourage rigid numbered-list formatting")
	}
	if !strings.Contains(prompt, "Use numbered steps only when strict ordering is essential") {
		t.Error("plan mode prompt should only allow numbering when ordering is required")
	}
}

func TestChatTaskCreationInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"CREATE_TASK marker", "[CREATE_TASK]"},
		{"/CREATE_TASK marker", "[/CREATE_TASK]"},
		{"JSON format example", `"title"`},
		{"JSON prompt field", `"prompt"`},
		{"JSON category field", `"category"`},
		{"active category", `"active"`},
		{"backlog category", `"backlog"`},
		{"immediate creation instruction", "output the [CREATE_TASK] block immediately"},
		{"no excessive confirmation", "Do not ask for confirmation"},
		{"create when in doubt", "When in doubt, create the task"},
		{"MUST output marker", "MUST output the [CREATE_TASK] block"},
		{"thinking not enough", "thinking about it is not enough"},
		{"concrete example response", "Fix the login bug"},
		{"ONLY way to create", "ONLY way to create a task"},
		{"proactive task creation for bugs", "describes a bug"},
		{"proactive task creation for features", "feature request"},
		{"no numbered options", "NEVER offer numbered options"},
		{"numbered option follow-through", "selects a numbered option"},
		{"concrete resources immediate task", "concrete resources"},
	}

	for _, r := range required {
		if !strings.Contains(ChatTaskCreationInstructions, r.content) {
			t.Errorf("ChatTaskCreationInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}

func TestChatTaskAwarenessInstructions_ContainsRequiredElements(t *testing.T) {
	required := []struct {
		name    string
		content string
	}{
		{"task listing reference", "Current tasks in this project"},
		{"task query capability", "Answer questions about existing tasks"},
		{"task explanation capability", "Explain what a specific task does"},
	}

	for _, r := range required {
		if !strings.Contains(ChatTaskAwarenessInstructions, r.content) {
			t.Errorf("ChatTaskAwarenessInstructions missing %s: expected to contain %q", r.name, r.content)
		}
	}
}
