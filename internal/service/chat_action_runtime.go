package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func supportsRuntimeChatActionTools(agent models.LLMConfig) bool {
	switch agent.Provider {
	case models.ProviderOpenAI:
		return agent.IsOpenAIAPIKey() || agent.IsOpenAIOAuth()
	case models.ProviderOpenAICompatible:
		return agent.IsOpenAICompatibleAPIKey() || agent.IsOAuth()
	case models.ProviderAnthropic:
		return agent.IsAnthropicAPIKey() || agent.IsOAuth()
	default:
		return false
	}
}

// SupportsRuntimeChatActionTools reports whether the concrete transport for a
// model config can receive and execute request-scoped runtime tools. Virtual
// mixtures inherit this capability only from their configured aggregator.
func SupportsRuntimeChatActionTools(ctx context.Context, repo *repository.LLMConfigRepo, agent models.LLMConfig) bool {
	if agent.Provider != models.ProviderMixture {
		return supportsRuntimeChatActionTools(agent)
	}
	if repo == nil {
		return false
	}
	cfg, err := llmmixture.ParseConfig(agent.MixtureConfigJSON)
	if err != nil {
		return false
	}
	aggregatorID := strings.TrimSpace(cfg.Aggregator.AgentConfigID)
	if aggregatorID == "" {
		return false
	}
	aggregator, err := repo.GetByID(ctx, aggregatorID)
	if err != nil || aggregator == nil || aggregator.Provider == models.ProviderMixture {
		return false
	}
	return supportsRuntimeChatActionTools(*aggregator)
}

type channelActionSummaryCollector struct {
	createdLines []string
	editedLines  []string
}

// llmServiceForAutomation is the subset of LLMService used by automation task
// creation callbacks so that the shared builder can be tested and called without
// a concrete *LLMService dependency.
type llmServiceForAutomation interface {
	prepareAutomationTaskCreation(ctx context.Context, projectID string, request *TaskCreationRequest) error
	createPreparedAutomationTask(ctx context.Context, projectID string, request TaskCreationRequest, agents []models.LLMConfig) ([]models.Task, string, bool, error)
}

// buildAutomationTaskCreationCallbacks returns the PrepareTaskCreation and
// CreatePreparedTask callbacks shared by Slack, Discord, and Telegram channel
// action handlers. When callerTaskID is empty or llmSvc is nil the callbacks
// are no-ops, preserving the short-circuit behaviour required for direct Chat
// sessions.
func buildAutomationTaskCreationCallbacks(callerTaskID, projectID string, llmSvc llmServiceForAutomation) (
	func(context.Context, *TaskCreationRequest) error,
	func(context.Context, TaskCreationRequest, []models.LLMConfig) ([]models.Task, string, bool, error),
) {
	prepare := func(ctx context.Context, request *TaskCreationRequest) error {
		if callerTaskID == "" || llmSvc == nil {
			return nil
		}
		return llmSvc.prepareAutomationTaskCreation(ctx, projectID, request)
	}
	create := func(ctx context.Context, request TaskCreationRequest, agents []models.LLMConfig) ([]models.Task, string, bool, error) {
		if callerTaskID == "" || llmSvc == nil {
			return nil, "", false, nil
		}
		return llmSvc.createPreparedAutomationTask(ctx, projectID, request, agents)
	}
	return prepare, create
}

type channelTaskActionHandlerOptions struct {
	ProjectID           string
	TaskSvc             *TaskService
	SwarmSvc            *SwarmService
	LLMConfigRepo       *repository.LLMConfigRepo
	Collector           *channelActionSummaryCollector
	PrepareTaskCreation func(context.Context, *TaskCreationRequest) error
	CreatePreparedTask  func(context.Context, TaskCreationRequest, []models.LLMConfig) ([]models.Task, string, bool, error)
	OnTasksCreated      func(context.Context, []TaskCreationRequest, []models.Task) error
}

