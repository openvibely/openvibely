package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

var hexIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

const (
	maxMessageLength         = 4096 // Telegram message length limit
	telegramProcessTimeout   = 5 * time.Minute
	telegramChatHistoryLimit = 50
	telegramMaxFileSize      = 20 << 20   // 20 MB (Telegram Bot API limit)
	telegramMaxTextFileSize  = 100 * 1024 // 100KB for text content injection

	TelegramSettingBotToken       = "telegram_bot_token"
	TelegramSettingSendResponses  = "telegram_send_responses"
	TelegramSettingRichMessagesV2 = "telegram_rich_messages_v2"
)

var telegramUploadsDir = "uploads" // same as handler's uploadsDir
var telegramStreamInterval = 2 * time.Second

type telegramPreviewKey struct {
	chatID    int64
	messageID int
}

type telegramPreviewState struct {
	done             chan struct{}
	richDraftID      int
	richDraftVisible bool
}

type telegramAuthorizationStore interface {
	IsAuthorized(ctx context.Context, projectID string, userID int64, username string) (bool, error)
	IsAuthorizedAnywhere(ctx context.Context, userID int64, username string) (bool, error)
	BackfillUserID(ctx context.Context, projectID, username string, userID int64) error
}

// TelegramService manages Telegram bot integration.
// It acts as a proxy to the /chat page orchestrator — every message sent to the bot
// is forwarded to the same chat assistant that powers the /chat web UI.
type TelegramService struct {
	bot                        *tgbotapi.BotAPI
	taskSvc                    *TaskService
	projectSvc                 *ProjectService
	githubProjectSvc           GitHubProjectCloneProvider
	memorySvc                  *MemoryService
	agentLibraryMaintenanceSvc *AgentLibraryMaintenanceService
	projectRepo                *repository.ProjectRepo
	llmConfigRepo              *repository.LLMConfigRepo
	taskRepo                   *repository.TaskRepo
	execRepo                   *repository.ExecutionRepo
	scheduleRepo               *repository.ScheduleRepo
	chatAttachmentRepo         *repository.ChatAttachmentRepo
	threadInputRepo            *repository.ThreadInputRepo
	telegramAuthRepo           telegramAuthorizationStore
	telegramUserProjectRepo    *repository.TelegramUserProjectRepo
	settingsRepo               *repository.SettingsRepo
	customPersonalityRepo      *repository.CustomPersonalityRepo
	agentRepo                  *repository.AgentRepo
	alertSvc                   *AlertService
	channelMessageRouter       *ChannelMessageRouter
	emailStatus                func(context.Context) EmailConnectionStatus
	emailAuthRepo              *repository.EmailAuthRepo
	webhookRepo                *repository.WebhookRepo
	taskGoalSvc                *TaskGoalService
	llmSvc                     *LLMService
	workerSvc                  *WorkerService
	automationGraphSvc         *AutomationGraphService
	chatBroadcaster            *events.ChatBroadcaster
	executionStreamHub         *events.ExecutionStreamHub
	queuedTurnPromoter         func(projectID string)
	queuedTaskThreadPromoter   func(taskID string)
	channelChatRunner          ChannelChatRunner
	channelTaskRunner          ChannelTaskRunner
	sendMessageFunc            func(chatID int64, text string)
	editMessageFunc            func(chatID int64, messageID int, text string)
	sendConfigFunc             func(c tgbotapi.Chattable) (tgbotapi.Message, error)
	makeRequestFunc            func(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error)
	newBotAPI                  func(token string) (*tgbotapi.BotAPI, error)
	previewMu                  sync.Mutex
	activePreviews             map[telegramPreviewKey]*telegramPreviewState
	userProjectsMu             sync.RWMutex
	userProjects               map[int64]string // Maps Telegram user ID to active project ID
	userProjectVersions        map[int64]uint64
	userProjectSwitchMu        sync.Mutex
	activeProjectReadHook      func(int64) // deterministic project-resolution test barrier
	lifecycleOpMu              sync.Mutex
	lifecycleMu                sync.Mutex
	ctx                        context.Context
	cancel                     context.CancelFunc
	runDone                    chan struct{}
	running                    bool
}

// NewTelegramService creates a new Telegram bot service
func NewTelegramService(
	token string,
	taskSvc *TaskService,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	execRepo *repository.ExecutionRepo,
	scheduleRepo *repository.ScheduleRepo,
	chatAttachmentRepo *repository.ChatAttachmentRepo,
	llmSvc *LLMService,
	workerSvc *WorkerService,
) (*TelegramService, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram bot token is empty")
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	bot.Debug = false
	applog.Infof("[telegram] authorized on account %s", bot.Self.UserName)

	ctx, cancel := context.WithCancel(context.Background())

	return &TelegramService{
		bot:                 bot,
		taskSvc:             taskSvc,
		projectRepo:         projectRepo,
		llmConfigRepo:       llmConfigRepo,
		taskRepo:            taskRepo,
		execRepo:            execRepo,
		scheduleRepo:        scheduleRepo,
		chatAttachmentRepo:  chatAttachmentRepo,
		llmSvc:              llmSvc,
		workerSvc:           workerSvc,
		newBotAPI:           tgbotapi.NewBotAPI,
		userProjects:        make(map[int64]string),
		userProjectVersions: make(map[int64]uint64),
		ctx:                 ctx,
		cancel:              cancel,
	}, nil
}

// IsRunning returns whether the bot is currently running.
func (s *TelegramService) IsRunning() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.running
}

// SetChatBroadcaster sets the chat event broadcaster for real-time chat updates.
func (s *TelegramService) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	s.chatBroadcaster = cb
}

func (s *TelegramService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
}

func (s *TelegramService) SetThreadInputRepo(repo *repository.ThreadInputRepo) {
	s.threadInputRepo = repo
}

func (s *TelegramService) SetQueuedTurnPromoter(promoter func(projectID string)) {
	s.queuedTurnPromoter = promoter
}

func (s *TelegramService) SetQueuedTaskThreadPromoter(promoter func(taskID string)) {
	s.queuedTaskThreadPromoter = promoter
}

func (s *TelegramService) SetChannelChatRunner(runner ChannelChatRunner) {
	s.channelChatRunner = runner
}

func (s *TelegramService) SetChannelTaskRunner(runner ChannelTaskRunner) {
	s.channelTaskRunner = runner
}

func (s *TelegramService) HasChannelChatRunner() bool {
	return s.channelChatRunner != nil
}

func (s *TelegramService) HasAgentRepo() bool {
	return s.agentRepo != nil
}

// SetTelegramAuthRepo sets the Telegram authorization repo for user verification.
func (s *TelegramService) SetTelegramAuthRepo(repo *repository.TelegramAuthRepo) {
	s.telegramAuthRepo = repo
}

// SetTelegramUserProjectRepo sets the repo for persisting user project selections across restarts.
func (s *TelegramService) SetTelegramUserProjectRepo(repo *repository.TelegramUserProjectRepo) {
	s.telegramUserProjectRepo = repo
}

// SetSettingsRepo sets the global settings repo for reading app-wide settings like personality.
func (s *TelegramService) SetSettingsRepo(repo *repository.SettingsRepo) {
	s.settingsRepo = repo
}

// SetCustomPersonalityRepo sets the custom personality repo for resolving custom personality prompts.
func (s *TelegramService) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	s.customPersonalityRepo = repo
}

func (s *TelegramService) SetProjectCreationServices(projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider, memorySvc *MemoryService, agentLibraryMaintenanceSvc *AgentLibraryMaintenanceService) {
	s.projectSvc = projectSvc
	s.githubProjectSvc = githubSvc
	s.memorySvc = memorySvc
	s.agentLibraryMaintenanceSvc = agentLibraryMaintenanceSvc
}

// SetAgentRepo sets the agent repo for listing agent definitions from Telegram chat.
func (s *TelegramService) SetAgentRepo(repo *repository.AgentRepo) {
	s.agentRepo = repo
}

// SetAlertService sets the alert service for managing alerts from Telegram chat.
func (s *TelegramService) SetAlertService(svc *AlertService) {
	s.alertSvc = svc
}

func (s *TelegramService) SetAutomationGraphService(svc *AutomationGraphService) {
	s.automationGraphSvc = svc
}

func (s *TelegramService) SetChannelMessageRouter(router *ChannelMessageRouter) {
	s.channelMessageRouter = router
}

func (s *TelegramService) SetEmailStatusProvider(provider func(context.Context) EmailConnectionStatus) {
	s.emailStatus = provider
}

func (s *TelegramService) SetEmailAuthRepo(repo *repository.EmailAuthRepo) {
	s.emailAuthRepo = repo
}

func (s *TelegramService) SetWebhookRepo(repo *repository.WebhookRepo) {
	s.webhookRepo = repo
}

// SetTaskGoalService injects the task goal service so Telegram can execute
// goal-related chat-control tools with the same durable TaskGoalService
// behavior as web/API chat.
func (s *TelegramService) SetTaskGoalService(svc *TaskGoalService) {
	s.taskGoalSvc = svc
}

