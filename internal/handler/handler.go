package handler

import (
	"context"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	echoSwagger "github.com/swaggo/echo-swagger"
)

type Handler struct {
	projectSvc                     *service.ProjectService
	taskSvc                        *service.TaskService
	llmSvc                         *service.LLMService
	workerSvc                      *service.WorkerService
	schedulerSvc                   *service.SchedulerService
	alertSvc                       *service.AlertService
	upcomingSvc                    *service.UpcomingService
	workflowSvc                    *service.WorkflowService
	collisionSvc                   *service.CollisionService
	insightsSvc                    *service.InsightsService
	architectSvc                   *service.ArchitectService
	backlogSvc                     *service.BacklogService
	autonomousTriggerSvc           *service.AutonomousTriggerService
	trendSvc                       *service.TrendIntelligenceService
	templateSvc                    *service.TemplateService
	patternSvc                     *service.PatternService
	llmConfigRepo                  *repository.LLMConfigRepo
	taskRepo                       *repository.TaskRepo
	scheduleRepo                   *repository.ScheduleRepo
	execRepo                       *repository.ExecutionRepo
	workerRepo                     *repository.WorkerRepo
	attachmentRepo                 *repository.AttachmentRepo
	chatAttachmentRepo             *repository.ChatAttachmentRepo
	projectRepo                    *repository.ProjectRepo
	settingsRepo                   *repository.SettingsRepo
	broadcaster                    *events.Broadcaster
	chatBroadcaster                *events.ChatBroadcaster
	fileChangeBroadcaster          *events.FileChangeBroadcaster
	telegramService                *service.TelegramService
	telegramAuthRepo               *repository.TelegramAuthRepo
	slackAuthRepo                  *repository.SlackAuthRepo
	reviewCommentRepo              *repository.ReviewCommentRepo
	customPersonalityRepo          *repository.CustomPersonalityRepo
	agentRepo                      *repository.AgentRepo
	worktreeSvc                    *service.WorktreeService
	taskPullRequestRepo            *repository.TaskPullRequestRepo
	githubSvc                      GitHubServiceProvider
	slackSvc                       SlackServiceProvider
	localRepoPathEnabled           *bool
	taskChangesMergeOptionsEnabled *bool
	projectFolderPicker            ProjectFolderPicker
	webhookRepo                    *repository.WebhookRepo
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
	PushBranch(ctx context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error
	FindPullRequestByBranch(ctx context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error)
	CreatePullRequest(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error)
}

type SlackServiceProvider interface {
	GetConnectionStatus(ctx context.Context) (service.SlackConnectionStatus, error)
	ConnectURL(ctx context.Context, redirectURI string) (string, error)
	HandleOAuthCallback(ctx context.Context, code, state, redirectURI string) error
	Disconnect(ctx context.Context) error
	ReloadFromSettings(ctx context.Context) error
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
	workflowSvc *service.WorkflowService,
	collisionSvc *service.CollisionService,
	insightsSvc *service.InsightsService,
	architectSvc *service.ArchitectService,
	backlogSvc *service.BacklogService,
	autonomousTriggerSvc *service.AutonomousTriggerService,
	trendSvc *service.TrendIntelligenceService,
	templateSvc *service.TemplateService,
	patternSvc *service.PatternService,
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
	return &Handler{
		projectSvc:           projectSvc,
		taskSvc:              taskSvc,
		llmSvc:               llmSvc,
		workerSvc:            workerSvc,
		schedulerSvc:         schedulerSvc,
		alertSvc:             alertSvc,
		upcomingSvc:          upcomingSvc,
		workflowSvc:          workflowSvc,
		collisionSvc:         collisionSvc,
		insightsSvc:          insightsSvc,
		architectSvc:         architectSvc,
		backlogSvc:           backlogSvc,
		autonomousTriggerSvc: autonomousTriggerSvc,
		trendSvc:             trendSvc,
		templateSvc:          templateSvc,
		patternSvc:           patternSvc,
		llmConfigRepo:        llmConfigRepo,
		taskRepo:             taskRepo,
		scheduleRepo:         scheduleRepo,
		execRepo:             execRepo,
		workerRepo:           workerRepo,
		attachmentRepo:       attachmentRepo,
		chatAttachmentRepo:   chatAttachmentRepo,
		projectRepo:          projectRepo,
		settingsRepo:         settingsRepo,
		broadcaster:          broadcaster,
		telegramService:      telegramSvc,
		projectFolderPicker:  pickProjectFolderNative,
	}
}

// SetChatBroadcaster sets the chat event broadcaster for real-time chat updates.
func (h *Handler) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	h.chatBroadcaster = cb
}

