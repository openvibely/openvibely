package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	telegramStreamInterval   = 2 * time.Second
	telegramProcessTimeout   = 5 * time.Minute
	telegramChatHistoryLimit = 50
	telegramMaxFileSize      = 20 << 20   // 20 MB (Telegram Bot API limit)
	telegramMaxTextFileSize  = 100 * 1024 // 100KB for text content injection
)

var telegramUploadsDir = "uploads" // same as handler's uploadsDir

// TelegramService manages Telegram bot integration.
// It acts as a proxy to the /chat page orchestrator — every message sent to the bot
// is forwarded to the same chat assistant that powers the /chat web UI.
type TelegramService struct {
	bot                     *tgbotapi.BotAPI
	taskSvc                 *TaskService
	projectRepo             *repository.ProjectRepo
	llmConfigRepo           *repository.LLMConfigRepo
	taskRepo                *repository.TaskRepo
	execRepo                *repository.ExecutionRepo
	scheduleRepo            *repository.ScheduleRepo
	chatAttachmentRepo      *repository.ChatAttachmentRepo
	telegramAuthRepo        *repository.TelegramAuthRepo
	telegramUserProjectRepo *repository.TelegramUserProjectRepo
	settingsRepo            *repository.SettingsRepo
	customPersonalityRepo   *repository.CustomPersonalityRepo
	agentRepo               *repository.AgentRepo
	alertSvc                *AlertService
	llmSvc                  *LLMService
	workerSvc               *WorkerService
	chatBroadcaster         *events.ChatBroadcaster
	sendMessageFunc         func(chatID int64, text string)
	userProjects            map[int64]string // Maps Telegram user ID to active project ID
	ctx                     context.Context
	cancel                  context.CancelFunc
	running                 bool
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
	log.Printf("[telegram] authorized on account %s", bot.Self.UserName)

	ctx, cancel := context.WithCancel(context.Background())

	return &TelegramService{
		bot:                bot,
		taskSvc:            taskSvc,
		projectRepo:        projectRepo,
		llmConfigRepo:      llmConfigRepo,
		taskRepo:           taskRepo,
		execRepo:           execRepo,
		scheduleRepo:       scheduleRepo,
		chatAttachmentRepo: chatAttachmentRepo,
		llmSvc:             llmSvc,
		workerSvc:          workerSvc,
		userProjects:       make(map[int64]string),
		ctx:                ctx,
		cancel:             cancel,
	}, nil
}

// IsRunning returns whether the bot is currently running.
func (s *TelegramService) IsRunning() bool {
	return s.running
}

// SetChatBroadcaster sets the chat event broadcaster for real-time chat updates.
func (s *TelegramService) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	s.chatBroadcaster = cb
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

// SetAgentRepo sets the agent repo for listing agent definitions from Telegram chat.
func (s *TelegramService) SetAgentRepo(repo *repository.AgentRepo) {
	s.agentRepo = repo
}

// SetAlertService sets the alert service for managing alerts from Telegram chat.
func (s *TelegramService) SetAlertService(svc *AlertService) {
	s.alertSvc = svc
}

// checkAuthorization verifies that a Telegram user is authorized for the given project.
// Deny-by-default: if the auth repo is configured, the user must be explicitly listed.
// If projectID is empty, checks authorization across all projects.
func (s *TelegramService) checkAuthorization(userID int64, username string, projectID string) bool {
	if s.telegramAuthRepo == nil {
		log.Printf("[telegram] auth check: no auth repo configured, allowing user %d (%s)", userID, username)
		return true // No auth repo configured, allow all
	}

	ctx := context.Background()

	if projectID == "" {
		// No project selected yet — check if user is authorized in ANY project
		authorized, err := s.telegramAuthRepo.IsAuthorizedAnywhere(ctx, userID, username)
		if err != nil {
			log.Printf("[telegram] error checking global authorization for user %d: %v", userID, err)
			return true // Fail open on error
		}
		log.Printf("[telegram] auth check: user %d (%s) global authorized=%v", userID, username, authorized)
		return authorized
	}

	// Check if this specific user is authorized for the project
	authorized, err := s.telegramAuthRepo.IsAuthorized(ctx, projectID, userID, username)
	if err != nil {
		log.Printf("[telegram] error checking authorization for user %d: %v", userID, err)
		return true // Fail open on error
	}

	// If not authorized for the specific project, fall back to checking any project.
	// Users may have been added to a different project than their current active one.
	if !authorized {
		authorized, err = s.telegramAuthRepo.IsAuthorizedAnywhere(ctx, userID, username)
		if err != nil {
			log.Printf("[telegram] error checking global authorization for user %d: %v", userID, err)
			return true // Fail open on error
		}
	}

	log.Printf("[telegram] auth check: user %d (%s) project=%s authorized=%v", userID, username, projectID, authorized)

	// If authorized via username match, backfill the user ID for future lookups
	if authorized && username != "" {
		_ = s.telegramAuthRepo.BackfillUserID(ctx, projectID, username, userID)
	}

	return authorized
}

// UpdateToken stops the current bot, reinitializes with the new token, and starts again.
func (s *TelegramService) UpdateToken(token string) error {
	if s.running {
		s.Stop()
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return fmt.Errorf("invalid telegram token: %w", err)
	}
	s.bot = bot
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	s.Start()
	return nil
}

// Start begins listening for Telegram updates
func (s *TelegramService) Start() {
	s.running = true
	go s.run()
}

// Stop stops the Telegram bot
func (s *TelegramService) Stop() {
	log.Println("[telegram] stopping bot")
	s.running = false
	s.cancel()
	s.bot.StopReceivingUpdates()
}

// run is the main bot loop
func (s *TelegramService) run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := s.bot.GetUpdatesChan(u)

	log.Println("[telegram] bot started, waiting for updates")

	for {
		select {
		case <-s.ctx.Done():
			log.Println("[telegram] bot stopped")
			return
		case update := <-updates:
			if update.Message == nil {
				continue
			}

			// Check authorization before processing any message (including commands)
			userID := update.Message.From.ID
			chatID := update.Message.Chat.ID
			username := update.Message.From.UserName

			// Resolve the user's active project for authorization check
			projectID := s.getActiveProject(userID)
			if !s.checkAuthorization(userID, username, projectID) {
				log.Printf("[telegram] unauthorized access attempt from user %d (username: %s) for project %s",
					userID, username, projectID)
				s.sendMessage(chatID, "You are not authorized to use this bot. Contact the project owner to get access.")
				continue
			}

			// Handle special commands: /start and /project
			if update.Message.IsCommand() {
				cmd := update.Message.Command()
				switch cmd {
				case "start":
					response := s.handleStart(update.Message.From.ID)
					s.sendMessage(update.Message.Chat.ID, response)
					continue
				case "project":
					response := s.handleProject(update.Message.From.ID, update.Message.CommandArguments())
					s.sendMessage(update.Message.Chat.ID, response)
					continue
				}
			}

			// Check for natural language project commands before forwarding to LLM
			if response, handled := s.handleNaturalLanguageProjectCommand(userID, update.Message.Text); handled {
				s.sendMessage(chatID, response)
				continue
			}

			// Forward all other messages (including unrecognized commands) to the chat orchestrator
			go s.handleChatMessage(update.Message)
		}
	}
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

	s.userProjects[userID] = defaultProject.ID
	if s.telegramUserProjectRepo != nil {
		if err := s.telegramUserProjectRepo.SetUserProject(context.Background(), fmt.Sprintf("%d", userID), defaultProject.ID); err != nil {
			log.Printf("[telegram] failed to persist default project for user %d: %v", userID, err)
		}
	}

	return fmt.Sprintf("Welcome to *OpenVibely*! 🚀\n\nYour active project is: *%s*\n\nJust send me any message and I'll help you manage tasks, answer questions about your project, or anything else — the same way the /chat page works in the web UI.\n\nExamples:\n- \"Create a task to fix the login bug\"\n- \"List my backlog tasks\"\n- \"What tasks are currently running?\"\n\nUse /project to view or change your active project.",
		defaultProject.Name)
}

// handleProject shows the current project or switches to a new one
func (s *TelegramService) handleProject(userID int64, args string) string {
	ctx := context.Background()
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return fmt.Sprintf("❌ Error loading projects: %v", err)
	}
	if len(projects) == 0 {
		return "No projects found. Please create a project first using the web interface."
	}

	// If no args, show current project and list available projects
	if strings.TrimSpace(args) == "" {
		currentProjectID := s.getActiveProject(userID)
		var currentProjectName string
		for _, p := range projects {
			if p.ID == currentProjectID {
				currentProjectName = p.Name
				break
			}
		}

		var projectList strings.Builder
		projectList.WriteString(fmt.Sprintf("📂 *Current project:* %s\n\n", currentProjectName))
		projectList.WriteString("*Available projects:*\n")
		for _, p := range projects {
			marker := ""
			if p.ID == currentProjectID {
				marker = " ← _current_"
			}
			projectList.WriteString(fmt.Sprintf("• %s%s\n", p.Name, marker))
		}
		projectList.WriteString("\nUse `/project <name>` to switch projects.")
		return projectList.String()
	}

	// Switch to the specified project (by name or ID)
	targetName := strings.TrimSpace(args)
	var targetProject *models.Project
	for i := range projects {
		if strings.EqualFold(projects[i].Name, targetName) || projects[i].ID == targetName {
			targetProject = &projects[i]
			break
		}
	}

	if targetProject == nil {
		var availableNames []string
		for _, p := range projects {
			availableNames = append(availableNames, p.Name)
		}
		return fmt.Sprintf("❌ Project not found: %q\n\nAvailable projects: %s",
			targetName, strings.Join(availableNames, ", "))
	}

	// Update user's active project (in-memory + persistent)
	s.userProjects[userID] = targetProject.ID
	if s.telegramUserProjectRepo != nil {
		if err := s.telegramUserProjectRepo.SetUserProject(ctx, fmt.Sprintf("%d", userID), targetProject.ID); err != nil {
			log.Printf("[telegram] failed to persist project selection for user %d: %v", userID, err)
		}
	}
	return fmt.Sprintf("✅ Switched to project: *%s*", targetProject.Name)
}

