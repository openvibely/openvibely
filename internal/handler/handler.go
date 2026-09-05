package handler

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	desktopicons "github.com/openvibely/openvibely/assets/desktop/icons"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/update"
	echoSwagger "github.com/swaggo/echo-swagger"
)

type Handler struct {
	projectSvc                 *service.ProjectService
	taskSvc                    *service.TaskService
	swarmSvc                   *service.SwarmService
	taskGoalSvc                *service.TaskGoalService
	llmSvc                     *service.LLMService
	workerSvc                  *service.WorkerService
	schedulerSvc               *service.SchedulerService
	alertSvc                   *service.AlertService
	upcomingSvc                *service.UpcomingService
	insightsSvc                *service.InsightsService
	automationGraphSvc         *service.AutomationGraphService
	automationRegistrationSvc  *service.AutomationRegistrationService
	automationDraftSvc         *service.AutomationDraftService
	automationCapabilitySvc    *service.AutomationCapabilitySnapshotBuilder
	automationSaveValidator    *service.AutomationSaveValidator
	automationCompiler         *service.AutomationCompiler
	automationConfirmationSvc  *service.AutomationConfirmationService
	automationLifecycleSvc     *service.AutomationLifecycleService
	automationExternalStateSvc *service.AutomationExternalStateService
	automationLiveViewTracker  *service.AutomationLiveViewTracker
	llmConfigRepo              *repository.LLMConfigRepo
	taskRepo                   *repository.TaskRepo
	scheduleRepo               *repository.ScheduleRepo
	execRepo                   *repository.ExecutionRepo
	threadInputRepo            *repository.ThreadInputRepo
	usageRepo                  *repository.UsageRepo
	skillAnalyticsRepo         *repository.SkillAnalyticsRepo
	workerRepo                 *repository.WorkerRepo
	attachmentRepo             *repository.AttachmentRepo
	chatAttachmentRepo         *repository.ChatAttachmentRepo
	projectRepo                *repository.ProjectRepo
	settingsRepo               *repository.SettingsRepo
	usageAnalyticsSvc          *service.UsageAnalyticsService
	broadcaster                *events.Broadcaster
	chatBroadcaster            *events.ChatBroadcaster
	fileChangeBroadcaster      *events.FileChangeBroadcaster
	executionStreamHub         *events.ExecutionStreamHub
	telegramService            *service.TelegramService
	xService                   *service.XService
	xServiceMu                 sync.RWMutex
	xConfigMu                  sync.Mutex
	emailService               EmailServiceProvider
	telegramAuthRepo           *repository.TelegramAuthRepo
	slackAuthRepo              *repository.SlackAuthRepo
	emailAuthRepo              *repository.EmailAuthRepo
	discordAuthRepo            *repository.DiscordAuthRepo
	xAuthRepo                  *repository.XAuthRepo
	xUserProjectRepo           *repository.XUserProjectRepo
	xTaskContextRepo           *repository.XTaskContextRepo
	xInboundReceiptRepo        *repository.XInboundReceiptRepo
	emailTaskContextRepo       *repository.EmailTaskContextRepo
	slackTaskContextRepo       *repository.SlackTaskContextRepo
	discordTaskContextRepo     *repository.DiscordTaskContextRepo
	reviewCommentRepo          *repository.ReviewCommentRepo
	customPersonalityRepo      *repository.CustomPersonalityRepo
	agentRepo                  *repository.AgentRepo
	lifecycleRepo              *repository.LifecycleRepo
	worktreeSvc                *service.WorktreeService
	taskPullRequestRepo        *repository.TaskPullRequestRepo
	taskCommitStatRepo         *repository.TaskCommitStatRepo
	githubPRFeedbackRepo       *repository.GitHubPRFeedbackRepo
	githubAuthRepo             *repository.GitHubAuthRepo
	githubSvc                  GitHubServiceProvider
	slackSvc                   SlackServiceProvider
	discordSvc                 DiscordServiceProvider
	channelMessageRouter       *service.ChannelMessageRouter
	localRepoPathEnabled       *bool
	projectFolderPicker        ProjectFolderPicker
	webhookRepo                *repository.WebhookRepo
	channelTargetRepo          *repository.ChannelTargetRepo
	memorySvc                  *service.MemoryService
	agentLibraryMaintenanceSvc *service.AgentLibraryMaintenanceService
	agentSkillRoot             string
	authCfg                    *auth.Config
	authMode                   auth.AuthMode
	hostedSSOClient            *auth.HostedSSOClient
	hostedPendingStore         *auth.PendingStore
	hostedSSOKey               []byte
	hostedSSOInstanceID        string
	appBaseURL                 string
	desktopMode                bool
	buildIdentity              buildinfo.Build
	updateMode                 string
	distribution               string
	hostedAgentToken           string
	dockerAgentToken           string
	databaseSchema             int
	drainManager               *update.DrainManager
	updateCoordinator          *update.Coordinator
	updateWorkTracker          *update.WorkTracker
	systemReady                bool
	managedUpdateError         string
	pendingRemovalHook         func(string)
	pendingPublicationHook     func(string)
	githubRuntimeHook          func()

	loginFailuresMu   sync.Mutex
	loginFailureTimes []time.Time
	loginLockedUntil  time.Time
}

type ProjectFolderPicker func(ctx context.Context) (path string, canceled bool, err error)

type GitHubServiceProvider interface {
	GetConnectionStatus(ctx context.Context) (service.GitHubConnectionStatus, error)
	ConnectURL(ctx context.Context) (string, error)
	HandleInstallCallback(ctx context.Context, installationID string) error
	Disconnect(ctx context.Context) error
	CloneProjectRepo(ctx context.Context, projectID, repoURL string) (string, string, error)
	RecloneProjectRepo(ctx context.Context, projectID, currentRepoPath, repoURL string) (string, string, error)
	ResolveRepo(ctx context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error)
	DefaultBranch(ctx context.Context, repo *service.GitHubRepoRef) (string, error)
	PublishBranch(ctx context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error)
	FindPullRequestByBranch(ctx context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error)
	GetPullRequest(ctx context.Context, repo *service.GitHubRepoRef, number int) (*service.GitHubPullRequest, error)
	CreatePullRequest(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error)
	CreateIssue(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreateIssueRequest) (*service.GitHubIssue, error)
	GetIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubIssue, error)
	GetAuthenticatedUser(ctx context.Context) (*service.GitHubAuthenticatedUser, error)
	GetAuthenticatedUserForRepo(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, error)
	ListAuthenticatedAssignedIssues(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error)
	ListAuthenticatedCreatedIssues(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error)
	ListAssignedIssues(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssue, error)
	ListAssignedIssuesWithPullRequests(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssueWithPullRequest, error)
	FindPullRequestForIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubPullRequest, error)
	ListPullRequestFeedback(ctx context.Context, repo *service.GitHubRepoRef, prNumber int) ([]service.GitHubPullRequestFeedback, error)
	CommentOnIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, bodyText string) error
	AddLabelsToIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, labels []string) error
	CloseIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) error
	GlobalAPIEndpoint(ctx context.Context) string
}