// channelCreateSwarmTaskInput mirrors the canonical create_swarm_task runtime
// tool schema for channel surfaces.
type channelCreateSwarmTaskInput struct {
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

type channelGoalActionHandlerOptions struct {
	ProjectID   string
	TaskRepo    *repository.TaskRepo
	TaskGoalSvc *TaskGoalService
}

type channelThreadActionHandlerOptions struct {
	Platform                 string
	ProjectID                string
	Surface                  chatcontrol.Surface
	Source                   string
	ActorID                  string
	TaskRepo                 *repository.TaskRepo
	ExecRepo                 *repository.ExecutionRepo
	ThreadInputRepo          *repository.ThreadInputRepo
	LLMConfigRepo            *repository.LLMConfigRepo
	SettingsRepo             *repository.SettingsRepo
	CustomPersonalityRepo    *repository.CustomPersonalityRepo
	ChannelTaskRunner        ChannelTaskRunner
	QueuedTaskThreadPromoter func(taskID string)
	CompleteExecution        func(context.Context, string, string, string, string, int, int64)
	ChannelMessageRouter     *ChannelMessageRouter
	ReplyContext             ChannelReplyContext
	NewQueuedInput           func(*models.Task, string, string) *models.ThreadInput
	FilterHistory            func([]models.Execution, string) []models.Execution
	ResultAdapter            func(string) (string, error)
	ConfigureSendOptions     func(*channelTaskThreadSendOptions)
}

type channelContextModeActionHandlerOptions struct {
	ChannelDisplayName string
	ProjectID          string
	ProjectRepo        *repository.ProjectRepo
}

type channelProjectActionHandlerOptions struct {
	ProjectID   string
	ProjectRepo *repository.ProjectRepo
	// SwitchProject persists the active project selection for the channel identity.
	// It must verify authorization before writing and return an error if
	// persistence or authorization fails. A nil SwitchProject means no persistence
	// is attempted (informational only).
	SwitchProject func(context.Context, *models.Project) error
}

type AlertRuntimeOptions struct {
	ProjectID                 string
	CallerTaskID              string
	Source                    string
	AlertSvc                  *AlertService
	TaskRepo                  *repository.TaskRepo
	PrepareImplementationTask func(context.Context, *models.AlertImplementationTaskInput) error
}

type channelUtilityActionHandlerOptions struct {
	ProjectID                 string
	CallerTaskID              string
	TaskRepo                  *repository.TaskRepo
	ScheduleRepo              *repository.ScheduleRepo
	AutomationGraphSvc        *AutomationGraphService
	WorkerSvc                 *WorkerService
	LLMConfigRepo             *repository.LLMConfigRepo
	AgentRepo                 *repository.AgentRepo
	SettingsRepo              *repository.SettingsRepo
	CustomPersonalityRepo     *repository.CustomPersonalityRepo
	ProjectRepo               *repository.ProjectRepo
	AlertSvc                  *AlertService
	PrepareImplementationTask func(context.Context, *models.AlertImplementationTaskInput) error
	UnavailableAgentsText     string
}

type channelListAutomationsInput struct {
	ProjectID string `json:"project_id"`
}

type channelGetAutomationInput struct {
	AutomationID string `json:"automation_id"`
	ProjectID    string `json:"project_id"`
}

func workerFromTaskService(taskSvc *TaskService) *WorkerService {
	if taskSvc == nil {
		return nil
	}
	return taskSvc.workerSvc
}

func buildChannelTaskActionHandlers(opts channelTaskActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	return map[string]chatcontrol.RuntimeActionHandler{
		"create_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			if opts.TaskSvc == nil {
				return "", fmt.Errorf("create_task: task service unavailable")
			}
			var req TaskCreationRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if opts.PrepareTaskCreation != nil {
				if err := opts.PrepareTaskCreation(ctx, &req); err != nil {
					return "", err
				}
			}
			if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
				return "", fmt.Errorf("create_task requires title and prompt")
			}
			if req.Priority == 0 {
				req.Priority = 2
			}
			agents := []models.LLMConfig{}
			if opts.LLMConfigRepo != nil {
				agents, _ = opts.LLMConfigRepo.List(ctx)
			}
			createdTasks := []models.Task(nil)
			summary := ""
			creationHandled := false
			if opts.CreatePreparedTask != nil {
				var err error
				createdTasks, summary, creationHandled, err = opts.CreatePreparedTask(ctx, req, agents)
				if err != nil {
					return "", err
				}
			}
			if !creationHandled {
				createdTasks, summary = ExecuteTaskCreationsWithReturn(ctx, []TaskCreationRequest{req}, opts.ProjectID, opts.TaskSvc, agents)
			}
			if !creationHandled && opts.OnTasksCreated != nil && len(createdTasks) > 0 {
				if err := opts.OnTasksCreated(ctx, []TaskCreationRequest{req}, createdTasks); err != nil {
					return "", err
				}
			}
			if opts.Collector != nil {
				opts.Collector.addCreated(summary)
			}
			return strings.TrimSpace(summary), nil
		},
		"create_swarm_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req channelCreateSwarmTaskInput
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
				return "", fmt.Errorf("create_swarm_task requires title and prompt")
			}
			projectID := strings.TrimSpace(opts.ProjectID)
			if projectID == "" {
				return "", fmt.Errorf("create_swarm_task requires project_id")
			}
			swarmSvc := opts.SwarmSvc
			if swarmSvc == nil && opts.TaskSvc != nil {
				swarmSvc = opts.TaskSvc.swarmSvc
			}
			if swarmSvc == nil {
				return "", fmt.Errorf("create_swarm_task: swarm service unavailable")
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
				resolved, err := resolveTaskCreationAgentDefinition(ctx, TaskCreationRequest{AgentDefinitionID: req.AgentDefinitionID, Agent: req.Agent}, projectID, opts.TaskSvc)
				if err != nil {
					return "", fmt.Errorf("create_swarm_task: %w", err)
				}
				if resolved != "" {
					agentDefinitionID = &resolved
				}
			}
			parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{
				ProjectID:         projectID,
				Title:             req.Title,
				Prompt:            req.Prompt,
				Goal:              req.Goal,
				Category:          category,
				Priority:          priority,
				AgentID:           agentID,
				AgentDefinitionID: agentDefinitionID,
				Tag:               tag,
				MaxWorkers:        req.MaxWorkers,
				WorkerIsolation:   req.WorkerIsolation,
				ReviewerEnabled:   reviewerEnabled,
				MergerEnabled:     mergerEnabled,
				StartImmediately:  req.StartImmediately,
				MergeTargetBranch: req.MergeTargetBranch,
			})
			if err != nil {
				return "", err
			}
			if opts.OnTasksCreated != nil {
				if err := opts.OnTasksCreated(ctx, nil, []models.Task{*parent}); err != nil {
					return "", err
				}
			}
			plannerMessage := "Planner starts when the swarm parent is Active."
			summary := fmt.Sprintf("Created swarm task: %s.\n%s\n- \"%s\" (%s) [TASK_ID:%s]", parent.Title, plannerMessage, parent.Title, parent.Category, parent.ID)
			if opts.Collector != nil {
				opts.Collector.addCreated(summary)
			}
			return summary, nil
		},
		"edit_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req TaskEditRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.ID) == "" {
				return "", fmt.Errorf("edit_task requires id")
			}
			summary := ExecuteTaskEdits(ctx, []TaskEditRequest{req}, opts.ProjectID, opts.TaskSvc, nil, "")
			if opts.Collector != nil {
				opts.Collector.addEdited(summary)
			}
			return strings.TrimSpace(summary), nil
		},
		"execute_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req TaskExecutionRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" && len(req.Tags) == 0 && req.MinPriority == 0 {
				return "", fmt.Errorf("execute_tasks requires task_id/title or tags/min_priority")
			}
			return strings.TrimSpace(ExecuteTaskExecutions(ctx, []TaskExecutionRequest{req}, opts.ProjectID, opts.TaskSvc)), nil
		},
	}
}

func buildChannelGoalActionHandlers(opts channelGoalActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	return BuildTaskGoalRuntimeActionHandlers(TaskGoalRuntimeActionOptions{
		TaskGoalSvc: opts.TaskGoalSvc,
		ResolveTaskID: func(ctx context.Context, req TaskGoalRuntimeToolInput) (string, error) {
			task, err := resolveChannelTaskReference(ctx, opts.TaskRepo, opts.ProjectID, req.TaskID, req.Title)
			if err != nil {
				return "", err
			}
			return task.ID, nil
		},
	})
}

func buildChannelThreadActionHandlers(opts channelThreadActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	handlers := map[string]chatcontrol.RuntimeActionHandler{
		"view_task_thread": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req ViewThreadRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return runChannelViewTaskThread(ctx, opts.TaskRepo, opts.ExecRepo, opts.ProjectID, req)
		},
		"send_to_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req SendToTaskRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			result := runChannelSendToTaskAction(ctx, opts, req)
			if opts.ResultAdapter != nil {
				return opts.ResultAdapter(result)
			}
			return result, nil
		},
		"send_message": func(ctx context.Context, input json.RawMessage) (string, error) {
			if opts.ChannelMessageRouter == nil {
				return "", fmt.Errorf("channel message router unavailable")
			}
			return ExecuteSendMessageTool(ctx, opts.ChannelMessageRouter.WithAuditContext(string(opts.Surface), opts.ActorID), opts.ProjectID, input)
		},
		"list_capabilities": func(_ context.Context, _ json.RawMessage) (string, error) {
			return formatChannelCapabilities(chatcontrol.ListForContext(models.ChatModeOrchestrate, opts.Surface)), nil
		},
	}
	return handlers
}

func runChannelViewTaskThread(ctx context.Context, taskRepo *repository.TaskRepo, execRepo *repository.ExecutionRepo, projectID string, req ViewThreadRequest) (string, error) {
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "", fmt.Errorf("view_task_thread requires task_id or title")
	}
	task, err := resolveChannelTaskReference(ctx, taskRepo, projectID, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}
	if execRepo == nil {
		return "", fmt.Errorf("execution repository not configured")
	}
	executions, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return "", fmt.Errorf("retrieving thread for %q: %w", task.Title, err)
	}
	return strings.TrimSpace(formatThreadTranscript(task, executions, req.Offset, req.Limit)), nil
}

func runChannelSendToTaskAction(ctx context.Context, opts channelThreadActionHandlerOptions, req SendToTaskRequest) string {
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "send_to_task requires task_id or title."
	}
	task, err := resolveChannelTaskReference(ctx, opts.TaskRepo, opts.ProjectID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Error resolving task: %v", err)
	}
	sendOpts := channelTaskThreadSendOptions{
		Platform:                 opts.Platform,
		ProjectID:                opts.ProjectID,
		Message:                  req.Message,
		Source:                   opts.Source,
		Surface:                  opts.Surface,
		ReplyContext:             opts.ReplyContext,
		TaskRepo:                 opts.TaskRepo,
		ExecRepo:                 opts.ExecRepo,
		ThreadInputRepo:          opts.ThreadInputRepo,
		LLMConfigRepo:            opts.LLMConfigRepo,
		SettingsRepo:             opts.SettingsRepo,
		CustomPersonalityRepo:    opts.CustomPersonalityRepo,
		ChannelTaskRunner:        opts.ChannelTaskRunner,
		QueuedTaskThreadPromoter: opts.QueuedTaskThreadPromoter,
		CompleteExecution:        opts.CompleteExecution,
		NewQueuedInput:           opts.NewQueuedInput,
		FilterHistory:            opts.FilterHistory,
	}
	if opts.ConfigureSendOptions != nil {
		opts.ConfigureSendOptions(&sendOpts)
	}
	return runChannelTaskThreadSend(ctx, task, sendOpts)
}