// handleChatMessage forwards a Telegram message to the chat orchestrator
func (s *TelegramService) handleChatMessage(message *tgbotapi.Message) {
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
		s.sendMessage(chatID, "Please send a text message or an attachment.")
		return
	}

	// If attachment with no caption, generate a default prompt
	if text == "" && fileID != "" {
		text = fmt.Sprintf("User sent an attachment: %s", fileName)
	}

	log.Printf("[telegram] chat message from user=%d text=%q hasAttachment=%v", userID, text, fileID != "")

	projectID := s.getActiveProject(userID)
	if projectID == "" {
		s.sendMessage(chatID, "No active project. Send /start to set up first.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), telegramProcessTimeout)
	defer cancel()

	// Download and save attachment if present
	var attachmentContext string
	var imageAttachments []models.Attachment
	var chatAttachments []models.ChatAttachment
	hasImages := false

	if fileID != "" {
		attCtx, imgAtts, chatAtts, err := s.downloadAndSaveTelegramAttachment(ctx, fileID, fileName, fileSize, mimeType)
		if err != nil {
			log.Printf("[telegram] attachment download error: %v", err)
			s.sendMessage(chatID, fmt.Sprintf("⚠️ Failed to process attachment: %v", err))
			// Continue without the attachment
		} else {
			attachmentContext = attCtx
			imageAttachments = imgAtts
			chatAttachments = chatAtts
			hasImages = len(imageAttachments) > 0
		}
	}

	// Auto-select agent (vision-aware if images present)
	agent, err := s.autoSelectAgent(ctx, text, hasImages)
	if err != nil {
		log.Printf("[telegram] agent selection error: %v", err)
		s.sendMessage(chatID, fmt.Sprintf("Error selecting model: %v", err))
		return
	}

	// Note: Interactive chat intentionally bypasses task worker capacity checks.
	// Task worker limits (per-project/per-model) only gate task execution, not chat.
	// This ensures the chat orchestrator remains responsive even when all task workers are busy.

	// Create chat task (same as ChatSend handler)
	selectedAgentID := agent.ID
	chatTitle := fmt.Sprintf("Telegram %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(text, 47))
	task := &models.Task{
		ProjectID:      projectID,
		Title:          chatTitle,
		Prompt:         text,
		Status:         models.StatusPending,
		Category:       models.CategoryChat,
		AgentID:        &selectedAgentID,
		CreatedVia:     models.TaskOriginTelegram,
		TelegramChatID: chatID,
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		log.Printf("[telegram] error creating task: %v", err)
		s.sendMessage(chatID, "Error processing your message. Please try again.")
		return
	}

	// Create execution record
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    text,
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		log.Printf("[telegram] error creating execution: %v", err)
		s.sendMessage(chatID, "Error processing your message. Please try again.")
		return
	}

	log.Printf("[telegram] created exec=%s task=%s for user=%d", exec.ID, task.ID, userID)

	// Broadcast new message event so /chat page updates in real-time
	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Message:   text,
			Source:    "telegram",
			AgentName: agent.Name,
		})
	}

	// Link chat attachments to this execution and move files
	if len(chatAttachments) > 0 {
		s.linkAttachmentsToExecution(ctx, exec.ID, chatAttachments)
		// Update imageAttachments file paths to match the moved file locations.
		// linkAttachmentsToExecution moves files from the temp directory to
		// uploads/chat/{execID}/ and updates chatAttachments paths, but
		// imageAttachments still has the old temp dir paths (now deleted).
		// Without this, callAnthropicChat tries to read from a nonexistent
		// temp path and silently skips the image.
		for i := range imageAttachments {
			for _, ca := range chatAttachments {
				if ca.FileName == imageAttachments[i].FileName {
					imageAttachments[i].FilePath = ca.FilePath
					break
				}
			}
		}
	}

	// Send initial "thinking" message that we'll edit later
	thinkingMsg := tgbotapi.NewMessage(chatID, "⏳ Thinking...")
	sentMsg, err := s.bot.Send(thinkingMsg)
	if err != nil {
		log.Printf("[telegram] error sending thinking message: %v", err)
	}

	// Load chat history
	chatHistory, err := s.execRepo.ListChatHistory(ctx, projectID, telegramChatHistoryLimit)
	if err != nil {
		log.Printf("[telegram] error loading chat history: %v", err)
		chatHistory = []models.Execution{}
	}
	priorHistory := filterTelegramChatHistory(chatHistory, exec.ID)

	// Build context (task list + model info + attachment context + personality)
	systemContext := s.buildChatContext(ctx, projectID)
	if attachmentContext != "" {
		systemContext = systemContext + attachmentContext
	}
	if personalityPrompt := s.getPersonalityContext(ctx, projectID); personalityPrompt != "" {
		systemContext = systemContext + personalityPrompt
	}

	// Resolve work directory
	workDir := s.resolveWorkDir(ctx, projectID)

	callCtx := llmcontracts.WithChatMode(ctx, models.ChatModeOrchestrate)
	if supportsRuntimeChatActionTools(*agent) {
		callCtx = llmcontracts.WithRuntimeTools(callCtx, s.buildTelegramActionToolRuntime(projectID, chatID, userID, nil))
	}

	// Register cancellation
	if s.workerSvc != nil {
		s.workerSvc.RegisterCancel(task.ID, cancel)
		defer s.workerSvc.DeregisterCancel(task.ID)
	}

	// Start streaming updates to Telegram in background
	streamDone := make(chan struct{})
	if sentMsg.MessageID != 0 {
		go s.streamUpdatesToTelegram(ctx, chatID, sentMsg.MessageID, exec.ID, streamDone)
	}

	// Call LLM (blocks until complete)
	start := time.Now()
	output, tokensUsed, llmErr := s.llmSvc.CallAgentDirectStreaming(
		callCtx, text, imageAttachments, *agent, exec.ID, priorHistory, systemContext, workDir, false,
	)
	durationMs := time.Since(start).Milliseconds()

	if llmErr != nil {
		log.Printf("[telegram] LLM call failed exec=%s: %v", exec.ID, llmErr)
		s.completeExecution(ctx, exec.ID, task.ID, "", llmErr.Error(), 0, durationMs)
		close(streamDone)
		if sentMsg.MessageID != 0 {
			s.editMessage(chatID, sentMsg.MessageID, fmt.Sprintf("❌ Error: %s", util.Truncate(llmErr.Error(), 197)))
		} else {
			s.sendMessage(chatID, fmt.Sprintf("❌ Error: %s", util.Truncate(llmErr.Error(), 197)))
		}
		return
	}

	// Complete execution
	s.completeExecution(ctx, exec.ID, task.ID, output, "", tokensUsed, durationMs)

	// Broadcast response done event
	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatResponseDone,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Source:    "telegram",
			AgentName: agent.Name,
		})
	}

	log.Printf("[telegram] completed exec=%s tokens=%d duration=%dms output_len=%d", exec.ID, tokensUsed, durationMs, len(output))

	// Stop streaming goroutine
	close(streamDone)

	// Clean markers from output for display (preserve summaries/results)
	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		cleaned = "(No response)"
	}

	// Send final response (or edit the thinking message)
	if sentMsg.MessageID != 0 {
		s.editMessage(chatID, sentMsg.MessageID, cleaned)
	} else {
		s.sendMessage(chatID, cleaned)
	}
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

	log.Printf("[telegram] downloaded attachment file=%s size=%d mime=%s path=%s", fileName, written, mimeType, destPath)

	// Make the path absolute
	absPath, err := filepath.Abs(destPath)
	if err != nil {
		absPath = destPath
	}

	// Build attachment data
	chatAtt := models.ChatAttachment{
		FileName:  fileName,
		FilePath:  absPath,
		MediaType: mimeType,
		FileSize:  written,
	}
	chatAttachments := []models.ChatAttachment{chatAtt}

	var imageAttachments []models.Attachment
	var attachmentContext string

	if isTelegramImageFile(mimeType) {
		// Image files: pass as multimodal attachments
		imageAttachments = append(imageAttachments, models.Attachment{
			FileName:  fileName,
			FilePath:  absPath,
			MediaType: mimeType,
			FileSize:  written,
		})
		log.Printf("[telegram] image attachment file=%s size=%d", fileName, written)
	} else if written <= telegramMaxTextFileSize {
		// Small text/document files: read content and include in context
		content, readErr := os.ReadFile(absPath)
		if readErr == nil {
			attachmentContext = fmt.Sprintf("\n\n--- Attached Files ---\n\nFile: %s\n```\n%s\n```\n", fileName, string(content))
			log.Printf("[telegram] text attachment file=%s size=%d", fileName, written)
		}
	} else {
		// Large files: mention but don't include content
		attachmentContext = fmt.Sprintf("\n\n--- Attached Files ---\n\nFile: %s (attached, %d bytes - too large to include inline)\n", fileName, written)
		log.Printf("[telegram] large file attachment file=%s size=%d (content not included)", fileName, written)
	}

	return attachmentContext, imageAttachments, chatAttachments, nil
}

// linkAttachmentsToExecution creates database records for chat attachments and moves files
// to the execution directory for proper storage.
func (s *TelegramService) linkAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment) {
	if s.chatAttachmentRepo == nil {
		return
	}

	execDir := filepath.Join(telegramUploadsDir, "chat", execID)
	if err := os.MkdirAll(execDir, 0755); err != nil {
		log.Printf("[telegram] error creating exec dir %s: %v", execDir, err)
		return
	}

	for i := range attachments {
		att := &attachments[i]

		// Move file from temp directory to execution directory
		destPath := filepath.Join(execDir, filepath.Base(att.FileName))
		if err := moveOrCopyFile(att.FilePath, destPath); err != nil {
			log.Printf("[telegram] error moving attachment file=%s: %v", att.FileName, err)
			continue
		}

		// Make the new path absolute
		absPath, err := filepath.Abs(destPath)
		if err != nil {
			absPath = destPath
		}

		// Clean up original temp directory
		tmpDir := filepath.Dir(att.FilePath)
		os.RemoveAll(tmpDir)

		// Update the attachment's file path
		att.FilePath = absPath
		att.ExecutionID = execID

		// Create database record
		if err := s.chatAttachmentRepo.Create(ctx, att); err != nil {
			log.Printf("[telegram] error creating chat attachment record: %v", err)
		} else {
			log.Printf("[telegram] linked attachment id=%s file=%s to exec=%s", att.ID, att.FileName, execID)
		}
	}
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

// streamUpdatesToTelegram polls the execution output and edits the Telegram message
// with incremental updates for a streaming feel
func (s *TelegramService) streamUpdatesToTelegram(ctx context.Context, chatID int64, messageID int, execID string, done <-chan struct{}) {
	ticker := time.NewTicker(telegramStreamInterval)
	defer ticker.Stop()

	lastContent := ""

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			exec, err := s.execRepo.GetByID(ctx, execID)
			if err != nil || exec == nil {
				continue
			}

			// Only update if we have new content
			currentOutput := exec.Output
			if currentOutput == "" || currentOutput == lastContent {
				continue
			}

			// Clean and truncate for Telegram display
			cleaned := llmoutput.CleanChatOutput(currentOutput)
			if cleaned == "" || cleaned == lastContent {
				continue
			}

			// Truncate if needed (Telegram limit)
			display := cleaned
			if len(display) > maxMessageLength-50 {
				display = display[:maxMessageLength-50] + "\n\n⏳ _Generating..._"
			} else {
				display += "\n\n⏳ _Generating..._"
			}

			s.editMessage(chatID, messageID, display)
			lastContent = cleaned

			// Stop if execution is complete
			if exec.Status == models.ExecCompleted || exec.Status == models.ExecFailed {
				return
			}
		}
	}
}

// autoSelectAgent picks an agent based on message complexity and whether images are present
func (s *TelegramService) autoSelectAgent(ctx context.Context, message string, hasImages bool) (*models.LLMConfig, error) {
	agents, err := s.llmConfigRepo.List(ctx)
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

	// Fallback to default or first agent
	for i := range agents {
		if agents[i].IsDefault {
			return &agents[i], nil
		}
	}
	return &agents[0], nil
}

