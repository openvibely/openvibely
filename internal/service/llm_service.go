package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmnormalize "github.com/openvibely/openvibely/internal/llm/normalize"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/update"
)

// LLMCaller abstracts model provider calls so tests can inject a mock
// instead of hitting real APIs.
type LLMCaller interface {
	CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (output, textOnly string, tokens int, err error)
}

type LLMService struct {
	llmConfigRepo             *repository.LLMConfigRepo
	execRepo                  *repository.ExecutionRepo
	taskRepo                  *repository.TaskRepo
	projectRepo               *repository.ProjectRepo
	scheduleRepo              *repository.ScheduleRepo
	attachmentRepo            *repository.AttachmentRepo
	agentRepo                 *repository.AgentRepo
	lifecycleRepo             *repository.LifecycleRepo
	mutationRecorder          func(models.Task) agentlibrary.MutationRecorder
	alertSvc                  *AlertService
	taskSvc                   *TaskService
	taskGoalSvc               *TaskGoalService
	worktreeSvc               *WorktreeService
	telegramSvc               *TelegramService
	slackSvc                  *SlackService
	discordSvc                *DiscordService
	llmCaller                 LLMCaller
	providerAdapters          map[models.LLMProvider]ProviderAdapter
	routing                   *agentRoutingStrategy
	fileChangeBroadcaster     *events.FileChangeBroadcaster
	threadInputRepo           *repository.ThreadInputRepo
	usageRepo                 *repository.UsageRepo
	upcomingSvc               *UpcomingService
	skillAnalyticsRepo        *repository.SkillAnalyticsRepo
	broadcaster               *events.Broadcaster
	executionStreamHub        *events.ExecutionStreamHub
	queuedTaskThreadPromoter  func(taskID string)
	channelMessageRouter      *ChannelMessageRouter
	githubIssueRuntime        GitHubIssueRuntimeProvider
	githubAuthRepo            *repository.GitHubAuthRepo
	taskPullRequestRepo       *repository.TaskPullRequestRepo
	githubPRFeedbackRepo      *repository.GitHubPRFeedbackRepo
	taskCommitStatRepo        *repository.TaskCommitStatRepo
	automationRegistrationSvc *AutomationRegistrationService
	automationRepo            *repository.AutomationRepo
	updateTracker             *update.WorkTracker

	// automationHandoffBeforeFinalAdmission is a deterministic test barrier for
	// the commit-to-submission lifecycle race. Production leaves it nil.
	automationHandoffBeforeFinalAdmission func()
	// globalSkillRoot is the parent directory holding <root>/agents for global
	// agents/skills. It is used for catalog construction and bounded skill
	// mutation writes; agents themselves remain user-managed.
	globalSkillRoot string
}

func NewLLMService(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo, taskRepo *repository.TaskRepo, projectRepo *repository.ProjectRepo, scheduleRepo *repository.ScheduleRepo, attachmentRepo *repository.AttachmentRepo) *LLMService {
	s := &LLMService{
		llmConfigRepo:  llmConfigRepo,
		execRepo:       execRepo,
		taskRepo:       taskRepo,
		projectRepo:    projectRepo,
		scheduleRepo:   scheduleRepo,
		attachmentRepo: attachmentRepo,
	}
	s.initProviderAdapters()
	s.routing = newAgentRoutingStrategy(s)
	return s
}

// SetAlertService sets the alert service for creating alerts on task failures.
// Called after construction to avoid circular dependencies.
func (s *LLMService) SetAlertService(alertSvc *AlertService) {
	s.alertSvc = alertSvc
}

// SetTaskService sets the task service used by authorized runtime actions.
// Called after construction to avoid circular dependencies
// (LLMService -> TaskService -> WorkerService -> LLMService).
func (s *LLMService) SetTaskService(taskSvc *TaskService) {
	s.taskSvc = taskSvc
}

func (s *LLMService) SetTaskGoalService(taskGoalSvc *TaskGoalService) {
	s.taskGoalSvc = taskGoalSvc
}

// SetWorktreeService sets the worktree service for task isolation.
func (s *LLMService) SetWorktreeService(wts *WorktreeService) {
	s.worktreeSvc = wts
}

// SetTelegramService sets the Telegram service for sending task completion notifications.
func (s *LLMService) SetTelegramService(ts *TelegramService) {
	s.telegramSvc = ts
}

// SetSlackService sets the Slack service for sending task completion notifications.
func (s *LLMService) SetSlackService(ss *SlackService) {
	s.slackSvc = ss
}

// SetDiscordService sets the Discord service for sending task completion notifications.
func (s *LLMService) SetDiscordService(ds *DiscordService) {
	s.discordSvc = ds
}

// SetFileChangeBroadcaster sets the file change broadcaster for real-time file change updates.
func (s *LLMService) SetFileChangeBroadcaster(fcb *events.FileChangeBroadcaster) {
	s.fileChangeBroadcaster = fcb
}

func (s *LLMService) SetThreadInputRepo(repo *repository.ThreadInputRepo) {
	s.threadInputRepo = repo
}

func (s *LLMService) SetUsageRepo(repo *repository.UsageRepo) {
	s.usageRepo = repo
}

func (s *LLMService) SetUpcomingService(upcomingSvc *UpcomingService) {
	s.upcomingSvc = upcomingSvc
}

func (s *LLMService) SetBroadcaster(b *events.Broadcaster) {
	s.broadcaster = b
}

func (s *LLMService) SetUpdateWorkTracker(tracker *update.WorkTracker) { s.updateTracker = tracker }

func (s *LLMService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
	s.initProviderAdapters()
}

func (s *LLMService) publishExecutionTerminal(execID string, status models.ExecutionStatus, errMsg string) {
	if s == nil {
		return
	}
	s.executionStreamHub.CloseTerminal(execID, status, errMsg)
}

func (s *LLMService) SetQueuedTaskThreadPromoter(promoter func(taskID string)) {
	s.queuedTaskThreadPromoter = promoter
}

func (s *LLMService) SetChannelMessageRouter(router *ChannelMessageRouter) {
	s.channelMessageRouter = router
}

func (s *LLMService) SetGitHubIssueRuntimeProvider(provider GitHubIssueRuntimeProvider) {
	s.githubIssueRuntime = provider
}

func (s *LLMService) SetGitHubAuthRepo(repo *repository.GitHubAuthRepo) {
	s.githubAuthRepo = repo
}

func (s *LLMService) SetAutomationRegistrationService(registration *AutomationRegistrationService) {
	s.automationRegistrationSvc = registration
}

func (s *LLMService) SetAutomationRepo(repo *repository.AutomationRepo) {
	s.automationRepo = repo
}

func (s *LLMService) SetTaskPullRequestRepo(repo *repository.TaskPullRequestRepo) {
	s.taskPullRequestRepo = repo
}

func (s *LLMService) SetGitHubPRFeedbackRepo(repo *repository.GitHubPRFeedbackRepo) {
	s.githubPRFeedbackRepo = repo
}

func (s *LLMService) taskSendMessageRuntimeTools(task models.Task) *llmcontracts.RuntimeTools {
	if s == nil || s.channelMessageRouter == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil
	}
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, false)
	filtered := make([]llmcontracts.RuntimeToolDefinition, 0, 1)
	for _, def := range defs {
		if def.Name == "send_message" {
			filtered = append(filtered, def)
			break
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &llmcontracts.RuntimeTools{
		Definitions: filtered,
		Executor: chatcontrol.BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, map[string]chatcontrol.RuntimeActionHandler{
			"send_message": func(ctx context.Context, input json.RawMessage) (string, error) {
				return ExecuteSendMessageTool(ctx, s.channelMessageRouter.WithAuditContext(string(chatcontrol.SurfaceWeb), "task"), task.ProjectID, input)
			},
		}, map[string]bool{"send_message": true}),
	}
}

func (s *LLMService) taskActionRuntimeTools(ctx context.Context, task models.Task) *llmcontracts.RuntimeTools {
	if s == nil {
		return nil
	}
	_, automationBound := AutomationContextFromContext(ctx)
	githubTools := buildGitHubIssueRuntimeTools(githubIssueRuntimeOptions{
		ProjectID:                task.ProjectID,
		ProjectRepo:              s.projectRepo,
		TaskRepo:                 s.taskRepo,
		TaskPullRequestRepo:      s.taskPullRequestRepo,
		TaskCommitStatRepo:       s.taskCommitStatRepo,
		GitHubPRFeedbackRepo:     s.githubPRFeedbackRepo,
		GitHubAuthRepo:           s.githubAuthRepo,
		ThreadInputRepo:          s.threadInputRepo,
		AutomationRepo:           s.automationRepo,
		GitHub:                   s.githubIssueRuntime,
		AfterPRFeedbackForwarded: s.promoteQueuedTaskThreadAfterCompletion,
		SuppressIssueComments:    automationBound,
	})
	return llmcontracts.CompositeRuntimeTools(s.taskSendMessageRuntimeTools(task), s.taskControlRuntimeTools(task), githubTools, s.automationBootstrapRuntimeTools(ctx, task))
}

