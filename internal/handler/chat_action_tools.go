package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func supportsChatActionTools(agent models.LLMConfig) bool {
	return service.SupportsRuntimeChatActionTools(context.Background(), nil, agent)
}

func (h *Handler) supportsChatActionTools(ctx context.Context, agent models.LLMConfig) bool {
	if h == nil {
		return false
	}
	return service.SupportsRuntimeChatActionTools(ctx, h.llmConfigRepo, agent)
}

type chatActionSummaryCollector struct {
	createdLines []string
	editedLines  []string
}

func newChatActionSummaryCollector() *chatActionSummaryCollector {
	return &chatActionSummaryCollector{
		createdLines: []string{},
		editedLines:  []string{},
	}
}

func (c *chatActionSummaryCollector) addCreated(summary string) {
	c.addMarkerLines(summary, "[TASK_ID:", &c.createdLines)
}

func (c *chatActionSummaryCollector) addEdited(summary string) {
	c.addMarkerLines(summary, "[TASK_EDITED:", &c.editedLines)
}

func (c *chatActionSummaryCollector) addMarkerLines(summary, marker string, target *[]string) {
	if c == nil || summary == "" || marker == "" {
		return
	}
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, marker) {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			line = "- " + line
		}
		if containsSummaryLine(*target, line) {
			continue
		}
		*target = append(*target, line)
	}
}

func containsSummaryLine(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

type createSwarmTaskToolInput struct {
	Title             string `json:"title"`
	Prompt            string `json:"prompt"`
	Goal              string `json:"goal"`
	ProjectID         string `json:"project_id"`
	Category          string `json:"category"`
	Priority          int    `json:"priority"`
	AgentID           string `json:"agent_id"`
	AgentDefinitionID string `json:"agent_definition_id"`
	Agent             string `json:"agent"`
	Tag               string `json:"tag"`
	MaxWorkers        int    `json:"max_workers"`
	WorkerIsolation   string `json:"worker_isolation"`
	ReviewerEnabled   *bool  `json:"reviewer_enabled"`
	MergerEnabled     *bool  `json:"merger_enabled"`
	StartImmediately  *bool  `json:"start_immediately"`
	MergeTargetBranch string `json:"merge_target_branch"`
}

func isChannelActionSurface(surface chatcontrol.Surface) bool {
	switch surface {
	case chatcontrol.SurfaceSlack, chatcontrol.SurfaceTelegram, chatcontrol.SurfaceDiscord, chatcontrol.SurfaceEmail:
		return true
	default:
		return false
	}
}

func (h *Handler) executeCreateSwarmTaskTool(ctx context.Context, params streamingResponseParams, input json.RawMessage, collector *chatActionSummaryCollector) (string, error) {
	if h.swarmSvc == nil {
		return "", fmt.Errorf("create_swarm_task: swarm service unavailable")
	}
	var req createSwarmTaskToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if !isChannelActionSurface(params.Surface) {
		if override := strings.TrimSpace(req.ProjectID); override != "" {
			projectID = override
		}
	}
	if projectID == "" {
		return "", fmt.Errorf("create_swarm_task: no current project")
	}
	category := models.CategoryActive
	if strings.EqualFold(strings.TrimSpace(req.Category), string(models.CategoryBacklog)) {
		category = models.CategoryBacklog
	}
	priority := req.Priority
	if priority == 0 {
		priority = 2
	}
	if priority < 1 || priority > 4 {
		return "", fmt.Errorf("create_swarm_task: priority must be between 1 and 4")
	}
	tag := models.TaskTag(strings.TrimSpace(req.Tag))
	switch tag {
	case models.TagNone, models.TagFeature, models.TagBug:
	default:
		return "", fmt.Errorf("create_swarm_task: tag must be bug or feature")
	}
	reviewerEnabled := true
	if req.ReviewerEnabled != nil {
		reviewerEnabled = *req.ReviewerEnabled
	}
	mergerEnabled := true
	if req.MergerEnabled != nil {
		mergerEnabled = *req.MergerEnabled
	}
	var agentID *string
	if trimmed := strings.TrimSpace(req.AgentID); trimmed != "" {
		agentID = &trimmed
	}
	var agentDefinitionID *string
	if strings.TrimSpace(req.AgentDefinitionID) != "" || strings.TrimSpace(req.Agent) != "" {
		resolved, err := service.ResolveTaskCreationAgentDefinition(ctx, service.TaskCreationRequest{AgentDefinitionID: req.AgentDefinitionID, Agent: req.Agent}, projectID, h.taskSvc)
		if err != nil {
			return "", fmt.Errorf("create_swarm_task: %w", err)
		}
		if resolved != "" {
			agentDefinitionID = &resolved
		}
	}
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{ProjectID: projectID, Title: req.Title, Prompt: req.Prompt, Goal: req.Goal, Category: category, Priority: priority, AgentID: agentID, AgentDefinitionID: agentDefinitionID, Tag: tag, MaxWorkers: req.MaxWorkers, WorkerIsolation: req.WorkerIsolation, ReviewerEnabled: reviewerEnabled, MergerEnabled: mergerEnabled, StartImmediately: req.StartImmediately, MergeTargetBranch: req.MergeTargetBranch})
	if err != nil {
		return "", err
	}
	plannerMessage := "Planner starts when the swarm parent is Active."
	summary := fmt.Sprintf("Created swarm task: %s.\n%s\n- \"%s\" (%s) [TASK_ID:%s]", parent.Title, plannerMessage, parent.Title, parent.Category, parent.ID)
	if collector != nil {
		collector.addCreated(summary)
	}
	return summary, nil
}

