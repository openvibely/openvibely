package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type channelChatAttachmentLinkOptions struct {
	Platform     string
	UploadsDir   string
	Repo         *repository.ChatAttachmentRepo
	FallbackName string
}

type channelChatIngressQueueOptions struct {
	Platform                string
	ProjectID               string
	ActiveExecID            string
	AgentID                 string
	Message                 string
	Source                  string
	AttachmentSessionID     string
	UploadsDir              string
	BroadcastHasAttachments bool

	ThreadInputRepo *repository.ThreadInputRepo
	ChatBroadcaster *events.ChatBroadcaster

	NewThreadInput        func() *models.ThreadInput
	CreateQueuedInput     func(context.Context, *models.ThreadInput) (bool, error)
	OnQueueFailure        func(context.Context)
	OnQueued              func(context.Context)
	OnDurableHandoff      func()
	CleanupPendingSession func(context.Context, string)
}

type channelTaskThreadSendOptions struct {
	Platform                   string
	ProjectID                  string
	TaskID                     string
	Title                      string
	Message                    string
	Source                     string
	Surface                    chatcontrol.Surface
	ReplyContext               ChannelReplyContext
	TaskRepo                   *repository.TaskRepo
	ExecRepo                   *repository.ExecutionRepo
	ThreadInputRepo            *repository.ThreadInputRepo
	LLMConfigRepo              *repository.LLMConfigRepo
	SettingsRepo               *repository.SettingsRepo
	CustomPersonalityRepo      *repository.CustomPersonalityRepo
	ChannelTaskRunner          ChannelTaskRunner
	QueuedTaskThreadPromoter   func(taskID string)
	CompleteExecution          func(context.Context, string, string, string, string, int, int64)
	NewQueuedInput             func(*models.Task, string, string) *models.ThreadInput
	FilterHistory              func([]models.Execution, string) []models.Execution
	OnBindQueuedInputSkipped   func(context.Context, *models.Task, *models.ThreadInput, error)
	OnPromotionRecheckSkipped  func(context.Context, *models.Task, *models.ThreadInput, error)
	QueueUnavailableResult     func(*models.Task) string
	ErrorResult                func(string, ...any) string
	QueueErrorResult           func(*models.Task, error) string
	ActiveLookupErrorResult    func(*models.Task, error) string
	AgentSelectionErrorResult  func(*models.Task, error) string
	ExecutionCreateErrorResult func(*models.Task, error) string
	RunnerUnavailableResult    func(*models.Task, string) string
	QueuedResult               func(*models.Task) string
	StartedResult              func(*models.Task) string
}

type channelChatIngressFirstTurnOptions struct {
	Platform          string
	ProjectID         string
	Message           string
	Source            string
	Task              *models.Task
	Agent             *models.LLMConfig
	Attachments       []models.ChatAttachment
	AttachmentContext string
	ImageAttachments  []models.Attachment
	HasAttachments    bool
	Surface           chatcontrol.Surface
	ReplyContext      ChannelReplyContext
	InitialAckID      int
	Start             time.Time
	ChatHistoryLimit  int

	// RuntimeTools holds channel-specific runtime tools to pass to the handler
	// runner. RuntimeToolsForTask is preferred when tool handlers require the
	// persisted channel chat task identity and is invoked only after task creation.
	RuntimeTools        *llmcontracts.RuntimeTools
	RuntimeToolsForTask func(taskID string) *llmcontracts.RuntimeTools

	TaskRepo           *repository.TaskRepo
	ExecRepo           *repository.ExecutionRepo
	ChatAttachmentRepo *repository.ChatAttachmentRepo
	ChatBroadcaster    *events.ChatBroadcaster
	ChannelChatRunner  ChannelChatRunner

	CreateTaskContext          func(context.Context, string) error
	CreateExecution            func(context.Context, *models.Execution) (bool, error)
	PrepareDurableAttachments  func(context.Context, string, []models.ChatAttachment) ([]models.ChatAttachment, error)
	CreateDurableFirstTurn     func(context.Context, *models.Task, *models.Execution, []models.ChatAttachment) (bool, error)
	CleanupProvisionalTask     func(context.Context, string, string)
	CleanupAttachmentSources   func(context.Context, []models.ChatAttachment)
	CompleteExecution          func(context.Context, string, string, string, string, int, int64)
	LinkAttachments            func(context.Context, string, []models.ChatAttachment) ([]models.ChatAttachment, error)
	AttachmentContextAndImages func([]models.ChatAttachment) (string, []models.Attachment)
	ListChatHistory            func(context.Context, string) ([]models.Execution, error)
	FilterChatHistory          func([]models.Execution, string) []models.Execution
	TaskSvc                    *TaskService
	LLMConfigRepo              *repository.LLMConfigRepo
	ScheduleRepo               *repository.ScheduleRepo
	AgentRepo                  *repository.AgentRepo
	SettingsRepo               *repository.SettingsRepo
	CustomPersonalityRepo      *repository.CustomPersonalityRepo
	ProjectRepo                *repository.ProjectRepo
	OnTaskCreateFailure        func(context.Context)
	OnTaskContextFailure       func(context.Context)
	OnExecutionCreateFailure   func(context.Context)
	OnDurableHandoff           func()
	OnAttachmentLinkFailure    func(context.Context, string)
	PrepareRunner              func(context.Context, string, string) int
	OnRunnerUnavailable        func(context.Context, string, int)
}

type channelChatIngressDownloadResult struct {
	AttachmentContext string
	ImageAttachments  []models.Attachment
	ChatAttachments   []models.ChatAttachment
}

type channelChatIngressOptions struct {
	Platform       string
	ProjectID      string
	Message        string
	Source         string
	Surface        chatcontrol.Surface
	HasAttachments bool
	Start          time.Time

	TaskRepo              *repository.TaskRepo
	ExecRepo              *repository.ExecutionRepo
	ThreadInputRepo       *repository.ThreadInputRepo
	LLMConfigRepo         *repository.LLMConfigRepo
	ScheduleRepo          *repository.ScheduleRepo
	AgentRepo             *repository.AgentRepo
	SettingsRepo          *repository.SettingsRepo
	CustomPersonalityRepo *repository.CustomPersonalityRepo
	ProjectRepo           *repository.ProjectRepo
	TaskSvc               *TaskService
	ChatBroadcaster       *events.ChatBroadcaster
	UploadsDir            string

	DownloadAttachments                       func(context.Context) (channelChatIngressDownloadResult, error)
	ContinueWithoutAttachmentsOnDownloadError bool
	IncomingAttachmentsNeedVision             func() bool
	SavePendingAttachments                    func([]models.ChatAttachment) (string, error)
	SavePendingAttachmentsWithContext         func(context.Context, []models.ChatAttachment) (string, error)
	CleanupAttachmentSources                  func(context.Context, []models.ChatAttachment)
	CleanupPendingSession                     func(context.Context, string)
	SelectionMessage                          string
	FindActiveExecution                       func(context.Context, string) (*models.Execution, error)
	RecordAttachmentFailure                   func(context.Context, string, string)
	NewQueuedInput                            func() *models.ThreadInput
	CreateQueuedInput                         func(context.Context, *models.ThreadInput) (bool, error)
	AttachmentDownloadFailureMessage          func(error, bool) string
	OnAttachmentDownloadFailed                func(context.Context, string)
	OnQueuedAttachmentDownloadFailed          func(context.Context, string)
	OnAttachmentStoreFailed                   func(context.Context, string)
	OnModelSelectionFailed                    func(context.Context, error)
	OnActiveLookupFailed                      func(context.Context)
	OnQueueFailure                            func(context.Context)
	OnQueued                                  func(context.Context)
	OnDurableHandoff                          func()
	FirstTurn                                 channelChatIngressFirstTurnOptions
}