func (s *LLMService) AutomationGitHubRuntimeTools(ctx context.Context, task models.Task, defs []llmcontracts.RuntimeToolDefinition) *llmcontracts.RuntimeTools {
	if s == nil || s.automationRepo == nil || s.githubIssueRuntime == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != task.ProjectID {
		return nil
	}
	allowed := make(map[string]bool, len(defs))
	for _, def := range defs {
		allowed[strings.ToLower(strings.TrimSpace(def.Name))] = true
	}
	runtime := buildGitHubIssueRuntimeTools(githubIssueRuntimeOptions{
		ProjectID:                task.ProjectID,
		ProjectRepo:              s.projectRepo,
		TaskRepo:                 s.taskRepo,
		TaskPullRequestRepo:      s.taskPullRequestRepo,
		TaskCommitStatRepo:       s.taskCommitStatRepo,
		GitHubPRFeedbackRepo:     s.githubPRFeedbackRepo,
		GitHubAuthRepo:           s.githubAuthRepo,
		ThreadInputRepo:          s.threadInputRepo,
		AutomationRepo:           s.automationRepo,
		GitHub:                   s.githubIssueRuntime,
		AfterPRFeedbackForwarded: s.promoteQueuedTaskThreadAfterCompletion,
		AfterPullRequestOpened:   s.clearGitHubPublicationGoalBlocker,
		TaskCreatedVia:           task.CreatedVia,
		SuppressIssueComments:    true,
	})
	if runtime == nil {
		return nil
	}
	filtered := make([]llmcontracts.RuntimeToolDefinition, 0, len(runtime.Definitions))
	for _, def := range runtime.Definitions {
		if allowed[strings.ToLower(strings.TrimSpace(def.Name))] {
			filtered = append(filtered, def)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	runtime.Definitions = filtered
	writeTools := make(map[string]bool, len(filtered))
	for _, def := range filtered {
		if def.Access == llmcontracts.RuntimeToolAccessWrite {
			writeTools[strings.ToLower(strings.TrimSpace(def.Name))] = true
		}
	}
	if len(writeTools) > 0 {
		baseExecutor := runtime.Executor
		runtime.Executor = func(toolCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			toolName := strings.ToLower(strings.TrimSpace(name))
			if writeTools[toolName] {
				if automationContext.OriginTask && toolName == "github_open_pull_request" {
					return baseExecutor(toolCtx, name, input)
				}
				if toolName == "github_create_issue" {
					return baseExecutor(toolCtx, name, input)
				}
				if automationContext.OriginTask && len(automationContext.Bindings) == 0 {
					return "", true, true, errors.New("GitHub mutation is not authorized by the caller's Automation graph because its originating graph is no longer current")
				}
				for _, binding := range automationContext.Bindings {
					current, err := s.automationRepo.IsCurrentActiveBinding(toolCtx, task.ProjectID, binding)
					if err != nil {
						return "", true, true, err
					}
					if !current {
						return "", true, true, errors.New("GitHub mutation is not authorized by the caller's current active Automation graph")
					}
				}
			}
			return baseExecutor(toolCtx, name, input)
		}
	}
	return runtime
}

func (s *LLMService) AutomationBootstrapRuntimeTools(ctx context.Context, task models.Task) *llmcontracts.RuntimeTools {
	return s.automationBootstrapRuntimeTools(ctx, task)
}

func (s *LLMService) automationBootstrapRuntimeTools(ctx context.Context, task models.Task) *llmcontracts.RuntimeTools {
	if s == nil || s.automationRegistrationSvc == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil
	}
	allowedAdapters := map[string]bool{}
	for _, handle := range SelectedSkillHandlesFromContext(ctx) {
		switch handle {
		case "openvibely_native_autonomous_sdlc_bootstrap":
			allowedAdapters[AutomationAdapterNativeSDLC] = true
		case "openvibely_github_autonomous_sdlc_bootstrap":
			allowedAdapters[AutomationAdapterGitHubSDLC] = true
		}
	}
	if len(allowedAdapters) == 0 {
		return nil
	}
	definition := llmcontracts.RuntimeToolDefinition{
		Name:        "register_automation_resources",
		Description: "Register the visible tasks and schedules created by this maintained bootstrap as a published Automation. Use actual resource IDs and canonical node keys; the current task project is enforced server-side.",
		Access:      llmcontracts.RuntimeToolAccessWrite,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"adapter_key":{"type":"string","enum":["native_sdlc","github_sdlc"]},"stable_key":{"type":"string"},"name":{"type":"string"},"resources":{"type":"array","minItems":2,"maxItems":100,"items":{"type":"object","properties":{"node_key":{"type":"string"},"resource_type":{"type":"string","enum":["task","schedule"]},"resource_id":{"type":"string"},"relation":{"type":"string","enum":["owned","shared"]}},"required":["node_key","resource_type","resource_id"],"additionalProperties":false}}},"required":["adapter_key","stable_key","resources"],"additionalProperties":false}`),
	}
	return &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{definition}, Executor: func(toolCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if name != "register_automation_resources" {
			return "", false, false, nil
		}
		var request AutomationRegistrationRequest
		if err := json.Unmarshal(input, &request); err != nil {
			return "", true, true, fmt.Errorf("invalid automation registration input: %w", err)
		}
		if !allowedAdapters[request.AdapterKey] {
			return "", true, true, fmt.Errorf("adapter %q is unavailable for the selected maintained bootstrap", request.AdapterKey)
		}
		request.ProjectID = task.ProjectID
		request.CreatedVia = "bootstrap"
		definition, reused, err := s.automationRegistrationSvc.Register(toolCtx, request)
		if err != nil {
			return "", true, true, err
		}
		result, err := json.Marshal(map[string]any{
			"automation_id": definition.Automation.ID,
			"version_id":    definition.Version.ID,
			"status":        definition.Automation.LifecycleState,
			"reused":        reused,
			"url":           fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, task.ProjectID),
		})
		if err != nil {
			return "", true, true, err
		}
		return string(result), true, false, nil
	}}
}

func (s *LLMService) taskControlRuntimeTools(task models.Task) *llmcontracts.RuntimeTools {
	if s == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil
	}
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
	filtered := make([]llmcontracts.RuntimeToolDefinition, 0, 5)
	allowed := map[string]bool{
		"list_tasks":                             true,
		"create_task":                            true,
		"create_swarm_task":                      true,
		"execute_tasks":                          true,
		"set_task_goal":                          true,
		"clear_task_goal":                        true,
		"get_task_goal":                          true,
		"pause_task_goal":                        true,
		"resume_task_goal":                       true,
		"list_schedules":                         true,
		"view_usage_analytics":                   true,
		"view_pulse":                             true,
		"schedule_task":                          true,
		"delete_schedule":                        true,
		"modify_schedule":                        true,
		"create_alert":                           true,
		"create_notification":                    true,
		"list_existing_automation_notifications": true,
		"list_alerts":                            true,
		"get_alert":                              true,
		"claim_alert":                            true,
		"create_alert_implementation_task":       true,
		"link_alert_implementation_task":         true,
		"complete_alert_processing":              true,
		"fail_alert_processing":                  true,
		"release_alert_claim":                    true,
		"list_capabilities":                      true,
	}
	if strings.HasPrefix(task.CreatedVia, "automation:") && strings.HasSuffix(task.CreatedVia, ":auditor") {
		delete(allowed, "create_task")
		delete(allowed, "create_swarm_task")
		delete(allowed, "execute_tasks")
		delete(allowed, "create_alert_implementation_task")
		delete(allowed, "link_alert_implementation_task")
	}
	for _, def := range defs {
		name := strings.ToLower(strings.TrimSpace(def.Name))
		if !allowed[name] {
			continue
		}
		if name == "list_alerts" && task.Category == models.CategoryScheduled {
			var schema map[string]json.RawMessage
			var properties map[string]json.RawMessage
			if json.Unmarshal(def.Parameters, &schema) == nil && json.Unmarshal(schema["properties"], &properties) == nil {
				delete(properties, "project_id")
				delete(properties, "read")
				if encodedProperties, err := json.Marshal(properties); err == nil {
					schema["properties"] = encodedProperties
					if encodedSchema, err := json.Marshal(schema); err == nil {
						def.Parameters = encodedSchema
					}
				}
			}
			def.Description += " Uses the persisted caller task's project and includes both read and unread alerts."
		}
		filtered = append(filtered, def)
	}
	if len(filtered) == 0 {
		return nil
	}
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID:          task.ProjectID,
		TaskSvc:            s.taskSvc,
		SwarmSvc:           nil,
		TaskRepo:           s.taskRepo,
		ExecRepo:           s.execRepo,
		ThreadInputRepo:    s.threadInputRepo,
		ExecutionStreamHub: s.executionStreamHub,
		LLMConfigRepo:      s.llmConfigRepo, PrepareTaskCreation: func(ctx context.Context, request *TaskCreationRequest) error {
			return s.prepareAutomationTaskCreation(ctx, task.ProjectID, request)
		},
		CreatePreparedTask: func(ctx context.Context, request TaskCreationRequest, agents []models.LLMConfig) ([]models.Task, string, bool, error) {
			return s.createPreparedAutomationTask(ctx, task.ProjectID, request, agents)
		},
		OnTasksCreated: func(ctx context.Context, requests []TaskCreationRequest, tasks []models.Task) error {
			return s.recordAutomationTasksCreated(ctx, task.ProjectID, requests, tasks)
		},
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{
		ProjectID:   task.ProjectID,
		TaskRepo:    s.taskRepo,
		TaskGoalSvc: s.taskGoalSvc,
	}))
	var usageAnalyticsSvc *UsageAnalyticsService
	if s.usageRepo != nil {
		usageAnalyticsSvc = NewUsageAnalyticsService(s.usageRepo, s.llmConfigRepo)
	}
	mergeChannelRuntimeActionHandlers(handlers, buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID:             task.ProjectID,
		CallerTaskID:          task.ID,
		TaskRepo:              s.taskRepo,
		ScheduleRepo:          s.scheduleRepo,
		WorkerSvc:             workerFromTaskService(s.taskSvc),
		LLMConfigRepo:         s.llmConfigRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          nil,
		CustomPersonalityRepo: nil,
		ProjectRepo:           s.projectRepo,
		AlertSvc:              s.alertSvc,
		UsageAnalyticsSvc:     usageAnalyticsSvc,
		UpcomingSvc:           s.upcomingSvc,
		PrepareImplementationTask: func(ctx context.Context, input *models.AlertImplementationTaskInput) error {
			return s.prepareAutomationAlertImplementationTask(ctx, task.ProjectID, input)
		},
	}))
	if task.Category == models.CategoryScheduled {
		if listAlerts := handlers["list_alerts"]; listAlerts != nil {
			handlers["list_alerts"] = func(ctx context.Context, input json.RawMessage) (string, error) {
				var request map[string]json.RawMessage
				if json.Unmarshal(input, &request) == nil {
					delete(request, "project_id")
					delete(request, "read")
					if normalized, err := json.Marshal(request); err == nil {
						input = normalized
					}
				}
				return listAlerts(ctx, input)
			}
		}
	}
	handlers["list_capabilities"] = func(_ context.Context, _ json.RawMessage) (string, error) {
		return formatChannelCapabilities(chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)), nil
	}
	return &llmcontracts.RuntimeTools{
		Definitions: filtered,
		Executor:    chatcontrol.BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, handlers, allowed),
	}
}

func (s *LLMService) connectedAutomationGitHubTask(ctx context.Context, projectID, automationID, versionID, nodeID string) (*models.AutomationNode, error) {
	// Immutable publications created before generic Task nodes used the
	// implementation role. Prefer it for those versions, then require the exact
	// GitHub inbox -> Task -> Open pull request topology used by current graphs.
	node, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, automationID, versionID, nodeID, "implementation", true)
	if err != nil || node != nil {
		return node, err
	}
	node, err = s.automationRepo.GetConnectedNodeByRole(ctx, projectID, automationID, versionID, nodeID, "task", true)
	if err != nil || node == nil {
		return node, err
	}
	inbox, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, automationID, versionID, node.ID, "github_inbox", false)
	if err != nil || inbox == nil || inbox.ID != nodeID {
		return nil, err
	}
	pullRequest, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, automationID, versionID, node.ID, "open_pull_request", true)
	if err != nil || pullRequest == nil {
		return nil, err
	}
	return node, nil
}

func (s *LLMService) prepareAutomationTaskCreation(ctx context.Context, projectID string, request *TaskCreationRequest) error {
	if s == nil || s.automationRepo == nil || request == nil {
		return nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != projectID {
		return nil
	}
	var selectedNode *models.AutomationNode
	for _, binding := range automationContext.Bindings {
		node, err := s.connectedAutomationGitHubTask(ctx, projectID, binding.AutomationID, binding.VersionID, binding.NodeID)
		if err != nil {
			return err
		}
		if node == nil {
			continue
		}
		if selectedNode != nil && selectedNode.ConfigJSON != node.ConfigJSON {
			return errors.New("Automation bindings have conflicting GitHub task configurations")
		}
		selectedNode = node
	}
	if selectedNode == nil {
		if request.SourceGitHubIssueNumber > 0 || strings.TrimSpace(request.SourceGitHubRepoURL) != "" {
			return errors.New("GitHub source issue task creation is not authorized by the caller's exact Automation graph")
		}
		return nil
	}
	if request.SourceGitHubIssueNumber <= 0 {
		return errors.New("Automation GitHub inbox task creation requires source_github_issue_number from this execution")
	}
	request.SourceGitHubRepoURL = ""
	request.Chain = nil
	var config map[string]any
	if err := json.Unmarshal([]byte(selectedNode.ConfigJSON), &config); err != nil {
		return fmt.Errorf("decoding GitHub task configuration: %w", err)
	}
	prompt, _ := config["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("published GitHub task configuration has no prompt")
	}
	if modelContext := strings.TrimSpace(request.Prompt); modelContext != "" && modelContext != prompt {
		prompt += "\n\nIssue-specific context:\n" + modelContext
	}
	for _, binding := range automationContext.Bindings {
		if binding.VersionID != selectedNode.VersionID || binding.AutomationID != selectedNode.AutomationID {
			continue
		}
		pullRequest, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, binding.AutomationID, binding.VersionID, selectedNode.ID, "open_pull_request", true)
		if err != nil {
			return err
		}
		if pullRequest != nil {
			var prConfig map[string]any
			if err := json.Unmarshal([]byte(pullRequest.ConfigJSON), &prConfig); err != nil {
				return fmt.Errorf("decoding pull request node configuration: %w", err)
			}
			instructions, _ := prConfig["instructions"].(string)
			base, _ := prConfig["base"].(string)
			draft, _ := prConfig["draft"].(bool)
			prompt += "\n\nPull request handoff:\n" + strings.TrimSpace(instructions) + "\nAfter implementation and validation, call github_open_pull_request for this task and source issue using base \"" + strings.TrimSpace(base) + "\" and draft=" + fmt.Sprintf("%t", draft) + ". Human review and merge remain authoritative."
		}
		break
	}
	request.Prompt = prompt
	if goal := automationConfigGoal(config); goal != "" {
		request.Goal = goal
	}
	category, _ := config["category"].(string)
	request.Category = category
	priority, _ := draftInt(config["priority"])
	request.Priority = priority
	modelConfigID, modelConfigConfigured := config["model_config_id"].(string)
	modelConfigID = strings.TrimSpace(modelConfigID)
	if strings.EqualFold(modelConfigID, automationDefaultModelConfigID) || !modelConfigConfigured || modelConfigID == "" {
		request.AgentID = automationDefaultModelConfigID
	} else {
		request.AgentID = modelConfigID
	}
	if ref, _ := config["agent_ref"].(string); strings.TrimSpace(ref) != "" {
		agent, err := resolveAutomationAgent(ctx, s.agentRepo, projectID, ref)
		if err != nil {
			return err
		}
		if agent == nil {
			return fmt.Errorf("GitHub task Agent %q is unavailable in this project", ref)
		}
		request.AgentDefinitionID = agent.ID
		request.Agent = ""
	}
	return nil
}

func (s *LLMService) prepareAutomationAlertImplementationTask(ctx context.Context, projectID string, input *models.AlertImplementationTaskInput) error {
	if s == nil || s.automationRepo == nil || input == nil {
		return nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != projectID {
		return nil
	}
	configuredGoal := ""
	configuredModelConfigID := ""
	for _, binding := range automationContext.Bindings {
		implementation, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, binding.AutomationID, binding.VersionID, binding.NodeID, "implementation", true)
		if err != nil {
			return err
		}
		if implementation == nil {
			continue
		}
		inbox, err := s.automationRepo.GetConnectedNodeByRole(ctx, projectID, binding.AutomationID, binding.VersionID, implementation.ID, "native_inbox", false)
		if err != nil {
			return err
		}
		if inbox == nil || inbox.ID != binding.NodeID {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(implementation.ConfigJSON), &config); err != nil {
			return fmt.Errorf("decoding Native implementation task configuration: %w", err)
		}
		goal := automationConfigGoal(config)
		if goal != "" {
			if configuredGoal != "" && configuredGoal != goal {
				return errors.New("Automation bindings have conflicting Native implementation task goals")
			}
			configuredGoal = goal
		}
		modelConfigID := automationExplicitModelConfigID(config["model_config_id"])
		if modelConfigID != "" {
			if configuredModelConfigID != "" && configuredModelConfigID != modelConfigID {
				return errors.New("Automation bindings have conflicting Native implementation task models")
			}
			configuredModelConfigID = modelConfigID
		}
	}
	if configuredGoal != "" {
		input.Goal = configuredGoal
	}
	if configuredModelConfigID != "" {
		input.AgentID = configuredModelConfigID
	}
	return nil
}

func automationConfigGoal(config map[string]any) string {
	goal, _ := config["goal"].(string)
	return strings.TrimSpace(goal)
}

type automationGitHubTaskCreationPlan struct {
	sourceBinding   models.AutomationBinding
	targetNode      models.AutomationNode
	issueResourceID string
	executionID     string
}

func (s *LLMService) automationGitHubTaskCreationPlan(ctx context.Context, projectID string, request TaskCreationRequest) (*automationGitHubTaskCreationPlan, error) {
	if s == nil || s.automationRepo == nil {
		return nil, nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != projectID {
		return nil, nil
	}
	_, executionID, executionOK := AutomationExecutionFromContext(ctx)
	if !executionOK || strings.TrimSpace(executionID) == "" {
		return nil, errors.New("Automation GitHub inbox task creation requires an exact current execution")
	}
	if request.SourceGitHubIssueNumber <= 0 {
		return nil, errors.New("Automation GitHub inbox task creation requires source_github_issue_number from this execution")
	}
	if s.githubIssueRuntime == nil || s.projectRepo == nil {
		return nil, errors.New("Automation GitHub issue provenance is unavailable")
	}
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("loading Automation project repository: %w", err)
	}
	if project == nil {
		return nil, errors.New("Automation project repository is unavailable")
	}
	repo, err := resolveAutomationProjectGitHubRepository(ctx, s.githubIssueRuntime, project)
	if err != nil {
		return nil, fmt.Errorf("resolving Automation project repository: %w", err)
	}
	issueResourceID := githubIssueResourceID(repo, request.SourceGitHubIssueNumber)
	discovered, err := s.automationRepo.BindingsForExecutionResource(ctx, projectID, executionID, "github_issue", issueResourceID)
	if err != nil {
		return nil, fmt.Errorf("loading exact Automation issue discovery: %w", err)
	}

	var plan *automationGitHubTaskCreationPlan
	for _, causalBinding := range automationContext.Bindings {
		targetNode, err := s.connectedAutomationGitHubTask(ctx, projectID, causalBinding.AutomationID, causalBinding.VersionID, causalBinding.NodeID)
		if err != nil {
			return nil, err
		}
		if targetNode == nil {
			continue
		}
		current, err := s.automationRepo.IsCurrentActiveBinding(ctx, projectID, causalBinding)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, errors.New("Automation GitHub inbox task creation is not authorized by the current active graph")
		}
		for _, discoveredBinding := range discovered.Bindings {
			if discoveredBinding.AutomationID != causalBinding.AutomationID || discoveredBinding.VersionID != causalBinding.VersionID ||
				discoveredBinding.InvocationID != causalBinding.InvocationID || strings.TrimSpace(discoveredBinding.WorkItemID) == "" {
				continue
			}
			sourceBinding := causalBinding
			sourceBinding.WorkItemID = discoveredBinding.WorkItemID
			candidate := &automationGitHubTaskCreationPlan{sourceBinding: sourceBinding,
				targetNode: *targetNode, issueResourceID: issueResourceID, executionID: executionID}
			if plan != nil && (plan.sourceBinding.AutomationID != candidate.sourceBinding.AutomationID ||
				plan.sourceBinding.VersionID != candidate.sourceBinding.VersionID || plan.sourceBinding.WorkItemID != candidate.sourceBinding.WorkItemID ||
				plan.targetNode.ID != candidate.targetNode.ID) {
				return nil, errors.New("source GitHub issue has conflicting Automation task provenance")
			}
			plan = candidate
		}
	}
	if plan == nil {
		return nil, errors.New("source GitHub issue was not discovered by this exact current Automation execution")
	}
	return plan, nil
}

func (s *LLMService) createPreparedAutomationTask(ctx context.Context, projectID string, request TaskCreationRequest, agents []models.LLMConfig) ([]models.Task, string, bool, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound || automationContext.ProjectID != projectID {
		return nil, "", false, nil
	}
	githubInboxTopology := false
	for _, binding := range automationContext.Bindings {
		target, err := s.connectedAutomationGitHubTask(ctx, projectID, binding.AutomationID, binding.VersionID, binding.NodeID)
		if err != nil {
			return nil, "", true, err
		}
		if target != nil {
			githubInboxTopology = true
			break
		}
	}
	if !githubInboxTopology {
		return nil, "", false, nil
	}
	plan, err := s.automationGitHubTaskCreationPlan(ctx, projectID, request)
	if err != nil {
		return nil, "", true, err
	}
	if plan == nil {
		return nil, "", false, nil
	}
	if s.taskSvc == nil || s.taskRepo == nil {
		return nil, "", true, errors.New("Automation task service unavailable")
	}

	selectedAgentID, _ := selectTaskCreationAgent(request, agents)
	category := resolveTaskCreationCategory(request, selectedAgentID, agents)
	task := &models.Task{ProjectID: projectID, Title: request.Title, Prompt: request.Prompt,
		Status: models.StatusPending, Category: category, Priority: request.Priority,
		CreatedVia: repository.AutomationCompilerTaskCreatedVia(plan.sourceBinding.AutomationID, plan.targetNode.NodeKey)}
	if selectedAgentID != "" {
		task.AgentID = &selectedAgentID
	}
	agentDefinitionID, err := resolveTaskCreationAgentDefinition(ctx, request, projectID, s.taskSvc)
	if err != nil {
		return nil, "", true, err
	}
	if agentDefinitionID != "" {
		task.AgentDefinitionID = &agentDefinitionID
	}
	objective := strings.TrimSpace(request.Goal)
	if len(objective) > MaxTaskGoalLength {
		return nil, "", true, ErrTaskGoalTooLong
	}
	var goal *models.TaskGoal
	if objective != "" {
		goal = &models.TaskGoal{GoalID: repository.NewID(), Objective: objective,
			Status: models.TaskGoalStatusActive, Reason: "set at task creation"}
	}
	canonical, created, err := s.automationRepo.CreateOrGetGitHubIssueTask(ctx, s.taskRepo, repository.AutomationGitHubIssueTaskCreation{
		ProjectID: projectID, ExecutionID: plan.executionID, IssueResourceID: plan.issueResourceID,
		SourceBinding: plan.sourceBinding, TargetNodeID: plan.targetNode.ID, Task: task, Goal: goal,
	})
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			return nil, "", true, ErrDuplicateTask
		}
		return nil, "", true, err
	}
	if created {
		if goal != nil && s.taskSvc.goalSvc != nil {
			s.taskSvc.goalSvc.publishGoalEvent(events.TaskGoalUpdated, goal)
		}
		s.taskSvc.SubmitSavedAutomationTask(*canonical)
	}
	action := "Created"
	if !created {
		action = "Reused"
	}
	summary := fmt.Sprintf("\n\n---\n%s 1 task(s):\n- \"%s\" (%s) [TASK_ID:%s]", action,
		canonical.Title, canonical.Category, canonical.ID)
	if objective != "" && created {
		summary += " [goal:set]"
	}
	return []models.Task{*canonical}, summary, true, nil
}

func (s *LLMService) recordAutomationTasksCreated(ctx context.Context, projectID string, requests []TaskCreationRequest, tasks []models.Task) error {
	if s == nil || s.automationRepo == nil {
		return nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != projectID {
		return nil
	}
	_, executionID, _ := AutomationExecutionFromContext(ctx)
	for i, created := range tasks {
		bindings := automationContext
		if i < len(requests) && (requests[i].SourceGitHubIssueNumber > 0 || strings.TrimSpace(requests[i].SourceGitHubRepoURL) != "") {
			return errors.New("Automation GitHub issue tasks must use atomic exact-provenance creation")
		}
		for _, binding := range bindings.Bindings {
			event := repository.AutomationProjectionEvent{
				Context: bindings, Binding: binding,
				ActivityKey:  "execution:" + executionID + ":create-task:" + created.ID,
				ActivityType: "create_task", ActivityStatus: models.AutomationActivityCompleted,
				Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: created.ID, Relation: "child"}},
			}
			if _, _, err := s.automationRepo.RecordProjectionEvent(ctx, event); err != nil {
				applog.Infof("[agent-svc] automation child task provenance failed task=%s: %v", created.ID, err)
			}
		}
	}
	return nil
}

func (s *LLMService) clearGitHubPublicationGoalBlocker(taskID string, result *OpenTaskPullRequestResult) {
	if s == nil || s.taskGoalSvc == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	reason := "GitHub PR publication succeeded"
	if result != nil && result.PullRequest != nil && result.PullRequest.Number > 0 {
		reason = fmt.Sprintf("GitHub PR publication succeeded with PR #%d", result.PullRequest.Number)
	}
	if _, err := s.taskGoalSvc.ClearBlockedReport(context.Background(), taskID, GitHubPRPublicationBlockerKey, reason); err != nil {
		applog.Infof("[agent-svc] clearing GitHub publication goal blocker failed task=%s: %v", taskID, err)
	}
}

func (s *LLMService) promoteQueuedTaskThreadAfterCompletion(taskID string) {
	if strings.TrimSpace(taskID) == "" {
		return
	}
	if s.queuedTaskThreadPromoter == nil {
		applog.Infof("[agent-svc] queued task-thread promoter not configured task=%s", taskID)
		return
	}
	applog.Infof("[agent-svc] promoting queued task-thread input after completion task=%s", taskID)
	s.queuedTaskThreadPromoter(taskID)
}

// SetAgentRepo sets the agent repository for resolving agent definitions on tasks.
func (s *LLMService) SetAgentRepo(repo *repository.AgentRepo) {
	s.agentRepo = repo
}

func (s *LLMService) SetLifecycleRepo(repo *repository.LifecycleRepo) {
	s.lifecycleRepo = repo
}

func (s *LLMService) SetLifecycleMutationRecorderFactory(fn func(models.Task) agentlibrary.MutationRecorder) {
	s.mutationRecorder = fn
}

// SetGlobalSkillRoot registers the on-disk parent directory that contains
// <root>/agents for global agents/skills. Empty disables the global skill root.
func (s *LLMService) SetGlobalSkillRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		s.globalSkillRoot = ""
		return
	}
	if abs, err := filepath.Abs(filepath.Clean(root)); err == nil {
		s.globalSkillRoot = abs
		return
	}
	s.globalSkillRoot = filepath.Clean(root)
}

// SetLLMCaller overrides the default model calling behavior.
// In tests, pass a mock to prevent real provider calls.
func (s *LLMService) SetLLMCaller(c LLMCaller) {
	s.llmCaller = c
}

func (s *LLMService) ensureRoutingStrategy() *agentRoutingStrategy {
	if s.routing == nil {
		if s.providerAdapters == nil {
			s.initProviderAdapters()
		}
		s.routing = newAgentRoutingStrategy(s)
	}
	return s.routing
}

func (s *LLMService) ExecuteTask(ctx context.Context, task models.Task) (*models.Execution, error) {
	exec, _, err := s.executeTaskWithChatContext(ctx, task)
	return exec, err
}

func (s *LLMService) executeTaskWithChatContext(ctx context.Context, task models.Task) (*models.Execution, llmcontracts.ChatContext, error) {
	applog.Infof("[agent-svc] ExecuteTask task=%s title=%q agent_id=%v", task.ID, task.Title, task.AgentID)

	var agent *models.LLMConfig
	var err error

	// If task has a specific agent assigned, use it; otherwise use default
	if task.AgentID != nil && *task.AgentID != "" {
		agent, err = s.llmConfigRepo.GetByID(ctx, *task.AgentID)
		if err != nil {
			applog.Infof("[agent-svc] ExecuteTask error getting agent %s: %v", *task.AgentID, err)
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("getting agent: %w", err)
		}
		if agent == nil {
			applog.Infof("[agent-svc] ExecuteTask agent %s not found, falling back to default", *task.AgentID)
			// Fall back to project default, then global default
			agent, err = s.getDefaultAgentForTask(ctx, task.ProjectID)
			if err != nil {
				applog.Infof("[agent-svc] ExecuteTask error getting default agent: %v", err)
				return nil, llmcontracts.ChatContext{}, fmt.Errorf("getting default agent: %w", err)
			}
		} else {
			applog.Infof("[agent-svc] ExecuteTask using assigned agent=%s provider=%s model=%s", agent.Name, agent.Provider, agent.Model)
		}
	} else {
		// Try project-level default agent first, then fall back to global default
		agent, err = s.getDefaultAgentForTask(ctx, task.ProjectID)
		if err != nil {
			applog.Infof("[agent-svc] ExecuteTask error getting default agent: %v", err)
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("getting default agent: %w", err)
		}
		if agent != nil {
			applog.Infof("[agent-svc] ExecuteTask using default agent=%s provider=%s model=%s", agent.Name, agent.Provider, agent.Model)
		}
	}

	if agent == nil {
		applog.Infof("[agent-svc] ExecuteTask no agent available")
		return nil, llmcontracts.ChatContext{}, fmt.Errorf("no agent configured")
	}

	return s.executeTaskWithAgent(ctx, task, *agent)
}

// getDefaultAgentForTask returns the appropriate default agent for a task.
// It checks the project's default agent first, then falls back to the global default.
func (s *LLMService) getDefaultAgentForTask(ctx context.Context, projectID string) (*models.LLMConfig, error) {
	// Try project-level default agent
	if projectID != "" && s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			applog.Infof("[agent-svc] getDefaultAgentForTask error getting project %s: %v", projectID, err)
		} else if project != nil && project.DefaultAgentConfigID != nil && *project.DefaultAgentConfigID != "" {
			agent, err := s.llmConfigRepo.GetByID(ctx, *project.DefaultAgentConfigID)
			if err != nil {
				applog.Infof("[agent-svc] getDefaultAgentForTask error getting project default agent %s: %v", *project.DefaultAgentConfigID, err)
			} else if agent != nil {
				applog.Infof("[agent-svc] getDefaultAgentForTask using project default agent=%s for project=%s", agent.Name, projectID)
				return agent, nil
			}
		}
	}

	// Fall back to global default
	return s.llmConfigRepo.GetDefault(ctx)
}

func (s *LLMService) reconcileMissingTaskAttachments(ctx context.Context, taskID string, attachments []models.Attachment) []models.Attachment {
	valid := make([]models.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if _, err := os.Stat(attachment.FilePath); err == nil {
			valid = append(valid, attachment)
			continue
		} else if !os.IsNotExist(err) {
			// Permission and transient filesystem errors remain provider-visible failures.
			valid = append(valid, attachment)
			continue
		}

		applog.Infof("[agent-svc] attachment lifecycle stage=runtime-reconcile task=%s attachment=%s path=%s error=file-not-found", taskID, attachment.ID, attachment.FilePath)
		if err := s.attachmentRepo.Delete(context.WithoutCancel(ctx), attachment.ID); err != nil {
			applog.Infof("[agent-svc] attachment lifecycle stage=runtime-reconcile-metadata task=%s attachment=%s path=%s error=%v", taskID, attachment.ID, attachment.FilePath, err)
		}
	}
	return valid
}

func (s *LLMService) ExecuteTaskWithAgent(ctx context.Context, task models.Task, agent models.LLMConfig) (*models.Execution, error) {
	exec, _, err := s.executeTaskWithAgent(ctx, task, agent)
	return exec, err
}

func (s *LLMService) executeTaskWithAgent(ctx context.Context, task models.Task, agent models.LLMConfig) (*models.Execution, llmcontracts.ChatContext, error) {
	applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s agent=%s model=%s", task.ID, agent.Name, agent.Model)
	finalizeCtx := context.Background()

	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && s.agentRepo != nil {
		if ad, adErr := s.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
			applog.Infof("[agent-svc] ExecuteTaskWithAgent using agent definition=%s (%s)", ad.Name, ad.ID)
		}
	}
	// Atomically claim the task (only succeeds if status is pending). The
	// worker pre-claims the task before invoking lifecycle hooks so the
	// kanban board can reflect the running state during early hooks; in
	// that case ClaimTask returns false (task is already running) but the
	// worker has signaled via context that it has already claimed the
	// task, so we proceed. Direct callers of ExecuteTaskWithAgent (no
	// pre-claim context flag) keep the original skip-on-running semantics.
	claimed, err := s.taskRepo.ClaimTask(ctx, task.ID)
	if err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error claiming task: %v", err)
		return nil, llmcontracts.ChatContext{}, fmt.Errorf("claiming task: %w", err)
	}
	if !claimed {
		if !isTaskPreClaimed(ctx) {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s not pending (already running/completed), skipping", task.ID)
			return nil, llmcontracts.ChatContext{}, nil
		}
		current, getErr := s.taskRepo.GetByID(ctx, task.ID)
		if getErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error checking task status after pre-claimed miss: %v", getErr)
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("checking task status: %w", getErr)
		}
		if current == nil || current.Status != models.StatusRunning {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s pre-claimed flag set but status=%v, skipping", task.ID, statusOrNil(current))
			return nil, llmcontracts.ChatContext{}, nil
		}
		applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s already claimed by worker, proceeding", task.ID)
	} else {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s status -> running", task.ID)
	}

	var runtimeTools *llmcontracts.RuntimeTools
	scopedFilesWorkDir := ""

	// Create or resolve the execution record. Automation dispatches precreate an
	// execution transactionally with their durable dispatch claim; ordinary task
	// runs keep the existing creation path.
	exec := &models.Execution{
		TaskID:           task.ID,
		AgentConfigID:    agent.ID,
		Status:           models.ExecRunning,
		PromptSent:       task.Prompt,
		StartsNewContext: task.StartsNewContext,
	}
	if preparedID := preparedAutomationExecutionID(ctx); preparedID != "" {
		prepared, getErr := s.execRepo.GetByID(ctx, preparedID)
		if getErr != nil {
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("loading prepared automation execution: %w", getErr)
		}
		if prepared == nil || prepared.TaskID != task.ID || prepared.Status != models.ExecRunning || prepared.DispatchID == "" {
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("prepared automation execution is invalid")
		}
		exec = prepared
		if err := s.execRepo.SetAgentConfigIfEmpty(ctx, exec.ID, agent.ID); err != nil {
			return nil, llmcontracts.ChatContext{}, fmt.Errorf("updating prepared automation execution agent: %w", err)
		}
		exec.AgentConfigID = agent.ID
	} else if err := s.execRepo.Create(ctx, exec); err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating execution: %v", err)
		return nil, llmcontracts.ChatContext{}, fmt.Errorf("creating execution: %w", err)
	}
	preparedExecution := preparedAutomationExecutionID(ctx) != ""
	ctx = withAutomationExecution(ctx, task.ID, exec.ID)
	finalizeCtx = context.WithoutCancel(ctx)
	if s.automationRepo != nil && !preparedExecution {
		if automationContext, ok := AutomationContextFromContext(ctx); ok && automationContext.ProjectID == task.ProjectID {
			for i, binding := range automationContext.Bindings {
				projection := repository.AutomationProjectionEvent{
					Context: automationContext, Binding: binding,
					ActivityKey: "execution:" + exec.ID + ":run", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
					Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}, {ResourceType: "execution", ResourceID: exec.ID}},
				}
				if binding.InvocationID == "" && binding.WorkItemID == "" {
					projection.WorkItemKey = "execution:" + exec.ID + ":root"
					projection.WorkItemKind = "task_execution"
					projection.WorkItemTitle = task.Title
					projection.EventKey = "execution:" + exec.ID + ":entered"
					projection.ToNodeID = binding.NodeID
					projection.Transition = models.AutomationTransitionEntered
				}
				workItem, _, projectionErr := s.automationRepo.RecordProjectionEvent(ctx, projection)
				if projectionErr != nil {
					return nil, llmcontracts.ChatContext{}, fmt.Errorf("recording automation execution provenance: %w", projectionErr)
				}
				if workItem != nil {
					automationContext.Bindings[i].WorkItemID = workItem.ID
					automationContext.Bindings[i].VersionID = workItem.OriginVersionID
				}
			}
			ctx = WithAutomationContext(ctx, automationContext)
			finalizeCtx = context.WithoutCancel(ctx)
		}
	}
	if s.broadcaster != nil {
		s.broadcaster.Publish(events.TaskEvent{
			Type:      events.TaskThreadExecutionStarted,
			ProjectID: task.ProjectID,
			TaskID:    task.ID,
			TaskName:  task.Title,
			ExecID:    exec.ID,
			Message:   task.Prompt,
		})
	}
	if s.threadInputRepo != nil {
		if err := s.threadInputRepo.BindPreExecutionQueuedTaskInputs(ctx, task.ID, exec.ID); err != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error binding pre-execution queued inputs: %v", err)
		}
	}
	applog.Infof("[agent-svc] ExecuteTaskWithAgent execution=%s created, calling LLM...", exec.ID)

	// Load attachments for the task
	attachments, err := s.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error loading attachments: %v", err)
		if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", err.Error(), 0, 0); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after attachment load failure: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecFailed, err.Error())
		}
		if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after attachment load failure: %v", statusErr)
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = err.Error()
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, llmcontracts.ChatContext{}, fmt.Errorf("loading attachments: %w", err)
	}
	attachments = s.reconcileMissingTaskAttachments(ctx, task.ID, attachments)
	applog.Infof("[agent-svc] ExecuteTaskWithAgent loaded %d attachments for task=%s", len(attachments), task.ID)

	// Vision-aware agent override: if the task has image attachments and the
	// current agent doesn't support vision, try to find a vision-capable agent.
	// API key and OAuth agents can support vision via multimodal content blocks.
	visionDecision := s.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, task.Prompt, attachments, agent, "ExecuteTaskWithAgent", task.ID)
	agent = visionDecision.Agent
	applog.Infof("[agent-svc] ExecuteTaskWithAgent vision routing changed=%v reason=%s detail=%q selected_agent=%s selected_provider=%s",
		visionDecision.Changed, visionDecision.Reason, visionDecision.Detail, agent.Name, agent.Provider)

	// Look up the project's repo path to use as the working directory for model
	// calls. Without this, provider tooling runs in the OpenVibely server
	// directory instead of the project's configured directory.
	workDir := ""
	repoDir := "" // original repo dir (for worktree setup and post-execution)
	managedWorktree := false
	startupWorktreeContext := ""
	if task.ProjectID != "" && s.projectRepo != nil {
		project, projErr := s.projectRepo.GetByID(ctx, task.ProjectID)
		if projErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error getting project for workDir: %v", projErr)
		} else if project != nil && project.RepoPath != "" {
			if _, statErr := os.Stat(project.RepoPath); os.IsNotExist(statErr) {
				errMsg := fmt.Sprintf("project repo path %q does not exist on disk", project.RepoPath)
				if project.RepoURL != "" {
					errMsg += fmt.Sprintf(" (cloned from %s). This typically happens after a container restart when PROJECT_REPO_ROOT is not on a persistent volume. Re-clone the project or fix your volume mounts.", project.RepoURL)
				} else {
					errMsg += ". Ensure the local repo path is mounted into the container."
				}
				applog.Infof("[agent-svc] ExecuteTaskWithAgent ERROR: %s", errMsg)
				if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); completeErr != nil {
					applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after missing repo: %v", completeErr)
				} else {
					s.publishExecutionTerminal(exec.ID, models.ExecFailed, errMsg)
				}
				if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
					applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after missing repo: %v", statusErr)
				}
				s.promoteQueuedTaskThreadAfterCompletion(task.ID)
				return exec, llmcontracts.ChatContext{}, fmt.Errorf("repo path missing: %s", errMsg)
			}
			repoDir = project.RepoPath
			workDir = project.RepoPath
			applog.Infof("[agent-svc] ExecuteTaskWithAgent using project workDir=%s", workDir)
		}
	}

	useRuntimeWorktree := agentDef == nil || !agentDef.ToolConfig.DisableRuntimeWorktree

	// Set up git worktree for task isolation when enabled for this agent.
	// Built-in agents that intentionally write directly to project-owned scoped
	// directories, such as memory consolidation, disable runtime worktrees in
	// their agent config.
	if useRuntimeWorktree && s.worktreeSvc != nil && repoDir != "" && task.Category != models.CategoryChat && IsGitRepo(repoDir) {
		wtPath, wtBranch, wtErr := s.worktreeSvc.SetupWorktree(ctx, &task, repoDir)
		if wtErr != nil {
			errMsg := fmt.Sprintf("setting up isolated task worktree: %v", wtErr)
			applog.Infof("[agent-svc] ExecuteTaskWithAgent worktree setup failed task=%s; refusing to use main repo: %v", task.ID, wtErr)
			if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); completeErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after worktree setup failure: %v", completeErr)
			} else {
				s.publishExecutionTerminal(exec.ID, models.ExecFailed, errMsg)
			}
			if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after worktree setup failure: %v", statusErr)
			}
			if task.Category == models.CategoryActive {
				if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
					applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving worktree-setup-failed task to completed category: %v", categoryErr)
				}
			}
			if s.alertSvc != nil {
				if alertErr := s.alertSvc.CreateTaskFailedAlert(finalizeCtx, task.ProjectID, task.ID, exec.ID, task.Title, errMsg); alertErr != nil {
					applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating worktree setup failure alert: %v", alertErr)
				}
			}
			exec.Status = models.ExecFailed
			exec.ErrorMessage = errMsg
			s.promoteQueuedTaskThreadAfterCompletion(task.ID)
			return exec, llmcontracts.ChatContext{}, fmt.Errorf("worktree setup failed: %w", wtErr)
		} else if wtPath != "" {
			workDir = wtPath
			managedWorktree = true
			task.WorktreePath = wtPath
			task.WorktreeBranch = wtBranch
			applog.Infof("[agent-svc] ExecuteTaskWithAgent using worktree workDir=%s branch=%s", workDir, wtBranch)

			if syncErr := s.worktreeSvc.SyncWorktreeFromMainAtStart(ctx, &task, repoDir); syncErr != nil {
				var conflictErr *StartupSyncConflictError
				if errors.As(syncErr, &conflictErr) {
					startupWorktreeContext = StartupSyncConflictContext(conflictErr)
					applog.Infof("[agent-svc] ExecuteTaskWithAgent startup worktree sync conflict task=%s, continuing in preserved worktree: %v", task.ID, syncErr)
				} else {
					applog.Infof("[agent-svc] ExecuteTaskWithAgent startup worktree auto-merge failed task=%s: %v", task.ID, syncErr)
					if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", syncErr.Error(), 0, 0); completeErr != nil {
						applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after startup auto-merge failure: %v", completeErr)
					} else {
						s.publishExecutionTerminal(exec.ID, models.ExecFailed, syncErr.Error())
					}
					if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
						applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after startup auto-merge failure: %v", statusErr)
					}
					if task.Category == models.CategoryActive {
						if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
							applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving startup-auto-merge-failed task to completed category: %v", categoryErr)
						} else {
							applog.Infof("[agent-svc] ExecuteTaskWithAgent moved startup-auto-merge-failed task=%s to completed category", task.ID)
						}
					}
					if s.alertSvc != nil {
						if alertErr := s.alertSvc.CreateTaskFailedAlert(finalizeCtx, task.ProjectID, task.ID, exec.ID, task.Title, syncErr.Error()); alertErr != nil {
							applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating startup auto-merge failure alert: %v", alertErr)
						}
					}
					exec.Status = models.ExecFailed
					exec.ErrorMessage = syncErr.Error()
					if s.telegramSvc != nil {
						s.telegramSvc.SendTaskCompletionNotification(finalizeCtx, task, "", syncErr.Error())
					}
					if s.slackSvc != nil {
						s.slackSvc.SendTaskCompletionNotification(finalizeCtx, task, "", syncErr.Error())
					}
					if s.discordSvc != nil {
						s.discordSvc.SendTaskCompletionNotification(finalizeCtx, task, "", syncErr.Error())
					}
					s.promoteQueuedTaskThreadAfterCompletion(task.ID)
					return exec, llmcontracts.ChatContext{}, fmt.Errorf("startup worktree auto-merge failed: %w", syncErr)
				}
			}
		}
	}

	if agentDef != nil && len(agentDef.ToolConfig.ScopedFiles) > 0 {
		// Scoped Files directories are configured project-relative and resolved
		// against the effective execution root. Agents using runtime worktrees get
		// scoped access inside the worktree; agents that disable runtime worktrees
		// operate directly on the project repo.
		scopedFilesRoot := workDir
		if scopedFilesRoot == "" {
			scopedFilesRoot = repoDir
		}
		preparedWorkDir, rt, prepErr := buildScopedFilesRuntimeTools(ctx, task.ProjectID, scopedFilesRoot, agentDef.ToolConfig)
		if prepErr != nil {
			errMsg := fmt.Sprintf("preparing scoped file tools: %v", prepErr)
			applog.Infof("[agent-svc] ExecuteTaskWithAgent scoped files prep failed task=%s: %v", task.ID, prepErr)
			if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); completeErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after scoped files prep failure: %v", completeErr)
			} else {
				s.publishExecutionTerminal(exec.ID, models.ExecFailed, errMsg)
			}
			if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after scoped files prep failure: %v", statusErr)
			}
			exec.Status = models.ExecFailed
			exec.ErrorMessage = errMsg
			s.promoteQueuedTaskThreadAfterCompletion(task.ID)
			return exec, llmcontracts.ChatContext{}, fmt.Errorf("preparing scoped file tools: %w", prepErr)
		}
		runtimeTools = rt
		scopedFilesWorkDir = preparedWorkDir
		if agentDef.ToolConfig.SkipDefaultTools && scopedFilesWorkDir != "" {
			workDir = scopedFilesWorkDir
			repoDir = ""
			applog.Infof("[agent-svc] ExecuteTaskWithAgent using scoped files workDir=%s", workDir)
		}
	}
	if agentSkillTools := s.agentDeclaredSkillRuntimeTools(ctx, task, agentDef, workDir); agentSkillTools != nil {
		runtimeTools = llmcontracts.CompositeRuntimeTools(runtimeTools, agentSkillTools)
	}
	projectInstructions := combineProjectInstructions(additionalProjectInstructionsFromContext(ctx), startupWorktreeContext, loadRootProjectInstructions(repoDir))
	if projectInstructions != "" {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent prepared project instructions (%d bytes)", len(projectInstructions))
	}
	// Start background diff snapshot broadcaster (if file change broadcaster is configured)
	var stopDiffBroadcast chan struct{}
	if s.fileChangeBroadcaster != nil && workDir != "" {
		stopDiffBroadcast = make(chan struct{})
		go s.broadcastDiffSnapshots(ctx, task.ID, exec.ID, workDir, repoDir, task.WorktreeBranch, task.MergeTargetBranch, managedWorktree, stopDiffBroadcast)
	}

	// Call the LLM
	callCtx := ctx
	var preparedSteering []models.ThreadInput
	if s.threadInputRepo != nil {
		callCtx = llmcontracts.WithSteeringCallback(callCtx, func(callbackCtx context.Context) (string, error) {
			inputs, steeringErr := s.threadInputRepo.PreparePendingTextSteering(callbackCtx, exec.ID, exec.ID)
			if steeringErr != nil || len(inputs) == 0 {
				return "", steeringErr
			}
			preparedSteering = append(preparedSteering, inputs...)
			s.publishTaskThreadInputAppliedEvents(exec.ID, inputs)
			return formatSteeringInstruction(combinedSteeringContent(inputs)), nil
		})
		callCtx = llmcontracts.WithSteeringRetryResetCallback(callCtx, func(callbackCtx context.Context) error {
			if len(preparedSteering) == 0 {
				return nil
			}
			ids := threadInputIDs(preparedSteering)
			if err := s.threadInputRepo.RestorePreparedSteering(callbackCtx, ids, exec.ID, exec.ID); err != nil {
				return err
			}
			preparedSteering = nil
			return nil
		})
	}
	runtimeToolsSupported := SupportsRuntimeChatActionTools(callCtx, s.llmConfigRepo, agent)
	var taskActionTools *llmcontracts.RuntimeTools
	if runtimeToolsSupported {
		taskActionTools = s.taskActionRuntimeTools(callCtx, task)
	}
	if runtimeToolsSupported || agent.Provider != models.ProviderMixture {
		if ctxTools := llmcontracts.RuntimeToolsFromContext(callCtx); ctxTools != nil || runtimeTools != nil || taskActionTools != nil {
			mergedTools := llmcontracts.CompositeRuntimeTools(runtimeTools, ctxTools, taskActionTools)
			callCtx = llmcontracts.WithRuntimeTools(callCtx, mergedTools)
			if mergedTools != nil && mergedTools.SkipDefaultTools {
				projectInstructions = ""
			}
		}
	} else {
		callCtx = llmcontracts.WithoutRuntimeTools(callCtx)
	}
	start := time.Now()
	result, err := s.callLLMDetailed(callCtx, task.Prompt, attachments, agent, exec.ID, workDir, projectInstructions, agentDef)
	output := result.Output
	textOnlyOutput := result.TextOnlyOutput
	tokensUsed := result.Usage.TotalTokens
	durationMs := time.Since(start).Milliseconds()
	taskCancelled := func() bool {
		current, getErr := s.taskRepo.GetByID(context.Background(), task.ID)
		if getErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error checking cancelled task state: %v", getErr)
			return false
		}
		return current != nil && current.Status == models.StatusCancelled
	}
	completeCancelled := func() (*models.Execution, llmcontracts.ChatContext, error) {
		bgCtx := context.Background()
		reason := "task cancelled by user"
		s.requeuePendingTaskSteeringForExecution(bgCtx, exec.ID)
		applog.Infof("[agent-svc] ExecuteTaskWithAgent CANCELLED task=%s duration=%dms", task.ID, durationMs)
		if completeErr := s.execRepo.Complete(bgCtx, exec.ID, models.ExecCancelled, output, reason, tokensUsed, durationMs); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing cancelled execution: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecCancelled, reason)
		}
		RecordUsageFromResult(bgCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecCancelled), ErrorMessage: reason, LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
		if statusErr := s.taskRepo.UpdateStatus(bgCtx, task.ID, models.StatusCancelled); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to cancelled: %v", statusErr)
		}
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(bgCtx, task.ID, models.CategoryBacklog); categoryErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving cancelled task to backlog: %v", categoryErr)
			} else {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent moved cancelled task=%s to backlog", task.ID)
			}
		}
		exec.Status = models.ExecCancelled
		exec.ErrorMessage = reason
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, result.ChatContext, fmt.Errorf("task cancelled")
	}

	// Stop diff snapshot broadcaster
	if stopDiffBroadcast != nil {
		close(stopDiffBroadcast)
	}

	if err != nil {
		// Distinguish between user cancellation and actual failures.
		// When a task is cancelled, the context is cancelled which stops the provider call.
		// Use background context for DB updates since the task context may be cancelled.
		bgCtx := context.Background()
		if ctx.Err() == context.Canceled {
			return completeCancelled()
		}

		s.requeuePendingTaskSteeringForExecution(bgCtx, exec.ID)
		applog.Infof("[agent-svc] ExecuteTaskWithAgent LLM call FAILED task=%s duration=%dms error=%v",
			task.ID, durationMs, err)
		// For max_tokens failures, preserve the partial output so the user can see
		// what work was done before the token limit was hit. For other failures,
		// clear the output — the task detail should only show the prompt and error.
		failedOutput := ""
		if output != "" {
			failedOutput = output
			applog.Infof("[agent-svc] ExecuteTaskWithAgent max_tokens failure, preserving partial output (%d bytes) task=%s", len(output), task.ID)
		}
		if completeErr := s.execRepo.Complete(bgCtx, exec.ID, models.ExecFailed, failedOutput, err.Error(), tokensUsed, durationMs); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecFailed, err.Error())
		}
		RecordUsageFromResult(bgCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecFailed), ErrorMessage: err.Error(), LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
		if statusErr := s.taskRepo.UpdateStatus(bgCtx, task.ID, models.StatusFailed); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to failed: %v", statusErr)
		}
		// Move failed tasks to completed category (same as successful tasks)
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(bgCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving failed task to completed category: %v", categoryErr)
			} else {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent moved failed task=%s to completed category", task.ID)
			}
		}
		// Create an alert for the failed task
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskFailedAlert(bgCtx, task.ProjectID, task.ID, exec.ID, task.Title, err.Error()); alertErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating alert: %v", alertErr)
			}
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = err.Error()
		// Send Telegram notification for tasks created via Telegram
		if s.telegramSvc != nil {
			s.telegramSvc.SendTaskCompletionNotification(bgCtx, task, "", err.Error())
		}
		if s.slackSvc != nil {
			s.slackSvc.SendTaskCompletionNotification(bgCtx, task, "", err.Error())
		}
		if s.discordSvc != nil {
			s.discordSvc.SendTaskCompletionNotification(bgCtx, task, "", err.Error())
		}
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, result.ChatContext, fmt.Errorf("calling LLM: %w", err)
	}
	if ctx.Err() == context.Canceled && taskCancelled() {
		return completeCancelled()
	}

	if err := s.commitPreparedTaskSteering(ctx, exec.ID, preparedSteering); err != nil {
		s.requeuePendingTaskSteeringForExecution(finalizeCtx, exec.ID)
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error committing steering task=%s exec=%s: %v", task.ID, exec.ID, err)
		if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, output, err.Error(), tokensUsed, durationMs); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution after steering commit failure: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecFailed, err.Error())
		}
		RecordUsageFromResult(finalizeCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecFailed), ErrorMessage: err.Error(), LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
		if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after steering commit failure: %v", statusErr)
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = err.Error()
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, result.ChatContext, fmt.Errorf("committing steering: %w", err)
	}

	if strings.TrimSpace(output) == "" && strings.TrimSpace(textOnlyOutput) == "" {
		reason := "model returned empty response"
		applog.Infof("[agent-svc] ExecuteTaskWithAgent empty LLM response task=%s duration=%dms", task.ID, durationMs)
		if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, "", reason, tokensUsed, durationMs); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing empty-response execution: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecFailed, reason)
		}
		RecordUsageFromResult(finalizeCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecFailed), ErrorMessage: reason, LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
		if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status after empty response: %v", statusErr)
		}
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving empty-response task to completed category: %v", categoryErr)
			}
		}
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskFailedAlert(finalizeCtx, task.ProjectID, task.ID, exec.ID, task.Title, reason); alertErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating empty-response alert: %v", alertErr)
			}
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = reason
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, result.ChatContext, fmt.Errorf("calling LLM: %s", reason)
	}

	applog.Infof("[agent-svc] ExecuteTaskWithAgent LLM call SUCCESS task=%s tokens=%d duration=%dms output_len=%d",
		task.ID, tokensUsed, durationMs, len(output))

	// Check for agent-reported failure/followup markers in the output.
	// The agent is instructed to end its response with [STATUS: FAILED | reason]
	// or [STATUS: NEEDS_FOLLOWUP | reason] when the task fails or needs attention.
	// Use textOnlyOutput (model text only, no tool results/thinking) to avoid
	// false positives from source code containing STATUS markers in tool results.
	statusCheckOutput := textOnlyOutput
	if statusCheckOutput == "" {
		statusCheckOutput = output
	}
	if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: FAILED |"); found {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent agent reported STATUS FAILED task=%s reason=%q", task.ID, reason)
		// Preserve the assistant's visible failure transcript so task-thread follow-ups
		// can replay the failed turn chronologically instead of looking like a blank task.
		if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecFailed, output, reason, tokensUsed, durationMs); completeErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
		} else {
			s.publishExecutionTerminal(exec.ID, models.ExecFailed, reason)
		}
		RecordUsageFromResult(finalizeCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecFailed), ErrorMessage: reason, LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
		if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusFailed); statusErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to failed: %v", statusErr)
		}
		// Move failed tasks to completed category (same as successful tasks)
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving failed task to completed category: %v", categoryErr)
			} else {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent moved failed task=%s to completed category", task.ID)
			}
		}
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskFailedAlert(finalizeCtx, task.ProjectID, task.ID, exec.ID, task.Title, reason); alertErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating alert: %v", alertErr)
			}
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = reason
		// Send Telegram notification for tasks created via Telegram
		if s.telegramSvc != nil {
			s.telegramSvc.SendTaskCompletionNotification(finalizeCtx, task, "", reason)
		}
		if s.slackSvc != nil {
			s.slackSvc.SendTaskCompletionNotification(finalizeCtx, task, "", reason)
		}
		if s.discordSvc != nil {
			s.discordSvc.SendTaskCompletionNotification(finalizeCtx, task, "", reason)
		}
		s.promoteQueuedTaskThreadAfterCompletion(task.ID)
		return exec, result.ChatContext, nil
	}

	// NOTE: detectToolFailures was previously used here to scan for non-zero exit
	// codes and fail the task. Provider agentic paths handle tool errors internally:
	// the model sees the error,
	// can retry or fix the issue, and continues working. Intermediate command failures
	// should not kill the task. The model uses [STATUS: FAILED | reason] to explicitly
	// report task failure when it determines the task cannot be completed.

	// Record success
	completedExecution := false
	if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecCompleted, output, "", tokensUsed, durationMs); completeErr != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
	} else {
		completedExecution = true
	}
	RecordUsageFromResult(finalizeCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecCompleted), LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)

	// Capture git diff of changes made during execution. Only a worktree
	// successfully established for this execution may use target-relative review
	// capture; persisted task metadata can refer to stale historical lineage.
	if workDir != "" {
		if managedWorktree {
			diffOutput := s.captureWorktreeDiffAfterExecution(finalizeCtx, exec, &task, repoDir, output, agent)
			if diffOutput != "" {
				exec.DiffOutput = diffOutput
			}
		} else if diffOutput := s.CaptureGitDiff(workDir); diffOutput != "" {
			if diffErr := s.execRepo.UpdateDiffOutput(finalizeCtx, exec.ID, diffOutput); diffErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error saving diff output: %v", diffErr)
			} else {
				exec.DiffOutput = diffOutput
				applog.Infof("[agent-svc] ExecuteTaskWithAgent captured diff output for exec=%s (%d bytes)", exec.ID, len(diffOutput))
				PublishDiffSnapshotIfChanged(finalizeCtx, nil, s.fileChangeBroadcaster, &DiffSnapshotState{}, task.ID, exec.ID, diffOutput, true)
			}
		}
	}

	// Commit, merge, and update worktree status only for the managed worktree
	// established for this execution, never from retained task metadata alone.
	if managedWorktree && s.worktreeSvc != nil && repoDir != "" {
		s.worktreeSvc.HandlePostExecution(finalizeCtx, &task, exec, repoDir)
	}
	// Keep the task non-terminal until all managed-worktree commit, diff, merge,
	// and cleanup writers have left the shared repository mutation boundary.
	// Card/manual actions gate on this status and cannot enter finalization early.
	if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusCompleted); statusErr != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to completed: %v", statusErr)
	}
	if completedExecution {
		s.publishExecutionTerminal(exec.ID, models.ExecCompleted, "")
	}

	// Check for follow-up marker (task still completed, but alert created)
	if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: NEEDS_FOLLOWUP |"); found {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent agent reported STATUS NEEDS_FOLLOWUP task=%s reason=%q", task.ID, reason)
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskNeedsFollowupAlert(finalizeCtx, task.ProjectID, task.ID, exec.ID, task.Title, reason); alertErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating followup alert: %v", alertErr)
			}
		}
	}

	// Automatically move completed tasks from active category to completed category
	if task.Category == models.CategoryActive {
		if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving task to completed category: %v", categoryErr)
		} else {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent moved task=%s to completed category", task.ID)
		}
	}
	// Automatically move completed scheduled tasks with RepeatOnce to completed category
	if task.Category == models.CategoryScheduled {
		schedules, err := s.scheduleRepo.ListByTask(finalizeCtx, task.ID)
		if err != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error getting schedules for task %s: %v", task.ID, err)
		} else if len(schedules) > 0 && schedules[0].RepeatType == models.RepeatOnce {
			if categoryErr := s.taskRepo.UpdateCategory(finalizeCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error moving RepeatOnce task to completed category: %v", categoryErr)
			} else {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent moved RepeatOnce task=%s to completed category", task.ID)
			}
		}
	}
	exec.Status = models.ExecCompleted
	exec.Output = output
	exec.TokensUsed = tokensUsed
	exec.DurationMs = durationMs

	// Trigger task chaining if configured
	if s.taskSvc != nil {
		if chainErr := s.triggerTaskChain(finalizeCtx, task, textOnlyOutput); chainErr != nil {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent error triggering task chain: %v", chainErr)
		}
	}

	// Send Telegram notification for tasks created via Telegram
	if s.telegramSvc != nil {
		s.telegramSvc.SendTaskCompletionNotification(finalizeCtx, task, output, "")
	}
	if s.slackSvc != nil {
		s.slackSvc.SendTaskCompletionNotification(finalizeCtx, task, output, "")
	}
	if s.discordSvc != nil {
		s.discordSvc.SendTaskCompletionNotification(finalizeCtx, task, output, "")
	}
	s.promoteQueuedTaskThreadAfterCompletion(task.ID)

	return exec, result.ChatContext, nil
}

func (s *LLMService) publishTaskThreadInputAppliedEvents(execID string, inputs []models.ThreadInput) {
	if s.broadcaster == nil {
		return
	}
	for _, input := range inputs {
		if input.TaskID == "" {
			continue
		}
		s.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputApplied,
			ProjectID:      input.ProjectID,
			TaskID:         input.TaskID,
			ExecID:         execID,
			Message:        input.Content,
			PendingInputID: input.ID,
			HasAttachments: input.AttachmentSessionID != "",
		})
	}
}

func (s *LLMService) publishTaskThreadInputQueuedEvents(inputs []models.ThreadInput) {
	if s.broadcaster == nil {
		return
	}
	for _, input := range inputs {
		if input.TaskID == "" {
			continue
		}
		s.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputQueued,
			ProjectID:      input.ProjectID,
			TaskID:         input.TaskID,
			ExecID:         input.RunExecutionID,
			Message:        input.Content,
			PendingInputID: input.ID,
			HasAttachments: input.AttachmentSessionID != "",
		})
	}
}

func (s *LLMService) commitPreparedTaskSteering(ctx context.Context, execID string, inputs []models.ThreadInput) error {
	if s.threadInputRepo == nil || len(inputs) == 0 {
		return nil
	}
	for _, input := range inputs {
		if err := s.threadInputRepo.MarkApplied(ctx, input.ID, execID, execID); err != nil {
			return err
		}
	}
	return nil
}

func (s *LLMService) requeuePendingTaskSteeringForExecution(ctx context.Context, execID string) {
	if s.threadInputRepo == nil {
		return
	}
	requeued, err := s.threadInputRepo.RequeuePendingSteeringForExecution(ctx, execID)
	if err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent exec=%s error requeueing pending steering: %v", execID, err)
		return
	}
	s.publishTaskThreadInputQueuedEvents(requeued)
}

func threadInputIDs(inputs []models.ThreadInput) []string {
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids = append(ids, input.ID)
	}
	return ids
}

func combinedSteeringContent(inputs []models.ThreadInput) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if content := strings.TrimSpace(input.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func formatSteeringInstruction(steeringMessage string) string {
	return strings.TrimSpace(steeringMessage)
}

func (s *LLMService) SummarizeWorktreeCommitDiffForAgentID(ctx context.Context, worktreePath string, agentID string, commitCtx WorktreeCommitMessageContext) string {
	if strings.TrimSpace(agentID) == "" || s.llmConfigRepo == nil {
		return ""
	}
	agent, err := s.llmConfigRepo.GetByID(ctx, agentID)
	if err != nil || agent == nil {
		if err != nil {
			applog.Infof("[agent-svc] commit diff summary agent lookup failed agent=%s: %v", agentID, err)
		}
		return ""
	}
	return s.SummarizeWorktreeCommitDiff(ctx, worktreePath, *agent, commitCtx)
}

func (s *LLMService) SummarizeWorktreeCommitDiff(ctx context.Context, worktreePath string, agent models.LLMConfig, commitCtx WorktreeCommitMessageContext) string {
	diffContext := BuildWorktreeCommitDiffContext(worktreePath)
	if diffContext == "" {
		return ""
	}
	prompt := buildWorktreeCommitSummaryPrompt(diffContext, commitCtx)
	summaryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, _, err := s.CallAgentRawDirectNoTools(summaryCtx, prompt, nil, agent, worktreePath)
	if err != nil {
		applog.Infof("[agent-svc] commit diff summary failed worktree=%s: %v", worktreePath, err)
		return ""
	}
	return parseWorktreeCommitSummaryOutput(output)
}

func parseWorktreeCommitSummaryOutput(output string) string {
	match := ""
	for offset, char := range output {
		if char != '{' {
			continue
		}
		candidate := parseWorktreeCommitSummaryObject(output[offset:])
		if candidate == "" {
			continue
		}
		if match != "" {
			return ""
		}
		match = candidate
	}
	return match
}

func parseWorktreeCommitSummaryObject(output string) string {
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(output))
	if err := decoder.Decode(&fields); err != nil || len(fields) != 1 {
		return ""
	}
	rawSubject, ok := fields["subject"]
	if !ok {
		return ""
	}
	var subject string
	if err := json.Unmarshal(rawSubject, &subject); err != nil {
		return ""
	}
	subject = strings.TrimSpace(subject)
	if subject == "" || strings.ContainsAny(subject, "\r\n") {
		return ""
	}
	subject = cleanCommitSubject(subject)
	if subject == "" || len(subject) > 72 {
		return ""
	}
	return subject
}

func buildWorktreeCommitSummaryPrompt(diffContext string, commitCtx WorktreeCommitMessageContext) string {
	var b strings.Builder
	b.WriteString("Write one concise git commit subject for these worktree changes.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Describe what actually changed in the diff, not which files were edited.\n")
	b.WriteString("- Use an imperative, capitalized subject, for example: Add analytics chart.\n")
	b.WriteString("- Use plain language with no conventional prefix such as feat:, fix:, chore:, docs:, or test:.\n")
	b.WriteString("- Do not mention tasks, task turns, follow-ups, lifecycle phases, worktrees, or file lists unless that is literally the product code being changed.\n")
	b.WriteString("- The subject must be at most 72 characters.\n")
	b.WriteString("- If supporting context conflicts with the diff, ignore the supporting context.\n\n")
	b.WriteString("Return exactly one JSON object with no markdown or other text:\n")
	b.WriteString(`{"subject":"Add concise description"}`)
	b.WriteString("\n\n")
	if context := buildWorktreeCommitSupportingContext(commitCtx); context != "" {
		b.WriteString("Supporting context, only if it agrees with the diff:\n")
		b.WriteString(context)
		b.WriteString("\n\n")
	}
	b.WriteString("Actual diff facts and hunks:\n")
	b.WriteString(diffContext)
	return b.String()
}

func buildWorktreeCommitSupportingContext(commitCtx WorktreeCommitMessageContext) string {
	parts := make([]string, 0, 3)
	if summary := strings.TrimSpace(commitCtx.Summary); summary != "" {
		parts = append(parts, "Previous execution output: "+truncateCommitSnippet(summary, 1500))
	}
	if intent := strings.TrimSpace(commitCtx.TurnIntent); intent != "" {
		parts = append(parts, "User request: "+truncateCommitSnippet(intent, 1000))
	}
	if title := strings.TrimSpace(commitCtx.TaskTitle); title != "" {
		parts = append(parts, "Task title: "+truncateCommitSnippet(title, 300))
	}
	return strings.Join(parts, "\n")
}

type DiffSnapshotState struct {
	lastDiff string
	hasLast  bool
}

func (s *DiffSnapshotState) shouldPublishPeriodic(diffOutput string) bool {
	if diffOutput == "" {
		return false
	}
	if s.hasLast && diffOutput == s.lastDiff {
		return false
	}
	s.lastDiff = diffOutput
	s.hasLast = true
	return true
}

func (s *DiffSnapshotState) publishFinal(diffOutput string) bool {
	if diffOutput == "" {
		return false
	}
	s.lastDiff = diffOutput
	s.hasLast = true
	return true
}

func PublishDiffSnapshotIfChanged(ctx context.Context, execRepo interface {
	UpdateDiffOutput(context.Context, string, string) error
}, broadcaster *events.FileChangeBroadcaster, state *DiffSnapshotState, taskID, execID, diffOutput string, final bool) bool {
	if state == nil {
		state = &DiffSnapshotState{}
	}
	shouldPublish := state.shouldPublishPeriodic(diffOutput)
	if final {
		shouldPublish = state.publishFinal(diffOutput)
	}
	if !shouldPublish {
		return false
	}
	if execRepo != nil {
		if err := execRepo.UpdateDiffOutput(ctx, execID, diffOutput); err != nil {
			applog.Infof("[diff-broadcast] error updating execution diff output: %v", err)
		}
	}
	if broadcaster != nil {
		broadcaster.Publish(events.FileChangeEvent{
			Type:      events.DiffSnapshot,
			TaskID:    taskID,
			ExecID:    execID,
			Timestamp: time.Now().UnixMilli(),
		})
	}
	return true
}

func (s *LLMService) captureWorktreeDiffAfterExecution(ctx context.Context, exec *models.Execution, task *models.Task, repoDir string, outputSummary string, agent models.LLMConfig) string {
	if exec == nil || task == nil || task.WorktreePath == "" || repoDir == "" {
		return ""
	}
	var diffOutput string
	if err := WithRepositoryMutation(repoDir, func() error {
		diffOutput = s.captureWorktreeDiffAfterExecutionUnlocked(ctx, exec, task, repoDir, outputSummary, agent)
		return nil
	}); err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error acquiring finalization lease task=%s worktree=%s: %v", task.ID, task.WorktreePath, err)
	}
	return diffOutput
}

func (s *LLMService) captureWorktreeDiffAfterExecutionUnlocked(ctx context.Context, exec *models.Execution, task *models.Task, repoDir string, outputSummary string, agent models.LLMConfig) string {
	worktreeBranch := GetCurrentBranch(task.WorktreePath)
	if worktreeBranch == "" {
		worktreeBranch = task.WorktreeBranch
	}
	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}
	commitCtx := WorktreeCommitMessageContext{
		Phase:      WorktreeCommitPhaseInitial,
		TaskTitle:  task.Title,
		TurnIntent: exec.PromptSent,
		Summary:    outputSummary,
	}
	commitCtx.DiffSummary = s.SummarizeWorktreeCommitDiff(ctx, task.WorktreePath, agent, commitCtx)
	commitMessage := BuildWorktreeCommitMessage(task.WorktreePath, commitCtx)
	if err := s.CommitTaskWorktreeChanges(ctx, task, exec, task.WorktreePath, commitMessage); err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error committing worktree changes task=%s worktree=%s branch=%s: %v", task.ID, task.WorktreePath, worktreeBranch, err)
	}

	// Persist the authoritative branch diff when the auto-commit succeeds or the
	// provider already committed. If the provider left uncommitted edits and the
	// app-level commit fails, preserve those edits in diff_output so Changes does
	// not appear empty and the merge path can still commit them just-in-time.
	diffOutput := GetWorktreeDiffWithUncommitted(repoDir, worktreeBranch, targetBranch, task.WorktreePath)
	if diffOutput == "" {
		return ""
	}
	if diffErr := s.execRepo.UpdateDiffOutput(ctx, exec.ID, diffOutput); diffErr != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error saving worktree diff output: %v", diffErr)
		return diffOutput
	}
	applog.Infof("[agent-svc] ExecuteTaskWithAgent captured worktree diff output for exec=%s (%d bytes)", exec.ID, len(diffOutput))
	PublishDiffSnapshotIfChanged(ctx, nil, s.fileChangeBroadcaster, &DiffSnapshotState{}, task.ID, exec.ID, diffOutput, true)
	return diffOutput
}

// broadcastDiffSnapshots periodically captures and broadcasts git diff snapshots
// while a task is executing, allowing real-time file change monitoring. Managed
// worktrees use the reviewable target-to-worktree diff; direct project-checkout
// executions use the pending git diff against HEAD.
func (s *LLMService) broadcastDiffSnapshots(ctx context.Context, taskID, execID, workDir, repoDir, worktreeBranch, mergeTargetBranch string, managedWorktree bool, stop <-chan struct{}) {
	captureDiff := func() string {
		if managedWorktree && repoDir != "" {
			targetBranch := mergeTargetBranch
			if targetBranch == "" {
				targetBranch = GetDefaultBranch(repoDir)
			}
			currentBranch := GetCurrentBranch(workDir)
			if currentBranch == "" {
				currentBranch = worktreeBranch
			}
			// Task Changes uses one target-to-current-worktree diff. This includes
			// committed, staged, and unstaged tracked state without concatenating
			// a separate HEAD-based pending diff.
			return GetWorktreeDiffWithUncommitted(repoDir, currentBranch, targetBranch, workDir)
		}
		return s.CaptureGitDiff(workDir)
	}

	ticker := time.NewTicker(2 * time.Second) // Capture diff every 2 seconds
	defer ticker.Stop()
	state := &DiffSnapshotState{}

	for {
		select {
		case <-stop:
			// Final invalidation on completion. Final DB persistence is handled by
			// captureWorktreeDiffAfterExecution/CaptureGitDiff in the finalize path.
			if diffOutput := captureDiff(); diffOutput != "" {
				PublishDiffSnapshotIfChanged(ctx, nil, s.fileChangeBroadcaster, state, taskID, execID, diffOutput, true)
			}
			return

		case <-ctx.Done():
			return

		case <-ticker.C:
			diffOutput := captureDiff()
			// Update execution's diff output in database for realtime UI refresh only
			// when the diff actually changed. The SSE event is a small invalidation
			// signal; clients fetch GET /tasks/:taskId/changes for rendered content.
			PublishDiffSnapshotIfChanged(ctx, s.execRepo, s.fileChangeBroadcaster, state, taskID, execID, diffOutput, false)
		}
	}
}

// CallAgentDirect calls the agent directly with a message, without task execution overhead
func (s *LLMService) CallAgentDirect(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	return s.callAgentDirect(ctx, message, attachments, agent, workDir, false)
}

// CallAgentDirectWithDefinition calls an agent directly while applying a persisted
// agent definition's prompt, runtime tools, and scoped-file grants. Lifecycle
// hooks use this to run protected system agents such as Skill Curator and Memory
// Curator with the same scoped tools they receive during normal task execution.
func (s *LLMService) CallAgentDirectWithDefinition(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent) (string, int, error) {
	return s.callAgentDirectWithDefinition(ctx, message, attachments, agent, workDir, agentDef, false)
}

// CallAgentDirectWithDefinitionNoTools calls an agent directly while preserving
// its prompt/model identity but suppressing scoped, plugin, MCP, and provider
// default tools for hooks that are pure JSON routing/selection steps.
func (s *LLMService) CallAgentDirectWithDefinitionNoTools(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent) (string, int, error) {
	return s.callAgentDirectWithDefinition(ctx, message, attachments, agent, workDir, agentDef, true)
}

// CallAgentRawDirectNoTools calls the agent with an already-composed utility
// prompt, without coding-agent framing or tools.
func (s *LLMService) CallAgentRawDirectNoTools(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	ctx = llmcontracts.WithoutRuntimeTools(ctx)
	return s.callAgentDirectWithDefinitionMode(ctx, message, attachments, agent, workDir, nil, true, true)
}

type directUsageProjectContextKey struct{}

func WithDirectUsageProject(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, directUsageProjectContextKey{}, strings.TrimSpace(projectID))
}

func directUsageProjectFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(directUsageProjectContextKey{}).(string)
	return strings.TrimSpace(value)
}

// CallAgentDirectNoTools calls the agent directly and explicitly suppresses
// tool/plugin execution. Use this for strict JSON-generation helpers.
func (s *LLMService) CallAgentDirectNoTools(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	return s.callAgentDirect(ctx, message, attachments, agent, workDir, true)
}

func (s *LLMService) callAgentDirect(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	return s.callAgentDirectWithDefinition(ctx, message, attachments, agent, workDir, nil, disableTools)
}

func (s *LLMService) callAgentDirectWithDefinition(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent, disableTools bool) (string, int, error) {
	return s.callAgentDirectWithDefinitionMode(ctx, message, attachments, agent, workDir, agentDef, disableTools, false)
}

func (s *LLMService) callAgentDirectWithDefinitionMode(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent, disableTools bool, rawDirectPrompt bool) (string, int, error) {
	if s.updateTracker != nil {
		done, err := s.updateTracker.Start(update.WorkChat)
		if err != nil {
			return "", 0, err
		}
		defer done()
	}
	applog.Infof("[agent-svc] CallAgentDirect agent=%s model=%s message_len=%d workDir=%s disable_tools=%v agent_def=%t", agent.Name, agent.Model, len(message), workDir, disableTools, agentDef != nil)

	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return "", 0, err
	}
	callCtx := ctx
	if agentDef != nil && AgentAllowsTool(agentDef, models.AgentToolScopedFiles) && len(agentDef.ToolConfig.ScopedFiles) > 0 && !disableTools {
		scopedWorkDir, rt, prepErr := s.directScopedFilesRuntime(ctx, agentDef, workDir)
		if prepErr != nil {
			return "", 0, prepErr
		}
		if scopedWorkDir != "" {
			workDir = scopedWorkDir
		}
		if rt != nil {
			rt = llmcontracts.TraceRuntimeTools(rt, llmcontracts.RuntimeToolTraceRecorderFromContext(callCtx))
			callCtx = llmcontracts.WithRuntimeTools(callCtx, llmcontracts.CompositeRuntimeTools(llmcontracts.RuntimeToolsFromContext(callCtx), rt))
		}
	}
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:               callCtx,
		Operation:         llmcontracts.OperationDirect,
		Message:           message,
		Attachments:       attachments,
		Agent:             agent,
		WorkDir:           workDir,
		DisableTools:      disableTools,
		RawDirectPrompt:   rawDirectPrompt,
		AgentDefinition:   agentDef,
		LifecycleHookCall: llmcontracts.LifecycleHookCallFromContext(ctx),
	})
	if err != nil {
		return "", 0, err
	}
	start := time.Now()
	res, err := adapter.Call(req)
	durationMs := time.Since(start).Milliseconds()
	status := string(models.ExecCompleted)
	errMsg := ""
	if err != nil {
		status = string(models.ExecFailed)
		errMsg = err.Error()
	}
	projectID := directUsageProjectFromContext(ctx)
	if projectID == "" {
		projectID = s.projectIDForWorkDir(context.Background(), workDir)
	}
	RecordUsageFromResult(context.Background(), s.usageRepo, UsageCapture{ProjectID: projectID, Operation: string(llmcontracts.OperationDirect), Status: status, ErrorMessage: errMsg, LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, res)
	if err != nil {
		if res.StopReason == "max_tokens" {
			return res.Output, res.Usage.TotalTokens, err
		}
		return "", 0, err
	}
	return res.Output, res.Usage.TotalTokens, nil
}

func (s *LLMService) projectIDForWorkDir(ctx context.Context, workDir string) string {
	if s == nil || s.projectRepo == nil || strings.TrimSpace(workDir) == "" {
		return ""
	}
	want := filepath.Clean(workDir)
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		applog.Infof("[usage] error resolving project for workDir=%s: %v", workDir, err)
		return ""
	}
	bestProjectID := ""
	bestRepoLen := -1
	for _, project := range projects {
		if !projectWorkDirMatches(project.RepoPath, want) {
			continue
		}
		repoLen := len(filepath.Clean(project.RepoPath))
		if repoLen > bestRepoLen {
			bestProjectID = project.ID
			bestRepoLen = repoLen
		}
	}
	return bestProjectID
}

func projectWorkDirMatches(repoPath string, workDir string) bool {
	repo := strings.TrimSpace(repoPath)
	if repo == "" || strings.TrimSpace(workDir) == "" {
		return false
	}
	repo = filepath.Clean(repo)
	want := filepath.Clean(workDir)
	if repo == want {
		return true
	}
	if rel, err := filepath.Rel(repo, want); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return true
	}
	worktreesDir := filepath.Join(repo, ".worktrees")
	if rel, err := filepath.Rel(worktreesDir, want); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return true
	}
	return false
}

func (s *LLMService) directScopedFilesRuntime(ctx context.Context, agentDef *models.Agent, workDir string) (string, *llmcontracts.RuntimeTools, error) {
	if agentDef == nil || len(agentDef.ToolConfig.ScopedFiles) == 0 {
		return workDir, nil, nil
	}
	root := strings.TrimSpace(workDir)
	if root == "" {
		root = "."
	}
	preparedWorkDir, rt, err := buildScopedFilesRuntimeTools(ctx, "", root, agentDef.ToolConfig)
	if err != nil {
		return "", nil, fmt.Errorf("preparing direct scoped file tools: %w", err)
	}
	if agentDef.ToolConfig.SkipDefaultTools && preparedWorkDir != "" {
		return preparedWorkDir, rt, nil
	}
	return workDir, rt, nil
}

// CallAgentDirectStreaming calls the agent with streaming support, writing output to DB in real-time.
// chatHistory provides prior conversation turns for context (nil for non-chat calls).
// chatSystemContext is optional additional context appended to the chat system prompt (e.g., task list).
// workDir is the project's repo path used as the working directory for model calls.
// isTaskFollowup when true uses the coding agent system prompt instead of task management prompt.
func (s *LLMService) CallAgentDirectStreamingDetailed(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, agentDef *models.Agent, isTaskFollowup ...bool) (llmcontracts.AgentResult, error) {
	followup := len(isTaskFollowup) > 0 && isTaskFollowup[0]
	applog.Infof("[agent-svc] CallAgentDirectStreaming agent=%s model=%s message_len=%d exec=%s history=%d workDir=%s isTaskFollowup=%v", agent.Name, agent.Model, len(message), execID, len(chatHistory), workDir, followup)
	chatMode := llmcontracts.ChatModeFromContext(ctx)
	if followup {
		chatMode = models.ChatModeOrchestrate
	}

	visionDecision := s.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, message, attachments, agent, "CallAgentDirectStreaming", "")
	agent = visionDecision.Agent
	applog.Infof("[agent-svc] CallAgentDirectStreaming vision routing changed=%v reason=%s detail=%q selected_agent=%s selected_provider=%s",
		visionDecision.Changed, visionDecision.Reason, visionDecision.Detail, agent.Name, agent.Provider)
	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	callCtx, lifecycleUserMessage := trackLifecycleCompletionUserMessage(ctx, message)
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:               callCtx,
		Operation:         llmcontracts.OperationStreaming,
		Message:           message,
		Attachments:       attachments,
		Agent:             agent,
		ExecID:            execID,
		TransportScope:    llmcontracts.TransportScopeFromContext(ctx),
		ChatHistory:       chatHistory,
		ChatMode:          chatMode,
		ChatSystemContext: combineAdditionalProjectInstructions(ctx, chatSystemContext),
		WorkDir:           workDir,
		AgentDefinition:   agentDef,
		Followup:          followup,
	})
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	res, err := adapter.Call(req)
	assistantOutput := res.TextOnlyOutput
	if assistantOutput == "" {
		assistantOutput = res.Output
	}
	res.ChatContext = lifecycleCompletionChatContext(lifecycleUserMessage(), assistantOutput)
	if err != nil {
		if res.StopReason == "max_tokens" {
			return res, err
		}
		return llmcontracts.AgentResult{ChatContext: res.ChatContext}, err
	}
	return res, nil
}

func (s *LLMService) CallAgentDirectStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup ...bool) (string, int, error) {
	res, err := s.CallAgentDirectStreamingDetailed(ctx, message, attachments, agent, execID, chatHistory, chatSystemContext, workDir, nil, isTaskFollowup...)
	return res.Output, res.Usage.TotalTokens, err
}

func loadRootProjectInstructions(repoDir string) string {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return ""
	}
	var parts []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(repoDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				applog.Infof("[agent-svc] warning: failed to read %s: %v", path, err)
			}
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("# %s\n\n%s", name, text))
	}
	return strings.Join(parts, "\n\n")
}

func combineProjectInstructions(parts ...string) string {
	var cleaned []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "\n\n")
}

func (s *LLMService) callLLM(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef ...*models.Agent) (string, string, int, error) {
	res, err := s.callLLMDetailed(ctx, prompt, attachments, agent, execID, workDir, projectInstructions, agentDef...)
	return res.Output, res.TextOnlyOutput, res.Usage.TotalTokens, err
}

func (s *LLMService) callLLMDetailed(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef ...*models.Agent) (llmcontracts.AgentResult, error) {
	applog.Infof("[agent-svc] callLLM provider=%s model=%s prompt_len=%d attachments=%d workDir=%s projectInstructions=%d", agent.Provider, agent.Model, len(prompt), len(attachments), workDir, len(projectInstructions))
	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	var ad *models.Agent
	if len(agentDef) > 0 {
		ad = agentDef[0]
	}
	callCtx, lifecycleUserMessage := trackLifecycleCompletionUserMessage(ctx, prompt)
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:                 callCtx,
		Operation:           llmcontracts.OperationTask,
		Message:             prompt,
		Attachments:         attachments,
		Agent:               agent,
		ExecID:              execID,
		WorkDir:             workDir,
		ProjectInstructions: projectInstructions,
		AgentDefinition:     ad,
	})
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	res, err := adapter.Call(req)
	assistantOutput := res.TextOnlyOutput
	if assistantOutput == "" {
		assistantOutput = res.Output
	}
	res.ChatContext = lifecycleCompletionChatContext(lifecycleUserMessage(), assistantOutput)
	if err != nil {
		// On max_tokens, return the partial output so callers can preserve it.
		// The error still propagates so the task is marked as failed.
		if res.StopReason == "max_tokens" {
			return res, err
		}
		return llmcontracts.AgentResult{ChatContext: res.ChatContext}, err
	}
	return res, nil
}

// lifecycleCompletionChatContext returns only the user/assistant pair completed
// by this call. Provider requests may still receive history when answering a
// follow-up, but after_complete hooks must not replay that history a second time.
func lifecycleCompletionChatContext(userMessage, assistantOutput string) llmcontracts.ChatContext {
	messages := make([]llmcontracts.ChatContextMessage, 0, 2)
	if userMessage = strings.TrimSpace(userMessage); userMessage != "" {
		messages = append(messages, llmcontracts.ChatContextMessage{Role: "user", Content: userMessage})
	}
	if strings.TrimSpace(assistantOutput) != "" {
		messages = append(messages, llmcontracts.ChatContextMessage{Role: "assistant", Content: assistantOutput})
	}
	return llmcontracts.ChatContext{Messages: messages}
}

func trackLifecycleCompletionUserMessage(ctx context.Context, fallback string) (context.Context, func() string) {
	latest := strings.TrimSpace(fallback)
	if explicit, ok := llmcontracts.LifecycleCompletionUserMessageFromContext(ctx); ok {
		latest = strings.TrimSpace(explicit)
	}
	var mu sync.Mutex
	current := func() string {
		mu.Lock()
		defer mu.Unlock()
		return latest
	}
	steering := llmcontracts.SteeringCallbackFromContext(ctx)
	if steering == nil {
		return ctx, current
	}
	tracked := func(callbackCtx context.Context) (string, error) {
		message, err := steering(callbackCtx)
		if err == nil {
			if latestMessage := strings.TrimSpace(message); latestMessage != "" {
				mu.Lock()
				latest = latestMessage
				mu.Unlock()
			}
		}
		return message, err
	}
	return llmcontracts.WithSteeringCallback(ctx, tracked), current
}

// statusOrNil returns a stringified status for logging, or "<nil>" when the
// task pointer is nil. Used by executeTaskWithAgent to log post-claim state.
func statusOrNil(t *models.Task) string {
	if t == nil {
		return "<nil>"
	}
	return string(t.Status)
}

// taskPreClaimedKey is a context key flagging that the worker has already
// transitioned the task to running before invoking the LLM service. When
// set, executeTaskWithAgent treats a failed ClaimTask as expected (because
// the worker did the claim) and proceeds with execution instead of skipping.
// This drives the live kanban update during lifecycle hooks — see
// WorkerService.executeTask for the corresponding pre-claim path.
type taskPreClaimedKey struct{}

// withTaskPreClaimed marks ctx so the LLM service knows the worker already
// claimed the task via TaskRepo.ClaimTask.
func withTaskPreClaimed(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskPreClaimedKey{}, true)
}

// isTaskPreClaimed reports whether ctx was tagged by the worker as having
// already claimed the task.
func isTaskPreClaimed(ctx context.Context) bool {
	v, _ := ctx.Value(taskPreClaimedKey{}).(bool)
	return v
}