// buildChatContext builds context string with task list, model info, and schedule data.
// Uses the shared BuildChatContext function so Telegram produces identical context to /chat.
func (s *TelegramService) buildChatContext(ctx context.Context, projectID string) string {
	existingTasks, err := s.taskSvc.ListByProject(ctx, projectID, "")
	if err != nil {
		log.Printf("[telegram] error listing tasks for context: %v", err)
		existingTasks = []models.Task{}
	}

	availableModels, _ := s.llmConfigRepo.List(ctx)

	var schedules []models.Schedule
	if s.scheduleRepo != nil {
		schedules, err = s.scheduleRepo.ListByProject(ctx, projectID)
		if err != nil {
			log.Printf("[telegram] error listing schedules for context: %v", err)
			schedules = []models.Schedule{}
		}
	}

	return BuildChatContext(existingTasks, availableModels, schedules, time.Now())
}

// resolveWorkDir gets the repo path for the project
func (s *TelegramService) resolveWorkDir(ctx context.Context, projectID string) string {
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err == nil && project != nil {
		return project.RepoPath
	}
	return ""
}

// getPersonalityContext loads the global personality setting and returns the
// corresponding system prompt modifier. Returns empty string if no personality is set.
func (s *TelegramService) getPersonalityContext(ctx context.Context, projectID string) string {
	if s.settingsRepo == nil {
		return ""
	}
	personality, err := s.settingsRepo.Get(ctx, "personality")
	if err != nil || personality == "" {
		return ""
	}
	prompt := GetPersonalityPromptWithCustom(ctx, personality, s.customPersonalityRepo)
	if prompt == "" {
		return ""
	}
	return "\n# Communication Style\n\n" + prompt
}

func (s *TelegramService) buildTelegramActionToolRuntime(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	handlers := s.telegramActionHandlers(projectID, chatID, userID, collector)
	return &llmcontracts.RuntimeTools{
		Definitions: actionToolDefinitions(chatcontrol.SurfaceTelegram, true),
		Executor:    chatcontrol.BuildRuntimeToolExecutor(models.ChatModeOrchestrate, chatcontrol.SurfaceTelegram, handlers),
	}
}

func (s *TelegramService) telegramActionHandlers(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return map[string]chatcontrol.RuntimeActionHandler{
		"create_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req TaskCreationRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
				return "", fmt.Errorf("create_task requires title and prompt")
			}
			if req.Priority == 0 {
				req.Priority = 2
			}
			agents := []models.LLMConfig{}
			if s.llmConfigRepo != nil {
				agents, _ = s.llmConfigRepo.List(ctx)
			}
			createdTasks, summary := ExecuteTaskCreationsWithReturn(ctx, []TaskCreationRequest{req}, projectID, s.taskSvc, agents)
			for _, t := range createdTasks {
				if err := s.taskRepo.UpdateTelegramOrigin(ctx, t.ID, chatID); err != nil {
					log.Printf("[telegram] runtime create_task error setting telegram origin for task %s: %v", t.ID, err)
				}
			}
			if collector != nil {
				collector.addCreated(summary)
			}
			return strings.TrimSpace(summary), nil
		},
		"edit_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req TaskEditRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.ID) == "" {
				return "", fmt.Errorf("edit_task requires id")
			}
			summary := ExecuteTaskEdits(ctx, []TaskEditRequest{req}, projectID, s.taskSvc, nil, "")
			if collector != nil {
				collector.addEdited(summary)
			}
			return strings.TrimSpace(summary), nil
		},
		"execute_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req TaskExecutionRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" && len(req.Tags) == 0 && req.MinPriority == 0 {
				return "", fmt.Errorf("execute_tasks requires task_id/title or tags/min_priority")
			}
			summary := ExecuteTaskExecutions(ctx, []TaskExecutionRequest{req}, projectID, s.taskSvc)
			return strings.TrimSpace(summary), nil
		},
		"view_task_thread": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req ViewThreadRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return s.executeViewTaskThread(ctx, projectID, req)
		},
		"send_to_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req SendToTaskRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return s.executeSendToTask(ctx, projectID, req)
		},
		"schedule_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeScheduleTaskChannel(ctx, projectID, input), nil
		},
		"delete_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeDeleteScheduleChannel(ctx, projectID, input), nil
		},
		"modify_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeModifyScheduleChannel(ctx, projectID, input), nil
		},
		"list_personalities": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeListPersonalitiesChannel(ctx), nil
		},
		"set_personality": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeSetPersonalityChannel(ctx, input), nil
		},
		"list_models": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeListModelsChannel(ctx), nil
		},
		"list_agents": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeListAgentsChannel(ctx), nil
		},
		"view_settings": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeViewSettingsChannel(ctx), nil
		},
		"project_info": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeProjectInfoChannel(ctx, projectID), nil
		},
		"create_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeCreateAlertChannel(ctx, projectID, input), nil
		},
		"delete_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeDeleteAlertChannel(ctx, input), nil
		},
		"toggle_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeToggleAlertChannel(ctx, input), nil
		},
		"list_projects": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.buildTelegramProjectListResult(ctx, projectID), nil
		},
		"switch_project": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req SwitchProjectRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return s.switchTelegramProjectResult(ctx, userID, req.Project), nil
		},
		"get_personality": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeGetPersonalityChannel(ctx), nil
		},
		"get_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeGetModelChannel(ctx, input), nil
		},
		"get_current_project": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeGetCurrentProjectChannel(ctx, projectID), nil
		},
		"list_alerts": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.executeListAlertsChannel(ctx, projectID), nil
		},
		"get_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.executeGetAlertChannel(ctx, input), nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Current chat mode: orchestrate", nil
		},
		"set_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Chat mode changes are not supported on Telegram. Telegram always uses orchestrate mode.", nil
		},
		"list_capabilities": func(_ context.Context, _ json.RawMessage) (string, error) {
			summaries := chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceTelegram)
			return formatChannelCapabilities(summaries), nil
		},
	}
}

func (s *TelegramService) executeViewTaskThread(ctx context.Context, projectID string, req ViewThreadRequest) (string, error) {
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "", fmt.Errorf("view_task_thread requires task_id or title")
	}
	task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}
	executions, err := s.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return "", fmt.Errorf("retrieving thread for %q: %w", task.Title, err)
	}
	return strings.TrimSpace(formatThreadTranscript(task, executions, req.Offset, req.Limit)), nil
}

func (s *TelegramService) executeSendToTask(ctx context.Context, projectID string, req SendToTaskRequest) (string, error) {
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "", fmt.Errorf("send_to_task requires task_id or title")
	}
	if strings.TrimSpace(req.Message) == "" {
		return "", fmt.Errorf("send_to_task requires message")
	}

	task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}

	if task.Status == models.StatusRunning {
		return fmt.Sprintf("Task %q is currently running. Wait for it to finish before sending a message.", task.Title), nil
	}

	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed || task.Status == models.StatusCancelled {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
			return "", fmt.Errorf("reactivating task %q: %w", task.Title, err)
		}
		if task.Category == models.CategoryCompleted {
			if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				log.Printf("[telegram] runtime send_to_task error updating category for task %s: %v", task.ID, err)
			}
		}
	} else if task.Status == models.StatusPending {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
			log.Printf("[telegram] runtime send_to_task error setting task running task=%s: %v", task.ID, err)
		}
	}

	var agent *models.LLMConfig
	if task.AgentID != nil {
		agent, _ = s.llmConfigRepo.GetByID(ctx, *task.AgentID)
	}
	if agent == nil {
		agent, err = s.selectDefaultAgent(ctx)
		if err != nil {
			return "", fmt.Errorf("selecting agent for task %q: %w", task.Title, err)
		}
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    req.Message,
		IsFollowup:    true,
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		return "", fmt.Errorf("creating follow-up execution for %q: %w", task.Title, err)
	}

	priorExecs, _ := s.execRepo.ListByTaskChronological(ctx, task.ID)
	priorHistory := filterTelegramChatHistory(priorExecs, exec.ID)
	systemContext := buildTelegramTaskChatContext(task.Title, len(priorHistory) > 0)
	if pCtx := s.getPersonalityContext(ctx, task.ProjectID); pCtx != "" {
		systemContext += pCtx
	}
	workDir := s.resolveWorkDir(ctx, task.ProjectID)

	go s.processTaskMessage(exec.ID, task.ID, req.Message, *agent, priorHistory, task.ProjectID, systemContext, workDir)

	return fmt.Sprintf("Sent message to task %q [TASK_ID:%s] and started processing.", task.Title, task.ID), nil
}

func (s *TelegramService) buildTelegramProjectListResult(ctx context.Context, projectID string) string {
	if s.projectRepo == nil {
		return "Error retrieving projects: project repository not configured"
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return "Error retrieving projects: " + err.Error()
	}
	var sb strings.Builder
	sb.WriteString("Available Projects:\n")
	if len(projects) == 0 {
		sb.WriteString("No projects found.")
		return sb.String()
	}
	for _, p := range projects {
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

func (s *TelegramService) switchTelegramProjectResult(ctx context.Context, userID int64, targetProject string) string {
	targetProject = strings.TrimSpace(targetProject)
	if targetProject == "" {
		return "Project switch requires a project name or ID."
	}
	if s.projectRepo == nil {
		return "Error loading projects: project repository not configured"
	}

	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return "Error loading projects: " + err.Error()
	}

	var target *models.Project
	for i := range projects {
		if strings.EqualFold(projects[i].Name, targetProject) || projects[i].ID == targetProject {
			target = &projects[i]
			break
		}
	}
	if target == nil {
		var names []string
		for _, p := range projects {
			names = append(names, p.Name)
		}
		return fmt.Sprintf("Project not found: %q. Available projects: %s", targetProject, strings.Join(names, ", "))
	}

	s.userProjects[userID] = target.ID
	if s.telegramUserProjectRepo != nil {
		if err := s.telegramUserProjectRepo.SetUserProject(ctx, fmt.Sprintf("%d", userID), target.ID); err != nil {
			log.Printf("[telegram] runtime switch_project error persisting selection: %v", err)
		}
	}
	return fmt.Sprintf("Switched to project: %s", target.Name)
}

// ---- New channel action executors ----

func (s *TelegramService) executeGetPersonalityChannel(ctx context.Context) string {
	if s.settingsRepo == nil {
		return "Current personality: default (no personality set)"
	}
	current, err := s.settingsRepo.Get(ctx, "personality")
	if err != nil {
		log.Printf("[telegram] executeGetPersonalityChannel error: %v", err)
		return "Error retrieving personality setting."
	}
	if current == "" {
		return "Current personality: default (base, no personality modifier active)"
	}
	return fmt.Sprintf("Current personality: %s", current)
}

func (s *TelegramService) executeGetModelChannel(ctx context.Context, input json.RawMessage) string {
	var req struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_model."
	}
	configs, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		return "Error retrieving model configurations."
	}
	for _, c := range configs {
		if (req.ModelID != "" && c.ID == req.ModelID) ||
			(req.Name != "" && strings.EqualFold(c.Name, req.Name)) {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			return fmt.Sprintf("Model: %s%s\n  Provider: %s\n  Model ID: %s\n  Auth: %s",
				c.Name, defaultStr, c.Provider, c.Model, c.AuthMethod)
		}
	}
	if req.ModelID != "" {
		return fmt.Sprintf("Model with id %q not found.", req.ModelID)
	}
	return fmt.Sprintf("Model with name %q not found.", req.Name)
}