func (opts channelChatIngressOptions) selectionPrompt() string {
	if strings.TrimSpace(opts.SelectionMessage) != "" {
		return opts.SelectionMessage
	}
	return opts.Message
}

func selectChannelChatAgent(ctx context.Context, repo *repository.LLMConfigRepo, message string, hasImages bool) (*models.LLMConfig, error) {
	if repo == nil {
		return nil, fmt.Errorf("no model repository configured")
	}
	agents, err := repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents configured")
	}
	complexity := AnalyzeComplexity(message)
	if result := SelectLLMWithVision(complexity, agents, hasImages); result != nil {
		return result.LLMConfig, nil
	}
	for i := range agents {
		if agents[i].IsDefault {
			return &agents[i], nil
		}
	}
	return &agents[0], nil
}

type channelChatContextOptions struct {
	Platform              string
	ProjectID             string
	TaskSvc               *TaskService
	LLMConfigRepo         *repository.LLMConfigRepo
	ScheduleRepo          *repository.ScheduleRepo
	AgentRepo             *repository.AgentRepo
	SettingsRepo          *repository.SettingsRepo
	CustomPersonalityRepo *repository.CustomPersonalityRepo
	AttachmentContext     string
}

func buildChannelChatContext(ctx context.Context, opts channelChatContextOptions) string {
	existingTasks := []models.Task{}
	if opts.TaskSvc != nil {
		if tasks, err := opts.TaskSvc.ListByProject(ctx, opts.ProjectID, ""); err == nil {
			existingTasks = tasks
		}
	}
	availableModels := []models.LLMConfig{}
	if opts.LLMConfigRepo != nil {
		if configs, err := opts.LLMConfigRepo.List(ctx); err == nil {
			availableModels = configs
		}
	}
	schedules := []models.Schedule{}
	if opts.ScheduleRepo != nil {
		if rows, err := opts.ScheduleRepo.ListByProject(ctx, opts.ProjectID); err == nil {
			schedules = rows
		}
	}
	systemContext := BuildChatContextWithAgentDefinitions(existingTasks, availableModels, listChannelChatAssignableAgentDefinitions(ctx, opts.Platform, opts.AgentRepo), schedules, time.Now())
	if opts.AttachmentContext != "" {
		systemContext += opts.AttachmentContext
	}
	if personalityPrompt := getChannelChatPersonalityContext(ctx, opts.SettingsRepo, opts.CustomPersonalityRepo); personalityPrompt != "" {
		systemContext += personalityPrompt
	}
	return systemContext
}

func listChannelChatAssignableAgentDefinitions(ctx context.Context, platform string, repo *repository.AgentRepo) []models.ChatAssignableAgentDefinition {
	if repo == nil {
		return nil
	}
	agents, err := repo.ListChatAssignableDefinitions(ctx)
	if err != nil {
		applog.Infof("[%s] error listing agent definitions for context: %v", channelChatLogPlatform(platform), err)
		return nil
	}
	return UniqueChatAssignableAgentDefinitions(agents)
}

func getChannelChatPersonalityContext(ctx context.Context, settingsRepo *repository.SettingsRepo, customRepo *repository.CustomPersonalityRepo) string {
	if settingsRepo == nil {
		return ""
	}
	personality, err := settingsRepo.Get(ctx, "personality")
	if err != nil || personality == "" {
		return ""
	}
	prompt := GetPersonalityPromptWithCustom(ctx, personality, customRepo)
	if prompt == "" {
		return ""
	}
	return "\n# Communication Style\n\n" + prompt
}

func resolveChannelChatWorkDir(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	if projectRepo == nil {
		return ""
	}
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return ""
	}
	return project.RepoPath
}

func completeChannelExecution(ctx context.Context, opts channelExecutionCompletionOptions) {
	if opts.ExecRepo == nil || opts.TaskRepo == nil {
		return
	}
	platform := channelChatLogPlatform(opts.Platform)
	if opts.ErrorMessage != "" {
		if err := opts.ExecRepo.Complete(ctx, opts.ExecID, models.ExecFailed, "", opts.ErrorMessage, 0, opts.DurationMs); err != nil {
			applog.Infof("[%s] complete failed execution error: %v", platform, err)
		} else {
			opts.ExecutionStreamHub.CloseTerminal(opts.ExecID, models.ExecFailed, opts.ErrorMessage)
		}
		if err := opts.TaskRepo.UpdateStatus(ctx, opts.TaskID, models.StatusFailed); err != nil {
			applog.Infof("[%s] update failed task status error: %v", platform, err)
		}
		promoteQueuedChannelChatAfterCompletion(ctx, opts.TaskRepo, opts.QueuedTurnPromoter, opts.TaskID)
		return
	}
	if err := opts.ExecRepo.Complete(ctx, opts.ExecID, models.ExecCompleted, opts.Output, "", opts.TokensUsed, opts.DurationMs); err != nil {
		applog.Infof("[%s] complete execution error: %v", platform, err)
	} else {
		opts.ExecutionStreamHub.CloseTerminal(opts.ExecID, models.ExecCompleted, "")
	}
	if err := opts.TaskRepo.UpdateStatus(ctx, opts.TaskID, models.StatusCompleted); err != nil {
		applog.Infof("[%s] update task status error: %v", platform, err)
	}
	promoteQueuedChannelChatAfterCompletion(ctx, opts.TaskRepo, opts.QueuedTurnPromoter, opts.TaskID)
}

type channelExecutionCompletionOptions struct {
	Platform           string
	ExecRepo           *repository.ExecutionRepo
	TaskRepo           *repository.TaskRepo
	ExecutionStreamHub *events.ExecutionStreamHub
	QueuedTurnPromoter func(projectID string)
	ExecID             string
	TaskID             string
	Output             string
	ErrorMessage       string
	TokensUsed         int
	DurationMs         int64
}

func promoteQueuedChannelChatAfterCompletion(ctx context.Context, taskRepo *repository.TaskRepo, queuedTurnPromoter func(projectID string), taskID string) {
	if queuedTurnPromoter == nil || taskRepo == nil {
		return
	}
	task, err := taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil || task.Category != models.CategoryChat {
		return
	}
	queuedTurnPromoter(task.ProjectID)
}