// checkAuthorization verifies that a Telegram user is authorized for the given project.
// Deny-by-default: if the auth repo is configured, the user must be explicitly listed.
// If projectID is empty, checks authorization across all projects.
func (s *TelegramService) checkAuthorization(userID int64, username string, projectID string) bool {
	if s.telegramAuthRepo == nil {
		applog.Infof("[telegram] auth check: no auth repo configured, allowing user %d (%s)", userID, username)
		return true // No auth repo configured, allow all
	}

	ctx := context.Background()

	authorized, err := s.telegramAuthRepo.IsAuthorizedAnywhere(ctx, userID, username)
	if err != nil {
		if projectID == "" {
			applog.Infof("[telegram] global authorization lookup failed for user_id=%d: %v", userID, err)
		} else {
			applog.Infof("[telegram] project authorization lookup failed for user_id=%d project_id=%s: %v", userID, projectID, err)
		}
		return false
	}

	if projectID == "" {
		applog.Infof("[telegram] auth check: user %d (%s) global authorized=%v", userID, username, authorized)
		return authorized
	}

	applog.Infof("[telegram] auth check: user %d (%s) project=%s authorized=%v", userID, username, projectID, authorized)

	// If authorized via username match, backfill the user ID for future lookups
	if authorized && username != "" {
		_ = s.telegramAuthRepo.BackfillUserID(ctx, projectID, username, userID)
	}

	return authorized
}

// UpdateToken stops the current bot, reinitializes with the new token, and starts again.
func (s *TelegramService) UpdateToken(token string) error {
	s.lifecycleOpMu.Lock()
	defer s.lifecycleOpMu.Unlock()

	if !s.stopLocked(true) {
		return fmt.Errorf("timed out waiting for previous telegram poller to stop")
	}
	newBotAPI := s.newBotAPI
	if newBotAPI == nil {
		newBotAPI = tgbotapi.NewBotAPI
	}
	bot, err := newBotAPI(token)
	if err != nil {
		return fmt.Errorf("invalid telegram token: %w", err)
	}
	s.lifecycleMu.Lock()
	s.bot = bot
	s.lifecycleMu.Unlock()
	s.startLocked()
	return nil
}

// Start begins listening for Telegram updates.
func (s *TelegramService) Start() {
	s.lifecycleOpMu.Lock()
	defer s.lifecycleOpMu.Unlock()
	s.startLocked()
}

func (s *TelegramService) startLocked() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.bot == nil || s.running || s.runDone != nil {
		return
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.runDone = make(chan struct{})
	s.running = true
	go s.run(s.ctx, s.bot, s.runDone)
}

// Stop stops the Telegram bot.
func (s *TelegramService) Stop() {
	s.lifecycleOpMu.Lock()
	defer s.lifecycleOpMu.Unlock()
	s.stopLocked(false)
}

func (s *TelegramService) stopLocked(wait bool) bool {
	s.lifecycleMu.Lock()
	done := s.runDone
	if !s.running {
		s.lifecycleMu.Unlock()
		if wait && done != nil {
			select {
			case <-done:
				return true
			case <-time.After(65 * time.Second):
				applog.Infof("[telegram] timeout waiting for bot poller to stop")
				return false
			}
		}
		return true
	}
	applog.Infof("[telegram] stopping bot")
	cancel := s.cancel
	s.running = false
	s.cancel = nil
	s.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if wait && done != nil {
		select {
		case <-done:
			return true
		case <-time.After(65 * time.Second):
			applog.Infof("[telegram] timeout waiting for bot poller to stop")
			return false
		}
	}
	return true
}