func (c *chatActionSummaryCollector) appendToOutput(output string) string {
	if c == nil {
		return output
	}
	var blocks []string
	if len(c.createdLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Created %d task(s):\n", len(c.createdLines)))
		b.WriteString(strings.Join(c.createdLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(c.editedLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Edited %d task(s):\n", len(c.editedLines)))
		b.WriteString(strings.Join(c.editedLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return output
	}

	summary := "\n\n---\n" + strings.Join(blocks, "\n\n")
	if strings.Contains(output, summary) {
		return output
	}
	return output + summary
}

// buildChatActionToolRuntime creates request-scoped runtime tools using the
// canonical registry and direct typed action handlers.
func (h *Handler) buildChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceWeb
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	// Web handler includes thread tools for orchestrate mode
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatcontrol.ToolDefsForContext(mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// buildAPIChatActionToolRuntime creates a RuntimeTools for the API surface.
func (h *Handler) buildAPIChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceAPI
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatcontrol.ToolDefsForContext(mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// chatActionExecutor returns a RuntimeToolExecutor that uses the canonical
// chatcontrol execution engine with a surface-aware action handler map.
func (h *Handler) chatActionExecutor(params streamingResponseParams, collector *chatActionSummaryCollector, mode models.ChatMode, surface chatcontrol.Surface) llmcontracts.RuntimeToolExecutor {
	handlers := h.chatActionHandlers(params, collector, mode, surface)
	return chatcontrol.BuildRuntimeToolExecutor(mode, surface, handlers)
}

func (h *Handler) chatActionHandlers(params streamingResponseParams, collector *chatActionSummaryCollector, mode models.ChatMode, surface chatcontrol.Surface) map[string]chatcontrol.RuntimeActionHandler {
	alertHandlers := service.BuildAlertRuntimeActionHandlers(service.AlertRuntimeOptions{
		ProjectID: params.ProjectID, CallerTaskID: params.TaskID, Source: "agent", AlertSvc: h.alertSvc, TaskRepo: h.taskRepo,
	})
	goalHandlers := h.taskGoalActionHandlers(params)
	handlers := map[string]chatcontrol.RuntimeActionHandler{
		"list_automations": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeListAutomationsTool(ctx, params, input)
		},
		"get_automation": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGetAutomationTool(ctx, params, input)
		},
		"preview_automation_description": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeAutomationPreviewAction(ctx, params, input)
		},
		"save_automation": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeAutomationSaveAction(ctx, params, input)
		},
		"run_automation_now": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeRunAutomationNowTool(ctx, params, input)
		},
		"pause_automation": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executePauseAutomationTool(ctx, params, input)
		},
		"resume_automation": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeResumeAutomationTool(ctx, params, input)
		},
		"create_swarm_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeCreateSwarmTaskTool(ctx, params, input, collector)
		},
		"create_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.TaskCreationRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(params.ProjectID) == "" {
				return "", fmt.Errorf("create_task: no current project — cannot create task without a project context")
			}
			if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
				return "", fmt.Errorf("create_task requires title and prompt")
			}
			if req.Priority == 0 {
				req.Priority = 2
			}
			agents, err := h.llmConfigRepo.List(ctx)
			if err != nil {
				agents = nil
			}
			summary, _ := h.executeChatTaskCreationRequests(ctx, params.ExecID, params.ProjectID, []service.TaskCreationRequest{req}, agents, params.ChannelReply)
			createdIDs := extractTaskIDsFromOutput(summary)
			if len(createdIDs) == 0 {
				return summary, fmt.Errorf("create_task: no tasks were persisted (see summary for details)")
			}
			if h.taskRepo != nil {
				var missing []string
				for _, id := range createdIDs {
					task, getErr := h.taskRepo.GetByID(ctx, id)
					if getErr != nil || task == nil || task.ProjectID != params.ProjectID {
						missing = append(missing, id)
					}
				}
				if len(missing) > 0 {
					return summary, fmt.Errorf("create_task: %d task(s) reported as created are not present in project %s: %s", len(missing), params.ProjectID, strings.Join(missing, ", "))
				}
			}
			if collector != nil {
				collector.addCreated(summary)
			}
			return strings.TrimSpace(summary), nil
		},
		"edit_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.TaskEditRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.ID) == "" {
				return "", fmt.Errorf("edit_task requires id")
			}
			summary := h.executeChatTaskEditRequests(ctx, params.ExecID, params.ProjectID, []service.TaskEditRequest{req})
			trimmedSummary := strings.TrimSpace(summary)
			if strings.Contains(summary, "Failed to edit") && !strings.Contains(summary, "[TASK_EDITED:") {
				return trimmedSummary, fmt.Errorf("edit_task: no tasks were updated")
			}
			if collector != nil {
				collector.addEdited(summary)
			}
			return trimmedSummary, nil
		},
		"execute_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.TaskExecutionRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" && len(req.Tags) == 0 && req.MinPriority == 0 {
				return "", fmt.Errorf("execute_tasks requires task_id/title or tags/min_priority")
			}
			return strings.TrimSpace(h.executeChatTaskExecutionRequests(ctx, params.ExecID, params.ProjectID, []service.TaskExecutionRequest{req})), nil
		},
		"list_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			return service.ExecuteListTasksTool(ctx, h.taskRepo, params.ProjectID, input)
		},
		"view_task_thread": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.ViewThreadRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" && params.IsTaskFollowup && params.TaskID != "" {
				req.TaskID = "current"
			}
			return h.executeViewTaskThreadRequest(ctx, params, req)
		},
		"send_to_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeSendToTaskTool(ctx, params, input)
		},
		"send_message": func(ctx context.Context, input json.RawMessage) (string, error) {
			if h.channelMessageRouter == nil {
				return "", fmt.Errorf("channel message router unavailable")
			}
			return service.ExecuteSendMessageTool(ctx, h.channelMessageRouter.WithAuditContext(string(surface), params.RuntimeOriginAgent), params.ProjectID, input)
		},
		"github_create_issue": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubCreateIssueTool(ctx, params.ProjectID, input)
		},
		"github_get_issue": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubGetIssueTool(ctx, params.ProjectID, input)
		},
		"github_get_project_inbox": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubGetProjectInboxTool(ctx, params.ProjectID, input)
		},
		"github_is_actor_authorized": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubIsActorAuthorizedTool(ctx, input)
		},
		"github_list_my_assigned_issues": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubListMyAssignedIssuesTool(ctx, params.ProjectID, input)
		},
		"github_list_existing_automation_issues": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubListExistingAutomationIssuesTool(ctx, params.ProjectID, input)
		},
		"github_list_assigned_issues": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubListAssignedIssuesTool(ctx, params.ProjectID, input)
		},
		"github_list_assigned_issues_with_prs": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubListAssignedIssuesWithPRsTool(ctx, params.ProjectID, input)
		},
		"github_comment_on_issue": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubCommentOnIssueTool(ctx, params.ProjectID, input)
		},
		"github_add_issue_labels": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubAddIssueLabelsTool(ctx, params.ProjectID, input)
		},
		"github_close_issue": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubCloseIssueTool(ctx, params.ProjectID, input)
		},
		"github_open_pull_request": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubOpenPullRequestTool(ctx, params, input)
		},
		"github_replace_pull_request_branch": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubReplacePullRequestBranchTool(ctx, params, input)
		},
		"github_forward_pr_feedback_to_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGitHubForwardPRFeedbackToTasksTool(ctx, params.ProjectID, input)
		},
		"schedule_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.ScheduleTaskRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := h.normalizeScheduleToolTaskReference(ctx, params, &req.TaskID, &req.Title, true); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeChatScheduleRequests(ctx, params.ProjectID, []service.ScheduleTaskRequest{req})), nil
		},
		"delete_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.DeleteScheduleRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := h.normalizeScheduleToolTaskReference(ctx, params, &req.TaskID, &req.Title, strings.TrimSpace(req.ScheduleID) == ""); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeChatDeleteScheduleRequests(ctx, params.ProjectID, []service.DeleteScheduleRequest{req})), nil
		},
		"modify_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.ModifyScheduleRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := h.normalizeScheduleToolTaskReference(ctx, params, &req.TaskID, &req.Title, strings.TrimSpace(req.ScheduleID) == ""); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeChatModifyScheduleRequests(ctx, params.ProjectID, []service.ModifyScheduleRequest{req})), nil
		},
		"list_schedules": func(ctx context.Context, input json.RawMessage) (string, error) {
			return service.ExecuteListSchedulesTool(ctx, h.scheduleRepo, params.ProjectID, input)
		},
		"list_personalities": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeListPersonalities(ctx)), nil
		},
		"get_personality": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return h.executeGetPersonality(ctx), nil
		},
		"set_personality": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.SetPersonalityRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeSetPersonality(ctx, req)), nil
		},
		"list_models": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeListModels(ctx)), nil
		},
		"get_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGetModel(ctx, input), nil
		},
		"list_agents": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeListAgents(ctx)), nil
		},
		"view_settings": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeViewSettings(ctx)), nil
		},
		"project_info": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeProjectInfo(ctx, params.ProjectID)), nil
		},
		"get_current_project": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return h.executeGetCurrentProject(ctx, params.ProjectID), nil
		},
		"list_projects": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return strings.TrimSpace(h.executeListProjects(ctx, params.ProjectID)), nil
		},
		"switch_project": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeSwitchProject(ctx, params.ProjectID, input), nil
		},
		"list_alerts":                            alertHandlers["list_alerts"],
		"get_alert":                              alertHandlers["get_alert"],
		"list_existing_automation_notifications": alertHandlers["list_existing_automation_notifications"],
		"create_alert":                           alertHandlers["create_alert"],
		"create_notification":                    alertHandlers["create_notification"],
		"claim_alert":                            alertHandlers["claim_alert"],
		"create_alert_implementation_task":       alertHandlers["create_alert_implementation_task"],
		"link_alert_implementation_task":         alertHandlers["link_alert_implementation_task"],
		"complete_alert_processing":              alertHandlers["complete_alert_processing"],
		"fail_alert_processing":                  alertHandlers["fail_alert_processing"],
		"release_alert_claim":                    alertHandlers["release_alert_claim"],
		"delete_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.DeleteAlertRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeDeleteAlertRequests(ctx, params.ProjectID, []service.DeleteAlertRequest{req})), nil
		},
		"toggle_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req service.ToggleAlertRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return strings.TrimSpace(h.executeToggleAlertRequests(ctx, params.ProjectID, []service.ToggleAlertRequest{req})), nil
		},
		"memory_view": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "memory_view is available only after the lifecycle memory router selects memory handles for this turn. Use memory_view only with handles listed in the selected memory index.", nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return fmt.Sprintf("Current chat mode: %s", mode), nil
		},
		"set_chat_mode": func(_ context.Context, input json.RawMessage) (string, error) {
			var req struct {
				Mode string `json:"mode"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			newMode := models.NormalizeChatMode(req.Mode)
			return fmt.Sprintf("Chat mode set to %s. The mode change will take effect on the next message.", newMode), nil
		},
		"list_capabilities": func(ctx context.Context, _ json.RawMessage) (string, error) {
			summaries := chatcontrol.ListForContext(mode, surface)
			selectedMemoryHandles := service.SelectedMemoryHandlesFromContext(ctx)
			if params.IsTaskFollowup {
				summaries = filterTaskThreadCapabilitySummaries(summaries, params.AgentDefinition, len(selectedMemoryHandles) > 0)
			} else if params.AgentDefinition != nil {
				summaries = filterAssignedAgentCapabilitySummaries(summaries, params.AgentDefinition)
			}
			return formatCapabilities(summaries, selectedMemoryHandles), nil
		},
	}
	for name, handler := range goalHandlers {
		handlers[name] = handler
	}
	return handlers
}

// ---- New action executors ----

type githubCreateIssueToolInput struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	RepoURL   string   `json:"repo_url"`
}

func (h *Handler) resolveGitHubRepoForTool(ctx context.Context, projectID string) (*service.GitHubRepoRef, error) {
	return h.resolveGitHubRepoForToolURL(ctx, projectID, "")
}

func (h *Handler) resolveGitHubRepoForToolURL(ctx context.Context, projectID, repoURL string) (*service.GitHubRepoRef, error) {
	if h.githubSvc == nil {
		return nil, fmt.Errorf("github service unavailable")
	}
	if h.projectRepo == nil {
		return nil, fmt.Errorf("project repository unavailable")
	}
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, fmt.Errorf("current project not found")
	}
	return service.ResolveGitHubToolRepository(ctx, h.githubSvc, projectID, repoURL, project)
}

func requireAutomationGitHubRepo(ctx context.Context, projectID string, project *models.Project) error {
	if automationContext, ok := service.AutomationContextFromContext(ctx); ok && automationContext.ProjectID == projectID &&
		(project == nil || (strings.TrimSpace(project.RepoURL) == "" && strings.TrimSpace(project.RepoPath) == "")) {
		return fmt.Errorf("Automation GitHub runtime requires a project repository URL or local Git checkout")
	}
	return nil
}

func (h *Handler) executeGitHubCreateIssueTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	var req githubCreateIssueToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	repo, err := h.resolveGitHubRepoForToolURL(ctx, projectID, req.RepoURL)
	if err != nil {
		return "", err
	}
	issue, err := h.githubSvc.CreateIssue(ctx, repo, service.GitHubCreateIssueRequest{Title: req.Title, Body: req.Body, Labels: req.Labels, Assignees: req.Assignees})
	if err != nil {
		return "", err
	}
	return githubToolJSON(map[string]any{"ok": true, "issue": issue})
}

func (h *Handler) githubIssueActionCore(projectID string) *service.GitHubIssueActionCore {
	return service.NewGitHubIssueActionCore(h.githubSvc, h.githubAuthRepo, projectID,
		chatcontrol.DecodeRuntimeToolInput,
		func(ctx context.Context, repoURL string) (*service.GitHubRepoRef, error) {
			return h.resolveGitHubRepoForToolURL(ctx, projectID, repoURL)
		})
}

func (h *Handler) executeGitHubGetIssueTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteGetIssue(ctx, input)
}

func (h *Handler) executeGitHubGetProjectInboxTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteGetProjectInbox(ctx, input)
}

func (h *Handler) executeGitHubIsActorAuthorizedTool(ctx context.Context, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore("").ExecuteIsActorAuthorized(ctx, input)
}

func (h *Handler) executeGitHubListMyAssignedIssuesTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteListMyAssignedIssues(ctx, input, nil)
}

func (h *Handler) executeGitHubListExistingAutomationIssuesTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteListExistingAutomationIssues(ctx, input)
}

func (h *Handler) executeGitHubListAssignedIssuesTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteListAssignedIssues(ctx, input, nil)
}

func (h *Handler) executeGitHubListAssignedIssuesWithPRsTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteListAssignedIssuesWithPRs(ctx, input)
}

func (h *Handler) executeGitHubCommentOnIssueTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteCommentOnIssue(ctx, input)
}

func (h *Handler) executeGitHubAddIssueLabelsTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteAddIssueLabels(ctx, input)
}

func (h *Handler) executeGitHubCloseIssueTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	return h.githubIssueActionCore(projectID).ExecuteCloseIssue(ctx, input)
}

func (h *Handler) executeGitHubForwardPRFeedbackToTasksTool(ctx context.Context, projectID string, input json.RawMessage) (string, error) {
	if h.taskPullRequestRepo == nil || h.githubPRFeedbackRepo == nil || h.githubAuthRepo == nil || h.threadInputRepo == nil {
		return "", fmt.Errorf("github pr feedback forwarding dependencies unavailable")
	}
	var req service.GitHubIssueActionRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	repo, err := h.resolveGitHubRepoForToolURL(ctx, projectID, req.RepoURL)
	if err != nil {
		return "", err
	}
	result, err := service.NewGitHubPRFeedbackForwarder(h.githubSvc, h.taskPullRequestRepo, h.githubPRFeedbackRepo, h.githubAuthRepo, h.threadInputRepo).ForwardAuthorizedFeedback(ctx, projectID, repo)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, forwarded := range result.Forwarded {
		if strings.TrimSpace(forwarded.TaskID) == "" || seen[forwarded.TaskID] {
			continue
		}
		seen[forwarded.TaskID] = true
		h.PromoteQueuedTaskThreadInput(forwarded.TaskID)
	}
	return githubToolJSON(map[string]any{"ok": true, "result": result})
}

func (h *Handler) resolveGitHubPRTaskForTool(ctx context.Context, params streamingResponseParams, taskID, title string) (*models.Task, *models.Project, error) {
	resolvedTaskID, err := h.resolveTaskIDForTool(ctx, params, taskID, title)
	if err != nil {
		return nil, nil, err
	}
	task, err := h.taskRepo.GetByID(ctx, resolvedTaskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil || task.ProjectID != params.ProjectID {
		return nil, nil, fmt.Errorf("task not found in current project")
	}
	project, err := h.projectRepo.GetByID(ctx, params.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	if project == nil {
		return nil, nil, fmt.Errorf("current project not found")
	}
	if err := requireAutomationGitHubRepo(ctx, params.ProjectID, project); err != nil {
		return nil, nil, err
	}
	return task, project, nil
}

func (h *Handler) executeGitHubOpenPullRequestTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.taskPullRequestRepo == nil {
		return "", fmt.Errorf("task pull request repository unavailable")
	}
	var req service.GitHubIssueActionRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	task, project, err := h.resolveGitHubPRTaskForTool(ctx, params, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}
	var issueNumber *int
	if req.IssueNumber > 0 {
		issueNumber = &req.IssueNumber
	}
	result, err := service.NewTaskPullRequestService(h.githubSvc, h.taskPullRequestRepo).OpenForTask(ctx, project, task, service.OpenTaskPullRequestOptions{
		Title:       req.PRTitle,
		Body:        req.PRBody,
		Base:        req.Base,
		Draft:       req.Draft,
		IssueNumber: issueNumber,
		IssueURL:    req.IssueURL,
	})
	if err != nil {
		return "", err
	}
	return githubToolJSON(map[string]any{"ok": true, "task_id": task.ID, "pull_request": result.PullRequest, "reused_existing_record": result.ReusedExistingRecord, "reused_remote": result.ReusedRemote, "created": result.Created})
}

func (h *Handler) executeGitHubReplacePullRequestBranchTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.taskPullRequestRepo == nil {
		return "", fmt.Errorf("task pull request repository unavailable")
	}
	var req service.GitHubIssueActionRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	if !req.ConfirmHistoryRewrite {
		return "", fmt.Errorf("confirm_history_rewrite must be true to replace pull request branch history")
	}
	task, project, err := h.resolveGitHubPRTaskForTool(ctx, params, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}
	record, err := service.NewTaskPullRequestService(h.githubSvc, h.taskPullRequestRepo).ReplaceBranchHeadForTask(ctx, project, task, req.ExpectedHeadSHA)
	if err != nil {
		return "", err
	}
	return githubToolJSON(map[string]any{
		"ok":                true,
		"task_id":           task.ID,
		"pull_request":      record,
		"replaced_branch":   task.WorktreeBranch,
		"expected_head_sha": strings.ToLower(strings.TrimSpace(req.ExpectedHeadSHA)),
	})
}

func githubToolJSON(payload map[string]any) (string, error) {
	b, err := json.Marshal(payload)
	return string(b), err
}

func (h *Handler) executeGetPersonality(ctx context.Context) string {
	if h.settingsRepo == nil {
		return "Current personality: default (no personality set)"
	}
	current, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		applog.Infof("[handler] executeGetPersonality error: %v", err)
		return "Error retrieving personality setting."
	}
	if current == "" {
		return "Current personality: default (base, no personality modifier active)"
	}
	return fmt.Sprintf("Current personality: %s", current)
}

func (h *Handler) executeGetModel(ctx context.Context, input json.RawMessage) string {
	var req struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_model."
	}

	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		applog.Infof("[handler] executeGetModel error: %v", err)
		return "Error retrieving model configurations."
	}

	for _, c := range configs {
		if (req.ModelID != "" && c.ID == req.ModelID) ||
			(req.Name != "" && strings.EqualFold(c.Name, req.Name)) {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			workerInfo := ""
			if c.MaxWorkers > 0 {
				workerInfo = fmt.Sprintf(", max_workers: %d", c.MaxWorkers)
			}
			return fmt.Sprintf("Model: %s%s\n  Provider: %s\n  Model ID: %s\n  Auth: %s%s",
				c.Name, defaultStr, c.Provider, c.Model, c.AuthMethod, workerInfo)
		}
	}

	if req.ModelID != "" {
		return fmt.Sprintf("Model with id %q not found.", req.ModelID)
	}
	return fmt.Sprintf("Model with name %q not found.", req.Name)
}

func (h *Handler) executeGetCurrentProject(ctx context.Context, projectID string) string {
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	desc := ""
	if project.Description != "" {
		desc = fmt.Sprintf("\nDescription: %s", project.Description)
	}
	repo := ""
	if project.RepoPath != "" {
		repo = fmt.Sprintf("\nRepository: %s", project.RepoPath)
	}
	return fmt.Sprintf("Current project: %s (id: %s)%s%s", project.Name, project.ID, desc, repo)
}

func (h *Handler) executeSwitchProject(ctx context.Context, currentProjectID string, input json.RawMessage) string {
	var req struct {
		Project string `json:"project"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for switch_project."
	}
	target := strings.TrimSpace(req.Project)
	if target == "" {
		return "switch_project requires a project name or ID."
	}

	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		return "Error loading projects: " + err.Error()
	}

	for _, p := range projects {
		if strings.EqualFold(p.Name, target) || p.ID == target {
			// For web/API, the project switch is informational — the frontend
			// manages the active project_id. Return the target so the model
			// can communicate the switch to the user.
			return fmt.Sprintf("Switched to project: %s (id: %s). Use this project for subsequent actions.", p.Name, p.ID)
		}
	}

	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return fmt.Sprintf("Project not found: %q. Available projects: %s", target, strings.Join(names, ", "))
}

