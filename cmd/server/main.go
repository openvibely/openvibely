package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/handler"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"

	_ "github.com/openvibely/openvibely/docs" // Swagger docs
)

// @title OpenVibely API
// @version 1.0
// @description REST API for OpenVibely - AI-powered task scheduling and management
// @description This API provides endpoints for managing projects, tasks, and chat interactions with AI agents.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url https://github.com/openvibely/openvibely
// @contact.email support@openvibely.dev

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:3001
// @BasePath /
// @schemes http https

// @tag.name projects
// @tag.description Operations for managing projects

// @tag.name chat
// @tag.description AI chat operations with file upload support

// @tag.name analytics
// @tag.description Execution analytics API endpoints

// @tag.name capacity
// @tag.description Worker capacity and utilization API endpoints

// @tag.name workflows
// @tag.description Multi-agent workflow and metrics API endpoints

// @tag.name autonomous
// @tag.description Autonomous build and trend intelligence API endpoints

// @tag.name collisions
// @tag.description Semantic collision analysis API endpoints

func main() {
	cfg := config.Load()

	// Database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("database initialized")

	// Event broadcasters for real-time UI updates
	broadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileChangeBroadcaster := events.NewFileChangeBroadcaster()

	// Repositories
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
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
		if dbToken, err := settingsRepo.Get(context.Background(), "telegram_bot_token"); err == nil && dbToken != "" {
			telegramToken = dbToken
		}
	}
	var telegramSvc *service.TelegramService
	if telegramToken != "" {
		var err error
		telegramSvc, err = service.NewTelegramService(
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
		if err != nil {
			log.Printf("warning: failed to initialize telegram bot: %v", err)
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

	// Reset any tasks orphaned in 'running' state from a previous crash
	if count, err := taskRepo.ResetOrphanedRunning(context.Background()); err != nil {
		log.Printf("warning: failed to reset orphaned running tasks: %v", err)
	} else if count > 0 {
		log.Printf("reset %d orphaned running tasks to pending", count)
	}

	// Clean up orphaned attachment files
	if count, err := attachmentRepo.CleanupOrphanedFiles(context.Background(), "uploads"); err != nil {
		log.Printf("warning: failed to cleanup orphaned attachments: %v", err)
	} else if count > 0 {
		log.Printf("cleaned up %d orphaned attachment files", count)
	}

	// Clean up orphaned chat attachment files
	if count, err := chatAttachmentRepo.CleanupOrphanedFiles(context.Background(), "uploads"); err != nil {
		log.Printf("warning: failed to cleanup orphaned chat attachments: %v", err)
	} else if count > 0 {
		log.Printf("cleaned up %d orphaned chat attachment files", count)
	}

	workDir, err := os.Getwd()
	if err != nil || workDir == "" {
		workDir = "."
	}
	mcpBootCtx, mcpBootCancel := context.WithTimeout(context.Background(), 45*time.Second)
	if err := agentplugins.EnsureInstalledPluginMCPRunning(mcpBootCtx, workDir); err != nil {
		log.Printf("warning: persistent plugin MCP startup incomplete: %v", err)
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
				Message:   err.Error(),
			}
			if alertErr := alertSvc.Create(alertCtx, a); alertErr != nil {
				log.Printf("warning: failed to create MCP startup alert: %v", alertErr)
			}
		}
		alertCancel()
	}
	mcpBootCancel()

	// Start background services
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workerSvc.Start(ctx)
	schedulerSvc.Start(ctx)

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
	// (e.g., when duplicating a browser tab). Two-pronged approach:
	// 1. Vary: HX-Request — tells caches to key responses by the HX-Request header
	// 2. Cache-Control: no-store on HTMX partials — prevents partials from being cached
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
	h.SetLocalRepoPathEnabled(cfg.EnableLocalRepoPath)
	h.SetTaskChangesMergeOptionsEnabled(cfg.EnableTaskChangesMergeOptions)
	llmSvc.SetAgentRepo(agentRepo)
	llmSvc.SetFileChangeBroadcaster(fileChangeBroadcaster)
	llmSvc.SetSlackService(slackSvc)
	if telegramSvc != nil {
		telegramSvc.SetChatBroadcaster(chatBroadcaster)
		telegramSvc.SetAgentRepo(agentRepo)
		llmSvc.SetTelegramService(telegramSvc)
	}
	h.RegisterRoutes(e)

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()
		workerSvc.Stop()
		schedulerSvc.Stop()
		if telegramSvc != nil {
			telegramSvc.Stop()
		}
		if slackSvc != nil {
			slackSvc.Stop()
		}
		e.Close()
	}()

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("starting server on %s", addr)
	if err := e.Start(addr); err != nil {
		log.Printf("server stopped: %v", err)
	}
}
