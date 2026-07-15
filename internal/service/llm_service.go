package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmnormalize "github.com/openvibely/openvibely/internal/llm/normalize"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

// buildAttachmentInstructionsForCLI is a helper that builds CLI-specific attachment
// instructions, separating text files (which can be read) from image files (which cannot).
// Exposed for testing.
func buildAttachmentInstructionsForCLI(attachments []models.Attachment) string {
	return llmprompt.BuildAttachmentInstructions(attachments)
}

// LLMCaller abstracts model provider calls so tests can inject a mock
// instead of hitting real APIs or spawning CLI subprocesses.
type LLMCaller interface {
	CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (output, textOnly string, tokens int, err error)
}

type LLMService struct {
	llmConfigRepo            *repository.LLMConfigRepo
	execRepo                 *repository.ExecutionRepo
	taskRepo                 *repository.TaskRepo
	projectRepo              *repository.ProjectRepo
	scheduleRepo             *repository.ScheduleRepo
	attachmentRepo           *repository.AttachmentRepo
	agentRepo                *repository.AgentRepo
	lifecycleRepo            *repository.LifecycleRepo
	mutationRecorder         func(models.Task) agentlibrary.MutationRecorder
	alertSvc                 *AlertService
	taskSvc                  *TaskService
	taskGoalSvc              *TaskGoalService
	worktreeSvc              *WorktreeService
	telegramSvc              *TelegramService
	slackSvc                 *SlackService
	discordSvc               *DiscordService
	llmCaller                LLMCaller
	providerAdapters         map[models.LLMProvider]ProviderAdapter
	routing                  *agentRoutingStrategy
	fileChangeBroadcaster    *events.FileChangeBroadcaster
	threadInputRepo          *repository.ThreadInputRepo
	usageRepo                *repository.UsageRepo
	skillAnalyticsRepo       *repository.SkillAnalyticsRepo
	broadcaster              *events.Broadcaster
	executionStreamHub       *events.ExecutionStreamHub
	queuedTaskThreadPromoter func(taskID string)
	channelMessageRouter     *ChannelMessageRouter
	githubIssueRuntime       GitHubIssueRuntimeProvider
	githubAuthRepo           *repository.GitHubAuthRepo
	taskPullRequestRepo      *repository.TaskPullRequestRepo
	githubPRFeedbackRepo     *repository.GitHubPRFeedbackRepo
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

// isTestMode detects whether the code is running inside a Go test binary.
// Checks both GO_TESTING env var (set by testutil.init()) and the binary name
// suffix (.test), which Go always uses for compiled test binaries.
func isTestMode() bool {
	if os.Getenv("GO_TESTING") != "" {
		return true
	}
	return strings.HasSuffix(os.Args[0], ".test") ||
		strings.Contains(os.Args[0], "/_test/")
}

// SetAlertService sets the alert service for creating alerts on task failures.
// Called after construction to avoid circular dependencies.
func (s *LLMService) SetAlertService(alertSvc *AlertService) {
	s.alertSvc = alertSvc
}

// SetTaskService sets the task service for creating tasks from agent output.
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

func (s *LLMService) SetBroadcaster(b *events.Broadcaster) {
	s.broadcaster = b
}

func (s *LLMService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
	s.initProviderAdapters()
}

func (s *LLMService) publishExecutionTerminal(execID string, status models.ExecutionStatus, errMsg string) {
	if s == nil || s.executionStreamHub == nil || execID == "" {
		return
	}
	event := events.ExecutionStreamEvent{ExecID: execID}
	switch status {
	case models.ExecCompleted:
		event.Type = events.ExecutionStreamDone
		event.Status = "completed"
	case models.ExecCancelled:
		event.Type = events.ExecutionStreamDone
		event.Status = "cancelled"
	case models.ExecFailed:
		event.Type = events.ExecutionStreamError
		event.Error = errMsg
	default:
		return
	}
	s.executionStreamHub.Close(execID, event)
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

func (s *LLMService) taskActionRuntimeTools(task models.Task) *llmcontracts.RuntimeTools {
	if s == nil {
		return nil
	}
	githubTools := buildGitHubIssueRuntimeTools(githubIssueRuntimeOptions{
		ProjectID:                task.ProjectID,
		ProjectRepo:              s.projectRepo,
		TaskRepo:                 s.taskRepo,
		TaskPullRequestRepo:      s.taskPullRequestRepo,
		GitHubPRFeedbackRepo:     s.githubPRFeedbackRepo,
		GitHubAuthRepo:           s.githubAuthRepo,
		ThreadInputRepo:          s.threadInputRepo,
		GitHub:                   s.githubIssueRuntime,
		AfterPRFeedbackForwarded: s.promoteQueuedTaskThreadAfterCompletion,
	})
	return llmcontracts.CompositeRuntimeTools(s.taskSendMessageRuntimeTools(task), s.taskControlRuntimeTools(task), githubTools)
}

func (s *LLMService) taskControlRuntimeTools(task models.Task) *llmcontracts.RuntimeTools {
	if s == nil || strings.TrimSpace(task.ProjectID) == "" {
		return nil
	}
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
	filtered := make([]llmcontracts.RuntimeToolDefinition, 0, 5)
	allowed := map[string]bool{
		"list_tasks":        true,
		"create_task":       true,
		"create_swarm_task": true,
		"set_task_goal":     true,
		"clear_task_goal":   true,
		"get_task_goal":     true,
		"pause_task_goal":   true,
		"resume_task_goal":  true,
		"schedule_task":     true,
		"delete_schedule":   true,
		"modify_schedule":   true,
		"list_capabilities": true,
	}
	for _, def := range defs {
		if allowed[strings.ToLower(strings.TrimSpace(def.Name))] {
			filtered = append(filtered, def)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID:     task.ProjectID,
		TaskSvc:       s.taskSvc,
		SwarmSvc:      nil,
		LLMConfigRepo: s.llmConfigRepo,
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{
		ProjectID:   task.ProjectID,
		TaskRepo:    s.taskRepo,
		TaskGoalSvc: s.taskGoalSvc,
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID:             task.ProjectID,
		TaskRepo:              s.taskRepo,
		ScheduleRepo:          s.scheduleRepo,
		LLMConfigRepo:         s.llmConfigRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          nil,
		CustomPersonalityRepo: nil,
		ProjectRepo:           s.projectRepo,
		AlertSvc:              s.alertSvc,
	}))
	handlers["list_capabilities"] = func(_ context.Context, _ json.RawMessage) (string, error) {
		return formatChannelCapabilities(chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)), nil
	}
	return &llmcontracts.RuntimeTools{
		Definitions: filtered,
		Executor:    chatcontrol.BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, handlers, allowed),
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
// In tests, pass a mock to prevent real API/CLI calls.
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

	// Create execution record
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error creating execution: %v", err)
		return nil, llmcontracts.ChatContext{}, fmt.Errorf("creating execution: %w", err)
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
	// current agent doesn't support vision (e.g., Anthropic CLI which can't
	// send images as multimodal content), try to find a vision-capable agent.
	// API key and OAuth agents support vision natively via multimodal content blocks.
	visionDecision := s.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, task.Prompt, attachments, agent, "ExecuteTaskWithAgent", task.ID)
	agent = visionDecision.Agent
	applog.Infof("[agent-svc] ExecuteTaskWithAgent vision routing changed=%v reason=%s detail=%q selected_agent=%s selected_provider=%s",
		visionDecision.Changed, visionDecision.Reason, visionDecision.Detail, agent.Name, agent.Provider)

	// Look up the project's repo path to use as the working directory
	// for the CLI subprocess. Without this, the agent runs in the OpenVibely
	// server directory instead of the project's configured directory.
	workDir := ""
	repoDir := "" // original repo dir (for worktree setup and post-execution)
	managedWorktree := false
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
	projectInstructions := combineProjectInstructions(additionalProjectInstructionsFromContext(ctx), loadRootProjectInstructions(repoDir))
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
		taskActionTools = s.taskActionRuntimeTools(task)
	}
	runtimeToolsActive := false
	if runtimeToolsSupported || agent.Provider != models.ProviderMixture {
		if ctxTools := llmcontracts.RuntimeToolsFromContext(callCtx); ctxTools != nil || runtimeTools != nil || taskActionTools != nil {
			mergedTools := llmcontracts.CompositeRuntimeTools(runtimeTools, ctxTools, taskActionTools)
			callCtx = llmcontracts.WithRuntimeTools(callCtx, mergedTools)
			runtimeToolsActive = mergedTools != nil && len(mergedTools.Definitions) > 0
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

	// Stop diff snapshot broadcaster
	if stopDiffBroadcast != nil {
		close(stopDiffBroadcast)
	}

	if err != nil {
		// Distinguish between user cancellation and actual failures.
		// When a task is cancelled, the context is cancelled which kills the CLI process.
		// Use background context for DB updates since the task context may be cancelled.
		bgCtx := context.Background()
		if ctx.Err() == context.Canceled {
			s.requeuePendingTaskSteeringForExecution(bgCtx, exec.ID)
			applog.Infof("[agent-svc] ExecuteTaskWithAgent CANCELLED task=%s duration=%dms",
				task.ID, durationMs)
			// Pass output (may contain partial streamed content) so Complete preserves it
			if completeErr := s.execRepo.Complete(bgCtx, exec.ID, models.ExecCancelled, output, "task cancelled by user", tokensUsed, durationMs); completeErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing cancelled execution: %v", completeErr)
			} else {
				s.publishExecutionTerminal(exec.ID, models.ExecCancelled, "task cancelled by user")
			}
			RecordUsageFromResult(bgCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecCancelled), ErrorMessage: "task cancelled by user", LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
			// Task status is already set to cancelled by CancelTask, but set it again
			// in case the cancellation came from a different path (e.g., server shutdown).
			if statusErr := s.taskRepo.UpdateStatus(bgCtx, task.ID, models.StatusCancelled); statusErr != nil {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to cancelled: %v", statusErr)
			}
			exec.Status = models.ExecCancelled
			exec.ErrorMessage = "task cancelled by user"
			s.promoteQueuedTaskThreadAfterCompletion(task.ID)
			return exec, result.ChatContext, fmt.Errorf("task cancelled")
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
	// codes and fail the task. This was removed because both the CLI path (Claude Code)
	// and OAuth agentic path handle tool errors internally — the model sees the error,
	// can retry or fix the issue, and continues working. Intermediate command failures
	// should not kill the task. The model uses [STATUS: FAILED | reason] to explicitly
	// report task failure when it determines the task cannot be completed.

	// Process task creation markers from the agent's output.
	// Webhook-created tasks must remain one-task-per-webhook-call, so do not
	// allow marker-driven fan-out during their execution.
	if s.taskSvc != nil {
		if runtimeToolsActive {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent skipping marker task creation because runtime tools are active task=%s", task.ID)
		} else if task.CreatedVia == models.TaskOriginWebhook {
			applog.Infof("[agent-svc] ExecuteTaskWithAgent skipping marker task creation for webhook-origin task=%s", task.ID)
		} else {
			taskRequests := ParseTaskCreations(output)
			if len(taskRequests) > 0 {
				applog.Infof("[agent-svc] ExecuteTaskWithAgent task=%s found %d task creation requests", task.ID, len(taskRequests))
				summary := ExecuteTaskCreations(finalizeCtx, taskRequests, task.ProjectID, s.taskSvc)
				if summary != "" {
					output += summary
				}
			}
		}
	}

	// Record success
	completedExecution := false
	if completeErr := s.execRepo.Complete(finalizeCtx, exec.ID, models.ExecCompleted, output, "", tokensUsed, durationMs); completeErr != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
	} else {
		completedExecution = true
	}
	RecordUsageFromResult(finalizeCtx, s.usageRepo, UsageCapture{ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, TurnID: exec.ID, Operation: string(llmcontracts.OperationTask), Status: string(models.ExecCompleted), LatencyMs: durationMs, OccurredAt: time.Now().UTC()}, agent, result)
	if statusErr := s.taskRepo.UpdateStatus(finalizeCtx, task.ID, models.StatusCompleted); statusErr != nil {
		applog.Infof("[agent-svc] ExecuteTaskWithAgent error updating task status to completed: %v", statusErr)
	}
	if completedExecution {
		s.publishExecutionTerminal(exec.ID, models.ExecCompleted, "")
	}

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
		s.worktreeSvc.HandlePostExecution(finalizeCtx, &task, repoDir)
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
	output, _, err := s.CallAgentDirectNoTools(summaryCtx, prompt, nil, agent, worktreePath)
	if err != nil {
		applog.Infof("[agent-svc] commit diff summary failed worktree=%s: %v", worktreePath, err)
		return ""
	}
	for _, summary := range summarizeCommitIntentLines(output) {
		if summary != "" {
			return summary
		}
	}
	return ""
}

func buildWorktreeCommitSummaryPrompt(diffContext string, commitCtx WorktreeCommitMessageContext) string {
	var b strings.Builder
	b.WriteString("Write one concise git commit subject for these worktree changes.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Describe what actually changed in the diff, not which files were edited.\n")
	b.WriteString("- Use an imperative, capitalized subject, for example: Add analytics chart.\n")
	b.WriteString("- Use plain language with no conventional prefix such as feat:, fix:, chore:, docs:, or test:.\n")
	b.WriteString("- Do not mention tasks, task turns, follow-ups, lifecycle phases, worktrees, or file lists unless that is literally the product code being changed.\n")
	b.WriteString("- Return only the subject line, max 72 characters.\n")
	b.WriteString("- If supporting context conflicts with the diff, ignore the supporting context.\n\n")
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
	if err := CommitWorktreeChanges(task.WorktreePath, commitMessage); err != nil {
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

// CallAgentDirectNoTools calls the agent directly and explicitly suppresses
// tool/plugin execution. Use this for strict JSON-generation helpers.
func (s *LLMService) CallAgentDirectNoTools(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	return s.callAgentDirect(ctx, message, attachments, agent, workDir, true)
}

func (s *LLMService) callAgentDirect(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	return s.callAgentDirectWithDefinition(ctx, message, attachments, agent, workDir, nil, disableTools)
}

func (s *LLMService) callAgentDirectWithDefinition(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent, disableTools bool) (string, int, error) {
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
		Ctx:             callCtx,
		Operation:       llmcontracts.OperationDirect,
		Message:         message,
		Attachments:     attachments,
		Agent:           agent,
		WorkDir:         workDir,
		DisableTools:    disableTools,
		AgentDefinition: agentDef,
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
	projectID := s.projectIDForWorkDir(context.Background(), workDir)
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
// workDir is the project's repo path used as the working directory for CLI subprocesses.
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
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:               ctx,
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
	res.ChatContext = chatContextFromNormalizedRequest(req, assistantOutput)
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
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:                 ctx,
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
	res.ChatContext = chatContextFromNormalizedRequest(req, assistantOutput)
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

func chatContextFromNormalizedRequest(req llmcontracts.AgentRequest, assistantOutput string) llmcontracts.ChatContext {
	messages := make([]llmcontracts.ChatContextMessage, 0, len(req.ChatHistory)*2+2)
	for _, turn := range req.ChatHistory {
		if strings.TrimSpace(turn.PromptSent) != "" {
			messages = append(messages, llmcontracts.ChatContextMessage{Role: "user", Content: turn.PromptSent})
		}
		if replay := llmprompt.ReplayAssistantContent(turn); replay != "" {
			messages = append(messages, llmcontracts.ChatContextMessage{Role: "assistant", Content: replay})
		}
	}
	if strings.TrimSpace(req.Message) != "" {
		messages = append(messages, llmcontracts.ChatContextMessage{Role: "user", Content: req.Message})
	}
	if strings.TrimSpace(assistantOutput) != "" {
		messages = append(messages, llmcontracts.ChatContextMessage{Role: "assistant", Content: assistantOutput})
	}
	return llmcontracts.ChatContext{Messages: messages}
}

func (s *LLMService) callAnthropic(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig) (string, int, error) {
	applog.Infof("[agent-svc] callAnthropic model=%s temp=%.1f attachments=%d",
		agent.Model, agent.Temperature, len(attachments))

	client := anthropicclient.NewWithAPIKey(agent.APIKey)

	// Convert attachments to anthropicclient format
	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	// If there are non-image attachments, mention them in the prompt
	fullPrompt := prompt
	if len(attachments) > 0 {
		attachmentInfo := "\n\nYou have been provided with the following attached files:\n"
		for _, att := range attachments {
			attachmentInfo += fmt.Sprintf("- %s\n", att.FileName)
		}
		attachmentInfo += "\nPlease examine these files as part of your task.\n"
		fullPrompt += attachmentInfo
	}

	resp, err := client.Send(ctx, fullPrompt, &anthropicclient.SendOptions{
		Model:       agent.Model,
		MaxTokens:   anthropicDirectOutputBudget,
		Attachments: mcAttachments,
	})
	if err != nil {
		applog.Infof("[agent-svc] callAnthropic API error: %v", err)
		return "", 0, fmt.Errorf("anthropic API call: %w", err)
	}

	tokensUsed := resp.InputTokens + resp.OutputTokens
	applog.Infof("[agent-svc] callAnthropic success input_tokens=%d output_tokens=%d stop_reason=%s",
		resp.InputTokens, resp.OutputTokens, resp.StopReason)

	return resp.Text, tokensUsed, nil
}

// callAnthropicChat is the chat-specific variant of callAnthropic.
// It includes a system prompt with task context and conversation history.
// Image attachments are sent as proper multimodal content blocks instead of text.
// Uses anthropicclient for retries, connection pooling, and streaming.
func (s *LLMService) callAnthropicChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	applog.Infof("[agent-svc] callAnthropicChat model=%s history=%d message_len=%d context_len=%d attachments=%d exec=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), execID, isTaskFollowup, chatMode)

	client := anthropicclient.NewWithAPIKey(agent.APIKey)

	// Build the system prompt based on whether this is a task followup or orchestration chat
	// Anthropic API agents don't need tool restrictions (restrictTools=false)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	client.History = append(client.History, buildAnthropicClientHistory(chatHistory)...)

	// Convert attachments to anthropicclient format
	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", s.execRepo, ctx, 500*time.Millisecond, s.executionStreamHub)
	defer sw.Stop()

	chatInThinking := false
	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan
	opts := &anthropicclient.AgenticOptions{
		Model:          agent.Model,
		MaxTokens:      anthropicAgenticOutputBudget,
		EnableThinking: true,
		DisableTools:   disableTools,
		System:         systemPromptStr,
		Attachments:    mcAttachments,
		AutoCompaction: true,
		OnThinking: func(text string) {
			if !chatInThinking {
				chatInThinking = true
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingOpen}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: text}, false)
		},
		OnText: func(text string) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
		OnCompaction: func(summary string) {
			applog.Infof("[agent-svc] callAnthropicChat context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, message, opts)
	if err != nil {
		sw.Flush()
		applog.Infof("[agent-svc] callAnthropicChat error: %v", err)
		return "", 0, fmt.Errorf("anthropic API streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	tokensUsed := resp.InputTokens + resp.OutputTokens
	applog.Infof("[agent-svc] callAnthropicChat success input_tokens=%d output_tokens=%d output_len=%d tools=%d stop=%s", resp.InputTokens, resp.OutputTokens, len(output), len(resp.ToolCalls), resp.StopReason)
	if resp.StopReason == "max_tokens" {
		return output, tokensUsed, errMaxTokens
	}
	return output, tokensUsed, nil
}

func (s *LLMService) callClaudeCLI(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, pluginDirs []string, agentDef ...*models.Agent) (string, string, int, error) {
	applog.Infof("[agent-svc] callClaudeCLI model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", "", 0, fmt.Errorf("callClaudeCLI blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	// Find the claude binary
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLI 'claude' not found in PATH: %v", err)
		return "", "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}
	applog.Infof("[agent-svc] callClaudeCLI using binary: %s", claudePath)

	var fullPrompt strings.Builder
	fullPrompt.WriteString(llmprompt.BuildTaskPromptHeader())
	if worktreeContext := llmprompt.BuildWorktreeContextSentence(workDir); worktreeContext != "" {
		fullPrompt.WriteString(worktreeContext)
		fullPrompt.WriteString("\n\n")
	}
	if strings.TrimSpace(projectInstructions) != "" {
		fullPrompt.WriteString(strings.TrimSpace(projectInstructions))
		fullPrompt.WriteString("\n\n")
	}
	fullPrompt.WriteString(llmprompt.BuildAttachmentInstructions(attachments))
	fullPrompt.WriteString(prompt)

	// Add task creation instructions so the agent can create sub-tasks
	fullPrompt.WriteString("\n\n")
	fullPrompt.WriteString(llmprompt.TaskCreationInstructions)

	// Append status reporting instructions AFTER the task prompt so the agent
	// sees them last and is more likely to follow them.
	fullPrompt.WriteString("\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response.")

	// Build command: -p reads prompt from stdin, stream-json gives us JSON events,
	// --include-partial-messages gives us token-level streaming for real-time output
	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if effort := claudeEffort(agent.ReasoningEffort); effort != "" {
		args = append(args, "--effort", effort)
	}
	for _, dir := range pluginDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		args = append(args, "--plugin-dir", dir)
	}

	applog.Infof("[agent-svc] callClaudeCLI executing: claude %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callClaudeCLI using workDir=%s", workDir)
	}

	// Write agent definition files (agent.md, skills, .mcp.json) if present
	var ad *models.Agent
	if len(agentDef) > 0 {
		ad = agentDef[0]
	}
	if ad != nil && workDir != "" {
		cleanup, writeErr := WriteAgentFiles(workDir, ad)
		if writeErr != nil {
			applog.Infof("[agent-svc] callClaudeCLI error writing agent files: %v", writeErr)
		} else {
			defer cleanup()
			applog.Infof("[agent-svc] callClaudeCLI wrote agent definition files for %q", ad.Name)
		}
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	// Pass prompt via stdin for streaming output
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	// Get stdout pipe for reading JSON stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLI error creating stdout pipe: %v", err)
		return "", "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Use streaming writer for real-time DB updates.
	// The background periodic flush ensures output is visible even during
	// long pauses (e.g., while a tool is running).
	sw := llmstream.NewWriterWithPublisher(execID, "", s.execRepo, ctx, 500*time.Millisecond, s.executionStreamHub)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Start the command
	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callClaudeCLI error starting command: %v", err)
		return "", "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	// Parse JSON stream in a goroutine and extract text content
	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseJSONStream(stdoutPipe, sw, false)
	}()

	// Wait for command to finish
	err = cmd.Wait()

	// Wait for parsing to complete
	if pErr := <-parseErr; pErr != nil {
		applog.Infof("[agent-svc] callClaudeCLI JSON parsing error: %v", pErr)
	}

	// Flush any remaining output to the DB
	sw.Flush()

	if err != nil {
		errOutput := stderr.String()
		applog.Infof("[agent-svc] callClaudeCLI error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	// Check if the CLI result event reported an error (e.g., max turns exceeded).
	// The CLI may exit 0 even when it reports is_error=true in the result event.
	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		applog.Infof("[agent-svc] callClaudeCLI result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, "", 0, fmt.Errorf("claude CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	textOnly := sw.TextString()
	applog.Infof("[agent-svc] callClaudeCLI success output_len=%d text_only_len=%d", len(output), len(textOnly))

	// CLI doesn't report token counts, so we return 0
	return output, textOnly, 0, nil
}

// callClaudeCLIChat is the chat-specific variant of callClaudeCLI.
// It builds a lightweight prompt with conversation history and no task-execution
// directives (no AGENTS.md, no STATUS markers).
func (s *LLMService) callClaudeCLIChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup bool, chatMode models.ChatMode, pluginDirs []string) (string, int, error) {
	applog.Infof("[agent-svc] callClaudeCLIChat model=%s history=%d message_len=%d context_len=%d attachments=%d workDir=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), workDir, isTaskFollowup, chatMode)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callClaudeCLIChat blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLIChat 'claude' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}

	// Check if we have a CLI session ID from a prior chat turn to resume
	var lastSessionID string
	for i := len(chatHistory) - 1; i >= 0; i-- {
		if chatHistory[i].CliSessionID != "" {
			lastSessionID = chatHistory[i].CliSessionID
			break
		}
	}

	// Build prompt — just the system prompt + current message (no manual history).
	// If resuming a session, the CLI manages its own conversation state.
	var fullPrompt strings.Builder
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, true)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	fullPrompt.WriteString(systemPromptStr)
	fullPrompt.WriteString("\n")

	if lastSessionID == "" {
		// First message — no session to resume, include history text as context
		fullPrompt.WriteString(llmprompt.BuildChatHistoryText(chatHistory))
	}

	fullPrompt.WriteString(message)

	// Pass attachments - separate handling for text vs images
	if len(attachments) > 0 {
		var textFiles []models.Attachment
		var imageFiles []models.Attachment

		for _, att := range attachments {
			if strings.HasPrefix(strings.ToLower(att.MediaType), "image/") {
				imageFiles = append(imageFiles, att)
			} else {
				textFiles = append(textFiles, att)
			}
		}

		// Text files can be read normally
		if len(textFiles) > 0 {
			fullPrompt.WriteString("\n\n[The user attached the following files:\n")
			for _, att := range textFiles {
				absPath := llmprompt.AttachmentAbsPath(att)
				fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			}
			fullPrompt.WriteString("You can read these files using your Read tool.]")
		}

		// Image files cannot be viewed in CLI mode
		if len(imageFiles) > 0 {
			fullPrompt.WriteString("\n\n[NOTE: The user attached the following image files, but you cannot view them because you are running in CLI mode without vision support:\n")
			for _, att := range imageFiles {
				absPath := llmprompt.AttachmentAbsPath(att)
				fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			}
			fullPrompt.WriteString("\nPlease inform the user that you cannot analyze images in CLI mode and suggest they reconfigure to use a vision-capable model (Anthropic API or OpenAI API with an API key or OAuth).]")
		}
	}

	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if !isTaskFollowup && chatMode == models.ChatModePlan {
		args = append(args, "--permission-mode", "plan")
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if effort := claudeEffort(agent.ReasoningEffort); effort != "" {
		args = append(args, "--effort", effort)
	}
	if !(chatMode == models.ChatModePlan && !isTaskFollowup) {
		for _, dir := range pluginDirs {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			args = append(args, "--plugin-dir", dir)
		}
	}
	// Resume the CLI session if we have one from a prior chat turn
	if lastSessionID != "" {
		args = append(args, "--resume", lastSessionID)
		applog.Infof("[agent-svc] callClaudeCLIChat resuming session=%s", lastSessionID)
	}

	applog.Infof("[agent-svc] callClaudeCLIChat executing: claude %s (prompt via stdin, len=%d)", strings.Join(args, " "), fullPrompt.Len())

	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callClaudeCLIChat using workDir=%s", workDir)
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLIChat error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", s.execRepo, ctx, 500*time.Millisecond, s.executionStreamHub)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callClaudeCLIChat error starting command: %v", err)
		return "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()

	if pErr := <-parseErr; pErr != nil {
		applog.Infof("[agent-svc] callClaudeCLIChat JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := stderr.String()
		applog.Infof("[agent-svc] callClaudeCLIChat error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		applog.Infof("[agent-svc] callClaudeCLIChat result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("claude CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()

	// Persist the CLI session ID so subsequent chat calls can --resume
	sid := sw.SessionID()
	if sid != "" && s.execRepo != nil {
		if err := s.execRepo.UpdateCliSessionID(ctx, execID, sid); err != nil {
			applog.Infof("[agent-svc] callClaudeCLIChat error persisting session_id: %v", err)
		} else {
			applog.Infof("[agent-svc] callClaudeCLIChat persisted session_id=%s for exec=%s", sid, execID)
		}
	}

	applog.Infof("[agent-svc] callClaudeCLIChat success output_len=%d session_id=%s", len(output), sid)
	return output, 0, nil
}

func claudeEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func prependDirectNoToolsInstruction(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	prefix := "IMPORTANT: Do not execute any tools, plugins, MCP actions, or shell commands for this request. Reply directly with plain text only."
	if prompt == "" {
		return prefix
	}
	return prefix + "\n\n" + prompt
}

func (s *LLMService) callClaudeCLISimple(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	applog.Infof("[agent-svc] callClaudeCLISimple model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callClaudeCLISimple blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	// Find the claude binary
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLISimple 'claude' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}
	applog.Infof("[agent-svc] callClaudeCLISimple using binary: %s", claudePath)

	// Build command with streaming JSON output
	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if effort := claudeEffort(agent.ReasoningEffort); effort != "" {
		args = append(args, "--effort", effort)
	}

	applog.Infof("[agent-svc] callClaudeCLISimple executing: claude %s (prompt via stdin)", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callClaudeCLISimple using workDir=%s", workDir)
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	fullPrompt := prompt
	if disableTools {
		fullPrompt = prependDirectNoToolsInstruction(prompt)
	}

	// Pass prompt via stdin
	cmd.Stdin = strings.NewReader(fullPrompt)

	// Get stdout pipe for reading JSON stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callClaudeCLISimple error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Use a simple buffer writer for collecting output
	var outputBuf bytes.Buffer

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Start the command
	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callClaudeCLISimple error starting command: %v", err)
		return "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	// Parse JSON stream and collect text
	scanner := bufio.NewScanner(stdoutPipe)
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		eventType, hasType := event["type"].(string)
		if hasType {
			switch eventType {
			case "content_block_delta":
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					if dt, _ := delta["type"].(string); dt == "text_delta" {
						if text, ok := delta["text"].(string); ok && text != "" {
							outputBuf.WriteString(text)
						}
					}
				}
			case "content_block_start":
				if cb, ok := event["content_block"].(map[string]interface{}); ok {
					if bt, _ := cb["type"].(string); bt == "tool_use" {
						if name, ok := cb["name"].(string); ok && name != "" {
							outputBuf.WriteString(fmt.Sprintf("\n[Using tool: %s]\n", name))
						}
					}
				}
			case "result":
				if result, ok := event["result"].(string); ok && result != "" {
					if outputBuf.Len() == 0 {
						outputBuf.WriteString(result)
					}
				}
			}
		}
	}

	// Wait for command to finish
	err = cmd.Wait()

	if err != nil {
		errOutput := stderr.String()
		applog.Infof("[agent-svc] callClaudeCLISimple error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	output := outputBuf.String()
	applog.Infof("[agent-svc] callClaudeCLISimple success output_len=%d", len(output))

	return output, 0, nil
}

func (s *LLMService) callCodexCLI(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	applog.Infof("[agent-svc] callCodexCLI model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", "", 0, fmt.Errorf("callCodexCLI blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLI 'codex' not found in PATH: %v", err)
		return "", "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}
	applog.Infof("[agent-svc] callCodexCLI using binary: %s", codexPath)

	var fullPrompt strings.Builder
	fullPrompt.WriteString(llmprompt.BuildTaskPromptHeader())
	if worktreeContext := llmprompt.BuildWorktreeContextSentence(workDir); worktreeContext != "" {
		fullPrompt.WriteString(worktreeContext)
		fullPrompt.WriteString("\n\n")
	}
	fullPrompt.WriteString(llmprompt.BuildAttachmentInstructions(attachments))
	fullPrompt.WriteString(prompt)

	imagePaths := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if llmoutput.IsImageMediaType(att.MediaType) {
			imagePaths = append(imagePaths, llmprompt.AttachmentAbsPath(att))
		}
	}
	fullPrompt.WriteString("\n\n")
	fullPrompt.WriteString(llmprompt.TaskCreationInstructions)
	fullPrompt.WriteString("\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response.")

	args := llmprompt.CodexExecArgs(agent.Model, agent.ReasoningEffort, imagePaths)
	applog.Infof("[agent-svc] callCodexCLI executing: codex %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callCodexCLI using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLI error creating stdout pipe: %v", err)
		return "", "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", s.execRepo, ctx, 500*time.Millisecond, s.executionStreamHub)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callCodexCLI error starting command: %v", err)
		return "", "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, false)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		applog.Infof("[agent-svc] callCodexCLI JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		applog.Infof("[agent-svc] callCodexCLI error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		applog.Infof("[agent-svc] callCodexCLI result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, "", 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	textOnly := sw.TextString()
	applog.Infof("[agent-svc] callCodexCLI success output_len=%d text_only_len=%d", len(output), len(textOnly))
	return output, textOnly, 0, nil
}

func (s *LLMService) callCodexCLIChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	applog.Infof("[agent-svc] callCodexCLIChat model=%s history=%d message_len=%d context_len=%d attachments=%d workDir=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), workDir, isTaskFollowup, chatMode)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callCodexCLIChat blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLIChat 'codex' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}

	// Check for a prior Codex thread ID to resume
	var lastThreadID string
	for i := len(chatHistory) - 1; i >= 0; i-- {
		if chatHistory[i].CliSessionID != "" {
			lastThreadID = chatHistory[i].CliSessionID
			break
		}
	}

	var fullPrompt strings.Builder
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, true)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	fullPrompt.WriteString(systemPromptStr)
	fullPrompt.WriteString("\n")

	if lastThreadID == "" {
		// First message — no thread to resume, include history text as context
		fullPrompt.WriteString(llmprompt.BuildChatHistoryText(chatHistory))
	}

	fullPrompt.WriteString(message)

	imagePaths := make([]string, 0, len(attachments))
	if len(attachments) > 0 {
		fullPrompt.WriteString("\n\n[The user attached files. Use the absolute paths below when needed:\n")
		for _, att := range attachments {
			absPath := llmprompt.AttachmentAbsPath(att)
			fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			if llmoutput.IsImageMediaType(att.MediaType) {
				imagePaths = append(imagePaths, absPath)
			}
		}
		fullPrompt.WriteString("]")
	}

	var args []string
	if lastThreadID != "" {
		// Resume existing thread — codex manages its own history
		args = llmprompt.CodexResumeArgs(agent.Model, agent.ReasoningEffort, lastThreadID, imagePaths, chatMode)
		applog.Infof("[agent-svc] callCodexCLIChat resuming thread=%s", lastThreadID)
	} else {
		args = llmprompt.CodexChatArgs(agent.Model, agent.ReasoningEffort, imagePaths, chatMode)
	}
	applog.Infof("[agent-svc] callCodexCLIChat executing: codex %s (prompt via stdin, len=%d)", strings.Join(args, " "), fullPrompt.Len())

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callCodexCLIChat using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLIChat error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", s.execRepo, ctx, 500*time.Millisecond, s.executionStreamHub)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callCodexCLIChat error starting command: %v", err)
		return "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		applog.Infof("[agent-svc] callCodexCLIChat JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		applog.Infof("[agent-svc] callCodexCLIChat error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		applog.Infof("[agent-svc] callCodexCLIChat result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()

	// Persist the Codex thread ID so subsequent chat calls can resume
	tid := sw.SessionID()
	if tid != "" && s.execRepo != nil {
		if err := s.execRepo.UpdateCliSessionID(ctx, execID, tid); err != nil {
			applog.Infof("[agent-svc] callCodexCLIChat error persisting thread_id: %v", err)
		} else {
			applog.Infof("[agent-svc] callCodexCLIChat persisted thread_id=%s for exec=%s", tid, execID)
		}
	}

	applog.Infof("[agent-svc] callCodexCLIChat success output_len=%d thread_id=%s", len(output), tid)
	return output, 0, nil
}

func (s *LLMService) callCodexCLISimple(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	applog.Infof("[agent-svc] callCodexCLISimple model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callCodexCLISimple blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLISimple 'codex' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}

	fullPrompt := strings.TrimSpace(prompt)
	if disableTools {
		fullPrompt = prependDirectNoToolsInstruction(fullPrompt)
	}
	imagePaths := make([]string, 0, len(attachments))
	if len(attachments) > 0 {
		fullPrompt += "\n\nAttached files:\n"
		for _, att := range attachments {
			absPath := llmprompt.AttachmentAbsPath(att)
			fullPrompt += fmt.Sprintf("- %s (absolute path: %s)\n", att.FileName, absPath)
			if llmoutput.IsImageMediaType(att.MediaType) {
				imagePaths = append(imagePaths, absPath)
			}
		}
	}

	args := llmprompt.CodexExecArgs(agent.Model, agent.ReasoningEffort, imagePaths)
	applog.Infof("[agent-svc] callCodexCLISimple executing: codex %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		applog.Infof("[agent-svc] callCodexCLISimple using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		applog.Infof("[agent-svc] callCodexCLISimple error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriter("", "", nil, ctx, time.Hour)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		applog.Infof("[agent-svc] callCodexCLISimple error starting command: %v", err)
		return "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		applog.Infof("[agent-svc] callCodexCLISimple JSON parsing error: %v", pErr)
	}

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		applog.Infof("[agent-svc] callCodexCLISimple error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		applog.Infof("[agent-svc] callCodexCLISimple result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	applog.Infof("[agent-svc] callCodexCLISimple success output_len=%d", len(output))
	return output, 0, nil
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