func resolveChannelTaskReference(ctx context.Context, taskRepo *repository.TaskRepo, projectID, taskID, title string) (*models.Task, error) {
	if taskRepo == nil {
		return nil, fmt.Errorf("task repository not configured")
	}
	if strings.TrimSpace(taskID) != "" {
		taskID = strings.TrimSpace(taskID)
		task, err := taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("error looking up task %s: %w", taskID, err)
		}
		if task == nil {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		if task.ProjectID != projectID {
			return nil, fmt.Errorf("task %s belongs to a different project", taskID)
		}
		return task, nil
	}
	title = strings.TrimSpace(title)
	if title != "" {
		tasks, err := taskRepo.SearchByTitle(ctx, projectID, title)
		if err != nil {
			return nil, fmt.Errorf("error searching for task %q: %w", title, err)
		}
		if len(tasks) == 0 {
			return nil, fmt.Errorf("no task found matching %q", title)
		}
		return &tasks[0], nil
	}
	return nil, fmt.Errorf("no task_id or title provided")
}

func runChannelTaskThreadSend(ctx context.Context, task *models.Task, opts channelTaskThreadSendOptions) string {
	formatErr := opts.ErrorResult
	if formatErr == nil {
		formatErr = func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	}
	if task == nil {
		return formatErr("Error resolving task: task not found")
	}
	if strings.TrimSpace(opts.Message) == "" {
		return "send_to_task requires message."
	}
	activeExec, queueBehindFirstTurn, err := findActiveOrStartingChannelTaskTurn(ctx, opts.ExecRepo, task)
	if err != nil {
		if opts.ActiveLookupErrorResult != nil {
			return opts.ActiveLookupErrorResult(task, err)
		}
		return formatErr("Error checking active turn for task %q: %v", task.Title, err)
	}
	if activeExec == nil && !queueBehindFirstTurn {
		activeExec, _, err = findActiveOrStartingChannelTaskTurn(ctx, opts.ExecRepo, task)
		if err != nil {
			if opts.ActiveLookupErrorResult != nil {
				return opts.ActiveLookupErrorResult(task, err)
			}
			return formatErr("Error checking active turn for task %q: %v", task.Title, err)
		}
	}
	if activeExec != nil || queueBehindFirstTurn {
		if opts.ThreadInputRepo == nil {
			if opts.QueueUnavailableResult != nil {
				return opts.QueueUnavailableResult(task)
			}
			return fmt.Sprintf("Task %q is currently running. Queue is unavailable, so wait for it to finish before sending a message.", task.Title)
		}
		agentID := ""
		if task.AgentID != nil {
			agentID = *task.AgentID
		}
		runExecutionID := ""
		if activeExec != nil {
			runExecutionID = activeExec.ID
		}
		queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, RunExecutionID: runExecutionID, AgentConfigID: agentID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: opts.Message, Source: opts.Source}
		if opts.NewQueuedInput != nil {
			if custom := opts.NewQueuedInput(task, runExecutionID, agentID); custom != nil {
				queued = custom
				queued.Scope = models.ThreadInputScopeTask
				queued.ProjectID = task.ProjectID
				queued.TaskID = task.ID
				queued.RunExecutionID = runExecutionID
				queued.AgentConfigID = agentID
				queued.InputMode = models.ThreadInputModeQueued
				queued.InputStatus = models.ThreadInputPending
				queued.Content = opts.Message
				queued.Source = opts.Source
			}
		}
		if err := opts.ThreadInputRepo.CreateQueued(ctx, queued); err != nil {
			if opts.QueueErrorResult != nil {
				return opts.QueueErrorResult(task, err)
			}
			return formatErr("Error queueing message for task %q: %v", task.Title, err)
		}
		if queued.RunExecutionID == "" {
			if err := bindQueuedChannelTaskInputToActiveExecutionIfAvailable(ctx, opts.ExecRepo, opts.ThreadInputRepo, queued); err != nil && opts.OnBindQueuedInputSkipped != nil {
				opts.OnBindQueuedInputSkipped(ctx, task, queued, err)
			}
		}
		if shouldPromote, promoteErr := shouldPromotePreExecutionChannelTaskInput(ctx, opts.ExecRepo, task, queued); promoteErr != nil {
			if opts.OnPromotionRecheckSkipped != nil {
				opts.OnPromotionRecheckSkipped(ctx, task, queued, promoteErr)
			}
		} else if shouldPromote && opts.QueuedTaskThreadPromoter != nil {
			go opts.QueuedTaskThreadPromoter(task.ID)
		}
		if opts.QueuedResult != nil {
			return opts.QueuedResult(task)
		}
		return fmt.Sprintf("Queued message to task %q [TASK_ID:%s]. It will run after the active thread turn finishes.", task.Title, task.ID)
	}
	agent, err := selectChannelTaskThreadAgent(ctx, opts.LLMConfigRepo, task, opts.Message)
	if err != nil {
		if opts.AgentSelectionErrorResult != nil {
			return opts.AgentSelectionErrorResult(task, err)
		}
		return formatErr("Error selecting agent for task %q: %v", task.Title, err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecQueued, PromptSent: opts.Message, IsFollowup: true}
	if opts.ExecRepo == nil {
		return formatErr("Error creating follow-up execution for %q: execution repository not configured", task.Title)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: opts.Message, Source: opts.Source}
	if opts.NewQueuedInput != nil {
		if custom := opts.NewQueuedInput(task, "", agent.ID); custom != nil {
			queued = custom
			queued.Scope = models.ThreadInputScopeTask
			queued.ProjectID = task.ProjectID
			queued.TaskID = task.ID
			queued.RunExecutionID = ""
			queued.AgentConfigID = agent.ID
			queued.InputMode = models.ThreadInputModeQueued
			queued.InputStatus = models.ThreadInputPending
			queued.Content = opts.Message
			queued.Source = opts.Source
		}
	}
	started, err := opts.ExecRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, queued)
	if err != nil {
		if opts.ExecutionCreateErrorResult != nil {
			return opts.ExecutionCreateErrorResult(task, err)
		}
		return formatErr("Error admitting follow-up execution for %q: %v", task.Title, err)
	}
	if !started {
		if opts.QueuedTaskThreadPromoter != nil {
			go opts.QueuedTaskThreadPromoter(task.ID)
		}
		if opts.QueuedResult != nil {
			return opts.QueuedResult(task)
		}
		return fmt.Sprintf("Queued message to task %q [TASK_ID:%s]. It will run after the active thread turn finishes.", task.Title, task.ID)
	}
	priorExecs, _ := opts.ExecRepo.ListByTaskChronological(ctx, task.ID)
	priorHistory := priorExecs
	if opts.FilterHistory != nil {
		priorHistory = opts.FilterHistory(priorExecs, exec.ID)
	}
	systemContext := buildTelegramTaskChatContext(task.Title, len(priorHistory) > 0)
	if pCtx := getChannelChatPersonalityContext(ctx, opts.SettingsRepo, opts.CustomPersonalityRepo); pCtx != "" {
		systemContext += pCtx
	}
	if opts.ChannelTaskRunner == nil {
		msgText := "shared task runner is unavailable"
		if opts.CompleteExecution != nil {
			opts.CompleteExecution(ctx, exec.ID, task.ID, "", msgText, 0, 0)
		}
		if opts.RunnerUnavailableResult != nil {
			return opts.RunnerUnavailableResult(task, msgText)
		}
		return formatErr("Error sending message to task %q: %s", task.Title, msgText)
	}
	if opts.TaskRepo == nil {
		return formatErr("Error updating task %q: task repository not configured", task.Title)
	}
	if updatedTask, getErr := opts.TaskRepo.GetByID(ctx, task.ID); getErr == nil && updatedTask != nil {
		task = updatedTask
	}
	opts.ChannelTaskRunner(context.Background(), ChannelTaskRunRequest{ExecID: exec.ID, TaskID: task.ID, ProjectID: task.ProjectID, Message: opts.Message, Agent: *agent, ChatHistory: priorHistory, SystemContext: systemContext, Surface: opts.Surface, ReplyContext: opts.ReplyContext})
	if opts.StartedResult != nil {
		return opts.StartedResult(task)
	}
	return fmt.Sprintf("Sent message to task %q [TASK_ID:%s] and started processing.", task.Title, task.ID)
}

