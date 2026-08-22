package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFilterAssignedAgentRuntimeToolDefs_IncludesSendMessageByDefault(t *testing.T) {
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)

	filtered := toolDefNameSet(filterAssignedAgentRuntimeToolDefs(defs, &models.Agent{Tools: []string{"Read"}}))
	if !filtered["send_message"] {
		t.Fatalf("assigned task agents should get send_message by default, got %+v", filtered)
	}
	if !filtered["create_task"] {
		t.Fatalf("assigned-agent filter should preserve unrelated chat tools, got %+v", filtered)
	}
}

func TestFilterAssignedAgentCapabilitySummaries_IncludesSendMessageByDefault(t *testing.T) {
	summaries := chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	filtered := capabilityNameSet(filterAssignedAgentCapabilitySummaries(summaries, &models.Agent{Tools: []string{"Read"}}))
	if !filtered["send_message"] {
		t.Fatalf("assigned task agents should advertise send_message by default, got %+v", filtered)
	}
	if !filtered["create_task"] {
		t.Fatalf("assigned-agent capability filter should preserve unrelated capabilities, got %+v", filtered)
	}
}

func TestFilterTaskThreadRuntimeToolDefs_GoalStatusToolsRequireExplicitGrant(t *testing.T) {
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)

	ungranted := toolDefNameSet(filterTaskThreadRuntimeToolDefs(defs, nil, false))
	if ungranted["mark_task_goal_achieved"] || ungranted["report_task_goal_blocked"] {
		t.Fatalf("ungranted task agent got goal status tools: %+v", ungranted)
	}
	if !ungranted["get_task_goal"] || !ungranted["send_to_task"] {
		t.Fatalf("base task goal tools missing for ungranted agent: %+v", ungranted)
	}
	if !ungranted["send_message"] {
		t.Fatalf("task agents should get send_message by default: %+v", ungranted)
	}
	if !ungranted["create_task"] || !ungranted["execute_tasks"] || !ungranted["schedule_task"] || !ungranted["modify_schedule"] {
		t.Fatalf("task agents should get visible task creation/execution and schedule bootstrap tools by default: %+v", ungranted)
	}
	if !ungranted["github_get_issue"] || !ungranted["github_get_project_inbox"] || !ungranted["github_is_actor_authorized"] || !ungranted["github_list_my_assigned_issues"] || !ungranted["github_list_assigned_issues"] || !ungranted["github_close_issue"] || !ungranted["github_open_pull_request"] || !ungranted["github_replace_pull_request_branch"] || !ungranted["github_forward_pr_feedback_to_tasks"] {
		t.Fatalf("task agents should get GitHub issue tools by default: %+v", ungranted)
	}
	if ungranted["memory_view"] {
		t.Fatalf("unselected memory_view runtime tool was exposed: %+v", ungranted)
	}
	withMemory := toolDefNameSet(filterTaskThreadRuntimeToolDefs(defs, nil, true))
	if !withMemory["memory_view"] {
		t.Fatalf("selected memory_view runtime tool missing: %+v", withMemory)
	}

	agentDef := &models.Agent{Tools: []string{"mark_task_goal_achieved", "send_message"}}
	granted := toolDefNameSet(filterTaskThreadRuntimeToolDefs(defs, agentDef, false))
	if !granted["mark_task_goal_achieved"] {
		t.Fatalf("explicitly granted goal achieved tool missing: %+v", granted)
	}
	if !granted["send_message"] {
		t.Fatalf("explicitly granted send_message tool missing: %+v", granted)
	}
	if granted["report_task_goal_blocked"] {
		t.Fatalf("ungranted blocked-report tool was exposed: %+v", granted)
	}
}