func (s *TelegramService) executeGetCurrentProjectChannel(ctx context.Context, projectID string) string {
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	return fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
}

func (s *TelegramService) executeListAlertsChannel(ctx context.Context, projectID string) string {
	if s.alertSvc == nil {
		return "Alert service not available."
	}
	alerts, err := s.alertSvc.ListByProject(ctx, projectID, 50)
	if err != nil {
		return "Error retrieving alerts: " + err.Error()
	}
	if len(alerts) == 0 {
		return "No alerts found. You're all clear!"
	}
	unreadCount, _ := s.alertSvc.CountUnread(ctx, projectID)
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

func (s *TelegramService) executeGetAlertChannel(ctx context.Context, input json.RawMessage) string {
	var req struct {
		AlertID string `json:"alert_id"`
	}
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for get_alert."
	}
	if req.AlertID == "" {
		return "get_alert requires alert_id."
	}
	if s.alertSvc == nil {
		return "Alert service not available."
	}
	alert, err := s.alertSvc.GetByID(ctx, req.AlertID)
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
	return fmt.Sprintf("Alert: %s\n  ID: %s\n  Type: %s\n  Severity: %s\n  Status: %s\n  Message: %s",
		alert.Title, alert.ID, alert.Type, alert.Severity, readStr, alert.Message)
}

func (s *TelegramService) executeScheduleTaskChannel(ctx context.Context, projectID string, input json.RawMessage) string {
	var req ScheduleTaskRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for schedule_task."
	}
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "schedule_task requires task_id or title."
	}
	if strings.TrimSpace(req.Time) == "" {
		return "schedule_task requires time."
	}
	marker, err := buildToolMarker("SCHEDULE_TASK", input, true)
	if err != nil {
		return "Invalid input for schedule_task."
	}
	updated := s.processScheduleTask(ctx, "runtime-tool", projectID, marker)
	return toolSummaryFromMarker(marker, updated)
}

func (s *TelegramService) executeDeleteScheduleChannel(ctx context.Context, projectID string, input json.RawMessage) string {
	var req DeleteScheduleRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for delete_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "delete_schedule requires schedule_id, task_id, or title."
	}
	marker, err := buildToolMarker("DELETE_SCHEDULE", input, true)
	if err != nil {
		return "Invalid input for delete_schedule."
	}
	updated := s.processDeleteSchedule(ctx, "runtime-tool", projectID, marker)
	return toolSummaryFromMarker(marker, updated)
}

func (s *TelegramService) executeModifyScheduleChannel(ctx context.Context, projectID string, input json.RawMessage) string {
	var req ModifyScheduleRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for modify_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "modify_schedule requires schedule_id, task_id, or title."
	}
	marker, err := buildToolMarker("MODIFY_SCHEDULE", input, true)
	if err != nil {
		return "Invalid input for modify_schedule."
	}
	updated := s.processModifySchedule(ctx, "runtime-tool", projectID, marker)
	return toolSummaryFromMarker(marker, updated)
}

func (s *TelegramService) executeListPersonalitiesChannel(ctx context.Context) string {
	personalities := AllPersonalitiesWithCustom(ctx, s.customPersonalityRepo)
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
	if s.settingsRepo != nil {
		current, err := s.settingsRepo.Get(ctx, "personality")
		if err == nil {
			if current == "" {
				current = "default"
			}
			sb.WriteString(fmt.Sprintf("\nCurrent personality: %s", current))
		}
	}
	return strings.TrimSpace(sb.String())
}

func (s *TelegramService) executeSetPersonalityChannel(ctx context.Context, input json.RawMessage) string {
	var req struct {
		Personality string `json:"personality"`
	}
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for set_personality."
	}
	key := strings.TrimSpace(req.Personality)
	if key == "" {
		return "set_personality requires personality."
	}
	valid := false
	for _, p := range AllPersonalitiesWithCustom(ctx, s.customPersonalityRepo) {
		if p.Key == key {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Sprintf("Unknown personality %q. Use list_personalities to view options.", key)
	}
	if s.settingsRepo == nil {
		return "Error setting personality: settings repository not configured."
	}
	if err := s.settingsRepo.Set(ctx, "personality", key); err != nil {
		return fmt.Sprintf("Error setting personality to %q: %v", key, err)
	}
	return fmt.Sprintf("Personality changed to %q.", key)
}

func (s *TelegramService) executeListModelsChannel(ctx context.Context) string {
	configs, err := s.llmConfigRepo.List(ctx)
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
			auth = "cli"
		}
		sb.WriteString(fmt.Sprintf("- %s%s — provider: %s, model: %s, auth: %s\n", c.Name, defaultStr, c.Provider, c.Model, auth))
	}
	return strings.TrimSpace(sb.String())
}

func (s *TelegramService) executeListAgentsChannel(ctx context.Context) string {
	if s.agentRepo == nil {
		return "Agent definitions not available."
	}
	agents, err := s.agentRepo.List(ctx)
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
		sb.WriteString(fmt.Sprintf("- %s — %s%s, %d skills, %d MCP servers\n", a.Name, a.Description, modelStr, len(a.Skills), len(a.MCPServers)))
	}
	return strings.TrimSpace(sb.String())
}

func (s *TelegramService) executeViewSettingsChannel(ctx context.Context) string {
	var sb strings.Builder
	sb.WriteString("App Settings:\n")
	if s.settingsRepo != nil {
		personality, err := s.settingsRepo.Get(ctx, "personality")
		if err == nil {
			if personality == "" {
				personality = "default"
			}
			sb.WriteString(fmt.Sprintf("- Personality: %s\n", personality))
		}
	}
	configs, err := s.llmConfigRepo.List(ctx)
	if err == nil {
		sb.WriteString(fmt.Sprintf("- Configured models: %d\n", len(configs)))
	}
	if s.projectRepo != nil {
		projects, err := s.projectRepo.List(ctx)
		if err == nil {
			sb.WriteString(fmt.Sprintf("- Projects: %d\n", len(projects)))
		}
	}
	return strings.TrimSpace(sb.String())
}

func (s *TelegramService) executeProjectInfoChannel(ctx context.Context, projectID string) string {
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return "Error retrieving project details."
	}
	counts, err := s.taskRepo.CountByProjectAndCategory(ctx, projectID)
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

func (s *TelegramService) executeCreateAlertChannel(ctx context.Context, projectID string, input json.RawMessage) string {
	var req CreateAlertRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for create_alert."
	}
	if strings.TrimSpace(req.Title) == "" {
		return "create_alert requires title."
	}
	if s.alertSvc == nil {
		return "Alert service not available."
	}
	severity := models.SeverityInfo
	switch strings.ToLower(strings.TrimSpace(req.Severity)) {
	case "warning":
		severity = models.SeverityWarning
	case "error":
		severity = models.SeverityError
	case "", "info":
		severity = models.SeverityInfo
	default:
		return fmt.Sprintf("Invalid severity %q.", req.Severity)
	}
	alertType := models.AlertCustom
	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case "task_failed":
		alertType = models.AlertTaskFailed
	case "task_needs_followup":
		alertType = models.AlertTaskNeedsFollowup
	case "", "custom":
		alertType = models.AlertCustom
	default:
		return fmt.Sprintf("Invalid alert type %q.", req.Type)
	}
	a := &models.Alert{ProjectID: projectID, Type: alertType, Severity: severity, Title: req.Title, Message: req.Message}
	if strings.TrimSpace(req.TaskID) != "" {
		tid := strings.TrimSpace(req.TaskID)
		a.TaskID = &tid
	}
	if err := s.alertSvc.Create(ctx, a); err != nil {
		return fmt.Sprintf("Error creating alert %q: %v", req.Title, err)
	}
	return fmt.Sprintf("Created alert %q (id: %s, severity: %s)", req.Title, a.ID, severity)
}

func (s *TelegramService) executeDeleteAlertChannel(ctx context.Context, input json.RawMessage) string {
	var req DeleteAlertRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for delete_alert."
	}
	if strings.TrimSpace(req.AlertID) == "" {
		return "delete_alert requires alert_id."
	}
	if s.alertSvc == nil {
		return "Alert service not available."
	}
	if err := s.alertSvc.Delete(ctx, req.AlertID); err != nil {
		return fmt.Sprintf("Error deleting alert %q: %v", req.AlertID, err)
	}
	return fmt.Sprintf("Deleted alert %s.", req.AlertID)
}

func (s *TelegramService) executeToggleAlertChannel(ctx context.Context, input json.RawMessage) string {
	var req ToggleAlertRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for toggle_alert."
	}
	if strings.TrimSpace(req.AlertID) == "" {
		return "toggle_alert requires alert_id."
	}
	if s.alertSvc == nil {
		return "Alert service not available."
	}
	if err := s.alertSvc.MarkRead(ctx, req.AlertID); err != nil {
		return fmt.Sprintf("Error marking alert %q as read: %v", req.AlertID, err)
	}
	return fmt.Sprintf("Marked alert %s as read.", req.AlertID)
}

// formatChannelCapabilities formats capability summaries for channel surfaces.
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