func buildChannelContextModeActionHandlers(opts channelContextModeActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	return map[string]chatcontrol.RuntimeActionHandler{
		"get_current_project": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelCurrentProjectResult(ctx, opts.ProjectRepo, opts.ProjectID), nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Current chat mode: orchestrate", nil
		},
		"set_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return fmt.Sprintf("Chat mode changes are not supported on %s. %s always uses orchestrate mode.", opts.ChannelDisplayName, opts.ChannelDisplayName), nil
		},
	}
}

func buildChannelProjectActionHandlers(opts channelProjectActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	return map[string]chatcontrol.RuntimeActionHandler{
		"list_projects": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return buildChannelProjectListResult(ctx, opts.ProjectRepo, opts.ProjectID), nil
		},
		"switch_project": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req SwitchProjectRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return switchChannelProjectResult(ctx, opts.ProjectRepo, strings.TrimSpace(req.Project), opts.SwitchProject)
		},
	}
}

func buildChannelUtilityActionHandlers(opts channelUtilityActionHandlerOptions) map[string]chatcontrol.RuntimeActionHandler {
	handlers := map[string]chatcontrol.RuntimeActionHandler{
		"list_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			return ExecuteListTasksTool(ctx, opts.TaskRepo, opts.ProjectID, input)
		},
		"list_automations": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelListAutomationsResult(ctx, opts.AutomationGraphSvc, opts.ProjectID, input)
		},
		"get_automation": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelGetAutomationResult(ctx, opts.AutomationGraphSvc, opts.ProjectID, input)
		},
		"schedule_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return runChannelScheduleTask(ctx, opts, input), nil
		},
		"delete_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return runChannelDeleteSchedule(ctx, opts, input), nil
		},
		"modify_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return runChannelModifySchedule(ctx, opts, input), nil
		},
		"list_schedules": func(ctx context.Context, input json.RawMessage) (string, error) {
			return ExecuteListSchedulesTool(ctx, opts.ScheduleRepo, opts.ProjectID, input)
		},
		"list_personalities": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelListPersonalitiesResult(ctx, opts.SettingsRepo, opts.CustomPersonalityRepo), nil
		},
		"set_personality": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelSetPersonalityResult(ctx, opts.SettingsRepo, opts.CustomPersonalityRepo, input), nil
		},
		"get_personality": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelGetPersonalityResult(ctx, opts.SettingsRepo), nil
		},
		"list_models": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelListModelsResult(ctx, opts.LLMConfigRepo), nil
		},
		"get_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelGetModelResult(ctx, opts.LLMConfigRepo, input), nil
		},
		"set_default_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return ExecuteSetDefaultModelTool(ctx, opts.LLMConfigRepo, input)
		},
		"list_agents": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelListAgentsResult(ctx, opts.AgentRepo, opts.UnavailableAgentsText), nil
		},
		"view_settings": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelViewSettingsResult(ctx, opts.SettingsRepo, opts.LLMConfigRepo, opts.ProjectRepo), nil
		},
		"project_info": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelProjectInfoResult(ctx, opts.ProjectRepo, opts.TaskRepo, opts.ProjectID), nil
		},
		"list_alerts": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return channelListAlertsResult(ctx, opts.AlertSvc, opts.ProjectID), nil
		},
		"get_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelGetAlertResult(ctx, opts.AlertSvc, opts.ProjectID, input), nil
		},
		"create_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelCreateAlertResult(ctx, opts.AlertSvc, opts.ProjectID, input), nil
		},
		"delete_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelDeleteAlertResult(ctx, opts.AlertSvc, opts.ProjectID, input), nil
		},
		"toggle_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return channelToggleAlertResult(ctx, opts.AlertSvc, opts.ProjectID, input), nil
		},
	}
	mergeChannelRuntimeActionHandlers(handlers, BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{
		ProjectID: opts.ProjectID, CallerTaskID: opts.CallerTaskID, Source: "agent", AlertSvc: opts.AlertSvc, TaskRepo: opts.TaskRepo,
		PrepareImplementationTask: opts.PrepareImplementationTask,
	}))
	return handlers
}

func channelListAutomationsResult(ctx context.Context, graphSvc *AutomationGraphService, currentProjectID string, input json.RawMessage) (string, error) {
	var req channelListAutomationsInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	projectID, err := resolveChannelAutomationProjectID(currentProjectID, req.ProjectID, "list_automations")
	if err != nil {
		return "", err
	}
	if graphSvc == nil {
		return marshalChannelAutomationResult(map[string]any{"automations": []any{}})
	}
	cards, err := graphSvc.List(ctx, projectID)
	if err != nil {
		return "", err
	}
	summaries := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		summaries = append(summaries, channelAutomationCardSummary(card))
	}
	return marshalChannelAutomationResult(map[string]any{"automations": summaries})
}

func channelGetAutomationResult(ctx context.Context, graphSvc *AutomationGraphService, currentProjectID string, input json.RawMessage) (string, error) {
	var req channelGetAutomationInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	automationID := strings.TrimSpace(req.AutomationID)
	if automationID == "" {
		return "", fmt.Errorf("get_automation: automation_id is required")
	}
	projectID, err := resolveChannelAutomationProjectID(currentProjectID, req.ProjectID, "get_automation")
	if err != nil {
		return "", err
	}
	if graphSvc == nil {
		return "", fmt.Errorf("automations unavailable")
	}
	cards, err := graphSvc.List(ctx, projectID)
	if err != nil {
		return "", err
	}
	for _, card := range cards {
		if card.Automation.ID == automationID {
			return marshalChannelAutomationResult(map[string]any{"automation": channelAutomationCardSummary(card)})
		}
	}
	return marshalChannelAutomationResult(map[string]any{"error": fmt.Sprintf("automation %q not found in project %s", automationID, projectID), "found": false})
}

func resolveChannelAutomationProjectID(currentProjectID, requestedProjectID, toolName string) (string, error) {
	currentProjectID = strings.TrimSpace(currentProjectID)
	requestedProjectID = strings.TrimSpace(requestedProjectID)
	if currentProjectID == "" {
		return "", fmt.Errorf("%s: no current project", toolName)
	}
	if requestedProjectID != "" && requestedProjectID != currentProjectID {
		return "", fmt.Errorf("project_id %q is outside the caller's authorized project context", requestedProjectID)
	}
	return currentProjectID, nil
}

func channelAutomationCardSummary(card models.AutomationCard) map[string]any {
	paused := card.Automation.LifecycleState == models.AutomationPaused
	summary := map[string]any{
		"id":          card.Automation.ID,
		"name":        card.Automation.Name,
		"status":      string(card.Automation.LifecycleState),
		"paused":      paused,
		"adapter_key": card.Version.AdapterKey,
		"node_count": card.Counts.Running + card.Counts.Waiting +
			card.Counts.Blocked + card.Counts.Failed + card.Counts.CompletedRecently,
		"counts": map[string]int{
			"running":            card.Counts.Running,
			"waiting":            card.Counts.Waiting,
			"blocked":            card.Counts.Blocked,
			"failed":             card.Counts.Failed,
			"completed_recently": card.Counts.CompletedRecently,
		},
	}
	if card.NextRun != nil {
		summary["next_run"] = card.NextRun.UTC().Format("2006-01-02T15:04:05Z")
	}
	if card.LastRun != nil {
		summary["last_run"] = card.LastRun.UTC().Format("2006-01-02T15:04:05Z")
	}
	return summary
}

func marshalChannelAutomationResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func buildChannelProjectListResult(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	selection, err := selectChannelProject(ctx, projectRepo, projectID, "", nil)
	if err != nil {
		return "Error retrieving projects: " + err.Error()
	}
	var sb strings.Builder
	sb.WriteString("Available Projects:\n")
	if len(selection.Projects) == 0 {
		sb.WriteString("No projects found.")
		return sb.String()
	}
	for _, p := range selection.Projects {
		marker := ""
		if p.ID == projectID {
			marker = " <- current"
		}
		desc := ""
		if p.Description != "" {
			desc = " - " + p.Description
		}
		sb.WriteString(fmt.Sprintf("- %s%s%s\n", p.Name, desc, marker))
	}
	sb.WriteString("Ask me to switch projects by name when needed.")
	return strings.TrimSpace(sb.String())
}

type channelProjectSelection struct {
	Projects       []models.Project
	Current        *models.Project
	Target         *models.Project
	TargetName     string
	AvailableNames []string
}

func selectChannelProject(ctx context.Context, projectRepo *repository.ProjectRepo, currentProjectID, targetProject string, switchProject func(context.Context, *models.Project) error) (channelProjectSelection, error) {
	var selection channelProjectSelection
	if projectRepo == nil {
		return selection, fmt.Errorf("project repository not configured")
	}
	projects, err := projectRepo.List(ctx)
	if err != nil {
		return selection, err
	}
	selection.Projects = projects
	selection.TargetName = strings.TrimSpace(targetProject)
	selection.AvailableNames = channelProjectAvailableNames(projects)
	for i := range projects {
		if projects[i].ID == currentProjectID {
			selection.Current = &projects[i]
		}
		if selection.TargetName != "" && matchesChannelProjectTarget(projects[i], selection.TargetName) {
			selection.Target = &projects[i]
		}
	}
	if selection.Target != nil && switchProject != nil {
		if err := switchProject(ctx, selection.Target); err != nil {
			return selection, err
		}
	}
	return selection, nil
}

func matchesChannelProjectTarget(project models.Project, target string) bool {
	return strings.EqualFold(project.Name, target) || project.ID == target
}

func channelProjectAvailableNames(projects []models.Project) []string {
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names
}

func switchChannelProjectResult(ctx context.Context, projectRepo *repository.ProjectRepo, targetProject string, switchProject func(context.Context, *models.Project) error) (string, error) {
	if targetProject == "" {
		return "Project switch requires a project name or ID.", nil
	}
	selection, err := selectChannelProject(ctx, projectRepo, "", targetProject, switchProject)
	if err != nil {
		if selection.Target != nil {
			return "", fmt.Errorf("failed to switch project: %w", err)
		}
		return "Error loading projects: " + err.Error(), nil
	}
	if selection.Target == nil {
		return fmt.Sprintf("Project not found: %q. Available projects: %s", selection.TargetName, strings.Join(selection.AvailableNames, ", ")), nil
	}
	return fmt.Sprintf("Switched to project: %s. Future messages from this channel identity will use that project.", selection.Target.Name), nil
}

func runChannelScheduleTask(ctx context.Context, opts channelUtilityActionHandlerOptions, input json.RawMessage) string {
	var req ScheduleTaskRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for schedule_task."
	}
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "schedule_task requires task_id or title."
	}
	if strings.TrimSpace(req.Time) == "" {
		return "schedule_task requires time."
	}
	if opts.ScheduleRepo == nil {
		return "Error scheduling task: schedule repository not available."
	}
	result, err := NewScheduleActionService(opts.TaskRepo, opts.ScheduleRepo, opts.WorkerSvc).Create(ctx, opts.ProjectID, req)
	if err != nil {
		actionErr, _ := err.(*ScheduleActionError)
		switch {
		case actionErr != nil && actionErr.Kind == ScheduleActionReferenceError:
			return fmt.Sprintf("Could not find task: %v", err)
		case actionErr != nil && actionErr.Kind == ScheduleActionTimeError:
			return fmt.Sprintf("Invalid time %q (expected HH:MM).", req.Time)
		case actionErr != nil && actionErr.Kind == ScheduleActionRepeatError:
			return fmt.Sprintf("Unknown repeat type %q.", req.Repeat)
		case actionErr != nil && actionErr.Kind == ScheduleActionDaysError:
			return fmt.Sprintf("Invalid weekly days: %v.", err)
		case actionErr != nil && actionErr.Kind == ScheduleActionIntervalError:
			return fmt.Sprintf("Invalid interval %d: %v.", req.Interval, err)
		default:
			title := req.Title
			if result != nil && result.Task != nil {
				title = result.Task.Title
			}
			return fmt.Sprintf("Error scheduling task %q: %v", title, err)
		}
	}
	return fmt.Sprintf("Scheduled task %q [TASK_ID:%s] at %s (%s).", result.Task.Title, result.Task.ID, req.Time, channelScheduleRepeatDescription(result.Schedule.RepeatType, result.Schedule.RepeatInterval, req.Days))
}

func runChannelDeleteSchedule(ctx context.Context, opts channelUtilityActionHandlerOptions, input json.RawMessage) string {
	var req DeleteScheduleRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for delete_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "delete_schedule requires schedule_id, task_id, or title."
	}
	if opts.ScheduleRepo == nil {
		return "Error deleting schedule: schedule repository not available."
	}
	result, err := NewScheduleActionService(opts.TaskRepo, opts.ScheduleRepo, opts.WorkerSvc).Delete(ctx, opts.ProjectID, req)
	if err != nil {
		actionErr, _ := err.(*ScheduleActionError)
		if actionErr != nil && actionErr.Kind == ScheduleActionReferenceError {
			return fmt.Sprintf("Could not find schedule: %v", err)
		}
		title := req.Title
		if result != nil && result.Task != nil {
			title = result.Task.Title
		}
		return fmt.Sprintf("Error deleting schedule for task %q: %v", title, err)
	}
	return fmt.Sprintf("Deleted schedule for task %q [TASK_ID:%s].", result.Task.Title, result.Task.ID)
}

func runChannelModifySchedule(ctx context.Context, opts channelUtilityActionHandlerOptions, input json.RawMessage) string {
	var req ModifyScheduleRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for modify_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "modify_schedule requires schedule_id, task_id, or title."
	}
	if opts.ScheduleRepo == nil {
		return "Error modifying schedule: schedule repository not available."
	}
	result, err := NewScheduleActionService(opts.TaskRepo, opts.ScheduleRepo, opts.WorkerSvc).Modify(ctx, opts.ProjectID, req)
	if err != nil {
		actionErr, _ := err.(*ScheduleActionError)
		title := req.Title
		if result != nil && result.Task != nil {
			title = result.Task.Title
		}
		switch {
		case actionErr != nil && actionErr.Kind == ScheduleActionReferenceError:
			return fmt.Sprintf("Could not find schedule: %v", err)
		case actionErr != nil && actionErr.Kind == ScheduleActionTimeError:
			return fmt.Sprintf("Invalid time %q.", req.Time)
		case actionErr != nil && actionErr.Kind == ScheduleActionRepeatError:
			return fmt.Sprintf("Unknown repeat type %q.", req.Repeat)
		case actionErr != nil && actionErr.Kind == ScheduleActionDaysError:
			return fmt.Sprintf("Invalid weekly days: %v.", err)
		case actionErr != nil && actionErr.Kind == ScheduleActionIntervalError:
			return fmt.Sprintf("Invalid interval %d: %v.", *req.Interval, err)
		default:
			return fmt.Sprintf("Error updating schedule for task %q: %v", title, err)
		}
	}
	if len(result.Changes) == 0 {
		return fmt.Sprintf("No changes specified for schedule on task %q.", result.Task.Title)
	}
	return fmt.Sprintf("Updated schedule for task %q [TASK_ID:%s]: %s.", result.Task.Title, result.Task.ID, strings.Join(result.Changes, ", "))
}