func TestFilterTaskThreadRuntimeToolDefs_CreateNotificationDispatchUsesPersistedTaskContext(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Task Thread Notification Project")
	task := createTask(t, h, project.ID, "Task Thread Notification Source", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusPending
	})
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	advertised := toolDefNameSet(defs)
	require.True(t, advertised["create_notification"], "task-thread runtime must advertise create_notification")
	require.True(t, advertised["decide_alert"], "task-thread runtime must advertise decide_alert")
	capabilities := capabilityNameSet(filterTaskThreadCapabilitySummaries(
		chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb), nil, false,
	))
	require.True(t, capabilities["create_notification"], "task-thread capabilities must include create_notification")
	require.True(t, capabilities["decide_alert"], "task-thread capabilities must include decide_alert")

	runtime := h.buildChatActionToolRuntimeFromDefs(streamingResponseParams{
		TaskID:         task.ID,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	}, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	output, handled, isErr, err := runtime.Executor(ctx, "create_notification", json.RawMessage(`{
		"type":"task_thread_suggestion",
		"title":"Follow-up suggestion",
		"body":"Created from an ordinary task follow-up"
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)

	var result struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.NotEmpty(t, result.Notification.ID)
	stored, err := h.alertSvc.GetByID(ctx, project.ID, result.Notification.ID)
	require.NoError(t, err)
	require.Equal(t, project.ID, stored.ProjectID)
	require.Equal(t, models.AlertDecisionPending, stored.DecisionState)
	require.Equal(t, models.AlertProcessingUnclaimed, stored.ProcessingState)
	require.NotNil(t, stored.SourceTaskID)
	require.Equal(t, task.ID, *stored.SourceTaskID)

	decisionOutput, handled, isErr, err := runtime.Executor(ctx, "decide_alert", json.RawMessage(`{"alert_id":"`+result.Notification.ID+`","decision":"approved"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, decisionOutput, `"decision_state":"approved"`)
	decided, err := h.alertSvc.GetByID(ctx, project.ID, result.Notification.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertDecisionApproved, decided.DecisionState)
}

func TestFilterTaskThreadRuntimeToolDefs_HaveWebHandlers(t *testing.T) {
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, true)
	handlers := (&Handler{}).chatActionHandlers(streamingResponseParams{ExecID: "exec", ProjectID: "project", IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	advertised := toolDefNameSet(defs)

	for _, name := range []string{
		"create_task",
		"schedule_task",
		"modify_schedule",
		"decide_alert",
		"github_get_project_inbox",
		"github_list_my_assigned_issues",
		"github_list_assigned_issues",
		"github_list_assigned_issues_with_prs",
		"github_close_issue",
		"github_open_pull_request",
		"github_replace_pull_request_branch",
		"github_forward_pr_feedback_to_tasks",
	} {
		if !advertised[name] {
			t.Fatalf("task-thread web runtime did not advertise required GitHub tool %s", name)
		}
		if _, ok := handlers[name]; !ok {
			t.Fatalf("task-thread web runtime advertised %s without a handler", name)
		}
	}

	for _, def := range defs {
		if def.Name == "memory_view" {
			continue
		}
		if _, ok := handlers[def.Name]; !ok {
			t.Fatalf("task-thread web runtime advertised %s without a handler", def.Name)
		}
	}
}

func TestFilterTaskThreadCapabilitySummaries_GoalStatusToolsRequireExplicitGrant(t *testing.T) {
	summaries := chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	ungranted := capabilityNameSet(filterTaskThreadCapabilitySummaries(summaries, nil, false))
	if ungranted["mark_task_goal_achieved"] || ungranted["report_task_goal_blocked"] {
		t.Fatalf("ungranted task agent capabilities advertised goal status tools: %+v", ungranted)
	}
	if ungranted["memory_view"] {
		t.Fatalf("unselected memory_view capability was advertised: %+v", ungranted)
	}
	if !ungranted["get_task_goal"] || !ungranted["send_to_task"] || !ungranted["list_capabilities"] {
		t.Fatalf("base task-thread capabilities missing for ungranted agent: %+v", ungranted)
	}
	if !ungranted["create_task"] || !ungranted["schedule_task"] || !ungranted["modify_schedule"] {
		t.Fatalf("task-thread capabilities should include visible task/schedule bootstrap tools: %+v", ungranted)
	}
	if !ungranted["send_message"] {
		t.Fatalf("task agents should advertise send_message by default: %+v", ungranted)
	}
	if !ungranted["github_get_issue"] || !ungranted["github_get_project_inbox"] || !ungranted["github_is_actor_authorized"] || !ungranted["github_list_my_assigned_issues"] || !ungranted["github_list_assigned_issues"] || !ungranted["github_close_issue"] || !ungranted["github_open_pull_request"] || !ungranted["github_replace_pull_request_branch"] || !ungranted["github_forward_pr_feedback_to_tasks"] {
		t.Fatalf("task agents should advertise GitHub issue tools by default: %+v", ungranted)
	}

	withMemory := capabilityNameSet(filterTaskThreadCapabilitySummaries(summaries, nil, true))
	if !withMemory["memory_view"] {
		t.Fatalf("selected memory_view capability missing: %+v", withMemory)
	}

	agentDef := &models.Agent{Tools: []string{"report_task_goal_blocked", "send_message"}}
	granted := capabilityNameSet(filterTaskThreadCapabilitySummaries(summaries, agentDef, false))
	if !granted["report_task_goal_blocked"] {
		t.Fatalf("explicitly granted blocked-report capability missing: %+v", granted)
	}
	if !granted["send_message"] {
		t.Fatalf("explicitly granted send_message capability missing: %+v", granted)
	}
	if granted["mark_task_goal_achieved"] {
		t.Fatalf("ungranted achieved capability was advertised: %+v", granted)
	}
}

func toolDefNameSet(defs []llmcontracts.RuntimeToolDefinition) map[string]bool {
	out := make(map[string]bool, len(defs))
	for _, def := range defs {
		out[def.Name] = true
	}
	return out
}

func capabilityNameSet(summaries []chatcontrol.ActionSummary) map[string]bool {
	out := make(map[string]bool, len(summaries))
	for _, summary := range summaries {
		out[summary.Name] = true
	}
	return out
}