type SlackServiceProvider interface {
	GetConnectionStatus(ctx context.Context) (service.SlackConnectionStatus, error)
	ConnectURL(ctx context.Context, redirectURI string) (string, error)
	HandleOAuthCallback(ctx context.Context, code, state, redirectURI string) error
	Disconnect(ctx context.Context) error
	ReloadFromSettings(ctx context.Context) error
	TestConnection(ctx context.Context) error
}

type EmailServiceProvider interface {
	Start() error
	Stop()
	IsRunning() bool
	ReloadFromSettings(ctx context.Context) error
	TestConnection(ctx context.Context) error
	GetConnectionStatus(ctx context.Context) service.EmailConnectionStatus
	SendTaskCompletionToThread(ctx context.Context, to, inboundMessageID, references, subject, taskTitle, output, errMsg string)
	SendChatResponse(ctx context.Context, task models.Task, output, errMsg string)
	SendTaskCompletionNotification(ctx context.Context, task models.Task, output, errMsg string)
}

type channelEmailStatusProviderSetter interface {
	SetEmailStatusProvider(func(context.Context) service.EmailConnectionStatus)
}

type channelEmailAuthRepoSetter interface {
	SetEmailAuthRepo(*repository.EmailAuthRepo)
}

type channelWebhookRepoSetter interface {
	SetWebhookRepo(*repository.WebhookRepo)
}

type DiscordServiceProvider interface {
	GetConnectionStatus(ctx context.Context) (service.DiscordConnectionStatus, error)
	ReloadFromSettings(ctx context.Context) error
	Disconnect(ctx context.Context) error
	TestConnection(ctx context.Context) error
}

func New(
	projectSvc *service.ProjectService,
	taskSvc *service.TaskService,
	llmSvc *service.LLMService,
	workerSvc *service.WorkerService,
	schedulerSvc *service.SchedulerService,
	alertSvc *service.AlertService,
	upcomingSvc *service.UpcomingService,
	insightsSvc *service.InsightsService,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	scheduleRepo *repository.ScheduleRepo,
	execRepo *repository.ExecutionRepo,
	workerRepo *repository.WorkerRepo,
	attachmentRepo *repository.AttachmentRepo,
	chatAttachmentRepo *repository.ChatAttachmentRepo,
	projectRepo *repository.ProjectRepo,
	settingsRepo *repository.SettingsRepo,
	broadcaster *events.Broadcaster,
	telegramSvc *service.TelegramService,
) *Handler {
	var threadInputRepo *repository.ThreadInputRepo
	var usageRepo *repository.UsageRepo
	var skillAnalyticsRepo *repository.SkillAnalyticsRepo
	var taskCommitStatRepo *repository.TaskCommitStatRepo
	var usageAnalyticsSvc *service.UsageAnalyticsService
	if execRepo != nil {
		if db := execRepo.DB(); db != nil {
			threadInputRepo = repository.NewThreadInputRepo(db)
			usageRepo = repository.NewUsageRepo(db)
			skillAnalyticsRepo = repository.NewSkillAnalyticsRepo(db)
			taskCommitStatRepo = repository.NewTaskCommitStatRepo(db)
			usageAnalyticsSvc = service.NewUsageAnalyticsService(usageRepo, llmConfigRepo)
		}
	}

	var h *Handler
	if llmSvc != nil && usageRepo != nil {
		llmSvc.SetUsageRepo(usageRepo)
	}
	if llmSvc != nil && skillAnalyticsRepo != nil {
		llmSvc.SetSkillAnalyticsRepo(skillAnalyticsRepo)
	}
	if llmSvc != nil && taskCommitStatRepo != nil {
		llmSvc.SetTaskCommitStatRepo(taskCommitStatRepo)
	}
	if upcomingSvc != nil && taskCommitStatRepo != nil {
		upcomingSvc.SetTaskCommitStatRepo(taskCommitStatRepo)
	}
	if workerSvc != nil && skillAnalyticsRepo != nil {
		workerSvc.SetSkillAnalyticsRepo(skillAnalyticsRepo)
	}
	var swarmSvc *service.SwarmService
	if taskSvc != nil && taskRepo != nil {
		swarmSvc = service.NewSwarmService(taskSvc, taskRepo, execRepo, workerSvc)
		swarmSvc.SetModelSelectionRepos(llmConfigRepo, projectRepo)
		taskSvc.SetSwarmService(swarmSvc)
		if schedulerSvc != nil {
			schedulerSvc.SetSwarmPlannerStarter(swarmSvc)
		}
		if workerSvc != nil {
			workerSvc.SetOnTaskComplete(func(task models.Task, executionErr error) {
				if models.IsSwarmChildRole(task.SwarmRole) && swarmSvc != nil {
					if err := swarmSvc.OnChildCompleted(context.Background(), task.ID); err != nil {
						applog.Infof("[swarm] completion callback failed task=%s role=%s: %v", task.ID, task.SwarmRole, err)
					}
				}
			})
		}
	}
	if llmSvc != nil && threadInputRepo != nil {
		llmSvc.SetThreadInputRepo(threadInputRepo)
		llmSvc.SetBroadcaster(broadcaster)
		llmSvc.SetQueuedTaskThreadPromoter(func(taskID string) {
			if h != nil {
				h.PromoteQueuedTaskThreadInput(taskID)
			}
		})
	}

	h = &Handler{
		projectSvc:          projectSvc,
		taskSvc:             taskSvc,
		swarmSvc:            swarmSvc,
		llmSvc:              llmSvc,
		workerSvc:           workerSvc,
		schedulerSvc:        schedulerSvc,
		alertSvc:            alertSvc,
		upcomingSvc:         upcomingSvc,
		insightsSvc:         insightsSvc,
		llmConfigRepo:       llmConfigRepo,
		taskRepo:            taskRepo,
		scheduleRepo:        scheduleRepo,
		execRepo:            execRepo,
		threadInputRepo:     threadInputRepo,
		usageRepo:           usageRepo,
		skillAnalyticsRepo:  skillAnalyticsRepo,
		taskCommitStatRepo:  taskCommitStatRepo,
		usageAnalyticsSvc:   usageAnalyticsSvc,
		workerRepo:          workerRepo,
		attachmentRepo:      attachmentRepo,
		chatAttachmentRepo:  chatAttachmentRepo,
		projectRepo:         projectRepo,
		settingsRepo:        settingsRepo,
		broadcaster:         broadcaster,
		telegramService:     telegramSvc,
		projectFolderPicker: pickProjectFolderNative,
	}
	if projectSvc != nil && taskSvc != nil {
		projectSvc.SetTaskService(taskSvc)
	}
	if taskSvc != nil {
		taskSvc.SetQueuedTaskThreadFollowupHook(h.StartPendingTaskThreadFollowup)
		taskSvc.SetFailedTaskThreadFollowupRetryHook(h.RetryLatestFailedTaskThreadFollowup)
	}
	return h
}