// run is the main bot loop.
func (s *TelegramService) run(ctx context.Context, bot *tgbotapi.BotAPI, done chan struct{}) {
	defer func() {
		s.lifecycleMu.Lock()
		if s.runDone == done {
			s.runDone = nil
			s.cancel = nil
			s.running = false
		}
		s.lifecycleMu.Unlock()
		close(done)
	}()
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	applog.Infof("[telegram] bot started, waiting for updates")

	var lastConflictLog time.Time
	for {
		select {
		case <-ctx.Done():
			applog.Infof("[telegram] bot stopped")
			return
		default:
		}

		updates, err := bot.GetUpdates(u)
		if err != nil {
			if isTelegramConflictError(err) {
				if time.Since(lastConflictLog) >= 30*time.Second {
					applog.Infof("[telegram] getUpdates conflict: another consumer is polling this bot token; retrying with backoff")
					lastConflictLog = time.Now()
				}
			} else {
				applog.Infof("[telegram] getUpdates error: %v; retrying with backoff", err)
			}
			select {
			case <-ctx.Done():
				applog.Infof("[telegram] bot stopped")
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		if !processTelegramUpdateBatch(ctx, &u.Offset, updates, s.handleTelegramUpdate) {
			select {
			case <-ctx.Done():
				applog.Infof("[telegram] bot stopped")
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func processTelegramUpdateBatch(ctx context.Context, offset *int, updates []tgbotapi.Update, handle func(context.Context, tgbotapi.Update) bool) bool {
	for _, update := range updates {
		if update.UpdateID < *offset {
			continue
		}
		if !handle(ctx, update) {
			return false
		}
		*offset = update.UpdateID + 1
	}
	return true
}

func (s *TelegramService) handleTelegramUpdate(ctx context.Context, update tgbotapi.Update) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	if update.Message == nil {
		return true
	}
	if update.Message.From == nil {
		if update.Message.SenderChat != nil {
			applog.Debugf("[telegram] ignoring sender-chat update update_id=%d sender_chat_id=%d", update.UpdateID, update.Message.SenderChat.ID)
		} else {
			applog.Debugf("[telegram] ignoring senderless update update_id=%d", update.UpdateID)
		}
		return true
	}

	// Check authorization before processing any message (including commands)
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	username := update.Message.From.UserName

	// Resolve the user's active project for authorization check
	projectID := s.getActiveProject(userID)
	if !s.checkAuthorization(userID, username, projectID) {
		applog.Infof("[telegram] unauthorized access attempt from user %d (username: %s) for project %s",
			userID, username, projectID)
		s.sendMessage(s.ctx, chatID, "You are not authorized to use this bot. Contact the project owner to get access.")
		return true
	}

	// Handle special commands: /start and /project
	if update.Message.IsCommand() {
		cmd := update.Message.Command()
		switch cmd {
		case "start":
			response := s.handleStart(update.Message.From.ID)
			s.sendMessage(s.ctx, update.Message.Chat.ID, response)
			return true
		case "project":
			response := s.handleProject(update.Message.From.ID, update.Message.CommandArguments())
			s.sendMessage(s.ctx, update.Message.Chat.ID, response)
			return true
		}
	}

	// Check for natural language project commands before forwarding to LLM
	if response, handled := s.handleNaturalLanguageProjectCommand(userID, update.Message.Text); handled {
		s.sendMessage(s.ctx, chatID, response)
		return true
	}

	// Forward all other messages (including unrecognized commands) to the chat orchestrator.
	// Polling waits only for durable persistence, while model response work continues asynchronously.
	return s.handleChatMessageUntilDurable(ctx, update.Message)
}

func isTelegramConflictError(err error) bool {
	var tgErr *tgbotapi.Error
	if errors.As(err, &tgErr) && tgErr.Code == http.StatusConflict {
		return true
	}
	return strings.Contains(err.Error(), "terminated by other getUpdates request")
}

// handleStart welcomes the user and sets the default project
func (s *TelegramService) handleStart(userID int64) string {
	projects, err := s.projectRepo.List(context.Background())
	if err != nil {
		return fmt.Sprintf("Error loading projects: %v", err)
	}

	if len(projects) == 0 {
		return "Welcome to OpenVibely! No projects found. Please create a project first using the web interface."
	}

	// Find default project or use first one
	var defaultProject *models.Project
	for i := range projects {
		if projects[i].IsDefault {
			defaultProject = &projects[i]
			break
		}
	}
	if defaultProject == nil {
		defaultProject = &projects[0]
	}

	if err := s.setTelegramActiveProject(context.Background(), userID, defaultProject.ID); err != nil {
		applog.Infof("[telegram] failed to set default project for user %d: %v", userID, err)
		return fmt.Sprintf("Error setting default project: %v", err)
	}

	return fmt.Sprintf("Welcome to *OpenVibely*! 🚀\n\nYour active project is: *%s*\n\nJust send me any message and I'll help you manage tasks, answer questions about your project, or anything else — the same way the /chat page works in the web UI.\n\nExamples:\n- \"Create a task to fix the login bug\"\n- \"List my backlog tasks\"\n- \"What tasks are currently running?\"\n\nUse /project to view or change your active project.",
		defaultProject.Name)
}

// handleProject shows the current project or switches to a new one
func (s *TelegramService) handleProject(userID int64, args string) string {
	ctx := context.Background()
	targetName := strings.TrimSpace(args)
	currentProjectID := ""
	if targetName == "" {
		currentProjectID = s.getActiveProject(userID)
	}
	selection, err := selectChannelProject(ctx, s.projectRepo, currentProjectID, targetName, func(ctx context.Context, project *models.Project) error {
		// Persist before publishing the switch to the active-project cache. A failed
		// write must leave both the durable selection and live routing unchanged.
		return s.setTelegramActiveProject(ctx, userID, project.ID)
	})
	if err != nil {
		if selection.Target != nil {
			applog.Infof("[telegram] failed to switch project for user %d: %v", userID, err)
			return fmt.Sprintf("❌ Error switching project: %v", err)
		}
		return fmt.Sprintf("❌ Error loading projects: %v", err)
	}
	if len(selection.Projects) == 0 {
		return "No projects found. Please create a project first using the web interface."
	}

	// If no args, show current project and list available projects
	if selection.TargetName == "" {
		var currentProjectName string
		if selection.Current != nil {
			currentProjectName = selection.Current.Name
		}

		var projectList strings.Builder
		projectList.WriteString(fmt.Sprintf("📂 *Current project:* %s\n\n", currentProjectName))
		projectList.WriteString("*Available projects:*\n")
		for _, p := range selection.Projects {
			marker := ""
			if p.ID == currentProjectID {
				marker = " ← _current_"
			}
			projectList.WriteString(fmt.Sprintf("• %s%s\n", p.Name, marker))
		}
		projectList.WriteString("\nUse `/project <name>` to switch projects.")
		return projectList.String()
	}

	if selection.Target == nil {
		return fmt.Sprintf("❌ Project not found: %q\n\nAvailable projects: %s",
			selection.TargetName, strings.Join(selection.AvailableNames, ", "))
	}

	return fmt.Sprintf("✅ Switched to project: *%s*", selection.Target.Name)
}

// handleChatMessage forwards a Telegram message to the chat orchestrator.
func (s *TelegramService) handleChatMessage(message *tgbotapi.Message) {
	s.handleChatMessageWithDurableHandoff(context.Background(), message, nil)
}

func (s *TelegramService) handleChatMessageUntilDurable(ctx context.Context, message *tgbotapi.Message) bool {
	result := make(chan bool, 1)
	var reportOnce sync.Once
	report := func(success bool) {
		reportOnce.Do(func() { result <- success })
	}
	go func() {
		report(s.handleChatMessageWithDurableHandoff(ctx, message, func() { report(true) }))
	}()
	select {
	case success := <-result:
		return success
	case <-ctx.Done():
		return false
	}
}

func (s *TelegramService) handleChatMessageWithDurableHandoff(parentCtx context.Context, message *tgbotapi.Message, onDurableHandoff func()) bool {
	userID := message.From.ID
	chatID := message.Chat.ID

	// Extract text from message or caption (for attachment messages)
	text := message.Text
	if text == "" {
		text = message.Caption
	}

	// Extract attachment info from the message
	fileID, fileName, fileSize, mimeType := extractTelegramAttachment(message)

	// Require either text or an attachment
	if text == "" && fileID == "" {
		s.sendMessage(context.Background(), chatID, "Please send a text message or an attachment.")
		return true
	}

	// If attachment with no caption, generate a default prompt
	if text == "" && fileID != "" {
		text = fmt.Sprintf("User sent an attachment: %s", fileName)
	}

	applog.Infof("[telegram] chat message from user=%d text=%q hasAttachment=%v", userID, text, fileID != "")

	projectID := s.getActiveProject(userID)
	if projectID == "" {
		s.sendMessage(context.Background(), chatID, "No active project. Send /start to set up first.")
		return true
	}

	ctx, cancel := context.WithTimeout(parentCtx, telegramProcessTimeout)
	defer cancel()

	start := time.Now()
	var telegramImageAttachments []models.Attachment
	durablyHandedOff := false
	runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:              "telegram",
		ProjectID:             projectID,
		Message:               text,
		Source:                models.TaskOriginTelegram,
		Surface:               chatcontrol.SurfaceTelegram,
		HasAttachments:        fileID != "",
		Start:                 start,
		TaskRepo:              s.taskRepo,
		ExecRepo:              s.execRepo,
		ThreadInputRepo:       s.threadInputRepo,
		LLMConfigRepo:         s.llmConfigRepo,
		ChatBroadcaster:       s.chatBroadcaster,
		UploadsDir:            telegramUploadsDir,
		TaskSvc:               s.taskSvc,
		ScheduleRepo:          s.scheduleRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		OnDurableHandoff: func() {
			durablyHandedOff = true
			if onDurableHandoff != nil {
				onDurableHandoff()
			}
		},
		DownloadAttachments: func(ctx context.Context) (channelChatIngressDownloadResult, error) {
			if fileID == "" {
				return channelChatIngressDownloadResult{}, nil
			}
			attCtx, imgAtts, chatAtts, err := s.downloadAndSaveTelegramAttachment(ctx, fileID, fileName, fileSize, mimeType)
			telegramImageAttachments = imgAtts
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAtts}, err
		},
		ContinueWithoutAttachmentsOnDownloadError: true,
		IncomingAttachmentsNeedVision:             func() bool { return isTelegramImageFile(mimeType) },
		SavePendingAttachments:                    s.saveChatAttachmentsToPendingSession,
		FindActiveExecution:                       s.execRepo.FindLatestActiveChatExecution,
		NewQueuedInput:                            func() *models.ThreadInput { return &models.ThreadInput{TelegramChatID: chatID} },
		OnAttachmentDownloadFailed:                func(_ context.Context, msgText string) { s.sendMessage(ctx, chatID, "⚠️ "+msgText) },
		OnAttachmentStoreFailed: func(context.Context, string) {
			s.sendMessage(ctx, chatID, "Error queueing your attachment. Please try again.")
		},
		OnModelSelectionFailed: func(_ context.Context, err error) {
			applog.Infof("[telegram] agent selection error: %v", err)
			s.sendMessage(ctx, chatID, fmt.Sprintf("Error selecting model: %v", err))
		},
		OnActiveLookupFailed: func(context.Context) {
			s.sendMessage(ctx, chatID, "Error checking active chat response. Please try again.")
		},
		OnQueueFailure: func(context.Context) { s.sendMessage(ctx, chatID, "Error queueing your message. Please try again.") },
		OnQueued: func(context.Context) {
			s.sendMessage(ctx, chatID, "Queued. I'll send this after the current response finishes.")
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: &models.Task{Title: fmt.Sprintf("Telegram %s: %s", start.Format("15:04:05.000"), util.Truncate(text, 47)), CreatedVia: models.TaskOriginTelegram, TelegramChatID: chatID},
			RuntimeToolsForTask: func(taskID string) *llmcontracts.RuntimeTools {
				return s.buildTelegramActionToolRuntimeForTask(projectID, taskID, chatID, userID, nil)
			},
			ChannelChatRunner: s.channelChatRunner,
			CompleteExecution: channelCompletionFunc("telegram", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
			LinkAttachments: func(ctx context.Context, execID string, atts []models.ChatAttachment) ([]models.ChatAttachment, error) {
				linked, err := s.linkAttachmentsToExecution(ctx, execID, atts)
				if err != nil {
					return nil, err
				}
				telegramImageAttachments = updateChannelChatImageAttachmentPaths(telegramImageAttachments, linked)
				return linked, nil
			},
			AttachmentContextAndImages: func(atts []models.ChatAttachment) (string, []models.Attachment) {
				ctxText, imgs := channelChatAttachmentContextAndImages(atts, telegramMaxTextFileSize)
				if len(telegramImageAttachments) > 0 {
					imgs = updateChannelChatImageAttachmentPaths(telegramImageAttachments, atts)
				}
				return ctxText, imgs
			},
			ListChatHistory: func(ctx context.Context, projectID string) ([]models.Execution, error) {
				return s.execRepo.ListChatHistory(ctx, projectID, telegramChatHistoryLimit)
			},
			FilterChatHistory:        filterTelegramChatHistory,
			OnTaskCreateFailure:      func(context.Context) { s.sendMessage(ctx, chatID, "Error processing your message. Please try again.") },
			OnExecutionCreateFailure: func(context.Context) { s.sendMessage(ctx, chatID, "Error processing your message. Please try again.") },
			OnAttachmentLinkFailure:  func(_ context.Context, msgText string) { s.sendMessage(ctx, chatID, "⚠️ "+msgText) },
			PrepareRunner: func(ctx context.Context, taskID, execID string) int {
				applog.Infof("[telegram] created exec=%s task=%s for user=%d", execID, taskID, userID)
				var sentMsg tgbotapi.Message
				if s.bot != nil || s.sendConfigFunc != nil {
					thinkingMsg := tgbotapi.NewMessage(chatID, "⏳ Thinking...")
					var err error
					sentMsg, err = s.sendConfig(thinkingMsg)
					if err != nil {
						applog.Infof("[telegram] error sending thinking message: %v", err)
					}
				}
				if sentMsg.MessageID != 0 {
					previewDone := s.beginTelegramPreview(chatID, sentMsg.MessageID)
					go func(done <-chan struct{}) {
						streamCtx, streamCancel := context.WithTimeout(context.Background(), telegramProcessTimeout)
						defer streamCancel()
						defer s.clearTelegramPreview(chatID, sentMsg.MessageID, done)
						s.streamUpdatesToTelegram(streamCtx, chatID, sentMsg.MessageID, execID, done)
					}(previewDone)
				}
				return sentMsg.MessageID
			},
			OnRunnerUnavailable: func(ctx context.Context, msgText string, ackID int) {
				if ackID != 0 {
					s.editMessage(ctx, chatID, ackID, "❌ "+msgText)
				} else {
					s.sendMessage(ctx, chatID, "❌ "+msgText)
				}
			},
		},
	})
	return durablyHandedOff
}

// extractTelegramAttachment extracts file information from a Telegram message.
// Returns fileID, fileName, fileSize, mimeType. Returns empty fileID if no attachment.
func extractTelegramAttachment(message *tgbotapi.Message) (fileID, fileName string, fileSize int, mimeType string) {
	switch {
	case message.Photo != nil && len(message.Photo) > 0:
		// Photos come in multiple sizes; use the largest
		photo := message.Photo[len(message.Photo)-1]
		fileID = photo.FileID
		fileName = fmt.Sprintf("photo_%d.jpg", photo.FileSize)
		fileSize = photo.FileSize
		mimeType = "image/jpeg"

	case message.Document != nil:
		fileID = message.Document.FileID
		fileName = message.Document.FileName
		fileSize = message.Document.FileSize
		mimeType = message.Document.MimeType
		if fileName == "" {
			fileName = "document"
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

	case message.Audio != nil:
		fileID = message.Audio.FileID
		fileName = message.Audio.FileName
		fileSize = message.Audio.FileSize
		mimeType = message.Audio.MimeType
		if fileName == "" {
			fileName = "audio.mp3"
		}
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}

	case message.Video != nil:
		fileID = message.Video.FileID
		fileName = message.Video.FileName
		fileSize = message.Video.FileSize
		mimeType = message.Video.MimeType
		if fileName == "" {
			fileName = "video.mp4"
		}
		if mimeType == "" {
			mimeType = "video/mp4"
		}

	case message.Voice != nil:
		fileID = message.Voice.FileID
		fileName = "voice.ogg"
		fileSize = message.Voice.FileSize
		mimeType = message.Voice.MimeType
		if mimeType == "" {
			mimeType = "audio/ogg"
		}

	case message.VideoNote != nil:
		fileID = message.VideoNote.FileID
		fileName = "video_note.mp4"
		fileSize = message.VideoNote.FileSize
		mimeType = "video/mp4"

	case message.Sticker != nil:
		fileID = message.Sticker.FileID
		fileName = "sticker.webp"
		fileSize = message.Sticker.FileSize
		mimeType = "image/webp"
	}

	return
}

// downloadAndSaveTelegramAttachment downloads a file from Telegram servers and saves it locally.
// Returns attachment context (for text files), image attachments (for multimodal API), and chat attachment records.
func (s *TelegramService) downloadAndSaveTelegramAttachment(
	ctx context.Context,
	fileID, fileName string,
	fileSize int,
	mimeType string,
) (string, []models.Attachment, []models.ChatAttachment, error) {
	// Check file size limit
	if fileSize > telegramMaxFileSize {
		return "", nil, nil, fmt.Errorf("file too large (%d bytes, max %d)", fileSize, telegramMaxFileSize)
	}

	// Get file info from Telegram
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	tgFile, err := s.bot.GetFile(fileConfig)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get file from Telegram: %w", err)
	}

	// Download file
	fileURL := tgFile.Link(s.bot.Token)
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, fmt.Errorf("failed to download file: HTTP %d", resp.StatusCode)
	}

	// Create a temporary directory for this attachment
	tmpDir, err := os.MkdirTemp("", "telegram-attachment-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Save the file
	destPath := filepath.Join(tmpDir, filepath.Base(fileName))
	destFile, err := os.Create(destPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, nil, fmt.Errorf("failed to create file: %w", err)
	}

	written, err := io.Copy(destFile, resp.Body)
	destFile.Close()
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, nil, fmt.Errorf("failed to save file: %w", err)
	}

	applog.Infof("[telegram] downloaded attachment file=%s size=%d mime=%s path=%s", fileName, written, mimeType, destPath)

	// Make the path absolute
	absPath, err := filepath.Abs(destPath)
	if err != nil {
		absPath = destPath
	}

	mediaType := detectTelegramDownloadedAttachmentMediaType(absPath, mimeType)
	if mediaType != mimeType {
		applog.Infof("[telegram] attachment file=%s declared mime=%s but detected mime=%s; using detected mime", fileName, mimeType, mediaType)
	}

	// Build attachment data
	chatAtt := models.ChatAttachment{
		FileName:  fileName,
		FilePath:  absPath,
		MediaType: mediaType,
		FileSize:  written,
	}
	chatAttachments := []models.ChatAttachment{chatAtt}

	attachmentContext, imageAttachments := channelChatAttachmentContextAndImages(chatAttachments, telegramMaxTextFileSize)
	if len(imageAttachments) > 0 {
		applog.Infof("[telegram] image attachment file=%s size=%d", fileName, written)
	} else if attachmentContext != "" {
		applog.Infof("[telegram] non-image attachment file=%s size=%d", fileName, written)
	}

	return attachmentContext, imageAttachments, chatAttachments, nil
}

func detectTelegramDownloadedAttachmentMediaType(path, declaredMediaType string) string {
	declaredMediaType = strings.ToLower(strings.TrimSpace(strings.Split(declaredMediaType, ";")[0]))
	if declaredMediaType == "" {
		declaredMediaType = "application/octet-stream"
	}
	if declaredMediaType != "application/octet-stream" && !isTelegramImageFile(declaredMediaType) {
		return declaredMediaType
	}
	file, err := os.Open(path)
	if err != nil {
		return declaredMediaType
	}
	defer file.Close()
	head := make([]byte, 512)
	n, readErr := io.ReadFull(file, head)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return declaredMediaType
	}
	sniffedMediaType := strings.ToLower(strings.TrimSpace(http.DetectContentType(head[:n])))
	if channelChatLooksLikeWebP(head[:n]) {
		sniffedMediaType = "image/webp"
	}
	if isTelegramImageFile(sniffedMediaType) {
		return sniffedMediaType
	}
	return declaredMediaType
}