func channelScheduleRepeatDescription(repeatType models.RepeatType, interval int, days []string) string {
	if repeatType == models.RepeatWeekly && len(days) > 0 {
		if interval > 1 {
			return fmt.Sprintf("every %d weeks on %s", interval, strings.Join(days, ", "))
		}
		return fmt.Sprintf("weekly on %s", strings.Join(days, ", "))
	}
	return FormatRepeatPattern(repeatType, interval)
}

func channelGetPersonalityResult(ctx context.Context, settingsRepo *repository.SettingsRepo) string {
	if settingsRepo == nil {
		return "Current personality: default (no personality set)"
	}
	current, err := settingsRepo.Get(ctx, "personality")
	if err != nil {
		return "Error retrieving personality setting."
	}
	if current == "" {
		return "Current personality: default (base, no personality modifier active)"
	}
	return fmt.Sprintf("Current personality: %s", current)
}

func channelListPersonalitiesResult(ctx context.Context, settingsRepo *repository.SettingsRepo, customRepo *repository.CustomPersonalityRepo) string {
	personalities := AllPersonalitiesWithCustom(ctx, customRepo)
	if len(personalities) == 0 {
		return "No personalities available."
	}
	var sb strings.Builder
	sb.WriteString("Available Personalities:\n")
	for _, p := range personalities {
		if p.Key == "" {
			sb.WriteString(fmt.Sprintf("- %s (default): %s\n", p.Name, p.Description))
		} else if p.IsCustom {
			sb.WriteString(fmt.Sprintf("- %s (key: %s, custom): %s\n", p.Name, p.Key, p.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s (key: %s): %s\n", p.Name, p.Key, p.Description))
		}
	}
	if settingsRepo != nil {
		if current, err := settingsRepo.Get(ctx, "personality"); err == nil {
			if current == "" {
				current = "default"
			}
			sb.WriteString(fmt.Sprintf("\nCurrent personality: %s", current))
		}
	}
	return strings.TrimSpace(sb.String())
}

func channelSetPersonalityResult(ctx context.Context, settingsRepo *repository.SettingsRepo, customRepo *repository.CustomPersonalityRepo, input json.RawMessage) string {
	var req struct {
		Personality string `json:"personality"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for set_personality."
	}
	key := strings.TrimSpace(req.Personality)
	if key == "" {
		return "set_personality requires personality."
	}
	valid := false
	for _, p := range AllPersonalitiesWithCustom(ctx, customRepo) {
		if p.Key == key {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Sprintf("Unknown personality %q. Use list_personalities to view options.", key)
	}
	if settingsRepo == nil {
		return "Error setting personality: settings repository not configured."
	}
	if err := settingsRepo.Set(ctx, "personality", key); err != nil {
		return fmt.Sprintf("Error setting personality to %q: %v", key, err)
	}
	return fmt.Sprintf("Personality changed to %q.", key)
}

func channelListModelsResult(ctx context.Context, repo *repository.LLMConfigRepo) string {
	configs, err := repo.List(ctx)
	if err != nil {
		return "Error retrieving model configurations."
	}
	if len(configs) == 0 {
		return "No models configured."
	}
	var sb strings.Builder
	sb.WriteString("Configured Models:\n")
	for _, c := range configs {
		defaultStr := ""
		if c.IsDefault {
			defaultStr = " (default)"
		}
		auth := string(c.AuthMethod)
		if auth == "" {
			auth = string(models.AuthMethodAPIKey)
		}
		sb.WriteString(fmt.Sprintf("- %s%s — provider: %s, model: %s, auth: %s\n", c.Name, defaultStr, c.Provider, c.Model, auth))
	}
	return strings.TrimSpace(sb.String())
}

func channelGetModelResult(ctx context.Context, repo *repository.LLMConfigRepo, input json.RawMessage) string {
	var req struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_model."
	}
	configs, err := repo.List(ctx)
	if err != nil {
		return "Error retrieving model configurations."
	}
	for _, c := range configs {
		if (req.ModelID != "" && c.ID == req.ModelID) || (req.Name != "" && strings.EqualFold(c.Name, req.Name)) {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			return fmt.Sprintf("Model: %s%s\n  Provider: %s\n  Model ID: %s\n  Auth: %s", c.Name, defaultStr, c.Provider, c.Model, c.AuthMethod)
		}
	}
	if req.ModelID != "" {
		return fmt.Sprintf("Model with id %q not found.", req.ModelID)
	}
	return fmt.Sprintf("Model with name %q not found.", req.Name)
}

type SetDefaultModelRequest struct {
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
}

// ExecuteSetDefaultModelTool sets the global default model using the same full
// config update path as the browser Models page so the repository preserves the
// single-default invariant.
func ExecuteSetDefaultModelTool(ctx context.Context, repo *repository.LLMConfigRepo, input json.RawMessage) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("model repository not available")
	}
	var req SetDefaultModelRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	modelID := strings.TrimSpace(req.ModelID)
	name := strings.TrimSpace(req.Name)
	if modelID == "" && name == "" {
		return "", fmt.Errorf("set_default_model requires model_id or name")
	}

	var target *models.LLMConfig
	if modelID != "" {
		model, err := repo.GetByID(ctx, modelID)
		if err != nil {
			return "", err
		}
		if model == nil {
			return "", fmt.Errorf("model with id %q not found", modelID)
		}
		target = model
	} else {
		configs, err := repo.List(ctx)
		if err != nil {
			return "", err
		}
		var matches []models.LLMConfig
		for _, c := range configs {
			if strings.EqualFold(strings.TrimSpace(c.Name), name) {
				matches = append(matches, c)
			}
		}
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("model with name %q not found", name)
		case 1:
			model, err := repo.GetByID(ctx, matches[0].ID)
			if err != nil {
				return "", err
			}
			if model == nil {
				return "", fmt.Errorf("model with name %q not found", name)
			}
			target = model
		default:
			return "", fmt.Errorf("model name %q is ambiguous; use model_id", name)
		}
	}

	if target.IsDefault {
		return fmt.Sprintf("Default model is already %q (id: %s).", target.Name, target.ID), nil
	}
	target.IsDefault = true
	if err := repo.Update(ctx, target); err != nil {
		return "", err
	}
	return fmt.Sprintf("Default model changed to %q (id: %s).", target.Name, target.ID), nil
}

func channelListAgentsResult(ctx context.Context, repo *repository.AgentRepo, unavailable string) string {
	if repo == nil {
		if strings.TrimSpace(unavailable) != "" {
			return unavailable
		}
		return "Agent definitions not available."
	}
	agents, err := repo.ListRuntimeSummaries(ctx)
	if err != nil {
		return "Error retrieving agent definitions: " + err.Error()
	}
	if len(agents) == 0 {
		return "No agents configured."
	}
	var sb strings.Builder
	sb.WriteString("Configured Agents:\n")
	for _, a := range agents {
		modelStr := ""
		if a.Model != "inherit" {
			modelStr = fmt.Sprintf(", model: %s", a.Model)
		}
		sb.WriteString(fmt.Sprintf("- %s — %s%s, %d skills, %d MCP servers\n", a.Name, a.Description, modelStr, a.SkillCount, a.MCPServerCount))
	}
	return strings.TrimSpace(sb.String())
}

func channelViewSettingsResult(ctx context.Context, settingsRepo *repository.SettingsRepo, llmRepo *repository.LLMConfigRepo, projectRepo *repository.ProjectRepo) string {
	var sb strings.Builder
	sb.WriteString("App Settings:\n")
	if settingsRepo != nil {
		personality, err := settingsRepo.Get(ctx, "personality")
		if err == nil {
			if personality == "" {
				personality = "default"
			}
			sb.WriteString(fmt.Sprintf("- Personality: %s\n", personality))
		}
	}
	if configs, err := llmRepo.List(ctx); err == nil {
		sb.WriteString(fmt.Sprintf("- Configured models: %d\n", len(configs)))
	}
	if projectRepo != nil {
		if projects, err := projectRepo.List(ctx); err == nil {
			sb.WriteString(fmt.Sprintf("- Projects: %d\n", len(projects)))
		}
	}
	return strings.TrimSpace(sb.String())
}

func channelCurrentProjectResult(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	return fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
}

func channelProjectInfoResult(ctx context.Context, projectRepo *repository.ProjectRepo, taskRepo *repository.TaskRepo, projectID string) string {
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return "Error retrieving project details."
	}
	counts, err := taskRepo.CountByProjectAndCategory(ctx, projectID)
	if err != nil {
		counts = map[string]int{}
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Project: %s (id: %s)\n", project.Name, project.ID))
	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", project.Description))
	}
	if project.RepoPath != "" {
		sb.WriteString(fmt.Sprintf("Repository: %s\n", project.RepoPath))
	}
	sb.WriteString(fmt.Sprintf("Total tasks: %d\n", total))
	for category, count := range counts {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", category, count))
	}
	return strings.TrimSpace(sb.String())
}

func BuildAlertRuntimeActionHandlers(opts AlertRuntimeOptions) map[string]chatcontrol.RuntimeActionHandler {
	resultJSON := func(value any) (string, error) {
		data, err := json.Marshal(value)
		return string(data), err
	}
	assertProject := func(requested string) error {
		requested = strings.TrimSpace(requested)
		if strings.TrimSpace(opts.ProjectID) == "" {
			return fmt.Errorf("notification project context is unavailable")
		}
		if requested != "" && requested != opts.ProjectID {
			return fmt.Errorf("project_id %q is outside the caller's authorized project context", requested)
		}
		return nil
	}
	requireCaller := func() error {
		if strings.TrimSpace(opts.CallerTaskID) == "" {
			return fmt.Errorf("this notification operation requires a persisted caller task")
		}
		return nil
	}
	requireService := func() error {
		if opts.AlertSvc == nil {
			return fmt.Errorf("alert service not available")
		}
		return nil
	}
	return map[string]chatcontrol.RuntimeActionHandler{
		"create_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				Title    string `json:"title"`
				Message  string `json:"message"`
				Severity string `json:"severity"`
				Type     string `json:"type"`
				TaskID   string `json:"task_id"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			req.Title = strings.TrimSpace(req.Title)
			if req.Title == "" {
				return "", fmt.Errorf("title is required")
			}
			severity, severityErr := channelAlertSeverity(req.Severity)
			if severityErr != "" {
				return "", fmt.Errorf("%s", severityErr)
			}
			alertType, typeErr := channelAlertType(req.Type)
			if typeErr != "" {
				return "", fmt.Errorf("%s", typeErr)
			}
			a := &models.Alert{ProjectID: opts.ProjectID, Scope: models.AlertScopeProject, Type: alertType,
				Severity: severity, Title: req.Title, Message: req.Message, Source: strings.TrimSpace(opts.Source)}
			if taskID := strings.TrimSpace(req.TaskID); taskID != "" {
				if opts.TaskRepo == nil {
					return "", fmt.Errorf("task repository not available")
				}
				task, err := opts.TaskRepo.GetByID(ctx, taskID)
				if err != nil || task == nil || task.ProjectID != opts.ProjectID {
					return "", fmt.Errorf("task_id %q is outside the caller's authorized project context", taskID)
				}
				a.TaskID = &taskID
			}
			if err := opts.AlertSvc.Create(ctx, a); err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"alert": a})
		},
		"create_notification": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID string         `json:"project_id"`
				Type      string         `json:"type"`
				Title     string         `json:"title"`
				Message   string         `json:"message"`
				Body      string         `json:"body"`
				Severity  string         `json:"severity"`
				Source    string         `json:"source"`
				Metadata  map[string]any `json:"metadata"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if opts.AlertSvc == nil {
				return "", fmt.Errorf("alert service not available")
			}
			req.Title = strings.TrimSpace(req.Title)
			req.Type = strings.TrimSpace(req.Type)
			req.Source = strings.TrimSpace(req.Source)
			if req.Title == "" || len(req.Title) > 200 {
				return "", fmt.Errorf("title is required and must be at most 200 characters")
			}
			if req.Type == "" || len(req.Type) > 100 {
				return "", fmt.Errorf("type is required and must be at most 100 characters")
			}
			if len(req.Message) > 2000 || len(req.Body) > 20000 {
				return "", fmt.Errorf("notification content exceeds the allowed size")
			}
			severity, severityErr := channelAlertSeverity(req.Severity)
			if severityErr != "" {
				return "", fmt.Errorf("%s", severityErr)
			}
			source := req.Source
			if source == "" {
				source = strings.TrimSpace(opts.Source)
			}
			if source == "" {
				source = "agent"
			}
			if len(source) > 100 {
				return "", fmt.Errorf("source must be at most 100 characters")
			}
			delete(req.Metadata, models.AlertAutomationProvenanceMetadataKey)
			a := &models.Alert{ProjectID: opts.ProjectID, Scope: models.AlertScopeProject, Type: models.AlertType(req.Type),
				Severity: severity, Title: req.Title, Message: req.Message, Body: req.Body, Source: source,
				Metadata: req.Metadata}
			if opts.CallerTaskID != "" {
				sourceTaskID := opts.CallerTaskID
				a.SourceTaskID = &sourceTaskID
			}
			created, err := opts.AlertSvc.CreateActionable(ctx, a)
			if err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"notification": created})
		},
		"list_existing_automation_notifications": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if req.Limit == 0 {
				req.Limit = 50
			}
			if req.Limit < 1 || req.Limit > 100 || req.Offset < 0 {
				return "", fmt.Errorf("limit must be 1-100 and offset must be non-negative")
			}
			notifications, err := opts.AlertSvc.ListExistingAutomationNotificationSummariesForRuntime(ctx, opts.ProjectID, models.AlertListFilter{Limit: req.Limit, Offset: req.Offset})
			if err != nil {
				return "", err
			}
			compact := make([]map[string]any, 0, len(notifications))
			for _, notification := range notifications {
				compact = append(compact, map[string]any{
					"id":                         notification.ID,
					"project_id":                 notification.ProjectID,
					"type":                       notification.Type,
					"severity":                   notification.Severity,
					"title":                      notification.Title,
					"source":                     notification.Source,
					"decision_state":             notification.DecisionState,
					"processing_state":           notification.ProcessingState,
					"implementation_task_id":     notification.ImplementationTaskID,
					"implementation_task_linked": notification.ImplementationTaskID != nil,
					"is_read":                    notification.IsRead,
					"created_at":                 notification.CreatedAt,
					"updated_at":                 notification.UpdatedAt,
				})
			}
			nextOffset := 0
			if len(notifications) == req.Limit {
				nextOffset = req.Offset + len(notifications)
			}
			return resultJSON(map[string]any{"notifications": compact, "project_id": opts.ProjectID, "offset": req.Offset, "next_offset": nextOffset})
		},
		"list_alerts": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID                string `json:"project_id"`
				DecisionState            string `json:"decision_state"`
				ProcessingState          string `json:"processing_state"`
				Type                     string `json:"type"`
				Source                   string `json:"source"`
				Read                     *bool  `json:"read"`
				ImplementationTaskLinked *bool  `json:"implementation_task_linked"`
				Limit                    int    `json:"limit"`
				Offset                   int    `json:"offset"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if req.Limit == 0 {
				req.Limit = 50
			}
			if req.Limit < 1 || req.Limit > 100 || req.Offset < 0 {
				return "", fmt.Errorf("limit must be 1-100 and offset must be non-negative")
			}
			if req.DecisionState != "" && req.DecisionState != string(models.AlertDecisionNotRequired) && req.DecisionState != string(models.AlertDecisionPending) && req.DecisionState != string(models.AlertDecisionApproved) && req.DecisionState != string(models.AlertDecisionRejected) && req.DecisionState != string(models.AlertDecisionDismissed) {
				return "", fmt.Errorf("invalid decision_state %q", req.DecisionState)
			}
			if req.ProcessingState != "" && req.ProcessingState != string(models.AlertProcessingNotApplicable) && req.ProcessingState != string(models.AlertProcessingUnclaimed) && req.ProcessingState != string(models.AlertProcessingClaimed) && req.ProcessingState != string(models.AlertProcessingImplementationTaskLinked) && req.ProcessingState != string(models.AlertProcessingCompleted) && req.ProcessingState != string(models.AlertProcessingFailed) {
				return "", fmt.Errorf("invalid processing_state %q", req.ProcessingState)
			}
			alerts, err := opts.AlertSvc.ListFilteredSummariesForRuntime(ctx, opts.ProjectID, models.AlertListFilter{
				DecisionState: models.AlertDecisionState(req.DecisionState), ProcessingState: models.AlertProcessingState(req.ProcessingState),
				Type: models.AlertType(req.Type), Source: req.Source, Read: req.Read,
				ImplementationTaskLinked: req.ImplementationTaskLinked, Limit: req.Limit, Offset: req.Offset,
			})
			if err != nil {
				return "", err
			}
			nextOffset := 0
			if len(alerts) == req.Limit {
				nextOffset = req.Offset + len(alerts)
			}
			return resultJSON(map[string]any{"notifications": alerts, "project_id": opts.ProjectID, "offset": req.Offset, "next_offset": nextOffset})
		},
		"get_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID string `json:"project_id"`
				AlertID   string `json:"alert_id"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.AlertID) == "" {
				return "", fmt.Errorf("alert_id is required")
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.RequireAutomationNotificationOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
				return "", err
			}
			a, err := opts.AlertSvc.GetByID(ctx, opts.ProjectID, req.AlertID)
			if err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"notification": a})
		},
		"claim_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID    string `json:"project_id"`
				AlertID      string `json:"alert_id"`
				LeaseSeconds *int   `json:"lease_seconds"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if req.LeaseSeconds != nil && (*req.LeaseSeconds < 1 || *req.LeaseSeconds > 86400) {
				return "", fmt.Errorf("lease_seconds must be between 1 and 86400")
			}
			if err := requireCaller(); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.RequireAutomationInboxOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
				return "", err
			}
			var lease time.Duration
			if req.LeaseSeconds != nil {
				lease = time.Duration(*req.LeaseSeconds) * time.Second
			}
			a, err := opts.AlertSvc.ClaimApproved(ctx, opts.ProjectID, req.AlertID, opts.CallerTaskID, lease)
			if err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"notification": a})
		},
		"create_alert_implementation_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID string         `json:"project_id"`
				AlertID   string         `json:"alert_id"`
				Title     string         `json:"title"`
				Prompt    string         `json:"prompt"`
				Goal      string         `json:"goal"`
				Priority  int            `json:"priority"`
				Tag       models.TaskTag `json:"tag"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if err := requireCaller(); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.RequireAutomationInboxOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
				return "", err
			}
			implementation := models.AlertImplementationTaskInput{
				Title: req.Title, Prompt: req.Prompt, Goal: req.Goal, Priority: req.Priority, Tag: req.Tag,
			}
			if opts.PrepareImplementationTask != nil {
				if err := opts.PrepareImplementationTask(ctx, &implementation); err != nil {
					return "", err
				}
			}
			if len(strings.TrimSpace(implementation.Goal)) > MaxTaskGoalLength {
				return "", ErrTaskGoalTooLong
			}
			task, err := opts.AlertSvc.CreateImplementationTask(ctx, opts.ProjectID, req.AlertID, opts.CallerTaskID, implementation)
			if err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"alert_id": req.AlertID, "implementation_task_id": task.ID, "task": task})
		},
		"link_alert_implementation_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID string `json:"project_id"`
				AlertID   string `json:"alert_id"`
				TaskID    string `json:"task_id"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if err := requireCaller(); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.RequireAutomationInboxOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.LinkImplementationTask(ctx, opts.ProjectID, req.AlertID, opts.CallerTaskID, req.TaskID); err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"alert_id": req.AlertID, "implementation_task_id": req.TaskID})
		},
		"complete_alert_processing": alertTerminalRuntimeHandler(opts, models.AlertProcessingCompleted, assertProject, requireCaller, resultJSON),
		"fail_alert_processing":     alertTerminalRuntimeHandler(opts, models.AlertProcessingFailed, assertProject, requireCaller, resultJSON),
		"release_alert_claim": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req struct {
				ProjectID string `json:"project_id"`
				AlertID   string `json:"alert_id"`
			}
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if err := assertProject(req.ProjectID); err != nil {
				return "", err
			}
			if err := requireCaller(); err != nil {
				return "", err
			}
			if err := requireService(); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.RequireAutomationInboxOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
				return "", err
			}
			if err := opts.AlertSvc.ReleaseClaim(ctx, opts.ProjectID, req.AlertID, opts.CallerTaskID); err != nil {
				return "", err
			}
			return resultJSON(map[string]any{"alert_id": req.AlertID, "processing_state": models.AlertProcessingUnclaimed})
		},
	}
}

