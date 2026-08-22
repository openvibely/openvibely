package handler

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestDefaultAgentToolsIncludesSelectedMemoryView(t *testing.T) {
	tools := defaultAgentTools()
	if !agentToolsInclude(tools, "memory_view") {
		t.Fatalf("defaultAgentTools() = %#v, missing memory_view", tools)
	}
	if !agentToolsInclude(models.AllAgentTools, "memory_view") {
		t.Fatalf("models.AllAgentTools missing memory_view")
	}
}

func TestNormalizeAgentToolsKeepsRuntimeMessagingGitHubAndTaskGoalTools(t *testing.T) {
	input := []string{
		"get_task_goal",
		"mark_task_goal_achieved",
		"report_task_goal_blocked",
		"send_to_task",
		"send_message",
		"github_create_issue",
		"github_get_issue",
		"github_get_project_inbox",
		"github_is_actor_authorized",
		"github_list_my_assigned_issues",
		"github_list_assigned_issues",
		"github_list_assigned_issues_with_prs",
		"github_comment_on_issue",
		"github_add_issue_labels",
		"github_close_issue",
		"github_open_pull_request",
		"create_alert",
		"create_notification",
		"list_alerts",
		"get_alert",
		"decide_alert",
		"claim_alert",
		"create_alert_implementation_task",
		"link_alert_implementation_task",
		"complete_alert_processing",
		"fail_alert_processing",
		"release_alert_claim",
	}

	tools := normalizeAgentTools(input)
	for _, want := range input {
		if !agentToolsInclude(tools, want) {
			t.Fatalf("normalizeAgentTools() = %#v, missing %q", tools, want)
		}
	}

	for _, want := range []string{
		"set_task_goal",
		"clear_task_goal",
		"pause_task_goal",
		"resume_task_goal",
	} {
		if !agentToolsInclude(models.AllAgentTools, want) {
			t.Fatalf("models.AllAgentTools missing %q", want)
		}
	}
}