// linkAttachmentsToExecution creates database records for chat attachments and moves files
// to the execution directory for proper storage.
func generateTelegramPendingSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *TelegramService) saveChatAttachmentsToPendingSession(attachments []models.ChatAttachment) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	sessionID := generateTelegramPendingSessionID()
	sessionDir := filepath.Join(telegramUploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating pending upload directory: %w", err)
	}
	for _, att := range attachments {
		fileName := filepath.Base(att.FileName)
		if fileName == "." || fileName == string(filepath.Separator) || fileName == "" {
			fileName = "telegram-attachment"
		}
		destPath := filepath.Join(sessionDir, fmt.Sprintf("%s_%s", generateTelegramPendingSessionID()[:8], fileName))
		if err := moveOrCopyFile(att.FilePath, destPath); err != nil {
			_ = os.RemoveAll(sessionDir)
			return "", fmt.Errorf("staging %s: %w", fileName, err)
		}
		_ = os.RemoveAll(filepath.Dir(att.FilePath))
	}
	return sessionID, nil
}

func (s *TelegramService) linkAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment) ([]models.ChatAttachment, error) {
	return linkChannelChatAttachmentsToExecution(ctx, execID, attachments, channelChatAttachmentLinkOptions{
		Platform:     "telegram",
		UploadsDir:   telegramUploadsDir,
		Repo:         s.chatAttachmentRepo,
		FallbackName: "telegram-attachment",
	})
}

// isTelegramImageFile checks if a MIME type is an image type supported by Anthropic's API
func isTelegramImageFile(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// moveOrCopyFile attempts to rename (move) a file, falling back to copy+delete
// if the source and destination are on different filesystems.
func moveOrCopyFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Rename failed (likely cross-device), fall back to copy
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		os.Remove(dst)
		return err
	}

	return os.Remove(src)
}