// SetFileChangeBroadcaster sets the file change event broadcaster for real-time file change updates.
func (h *Handler) SetFileChangeBroadcaster(fcb *events.FileChangeBroadcaster) {
	h.fileChangeBroadcaster = fcb
}

// SetTelegramAuthRepo sets the Telegram authorization repo for managing authorized users.
func (h *Handler) SetTelegramAuthRepo(repo *repository.TelegramAuthRepo) {
	h.telegramAuthRepo = repo
}

// SetSlackAuthRepo sets the Slack authorization repo for managing authorized users.
func (h *Handler) SetSlackAuthRepo(repo *repository.SlackAuthRepo) {
	h.slackAuthRepo = repo
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
}

// SetWorktreeService sets the worktree service for git worktree management.
func (h *Handler) SetWorktreeService(svc *service.WorktreeService) {
	h.worktreeSvc = svc
}

// SetTaskPullRequestRepo sets the task pull request repo for task PR records.
func (h *Handler) SetTaskPullRequestRepo(repo *repository.TaskPullRequestRepo) {
	h.taskPullRequestRepo = repo
}

// SetGitHubService sets the GitHub service used for auth/import/PR flows.
func (h *Handler) SetGitHubService(svc GitHubServiceProvider) {
	h.githubSvc = svc
}

func (h *Handler) SetSlackService(svc SlackServiceProvider) {
	h.slackSvc = svc
}

func (h *Handler) SetLocalRepoPathEnabled(enabled bool) {
	v := enabled
	h.localRepoPathEnabled = &v
}

func (h *Handler) SetTaskChangesMergeOptionsEnabled(enabled bool) {
	v := enabled
	h.taskChangesMergeOptionsEnabled = &v
}

func (h *Handler) SetProjectFolderPicker(picker ProjectFolderPicker) {
	h.projectFolderPicker = picker
}

// SetWebhookRepo sets the webhook endpoint repository for inbound webhook management.
func (h *Handler) SetWebhookRepo(repo *repository.WebhookRepo) {
	h.webhookRepo = repo
}

// getCurrentProjectID resolves the current project ID from the query param.
// If project_id is provided and valid, it uses GetByID to verify it exists.
// Otherwise it falls back to listing all projects and using the first one.
func (h *Handler) getCurrentProjectID(c echo.Context) (string, error) {
	projectID := c.QueryParam("project_id")
	if projectID != "" && projectID != "default" {
		p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
		if err != nil {
			return "", err
		}
		if p != nil {
			return projectID, nil
		}
	}
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return "", err
	}
	if len(projects) > 0 {
		return projects[0].ID, nil
	}
	return "", nil
}