func selectChannelTaskThreadAgent(ctx context.Context, repo *repository.LLMConfigRepo, task *models.Task, message string) (*models.LLMConfig, error) {
	if task != nil && task.AgentID != nil && repo != nil {
		if agent, _ := repo.GetByID(ctx, *task.AgentID); agent != nil {
			return agent, nil
		}
	}
	return selectChannelChatAgent(ctx, repo, message, false)
}

func findActiveOrStartingChannelTaskTurn(ctx context.Context, execRepo *repository.ExecutionRepo, task *models.Task) (*models.Execution, bool, error) {
	if task == nil || execRepo == nil {
		return nil, false, nil
	}
	activeExec, err := execRepo.FindActiveTaskExecution(ctx, task.ID, "")
	if err != nil || activeExec != nil {
		return activeExec, false, err
	}
	starting, err := channelTaskHasStartingFirstTurn(ctx, execRepo, task)
	return nil, starting, err
}

func channelTaskHasStartingFirstTurn(ctx context.Context, execRepo *repository.ExecutionRepo, task *models.Task) (bool, error) {
	if task == nil || task.ID == "" || execRepo == nil {
		return false, nil
	}
	if task.Category != models.CategoryActive || (task.Status != models.StatusPending && task.Status != models.StatusQueued && task.Status != models.StatusRunning) {
		return false, nil
	}
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return false, err
	}
	return len(execs) == 0, nil
}

func bindQueuedChannelTaskInputToActiveExecutionIfAvailable(ctx context.Context, execRepo *repository.ExecutionRepo, threadInputRepo *repository.ThreadInputRepo, input *models.ThreadInput) error {
	if input == nil || input.RunExecutionID != "" || execRepo == nil || threadInputRepo == nil {
		return nil
	}
	active, err := execRepo.FindActiveTaskExecution(ctx, input.TaskID, "")
	if err != nil || active == nil {
		return err
	}
	if err := threadInputRepo.BindPreExecutionQueuedTaskInputs(ctx, input.TaskID, active.ID); err != nil {
		return err
	}
	input.RunExecutionID = active.ID
	return nil
}

func shouldPromotePreExecutionChannelTaskInput(ctx context.Context, execRepo *repository.ExecutionRepo, task *models.Task, input *models.ThreadInput) (bool, error) {
	if task == nil || input == nil || input.RunExecutionID != "" {
		return false, nil
	}
	starting, err := channelTaskHasStartingFirstTurn(ctx, execRepo, task)
	if err != nil || starting {
		return false, err
	}
	return true, nil
}

func channelTaskThreadQueuedResult(task *models.Task) string {
	return fmt.Sprintf("Queued message to task %q [TASK_ID:%s]. It will run after the active thread turn finishes.", task.Title, task.ID)
}

func channelTaskThreadStartedResult(task *models.Task) string {
	return fmt.Sprintf("Sent message to task %q [TASK_ID:%s] and started processing.", task.Title, task.ID)
}

func channelChatLogPlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return "channel"
	}
	return platform
}