func (s *TelegramService) beginTelegramPreview(chatID int64, messageID int) <-chan struct{} {
	if messageID == 0 {
		return nil
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.activePreviews == nil {
		s.activePreviews = make(map[telegramPreviewKey]*telegramPreviewState)
	}
	key := telegramPreviewKey{chatID: chatID, messageID: messageID}
	if existing := s.activePreviews[key]; existing != nil {
		close(existing.done)
	}
	done := make(chan struct{})
	s.activePreviews[key] = &telegramPreviewState{done: done}
	return done
}

func (s *TelegramService) finishTelegramPreview(chatID int64, messageID int) *telegramPreviewState {
	if messageID == 0 {
		return nil
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	key := telegramPreviewKey{chatID: chatID, messageID: messageID}
	state := s.activePreviews[key]
	if state == nil {
		return nil
	}
	delete(s.activePreviews, key)
	close(state.done)
	return state
}

func (s *TelegramService) clearTelegramPreview(chatID int64, messageID int, done <-chan struct{}) {
	if messageID == 0 {
		return
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	key := telegramPreviewKey{chatID: chatID, messageID: messageID}
	state := s.activePreviews[key]
	if state == nil || state.done != done {
		return
	}
	if state.richDraftVisible {
		return
	}
	delete(s.activePreviews, key)
}

func (s *TelegramService) withActiveTelegramPreview(chatID int64, messageID int, done <-chan struct{}, deliver func(state *telegramPreviewState) bool) bool {
	if done == nil {
		return deliver(nil)
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	key := telegramPreviewKey{chatID: chatID, messageID: messageID}
	state := s.activePreviews[key]
	if state == nil || state.done != done {
		return false
	}
	return deliver(state)
}

// streamUpdatesToTelegram polls the execution output and previews incremental updates in Telegram.
func (s *TelegramService) streamUpdatesToTelegram(ctx context.Context, chatID int64, messageID int, execID string, done <-chan struct{}) {
	if s.IsRichMessagesV2Enabled(ctx) {
		s.streamRichDraftUpdatesToTelegram(ctx, chatID, messageID, execID, done)
		return
	}
	s.streamEditUpdatesToTelegram(ctx, chatID, messageID, execID, done)
}

func (s *TelegramService) streamEditUpdatesToTelegram(ctx context.Context, chatID int64, messageID int, execID string, done <-chan struct{}) {
	s.streamTelegramExecutionOutput(ctx, execID, done, func(cleaned string, terminal bool) bool {
		display := telegramStreamingDisplay(cleaned, terminal)
		return s.withActiveTelegramPreview(chatID, messageID, done, func(_ *telegramPreviewState) bool {
			s.editMessage(ctx, chatID, messageID, display)
			return true
		})
	})
}

func (s *TelegramService) streamRichDraftUpdatesToTelegram(ctx context.Context, chatID int64, messageID int, execID string, done <-chan struct{}) {
	draftID := newTelegramRichDraftID()
	fallbackToEdit := false
	loggedFallback := false
	s.streamTelegramExecutionOutput(ctx, execID, done, func(cleaned string, terminal bool) bool {
		if fallbackToEdit {
			display := telegramStreamingDisplay(cleaned, terminal)
			return s.withActiveTelegramPreview(chatID, messageID, done, func(_ *telegramPreviewState) bool {
				s.editMessage(ctx, chatID, messageID, display)
				return true
			})
		}
		var sent bool
		var err error
		delivered := s.withActiveTelegramPreview(chatID, messageID, done, func(state *telegramPreviewState) bool {
			sent, err = s.sendRichMessageDraft(ctx, chatID, draftID, cleaned)
			if sent && state != nil {
				state.richDraftID = draftID
				state.richDraftVisible = true
			}
			return sent
		})
		if delivered {
			return true
		}
		if err != nil && isTelegramRichFallbackError(err) {
			if !loggedFallback {
				applog.Infof("[telegram] rich draft streaming unavailable, falling back to edit previews: %v", err)
				loggedFallback = true
			}
			fallbackToEdit = true
			display := telegramStreamingDisplay(cleaned, terminal)
			return s.withActiveTelegramPreview(chatID, messageID, done, func(_ *telegramPreviewState) bool {
				s.editMessage(ctx, chatID, messageID, display)
				return true
			})
		}
		if err != nil {
			applog.Infof("[telegram] rich draft streaming error: %v", err)
		}
		return false
	})
}

func (s *TelegramService) streamTelegramExecutionOutput(ctx context.Context, execID string, done <-chan struct{}, preview func(cleaned string, terminal bool) bool) {
	ticker := time.NewTicker(telegramStreamInterval)
	defer ticker.Stop()

	lastContent := ""

	for {
		if isTelegramPreviewDone(done) {
			return
		}
		select {
		case <-doneChan(done):
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			exec, err := s.execRepo.GetByID(ctx, execID)
			if err != nil || exec == nil {
				continue
			}
			terminal := isTelegramTerminalExecution(exec.Status)

			currentOutput := exec.Output
			if currentOutput == "" || currentOutput == lastContent {
				if terminal {
					return
				}
				continue
			}

			cleaned := llmoutput.CleanChatOutput(currentOutput)
			if cleaned == "" || cleaned == lastContent {
				if terminal {
					return
				}
				continue
			}

			if isTelegramPreviewDone(done) {
				return
			}
			if preview(cleaned, terminal) {
				lastContent = cleaned
			}

			if terminal {
				return
			}
		}
	}
}

func doneChan(done <-chan struct{}) <-chan struct{} {
	if done == nil {
		return nil
	}
	return done
}

func isTelegramPreviewDone(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func isTelegramTerminalExecution(status models.ExecutionStatus) bool {
	return status == models.ExecCompleted || status == models.ExecFailed || status == models.ExecCancelled
}

func telegramStreamingDisplay(cleaned string, terminal bool) string {
	if terminal {
		return cleaned
	}
	if len(cleaned) > maxMessageLength-50 {
		return cleaned[:maxMessageLength-50] + "\n\n⏳ _Generating..._"
	}
	return cleaned + "\n\n⏳ _Generating..._"
}

func (s *TelegramService) buildTelegramActionToolRuntime(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildTelegramActionToolRuntimeForTask(projectID, "", chatID, userID, collector)
}

func (s *TelegramService) buildTelegramActionToolRuntimeForTask(projectID, callerTaskID string, chatID int64, userID int64, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	handlers := s.telegramActionHandlersForTask(projectID, callerTaskID, chatID, userID, collector)
	return buildFullChannelActionToolRuntime(chatcontrol.SurfaceTelegram, handlers)
}

func (s *TelegramService) telegramActionHandlers(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.telegramActionHandlersForTask(projectID, "", chatID, userID, collector)
}

func (s *TelegramService) telegramActionHandlersForTask(projectID, callerTaskID string, chatID int64, userID int64, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	var llmSvcForAutomation llmServiceForAutomation
	if s.llmSvc != nil {
		llmSvcForAutomation = s.llmSvc
	}
	prepareTaskCreation, createPreparedTask := buildAutomationTaskCreationCallbacks(callerTaskID, projectID, llmSvcForAutomation)
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID:           projectID,
		TaskSvc:             s.taskSvc,
		LLMConfigRepo:       s.llmConfigRepo,
		Collector:           collector,
		PrepareTaskCreation: prepareTaskCreation,
		CreatePreparedTask:  createPreparedTask,
		OnTasksCreated: func(ctx context.Context, _ []TaskCreationRequest, createdTasks []models.Task) error {
			for _, t := range createdTasks {
				if s.taskRepo != nil {
					if err := s.taskRepo.UpdateTelegramOrigin(ctx, t.ID, chatID); err != nil {
						applog.Infof("[telegram] runtime create_task error setting telegram origin for task %s: %v", t.ID, err)
					}
				}
			}
			return nil
		},
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{ProjectID: projectID, TaskRepo: s.taskRepo, TaskGoalSvc: s.taskGoalSvc}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelThreadActionHandlers(channelThreadActionHandlerOptions{
		Platform:                 "telegram",
		ProjectID:                projectID,
		Surface:                  chatcontrol.SurfaceTelegram,
		Source:                   models.TaskOriginTelegram,
		ActorID:                  fmt.Sprintf("%d", userID),
		TaskRepo:                 s.taskRepo,
		ExecRepo:                 s.execRepo,
		ThreadInputRepo:          s.threadInputRepo,
		LLMConfigRepo:            s.llmConfigRepo,
		SettingsRepo:             s.settingsRepo,
		CustomPersonalityRepo:    s.customPersonalityRepo,
		ChannelTaskRunner:        s.channelTaskRunner,
		QueuedTaskThreadPromoter: s.queuedTaskThreadPromoter,
		CompleteExecution:        channelCompletionFunc("telegram", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
		ChannelMessageRouter:     s.channelMessageRouter,
		ReplyContext:             ChannelReplyContext{Source: models.TaskOriginTelegram, TelegramChatID: chatID},
		NewQueuedInput: func(_ *models.Task, runExecutionID, agentID string) *models.ThreadInput {
			return &models.ThreadInput{TelegramChatID: chatID}
		},
		FilterHistory: filterTelegramChatHistory,
		ConfigureSendOptions: func(opts *channelTaskThreadSendOptions) {
			opts.OnBindQueuedInputSkipped = func(_ context.Context, task *models.Task, input *models.ThreadInput, err error) {
				applog.Infof("[telegram] send_to_task task=%s input=%s active execution bind skipped: %v", task.ID, input.ID, err)
			}
			opts.OnPromotionRecheckSkipped = func(_ context.Context, task *models.Task, input *models.ThreadInput, err error) {
				applog.Infof("[telegram] send_to_task task=%s input=%s promotion recheck skipped: %v", task.ID, input.ID, err)
			}
			opts.ActiveLookupErrorResult = func(task *models.Task, err error) string {
				return fmt.Sprintf("checking active turn for task %q: %v", task.Title, err)
			}
			opts.QueueErrorResult = func(task *models.Task, err error) string {
				return fmt.Sprintf("queueing message for task %q: %v", task.Title, err)
			}
			opts.AgentSelectionErrorResult = func(task *models.Task, err error) string {
				return fmt.Sprintf("selecting agent for task %q: %v", task.Title, err)
			}
			opts.ExecutionCreateErrorResult = func(task *models.Task, err error) string {
				return fmt.Sprintf("creating follow-up execution for %q: %v", task.Title, err)
			}
			opts.RunnerUnavailableResult = func(task *models.Task, msg string) string {
				return fmt.Sprintf("sending message to task %q: %s", task.Title, msg)
			}
		},
		ResultAdapter: telegramSendToTaskActionResult,
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID:             projectID,
		CallerTaskID:          callerTaskID,
		TaskRepo:              s.taskRepo,
		ScheduleRepo:          s.scheduleRepo,
		AutomationGraphSvc:    s.automationGraphSvc,
		WorkerSvc:             s.workerSvc,
		LLMConfigRepo:         s.llmConfigRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		AlertSvc:              s.alertSvc,
		TelegramRunning:       s.IsRunning,
		TelegramAuthRepo:      telegramAuthListStore(s.telegramAuthRepo),
		EmailStatus:           s.emailStatus,
		EmailAuthRepo:         s.emailAuthRepo,
		WebhookRepo:           s.webhookRepo,
		ChannelTargets:        channelTargetsFromRouter(s.channelMessageRouter),
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:   projectID,
		ProjectRepo: s.projectRepo,
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			if !s.checkAuthorization(userID, "", project.ID) {
				return fmt.Errorf("Telegram user %d is not authorized to use project %q", userID, project.Name)
			}
			return s.setTelegramActiveProject(ctx, userID, project.ID)
		},
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
		ChannelDisplayName: "Telegram",
		ProjectID:          projectID,
		ProjectRepo:        s.projectRepo,
	}))
	return handlers
}

func telegramSendToTaskActionResult(result string) (string, error) {
	if strings.HasPrefix(result, "checking active turn") || strings.HasPrefix(result, "queueing message") || strings.HasPrefix(result, "selecting agent") || strings.HasPrefix(result, "creating follow-up execution") || strings.HasPrefix(result, "sending message") || strings.HasPrefix(result, "updating task") {
		return "", errors.New(result)
	}
	return result, nil
}

func (s *TelegramService) setTelegramActiveProject(ctx context.Context, userID int64, projectID string) error {
	// Serialize explicit switches so an older persistence operation cannot finish
	// after and overwrite a newer successful switch. Cache locks remain short-held.
	s.userProjectSwitchMu.Lock()
	defer s.userProjectSwitchMu.Unlock()

	if s.telegramUserProjectRepo != nil {
		if err := s.telegramUserProjectRepo.SetUserProject(ctx, fmt.Sprintf("%d", userID), projectID); err != nil {
			applog.Infof("[telegram] error persisting active project selection: %v", err)
			return fmt.Errorf("persist failed: %w", err)
		}
	}
	s.cacheTelegramActiveProject(userID, projectID)
	return nil
}

// handleNaturalLanguageProjectCommand detects natural language project commands
// (e.g. "list projects", "switch to project X") and handles them directly
// without forwarding to the LLM. Returns the response string and true if handled.
func (s *TelegramService) handleNaturalLanguageProjectCommand(userID int64, text string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(text))

	// Detect project list requests
	if isProjectListRequest(lower) {
		return s.handleProject(userID, ""), true
	}

	// Detect project switch requests
	if projectName := extractProjectSwitchTarget(lower); projectName != "" {
		return s.handleProject(userID, projectName), true
	}

	return "", false
}

// isProjectListRequest returns true if the message is asking to list projects.
func isProjectListRequest(lower string) bool {
	listPhrases := []string{
		"list projects",
		"list all projects",
		"show projects",
		"show all projects",
		"show my projects",
		"my projects",
		"available projects",
		"what projects",
		"which projects",
	}
	for _, phrase := range listPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// projectSwitchPatterns are regex patterns for detecting project switch commands.
// Each pattern should capture the project name in group 1.
var projectSwitchPatterns = []*regexp.Regexp{
	// More specific patterns first (with "to" after "project")
	regexp.MustCompile(`(?i)^switch\s+project\s+to\s+(.+)$`),
	regexp.MustCompile(`(?i)^change\s+project\s+to\s+(.+)$`),
	regexp.MustCompile(`(?i)^set\s+project\s+to\s+(.+)$`),
	// Less specific patterns (with optional "to" before "project")
	regexp.MustCompile(`(?i)^switch\s+(?:to\s+)?project\s+(.+)$`),
	regexp.MustCompile(`(?i)^change\s+(?:to\s+)?project\s+(.+)$`),
	regexp.MustCompile(`(?i)^use\s+project\s+(.+)$`),
	regexp.MustCompile(`(?i)^set\s+project\s+(.+)$`),
	regexp.MustCompile(`(?i)^select\s+project\s+(.+)$`),
}

// extractProjectSwitchTarget extracts the target project name from a natural
// language switch command. Returns empty string if the message is not a switch command.
func extractProjectSwitchTarget(lower string) string {
	for _, re := range projectSwitchPatterns {
		if m := re.FindStringSubmatch(lower); len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// selectDefaultAgent retrieves the default model or falls back to the first available.
func (s *TelegramService) selectDefaultAgent(ctx context.Context) (*models.LLMConfig, error) {
	agents, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents configured")
	}
	for i := range agents {
		if agents[i].IsDefault {
			return &agents[i], nil
		}
	}
	return &agents[0], nil
}

// telegramMaxThreadTranscriptBytes is the total size budget for a Telegram thread transcript (80KB).
const telegramMaxThreadTranscriptBytes = 80 * 1024

// telegramMaxPerMessageBytes is a safety limit for a single message within Telegram transcripts (50KB).
const telegramMaxPerMessageBytes = 50 * 1024

// formatThreadTranscript formats a task's execution history as a readable thread transcript.
// offset/limit control pagination: offset is the execution index to start from (0-based),
// limit is the max number of executions to include (0 = all that fit).
func formatThreadTranscript(task *models.Task, executions []models.Execution, offset, limit int) string {
	total := len(executions)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n\n---\n**Thread history for task: \"%s\"** [TASK_ID:%s]\n", task.Title, task.ID))
	sb.WriteString(fmt.Sprintf("Status: %s | Category: %s | Priority: %d\n", task.Status, task.Category, task.Priority))
	sb.WriteString(fmt.Sprintf("Total executions: %d\n\n", total))

	if total == 0 {
		sb.WriteString("No execution history found for this task.\n")
		return sb.String()
	}

	// Apply offset
	if offset > 0 {
		if offset >= total {
			sb.WriteString(fmt.Sprintf("Offset %d exceeds total executions (%d). Use a lower offset.\n", offset, total))
			return sb.String()
		}
		executions = executions[offset:]
	}

	// Apply limit
	if limit > 0 && limit < len(executions) {
		executions = executions[:limit]
	}

	// Format each execution, tracking total size
	budgetExceeded := false
	included := 0
	for i, exec := range executions {
		execIdx := offset + i
		timestamp := exec.StartedAt.Local().Format("2006-01-02 15:04:05")

		var entry strings.Builder

		// User message
		prompt := exec.PromptSent
		if !exec.IsFollowup && execIdx == 0 {
			prompt = task.Prompt
		}
		if prompt != "" {
			prompt = util.TruncateWithSuffix(prompt, telegramMaxPerMessageBytes, "\n... (message truncated at 50KB)")
			entry.WriteString(fmt.Sprintf("**[%s] User:**\n%s\n\n", timestamp, prompt))
		}

		// Assistant response
		if exec.Output != "" {
			cleaned := util.TruncateWithSuffix(exec.Output, telegramMaxPerMessageBytes, "\n... (message truncated at 50KB)")
			entry.WriteString(fmt.Sprintf("**[%s] Assistant** (status: %s):\n%s\n\n", timestamp, exec.Status, cleaned))
		}

		// Error message
		if exec.ErrorMessage != "" {
			entry.WriteString(fmt.Sprintf("**Error:** %s\n\n", exec.ErrorMessage))
		}

		// Check total budget before appending
		if sb.Len()+entry.Len() > telegramMaxThreadTranscriptBytes {
			budgetExceeded = true
			break
		}

		sb.WriteString(entry.String())
		included++
	}

	remaining := total - offset - included
	if budgetExceeded && remaining > 0 {
		sb.WriteString(fmt.Sprintf("\n---\n⚠️ Transcript size limit reached. Showing executions %d–%d of %d. Use `offset: %d` to fetch the next page.\n",
			offset+1, offset+included, total, offset+included))
	} else if offset > 0 {
		sb.WriteString(fmt.Sprintf("\n---\nShowing executions %d–%d of %d.\n", offset+1, offset+included, total))
	}

	return sb.String()
}

// buildTelegramTaskChatContext builds the system context for task chat follow-ups.
func buildTelegramTaskChatContext(taskTitle string, hasHistory bool) string {
	if hasHistory {
		return fmt.Sprintf("You are continuing work on a task titled %q. The conversation history shows the original task prompt and all prior work done on this task. The user's new message is a follow-up instruction — continue from where you left off, do NOT restart the original task from scratch.", taskTitle)
	}
	return "You are starting work on a task. The task prompt is provided as the user's message below."
}

// filterTelegramChatHistory filters executions to exclude the current one and running ones
func filterTelegramChatHistory(executions []models.Execution, currentExecID string) []models.Execution {
	if len(executions) == 0 {
		return []models.Execution{}
	}

	result := make([]models.Execution, 0, len(executions))
	for i := range executions {
		if executions[i].ID == currentExecID || executions[i].Status == models.ExecRunning {
			continue
		}
		result = append(result, executions[i])
	}
	return result
}

// getActiveProject returns the active project ID for a user.
func (s *TelegramService) getActiveProject(userID int64) string {
	projectID, ok, cacheVersion := s.cachedTelegramActiveProject(userID)
	if s.activeProjectReadHook != nil {
		s.activeProjectReadHook(userID)
	}
	if ok {
		return projectID
	}

	// Keep database and project-list reads outside the cache lock. The cache
	// version prevents a concurrent explicit switch from being overwritten by
	// the stale result of either lookup.
	if s.telegramUserProjectRepo != nil {
		savedProjectID, err := s.telegramUserProjectRepo.GetUserProject(context.Background(), fmt.Sprintf("%d", userID))
		if err != nil {
			applog.Infof("[telegram] error loading persisted project for user %d: %v", userID, err)
		} else if savedProjectID != "" {
			return s.populateTelegramActiveProject(userID, savedProjectID, cacheVersion)
		}
	}

	projects, err := s.projectRepo.List(context.Background())
	if err != nil || len(projects) == 0 {
		return ""
	}

	projectID = projects[0].ID
	for _, project := range projects {
		if project.IsDefault {
			projectID = project.ID
			break
		}
	}
	return s.populateTelegramActiveProject(userID, projectID, cacheVersion)
}

func (s *TelegramService) cachedTelegramActiveProject(userID int64) (string, bool, uint64) {
	s.userProjectsMu.RLock()
	defer s.userProjectsMu.RUnlock()
	projectID, ok := s.userProjects[userID]
	return projectID, ok, s.userProjectVersions[userID]
}

func (s *TelegramService) cacheTelegramActiveProject(userID int64, projectID string) {
	s.userProjectsMu.Lock()
	defer s.userProjectsMu.Unlock()
	if s.userProjects == nil {
		s.userProjects = make(map[int64]string)
	}
	if s.userProjectVersions == nil {
		s.userProjectVersions = make(map[int64]uint64)
	}
	s.userProjects[userID] = projectID
	s.userProjectVersions[userID]++
}

func (s *TelegramService) populateTelegramActiveProject(userID int64, projectID string, expectedVersion uint64) string {
	s.userProjectsMu.Lock()
	defer s.userProjectsMu.Unlock()
	if currentProjectID, ok := s.userProjects[userID]; ok || s.userProjectVersions[userID] != expectedVersion {
		return currentProjectID
	}
	if s.userProjects == nil {
		s.userProjects = make(map[int64]string)
	}
	s.userProjects[userID] = projectID
	return projectID
}

type telegramInputRichMessage struct {
	Markdown            string `json:"markdown,omitempty"`
	HTML                string `json:"html,omitempty"`
	IsRTL               bool   `json:"is_rtl,omitempty"`
	SkipEntityDetection bool   `json:"skip_entity_detection,omitempty"`
}

func telegramRichMarkdownPayload(text string) telegramInputRichMessage {
	return telegramInputRichMessage{Markdown: normalizeTelegramRichMarkdown(text)}
}

func normalizeTelegramRichMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func escapeTelegramMarkdownV2(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

func isTelegramRichFallbackError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "method") ||
		strings.Contains(msg, "rich_message") ||
		strings.Contains(msg, "can't parse") ||
		strings.Contains(msg, "bad request")
}

func newTelegramRichDraftID() int {
	id := int(time.Now().UnixNano() & 0x7fffffff)
	if id == 0 {
		return 1
	}
	return id
}

func (s *TelegramService) makeTelegramRequest(endpoint string, params tgbotapi.Params) (*tgbotapi.APIResponse, error) {
	if s.makeRequestFunc != nil {
		return s.makeRequestFunc(endpoint, params)
	}
	if s.bot == nil {
		return nil, fmt.Errorf("telegram bot is not configured")
	}
	return s.bot.MakeRequest(endpoint, params)
}

func (s *TelegramService) sendConfig(config tgbotapi.Chattable) (tgbotapi.Message, error) {
	if s.sendConfigFunc != nil {
		return s.sendConfigFunc(config)
	}
	if s.bot == nil {
		return tgbotapi.Message{}, fmt.Errorf("telegram bot is not configured")
	}
	return s.bot.Send(config)
}

func (s *TelegramService) sendRichMessage(ctx context.Context, chatID int64, text string) (bool, error) {
	if !s.IsRichMessagesV2Enabled(ctx) {
		return false, nil
	}
	params := tgbotapi.Params{"chat_id": strconv.FormatInt(chatID, 10)}
	if err := params.AddInterface("rich_message", telegramRichMarkdownPayload(text)); err != nil {
		return false, err
	}
	if _, err := s.makeTelegramRequest("sendRichMessage", params); err != nil {
		return false, err
	}
	return true, nil
}

func (s *TelegramService) editRichMessage(ctx context.Context, chatID int64, messageID int, text string) (bool, error) {
	if !s.IsRichMessagesV2Enabled(ctx) {
		return false, nil
	}
	params := tgbotapi.Params{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(messageID),
	}
	if err := params.AddInterface("rich_message", telegramRichMarkdownPayload(text)); err != nil {
		return false, err
	}
	if _, err := s.makeTelegramRequest("editMessageText", params); err != nil {
		return false, err
	}
	return true, nil
}

func (s *TelegramService) sendRichMessageDraft(ctx context.Context, chatID int64, draftID int, text string) (bool, error) {
	if !s.IsRichMessagesV2Enabled(ctx) {
		return false, nil
	}
	if draftID == 0 {
		return false, fmt.Errorf("telegram rich draft id must be non-zero")
	}
	params := tgbotapi.Params{
		"chat_id":  strconv.FormatInt(chatID, 10),
		"draft_id": strconv.Itoa(draftID),
	}
	if err := params.AddInterface("rich_message", telegramRichMarkdownPayload(text)); err != nil {
		return false, err
	}
	if _, err := s.makeTelegramRequest("sendRichMessageDraft", params); err != nil {
		return false, err
	}
	return true, nil
}

// sendMessage sends a message to a chat, splitting if needed.
func (s *TelegramService) sendMessage(ctx context.Context, chatID int64, text string) {
	if err := s.sendMessageToTarget(ctx, chatID, 0, text); err != nil {
		applog.Infof("[telegram] error sending message: %v", err)
	}
}

func (s *TelegramService) sendLegacyMessage(ctx context.Context, chatID int64, text string) bool {
	delivered := true
	for _, msg := range splitMessage(text, maxMessageLength) {
		msgConfig := tgbotapi.NewMessage(chatID, escapeTelegramMarkdownV2(msg))
		msgConfig.ParseMode = "MarkdownV2"
		if _, err := s.sendConfig(msgConfig); err != nil {
			applog.Infof("[telegram] error sending message with MarkdownV2: %v, retrying without formatting", err)
			plainConfig := tgbotapi.NewMessage(chatID, msg)
			if _, err := s.sendConfig(plainConfig); err != nil {
				applog.Infof("[telegram] error sending message: %v", err)
				delivered = false
			}
		}
	}
	return delivered
}

func (s *TelegramService) sendMessageToTarget(ctx context.Context, chatID int64, threadID int, text string) error {
	if s.sendMessageFunc != nil && threadID == 0 {
		s.sendMessageFunc(chatID, text)
		return nil
	}

	if threadID == 0 {
		if sent, err := s.sendRichMessage(ctx, chatID, text); sent {
			return nil
		} else if err != nil {
			if isTelegramRichFallbackError(err) {
				applog.Infof("[telegram] rich message send unavailable, falling back: %v", err)
			} else {
				applog.Infof("[telegram] rich message send returned ambiguous error; not sending fallback to avoid duplicate output: %v", err)
				return err
			}
		}
	}

	var firstErr error
	messages := splitMessage(text, maxMessageLength)
	for _, msg := range messages {
		if threadID > 0 {
			params := tgbotapi.Params{
				"chat_id":           strconv.FormatInt(chatID, 10),
				"text":              escapeTelegramMarkdownV2(msg),
				"parse_mode":        "MarkdownV2",
				"message_thread_id": strconv.Itoa(threadID),
			}
			if _, err := s.makeTelegramRequest("sendMessage", params); err != nil {
				applog.Infof("[telegram] error sending threaded message with MarkdownV2: %v, retrying without formatting", err)
				if firstErr == nil {
					firstErr = err
				}
				plainParams := tgbotapi.Params{
					"chat_id":           strconv.FormatInt(chatID, 10),
					"text":              msg,
					"message_thread_id": strconv.Itoa(threadID),
				}
				if _, err := s.makeTelegramRequest("sendMessage", plainParams); err != nil {
					applog.Infof("[telegram] error sending threaded message: %v", err)
					if firstErr == nil {
						firstErr = err
					}
				}
			}
			continue
		}
		msgConfig := tgbotapi.NewMessage(chatID, escapeTelegramMarkdownV2(msg))
		msgConfig.ParseMode = "MarkdownV2"
		if _, err := s.sendConfig(msgConfig); err != nil {
			applog.Infof("[telegram] error sending message with MarkdownV2: %v, retrying without formatting", err)
			if firstErr == nil {
				firstErr = err
			}
			plainConfig := tgbotapi.NewMessage(chatID, msg)
			if _, err := s.sendConfig(plainConfig); err != nil {
				applog.Infof("[telegram] error sending message: %v", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (s *TelegramService) SendOutboundMessage(ctx context.Context, chatID int64, threadID int, text string) SendMessageResult {
	if chatID == 0 {
		return SendMessageResult{OK: false, Platform: "telegram", Error: "telegram chat id is required"}
	}
	if strings.TrimSpace(text) == "" {
		return SendMessageResult{OK: false, Platform: "telegram", Target: formatResolvedMessageTarget("telegram", fmt.Sprintf("%d", chatID), threadIDString(threadID)), Error: "message is required"}
	}
	if err := s.sendMessageToTarget(ctx, chatID, threadID, text); err != nil {
		return SendMessageResult{OK: false, Platform: "telegram", Target: formatResolvedMessageTarget("telegram", fmt.Sprintf("%d", chatID), threadIDString(threadID)), Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "telegram", Target: formatResolvedMessageTarget("telegram", fmt.Sprintf("%d", chatID), threadIDString(threadID))}
}

func threadIDString(threadID int) string {
	if threadID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", threadID)
}

// editMessage edits an existing Telegram message.
func (s *TelegramService) editMessage(ctx context.Context, chatID int64, messageID int, text string) {
	if s.editMessageFunc != nil {
		s.editMessageFunc(chatID, messageID, text)
		return
	}
	if sent, err := s.editRichMessage(ctx, chatID, messageID, text); sent {
		return
	} else if err != nil {
		if isTelegramRichFallbackError(err) {
			applog.Infof("[telegram] rich message edit unavailable, falling back: %v", err)
		} else {
			applog.Infof("[telegram] rich message edit error, falling back to legacy edit: %v", err)
		}
	}

	legacyText := text
	if len(legacyText) > maxMessageLength {
		legacyText = legacyText[:maxMessageLength-3] + "..."
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, escapeTelegramMarkdownV2(legacyText))
	edit.ParseMode = "MarkdownV2"
	if _, err := s.sendConfig(edit); err != nil {
		plainEdit := tgbotapi.NewEditMessageText(chatID, messageID, legacyText)
		if _, err := s.sendConfig(plainEdit); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
				applog.Infof("[telegram] error editing message: %v", err)
			}
		}
	}
}

// splitMessage splits a message into chunks that fit Telegram's limit
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var messages []string
	lines := strings.Split(text, "\n")
	var currentMsg strings.Builder

	for _, line := range lines {
		if currentMsg.Len()+len(line)+1 > maxLen {
			if currentMsg.Len() > 0 {
				messages = append(messages, currentMsg.String())
				currentMsg.Reset()
			}

			// If single line is too long, split it
			if len(line) > maxLen {
				for i := 0; i < len(line); i += maxLen {
					end := i + maxLen
					if end > len(line) {
						end = len(line)
					}
					messages = append(messages, line[i:end])
				}
				continue
			}
		}

		if currentMsg.Len() > 0 {
			currentMsg.WriteString("\n")
		}
		currentMsg.WriteString(line)
	}

	if currentMsg.Len() > 0 {
		messages = append(messages, currentMsg.String())
	}

	return messages
}

// escapeTelegramCommands prevents forward slashes in user text from being
// detected as bot commands by inserting a zero-width space after each slash.
func escapeTelegramCommands(s string) string {
	return strings.ReplaceAll(s, "/", "/\u200B")
}

// getStatusIcon returns an emoji icon for task status
func getStatusIcon(status models.TaskStatus) string {
	switch status {
	case models.StatusPending:
		return "⏳"
	case models.StatusRunning:
		return "🔄"
	case models.StatusCompleted:
		return "✅"
	case models.StatusFailed:
		return "❌"
	case models.StatusCancelled:
		return "🚫"
	default:
		return "❓"
	}
}

// isHexID returns true if the input looks like a 32-character hex project ID
func isHexID(s string) bool {
	return hexIDPattern.MatchString(s)
}

// ParseTaskID extracts a task ID from various formats (with or without backticks)
func ParseTaskID(input string) (string, error) {
	input = strings.TrimSpace(input)
	input = strings.Trim(input, "`")

	if input == "" {
		return "", fmt.Errorf("empty task ID")
	}

	return input, nil
}

// FormatTaskID formats a task ID for display in Telegram (with backticks for monospace)
func FormatTaskID(taskID string) string {
	return fmt.Sprintf("`%s`", taskID)
}

// IsSendResponsesEnabled checks the Telegram send-responses setting.
// Returns true (default) when the setting is not explicitly "false".
func (s *TelegramService) IsSendResponsesEnabled(ctx context.Context) bool {
	if s.settingsRepo == nil {
		return true
	}
	val, err := s.settingsRepo.Get(ctx, TelegramSettingSendResponses)
	if err != nil || strings.TrimSpace(val) == "" {
		return true // default: enabled
	}
	return !strings.EqualFold(strings.TrimSpace(val), "false")
}

// IsRichMessagesV2Enabled checks the Telegram rich messaging setting.
// Returns true by default; only an explicit saved "false" disables rich delivery.
func (s *TelegramService) IsRichMessagesV2Enabled(ctx context.Context) bool {
	if s.settingsRepo == nil {
		return true
	}
	val, err := s.settingsRepo.Get(ctx, TelegramSettingRichMessagesV2)
	if err != nil {
		return true
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return true
	}
	return !strings.EqualFold(val, "false")
}

func (s *TelegramService) SendTaskCompletionToChat(ctx context.Context, chatID int64, taskTitle, output string, errMsg string) {
	if chatID == 0 {
		return
	}
	if !s.IsSendResponsesEnabled(ctx) {
		return
	}
	var message string
	if errMsg != "" {
		message = fmt.Sprintf("❌ *Task failed:* %s\n\n%s", taskTitle, util.Truncate(errMsg, 500))
	} else {
		cleaned := llmoutput.CleanChatOutputForDisplay(output)
		if cleaned == "" {
			cleaned = "(No output)"
		}
		message = fmt.Sprintf("✅ *Task completed:* %s\n\n%s", taskTitle, util.Truncate(cleaned, 3500))
	}
	s.sendMessage(ctx, chatID, message)
	applog.Infof("[telegram] sent completion notification for task %q to chat %d", taskTitle, chatID)
}

// SendTaskCompletionNotification sends a task result back to the Telegram user
// who created it, if send-responses is enabled and the task originated from Telegram.
func (s *TelegramService) SendTaskCompletionNotification(ctx context.Context, task models.Task, output string, errMsg string) {
	needsHydration := task.CreatedVia != models.TaskOriginTelegram || task.TelegramChatID == 0
	if needsHydration {
		if task.ID == "" || s.taskRepo == nil {
			applog.Infof("[telegram] completion notification task %s missing Telegram origin and cannot reload (has_id=%t task_repo_set=%t)", task.ID, task.ID != "", s.taskRepo != nil)
		} else {
			applog.Infof("[telegram] completion notification task %s missing Telegram origin in memory (created_via=%q chat_id=%d), reloading from DB", task.ID, task.CreatedVia, task.TelegramChatID)
			loadedTask, err := s.taskRepo.GetByID(ctx, task.ID)
			if err != nil {
				applog.Infof("[telegram] failed reloading task %s for completion notification: %v", task.ID, err)
			} else if loadedTask == nil {
				applog.Infof("[telegram] task %s not found during completion notification reload", task.ID)
			} else {
				task = *loadedTask
				applog.Infof("[telegram] reloaded task %s for completion notification (created_via=%q chat_id=%d category=%s)", task.ID, task.CreatedVia, task.TelegramChatID, task.Category)
			}
		}
	}

	if task.CreatedVia != models.TaskOriginTelegram {
		applog.Infof("[telegram] skipping completion notification for task %s: created_via=%q", task.ID, task.CreatedVia)
		return
	}
	if task.TelegramChatID == 0 {
		applog.Infof("[telegram] skipping completion notification for task %s: missing telegram chat id", task.ID)
		return
	}

	// Check the setting
	if !s.IsSendResponsesEnabled(ctx) {
		applog.Infof("[telegram] send-responses disabled, skipping notification for task %s", task.ID)
		return
	}

	// Don't notify for chat tasks (they already get a direct response)
	if task.Category == models.CategoryChat {
		applog.Infof("[telegram] skipping completion notification for task %s: category=chat", task.ID)
		return
	}

	var message string
	if errMsg != "" {
		message = fmt.Sprintf("❌ *Task failed:* %s\n\n%s", task.Title, util.Truncate(errMsg, 500))
	} else {
		cleaned := llmoutput.CleanChatOutputForDisplay(output)
		if cleaned == "" {
			cleaned = "(No output)"
		}
		message = fmt.Sprintf("✅ *Task completed:* %s\n\n%s", task.Title, util.Truncate(cleaned, 3500))
	}

	s.sendMessage(ctx, task.TelegramChatID, message)
	applog.Infof("[telegram] sent completion notification for task %s to chat %d", task.ID, task.TelegramChatID)
}

func firstInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

// SendChatResponse sends a completed chat-orchestrator response back to the
// originating Telegram chat. Unlike task completion notifications, this is only
// for chat-category tasks that were promoted from queued channel input.
func (s *TelegramService) SendChatResponse(ctx context.Context, task models.Task, output string, errMsg string, messageID ...int) {
	if task.CreatedVia != models.TaskOriginTelegram || task.Category != models.CategoryChat || task.TelegramChatID == 0 {
		return
	}
	if !s.IsSendResponsesEnabled(ctx) {
		return
	}
	msgID := firstInt(messageID)
	if errMsg != "" {
		message := fmt.Sprintf("❌ Error: %s", util.Truncate(errMsg, 197))
		s.deliverTelegramChatFinal(ctx, task.TelegramChatID, msgID, message)
		return
	}
	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		cleaned = "(No response)"
	}
	s.deliverTelegramChatFinal(ctx, task.TelegramChatID, msgID, cleaned)
}

func (s *TelegramService) deliverTelegramChatFinal(ctx context.Context, chatID int64, messageID int, text string) {
	if messageID == 0 {
		s.sendMessage(ctx, chatID, text)
		return
	}
	previewState := s.finishTelegramPreview(chatID, messageID)
	if previewState != nil && previewState.richDraftVisible {
		if sent, sendErr := s.sendRichMessage(ctx, chatID, text); sent {
			s.clearTelegramChatPlaceholderAfterRichFinalSend(chatID, messageID)
			return
		} else if sendErr != nil {
			if !isTelegramRichFallbackError(sendErr) {
				applog.Infof("[telegram] rich final send after visible draft returned ambiguous error; not editing placeholder to avoid duplicate final output: %v", sendErr)
				return
			}
			applog.Infof("[telegram] rich final send unavailable after a visible draft; falling back to legacy final send: %v", sendErr)
			if s.sendLegacyMessage(ctx, chatID, text) {
				s.clearTelegramChatPlaceholderAfterRichFinalSend(chatID, messageID)
			}
			return
		}
		return
	}
	if s.editMessageFunc != nil {
		s.editMessage(ctx, chatID, messageID, text)
		return
	}
	if sent, err := s.editRichMessage(ctx, chatID, messageID, text); sent {
		return
	} else if err != nil {
		if !isTelegramRichFallbackError(err) {
			applog.Infof("[telegram] rich final edit returned ambiguous error; not sending fallback to avoid duplicate final output: %v", err)
			return
		}
		applog.Infof("[telegram] rich final edit unavailable, trying rich send before legacy edit fallback: %v", err)
		if sent, sendErr := s.sendRichMessage(ctx, chatID, text); sent {
			s.clearTelegramChatPlaceholderAfterRichFinalSend(chatID, messageID)
			return
		} else if sendErr != nil {
			if isTelegramRichFallbackError(sendErr) {
				applog.Infof("[telegram] rich final send unavailable, falling back to legacy placeholder edit: %v", sendErr)
			} else {
				applog.Infof("[telegram] rich final send returned ambiguous error; not editing placeholder to avoid duplicate final output: %v", sendErr)
				return
			}
		}
	}
	s.editMessage(ctx, chatID, messageID, text)
}

func (s *TelegramService) clearTelegramChatPlaceholderAfterRichFinalSend(chatID int64, messageID int) {
	const clearedPlaceholderText = "✅ Response sent."
	if s.editMessageFunc != nil {
		s.editMessageFunc(chatID, messageID, clearedPlaceholderText)
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, clearedPlaceholderText)
	if _, err := s.sendConfig(edit); err != nil {
		applog.Infof("[telegram] error clearing placeholder after rich final send: %v", err)
	}
}