// SetChatBroadcaster sets the chat event broadcaster for real-time chat updates.
func (h *Handler) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	h.chatBroadcaster = cb
}

func (h *Handler) SetThreadInputRepo(repo *repository.ThreadInputRepo) {
	h.threadInputRepo = repo
}

func (h *Handler) SetUsageRepo(repo *repository.UsageRepo) {
	h.usageRepo = repo
	if h.llmSvc != nil {
		h.llmSvc.SetUsageRepo(repo)
	}
	if h.usageAnalyticsSvc == nil && repo != nil {
		h.usageAnalyticsSvc = service.NewUsageAnalyticsService(repo, h.llmConfigRepo)
	}
}

func (h *Handler) SetUsageAnalyticsService(svc *service.UsageAnalyticsService) {
	h.usageAnalyticsSvc = svc
}

func (h *Handler) SetTaskGoalService(svc *service.TaskGoalService) {
	h.taskGoalSvc = svc
}

// SetFileChangeBroadcaster sets the file change event broadcaster for real-time file change updates.
func (h *Handler) SetFileChangeBroadcaster(fcb *events.FileChangeBroadcaster) {
	h.fileChangeBroadcaster = fcb
}

func (h *Handler) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	h.executionStreamHub = hub
}

func (h *Handler) cancelActiveExecutionsAndPublish(ctx context.Context, taskID, operation string) {
	if h == nil || h.execRepo == nil {
		return
	}
	cancelledIDs, err := h.execRepo.CancelActiveByTaskReturningIDs(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] %s error cancelling active executions task=%s: %v", operation, taskID, err)
		return
	}
	if len(cancelledIDs) == 0 {
		return
	}
	applog.Infof("[handler] %s cancelled %d active executions task=%s", operation, len(cancelledIDs), taskID)
	for _, id := range cancelledIDs {
		h.publishExecutionTerminal(id, models.ExecCancelled, "cancelled")
	}
}

func (h *Handler) publishExecutionTerminal(execID string, status models.ExecutionStatus, errMsg string) {
	if h == nil {
		return
	}
	h.executionStreamHub.CloseTerminal(execID, status, errMsg)
}

// SetTelegramAuthRepo sets the Telegram authorization repo for managing authorized users.
func (h *Handler) SetTelegramAuthRepo(repo *repository.TelegramAuthRepo) {
	h.telegramAuthRepo = repo
}

// SetSlackAuthRepo sets the Slack authorization repo for managing authorized users.
func (h *Handler) SetSlackAuthRepo(repo *repository.SlackAuthRepo) {
	h.slackAuthRepo = repo
}

// SetEmailAuthRepo sets the Email authorization repo for managing authorized senders.
func (h *Handler) SetEmailAuthRepo(repo *repository.EmailAuthRepo) {
	h.emailAuthRepo = repo
	h.wireChannelIntegrationSummaryDeps(h.slackSvc)
	h.wireChannelIntegrationSummaryDeps(h.discordSvc)
	h.wireChannelIntegrationSummaryDeps(h.telegramService)
}

// SetDiscordAuthRepo sets the Discord authorization repo for managing authorized users.
func (h *Handler) SetDiscordAuthRepo(repo *repository.DiscordAuthRepo) {
	h.discordAuthRepo = repo
}

func (h *Handler) SetEmailTaskContextRepo(repo *repository.EmailTaskContextRepo) {
	h.emailTaskContextRepo = repo
}

func (h *Handler) SetDiscordTaskContextRepo(repo *repository.DiscordTaskContextRepo) {
	h.discordTaskContextRepo = repo
}

func (h *Handler) SetEmailService(svc EmailServiceProvider) {
	h.emailService = svc
	h.wireChannelIntegrationSummaryDeps(h.slackSvc)
	h.wireChannelIntegrationSummaryDeps(h.discordSvc)
	h.wireChannelIntegrationSummaryDeps(h.telegramService)
}

func (h *Handler) SetSlackTaskContextRepo(repo *repository.SlackTaskContextRepo) {
	h.slackTaskContextRepo = repo
}

// SetReviewCommentRepo sets the review comment repo for inline code review.
func (h *Handler) SetReviewCommentRepo(repo *repository.ReviewCommentRepo) {
	h.reviewCommentRepo = repo
}

// SetCustomPersonalityRepo sets the custom personality repo for managing custom personalities.
func (h *Handler) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	h.customPersonalityRepo = repo
}

// SetAgentRepo sets the agent definition repo for managing agents.
func (h *Handler) SetAgentRepo(repo *repository.AgentRepo) {
	h.agentRepo = repo
	if h.taskSvc != nil {
		h.taskSvc.SetAgentRepo(repo)
	}
}

// SetLifecycleRepo sets the lifecycle repo for agent hook management and
// lifecycle execution activity surfacing.
func (h *Handler) SetLifecycleRepo(repo *repository.LifecycleRepo) {
	h.lifecycleRepo = repo
}

// SetWorktreeService sets the worktree service for git worktree management.
func (h *Handler) SetWorktreeService(svc *service.WorktreeService) {
	h.worktreeSvc = svc
}