func alertTerminalRuntimeHandler(opts AlertRuntimeOptions, state models.AlertProcessingState, assertProject func(string) error, requireCaller func() error, resultJSON func(any) (string, error)) chatcontrol.RuntimeActionHandler {
	return func(ctx context.Context, input json.RawMessage) (string, error) {
		var req struct {
			ProjectID string `json:"project_id"`
			AlertID   string `json:"alert_id"`
			Message   string `json:"message"`
		}
		if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
			return "", err
		}
		if err := assertProject(req.ProjectID); err != nil {
			return "", err
		}
		if err := requireCaller(); err != nil {
			return "", err
		}
		if len(req.Message) > 2000 {
			return "", fmt.Errorf("message must be at most 2000 characters")
		}
		if opts.AlertSvc == nil {
			return "", fmt.Errorf("alert service not available")
		}
		if err := opts.AlertSvc.RequireAutomationInboxOwnership(ctx, opts.ProjectID, req.AlertID); err != nil {
			return "", err
		}
		if err := opts.AlertSvc.MarkProcessing(ctx, opts.ProjectID, req.AlertID, opts.CallerTaskID, state, req.Message); err != nil {
			return "", err
		}
		return resultJSON(map[string]any{"alert_id": req.AlertID, "processing_state": state})
	}
}