func runChannelChatIngress(ctx context.Context, opts channelChatIngressOptions) bool {
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "channel"
	}
	var activeChatExec *models.Execution
	if opts.FindActiveExecution != nil {
		var activeErr error
		activeChatExec, activeErr = opts.FindActiveExecution(ctx, opts.ProjectID)
		if activeErr != nil {
			applog.Infof("[%s] error checking active chat turn: %v", platform, activeErr)
			if opts.OnActiveLookupFailed != nil {
				opts.OnActiveLookupFailed(ctx)
			}
			return true
		}
	}

	attachmentContext := ""
	imageAttachments := []models.Attachment{}
	chatAttachments := []models.ChatAttachment{}
	if opts.DownloadAttachments != nil {
		downloaded, err := opts.DownloadAttachments(ctx)
		if err != nil {
			applog.Infof("[%s] attachment download error: %v", platform, err)
			msgText := fmt.Sprintf("Failed to process attachment: %v", err)
			if opts.AttachmentDownloadFailureMessage != nil {
				msgText = opts.AttachmentDownloadFailureMessage(err, activeChatExec != nil)
			} else if activeChatExec != nil {
				msgText = "Failed to process attachment: unable to download attachment. Please try again."
			}
			if activeChatExec != nil && opts.OnQueuedAttachmentDownloadFailed != nil {
				opts.OnQueuedAttachmentDownloadFailed(ctx, msgText)
			} else if opts.OnAttachmentDownloadFailed != nil {
				opts.OnAttachmentDownloadFailed(ctx, msgText)
			}
			if opts.ContinueWithoutAttachmentsOnDownloadError {
				// Preserve adapters such as Telegram that currently warn but continue the text turn.
			} else {
				agent, agentErr := selectChannelChatAgent(ctx, opts.LLMConfigRepo, opts.selectionPrompt(), opts.IncomingAttachmentsNeedVision != nil && opts.IncomingAttachmentsNeedVision())
				if agentErr != nil {
					if opts.OnModelSelectionFailed != nil {
						opts.OnModelSelectionFailed(ctx, agentErr)
					}
					return true
				}
				if opts.RecordAttachmentFailure != nil {
					opts.RecordAttachmentFailure(ctx, agent.ID, msgText)
				}
				return true
			}
		}
		attachmentContext = downloaded.AttachmentContext
		imageAttachments = downloaded.ImageAttachments
		chatAttachments = downloaded.ChatAttachments
	}

	agent, err := selectChannelChatAgent(ctx, opts.LLMConfigRepo, opts.selectionPrompt(), len(imageAttachments) > 0)
	if err != nil {
		cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, chatAttachments)
		if opts.OnModelSelectionFailed != nil {
			opts.OnModelSelectionFailed(ctx, err)
		}
		return true
	}

	if activeChatExec != nil {
		attachmentSessionID := ""
		if len(chatAttachments) > 0 {
			if opts.SavePendingAttachments == nil && opts.SavePendingAttachmentsWithContext == nil {
				cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, chatAttachments)
				if opts.OnAttachmentStoreFailed != nil {
					opts.OnAttachmentStoreFailed(ctx, "Failed to process attachment: unable to store attachment. Please try again.")
				}
				return true
			}
			var stageErr error
			if opts.SavePendingAttachmentsWithContext != nil {
				attachmentSessionID, stageErr = opts.SavePendingAttachmentsWithContext(ctx, chatAttachments)
			} else {
				attachmentSessionID, stageErr = opts.SavePendingAttachments(chatAttachments)
			}
			if stageErr != nil {
				applog.Infof("[%s] queue chat attachment staging failed: %v", platform, stageErr)
				msgText := "Failed to process attachment: unable to store attachment. Please try again."
				if opts.RecordAttachmentFailure != nil {
					opts.RecordAttachmentFailure(ctx, agent.ID, msgText)
				}
				if opts.OnAttachmentStoreFailed != nil {
					opts.OnAttachmentStoreFailed(ctx, msgText)
				}
				return true
			}
		}
		return runChannelChatQueuedInput(ctx, channelChatIngressQueueOptions{
			Platform:                platform,
			ProjectID:               opts.ProjectID,
			ActiveExecID:            activeChatExec.ID,
			AgentID:                 agent.ID,
			Message:                 opts.Message,
			Source:                  opts.Source,
			AttachmentSessionID:     attachmentSessionID,
			UploadsDir:              opts.UploadsDir,
			BroadcastHasAttachments: opts.HasAttachments,
			ThreadInputRepo:         opts.ThreadInputRepo,
			ChatBroadcaster:         opts.ChatBroadcaster,
			NewThreadInput:          opts.NewQueuedInput,
			CreateQueuedInput:       opts.CreateQueuedInput,
			OnQueueFailure:          opts.OnQueueFailure,
			OnQueued:                opts.OnQueued,
			OnDurableHandoff:        opts.OnDurableHandoff,
			CleanupPendingSession:   opts.CleanupPendingSession,
		})
	}

	first := opts.FirstTurn
	first.Platform = platform
	first.ProjectID = opts.ProjectID
	first.Message = opts.Message
	first.Source = opts.Source
	first.Agent = agent
	first.Attachments = chatAttachments
	first.AttachmentContext = attachmentContext
	first.ImageAttachments = imageAttachments
	first.HasAttachments = opts.HasAttachments || len(chatAttachments) > 0
	first.Surface = opts.Surface
	first.OnDurableHandoff = opts.OnDurableHandoff
	first.CleanupAttachmentSources = opts.CleanupAttachmentSources
	first.Start = opts.Start
	if first.Start.IsZero() {
		first.Start = time.Now()
	}
	if first.TaskRepo == nil {
		first.TaskRepo = opts.TaskRepo
	}
	if first.ExecRepo == nil {
		first.ExecRepo = opts.ExecRepo
	}
	if first.ChatBroadcaster == nil {
		first.ChatBroadcaster = opts.ChatBroadcaster
	}
	if first.TaskSvc == nil {
		first.TaskSvc = opts.TaskSvc
	}
	if first.LLMConfigRepo == nil {
		first.LLMConfigRepo = opts.LLMConfigRepo
	}
	if first.ScheduleRepo == nil {
		first.ScheduleRepo = opts.ScheduleRepo
	}
	if first.AgentRepo == nil {
		first.AgentRepo = opts.AgentRepo
	}
	if first.SettingsRepo == nil {
		first.SettingsRepo = opts.SettingsRepo
	}
	if first.CustomPersonalityRepo == nil {
		first.CustomPersonalityRepo = opts.CustomPersonalityRepo
	}
	if first.ProjectRepo == nil {
		first.ProjectRepo = opts.ProjectRepo
	}
	runChannelChatFirstTurn(ctx, first)
	return true
}

func runChannelChatQueuedInput(ctx context.Context, opts channelChatIngressQueueOptions) bool {
	if opts.ThreadInputRepo == nil && opts.CreateQueuedInput == nil {
		return false
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "channel"
	}
	queued := &models.ThreadInput{
		Scope:               models.ThreadInputScopeChat,
		ProjectID:           opts.ProjectID,
		RunExecutionID:      opts.ActiveExecID,
		AgentConfigID:       opts.AgentID,
		InputMode:           models.ThreadInputModeQueued,
		InputStatus:         models.ThreadInputPending,
		Content:             opts.Message,
		AttachmentSessionID: opts.AttachmentSessionID,
		ChatMode:            models.ChatModeOrchestrate,
		Source:              opts.Source,
	}
	if opts.NewThreadInput != nil {
		custom := opts.NewThreadInput()
		if custom != nil {
			queued = custom
			queued.Scope = models.ThreadInputScopeChat
			queued.ProjectID = opts.ProjectID
			queued.RunExecutionID = opts.ActiveExecID
			queued.AgentConfigID = opts.AgentID
			queued.InputMode = models.ThreadInputModeQueued
			queued.InputStatus = models.ThreadInputPending
			queued.Content = opts.Message
			queued.AttachmentSessionID = opts.AttachmentSessionID
			queued.ChatMode = models.ChatModeOrchestrate
			queued.Source = opts.Source
		}
	}
	createQueued := opts.CreateQueuedInput
	if createQueued == nil {
		createQueued = func(ctx context.Context, input *models.ThreadInput) (bool, error) {
			return false, opts.ThreadInputRepo.CreateQueued(ctx, input)
		}
	}
	alreadyHandedOff, err := createQueued(ctx, queued)
	if err != nil {
		applog.Infof("[%s] queue chat input failed: %v", platform, err)
		if opts.AttachmentSessionID != "" {
			cleanupChannelChatPendingSession(ctx, opts.CleanupPendingSession, opts.UploadsDir, opts.AttachmentSessionID)
		}
		if opts.OnQueueFailure != nil {
			opts.OnQueueFailure(ctx)
		}
		return true
	}
	if opts.OnDurableHandoff != nil {
		opts.OnDurableHandoff()
	}
	if alreadyHandedOff {
		if opts.AttachmentSessionID != "" {
			cleanupChannelChatPendingSession(ctx, opts.CleanupPendingSession, opts.UploadsDir, opts.AttachmentSessionID)
		}
		return true
	}
	if opts.ChatBroadcaster != nil {
		opts.ChatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: opts.ProjectID, ExecID: queued.ID, Message: opts.Message, Source: opts.Source, Queued: true, HasAttachments: opts.BroadcastHasAttachments || opts.AttachmentSessionID != ""})
	}
	if opts.OnQueued != nil {
		opts.OnQueued(ctx)
	}
	return true
}

func cleanupChannelChatAttachmentSources(ctx context.Context, cleanup func(context.Context, []models.ChatAttachment), attachments []models.ChatAttachment) {
	if cleanup != nil {
		cleanup(ctx, attachments)
		return
	}
	cleanupChannelChatAttachmentSourceDirs(attachments)
}