// SetTaskPullRequestRepo sets the task pull request repo for task PR records.
func (h *Handler) SetTaskPullRequestRepo(repo *repository.TaskPullRequestRepo) {
	h.taskPullRequestRepo = repo
}

func (h *Handler) SetTaskCommitStatRepo(repo *repository.TaskCommitStatRepo) {
	h.taskCommitStatRepo = repo
	if h.llmSvc != nil {
		h.llmSvc.SetTaskCommitStatRepo(repo)
	}
	if h.upcomingSvc != nil {
		h.upcomingSvc.SetTaskCommitStatRepo(repo)
	}
}

func (h *Handler) newTaskPullRequestService() *service.TaskPullRequestService {
	return service.NewTaskPullRequestService(h.githubSvc, h.taskPullRequestRepo).SetTaskCommitStatRepo(h.taskCommitStatRepo)
}

func (h *Handler) SetGitHubPRFeedbackRepo(repo *repository.GitHubPRFeedbackRepo) {
	h.githubPRFeedbackRepo = repo
}

func (h *Handler) SetGitHubAuthRepo(repo *repository.GitHubAuthRepo) {
	h.githubAuthRepo = repo
}

// SetGitHubService sets the GitHub service used for auth/import/PR flows.
func (h *Handler) SetGitHubService(svc GitHubServiceProvider) {
	h.githubSvc = svc
}

func (h *Handler) SetSlackService(svc SlackServiceProvider) {
	h.slackSvc = svc
	if setter, ok := svc.(automationGraphServiceSetter); ok {
		setter.SetAutomationGraphService(h.automationGraphSvc)
	}
	h.wireChannelIntegrationSummaryDeps(svc)
}

func (h *Handler) SetDiscordService(svc DiscordServiceProvider) {
	h.discordSvc = svc
	if setter, ok := svc.(automationGraphServiceSetter); ok {
		setter.SetAutomationGraphService(h.automationGraphSvc)
	}
	h.wireChannelIntegrationSummaryDeps(svc)
}

func (h *Handler) SetXRepositories(auth *repository.XAuthRepo, selections *repository.XUserProjectRepo, contexts *repository.XTaskContextRepo, receipts *repository.XInboundReceiptRepo) {
	h.xAuthRepo = auth
	h.xUserProjectRepo = selections
	h.xTaskContextRepo = contexts
	h.xInboundReceiptRepo = receipts
}

func (h *Handler) SetXService(svc *service.XService) {
	h.xServiceMu.Lock()
	h.xService = svc
	h.xServiceMu.Unlock()
	if h.llmSvc != nil {
		h.llmSvc.SetXService(svc)
	}
}

func (h *Handler) getXService() *service.XService {
	h.xServiceMu.RLock()
	defer h.xServiceMu.RUnlock()
	return h.xService
}

func (h *Handler) swapXService(svc *service.XService) *service.XService {
	h.xServiceMu.Lock()
	old := h.xService
	h.xService = svc
	h.xServiceMu.Unlock()
	if h.llmSvc != nil {
		h.llmSvc.SetXService(svc)
	}
	return old
}

// StopXService stops whichever dynamically configured X service is currently
// active. Server shutdown must not retain only the startup instance because the
// Channels settings flow can replace it at runtime.
func (h *Handler) StopXService() {
	h.xConfigMu.Lock()
	defer h.xConfigMu.Unlock()
	old := h.swapXService(nil)
	if h.channelMessageRouter != nil {
		h.channelMessageRouter.SetXService(nil)
	}
	if old != nil {
		old.Stop()
	}
}

func (h *Handler) SetChannelMessageRouter(router *service.ChannelMessageRouter) {
	h.channelMessageRouter = router
}

func (h *Handler) SetChannelTargetRepo(repo *repository.ChannelTargetRepo) {
	h.channelTargetRepo = repo
}

func (h *Handler) SetLocalRepoPathEnabled(enabled bool) {
	v := enabled
	h.localRepoPathEnabled = &v
}

// SetDesktopMode marks the handler as running inside the Wails desktop app.
// When true, the /open-external endpoint will open URLs in the system browser.
func (h *Handler) SetDesktopMode(desktop bool) {
	h.desktopMode = desktop
}

func (h *Handler) SetProjectFolderPicker(picker ProjectFolderPicker) {
	h.projectFolderPicker = picker
}

// SetWebhookRepo sets the webhook endpoint repository for inbound webhook management.
func (h *Handler) SetWebhookRepo(repo *repository.WebhookRepo) {
	h.webhookRepo = repo
	h.wireChannelIntegrationSummaryDeps(h.slackSvc)
	h.wireChannelIntegrationSummaryDeps(h.discordSvc)
	h.wireChannelIntegrationSummaryDeps(h.telegramService)
}

func (h *Handler) wireChannelIntegrationSummaryDeps(svc any) {
	if svc == nil {
		return
	}
	v := reflect.ValueOf(svc)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return
	}
	if setter, ok := svc.(channelEmailStatusProviderSetter); ok && h.emailService != nil {
		setter.SetEmailStatusProvider(h.emailService.GetConnectionStatus)
	}
	if setter, ok := svc.(channelEmailAuthRepoSetter); ok {
		setter.SetEmailAuthRepo(h.emailAuthRepo)
	}
	if setter, ok := svc.(channelWebhookRepoSetter); ok {
		setter.SetWebhookRepo(h.webhookRepo)
	}
}

// SetMemoryService wires the Memory service so project-create handlers
// can seed per-project memory storage and the Memory Consolidation
// scheduled task. The service no longer participates in task-time memory
// behavior; that runs through the Memory Curator agent's lifecycle hooks.
func (h *Handler) SetMemoryService(svc *service.MemoryService) {
	h.memorySvc = svc
}

func (h *Handler) SetAgentLibraryMaintenanceService(svc *service.AgentLibraryMaintenanceService) {
	h.agentLibraryMaintenanceSvc = svc
}

func (h *Handler) mutationProjectID(c echo.Context) string {
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" {
		return projectID
	}
	if h.settingsRepo == nil {
		return ""
	}
	selectedProjectID, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
	if err != nil {
		applog.Debugf("[handler] failed to load selected project preference for mutation: %v", err)
		return ""
	}
	return strings.TrimSpace(selectedProjectID)
}