func channelListAlertsResult(ctx context.Context, alertSvc *AlertService, projectID string) string {
	if alertSvc == nil {
		return "Alert service not available."
	}
	alerts, err := alertSvc.ListSummariesByProject(ctx, projectID, 50)
	if err != nil {
		return "Error retrieving alerts: " + err.Error()
	}
	if len(alerts) == 0 {
		return "No alerts found. You're all clear!"
	}
	unreadCount, _ := alertSvc.CountUnread(ctx, projectID)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d alerts (%d unread):\n", len(alerts), unreadCount))
	for _, a := range alerts {
		readStr := "unread"
		if a.IsRead {
			readStr = "read"
		}
		sb.WriteString(fmt.Sprintf("- %s (id: %s, severity: %s, %s)\n", a.Title, a.ID, a.Severity, readStr))
	}
	return strings.TrimSpace(sb.String())
}

func channelGetAlertResult(ctx context.Context, alertSvc *AlertService, projectID string, input json.RawMessage) string {
	var req struct {
		AlertID string `json:"alert_id"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_alert."
	}
	if req.AlertID == "" {
		return "get_alert requires alert_id."
	}
	if alertSvc == nil {
		return "Alert service not available."
	}
	alert, err := alertSvc.GetByID(ctx, projectID, req.AlertID)
	if err != nil {
		return fmt.Sprintf("Error retrieving alert %q: %v", req.AlertID, err)
	}
	if alert == nil {
		return fmt.Sprintf("Alert %q not found.", req.AlertID)
	}
	readStr := "unread"
	if alert.IsRead {
		readStr = "read"
	}
	return fmt.Sprintf("Alert: %s\n  ID: %s\n  Type: %s\n  Severity: %s\n  Status: %s\n  Message: %s", alert.Title, alert.ID, alert.Type, alert.Severity, readStr, alert.Message)
}

func channelCreateAlertResult(ctx context.Context, alertSvc *AlertService, projectID string, input json.RawMessage) string {
	var req CreateAlertRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for create_alert."
	}
	if strings.TrimSpace(req.Title) == "" {
		return "create_alert requires title."
	}
	if alertSvc == nil {
		return "Alert service not available."
	}
	severity, errText := channelAlertSeverity(req.Severity)
	if errText != "" {
		return errText
	}
	alertType, errText := channelAlertType(req.Type)
	if errText != "" {
		return errText
	}
	a := &models.Alert{ProjectID: projectID, Type: alertType, Severity: severity, Title: req.Title, Message: req.Message}
	if strings.TrimSpace(req.TaskID) != "" {
		tid := strings.TrimSpace(req.TaskID)
		a.TaskID = &tid
	}
	if err := alertSvc.Create(ctx, a); err != nil {
		return fmt.Sprintf("Error creating alert %q: %v", req.Title, err)
	}
	return fmt.Sprintf("Created alert %q (id: %s, severity: %s)", req.Title, a.ID, severity)
}

func channelDeleteAlertResult(ctx context.Context, alertSvc *AlertService, projectID string, input json.RawMessage) string {
	var req DeleteAlertRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for delete_alert."
	}
	if strings.TrimSpace(req.AlertID) == "" {
		return "delete_alert requires alert_id."
	}
	if alertSvc == nil {
		return "Alert service not available."
	}
	if err := alertSvc.Delete(ctx, projectID, req.AlertID); err != nil {
		return fmt.Sprintf("Error deleting alert %q: %v", req.AlertID, err)
	}
	return fmt.Sprintf("Deleted alert %s.", req.AlertID)
}

func channelToggleAlertResult(ctx context.Context, alertSvc *AlertService, projectID string, input json.RawMessage) string {
	var req ToggleAlertRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for toggle_alert."
	}
	if strings.TrimSpace(req.AlertID) == "" {
		return "toggle_alert requires alert_id."
	}
	if alertSvc == nil {
		return "Alert service not available."
	}
	if err := alertSvc.MarkRead(ctx, projectID, req.AlertID); err != nil {
		return fmt.Sprintf("Error marking alert %q as read: %v", req.AlertID, err)
	}
	return fmt.Sprintf("Marked alert %s as read.", req.AlertID)
}

func channelAlertSeverity(raw string) (models.AlertSeverity, string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warning":
		return models.SeverityWarning, ""
	case "error":
		return models.SeverityError, ""
	case "", "info":
		return models.SeverityInfo, ""
	default:
		return "", fmt.Sprintf("Invalid severity %q.", raw)
	}
}

func channelAlertType(raw string) (models.AlertType, string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "task_failed":
		return models.AlertTaskFailed, ""
	case "task_needs_followup":
		return models.AlertTaskNeedsFollowup, ""
	case "", "custom":
		return models.AlertCustom, ""
	default:
		return "", fmt.Sprintf("Invalid alert type %q.", raw)
	}
}

func formatChannelCapabilities(summaries []chatcontrol.ActionSummary) string {
	if len(summaries) == 0 {
		return "No capabilities available in the current mode."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Available capabilities (%d actions):\n", len(summaries)))
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

func channelCompletionFunc(platform string, execRepo *repository.ExecutionRepo, taskRepo *repository.TaskRepo, executionStreamHub *events.ExecutionStreamHub, queuedTurnPromoter func(projectID string)) func(context.Context, string, string, string, string, int, int64) {
	return func(ctx context.Context, execID, taskID, output, errorMessage string, tokensUsed int, durationMs int64) {
		completeChannelExecution(ctx, channelExecutionCompletionOptions{Platform: platform, ExecRepo: execRepo, TaskRepo: taskRepo, ExecutionStreamHub: executionStreamHub, QueuedTurnPromoter: queuedTurnPromoter, ExecID: execID, TaskID: taskID, Output: output, ErrorMessage: errorMessage, TokensUsed: tokensUsed, DurationMs: durationMs})
	}
}

func mergeChannelRuntimeActionHandlers(dst, src map[string]chatcontrol.RuntimeActionHandler) map[string]chatcontrol.RuntimeActionHandler {
	if dst == nil {
		dst = map[string]chatcontrol.RuntimeActionHandler{}
	}
	for name, handler := range src {
		dst[name] = handler
	}
	return dst
}

func newChannelActionSummaryCollector() *channelActionSummaryCollector {
	return &channelActionSummaryCollector{
		createdLines: []string{},
		editedLines:  []string{},
	}
}

func (c *channelActionSummaryCollector) addCreated(summary string) {
	c.addMarkerLines(summary, "[TASK_ID:", &c.createdLines)
}

func (c *channelActionSummaryCollector) addEdited(summary string) {
	c.addMarkerLines(summary, "[TASK_EDITED:", &c.editedLines)
}

func (c *channelActionSummaryCollector) addMarkerLines(summary, marker string, target *[]string) {
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
		if containsChannelSummaryLine(*target, line) {
			continue
		}
		*target = append(*target, line)
	}
}

func containsChannelSummaryLine(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (c *channelActionSummaryCollector) appendToOutput(output string) string {
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

// actionToolDefinitions returns tool definitions from the canonical registry
// for channel surfaces. Uses orchestrate mode since channels always operate in
// orchestrate mode.
func actionToolDefinitions(surface chatcontrol.Surface, includeThreadTools bool) []llmcontracts.RuntimeToolDefinition {
	return chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, surface, includeThreadTools)
}

func runtimeActionHandlerSet(handlers map[string]chatcontrol.RuntimeActionHandler) map[string]bool {
	out := make(map[string]bool, len(handlers))
	for name := range handlers {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// buildFullChannelActionToolRuntime assembles the shared complete channel runtime
// bundle. Channel services retain ownership of their handlers and context-specific
// authorization while this helper preserves the canonical definitions and gating.
func buildFullChannelActionToolRuntime(surface chatcontrol.Surface, handlers map[string]chatcontrol.RuntimeActionHandler) *llmcontracts.RuntimeTools {
	return &llmcontracts.RuntimeTools{
		Definitions: actionToolDefinitions(surface, true),
		Executor:    chatcontrol.BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, surface, handlers, runtimeActionHandlerSet(handlers)),
	}
}