func cleanupChannelChatPendingSession(ctx context.Context, cleanup func(context.Context, string), uploadsDir, sessionID string) {
	if sessionID == "" {
		return
	}
	if cleanup != nil {
		cleanup(ctx, sessionID)
		return
	}
	_ = os.RemoveAll(filepath.Join(uploadsDir, "chat", "pending", sessionID))
}

func cleanupChannelChatProvisionalTaskWithOptions(ctx context.Context, opts channelChatIngressFirstTurnOptions, platform, taskID, reason string) {
	if opts.CleanupProvisionalTask != nil {
		opts.CleanupProvisionalTask(ctx, taskID, reason)
		return
	}
	cleanupChannelChatProvisionalTask(ctx, platform, opts.TaskRepo, taskID, reason)
}

func cleanupChannelChatProvisionalTask(ctx context.Context, platform string, taskRepo *repository.TaskRepo, taskID, reason string) {
	if taskRepo == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := taskRepo.Delete(cleanupCtx, taskID); err != nil {
		applog.Infof("[%s] cleanup %s task failed task=%s: %v", platform, reason, taskID, err)
	}
}

func runChannelChatFirstTurn(ctx context.Context, opts channelChatIngressFirstTurnOptions) (bool, []models.ChatAttachment) {
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "channel"
	}
	if opts.TaskRepo == nil || (opts.ExecRepo == nil && opts.CreateExecution == nil && opts.CreateDurableFirstTurn == nil) || opts.Agent == nil || opts.Task == nil {
		applog.Infof("[%s] incoming message ignored: shared ingress dependencies are not fully configured", platform)
		cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
		return false, nil
	}
	selectedAgentID := opts.Agent.ID
	opts.Task.ProjectID = opts.ProjectID
	opts.Task.Prompt = opts.Message
	opts.Task.Status = models.StatusPending
	opts.Task.Category = models.CategoryChat
	opts.Task.AgentID = &selectedAgentID
	if opts.Task.CreatedVia == "" {
		opts.Task.CreatedVia = opts.Source
	}
	exec := &models.Execution{AgentConfigID: opts.Agent.ID, Status: models.ExecRunning, PromptSent: opts.Message}
	linkedAttachments := opts.Attachments
	attachmentsDurablyLinked := false
	if opts.CreateDurableFirstTurn != nil {
		if len(opts.Attachments) > 0 {
			if opts.PrepareDurableAttachments == nil {
				applog.Infof("[%s] durable first-turn attachment preparation is unavailable", platform)
				cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
				if opts.OnAttachmentLinkFailure != nil {
					opts.OnAttachmentLinkFailure(ctx, "Failed to process attachment: unable to store attachment. Please try again.")
				}
				return true, nil
			}
			exec.ID = generateChannelChatPendingSessionID()
			prepared, err := opts.PrepareDurableAttachments(ctx, exec.ID, opts.Attachments)
			if err != nil {
				applog.Infof("[%s] prepare durable first-turn attachments failed: %v", platform, err)
				if opts.OnAttachmentLinkFailure != nil {
					opts.OnAttachmentLinkFailure(ctx, "Failed to process attachment: unable to store attachment. Please try again.")
				}
				return true, nil
			}
			linkedAttachments = prepared
		}
		alreadyHandedOff, err := opts.CreateDurableFirstTurn(ctx, opts.Task, exec, linkedAttachments)
		if err != nil {
			applog.Infof("[%s] create durable first turn failed: %v", platform, err)
			cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, linkedAttachments)
			if opts.OnExecutionCreateFailure != nil {
				opts.OnExecutionCreateFailure(ctx)
			}
			return true, nil
		}
		if opts.OnDurableHandoff != nil {
			opts.OnDurableHandoff()
		}
		if alreadyHandedOff {
			cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, linkedAttachments)
			return true, nil
		}
		attachmentsDurablyLinked = len(linkedAttachments) > 0
	} else {
		if err := opts.TaskRepo.Create(ctx, opts.Task); err != nil {
			applog.Infof("[%s] create chat task failed: %v", platform, err)
			cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
			if opts.OnTaskCreateFailure != nil {
				opts.OnTaskCreateFailure(ctx)
			}
			return true, nil
		}
		if opts.CreateTaskContext != nil {
			if err := opts.CreateTaskContext(ctx, opts.Task.ID); err != nil {
				applog.Infof("[%s] create chat context failed task=%s: %v", platform, opts.Task.ID, err)
				cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
				cleanupChannelChatProvisionalTaskWithOptions(ctx, opts, platform, opts.Task.ID, "chat context")
				if opts.OnTaskContextFailure != nil {
					opts.OnTaskContextFailure(ctx)
				}
				return true, nil
			}
		}
		exec.TaskID = opts.Task.ID
		createExecution := opts.CreateExecution
		if createExecution == nil {
			createExecution = func(ctx context.Context, execution *models.Execution) (bool, error) {
				return false, opts.ExecRepo.Create(ctx, execution)
			}
		}
		alreadyHandedOff, err := createExecution(ctx, exec)
		if err != nil {
			applog.Infof("[%s] create execution failed: %v", platform, err)
			cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
			cleanupChannelChatProvisionalTaskWithOptions(ctx, opts, platform, opts.Task.ID, "chat task after execution create failure")
			if opts.OnExecutionCreateFailure != nil {
				opts.OnExecutionCreateFailure(ctx)
			}
			return true, nil
		}
		if opts.OnDurableHandoff != nil {
			opts.OnDurableHandoff()
		}
		if alreadyHandedOff {
			cleanupChannelChatAttachmentSources(ctx, opts.CleanupAttachmentSources, opts.Attachments)
			cleanupChannelChatProvisionalTaskWithOptions(ctx, opts, platform, opts.Task.ID, "duplicate chat")
			return true, nil
		}
	}

	hasLinkedAttachments := attachmentsDurablyLinked
	if len(linkedAttachments) > 0 && !attachmentsDurablyLinked {
		linkFn := opts.LinkAttachments
		if linkFn == nil {
			linkFn = func(ctx context.Context, execID string, atts []models.ChatAttachment) ([]models.ChatAttachment, error) {
				return linkChannelChatAttachmentsToExecution(ctx, execID, atts, channelChatAttachmentLinkOptions{Platform: platform, Repo: opts.ChatAttachmentRepo})
			}
		}
		var err error
		linkedAttachments, err = linkFn(ctx, exec.ID, linkedAttachments)
		if err != nil {
			applog.Infof("[%s] attachment link error: %v", platform, err)
			msgText := "Failed to process attachment: unable to store attachment. Please try again."
			if opts.CompleteExecution != nil {
				opts.CompleteExecution(ctx, exec.ID, opts.Task.ID, "", msgText, 0, time.Since(opts.Start).Milliseconds())
			}
			if opts.ChatBroadcaster != nil {
				opts.ChatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: opts.ProjectID, ExecID: exec.ID, TaskID: opts.Task.ID, Message: opts.Message, Source: opts.Source, AgentName: opts.Agent.Name, HasAttachments: opts.HasAttachments || len(opts.Attachments) > 0})
			}
			if opts.OnAttachmentLinkFailure != nil {
				opts.OnAttachmentLinkFailure(ctx, msgText)
			}
			return true, nil
		}
		hasLinkedAttachments = true
	}
	if hasLinkedAttachments && opts.AttachmentContextAndImages != nil {
		opts.AttachmentContext, opts.ImageAttachments = opts.AttachmentContextAndImages(linkedAttachments)
	}

	if opts.ChatBroadcaster != nil {
		opts.ChatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: opts.ProjectID, ExecID: exec.ID, TaskID: opts.Task.ID, Message: opts.Message, Source: opts.Source, AgentName: opts.Agent.Name, HasAttachments: hasLinkedAttachments})
	}

	history := []models.Execution{}
	if opts.ListChatHistory != nil {
		var err error
		history, err = opts.ListChatHistory(ctx, opts.ProjectID)
		if err != nil {
			history = []models.Execution{}
		}
	}
	if opts.FilterChatHistory != nil {
		history = opts.FilterChatHistory(history, exec.ID)
	}
	systemContext := buildChannelChatContext(ctx, channelChatContextOptions{
		Platform:              platform,
		ProjectID:             opts.ProjectID,
		TaskSvc:               opts.TaskSvc,
		LLMConfigRepo:         opts.LLMConfigRepo,
		ScheduleRepo:          opts.ScheduleRepo,
		AgentRepo:             opts.AgentRepo,
		SettingsRepo:          opts.SettingsRepo,
		CustomPersonalityRepo: opts.CustomPersonalityRepo,
		AttachmentContext:     opts.AttachmentContext,
	})
	workDir := resolveChannelChatWorkDir(ctx, opts.ProjectRepo, opts.ProjectID)
	initialAckID := opts.InitialAckID
	if opts.PrepareRunner != nil {
		initialAckID = opts.PrepareRunner(ctx, opts.Task.ID, exec.ID)
	}
	if opts.ChannelChatRunner == nil {
		msgText := channelChatAttachmentDisplayPlatform(platform) + " chat runner is unavailable. Please restart OpenVibely and try again."
		if opts.CompleteExecution != nil {
			opts.CompleteExecution(ctx, exec.ID, opts.Task.ID, "", msgText, 0, time.Since(opts.Start).Milliseconds())
		}
		if opts.OnRunnerUnavailable != nil {
			opts.OnRunnerUnavailable(ctx, msgText, initialAckID)
		}
		return true, linkedAttachments
	}
	runtimeTools := opts.RuntimeTools
	if opts.RuntimeToolsForTask != nil {
		runtimeTools = opts.RuntimeToolsForTask(opts.Task.ID)
	}
	opts.ChannelChatRunner(context.Background(), ChannelChatRunRequest{
		ExecID:              exec.ID,
		TaskID:              opts.Task.ID,
		ProjectID:           opts.ProjectID,
		Message:             opts.Message,
		Agent:               *opts.Agent,
		ChatHistory:         history,
		SystemContext:       systemContext,
		WorkDir:             workDir,
		ImageAttachments:    opts.ImageAttachments,
		Surface:             opts.Surface,
		InitialAckMessageID: initialAckID,
		ReplyContext:        opts.ReplyContext,
		RuntimeTools:        runtimeTools,
	})
	return true, linkedAttachments
}

func generateChannelChatPendingSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func uniqueChannelChatTempFilename(dir, filename string) string {
	candidate := filename
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	if base == "" {
		base = "attachment"
	}
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
}

func isChannelChatImageMediaType(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validateChannelChatDownloadedImageFile(path, fileName, declaredMediaType, platform string) (string, error) {
	declaredMediaType = strings.ToLower(strings.TrimSpace(strings.Split(declaredMediaType, ";")[0]))
	shouldValidateImage := isChannelChatImageMediaType(declaredMediaType)
	if !shouldValidateImage && declaredMediaType != "" && declaredMediaType != "application/octet-stream" {
		return declaredMediaType, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to validate image %q: %w", fileName, err)
	}
	defer file.Close()

	head := make([]byte, 512)
	n, readErr := io.ReadFull(file, head)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("failed to validate image %q: %w", fileName, readErr)
	}
	sniffedMediaType := strings.ToLower(strings.TrimSpace(http.DetectContentType(head[:n])))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to validate image %q: %w", fileName, err)
	}
	if channelChatLooksLikeWebP(head[:n]) {
		return "image/webp", nil
	}
	format, err := channelChatDecodeImageConfig(file)
	if err != nil {
		if !shouldValidateImage {
			return declaredMediaType, nil
		}
		return "", fmt.Errorf("downloaded %s file %q was labeled %s but is not a valid supported image (detected %s)", channelChatAttachmentDisplayPlatform(platform), fileName, declaredMediaType, sniffedMediaType)
	}
	detectedMediaType := channelChatImageFormatMediaType(format)
	if detectedMediaType == "" {
		return "", fmt.Errorf("downloaded %s file %q uses unsupported image format %q", channelChatAttachmentDisplayPlatform(platform), fileName, format)
	}
	if declaredMediaType != "" && declaredMediaType != "application/octet-stream" && declaredMediaType != detectedMediaType {
		applog.Infof("[%s] attachment file=%s declared mime=%s but detected mime=%s; using detected mime", platform, fileName, declaredMediaType, detectedMediaType)
	}
	return detectedMediaType, nil
}

func channelChatDecodeImageConfig(r io.Reader) (string, error) {
	_, format, err := image.DecodeConfig(r)
	return format, err
}

func channelChatLooksLikeWebP(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

func channelChatImageFormatMediaType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func channelChatAttachmentContextAndImages(chatAttachments []models.ChatAttachment, maxTextFileSize int64) (string, []models.Attachment) {
	var imageAttachments []models.Attachment
	var attachmentContents []string
	for _, att := range chatAttachments {
		if isChannelChatImageMediaType(att.MediaType) {
			imageAttachments = append(imageAttachments, models.Attachment{
				FileName:  att.FileName,
				FilePath:  att.FilePath,
				MediaType: att.MediaType,
				FileSize:  att.FileSize,
			})
			continue
		}
		if att.FileSize <= maxTextFileSize {
			content, readErr := os.ReadFile(att.FilePath)
			if readErr == nil {
				attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s\n```\n%s\n```\n", att.FileName, string(content)))
				continue
			}
		}
		attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s (attached, %d bytes - too large to include inline)\n", att.FileName, att.FileSize))
	}
	attachmentContext := ""
	if len(attachmentContents) > 0 {
		attachmentContext = "\n\n--- Attached Files ---\n" + strings.Join(attachmentContents, "")
	}
	return attachmentContext, imageAttachments
}

func saveChannelChatAttachmentsToPendingSession(uploadsDir, fallbackName string, attachments []models.ChatAttachment) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	sessionID := generateChannelChatPendingSessionID()
	sessionDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating pending upload directory: %w", err)
	}
	cleanupDirs := make(map[string]struct{})
	for _, att := range attachments {
		fileName := safeChannelChatAttachmentFileName(att.FileName, fallbackName)
		cleanupDirs[filepath.Dir(att.FilePath)] = struct{}{}
		destPath := filepath.Join(sessionDir, fmt.Sprintf("%s_%s", generateChannelChatPendingSessionID()[:8], fileName))
		if err := moveOrCopyFile(att.FilePath, destPath); err != nil {
			_ = os.RemoveAll(sessionDir)
			cleanupChannelChatAttachmentDirs(cleanupDirs)
			return "", fmt.Errorf("staging %s: %w", fileName, err)
		}
	}
	cleanupChannelChatAttachmentDirs(cleanupDirs)
	return sessionID, nil
}