// getCurrentProjectID resolves the current project ID from the query param.
// If project_id is provided and valid, it uses GetByID to verify it exists.
// Otherwise it falls back to the first compact selector option.
func (h *Handler) getCurrentProjectID(c echo.Context) (string, error) {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	if projectID != "" && projectID != "default" {
		p, err := h.projectSvc.GetByID(ctx, projectID)
		if err != nil {
			return "", err
		}
		if p != nil {
			return projectID, nil
		}
	}
	if h.settingsRepo != nil {
		selectedProjectID, err := h.settingsRepo.Get(ctx, uiPreferenceSelectedProjectIDKey)
		if err != nil {
			applog.Debugf("[handler] failed to load selected project preference: %v", err)
		} else if selectedProjectID = strings.TrimSpace(selectedProjectID); selectedProjectID != "" {
			p, err := h.projectSvc.GetByID(ctx, selectedProjectID)
			if err != nil {
				return "", err
			}
			if p != nil {
				return selectedProjectID, nil
			}
		}
	}
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return "", err
	}
	if len(projects) > 0 {
		return projects[0].ID, nil
	}
	return "", nil
}

// isHTMX returns true for ordinary HTMX fragment requests. History cache misses
// request a complete document so HTMX can restore the application shell and title.
func isHTMX(c echo.Context) bool {
	return c.Request().Header.Get("HX-Request") == "true" &&
		c.Request().Header.Get("HX-History-Restore-Request") != "true"
}