// processChatMarkers processes all marker types from AI responses:
// [CREATE_TASK], [EDIT_TASK], [EXECUTE_TASKS], [VIEW_TASK_CHAT], [SEND_TO_TASK],
// [SCHEDULE_TASK], [LIST_PERSONALITIES], [SET_PERSONALITY], [LIST_MODELS],
// [VIEW_SETTINGS], [PROJECT_INFO], [LIST_ALERTS], [CREATE_ALERT], [DELETE_ALERT],
// [TOGGLE_ALERT], [LIST_PROJECTS], [SWITCH_PROJECT]
func (s *TelegramService) processChatMarkers(ctx context.Context, execID, projectID, output string, chatID int64, userID int64) string {
	if output == "" {
		return output
	}

	originalOutput := output

	// Process task creation markers
	taskRequests := ParseTaskCreations(output)
	if len(taskRequests) > 0 {
		log.Printf("[telegram] processChatMarkers exec=%s found %d task creation requests", execID, len(taskRequests))
		agents, _ := s.llmConfigRepo.List(ctx)
		createdTasks, summary := ExecuteTaskCreationsWithReturn(ctx, taskRequests, projectID, s.taskSvc, agents)
		if summary != "" {
			output += summary
		}
		// Mark all created tasks as originating from Telegram
		for _, t := range createdTasks {
			if err := s.taskRepo.UpdateTelegramOrigin(ctx, t.ID, chatID); err != nil {
				log.Printf("[telegram] processChatMarkers error setting telegram origin for task %s: %v", t.ID, err)
			}
		}
	}

	// Process task edit markers
	editRequests := ParseTaskEdits(output)
	if len(editRequests) > 0 {
		log.Printf("[telegram] processChatMarkers exec=%s found %d task edit requests", execID, len(editRequests))
		editSummary := ExecuteTaskEdits(ctx, editRequests, projectID, s.taskSvc, nil, "")
		if editSummary != "" {
			output += editSummary
		}
	}

	// Process task execution markers
	execRequests := ParseTaskExecutions(output)
	if len(execRequests) > 0 {
		log.Printf("[telegram] processChatMarkers exec=%s found %d task execution requests", execID, len(execRequests))
		execSummary := ExecuteTaskExecutions(ctx, execRequests, projectID, s.taskSvc)
		if execSummary != "" {
			output += execSummary
		}
	}

	// Process thread view markers
	if newOutput := s.processViewThread(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process send-to-task markers
	if newOutput := s.processSendToTask(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process schedule task markers
	if newOutput := s.processScheduleTask(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process delete schedule markers
	if newOutput := s.processDeleteSchedule(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process modify schedule markers
	if newOutput := s.processModifySchedule(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list personalities markers
	if newOutput := s.processListPersonalities(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process set personality markers
	if newOutput := s.processSetPersonality(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list models markers
	if newOutput := s.processListModels(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list agents markers
	if newOutput := s.processListAgents(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process view settings markers
	if newOutput := s.processViewSettings(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process project info markers
	if newOutput := s.processProjectInfo(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process alert markers
	if newOutput := s.processListAlerts(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := s.processCreateAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := s.processDeleteAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := s.processToggleAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list projects markers
	if newOutput := s.processListProjects(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process switch project markers
	if newOutput := s.processSwitchProject(ctx, execID, projectID, output, userID); newOutput != output {
		output = newOutput
	}

	// Detect missing markers: warn when the LLM appears to promise an action
	// but didn't actually output the corresponding marker block
	if warnings := DetectMissingMarkers(originalOutput); len(warnings) > 0 {
		for _, w := range warnings {
			log.Printf("[telegram] processChatMarkers exec=%s WARNING: LLM promised %s action (matched %q) but no %s marker found in output",
				execID, w.Action, w.MatchedHint, w.MarkerName)
		}
		output += "\n\n---\n**Warning:** The assistant appeared to promise an action but did not include the required marker. The following actions were NOT performed:\n"
		for _, w := range warnings {
			output += fmt.Sprintf("- %s (no %s marker found)\n", w.Action, w.MarkerName)
		}
		output += "\nPlease retry your request."
	}

	// Update execution output if modified
	if output != originalOutput {
		if updateErr := s.execRepo.UpdateOutput(ctx, execID, output); updateErr != nil {
			log.Printf("[telegram] processChatMarkers error updating output: %v", updateErr)
		}
	}

	return output
}

// processViewThread handles [VIEW_TASK_CHAT] markers from AI responses.
// Resolves the target task, fetches execution history, and appends a formatted transcript.
func (s *TelegramService) processViewThread(ctx context.Context, execID, projectID, output string) string {
	viewRequests := ParseViewThread(output)
	if len(viewRequests) == 0 {
		return output
	}

	log.Printf("[telegram] processViewThread exec=%s found %d view requests", execID, len(viewRequests))

	for _, req := range viewRequests {
		task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[telegram] processViewThread error resolving task: %v", err)
			output += fmt.Sprintf("\n\n---\nCould not find task: %v", err)
			continue
		}

		executions, err := s.execRepo.ListByTaskChronological(ctx, task.ID)
		if err != nil {
			log.Printf("[telegram] processViewThread error listing executions for task %s: %v", task.ID, err)
			output += fmt.Sprintf("\n\n---\nError retrieving thread for task \"%s\": %v", task.Title, err)
			continue
		}

		transcript := formatThreadTranscript(task, executions, req.Offset, req.Limit)
		output += transcript
	}

	return output
}

// processSendToTask handles [SEND_TO_TASK] markers from AI responses.
// Resolves the target task, validates state, creates a follow-up execution,
// and spawns a background goroutine to process the message.
func (s *TelegramService) processSendToTask(ctx context.Context, execID, projectID, output string) string {
	sendRequests := ParseSendToTask(output)
	if len(sendRequests) == 0 {
		return output
	}

	log.Printf("[telegram] processSendToTask exec=%s found %d send requests", execID, len(sendRequests))

	var results []string
	for _, req := range sendRequests {
		task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[telegram] processSendToTask error resolving task: %v", err)
			results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			continue
		}

		// Don't send to running tasks — wait for them to finish
		if task.Status == models.StatusRunning {
			log.Printf("[telegram] processSendToTask task %s is running, cannot send", task.ID)
			results = append(results, fmt.Sprintf("- Task \"%s\" is currently running. Wait for it to finish before sending a message.", task.Title))
			continue
		}

		// Auto-reactivate completed/failed/cancelled tasks
		if task.Status == models.StatusCompleted || task.Status == models.StatusFailed || task.Status == models.StatusCancelled {
			log.Printf("[telegram] processSendToTask reactivating task=%s from status=%s", task.ID, task.Status)
			if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
				log.Printf("[telegram] processSendToTask error reactivating task: %v", err)
				results = append(results, fmt.Sprintf("- Error reactivating task \"%s\": %v", task.Title, err))
				continue
			}
			if task.Category == models.CategoryCompleted {
				if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
					log.Printf("[telegram] processSendToTask error updating category: %v", err)
				}
			}
		} else if task.Status == models.StatusPending {
			if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
				log.Printf("[telegram] processSendToTask error setting task running: %v", err)
			}
		}

		// Select agent: use task's assigned agent, or fall back to default
		var agent *models.LLMConfig
		if task.AgentID != nil {
			agent, _ = s.llmConfigRepo.GetByID(ctx, *task.AgentID)
		}
		if agent == nil {
			agent, err = s.selectDefaultAgent(ctx)
			if err != nil {
				log.Printf("[telegram] processSendToTask error selecting agent: %v", err)
				results = append(results, fmt.Sprintf("- Error selecting agent for task \"%s\": %v", task.Title, err))
				continue
			}
		}

		// Create follow-up execution
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    req.Message,
			IsFollowup:    true,
		}
		if err := s.execRepo.Create(ctx, exec); err != nil {
			log.Printf("[telegram] processSendToTask error creating execution: %v", err)
			results = append(results, fmt.Sprintf("- Error creating execution for task \"%s\": %v", task.Title, err))
			continue
		}

		// Load conversation history and build context
		priorExecs, _ := s.execRepo.ListByTaskChronological(ctx, task.ID)
		priorHistory := filterTelegramChatHistory(priorExecs, exec.ID)
		systemContext := buildTelegramTaskChatContext(task.Title, len(priorHistory) > 0)
		if pCtx := s.getPersonalityContext(ctx, task.ProjectID); pCtx != "" {
			systemContext += pCtx
		}
		workDir := s.resolveWorkDir(ctx, task.ProjectID)

		log.Printf("[telegram] processSendToTask sending message to task=%s exec=%s agent=%s", task.ID, exec.ID, agent.Name)

		// Spawn background goroutine to process the message
		go s.processTaskMessage(exec.ID, task.ID, req.Message, *agent, priorHistory, task.ProjectID, systemContext, workDir)

		results = append(results, fmt.Sprintf("- Sent message to task \"%s\" [TASK_ID:%s] — the agent is now processing your message.", task.Title, task.ID))
	}

	if len(results) > 0 {
		output += "\n\n---\nTask Chat Messages:\n" + strings.Join(results, "\n")
	}

	return output
}

// processScheduleTask handles [SCHEDULE_TASK] markers from AI responses.
// It resolves the target task, creates a schedule entry, and moves the task to 'scheduled' category.
func (s *TelegramService) processScheduleTask(ctx context.Context, execID, projectID, output string) string {
	scheduleRequests := ParseScheduleTask(output)
	if len(scheduleRequests) == 0 {
		return output
	}

	log.Printf("[telegram] processScheduleTask exec=%s found %d schedule requests", execID, len(scheduleRequests))

	var results []string
	for _, req := range scheduleRequests {
		task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[telegram] processScheduleTask error resolving task: %v", err)
			results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			continue
		}

		// Parse time (HH:MM format)
		var hourVal, minuteVal int
		if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
			log.Printf("[telegram] processScheduleTask invalid time: %s", req.Time)
			results = append(results, fmt.Sprintf("- Invalid time %q for task \"%s\" (expected HH:MM, 00:00-23:59)", req.Time, task.Title))
			continue
		}

		// Determine repeat type
		repeatType := models.RepeatDaily
		switch strings.ToLower(req.Repeat) {
		case "once":
			repeatType = models.RepeatOnce
		case "daily", "":
			repeatType = models.RepeatDaily
		case "weekly":
			repeatType = models.RepeatWeekly
		case "monthly":
			repeatType = models.RepeatMonthly
		case "hours", "hourly":
			repeatType = models.RepeatHours
		case "minutes":
			repeatType = models.RepeatMinutes
		case "seconds":
			repeatType = models.RepeatSeconds
		}

		// Determine repeat interval (default 1)
		repeatInterval := 1
		if req.Interval > 0 {
			repeatInterval = req.Interval
		}

		// Build RunAt: today at the specified time in local timezone
		now := time.Now().Local()
		runAt := time.Date(now.Year(), now.Month(), now.Day(), hourVal, minuteVal, 0, 0, time.Local)

		// For weekly schedules with specific days, adjust RunAt to the next matching day
		if repeatType == models.RepeatWeekly && len(req.Days) > 0 {
			dayMap := map[string]time.Weekday{
				"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
				"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
				"sat": time.Saturday,
			}
			bestOffset := 8
			for _, d := range req.Days {
				target, ok := dayMap[strings.ToLower(d)]
				if !ok {
					continue
				}
				offset := int(target - runAt.Weekday())
				if offset < 0 {
					offset += 7
				}
				if offset == 0 && runAt.Before(now) {
					offset = 7
				}
				if offset < bestOffset {
					bestOffset = offset
				}
			}
			if bestOffset < 8 {
				runAt = runAt.AddDate(0, 0, bestOffset)
			}
		}

		runAtUTC := runAt.UTC()

		schedule := &models.Schedule{
			TaskID:         task.ID,
			RunAt:          runAtUTC,
			RepeatType:     repeatType,
			RepeatInterval: repeatInterval,
			Enabled:        true,
		}

		if s.scheduleRepo == nil {
			log.Printf("[telegram] processScheduleTask scheduleRepo is nil, cannot create schedule")
			results = append(results, fmt.Sprintf("- Error scheduling task \"%s\": schedule repository not available", task.Title))
			continue
		}

		if err := s.scheduleRepo.Create(ctx, schedule); err != nil {
			log.Printf("[telegram] processScheduleTask error creating schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error scheduling task \"%s\": %v", task.Title, err))
			continue
		}

		// Move task to scheduled category
		if task.Category != models.CategoryScheduled {
			if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled); err != nil {
				log.Printf("[telegram] processScheduleTask error updating category: %v", err)
			}
			if task.Status != models.StatusPending {
				if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
					log.Printf("[telegram] processScheduleTask error updating status: %v", err)
				}
			}
		}

		repeatDesc := FormatRepeatPattern(repeatType, repeatInterval)
		if repeatType == models.RepeatWeekly && len(req.Days) > 0 {
			repeatDesc = fmt.Sprintf("weekly on %s", strings.Join(req.Days, ", "))
			if repeatInterval > 1 {
				repeatDesc = fmt.Sprintf("every %d weeks on %s", repeatInterval, strings.Join(req.Days, ", "))
			}
		}
		results = append(results, fmt.Sprintf("- Scheduled task \"%s\" [TASK_ID:%s] at %s (%s)", task.Title, task.ID, req.Time, repeatDesc))
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processDeleteSchedule handles [DELETE_SCHEDULE] markers from AI responses.
func (s *TelegramService) processDeleteSchedule(ctx context.Context, execID, projectID, output string) string {
	deleteRequests := ParseDeleteSchedule(output)
	if len(deleteRequests) == 0 {
		return output
	}

	log.Printf("[telegram] processDeleteSchedule exec=%s found %d delete requests", execID, len(deleteRequests))

	var results []string
	for _, req := range deleteRequests {
		schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[telegram] processDeleteSchedule error resolving schedule: %v", err)
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}

		if s.scheduleRepo == nil {
			log.Printf("[telegram] processDeleteSchedule scheduleRepo is nil")
			results = append(results, fmt.Sprintf("- Error deleting schedule for task \"%s\": schedule repository not available", task.Title))
			continue
		}

		if err := s.scheduleRepo.Delete(ctx, schedule.ID); err != nil {
			log.Printf("[telegram] processDeleteSchedule error deleting schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error deleting schedule for task \"%s\": %v", task.Title, err))
			continue
		}

		remaining, err := s.scheduleRepo.ListByTask(ctx, task.ID)
		if err != nil {
			log.Printf("[telegram] processDeleteSchedule error checking remaining schedules: %v", err)
		}
		if len(remaining) == 0 && task.Category == models.CategoryScheduled {
			if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
				log.Printf("[telegram] processDeleteSchedule error updating category: %v", err)
			}
		}

		results = append(results, fmt.Sprintf("- Deleted schedule for task \"%s\" [TASK_ID:%s]", task.Title, task.ID))
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Delete Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processModifySchedule handles [MODIFY_SCHEDULE] markers from AI responses.
func (s *TelegramService) processModifySchedule(ctx context.Context, execID, projectID, output string) string {
	modifyRequests := ParseModifySchedule(output)
	if len(modifyRequests) == 0 {
		return output
	}

	log.Printf("[telegram] processModifySchedule exec=%s found %d modify requests", execID, len(modifyRequests))

	var results []string
	for _, req := range modifyRequests {
		schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[telegram] processModifySchedule error resolving schedule: %v", err)
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}

		if s.scheduleRepo == nil {
			log.Printf("[telegram] processModifySchedule scheduleRepo is nil")
			results = append(results, fmt.Sprintf("- Error modifying schedule for task \"%s\": schedule repository not available", task.Title))
			continue
		}

		var changes []string

		if req.Time != "" {
			var hourVal, minuteVal int
			if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
				results = append(results, fmt.Sprintf("- Invalid time %q for schedule on task \"%s\"", req.Time, task.Title))
				continue
			}
			oldLocal := schedule.RunAt.Local()
			newRunAt := time.Date(oldLocal.Year(), oldLocal.Month(), oldLocal.Day(), hourVal, minuteVal, 0, 0, time.Local).UTC()
			schedule.RunAt = newRunAt
			changes = append(changes, fmt.Sprintf("time→%s", req.Time))
		}

		if req.Repeat != "" {
			switch strings.ToLower(req.Repeat) {
			case "once":
				schedule.RepeatType = models.RepeatOnce
			case "daily":
				schedule.RepeatType = models.RepeatDaily
			case "weekly":
				schedule.RepeatType = models.RepeatWeekly
			case "monthly":
				schedule.RepeatType = models.RepeatMonthly
			case "hours", "hourly":
				schedule.RepeatType = models.RepeatHours
			case "minutes":
				schedule.RepeatType = models.RepeatMinutes
			case "seconds":
				schedule.RepeatType = models.RepeatSeconds
			default:
				results = append(results, fmt.Sprintf("- Unknown repeat type %q for schedule on task \"%s\"", req.Repeat, task.Title))
				continue
			}
			changes = append(changes, fmt.Sprintf("repeat→%s", req.Repeat))
		}

		if req.Interval != nil {
			if *req.Interval < 1 {
				results = append(results, fmt.Sprintf("- Invalid interval %d for schedule on task \"%s\"", *req.Interval, task.Title))
				continue
			}
			schedule.RepeatInterval = *req.Interval
			changes = append(changes, fmt.Sprintf("interval→%d", *req.Interval))
		}

		if len(req.Days) > 0 && schedule.RepeatType == models.RepeatWeekly {
			dayMap := map[string]time.Weekday{
				"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
				"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
				"sat": time.Saturday,
			}
			now := time.Now().Local()
			runAtLocal := schedule.RunAt.Local()
			bestOffset := 8
			for _, d := range req.Days {
				target, ok := dayMap[strings.ToLower(d)]
				if !ok {
					continue
				}
				offset := int(target - runAtLocal.Weekday())
				if offset < 0 {
					offset += 7
				}
				if offset == 0 && runAtLocal.Before(now) {
					offset = 7
				}
				if offset < bestOffset {
					bestOffset = offset
				}
			}
			if bestOffset < 8 {
				newRunAt := time.Date(now.Year(), now.Month(), now.Day(), runAtLocal.Hour(), runAtLocal.Minute(), 0, 0, time.Local)
				newRunAt = newRunAt.AddDate(0, 0, bestOffset)
				schedule.RunAt = newRunAt.UTC()
			}
			changes = append(changes, fmt.Sprintf("days→%s", strings.Join(req.Days, ",")))
		}

		if req.Enabled != nil {
			schedule.Enabled = *req.Enabled
			if *req.Enabled {
				changes = append(changes, "enabled→true")
			} else {
				changes = append(changes, "enabled→false")
			}
		}

		if len(changes) == 0 {
			results = append(results, fmt.Sprintf("- No changes specified for schedule on task \"%s\"", task.Title))
			continue
		}

		nextRun := schedule.ComputeNextRun(time.Now())
		schedule.NextRun = nextRun

		if err := s.scheduleRepo.Update(ctx, schedule); err != nil {
			log.Printf("[telegram] processModifySchedule error updating schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error updating schedule for task \"%s\": %v", task.Title, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Updated schedule for task \"%s\" [TASK_ID:%s]: %s", task.Title, task.ID, strings.Join(changes, ", ")))
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Modify Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// resolveScheduleReference finds a schedule by schedule_id, or by task_id/title (returning the first schedule).
func (s *TelegramService) resolveScheduleReference(ctx context.Context, projectID, scheduleID, taskID, title string) (*models.Schedule, *models.Task, error) {
	if scheduleID != "" {
		if s.scheduleRepo == nil {
			return nil, nil, fmt.Errorf("schedule repository not available")
		}
		schedule, err := s.scheduleRepo.GetByID(ctx, scheduleID)
		if err != nil {
			return nil, nil, fmt.Errorf("error looking up schedule %s: %w", scheduleID, err)
		}
		if schedule == nil {
			return nil, nil, fmt.Errorf("schedule %s not found", scheduleID)
		}
		task, err := s.taskRepo.GetByID(ctx, schedule.TaskID)
		if err != nil || task == nil {
			return nil, nil, fmt.Errorf("task for schedule %s not found", scheduleID)
		}
		if task.ProjectID != projectID {
			return nil, nil, fmt.Errorf("schedule %s belongs to a different project", scheduleID)
		}
		return schedule, task, nil
	}

	task, err := s.resolveTaskReference(ctx, projectID, taskID, title)
	if err != nil {
		return nil, nil, err
	}

	if s.scheduleRepo == nil {
		return nil, nil, fmt.Errorf("schedule repository not available")
	}

	schedules, err := s.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("error listing schedules for task %s: %w", task.ID, err)
	}
	if len(schedules) == 0 {
		return nil, nil, fmt.Errorf("no schedules found for task \"%s\"", task.Title)
	}

	return &schedules[0], task, nil
}

// processListPersonalities handles [LIST_PERSONALITIES] markers by returning available personality presets.
func (s *TelegramService) processListPersonalities(ctx context.Context, execID, projectID, output string) string {
	if !HasListPersonalities(output) {
		return output
	}

	log.Printf("[telegram] processListPersonalities exec=%s", execID)

	personalities := AllPersonalitiesWithCustom(ctx, s.customPersonalityRepo)
	var sb strings.Builder
	sb.WriteString("\n\n---\nAvailable Personalities:\n")
	for _, p := range personalities {
		if p.Key == "" {
			sb.WriteString(fmt.Sprintf("- **%s** (default) — %s\n", p.Name, p.Description))
		} else if p.IsCustom {
			sb.WriteString(fmt.Sprintf("- **%s** (key: `%s`, custom) — %s\n", p.Name, p.Key, p.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s** (key: `%s`) — %s\n", p.Name, p.Key, p.Description))
		}
	}

	// Also show current personality
	if s.settingsRepo != nil {
		current, err := s.settingsRepo.Get(ctx, "personality")
		if err != nil {
			log.Printf("[telegram] processListPersonalities error reading current personality: %v", err)
		}
		if current == "" {
			current = "default"
		}
		sb.WriteString(fmt.Sprintf("\nCurrent personality: **%s**\n", current))
	}

	output += sb.String()
	return output
}

// processSetPersonality handles [SET_PERSONALITY] markers by changing the global personality setting.
func (s *TelegramService) processSetPersonality(ctx context.Context, execID, projectID, output string) string {
	requests := ParseSetPersonality(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[telegram] processSetPersonality exec=%s found %d requests", execID, len(requests))

	var results []string
	for _, req := range requests {
		// Validate personality key
		valid := false
		var matchedName string
		for _, p := range AllPersonalities() {
			if p.Key == req.Personality {
				valid = true
				matchedName = p.Name
				break
			}
		}
		if !valid {
			results = append(results, fmt.Sprintf("- Unknown personality %q. Use [LIST_PERSONALITIES] to see available options.", req.Personality))
			continue
		}

		if s.settingsRepo == nil {
			results = append(results, fmt.Sprintf("- Error setting personality to %q: settings not available", req.Personality))
			continue
		}

		if err := s.settingsRepo.Set(ctx, "personality", req.Personality); err != nil {
			log.Printf("[telegram] processSetPersonality error: %v", err)
			results = append(results, fmt.Sprintf("- Error setting personality to %q: %v", req.Personality, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Personality changed to **%s** (`%s`)", matchedName, req.Personality))
		log.Printf("[telegram] processSetPersonality set personality to %q", req.Personality)
	}

	if len(results) > 0 {
		output += "\n\n---\nPersonality Settings:\n" + strings.Join(results, "\n")
	}

	return output
}

// processListModels handles [LIST_MODELS] markers by returning available AI model configurations.
func (s *TelegramService) processListModels(ctx context.Context, execID, projectID, output string) string {
	if !HasListModels(output) {
		return output
	}

	log.Printf("[telegram] processListModels exec=%s", execID)

	configs, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processListModels error listing models: %v", err)
		output += "\n\n---\nModel Settings:\n- Error retrieving model configurations: " + err.Error()
		return output
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nConfigured Models:\n")
	if len(configs) == 0 {
		sb.WriteString("No models configured.\n")
	} else {
		for _, c := range configs {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			authStr := string(c.AuthMethod)
			if authStr == "" {
				authStr = "cli"
			}
			workerInfo := ""
			if c.MaxWorkers > 0 {
				workerInfo = fmt.Sprintf(" | max_workers: %d", c.MaxWorkers)
			}
			sb.WriteString(fmt.Sprintf("- **%s**%s — provider: %s, model: %s, auth: %s%s\n",
				c.Name, defaultStr, c.Provider, c.Model, authStr, workerInfo))
		}
	}

	output += sb.String()
	return output
}

// processListAgents handles [LIST_AGENTS] markers by returning available agent definitions.
func (s *TelegramService) processListAgents(ctx context.Context, execID, projectID, output string) string {
	if !HasListAgents(output) {
		return output
	}
	log.Printf("[telegram] processListAgents exec=%s", execID)
	if s.agentRepo == nil {
		output += "\n\n---\nConfigured Agents:\nAgent definitions not available.\n"
		return output
	}
	agents, err := s.agentRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processListAgents error: %v", err)
		output += "\n\n---\nConfigured Agents:\n- Error: " + err.Error()
		return output
	}
	var sb strings.Builder
	sb.WriteString("\n\n---\nConfigured Agents:\n")
	if len(agents) == 0 {
		sb.WriteString("No agents configured.\n")
	} else {
		for _, a := range agents {
			modelStr := ""
			if a.Model != "inherit" {
				modelStr = fmt.Sprintf(", model: %s", a.Model)
			}
			sb.WriteString(fmt.Sprintf("- **%s** — %s%s, %d skills, %d MCP servers\n",
				a.Name, a.Description, modelStr, len(a.Skills), len(a.MCPServers)))
		}
	}
	output += sb.String()
	return output
}

// processViewSettings handles [VIEW_SETTINGS] markers by returning current app settings.
func (s *TelegramService) processViewSettings(ctx context.Context, execID, projectID, output string) string {
	if !HasViewSettings(output) {
		return output
	}

	log.Printf("[telegram] processViewSettings exec=%s", execID)

	var sb strings.Builder
	sb.WriteString("\n\n---\nApp Settings:\n")

	// Personality
	if s.settingsRepo != nil {
		personality, err := s.settingsRepo.Get(ctx, "personality")
		if err != nil {
			log.Printf("[telegram] processViewSettings error reading personality: %v", err)
		}
		if personality == "" {
			personality = "default (no personality)"
		}
		sb.WriteString(fmt.Sprintf("- **Personality:** %s\n", personality))
	}

	// Model count
	configs, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processViewSettings error listing models: %v", err)
	} else {
		sb.WriteString(fmt.Sprintf("- **Configured models:** %d\n", len(configs)))
		for _, c := range configs {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			sb.WriteString(fmt.Sprintf("  - %s%s — %s/%s\n", c.Name, defaultStr, c.Provider, c.Model))
		}
	}

	// Per-project worker limits
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processViewSettings error listing projects: %v", err)
	} else {
		hasProjectLimits := false
		for _, p := range projects {
			if p.MaxWorkers != nil {
				if !hasProjectLimits {
					sb.WriteString("- **Per-project worker limits:**\n")
					hasProjectLimits = true
				}
				sb.WriteString(fmt.Sprintf("  - %s: %d\n", p.Name, *p.MaxWorkers))
			}
		}
		if !hasProjectLimits {
			sb.WriteString("- **Per-project worker limits:** none configured\n")
		}
	}

	// Per-model worker pools
	if configs != nil {
		hasModelPools := false
		for _, c := range configs {
			if c.MaxWorkers > 0 {
				if !hasModelPools {
					sb.WriteString("- **Per-model worker pools:**\n")
					hasModelPools = true
				}
				if c.WorkerTimeout > 0 {
					sb.WriteString(fmt.Sprintf("  - %s: max_workers=%d, timeout=%ds\n", c.Name, c.MaxWorkers, c.WorkerTimeout))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s: max_workers=%d\n", c.Name, c.MaxWorkers))
				}
			}
		}
		if !hasModelPools {
			sb.WriteString("- **Per-model worker pools:** none configured\n")
		}
	}

	output += sb.String()
	return output
}

// processProjectInfo handles [PROJECT_INFO] markers by returning current project details and task counts.
func (s *TelegramService) processProjectInfo(ctx context.Context, execID, projectID, output string) string {
	if !HasProjectInfo(output) {
		return output
	}

	log.Printf("[telegram] processProjectInfo exec=%s project=%s", execID, projectID)

	var sb strings.Builder
	sb.WriteString("\n\n---\nProject Info:\n")

	// Get project details
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		log.Printf("[telegram] processProjectInfo error getting project: %v", err)
		sb.WriteString("- Error retrieving project details\n")
		output += sb.String()
		return output
	}

	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", project.Name))
	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("- **Description:** %s\n", project.Description))
	}
	if project.RepoPath != "" {
		sb.WriteString(fmt.Sprintf("- **Repository:** %s\n", project.RepoPath))
	}

	// Task counts by category
	categoryCounts, err := s.taskRepo.CountByProjectAndCategory(ctx, projectID)
	if err != nil {
		log.Printf("[telegram] processProjectInfo error counting tasks: %v", err)
	} else {
		total := 0
		for _, count := range categoryCounts {
			total += count
		}
		sb.WriteString(fmt.Sprintf("- **Total tasks:** %d\n", total))
		sb.WriteString("- **Tasks by category:**\n")
		for category, count := range categoryCounts {
			sb.WriteString(fmt.Sprintf("  - %s: %d\n", category, count))
		}
	}

	output += sb.String()
	return output
}

// processListAlerts handles [LIST_ALERTS] markers by returning alerts for the current project.
func (s *TelegramService) processListAlerts(ctx context.Context, execID, projectID, output string) string {
	if !HasListAlerts(output) {
		return output
	}

	log.Printf("[telegram] processListAlerts exec=%s project=%s", execID, projectID)

	if s.alertSvc == nil {
		output += "\n\n---\nAlert Results:\n- Alert service not available"
		return output
	}

	alerts, err := s.alertSvc.ListByProject(ctx, projectID, 50)
	if err != nil {
		log.Printf("[telegram] processListAlerts error listing alerts: %v", err)
		output += "\n\n---\nAlert Results:\n- Error retrieving alerts: " + err.Error()
		return output
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nAlert Results:\n")
	if len(alerts) == 0 {
		sb.WriteString("No alerts found. You're all clear!\n")
	} else {
		unreadCount, _ := s.alertSvc.CountUnread(ctx, projectID)
		sb.WriteString(fmt.Sprintf("Found %d alerts (%d unread):\n", len(alerts), unreadCount))
		for _, a := range alerts {
			readStr := "unread"
			if a.IsRead {
				readStr = "read"
			}
			taskStr := ""
			if a.TaskID != nil {
				taskStr = fmt.Sprintf(" | task: %s", *a.TaskID)
			}
			sb.WriteString(fmt.Sprintf("- **%s** (id: `%s`, type: %s, severity: %s, %s%s) — %s\n",
				a.Title, a.ID, a.Type, a.Severity, readStr, taskStr, a.CreatedAt.Format("Jan 2, 2006 3:04 PM")))
			if a.Message != "" {
				sb.WriteString(fmt.Sprintf("  Message: %s\n", a.Message))
			}
		}
	}

	output += sb.String()
	return output
}

// processCreateAlert handles [CREATE_ALERT] markers by creating new alerts.
func (s *TelegramService) processCreateAlert(ctx context.Context, execID, projectID, output string) string {
	requests := ParseCreateAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[telegram] processCreateAlert exec=%s found %d requests", execID, len(requests))

	if s.alertSvc == nil {
		output += "\n\n---\nAlert Create Results:\n- Alert service not available"
		return output
	}

	var results []string
	for _, req := range requests {
		severity := models.SeverityInfo
		switch req.Severity {
		case "warning":
			severity = models.SeverityWarning
		case "error":
			severity = models.SeverityError
		case "info", "":
			severity = models.SeverityInfo
		default:
			results = append(results, fmt.Sprintf("- Invalid severity %q (use info, warning, or error)", req.Severity))
			continue
		}

		alertType := models.AlertCustom
		switch req.Type {
		case "task_failed":
			alertType = models.AlertTaskFailed
		case "task_needs_followup":
			alertType = models.AlertTaskNeedsFollowup
		case "custom", "":
			alertType = models.AlertCustom
		default:
			results = append(results, fmt.Sprintf("- Invalid alert type %q (use custom, task_failed, or task_needs_followup)", req.Type))
			continue
		}

		a := &models.Alert{
			ProjectID: projectID,
			Type:      alertType,
			Severity:  severity,
			Title:     req.Title,
			Message:   req.Message,
		}
		if req.TaskID != "" {
			a.TaskID = &req.TaskID
		}

		if err := s.alertSvc.Create(ctx, a); err != nil {
			log.Printf("[telegram] processCreateAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error creating alert %q: %v", req.Title, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Created alert %q (id: `%s`, severity: %s)", req.Title, a.ID, severity))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Create Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processDeleteAlert handles [DELETE_ALERT] markers by deleting alerts by ID.
func (s *TelegramService) processDeleteAlert(ctx context.Context, execID, projectID, output string) string {
	requests := ParseDeleteAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[telegram] processDeleteAlert exec=%s found %d requests", execID, len(requests))

	if s.alertSvc == nil {
		output += "\n\n---\nAlert Delete Results:\n- Alert service not available"
		return output
	}

	var results []string
	for _, req := range requests {
		if err := s.alertSvc.Delete(ctx, req.AlertID); err != nil {
			log.Printf("[telegram] processDeleteAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error deleting alert %q: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Deleted alert `%s`", req.AlertID))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Delete Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processToggleAlert handles [TOGGLE_ALERT] markers by marking alerts as read.
func (s *TelegramService) processToggleAlert(ctx context.Context, execID, projectID, output string) string {
	requests := ParseToggleAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[telegram] processToggleAlert exec=%s found %d requests", execID, len(requests))

	if s.alertSvc == nil {
		output += "\n\n---\nAlert Toggle Results:\n- Alert service not available"
		return output
	}

	var results []string
	for _, req := range requests {
		if err := s.alertSvc.MarkRead(ctx, req.AlertID); err != nil {
			log.Printf("[telegram] processToggleAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error marking alert %q as read: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Marked alert `%s` as read", req.AlertID))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Toggle Results:\n" + strings.Join(results, "\n")
	}

	return output
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

// processListProjects handles [LIST_PROJECTS] markers by returning all available projects.
func (s *TelegramService) processListProjects(ctx context.Context, execID, projectID, output string) string {
	if !HasListProjects(output) {
		return output
	}

	log.Printf("[telegram] processListProjects exec=%s", execID)

	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processListProjects error listing projects: %v", err)
		output += "\n\n---\nAvailable Projects:\n- Error retrieving projects: " + err.Error()
		return output
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nAvailable Projects:\n")
	if len(projects) == 0 {
		sb.WriteString("No projects found.\n")
	} else {
		for _, p := range projects {
			marker := ""
			if p.ID == projectID {
				marker = " ← _current_"
			}
			desc := ""
			if p.Description != "" {
				desc = fmt.Sprintf(" — %s", p.Description)
			}
			sb.WriteString(fmt.Sprintf("- **%s**%s%s\n", p.Name, desc, marker))
		}
	}
	sb.WriteString("\nUse `/project <name>` or say \"switch to project <name>\" to change projects.\n")

	output += sb.String()
	return output
}

// processSwitchProject handles [SWITCH_PROJECT] markers by changing the user's active project.
func (s *TelegramService) processSwitchProject(ctx context.Context, execID, projectID, output string, userID int64) string {
	requests := ParseSwitchProject(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[telegram] processSwitchProject exec=%s found %d requests", execID, len(requests))

	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		log.Printf("[telegram] processSwitchProject error listing projects: %v", err)
		output += "\n\n---\nProject Switch Results:\n- Error loading projects: " + err.Error()
		return output
	}

	var results []string
	for _, req := range requests {
		var targetProject *models.Project
		for i := range projects {
			if strings.EqualFold(projects[i].Name, req.Project) || projects[i].ID == req.Project {
				targetProject = &projects[i]
				break
			}
		}

		if targetProject == nil {
			var availableNames []string
			for _, p := range projects {
				availableNames = append(availableNames, p.Name)
			}
			results = append(results, fmt.Sprintf("- Project not found: %q. Available projects: %s", req.Project, strings.Join(availableNames, ", ")))
			continue
		}

		// Update user's active project
		s.userProjects[userID] = targetProject.ID
		if s.telegramUserProjectRepo != nil {
			if err := s.telegramUserProjectRepo.SetUserProject(ctx, fmt.Sprintf("%d", userID), targetProject.ID); err != nil {
				log.Printf("[telegram] processSwitchProject error persisting: %v", err)
			}
		}
		results = append(results, fmt.Sprintf("- Switched to project: **%s**", targetProject.Name))
		log.Printf("[telegram] processSwitchProject user=%d switched to project=%s (%s)", userID, targetProject.ID, targetProject.Name)
	}

	if len(results) > 0 {
		output += "\n\n---\nProject Switch Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processTaskMessage runs a task follow-up message in a background goroutine.
// Similar to handler.processStreamingResponse but simplified for Telegram.
func (s *TelegramService) processTaskMessage(execID, taskID, message string, agent models.LLMConfig, chatHistory []models.Execution, projectID, systemContext, workDir string) {
	if s.llmSvc == nil {
		log.Printf("[telegram] processTaskMessage exec=%s task=%s skipping: llmSvc is nil", execID, taskID)
		return
	}

	timeout := telegramProcessTimeout
	agentConfigID := agent.ID

	// Enforce per-project and per-model worker constraints for task followups.
	// Acquires both project and model slots atomically (releases project slot if model fails).
	if s.workerSvc != nil {
		// Apply model-specific worker_timeout if configured
		if modelTimeout := s.workerSvc.GetModelWorkerTimeout(agentConfigID); modelTimeout > 0 {
			timeout = modelTimeout
		}

		// Acquire per-project slot (respects project max_workers limit)
		if !s.workerSvc.TryAcquireProjectSlot(projectID) {
			log.Printf("[telegram] processTaskMessage exec=%s task=%s project %s at capacity",
				execID, taskID, projectID)
			s.completeExecution(context.Background(), execID, taskID, "",
				"project worker limit reached — please wait for a running task to complete", 0, 0)
			return
		}
		defer s.workerSvc.ReleaseProjectSlot(projectID)

		// Block until a model slot is available (respects max_workers)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), timeout)
		if err := s.workerSvc.AcquireModelSlot(waitCtx, agentConfigID); err != nil {
			waitCancel()
			log.Printf("[telegram] processTaskMessage exec=%s task=%s failed to acquire model slot for %s: %v",
				execID, taskID, agentConfigID, err)
			s.completeExecution(context.Background(), execID, taskID, "",
				fmt.Sprintf("model %s at capacity, timed out waiting for slot", agent.Name), 0, 0)
			return
		}
		waitCancel()
		defer s.workerSvc.ReleaseModelSlot(agentConfigID)

		log.Printf("[telegram] processTaskMessage exec=%s acquired project + model slots for %s", execID, agentConfigID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if s.workerSvc != nil {
		s.workerSvc.RegisterCancel(taskID, cancel)
		defer s.workerSvc.DeregisterCancel(taskID)
	}

	start := time.Now()
	output, tokensUsed, err := s.llmSvc.CallAgentDirectStreaming(
		ctx, message, nil, agent, execID, chatHistory, systemContext, workDir, true,
	)
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		log.Printf("[telegram] processTaskMessage exec=%s task=%s failed: %v", execID, taskID, err)
		s.completeExecution(ctx, execID, taskID, "", err.Error(), 0, durationMs)
		return
	}

	log.Printf("[telegram] processTaskMessage exec=%s task=%s success tokens=%d duration=%dms", execID, taskID, tokensUsed, durationMs)
	s.completeExecution(ctx, execID, taskID, output, "", tokensUsed, durationMs)
}

// resolveTaskReference finds a task by ID or by title search within a project.
func (s *TelegramService) resolveTaskReference(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
	if taskID != "" {
		task, err := s.taskRepo.GetByID(ctx, taskID)
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

	if title != "" {
		tasks, err := s.taskRepo.SearchByTitle(ctx, projectID, title)
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

// completeExecution marks an execution and its task as completed or failed
func (s *TelegramService) completeExecution(ctx context.Context, execID, taskID, output, errorMessage string, tokensUsed int, durationMs int64) {
	if errorMessage != "" {
		if err := s.execRepo.Complete(ctx, execID, models.ExecFailed, "", errorMessage, 0, durationMs); err != nil {
			log.Printf("[telegram] error completing execution (failed): %v", err)
		}
		if err := s.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
			log.Printf("[telegram] error updating task status (failed): %v", err)
		}
		return
	}

	if err := s.execRepo.Complete(ctx, execID, models.ExecCompleted, output, "", tokensUsed, durationMs); err != nil {
		log.Printf("[telegram] error completing execution: %v", err)
	}
	if err := s.taskRepo.UpdateStatus(ctx, taskID, models.StatusCompleted); err != nil {
		log.Printf("[telegram] error updating task status: %v", err)
	}
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

// getActiveProject returns the active project ID for a user
func (s *TelegramService) getActiveProject(userID int64) string {
	if projectID, ok := s.userProjects[userID]; ok {
		return projectID
	}

	// Check persisted project selection from DB
	if s.telegramUserProjectRepo != nil {
		savedProjectID, err := s.telegramUserProjectRepo.GetUserProject(context.Background(), fmt.Sprintf("%d", userID))
		if err != nil {
			log.Printf("[telegram] error loading persisted project for user %d: %v", userID, err)
		} else if savedProjectID != "" {
			s.userProjects[userID] = savedProjectID
			return savedProjectID
		}
	}

	// Try to get default project
	projects, err := s.projectRepo.List(context.Background())
	if err != nil || len(projects) == 0 {
		return ""
	}

	for _, project := range projects {
		if project.IsDefault {
			s.userProjects[userID] = project.ID
			return project.ID
		}
	}

	// Use first project as fallback
	s.userProjects[userID] = projects[0].ID
	return projects[0].ID
}

// sendMessage sends a message to a chat, splitting if needed
func (s *TelegramService) sendMessage(chatID int64, text string) {
	if s.sendMessageFunc != nil {
		s.sendMessageFunc(chatID, text)
		return
	}

	messages := splitMessage(text, maxMessageLength)

	for _, msg := range messages {
		msgConfig := tgbotapi.NewMessage(chatID, msg)
		msgConfig.ParseMode = "Markdown"

		if _, err := s.bot.Send(msgConfig); err != nil {
			// Retry without Markdown if parsing fails
			log.Printf("[telegram] error sending message with Markdown: %v, retrying without", err)
			msgConfig.ParseMode = ""
			if _, err := s.bot.Send(msgConfig); err != nil {
				log.Printf("[telegram] error sending message: %v", err)
			}
		}
	}
}

// editMessage edits an existing Telegram message
func (s *TelegramService) editMessage(chatID int64, messageID int, text string) {
	if len(text) > maxMessageLength {
		text = text[:maxMessageLength-3] + "..."
	}

	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = "Markdown"

	if _, err := s.bot.Send(edit); err != nil {
		// Retry without Markdown if parsing fails
		edit.ParseMode = ""
		if _, err := s.bot.Send(edit); err != nil {
			// Ignore "message is not modified" errors (content unchanged)
			if !strings.Contains(err.Error(), "message is not modified") {
				log.Printf("[telegram] error editing message: %v", err)
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

// IsSendResponsesEnabled checks the "telegram_send_responses" setting.
// Returns true (default) when the setting is not explicitly "false".
func (s *TelegramService) IsSendResponsesEnabled(ctx context.Context) bool {
	if s.settingsRepo == nil {
		return true
	}
	val, err := s.settingsRepo.Get(ctx, "telegram_send_responses")
	if err != nil || val == "" {
		return true // default: enabled
	}
	return val != "false"
}

// SendTaskCompletionNotification sends a task result back to the Telegram user
// who created it, if send-responses is enabled and the task originated from Telegram.
func (s *TelegramService) SendTaskCompletionNotification(ctx context.Context, task models.Task, output string, errMsg string) {
	needsHydration := task.CreatedVia != models.TaskOriginTelegram || task.TelegramChatID == 0
	if needsHydration {
		if task.ID == "" || s.taskRepo == nil {
			log.Printf("[telegram] completion notification task %s missing Telegram origin and cannot reload (has_id=%t task_repo_set=%t)", task.ID, task.ID != "", s.taskRepo != nil)
		} else {
			log.Printf("[telegram] completion notification task %s missing Telegram origin in memory (created_via=%q chat_id=%d), reloading from DB", task.ID, task.CreatedVia, task.TelegramChatID)
			loadedTask, err := s.taskRepo.GetByID(ctx, task.ID)
			if err != nil {
				log.Printf("[telegram] failed reloading task %s for completion notification: %v", task.ID, err)
			} else if loadedTask == nil {
				log.Printf("[telegram] task %s not found during completion notification reload", task.ID)
			} else {
				task = *loadedTask
				log.Printf("[telegram] reloaded task %s for completion notification (created_via=%q chat_id=%d category=%s)", task.ID, task.CreatedVia, task.TelegramChatID, task.Category)
			}
		}
	}

	if task.CreatedVia != models.TaskOriginTelegram {
		log.Printf("[telegram] skipping completion notification for task %s: created_via=%q", task.ID, task.CreatedVia)
		return
	}
	if task.TelegramChatID == 0 {
		log.Printf("[telegram] skipping completion notification for task %s: missing telegram chat id", task.ID)
		return
	}

	// Check the setting
	if !s.IsSendResponsesEnabled(ctx) {
		log.Printf("[telegram] send-responses disabled, skipping notification for task %s", task.ID)
		return
	}

	// Don't notify for chat tasks (they already get a direct response)
	if task.Category == models.CategoryChat {
		log.Printf("[telegram] skipping completion notification for task %s: category=chat", task.ID)
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

	s.sendMessage(task.TelegramChatID, message)
	log.Printf("[telegram] sent completion notification for task %s to chat %d", task.ID, task.TelegramChatID)
}