func publishChannelChatAttachmentFiles(execID string, attachments []models.ChatAttachment, opts channelChatAttachmentLinkOptions) ([]models.ChatAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "channel"
	}
	execDir := filepath.Join(opts.UploadsDir, "chat", execID)
	if err := os.MkdirAll(execDir, 0755); err != nil {
		cleanupChannelChatAttachmentSourceDirs(attachments)
		return nil, fmt.Errorf("storing %s attachment: %w", channelChatAttachmentDisplayPlatform(platform), err)
	}
	cleanupDirs := make(map[string]struct{})
	published := make([]models.ChatAttachment, 0, len(attachments))
	for i := range attachments {
		att := attachments[i]
		cleanupDirs[filepath.Dir(att.FilePath)] = struct{}{}
		destPath := filepath.Join(execDir, uniqueChannelChatTempFilename(execDir, safeChannelChatAttachmentFileName(att.FileName, opts.FallbackName)))
		if err := moveOrCopyFile(att.FilePath, destPath); err != nil {
			_ = os.RemoveAll(execDir)
			cleanupChannelChatAttachmentDirs(cleanupDirs)
			cleanupChannelChatAttachmentSourceDirs(attachments[i+1:])
			return nil, fmt.Errorf("storing %s attachment %s: %w", channelChatAttachmentDisplayPlatform(platform), att.FileName, err)
		}
		absPath, err := filepath.Abs(destPath)
		if err != nil {
			absPath = destPath
		}
		att.FilePath = absPath
		att.ExecutionID = execID
		published = append(published, att)
	}
	cleanupChannelChatAttachmentDirs(cleanupDirs)
	return published, nil
}

func linkChannelChatAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment, opts channelChatAttachmentLinkOptions) ([]models.ChatAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "channel"
	}
	if opts.Repo == nil {
		cleanupChannelChatAttachmentSourceDirs(attachments)
		return nil, fmt.Errorf("chat attachment repository is unavailable")
	}
	displayPlatform := channelChatAttachmentDisplayPlatform(platform)
	execDir := filepath.Join(opts.UploadsDir, "chat", execID)
	if err := os.MkdirAll(execDir, 0755); err != nil {
		applog.Infof("[%s] error creating exec dir %s: %v", platform, execDir, err)
		cleanupChannelChatAttachmentSourceDirs(attachments)
		return nil, fmt.Errorf("storing %s attachment: %w", displayPlatform, err)
	}
	cleanupDirs := make(map[string]struct{})
	linked := make([]models.ChatAttachment, 0, len(attachments))
	var linkErrs []string
	for i := range attachments {
		att := &attachments[i]
		cleanupDirs[filepath.Dir(att.FilePath)] = struct{}{}
		destPath := filepath.Join(execDir, uniqueChannelChatTempFilename(execDir, safeChannelChatAttachmentFileName(att.FileName, opts.FallbackName)))
		if err := moveOrCopyFile(att.FilePath, destPath); err != nil {
			applog.Infof("[%s] error moving attachment file=%s: %v", platform, att.FileName, err)
			linkErrs = append(linkErrs, fmt.Sprintf("%s: %v", att.FileName, err))
			continue
		}
		absPath, err := filepath.Abs(destPath)
		if err != nil {
			absPath = destPath
		}
		att.FilePath = absPath
		att.ExecutionID = execID
		if err := opts.Repo.Create(ctx, att); err != nil {
			applog.Infof("[%s] error creating chat attachment record: %v", platform, err)
			_ = os.Remove(destPath)
			linkErrs = append(linkErrs, fmt.Sprintf("%s: %v", att.FileName, err))
		} else {
			linked = append(linked, *att)
			applog.Infof("[%s] linked attachment id=%s file=%s to exec=%s", platform, att.ID, att.FileName, execID)
		}
	}
	cleanupChannelChatAttachmentDirs(cleanupDirs)
	if len(linkErrs) > 0 {
		cleanupLinkedChannelChatAttachments(ctx, opts.Repo, platform, linked)
		return nil, fmt.Errorf("storing %s attachment failed for %d of %d file(s): %s", displayPlatform, len(linkErrs), len(attachments), strings.Join(linkErrs, "; "))
	}
	return linked, nil
}

func updateChannelChatImageAttachmentPaths(imageAttachments []models.Attachment, chatAttachments []models.ChatAttachment) []models.Attachment {
	for i := range imageAttachments {
		for _, ca := range chatAttachments {
			if ca.FileName == imageAttachments[i].FileName {
				imageAttachments[i].FilePath = ca.FilePath
				break
			}
		}
	}
	return imageAttachments
}

func cleanupLinkedChannelChatAttachments(ctx context.Context, repo *repository.ChatAttachmentRepo, platform string, attachments []models.ChatAttachment) {
	for _, att := range attachments {
		if strings.TrimSpace(att.ID) != "" && repo != nil {
			if err := repo.Delete(ctx, att.ID); err != nil {
				applog.Infof("[%s] error deleting partial chat attachment record id=%s: %v", platform, att.ID, err)
			}
		}
		if strings.TrimSpace(att.FilePath) != "" {
			if err := os.Remove(att.FilePath); err != nil && !os.IsNotExist(err) {
				applog.Infof("[%s] error deleting partial chat attachment file=%s: %v", platform, att.FilePath, err)
			}
		}
	}
}

func cleanupChannelChatAttachmentSourceDirs(attachments []models.ChatAttachment) {
	cleanupDirs := make(map[string]struct{})
	for _, att := range attachments {
		if strings.TrimSpace(att.FilePath) == "" {
			continue
		}
		cleanupDirs[filepath.Dir(att.FilePath)] = struct{}{}
	}
	cleanupChannelChatAttachmentDirs(cleanupDirs)
}

func cleanupChannelChatAttachmentDirs(dirs map[string]struct{}) {
	for dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

func safeChannelChatAttachmentFileName(name, fallbackName string) string {
	fileName := filepath.Base(name)
	if fileName == "." || fileName == string(filepath.Separator) || fileName == "" {
		fileName = strings.TrimSpace(fallbackName)
		if fileName == "" {
			fileName = "channel-attachment"
		}
	}
	return fileName
}

func channelChatAttachmentDisplayPlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return "Channel"
	}
	return strings.ToUpper(platform[:1]) + platform[1:]
}