// parseIntClamped parses a form value as an integer and clamps it to [min, max].
// Returns min if the value is empty or invalid.
func parseIntClamped(value string, min, max int) int {
	if value == "" {
		return min
	}
	v, err := strconv.Atoi(value)
	if err != nil || v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (h *Handler) RegisterRoutes(e *echo.Echo) {
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("handler", h)
			return next(c)
		}
	})
	e.GET("/favicon.png", h.AppIconPNG)
	e.GET("/favicon.ico", h.AppIconICO)

	// Machine-readable readiness and immutable build identity.
	e.GET("/api/system/health", h.SystemHealth)
	e.POST("/ui/preferences", h.SaveUIPreferences)

	// Machine-readable system update state and administrator actions.
	e.GET("/api/system/update", h.SystemUpdate)
	e.POST("/api/system/update/apply", h.ApplySystemUpdate)
	e.POST("/api/system/update/cancel", h.CancelSystemUpdate)
	e.GET("/api/system/update/events", h.SystemUpdateEvents)

	// Swagger API documentation
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	// Desktop system-browser redirect (desktop mode only; 404 in server mode)
	e.POST("/open-external", h.OpenExternal)

	// Authentication
	e.GET("/login", h.AuthLoginPage)
	e.POST("/login", h.AuthLogin)
	e.POST("/logout", h.AuthLogout)
	e.GET("/auth/me", h.AuthMe)
	e.GET("/auth/sso/start", h.HostedSSOStart)
	e.GET("/auth/sso/callback", h.HostedSSOCallback)
	e.GET("/logged-out", h.LoggedOut)

	// Dashboard
	e.GET("/", h.Home)
	e.GET("/analytics", h.Analytics)

	// Analytics API endpoints
	e.GET("/api/analytics/usage", h.GetAnalyticsUsage)
	e.GET("/api/analytics/skills", h.GetSkillAnalytics)
	e.GET("/api/analytics/success-failure-rates", h.GetSuccessFailureRates)
	e.GET("/api/analytics/avg-execution-time-by-task", h.GetAvgExecutionTimeByTask)
	e.GET("/api/analytics/avg-execution-time-by-agent", h.GetAvgExecutionTimeByAgent)
	e.GET("/api/analytics/execution-trends-by-hour", h.GetExecutionTrendsByHour)
	e.GET("/api/analytics/agent-usage-by-project", h.GetAgentUsageByProject)
	e.GET("/api/analytics/most-frequent-tasks", h.GetMostFrequentTasks)
	e.GET("/api/analytics/failed-task-patterns", h.GetFailedTaskPatterns)

	// Projects
	e.GET("/projects", h.ListProjects)
	e.GET("/projects/new", h.NewProjectDialog)
	e.POST("/projects/pick-folder", h.PickProjectFolder)
	e.POST("/projects", h.CreateProject)
	e.PUT("/projects/:id", h.UpdateProject)
	e.DELETE("/projects/:id", h.DeleteProject)
	e.GET("/projects/:id/edit", h.EditProjectDialog)

	// Automations (project-scoped via ?project_id= query param)
	e.GET("/breadcrumb-selectors/:resource", h.GetBreadcrumbSelectorResults)
	e.GET("/automations", h.ListAutomations)
	e.POST("/automations/builder", h.BuildAutomationWeb)
	e.POST("/automations/yaml/parse", h.ParseAutomationYAML)
	e.GET("/automations/:automationId/builder", h.EditAutomationBuilder)
	e.POST("/automations/:automationId/builder", h.EditAutomationBuilder)
	e.POST("/automations/:automationId/run-now", h.RunAutomationNow)
	e.POST("/automations/:automationId/pause", h.PauseAutomation)
	e.POST("/automations/:automationId/resume", h.ResumeAutomation)
	e.POST("/automations/:automationId/refresh-external", h.RefreshAutomationExternalState)
	e.DELETE("/automations/bulk", h.DeleteAutomationsBulk)
	e.POST("/automations/:automationId/delete", h.DeleteAutomation)
	e.GET("/automations/:automationId", h.GetAutomationLive)

	// Tasks (project-scoped via ?project_id= query param)
	e.GET("/tasks", h.ListTasks)
	e.GET("/schedule", h.ViewSchedule)
	e.POST("/tasks", h.CreateTask)
	e.POST("/tasks/move-completed", h.MoveCompletedActiveToCompleted)
	e.DELETE("/tasks/completed", h.DeleteAllCompletedTasks)
	e.DELETE("/tasks/backlog", h.DeleteAllBacklogTasks)
	e.POST("/tasks/backlog/activate", h.ActivateAllBacklogTasks)
	e.POST("/tasks/backlog/execute", h.ExecuteBacklogTasks)
	e.GET("/tasks/backlog/priority-counts", h.CountBacklogByPriority)
	e.POST("/tasks/backlog/sort", h.SetBacklogSort)
	e.POST("/tasks/completed/sort", h.SetCompletedSort)
	e.PATCH("/tasks/batch-category", h.BatchUpdateTaskCategory)

	// Tasks (task-specific, no project in URL)
	e.GET("/tasks/:taskId/executions", h.GetTaskExecutions)
	e.GET("/tasks/:taskId/detail-status", h.GetTaskDetailStatus)
	e.GET("/tasks/:taskId/detail-actions", h.GetTaskDetailActions)
	e.GET("/tasks/:taskId/changes", h.GetTaskChanges)
	e.GET("/tasks/:taskId/changes/file", h.GetTaskChangesFile)
	e.POST("/tasks/:taskId/changes/live", h.GetTaskChangesLive)
	e.GET("/tasks/:taskId/thread", h.GetTaskThread)
	e.GET("/tasks/:taskId/thread/composer-action", h.TaskThreadComposerAction)
	e.GET("/tasks/:taskId/thread/executions/:execId/fragment", h.GetTaskThreadExecutionFragment)
	e.POST("/tasks/:taskId/thread", h.TaskThreadSend)
	e.POST("/tasks/:taskId/thread/model", h.TaskThreadSelectModel)
	e.POST("/tasks/:taskId/thread/steer", h.TaskThreadSteer)
	e.GET("/tasks/:taskId/thread/pending-inputs", h.TaskThreadPendingInputs)
	e.POST("/thread-inputs/:inputId/cancel", h.CancelThreadInput)
	e.POST("/tasks/:taskId/thread/queued/:inputId/steer", h.TaskThreadQueuedInputSteer)
	e.GET("/tasks/:taskId/goal", h.GetTaskGoal)
	e.POST("/tasks/:taskId/goal", h.SetTaskGoal)
	e.POST("/tasks/:taskId/goal/pause", h.PauseTaskGoal)
	e.POST("/tasks/:taskId/goal/resume", h.ResumeTaskGoal)
	e.POST("/tasks/:taskId/goal/clear", h.ClearTaskGoal)
	e.GET("/tasks/:taskId", h.GetTask)
	e.PUT("/tasks/:taskId", h.UpdateTask)
	e.DELETE("/tasks/:taskId", h.DeleteTask)
	e.POST("/tasks/:taskId/run", h.RunTask)
	e.POST("/tasks/:taskId/cancel", h.CancelTask)
	e.GET("/api/tasks/:id/swarm", h.GetSwarm)
	e.POST("/api/tasks/:id/swarm/start", h.StartSwarm)
	e.POST("/api/tasks/:id/swarm/followup", h.SwarmFollowup)
	e.POST("/api/tasks/:id/swarm/cancel", h.CancelSwarm)
	e.POST("/api/tasks/:id/swarm/rerun-reviewer", h.RerunSwarmReviewer)
	e.POST("/api/tasks/:id/swarm/rerun-merger", h.RerunSwarmMerger)
	e.POST("/api/tasks/:id/swarm/rerun-integrator", h.RerunSwarmMerger)
	e.PATCH("/tasks/:taskId/category", h.UpdateTaskCategory)
	e.PATCH("/tasks/:taskId/status", h.UpdateTaskStatus)
	e.PATCH("/tasks/:taskId/reorder", h.ReorderTask)
	e.PUT("/tasks/:taskId/chain", h.UpdateTaskChainConfig)

	// Schedules
	e.POST("/tasks/:taskId/schedule", h.CreateSchedule)
	e.POST("/schedules/:id", h.UpdateSchedule) // Native form fallback; HTMX uses PUT below.
	e.PUT("/schedules/:id", h.UpdateSchedule)
	e.DELETE("/schedules/:id", h.DeleteSchedule)
	e.POST("/schedules/:id/toggle", h.ToggleScheduleEnabled)
	e.PATCH("/schedules/:scheduleId/reschedule", h.RescheduleTask)

	// Attachments
	e.POST("/tasks/:taskId/attachments", h.UploadAttachment)
	e.DELETE("/attachments/:id", h.DeleteAttachment)

	// Executions
	e.GET("/executions/:id", h.GetExecution)

	// Model configs
	// Agent definitions
	e.GET("/agents", h.ListAgents)
	e.POST("/agents", h.CreateAgent)
	e.POST("/agents/generate", h.GenerateAgent)
	e.GET("/agents/plugins/state", h.GetPluginState)
	e.POST("/agents/plugins/marketplaces", h.AddPluginMarketplace)
	e.POST("/agents/plugins/marketplaces/:name/update", h.UpdatePluginMarketplace)
	e.DELETE("/agents/plugins/marketplaces/:name", h.DeletePluginMarketplace)
	e.POST("/agents/plugins/marketplaces/reset-defaults", h.ResetPluginMarketplaces)
	e.POST("/agents/plugins/install", h.InstallPlugin)
	e.POST("/agents/plugins/uninstall", h.UninstallPlugin)
	e.GET("/agents/:id/json", h.GetAgentJSON)
	e.GET("/agents/:id/skills", h.GetAgentSkills)
	e.POST("/agents/:id/skills", h.CreateAgentOwnedSkill)
	e.PUT("/agents/:id/skills/:skill", h.UpdateAgentOwnedSkill)
	e.POST("/agents/:id/skills/:skill/archive", h.ArchiveAgentOwnedSkill)
	e.GET("/skills", h.ListSkills)
	e.POST("/skills", h.CreateSkill)
	e.POST("/skills/import", h.ImportSkillPackage)
	e.GET("/skills/:skill/details", h.GetSkillDetail)
	e.PUT("/skills/:skill", h.UpdateSkill)
	e.POST("/skills/:skill/enabled", h.SetSkillEnabled)
	e.POST("/skills/:skill/always_use", h.SetSkillAlwaysUse)
	e.DELETE("/skills/bulk", h.DeleteSkillsBulk)
	e.DELETE("/skills/:skill", h.DeleteSkill)
	e.PUT("/agents/:id", h.UpdateAgent)
	e.DELETE("/agents/bulk", h.DeleteAgentsBulk)
	e.DELETE("/agents/:id", h.DeleteAgent)
	// Lifecycle hooks (runbook §Agent Create/Edit Dialog → Lifecycle Hooks Tab)
	e.GET("/agents/:id/lifecycle-hooks", h.GetAgentLifecycleHooks)
	e.PUT("/agents/:id/lifecycle-hooks", h.SaveAgentLifecycleHooks)
	// Lifecycle execution activity (runbook §Rollout step 17)
	e.GET("/api/tasks/:id/lifecycle-executions/:executionID", h.GetTaskLifecycleExecution)
	e.GET("/api/tasks/:id/lifecycle-executions", h.GetTaskLifecycleExecutions)
	e.GET("/api/lifecycle-executions/:id/events", h.GetLifecycleExecutionEvents)

	e.GET("/models", h.ListModels)
	e.POST("/models", h.CreateModel)
	e.GET("/models/:id/edit-details", h.GetModelEditDetails)
	e.GET("/models/openai-compatible/available", h.ListOpenAICompatibleAvailableModels)
	e.GET("/models/ollama/available", h.ListOllamaAvailableModels)
	e.POST("/models/:id", h.UpdateModel)
	e.PUT("/models/:id", h.UpdateModel)
	e.POST("/models/:id/set-default", h.SetDefaultModel)
	e.DELETE("/models/bulk", h.DeleteModelsBulk)
	e.DELETE("/models/:id", h.DeleteModel)

	// OAuth for model providers
	e.GET("/models/:id/oauth/initiate", h.OAuthInitiate)
	e.POST("/models/oauth/manual-complete", h.OAuthManualComplete)
	e.GET("/callback", h.OAuthCallback)              // Anthropic public-mode callback
	e.GET("/auth/callback", h.OAuthCallback)         // OpenAI public-mode callback
	e.GET("/models/oauth/callback", h.OAuthCallback) // Legacy/fallback
	e.GET("/models/:id/oauth/status", h.OAuthStatus)

	// Worker settings
	e.GET("/workers", h.WorkerSettings)
	e.POST("/workers", h.UpdateWorkerSettings)
	e.POST("/workers/projects/:projectId/limit", h.UpdateProjectWorkerLimit)
	// Worker stats polling endpoints
	e.GET("/workers/stats/global", h.GlobalWorkerStats)
	e.GET("/workers/stats/projects", h.ProjectWorkerStats)
	e.GET("/workers/stats/models", h.ModelWorkerStats)

	// Capacity API endpoints
	e.POST("/api/schedules/:id/toggle", h.APIToggleScheduleEnabled)

	e.GET("/api/capacity/global", h.GetGlobalCapacity)
	e.GET("/api/capacity/projects", h.GetProjectCapacities)
	e.GET("/api/capacity/projects/:projectId", h.GetProjectCapacity)
	e.GET("/api/capacity/models", h.GetModelCapacities)
	e.GET("/api/capacity/models/:modelId", h.GetModelCapacity)

	// Channels (Integrations)
	e.GET("/channels", h.handleChannels)
	e.GET("/channels/github/runtime-settings", h.GitHubRuntimeSettingsFragment)
	e.POST("/channels/telegram", h.handleTelegramSave)
	e.POST("/channels/telegram/test", h.handleTelegramTest)
	e.POST("/channels/telegram/remove", h.handleTelegramRemove)
	e.POST("/channels/telegram/send-responses", h.handleTelegramSendResponses)
	e.POST("/channels/github/configure", h.handleGitHubConfigure)
	e.GET("/channels/github/connect", h.handleGitHubConnect)
	e.GET("/channels/github/callback", h.handleGitHubCallback)
	e.POST("/channels/github/disconnect", h.handleGitHubDisconnect)
	e.POST("/channels/github/remove", h.handleGitHubRemove)
	e.POST("/channels/slack/configure", h.handleSlackConfigure)
	e.GET("/channels/slack/connect", h.handleSlackConnect)
	e.GET("/channels/slack/callback", h.handleSlackCallback)
	e.POST("/channels/slack/disconnect", h.handleSlackDisconnect)
	e.POST("/channels/slack/remove", h.handleSlackRemove)
	e.POST("/channels/slack/test", h.handleSlackTest)
	e.POST("/channels/discord/configure", h.handleDiscordConfigure)
	e.POST("/channels/discord/remove", h.handleDiscordRemove)
	e.POST("/channels/discord/test", h.handleDiscordTest)
	e.POST("/channels/x/configure", h.handleXConfigure)
	e.POST("/channels/x/test", h.handleXTest)
	e.POST("/channels/x/remove", h.handleXRemove)
	e.POST("/channels/x/authorized-users", h.AddXAuthorizedUser)
	e.DELETE("/channels/x/authorized-users/:id", h.RemoveXAuthorizedUser)
	e.POST("/channels/email/configure", h.handleEmailConfigure)
	e.POST("/channels/email/remove", h.handleEmailRemove)
	e.POST("/channels/email/test", h.handleEmailTest)
	e.GET("/channels/outbound-targets", h.handleOutboundTargetsFragment)
	e.GET("/channels/outbound-targets/card", h.handleOutboundTargetsCardFragment)
	e.POST("/channels/outbound-targets/test-draft", h.handleOutboundTargetDraftTest)
	e.POST("/channels/outbound-targets/:id/test", h.handleOutboundTargetTest)
	e.POST("/channels/send-message-explicit-targets", h.handleSendMessageExplicitTargets)

	// Personality
	e.GET("/personality", h.handleAppSettings)
	e.POST("/personality/save", h.handlePersonalitySave)

	// Custom personalities
	e.POST("/personality/custom", h.CreateCustomPersonality)
	e.GET("/personality/custom/:key", h.GetCustomPersonality)
	e.PUT("/personality/custom/:key", h.UpdateCustomPersonality)
	e.DELETE("/personality/custom/bulk", h.DeleteCustomPersonalitiesBulk)
	e.DELETE("/personality/custom/:key", h.DeleteCustomPersonality)

	// GitHub runtime trust and inbox settings
	e.POST("/channels/github/authorized-actors", h.AddGitHubAuthorizedActor)
	e.DELETE("/channels/github/authorized-actors/:id", h.RemoveGitHubAuthorizedActor)
	e.POST("/channels/github/project-inbox", h.SaveGitHubProjectInbox)

	// Telegram authorized users
	e.GET("/channels/telegram/authorized-users", h.ListTelegramAuthorizedUsers)
	e.POST("/channels/telegram/authorized-users", h.AddTelegramAuthorizedUser)
	e.DELETE("/channels/telegram/authorized-users/:id", h.RemoveTelegramAuthorizedUser)

	// Slack authorized users
	e.GET("/channels/slack/authorized-users", h.ListSlackAuthorizedUsers)
	e.POST("/channels/slack/authorized-users", h.AddSlackAuthorizedUser)
	e.DELETE("/channels/slack/authorized-users/:id", h.RemoveSlackAuthorizedUser)

	// Email authorized senders
	e.GET("/channels/email/authorized-senders", h.ListEmailAuthorizedSenders)
	e.POST("/channels/email/authorized-senders", h.AddEmailAuthorizedSender)
	e.DELETE("/channels/email/authorized-senders/:id", h.RemoveEmailAuthorizedSender)

	// Discord authorized users
	e.GET("/channels/discord/authorized-users", h.ListDiscordAuthorizedUsers)
	e.POST("/channels/discord/authorized-users", h.AddDiscordAuthorizedUser)
	e.DELETE("/channels/discord/authorized-users/:id", h.RemoveDiscordAuthorizedUser)

	// Webhooks
	e.POST("/channels/webhooks", h.HandleWebhookCreate)
	e.GET("/channels/webhooks/:id", h.HandleWebhookDetail)
	e.PUT("/channels/webhooks/:id", h.HandleWebhookUpdate)
	e.DELETE("/channels/webhooks/bulk", h.HandleWebhookBulkDelete)
	e.DELETE("/channels/webhooks/:id", h.HandleWebhookDelete)
	e.POST("/channels/webhooks/:id/rotate-secret", h.HandleWebhookRotateSecret)
	e.POST("/channels/webhooks/:id/test", h.HandleWebhookTest)

	// Inbound webhook endpoint (generic, no auth middleware)
	e.POST("/webhooks/inbound/:pathToken", h.HandleWebhookInbound)

	// Git Worktree
	e.GET("/tasks/:taskId/card/merge-options", h.GetTaskCardMergeOptions)
	e.GET("/tasks/:taskId/worktree", h.GetTaskWorktreeInfo)
	e.POST("/tasks/:taskId/worktree/auto-merge", h.UpdateTaskAutoMerge)
	e.POST("/tasks/:taskId/worktree/merge", h.MergeTaskBranch)
	e.POST("/tasks/:taskId/worktree/rebase", h.RebaseTaskBranch)
	e.POST("/tasks/:taskId/worktree/pull-request", h.CreateTaskPullRequest)
	e.POST("/tasks/:taskId/worktree/resolve", h.ResolveTaskConflicts)
	e.POST("/tasks/:taskId/worktree/abort", h.AbortTaskMerge)
	e.POST("/tasks/:taskId/worktree/cleanup", h.CleanupTaskWorktree)
	e.GET("/tasks/:taskId/changes/worktree", h.GetTaskChangesWorktree)
	e.POST("/settings/worktree", h.UpdateWorktreeSettings)

	// Code Review Comments
	e.GET("/tasks/:taskId/reviews", h.ListReviewComments)
	e.POST("/tasks/:taskId/reviews", h.AddReviewComment)
	e.PATCH("/reviews/:id", h.UpdateReviewComment)
	e.DELETE("/reviews/:id", h.DeleteReviewComment)
	e.POST("/tasks/:taskId/reviews/submit", h.SubmitReview)

	// Chat
	e.GET("/chat", h.Chat)
	e.POST("/chat/send", h.ChatSend)
	e.POST("/chat/stop", h.ChatStop)
	e.GET("/chat/composer-action", h.ChatComposerAction)
	e.POST("/chat/steer", h.ChatSteer)
	e.GET("/chat/pending-inputs", h.ChatPendingInputs)
	e.POST("/chat/queued/:inputId/steer", h.ChatQueuedInputSteer)

	// API endpoints (for Chrome extension)
	e.GET("/api/projects", h.APIGetProjects)
	e.POST("/api/chat/message", h.APIChatMessage)
	e.GET("/api/chat/message/:id", h.APIChatMessageStatus)
	e.DELETE("/chat/history", h.ClearChat)
	e.POST("/chat/attachments", h.UploadChatAttachment)
	e.GET("/chat/attachments/:id/download", h.DownloadChatAttachment)
	e.DELETE("/chat/attachments/:id", h.DeleteChatAttachment)

	// Brief & Debrief
	e.GET("/upcoming", h.ViewUpcoming)
	e.POST("/upcoming/summary", h.GeneratePulseSummary)
	e.GET("/history", h.ViewHistory)
	e.POST("/history/summary", h.GenerateReflectionSummary)

	// Alerts
	e.GET("/alerts", h.ListAlerts)
	e.GET("/alerts/:id/details", h.GetAlertDetail)
	e.POST("/alerts/:id/read", h.MarkAlertRead)
	e.POST("/alerts/:id/approve", h.ApproveAlert)
	e.POST("/alerts/:id/reject", h.RejectAlert)
	e.POST("/alerts/:id/dismiss", h.DismissAlert)
	e.POST("/alerts/read-all", h.MarkAllAlertsRead)
	e.DELETE("/alerts/bulk", h.DeleteAlertsBulk)
	e.DELETE("/alerts/:id", h.DeleteAlert)
	e.DELETE("/alerts", h.DeleteAllAlerts)
	e.GET("/alerts/unread-count", h.GetUnreadAlertCount)

	// Proactive Insights (individual endpoints still work)
	e.GET("/insights", h.ProactiveInsights)
	e.POST("/insights/analyze", h.RunInsightsAnalysis)
	e.POST("/insights/extract-knowledge", h.ExtractInsightsKnowledge)
	e.PATCH("/insights/:id/status", h.UpdateInsightStatus)
	e.DELETE("/insights/:id", h.DeleteInsight)
	e.GET("/insights/by-type", h.ListInsightsByType)
	e.DELETE("/insights/knowledge/:id", h.DeleteKnowledgeEntry)
	e.GET("/insights/reports", h.ListInsightReports)
	e.POST("/insights/health-check", h.RunHealthCheck)
	e.POST("/history/grade-ideas", h.GradeIdeas)

	// Server-Sent Events for real-time updates
	e.GET("/events/live", h.LiveEventsSSE)
	e.GET("/events/chat/:exec_id", h.ChatStreamSSE)
}

// AppIconPNG serves the shared browser icon for server and desktop web views.
func (h *Handler) AppIconPNG(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	return c.Blob(http.StatusOK, "image/png", desktopicons.BrowserPNG)
}

// AppIconICO serves the conventional multi-resolution favicon resource.
func (h *Handler) AppIconICO(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	return c.Blob(http.StatusOK, "image/x-icon", desktopicons.BrowserICO)
}
