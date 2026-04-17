// Package server provides a reusable bootstrap function that wires and starts
// the full OpenVibely backend (database, repos, services, HTTP routes, background
// workers, and graceful shutdown).  It is consumed by both cmd/server (web/VPS)
// and cmd/desktop (Wails desktop wrapper).
package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/handler"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"

	_ "github.com/openvibely/openvibely/docs" // Swagger docs
)

// Instance holds a running server's state so callers can inspect the bound
// address and trigger graceful shutdown.
type Instance struct {
	// BoundAddr is the address the HTTP server is listening on (e.g. "127.0.0.1:54321").
	BoundAddr string
	// BaseURL is the full http:// URL including scheme and bound address.
	BaseURL string
	// Shutdown gracefully stops all background services, the HTTP server, and the DB.
	// It is safe to call multiple times.
	Shutdown func()
}

// Start wires the full OpenVibely backend and starts serving HTTP on cfg.Port.
// It blocks until the HTTP listener is bound and background services are started,
// then returns an Instance with the bound address and a shutdown handle.
//
// The caller is responsible for calling Instance.Shutdown when done, or
// listening for OS signals and calling it from a signal handler.
func Start(ctx context.Context, cfg *config.Config) (*Instance, error) {
	if err := config.ValidateAppBaseURL(os.Getenv("APP_BASE_URL")); err != nil {
		log.Printf("warning: %v", err)
	}

	authCfg := auth.Config{
		Enabled:       cfg.AuthEnabled,
		Username:      cfg.AuthUsername,
		Password:      cfg.AuthPassword,
		SessionSecret: cfg.AuthSessionSecret,
		SessionTTL:    cfg.AuthSessionTTL,
	}
	if err := authCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid auth configuration: %w", err)
	}

	// Database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	log.Println("database initialized")

	// Event broadcasters for real-time UI updates
	broadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileChangeBroadcaster := events.NewFileChangeBroadcaster()

	// Repositories
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	if modelsList, listErr := llmConfigRepo.List(context.Background()); listErr != nil {
		log.Printf("warning: unable to check OAuth model configuration for APP_BASE_URL validation: %v", listErr)
	} else {
		hasOAuth := false
		hasOAuthAnthropic := false
		hasOAuthOpenAI := false
		for _, modelCfg := range modelsList {
			if !modelCfg.IsOAuth() {
				continue
			}
			hasOAuth = true
			if modelCfg.Provider == models.ProviderAnthropic {
				hasOAuthAnthropic = true
			}
			if modelCfg.Provider == models.ProviderOpenAI {
				hasOAuthOpenAI = true
			}
		}

		if cfg.AppBaseURL == "" {
			if hasOAuth {
				log.Printf("warning: APP_BASE_URL is not set while OAuth models are configured; hosted OAuth callbacks will use localhost. Set APP_BASE_URL to your public host (example: https://dubee.org).")
			}
		} else {
			log.Printf("app base url configured for OAuth callbacks: %s", cfg.AppBaseURL)
			if hasOAuthAnthropic && strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_CLIENT_ID")) == "" {
				log.Printf("warning: ANTHROPIC_OAUTH_CLIENT_ID not set; hosted Anthropic OAuth will use built-in client and may be rejected by provider redirect policy.")
			}
			if hasOAuthOpenAI && strings.TrimSpace(os.Getenv("OPENAI_OAUTH_CLIENT_ID")) == "" {
				log.Printf("warning: OPENAI_OAUTH_CLIENT_ID not set; hosted OpenAI OAuth will use built-in client and may be rejected by provider redirect policy.")
			}
		}
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	// Services
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)

	maxWorkers, _ := workerRepo.GetMaxWorkers(context.Background())
	workerSvc := service.NewWorkerService(llmSvc, maxWorkers, projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.SetLLMConfigRepo(llmConfigRepo)

	projectSvc := service.NewProjectService(projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, broadcaster)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	workflowRepo := repository.NewWorkflowRepo(db)
	workflowSvc := service.NewWorkflowService(workflowRepo, llmConfigRepo, taskRepo, llmSvc)
	workflowSvc.SetAlertService(alertSvc)
	collisionRepo := repository.NewCollisionRepo(db)
	collisionSvc := service.NewCollisionService(collisionRepo, taskRepo, projectRepo, llmConfigRepo)
	collisionSvc.SetLLMService(llmSvc)
	insightsRepo := repository.NewInsightsRepo(db)
	insightsSvc := service.NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	insightsSvc.SetLLMService(llmSvc)
	architectRepo := repository.NewArchitectRepo(db)
	architectSvc := service.NewArchitectService(architectRepo, taskRepo, projectRepo, llmConfigRepo)
	architectSvc.SetLLMService(llmSvc)
	backlogRepo := repository.NewBacklogRepo(db)
	backlogSvc := service.NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	backlogSvc.SetLLMService(llmSvc)
	upcomingSvc.SetBacklogRepo(backlogRepo)
	upcomingSvc.SetProjectRepo(projectRepo)
	upcomingSvc.SetLLMService(llmSvc)
	upcomingSvc.SetLLMConfigRepo(llmConfigRepo)

	// Autonomous Builds (task-chain based)
	autonomousRepo := repository.NewAutonomousRepo(db)
	autonomousTriggerSvc := service.NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Trend Intelligence (enriches autonomous builds with external data)
	trendRepo := repository.NewTrendRepo(db)
	trendSvc := service.NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)
	trendSvc.SetLLMService(llmSvc)
	autonomousTriggerSvc.SetTrendIntelligenceService(trendSvc)

	// Task Templates
	templateRepo := repository.NewTemplateRepo(db)
	templateSvc := service.NewTemplateService(templateRepo, taskRepo, projectRepo)

	// Pattern Library
	patternRepo := repository.NewPatternRepo(db)
	patternSvc := service.NewPatternService(patternRepo, taskRepo)

	llmSvc.SetAlertService(alertSvc)
	llmSvc.SetTaskService(taskSvc)

	// Code review comments
	reviewCommentRepo := repository.NewReviewCommentRepo(db)

	// Telegram user authorization
	telegramAuthRepo := repository.NewTelegramAuthRepo(db)

	// Telegram user project persistence
	telegramUserProjectRepo := repository.NewTelegramUserProjectRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	// Custom personalities
	customPersonalityRepo := repository.NewCustomPersonalityRepo(db)

	settingsRepo := repository.NewSettingsRepo(db)
	taskPullRequestRepo := repository.NewTaskPullRequestRepo(db)
	webhookRepo := repository.NewWebhookRepo(db)

	// Seed Slack settings from env when provided (useful for bootstrapping local setup).
	if cfg.SlackClientID != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingClientID, cfg.SlackClientID)
	}
	if cfg.SlackClientSecret != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingClientSecret, cfg.SlackClientSecret)
	}
	if cfg.SlackAppToken != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingAppToken, cfg.SlackAppToken)
	}
	if cfg.SlackBotToken != "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenOverride, cfg.SlackBotToken)
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenSource, service.SlackBotTokenSourceManual)
	}
	if val, _ := settingsRepo.Get(context.Background(), service.SlackSettingBotTokenSource); val == "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingBotTokenSource, service.SlackBotTokenSourceOAuth)
	}
	if val, _ := settingsRepo.Get(context.Background(), service.SlackSettingSendResponses); val == "" {
		_ = settingsRepo.Set(context.Background(), service.SlackSettingSendResponses, "true")
	}

	githubSvc := service.NewGitHubService(
		settingsRepo,
		cfg.GitHubAppID,
		cfg.GitHubAppSlug,
		cfg.GitHubAppPrivateKey,
		cfg.ProjectRepoRoot,
	)
	slackSvc := service.NewSlackService(
		settingsRepo,
		projectRepo,
		llmConfigRepo,
		taskRepo,
		execRepo,
		scheduleRepo,
		taskSvc,
		llmSvc,
		workerSvc,
		slackUserProjectRepo,
		slackTaskContextRepo,
		slackAuthRepo,
	)
	slackSvc.SetCustomPersonalityRepo(customPersonalityRepo)
	slackSvc.SetChatBroadcaster(chatBroadcaster)
	slackSvc.SetAlertService(alertSvc)
	if err := slackSvc.Start(); err != nil {
		log.Printf("warning: failed to start slack socket mode: %v", err)
	}

	// Git worktree service for task isolation
	worktreeSvc := service.NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	llmSvc.SetWorktreeService(worktreeSvc)
	worktreeSvc.SetLLMService(llmSvc)
	worktreeSvc.SetGitHubService(githubSvc)
	schedulerSvc.SetWorktreeService(worktreeSvc)

	// Telegram Bot (optional - starts if token is configured via env or saved in DB)
	telegramToken := cfg.TelegramToken
	if telegramToken == "" {
		// Fall back to token saved via Settings page
		if dbToken, getErr := settingsRepo.Get(context.Background(), "telegram_bot_token"); getErr == nil && dbToken != "" {
			telegramToken = dbToken
		}
	}
	var telegramSvc *service.TelegramService
	if telegramToken != "" {
		var tErr error
		telegramSvc, tErr = service.NewTelegramService(
			telegramToken,
			taskSvc,
			projectRepo,
			llmConfigRepo,
			taskRepo,
			execRepo,
			scheduleRepo,
			chatAttachmentRepo,
			llmSvc,
			workerSvc,
		)
		if tErr != nil {
			log.Printf("warning: failed to initialize telegram bot: %v", tErr)
		} else {
			telegramSvc.SetTelegramAuthRepo(telegramAuthRepo)
			telegramSvc.SetTelegramUserProjectRepo(telegramUserProjectRepo)
			telegramSvc.SetSettingsRepo(settingsRepo)
			telegramSvc.SetCustomPersonalityRepo(customPersonalityRepo)
			telegramSvc.SetAlertService(alertSvc)
			log.Println("telegram bot initialized")
		}
	} else {
		log.Println("telegram bot disabled (no token configured)")
	}

	// Validate project repo paths exist on disk (catches ephemeral path loss after container restart)
	if missing := projectSvc.ValidateRepoPaths(context.Background()); len(missing) > 0 {
		log.Printf("WARNING: %d project(s) have missing repo paths — tasks using these projects will fail until repos are restored. Ensure PROJECT_REPO_ROOT is on a persistent volume (e.g. /data/repos).", len(missing))
	}

	// Reset any tasks orphaned in 'running' state from a previous crash
	if count, resetErr := taskRepo.ResetOrphanedRunning(context.Background()); resetErr != nil {
		log.Printf("warning: failed to reset orphaned running tasks: %v", resetErr)
	} else if count > 0 {
		log.Printf("reset %d orphaned running tasks to pending", count)
	}

	// Clean up orphaned attachment files
	if count, cleanErr := attachmentRepo.CleanupOrphanedFiles(context.Background(), "uploads"); cleanErr != nil {
		log.Printf("warning: failed to cleanup orphaned attachments: %v", cleanErr)
	} else if count > 0 {
		log.Printf("cleaned up %d orphaned attachment files", count)
	}

	// Clean up orphaned chat attachment files
	if count, cleanErr := chatAttachmentRepo.CleanupOrphanedFiles(context.Background(), "uploads"); cleanErr != nil {
		log.Printf("warning: failed to cleanup orphaned chat attachments: %v", cleanErr)
	} else if count > 0 {
		log.Printf("cleaned up %d orphaned chat attachment files", count)
	}

	workDir, wdErr := os.Getwd()
	if wdErr != nil || workDir == "" {
		workDir = "."
	}
	mcpBootCtx, mcpBootCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if mcpErr := agentplugins.EnsureInstalledPluginMCPRunning(mcpBootCtx, workDir); mcpErr != nil {
		log.Printf("warning: persistent plugin MCP startup incomplete: %v", mcpErr)
		alertCtx, alertCancel := context.WithTimeout(context.Background(), 5*time.Second)
		projects, projectErr := projectRepo.List(alertCtx)
		if projectErr != nil {
			log.Printf("warning: could not load project for MCP startup alert: %v", projectErr)
		} else if len(projects) > 0 {
			a := &models.Alert{
				ProjectID: projects[0].ID,
				Type:      models.AlertCustom,
				Severity:  models.SeverityError,
				Title:     "Plugin MCP startup warning",
				Message:   mcpErr.Error(),
			}
			if alertErr := alertSvc.Create(alertCtx, a); alertErr != nil {
				log.Printf("warning: failed to create MCP startup alert: %v", alertErr)
			}
		}
		alertCancel()
	}
	mcpBootCancel()

	// Start background services
	srvCtx, srvCancel := context.WithCancel(ctx)

	workerSvc.Start(srvCtx)
	schedulerSvc.Start(srvCtx)

	// Start Telegram bot if configured
	if telegramSvc != nil {
		telegramSvc.Start()
	}

	// HTTP Server
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Handle PUT/PATCH/DELETE via form _method field
	e.Pre(middleware.MethodOverride())

	// Prevent browser from serving cached HTMX partials when a full page is needed
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("Vary", "HX-Request")
			if c.Request().Header.Get("HX-Request") == "true" {
				c.Response().Header().Set("Cache-Control", "no-store")
			}
			return next(c)
		}
	})

	h := handler.New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, workflowSvc, collisionSvc, insightsSvc, architectSvc, backlogSvc, autonomousTriggerSvc, trendSvc, templateSvc, patternSvc,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, broadcaster, telegramSvc,
	)
	h.SetChatBroadcaster(chatBroadcaster)
	h.SetFileChangeBroadcaster(fileChangeBroadcaster)
	h.SetTelegramAuthRepo(telegramAuthRepo)
	h.SetSlackAuthRepo(slackAuthRepo)
	h.SetReviewCommentRepo(reviewCommentRepo)
	h.SetCustomPersonalityRepo(customPersonalityRepo)
	h.SetWorktreeService(worktreeSvc)
	h.SetAgentRepo(agentRepo)
	h.SetTaskPullRequestRepo(taskPullRequestRepo)
	h.SetGitHubService(githubSvc)
	h.SetSlackService(slackSvc)
	h.SetWebhookRepo(webhookRepo)
	h.SetLocalRepoPathEnabled(cfg.EnableLocalRepoPath)
	h.SetTaskChangesMergeOptionsEnabled(cfg.EnableTaskChangesMergeOptions)
	h.SetAuthConfig(authCfg)
	e.Use(h.AuthMiddleware())
	llmSvc.SetAgentRepo(agentRepo)
	llmSvc.SetFileChangeBroadcaster(fileChangeBroadcaster)
	llmSvc.SetSlackService(slackSvc)
	if telegramSvc != nil {
		telegramSvc.SetChatBroadcaster(chatBroadcaster)
		telegramSvc.SetAgentRepo(agentRepo)
		llmSvc.SetTelegramService(telegramSvc)
	}
	h.RegisterRoutes(e)

	// Bind listener explicitly so we know the actual port before serving.
	addr := fmt.Sprintf(":%s", cfg.Port)
	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		srvCancel()
		db.Close()
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, listenErr)
	}

	boundAddr := ln.Addr().String()
	log.Printf("starting server on %s", boundAddr)

	// Derive a usable base URL.
	host, port, _ := net.SplitHostPort(boundAddr)
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	baseURL := fmt.Sprintf("http://%s:%s", host, port)

	shutdownOnce := make(chan struct{})
	shutdownDone := make(chan struct{})
	shutdownFn := func() {
		select {
		case <-shutdownOnce:
			// Already called — wait for completion.
			<-shutdownDone
			return
		default:
		}
		close(shutdownOnce)
		log.Println("shutting down...")
		srvCancel()
		workerSvc.Stop()
		schedulerSvc.Stop()
		if telegramSvc != nil {
			telegramSvc.Stop()
		}
		if slackSvc != nil {
			slackSvc.Stop()
		}
		e.Close()
		db.Close()
		close(shutdownDone)
	}

	// Serve in background.
	e.Listener = ln
	go func() {
		if sErr := e.Start(""); sErr != nil {
			log.Printf("server stopped: %v", sErr)
		}
	}()

	return &Instance{
		BoundAddr: boundAddr,
		BaseURL:   baseURL,
		Shutdown:  shutdownFn,
	}, nil
}