// isHTMX returns true if the request was initiated by HTMX.
func isHTMX(c echo.Context) bool {
	return c.Request().Header.Get("HX-Request") == "true"
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
	// Swagger API documentation
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	// Dashboard
	e.GET("/", h.Home)
	e.GET("/dashboard", h.Dashboard)
	e.GET("/dashboard-mockup", h.DashboardMockup)
	e.POST("/dashboard-mockup/actions", h.DashboardMockupAction)
	e.GET("/analytics", h.Analytics)

	// Analytics API endpoints
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
	e.GET("/tasks/:taskId/changes", h.GetTaskChanges)
	e.GET("/tasks/:taskId/changes/file", h.GetTaskChangesFile)
	e.POST("/tasks/:taskId/changes/live", h.GetTaskChangesLive)
	e.GET("/tasks/:taskId/thread", h.GetTaskThread)
	e.POST("/tasks/:taskId/thread", h.TaskThreadSend)
	e.GET("/tasks/:taskId", h.GetTask)
	e.PUT("/tasks/:taskId", h.UpdateTask)
	e.DELETE("/tasks/:taskId", h.DeleteTask)
	e.POST("/tasks/:taskId/run", h.RunTask)
	e.POST("/tasks/:taskId/cancel", h.CancelTask)
	e.PATCH("/tasks/:taskId/category", h.UpdateTaskCategory)
	e.PATCH("/tasks/:taskId/status", h.UpdateTaskStatus)
	e.PATCH("/tasks/:taskId/reorder", h.ReorderTask)
	e.PUT("/tasks/:taskId/chain", h.UpdateTaskChainConfig)

	// Schedules
	e.POST("/tasks/:taskId/schedule", h.CreateSchedule)
	e.PUT("/schedules/:id", h.UpdateSchedule)
	e.DELETE("/schedules/:id", h.DeleteSchedule)
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
	e.PUT("/agents/:id", h.UpdateAgent)
	e.DELETE("/agents/:id", h.DeleteAgent)
	e.GET("/agents/:id/json", h.GetAgentJSON)

	e.GET("/models", h.ListModels)
	e.POST("/models", h.CreateModel)
	e.GET("/models/ollama/available", h.ListOllamaAvailableModels)
	e.PUT("/models/:id", h.UpdateModel)
	e.POST("/models/:id/set-default", h.SetDefaultModel)
	e.DELETE("/models/:id", h.DeleteModel)

	// OAuth for model providers
	e.GET("/models/:id/oauth/initiate", h.OAuthInitiate)
	e.POST("/models/oauth/manual-complete", h.OAuthManualComplete)
	e.GET("/callback", h.OAuthCallback)             // Anthropic public-mode callback
	e.GET("/auth/callback", h.OAuthCallback)        // OpenAI public-mode callback
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
	e.GET("/api/capacity/global", h.GetGlobalCapacity)
	e.GET("/api/capacity/projects", h.GetProjectCapacities)
	e.GET("/api/capacity/projects/:projectId", h.GetProjectCapacity)
	e.GET("/api/capacity/models", h.GetModelCapacities)
	e.GET("/api/capacity/models/:modelId", h.GetModelCapacity)

	// Channels (Integrations)
	e.GET("/channels", h.handleChannels)
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

	// Personality
	e.GET("/personality", h.handleAppSettings)
	e.POST("/personality/save", h.handlePersonalitySave)

	// Custom personalities
	e.POST("/personality/custom", h.CreateCustomPersonality)
	e.GET("/personality/custom/:key", h.GetCustomPersonality)
	e.PUT("/personality/custom/:key", h.UpdateCustomPersonality)
	e.DELETE("/personality/custom/:key", h.DeleteCustomPersonality)

	// Telegram authorized users
	e.GET("/channels/telegram/authorized-users", h.ListTelegramAuthorizedUsers)
	e.POST("/channels/telegram/authorized-users", h.AddTelegramAuthorizedUser)
	e.DELETE("/channels/telegram/authorized-users/:id", h.RemoveTelegramAuthorizedUser)

	// Slack authorized users
	e.GET("/channels/slack/authorized-users", h.ListSlackAuthorizedUsers)
	e.POST("/channels/slack/authorized-users", h.AddSlackAuthorizedUser)
	e.DELETE("/channels/slack/authorized-users/:id", h.RemoveSlackAuthorizedUser)

	// Webhooks
	e.POST("/channels/webhooks", h.HandleWebhookCreate)
	e.PUT("/channels/webhooks/:id", h.HandleWebhookUpdate)
	e.DELETE("/channels/webhooks/:id", h.HandleWebhookDelete)
	e.POST("/channels/webhooks/:id/rotate-secret", h.HandleWebhookRotateSecret)
	e.POST("/channels/webhooks/:id/test", h.HandleWebhookTest)

	// Inbound webhook endpoint (generic, no auth middleware)
	e.POST("/webhooks/inbound/:pathToken", h.HandleWebhookInbound)

	// Git Worktree
	e.GET("/tasks/:taskId/worktree", h.GetTaskWorktreeInfo)
	e.POST("/tasks/:taskId/worktree/auto-merge", h.UpdateTaskAutoMerge)
	e.POST("/tasks/:taskId/worktree/merge", h.MergeTaskBranch)
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
	e.POST("/alerts/:id/read", h.MarkAlertRead)
	e.POST("/alerts/read-all", h.MarkAllAlertsRead)
	e.DELETE("/alerts/:id", h.DeleteAlert)
	e.DELETE("/alerts", h.DeleteAllAlerts)
	e.GET("/alerts/unread-count", h.GetUnreadAlertCount)

	// Multi-Agent Workflows
	e.GET("/workflows", h.ListWorkflows)
	e.POST("/workflows", h.CreateWorkflow)
	e.GET("/workflows/:id", h.GetWorkflow)
	e.PUT("/workflows/:id", h.UpdateWorkflow)
	e.DELETE("/workflows/:id", h.DeleteWorkflow)
	e.POST("/workflows/:id/steps", h.AddWorkflowStep)
	e.DELETE("/workflows/:id/steps/:stepId", h.DeleteWorkflowStep)
	e.POST("/workflows/:id/execute", h.ExecuteWorkflow)
	e.POST("/workflows/executions/:execId/cancel", h.CancelWorkflowExecution)
	e.GET("/workflows/executions/:execId", h.GetWorkflowExecution)

	// Workflow Templates
	e.GET("/workflows/templates", h.ListWorkflowTemplates)

	// Task Complexity Analysis
	e.GET("/tasks/:taskId/analyze", h.AnalyzeTaskComplexity)

	// Agent Performance Metrics
	e.GET("/api/workflows/metrics", h.GetAllAgentMetrics)
	e.GET("/api/workflows/metrics/:agentId", h.GetAgentMetrics)
	e.GET("/api/workflows/best-agent", h.GetBestAgent)
	e.GET("/api/workflows/cheapest-agent", h.GetCheapestAgent)

	// Vote Records
	e.GET("/api/workflows/votes/:stepExecId", h.GetVoteRecords)

	// Semantic Collision Detection
	e.POST("/api/collisions/analyze/:taskId", h.AnalyzeTaskImpact)
	e.GET("/api/collisions/impact/:taskId", h.GetTaskImpact)
	e.POST("/api/collisions/detect", h.DetectConflicts)
	e.GET("/api/collisions/conflicts/:taskId", h.GetTaskConflicts)
	e.PATCH("/api/collisions/conflicts/:id/status", h.UpdateConflictStatus)
	e.GET("/api/collisions/report", h.GetCollisionReport)
	e.POST("/api/collisions/recommend", h.RecommendExecutionOrder)
	e.GET("/api/collisions/recommendation", h.GetLatestRecommendation)
	e.POST("/api/collisions/recommendation/:id/accept", h.AcceptRecommendation)
	e.POST("/api/collisions/recommendation/:id/reject", h.RejectRecommendation)
	e.GET("/api/collisions/history", h.GetConflictHistory)
	e.POST("/api/collisions/history", h.RecordConflict)

	// Unified Suggestions (combined Insights + Backlog)
	e.GET("/suggestions", h.UnifiedSuggestions)
	e.POST("/suggestions/analyze", h.RunCombinedAnalysis)

	// Proactive Insights (individual endpoints still work)
	e.GET("/insights", h.ProactiveInsights)
	e.POST("/insights/analyze", h.RunInsightsAnalysis)
	e.POST("/insights/extract-knowledge", h.ExtractInsightsKnowledge)
	e.PATCH("/insights/:id/status", h.UpdateInsightStatus)
	e.DELETE("/insights/:id", h.DeleteInsight)
	e.GET("/insights/by-type", h.ListInsightsByType)
	e.GET("/insights/knowledge/search", h.SearchInsightsKnowledge)
	e.DELETE("/insights/knowledge/:id", h.DeleteKnowledgeEntry)
	e.GET("/insights/reports", h.ListInsightReports)
	e.POST("/insights/health-check", h.RunHealthCheck)
	e.POST("/history/grade-ideas", h.GradeIdeas)

	// Architect
	e.GET("/architect", h.ArchitectMode)
	e.POST("/architect/sessions", h.CreateArchitectSession)
	e.GET("/architect/sessions/:id", h.GetArchitectSession)
	e.DELETE("/architect/sessions/:id", h.DeleteArchitectSession)
	e.POST("/architect/sessions/:id/messages", h.SendArchitectMessage)
	e.POST("/architect/sessions/:id/advance", h.AdvanceArchitectPhase)
	e.POST("/architect/sessions/:id/architecture", h.GenerateArchitectArchitecture)
	e.POST("/architect/sessions/:id/risks", h.GenerateArchitectRisks)
	e.POST("/architect/sessions/:id/tasks", h.GenerateArchitectTasks)
	e.POST("/architect/sessions/:id/activate", h.ActivateArchitectPhase)
	e.POST("/architect/sessions/:id/abandon", h.AbandonArchitectSession)
	e.POST("/architect/sessions/:id/template", h.SaveArchitectTemplate)
	e.DELETE("/architect/templates/:id", h.DeleteArchitectTemplate)

	// Backlog Management
	e.GET("/backlog", h.BacklogManagement)
	e.POST("/backlog/analyze", h.RunBacklogAnalysis)
	e.PATCH("/backlog/suggestions/:id/status", h.UpdateBacklogSuggestionStatus)
	e.POST("/backlog/suggestions/:id/apply", h.ApplyBacklogSuggestion)
	e.DELETE("/backlog/suggestions/:id", h.DeleteBacklogSuggestion)
	e.POST("/backlog/health", h.SnapshotBacklogHealth)
	e.GET("/backlog/reports", h.ListBacklogReports)

	// Autonomous Builds
	e.GET("/autonomous", h.AutonomousBuilds)
	e.POST("/api/autonomous/trigger", h.TriggerAutonomousBuild)
	e.PUT("/api/autonomous/config", h.UpdateAutonomousConfig)
	e.GET("/api/autonomous/summary/:taskId", h.GetBuildSummary)
	e.GET("/api/autonomous/chain/:taskId", h.GetBuildChain)

	// Trend Intelligence (part of Autonomous)
	e.PUT("/api/autonomous/x-credentials", h.SaveXCredentials)
	e.POST("/api/autonomous/trends/sources", h.AddTrendSource)
	e.DELETE("/api/autonomous/trends/sources/:id", h.DeleteTrendSource)
	e.PATCH("/api/autonomous/trends/sources/:id/toggle", h.ToggleTrendSource)
	e.POST("/api/autonomous/trends/collect", h.CollectTrends)
	e.POST("/api/autonomous/trends/analyze", h.AnalyzeTrends)
	e.POST("/api/autonomous/trends/competitors", h.AnalyzeCompetitors)
	e.PATCH("/api/autonomous/trends/patterns/:id/status", h.UpdateTrendPatternStatus)
	e.GET("/api/autonomous/trends/dashboard", h.GetTrendDashboard)

	// Task Templates
	e.GET("/templates", h.Templates)
	e.POST("/templates", h.CreateTemplate)
	e.POST("/templates/:id/create-task", h.CreateTaskFromTemplate)
	e.POST("/tasks/:id/save-as-template", h.SaveTaskAsTemplate)
	e.PUT("/templates/:id", h.UpdateTemplate)
	e.DELETE("/templates/:id", h.DeleteTemplate)
	e.POST("/templates/:id/favorite", h.ToggleFavoriteTemplate)
	e.GET("/templates/search", h.SearchTemplates)
	e.GET("/templates/filter", h.FilterTemplates)

	// Pattern Library
	e.GET("/patterns", h.PatternsPage)
	e.POST("/patterns", h.CreatePattern)
	e.GET("/patterns/:id", h.GetPattern)
	e.PUT("/patterns/:id", h.UpdatePattern)
	e.DELETE("/patterns/:id", h.DeletePattern)
	e.GET("/patterns/:id/apply", h.ApplyPatternForm)
	e.POST("/patterns/:id/apply", h.ApplyPattern)
	e.POST("/patterns/:id/duplicate", h.DuplicatePattern)
	e.GET("/patterns/search", h.SearchPatterns)
	e.GET("/patterns/export", h.ExportPatterns)
	e.POST("/patterns/import", h.ImportPatterns)
	e.GET("/patterns/category/:category", h.ListPatternsByCategory)

	// Server-Sent Events for real-time updates
	e.GET("/events/live", h.LiveEventsSSE)
	e.GET("/events/chat/:exec_id", h.ChatStreamSSE)
}