func (h *Handler) executeGetAlert(ctx context.Context, projectID string, input json.RawMessage) string {
	var req struct {
		AlertID string `json:"alert_id"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_alert."
	}
	if req.AlertID == "" {
		return "get_alert requires alert_id."
	}

	if h.alertSvc == nil {
		return "Alert service not available."
	}

	alert, err := h.alertSvc.GetByID(ctx, projectID, req.AlertID)
	if err != nil {
		applog.Infof("[handler] executeGetAlert error: %v", err)
		return fmt.Sprintf("Error retrieving alert %q: %v", req.AlertID, err)
	}
	if alert == nil {
		return fmt.Sprintf("Alert %q not found.", req.AlertID)
	}

	readStr := "unread"
	if alert.IsRead {
		readStr = "read"
	}
	taskStr := ""
	if alert.TaskID != nil {
		taskStr = fmt.Sprintf("\nTask: %s", *alert.TaskID)
	}
	return fmt.Sprintf("Alert: %s\n  ID: %s\n  Type: %s\n  Severity: %s\n  Status: %s\n  Message: %s%s\n  Created: %s",
		alert.Title, alert.ID, alert.Type, alert.Severity, readStr,
		alert.Message, taskStr, alert.CreatedAt.Format("Jan 2, 2006 3:04 PM"))
}

func formatCapabilities(summaries []chatcontrol.ActionSummary, selectedMemoryHandles []string) string {
	if len(summaries) == 0 {
		return "No capabilities available in the current mode."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Available capabilities (%d actions):\n", len(summaries)))
	if len(selectedMemoryHandles) > 0 {
		sb.WriteString("\nSelected memories for this turn:\n")
		for _, handle := range selectedMemoryHandles {
			sb.WriteString(fmt.Sprintf("  - %s\n", handle))
		}
	}
	currentDomain := ""
	for _, s := range summaries {
		if s.Domain != currentDomain {
			currentDomain = s.Domain
			sb.WriteString(fmt.Sprintf("\n[%s]\n", currentDomain))
		}
		accessTag := ""
		if s.Access == "write" {
			accessTag = " (write)"
		}
		sb.WriteString(fmt.Sprintf("  - %s%s: %s\n", s.Name, accessTag, s.Description))
	}
	return sb.String()
}

// buildChatActionToolRuntimeFromDefs creates a RuntimeTools from pre-computed tool
// definitions and the shared executor. Used by processStreamingResponse which
// computes defs from the registry before calling this.
func (h *Handler) buildChatActionToolRuntimeFromDefs(params streamingResponseParams, collector *chatActionSummaryCollector, defs []llmcontracts.RuntimeToolDefinition, mode models.ChatMode, surface chatcontrol.Surface) *llmcontracts.RuntimeTools {
	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

func (h *Handler) buildLifecycleChatActionToolRuntimeFromDefs(params streamingResponseParams, collector *chatActionSummaryCollector, defs []llmcontracts.RuntimeToolDefinition, mode models.ChatMode, surface chatcontrol.Surface) *llmcontracts.RuntimeTools {
	handlers := h.chatActionHandlers(params, collector, mode, surface)
	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    chatcontrol.BuildLifecycleRuntimeToolExecutor(mode, surface, handlers),
	}
}

// chatActionToolDefinitions returns tool definitions from the canonical registry.
// Kept for backward compatibility with existing tests.
func chatActionToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	return chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
}

type sendToTaskToolInput struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Origin      string `json:"origin"`
	OriginAgent string `json:"origin_agent"`
}

func (h *Handler) normalizeScheduleToolTaskReference(ctx context.Context, params streamingResponseParams, taskID, title *string, defaultOmittedToCurrent bool) error {
	if taskID == nil || title == nil {
		return nil
	}
	trimmedTaskID := strings.TrimSpace(*taskID)
	trimmedTitle := strings.TrimSpace(*title)
	if trimmedTaskID != "current" {
		if trimmedTaskID != "" || trimmedTitle != "" || !defaultOmittedToCurrent || !params.IsTaskFollowup || params.TaskID == "" {
			return nil
		}
		trimmedTaskID = "current"
	}
	resolvedTaskID, err := h.resolveTaskIDForTool(ctx, params, trimmedTaskID, trimmedTitle)
	if err != nil {
		return err
	}
	*taskID = resolvedTaskID
	*title = ""
	return nil
}

func (h *Handler) resolveTaskIDForTool(ctx context.Context, params streamingResponseParams, taskID, title string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "current" {
		if !params.IsTaskFollowup || params.TaskID == "" {
			return "", fmt.Errorf("task_id current is only valid in a persisted task thread")
		}
		taskID = params.TaskID
	}
	if taskID != "" {
		task, err := h.resolveTaskReference(ctx, params.ProjectID, taskID, "")
		if err != nil {
			return "", err
		}
		return task.ID, nil
	}
	if strings.TrimSpace(title) != "" {
		task, err := h.resolveTaskReference(ctx, params.ProjectID, "", title)
		if err != nil {
			return "", err
		}
		return task.ID, nil
	}
	return "", fmt.Errorf("task_id is required")
}

func (h *Handler) taskGoalActionHandlers(params streamingResponseParams) map[string]chatcontrol.RuntimeActionHandler {
	return service.BuildTaskGoalRuntimeActionHandlers(service.TaskGoalRuntimeActionOptions{
		TaskGoalSvc: h.taskGoalSvc,
		ResolveTaskID: func(ctx context.Context, req service.TaskGoalRuntimeToolInput) (string, error) {
			return h.resolveTaskIDForTool(ctx, params, req.TaskID, req.Title)
		},
		AuthorizeStatusTool: func(ctx context.Context, toolName string) error {
			return requireGoalStatusToolGrant(ctx, params, toolName)
		},
	})
}

func requireGoalStatusToolGrant(ctx context.Context, params streamingResponseParams, toolName string) error {
	if hookAgent, ok := lifecycle.HookAgentFromContext(ctx); ok {
		if hookAgent.SystemKind == models.AgentSystemKindGoal {
			return nil
		}
		if hasToolGrant(hookAgent.Tools, toolName) {
			return nil
		}
		return fmt.Errorf("tool %s requires an explicit agent tool grant", toolName)
	}
	if hasToolGrant(agentTools(params.AgentDefinition), toolName) {
		return nil
	}
	return fmt.Errorf("tool %s requires an explicit agent tool grant", toolName)
}

func (h *Handler) executeSendToTaskTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	var req sendToTaskToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	taskIDInput := strings.TrimSpace(req.TaskID)
	if taskIDInput == "" && strings.TrimSpace(req.Title) == "" && params.IsTaskFollowup && params.TaskID != "" {
		taskIDInput = "current"
	}
	taskID, err := h.resolveTaskIDForTool(ctx, params, taskIDInput, req.Title)
	if err != nil {
		return "", err
	}
	origin, originAgent := sanitizeSendToTaskLineage(ctx, req.Origin, req.OriginAgent, params)
	if err := h.rejectStaleLifecycleSendToTask(ctx); err != nil {
		return "", err
	}
	if err := h.rejectCancelledLifecycleSendToTask(ctx, taskID); err != nil {
		return "", err
	}
	if origin == models.TaskOriginSystemAgent && originAgent == models.AgentSystemKindGoal && h.taskGoalSvc != nil {
		goal, goalErr := h.taskGoalSvc.GetEvaluableGoal(ctx, taskID)
		if goalErr != nil {
			return "", goalErr
		}
		if goal == nil {
			return "", fmt.Errorf("task goal is not active; continuation was not queued")
		}
	}
	queued, err := h.enqueueTaskThreadInput(ctx, taskID, req.Message, origin, originAgent, params.ChannelReply)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"ok": true, "task_id": taskID, "queued_message_id": queued.ID})
	return string(b), err
}

func isGoalLifecycleHookAgent(ctx context.Context) bool {
	agent, ok := lifecycle.HookAgentFromContext(ctx)
	return ok && agent.SystemKind == models.AgentSystemKindGoal
}

func (h *Handler) rejectCancelledLifecycleSendToTask(ctx context.Context, destinationTaskID string) error {
	agent, ok := lifecycle.HookAgentFromContext(ctx)
	if !ok || h.taskRepo == nil {
		return nil
	}
	for _, taskID := range []string{strings.TrimSpace(agent.TaskID), strings.TrimSpace(destinationTaskID)} {
		if taskID == "" {
			continue
		}
		if h.workerSvc != nil && h.workerSvc.IsCancellationRequested(taskID) {
			return fmt.Errorf("cancelled lifecycle task %s cannot enqueue continuation", taskID)
		}
		task, err := h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if task != nil && task.Status == models.StatusCancelled && !lifecycleHookMayContinueFromCancelledSource(agent, taskID) {
			return fmt.Errorf("cancelled lifecycle task %s cannot enqueue continuation", taskID)
		}
	}
	return nil
}

func lifecycleHookMayContinueFromCancelledSource(agent lifecycle.HookAgent, taskID string) bool {
	if strings.TrimSpace(agent.TaskID) == "" || strings.TrimSpace(agent.TaskID) != strings.TrimSpace(taskID) {
		return false
	}
	return lifecycleHookHasNonCancellationExecutionError(agent.ExecutionError)
}

func lifecycleHookHasNonCancellationExecutionError(executionError string) bool {
	msg := strings.ToLower(strings.TrimSpace(executionError))
	if msg == "" {
		return false
	}
	switch msg {
	case "task cancelled", "task canceled", "task cancelled by user", "task canceled by user", "context canceled", "context cancelled":
		return false
	default:
		return true
	}
}

func (h *Handler) rejectStaleLifecycleSendToTask(ctx context.Context) error {
	agent, ok := lifecycle.HookAgentFromContext(ctx)
	sourceTaskID := strings.TrimSpace(agent.TaskID)
	sourceRunID := strings.TrimSpace(agent.TaskRunID)
	if !ok || sourceTaskID == "" || sourceRunID == "" || h.lifecycleRepo == nil {
		return nil
	}
	detail, err := h.lifecycleRepo.TaskRunFreshness(ctx, sourceTaskID, sourceRunID)
	if err != nil {
		return err
	}
	if detail.Stale {
		applog.Infof("[handler] rejected lifecycle send_to_task as stale source_task=%s source_run=%s source_started_at=%s source_rowid=%d latest_run=%s latest_started_at=%s latest_rowid=%d",
			sourceTaskID, sourceRunID, detail.SourceStartedAt, detail.SourceRowID, detail.LatestRunID, detail.LatestStartedAt, detail.LatestRowID)
		return fmt.Errorf("stale lifecycle task run source_task=%s source_run=%s source_started_at=%s source_rowid=%d latest_run=%s latest_started_at=%s latest_rowid=%d; continuation was not queued",
			sourceTaskID, sourceRunID, detail.SourceStartedAt, detail.SourceRowID, detail.LatestRunID, detail.LatestStartedAt, detail.LatestRowID)
	}
	return nil
}

func agentTools(agentDef *models.Agent) []string {
	if agentDef == nil {
		return nil
	}
	return agentDef.Tools
}

func hasToolGrant(tools []string, toolName string) bool {
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool), toolName) {
			return true
		}
	}
	return false
}

func runtimeToolDefinitionsInclude(rt *llmcontracts.RuntimeTools, toolName string) bool {
	return rt != nil && rt.HasDefinition(toolName)
}

func sanitizeSendToTaskLineage(ctx context.Context, requestedOrigin, requestedOriginAgent string, params streamingResponseParams) (string, string) {
	if isGoalLifecycleHookAgent(ctx) {
		return models.TaskOriginSystemAgent, models.AgentSystemKindGoal
	}
	if strings.TrimSpace(params.RuntimeOrigin) != "" || strings.TrimSpace(params.RuntimeOriginAgent) != "" {
		origin := strings.TrimSpace(params.RuntimeOrigin)
		if origin == "" {
			origin = models.TaskOriginSystemAgent
		}
		return origin, strings.TrimSpace(params.RuntimeOriginAgent)
	}
	origin := strings.TrimSpace(requestedOrigin)
	if origin == "" {
		origin = strings.TrimSpace(params.ChannelReply.Source)
	}
	switch origin {
	case models.TaskOriginSlack, models.TaskOriginTelegram, models.TaskOriginEmail, models.TaskOriginDiscord:
		return origin, ""
	default:
		return models.TaskOriginWeb, ""
	}
}

func filterTaskThreadRuntimeToolDefs(defs []llmcontracts.RuntimeToolDefinition, agentDef *models.Agent, includeSelectedMemoryView bool) []llmcontracts.RuntimeToolDefinition {
	allowed := taskThreadAllowedRuntimeToolNames(agentDef)
	if includeSelectedMemoryView {
		allowed["memory_view"] = true
	}
	return filterRuntimeToolDefs(defs, allowed)
}

func filterTaskThreadCapabilitySummaries(summaries []chatcontrol.ActionSummary, agentDef *models.Agent, includeSelectedMemoryView bool) []chatcontrol.ActionSummary {
	allowed := taskThreadAllowedRuntimeToolNames(agentDef)
	if includeSelectedMemoryView {
		allowed["memory_view"] = true
	}
	out := make([]chatcontrol.ActionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if allowed[summary.Name] {
			out = append(out, summary)
		}
	}
	return out
}

func filterAssignedAgentRuntimeToolDefs(defs []llmcontracts.RuntimeToolDefinition, agentDef *models.Agent) []llmcontracts.RuntimeToolDefinition {
	out := make([]llmcontracts.RuntimeToolDefinition, 0, len(defs))
	for _, def := range defs {
		if assignedAgentToolDenied(def.Name, agentDef) {
			continue
		}
		out = append(out, def)
	}
	return out
}

func filterAssignedAgentCapabilitySummaries(summaries []chatcontrol.ActionSummary, agentDef *models.Agent) []chatcontrol.ActionSummary {
	out := make([]chatcontrol.ActionSummary, 0, len(summaries))
	for _, summary := range summaries {
		if assignedAgentToolDenied(summary.Name, agentDef) {
			continue
		}
		out = append(out, summary)
	}
	return out
}

func assignedAgentToolDenied(toolName string, agentDef *models.Agent) bool {
	return false
}

func taskThreadAllowedRuntimeToolNames(agentDef *models.Agent) map[string]bool {
	allowed := map[string]bool{
		"list_tasks":                             true,
		"view_task_thread":                       true,
		"send_to_task":                           true,
		"send_message":                           true,
		"create_task":                            true,
		"execute_tasks":                          true,
		"create_swarm_task":                      true,
		"list_schedules":                         true,
		"schedule_task":                          true,
		"delete_schedule":                        true,
		"modify_schedule":                        true,
		"create_alert":                           true,
		"create_notification":                    true,
		"list_alerts":                            true,
		"get_alert":                              true,
		"list_existing_automation_notifications": true,
		"claim_alert":                            true,
		"create_alert_implementation_task":       true,
		"link_alert_implementation_task":         true,
		"complete_alert_processing":              true,
		"fail_alert_processing":                  true,
		"release_alert_claim":                    true,
		"github_create_issue":                    true,
		"github_get_issue":                       true,
		"github_get_project_inbox":               true,
		"github_is_actor_authorized":             true,
		"github_list_my_assigned_issues":         true,
		"github_list_existing_automation_issues": true,
		"github_list_assigned_issues":            true,
		"github_list_assigned_issues_with_prs":   true,
		"github_comment_on_issue":                true,
		"github_add_issue_labels":                true,
		"github_close_issue":                     true,
		"github_open_pull_request":               true, "github_replace_pull_request_branch": true,
		"github_forward_pr_feedback_to_tasks": true,
		"set_task_goal":                       true,
		"clear_task_goal":                     true,
		"get_task_goal":                       true,
		"pause_task_goal":                     true,
		"resume_task_goal":                    true,
		"list_capabilities":                   true,
	}
	for _, tool := range explicitlyGrantedTaskThreadRuntimeTools(agentDef) {
		allowed[tool] = true
	}
	return allowed
}

func explicitlyGrantedTaskThreadRuntimeTools(agentDef *models.Agent) []string {
	if agentDef == nil || len(agentDef.Tools) == 0 {
		return nil
	}
	granted := []string{}
	for _, tool := range agentDef.Tools {
		switch strings.ToLower(strings.TrimSpace(tool)) {
		case "send_message":
			granted = append(granted, "send_message")
		}
	}
	granted = append(granted, explicitlyGrantedGoalStatusTools(agentDef)...)
	return granted
}

func explicitlyGrantedGoalStatusTools(agentDef *models.Agent) []string {
	if agentDef == nil || len(agentDef.Tools) == 0 {
		return nil
	}
	granted := []string{}
	for _, tool := range agentDef.Tools {
		switch strings.ToLower(strings.TrimSpace(tool)) {
		case "mark_task_goal_achieved":
			granted = append(granted, "mark_task_goal_achieved")
		case "report_task_goal_blocked":
			granted = append(granted, "report_task_goal_blocked")
		}
	}
	return granted
}

func filterGoalAgentRuntimeToolDefs(defs []llmcontracts.RuntimeToolDefinition) []llmcontracts.RuntimeToolDefinition {
	return filterRuntimeToolDefs(defs, map[string]bool{
		"view_task_thread":         true,
		"send_to_task":             true,
		"get_task_goal":            true,
		"mark_task_goal_achieved":  true,
		"report_task_goal_blocked": true,
	})
}

func filterRuntimeToolDefs(defs []llmcontracts.RuntimeToolDefinition, allowed map[string]bool) []llmcontracts.RuntimeToolDefinition {
	out := make([]llmcontracts.RuntimeToolDefinition, 0, len(defs))
	for _, def := range defs {
		if allowed[def.Name] {
			out = append(out, def)
		}
	}
	return out
}
