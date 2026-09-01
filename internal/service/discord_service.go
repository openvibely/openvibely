package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

const (
	DiscordSettingBotToken      = "discord_bot_token"
	DiscordSettingBotUserID     = "discord_bot_user_id"
	DiscordSettingSendResponses = "discord_send_responses"

	discordProcessTimeout   = 5 * time.Minute
	discordChatHistoryLimit = 50
	discordMessageLimit     = 2000
	discordMaxFileSize      = 10 << 20
	discordMaxTextFileSize  = 100 * 1024
	discordMaxFilesPerMsg   = 3
	discordMaxRedirects     = 10
)

var discordMentionRegex = regexp.MustCompile(`<@!?[0-9]+>`)

type DiscordConnectionStatus struct {
	Configured    bool
	Connected     bool
	Running       bool
	BotUserID     string
	SendResponses bool
	HasBotToken   bool
	LastError     string
}

type DiscordService struct {
	session                    *discordgo.Session
	settingsRepo               *repository.SettingsRepo
	discordAuthRepo            *repository.DiscordAuthRepo
	discordTaskContextRepo     *repository.DiscordTaskContextRepo
	discordUserProjectRepo     *repository.DiscordUserProjectRepo
	projectRepo                *repository.ProjectRepo
	projectSvc                 *ProjectService
	githubProjectSvc           GitHubProjectCloneProvider
	memorySvc                  *MemoryService
	agentLibraryMaintenanceSvc *AgentLibraryMaintenanceService
	llmConfigRepo              *repository.LLMConfigRepo
	taskRepo                   *repository.TaskRepo
	execRepo                   *repository.ExecutionRepo
	scheduleRepo               *repository.ScheduleRepo
	taskSvc                    *TaskService
	taskGoalSvc                *TaskGoalService
	llmSvc                     *LLMService
	workerSvc                  *WorkerService
	automationGraphSvc         *AutomationGraphService
	automationDraftSvc         *AutomationDraftService
	automationCompiler         *AutomationCompiler
	threadInputRepo            *repository.ThreadInputRepo
	chatAttachmentRepo         *repository.ChatAttachmentRepo
	customPersonalityRepo      *repository.CustomPersonalityRepo
	agentRepo                  *repository.AgentRepo
	alertSvc                   *AlertService
	usageAnalyticsSvc          *UsageAnalyticsService
	upcomingSvc                *UpcomingService
	emailStatus                func(context.Context) EmailConnectionStatus
	emailAuthRepo              *repository.EmailAuthRepo
	webhookRepo                *repository.WebhookRepo
	chatBroadcaster            *events.ChatBroadcaster
	executionStreamHub         *events.ExecutionStreamHub
	queuedTurnPromoter         func(projectID string)
	queuedTaskThreadPromoter   func(taskID string)
	channelChatRunner          ChannelChatRunner
	channelTaskRunner          ChannelTaskRunner
	channelMessageRouter       *ChannelMessageRouter
	userProjects               map[string]string
	uploadsDir                 string
	httpClient                 *http.Client

	mu                       sync.RWMutex
	running                  bool
	lastStartError           string
	ctx                      context.Context
	cancel                   context.CancelFunc
	sendMessageFunc          func(channelID, messageID, text string) (string, error)
	createDMChannelFunc      func(userID string) (string, error)
	processIncomingMessageFn func(msg discordIncomingMessage)
}

func NewDiscordService(
	settingsRepo *repository.SettingsRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	execRepo *repository.ExecutionRepo,
	scheduleRepo *repository.ScheduleRepo,
	taskSvc *TaskService,
	llmSvc *LLMService,
	workerSvc *WorkerService,
	discordAuthRepo *repository.DiscordAuthRepo,
	discordTaskContextRepo *repository.DiscordTaskContextRepo,
) *DiscordService {
	var usageAnalyticsSvc *UsageAnalyticsService
	if execRepo != nil {
		if db := execRepo.DB(); db != nil {
			usageAnalyticsSvc = NewUsageAnalyticsService(repository.NewUsageRepo(db), llmConfigRepo)
		}
	}
	return &DiscordService{
		settingsRepo:           settingsRepo,
		projectRepo:            projectRepo,
		llmConfigRepo:          llmConfigRepo,
		taskRepo:               taskRepo,
		execRepo:               execRepo,
		scheduleRepo:           scheduleRepo,
		taskSvc:                taskSvc,
		llmSvc:                 llmSvc,
		workerSvc:              workerSvc,
		usageAnalyticsSvc:      usageAnalyticsSvc,
		discordAuthRepo:        discordAuthRepo,
		discordTaskContextRepo: discordTaskContextRepo,
		userProjects:           make(map[string]string),
		uploadsDir:             "uploads",
		httpClient:             &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *DiscordService) SetChatBroadcaster(cb *events.ChatBroadcaster) { s.chatBroadcaster = cb }
func (s *DiscordService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
}
func (s *DiscordService) SetChatAttachmentRepo(repo *repository.ChatAttachmentRepo) {
	s.chatAttachmentRepo = repo
}
func (s *DiscordService) SetUploadsDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	s.uploadsDir = dir
}
func (s *DiscordService) SetThreadInputRepo(repo *repository.ThreadInputRepo) {
	s.threadInputRepo = repo
}
func (s *DiscordService) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	s.customPersonalityRepo = repo
}
func (s *DiscordService) SetProjectCreationServices(projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider, memorySvc *MemoryService, agentLibraryMaintenanceSvc *AgentLibraryMaintenanceService) {
	s.projectSvc = projectSvc
	s.githubProjectSvc = githubSvc
	s.memorySvc = memorySvc
	s.agentLibraryMaintenanceSvc = agentLibraryMaintenanceSvc
}
func (s *DiscordService) SetAgentRepo(repo *repository.AgentRepo) { s.agentRepo = repo }
func (s *DiscordService) SetAlertService(svc *AlertService)       { s.alertSvc = svc }
func (s *DiscordService) SetUpcomingService(svc *UpcomingService) { s.upcomingSvc = svc }
func (s *DiscordService) SetEmailStatusProvider(provider func(context.Context) EmailConnectionStatus) {
	s.emailStatus = provider
}
func (s *DiscordService) SetEmailAuthRepo(repo *repository.EmailAuthRepo) { s.emailAuthRepo = repo }
func (s *DiscordService) SetWebhookRepo(repo *repository.WebhookRepo)     { s.webhookRepo = repo }
func (s *DiscordService) SetTaskGoalService(svc *TaskGoalService)         { s.taskGoalSvc = svc }
func (s *DiscordService) SetAutomationGraphService(svc *AutomationGraphService) {
	s.automationGraphSvc = svc
}
func (s *DiscordService) SetAutomationTemplateUpdateServices(drafts *AutomationDraftService, compiler *AutomationCompiler) {
	s.automationDraftSvc = drafts
	s.automationCompiler = compiler
}
func (s *DiscordService) SetQueuedTurnPromoter(promoter func(projectID string)) {
	s.queuedTurnPromoter = promoter
}
func (s *DiscordService) SetQueuedTaskThreadPromoter(promoter func(taskID string)) {
	s.queuedTaskThreadPromoter = promoter
}
func (s *DiscordService) SetChannelChatRunner(runner ChannelChatRunner) { s.channelChatRunner = runner }
func (s *DiscordService) SetChannelTaskRunner(runner ChannelTaskRunner) { s.channelTaskRunner = runner }
func (s *DiscordService) SetChannelMessageRouter(router *ChannelMessageRouter) {
	s.channelMessageRouter = router
}
func (s *DiscordService) SetDiscordUserProjectRepo(repo *repository.DiscordUserProjectRepo) {
	s.discordUserProjectRepo = repo
}

func (s *DiscordService) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *DiscordService) runtimeStatus() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running, s.lastStartError
}

func (s *DiscordService) Start() error {
	botToken := strings.TrimSpace(s.getSetting(context.Background(), DiscordSettingBotToken))
	if botToken == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		wrapped := fmt.Errorf("create discord session: %w", err)
		s.lastStartError = wrapped.Error()
		return wrapped
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	session.AddHandler(func(sess *discordgo.Session, msg *discordgo.MessageCreate) {
		s.handleMessageCreate(context.Background(), sess, msg)
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.session = session
	s.ctx = ctx
	s.cancel = cancel
	if err := session.Open(); err != nil {
		cancel()
		s.session = nil
		s.ctx = nil
		s.cancel = nil
		s.running = false
		wrapped := fmt.Errorf("open discord gateway: %w", err)
		s.lastStartError = wrapped.Error()
		return wrapped
	}
	if session.State != nil && session.State.User != nil && strings.TrimSpace(session.State.User.ID) != "" {
		_ = s.setSetting(context.Background(), DiscordSettingBotUserID, session.State.User.ID)
	}
	s.running = true
	s.lastStartError = ""
	applog.Infof("[discord] gateway started")
	go func() {
		<-ctx.Done()
	}()
	return nil
}

func (s *DiscordService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running && s.session == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.session != nil {
		_ = s.session.Close()
	}
	s.running = false
	s.session = nil
	s.ctx = nil
	s.cancel = nil
	applog.Infof("[discord] gateway stopped")
}

func (s *DiscordService) ReloadFromSettings(ctx context.Context) error {
	s.Stop()
	return s.Start()
}

func (s *DiscordService) Disconnect(ctx context.Context) error {
	s.Stop()
	_ = s.setSetting(ctx, DiscordSettingBotToken, "")
	_ = s.setSetting(ctx, DiscordSettingBotUserID, "")
	_ = s.setSetting(ctx, DiscordSettingSendResponses, "")
	return nil
}

func (s *DiscordService) GetConnectionStatus(ctx context.Context) (DiscordConnectionStatus, error) {
	botToken := strings.TrimSpace(s.getSetting(ctx, DiscordSettingBotToken))
	running, lastErr := s.runtimeStatus()
	status := DiscordConnectionStatus{
		HasBotToken:   botToken != "",
		BotUserID:     strings.TrimSpace(s.getSetting(ctx, DiscordSettingBotUserID)),
		SendResponses: s.IsSendResponsesEnabled(ctx),
		Running:       running,
		LastError:     lastErr,
	}
	status.Configured = status.HasBotToken
	status.Connected = status.Configured && status.Running
	return status, nil
}

func (s *DiscordService) TestConnection(ctx context.Context) error {
	botToken := strings.TrimSpace(s.getSetting(ctx, DiscordSettingBotToken))
	if botToken == "" {
		return fmt.Errorf("discord bot token is not configured")
	}
	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}
	user, err := session.User("@me")
	if err != nil {
		return fmt.Errorf("auth test failed: %w", err)
	}
	if user != nil && strings.TrimSpace(user.ID) != "" {
		_ = s.setSetting(ctx, DiscordSettingBotUserID, user.ID)
	}
	return nil
}

func (s *DiscordService) IsSendResponsesEnabled(ctx context.Context) bool {
	val := strings.TrimSpace(strings.ToLower(s.getSetting(ctx, DiscordSettingSendResponses)))
	if val == "" {
		return true
	}
	return val != "false"
}

func (s *DiscordService) handleMessageCreate(ctx context.Context, sess *discordgo.Session, msg *discordgo.MessageCreate) {
	if msg == nil || msg.Message == nil || msg.Author == nil {
		return
	}
	botID := s.botUserID(ctx, sess)
	if msg.Author.ID == botID || msg.Author.Bot {
		return
	}
	incoming := discordIncomingMessage{
		ChannelID:       msg.ChannelID,
		ThreadID:        discordThreadID(msg.Message),
		ParentChannelID: discordParentChannelID(sess, msg.ChannelID),
		MessageID:       msg.ID,
		GuildID:         msg.GuildID,
		UserID:          msg.Author.ID,
		Username:        discordDisplayName(msg.Author),
		Text:            strings.TrimSpace(msg.Content),
		IsDM:            discordIsDM(sess, msg.ChannelID, msg.GuildID),
		Source:          "discord",
		Attachments:     discordIncomingAttachmentsFromMessage(msg.Attachments),
	}
	if strings.TrimSpace(incoming.Text) == "" && len(msg.Attachments) > 0 {
		incoming.Text = discordAttachmentPrompt(msg.Attachments)
	}
	if strings.TrimSpace(incoming.Text) == "" {
		return
	}
	if !incoming.IsDM && s.requiresMentionForMessage(ctx, incoming) && !discordMentionsBot(msg.Mentions, botID) && !strings.Contains(incoming.Text, "<@"+botID+">") && !strings.Contains(incoming.Text, "<@!"+botID+">") {
		return
	}
	incoming.Text = sanitizeDiscordText(incoming.Text, botID)
	if strings.TrimSpace(incoming.Text) == "" && len(msg.Attachments) > 0 {
		incoming.Text = discordAttachmentPrompt(msg.Attachments)
	}
	if strings.TrimSpace(incoming.Text) == "" {
		return
	}
	if s.processIncomingMessageFn != nil {
		s.processIncomingMessageFn(incoming)
		return
	}
	go s.processIncomingMessage(incoming)
}

type discordIncomingMessage struct {
	ChannelID       string
	ThreadID        string
	ParentChannelID string
	MessageID       string
	GuildID         string
	UserID          string
	Username        string
	Text            string
	IsDM            bool
	Source          string
	Attachments     []discordIncomingAttachment
}

type discordIncomingAttachment struct {
	ID          string
	FileName    string
	ContentType string
	Size        int
	URL         string
	ProxyURL    string
}

func (s *DiscordService) processIncomingMessage(msg discordIncomingMessage) {
	if msg.ChannelID == "" || msg.UserID == "" || strings.TrimSpace(msg.Text) == "" {
		return
	}
	if s.taskRepo == nil || s.execRepo == nil || s.llmConfigRepo == nil || s.llmSvc == nil || s.taskSvc == nil || s.projectRepo == nil {
		applog.Infof("[discord] incoming message ignored: service dependencies are not fully configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), discordProcessTimeout)
	defer cancel()
	start := time.Now()
	projectID := s.getActiveProject(ctx, msg.UserID)
	if projectID == "" {
		_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "No active project found. Please create a project first in the web UI.")
		return
	}
	if !s.checkAuthorization(ctx, projectID, msg.UserID) {
		applog.Infof("[discord] unauthorized access blocked for user=%s project=%s", msg.UserID, projectID)
		_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "You are not authorized to use Discord access for this project. Contact the project owner to get access.")
		return
	}
	discordAckID := ""
	runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:              "discord",
		ProjectID:             projectID,
		Message:               msg.Text,
		Source:                msg.Source,
		Surface:               chatcontrol.SurfaceDiscord,
		HasAttachments:        len(msg.Attachments) > 0,
		Start:                 start,
		TaskRepo:              s.taskRepo,
		ExecRepo:              s.execRepo,
		ThreadInputRepo:       s.threadInputRepo,
		LLMConfigRepo:         s.llmConfigRepo,
		ChatBroadcaster:       s.chatBroadcaster,
		UploadsDir:            s.uploadsDir,
		TaskSvc:               s.taskSvc,
		ScheduleRepo:          s.scheduleRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		DownloadAttachments: func(ctx context.Context) (channelChatIngressDownloadResult, error) {
			if len(msg.Attachments) == 0 {
				return channelChatIngressDownloadResult{}, nil
			}
			attCtx, imgAtts, chatAtts, err := s.downloadDiscordAttachments(ctx, msg.Attachments)
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAtts}, err
		},
		IncomingAttachmentsNeedVision: func() bool { return discordIncomingAttachmentsRequireVision(msg.Attachments) },
		AttachmentDownloadFailureMessage: func(error, bool) string {
			return "Failed to process attachment: unable to download attachment. Please try again."
		},
		SavePendingAttachments: s.saveChatAttachmentsToPendingSession,
		FindActiveExecution:    s.execRepo.FindLatestActiveChatExecution,
		RecordAttachmentFailure: func(ctx context.Context, agentID, msgText string) {
			s.recordQueuedAttachmentFailure(ctx, projectID, agentID, msg, msgText)
		},
		NewQueuedInput: func() *models.ThreadInput {
			return &models.ThreadInput{DiscordChannelID: msg.replyChannelID(), DiscordThreadID: msg.ThreadID, DiscordMessageID: msg.MessageID, DiscordUserID: msg.UserID}
		},
		OnAttachmentDownloadFailed: func(_ context.Context, msgText string) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "⚠️ "+msgText)
		},
		OnAttachmentStoreFailed: func(_ context.Context, msgText string) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "⚠️ "+msgText)
		},
		OnModelSelectionFailed: func(_ context.Context, err error) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, fmt.Sprintf("Error selecting model: %v", err))
		},
		OnActiveLookupFailed: func(context.Context) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error checking active chat response. Please try again.")
		},
		OnQueueFailure: func(context.Context) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error queueing your message. Please try again.")
		},
		OnQueued: func(context.Context) {
			_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Queued. I'll send this after the current response finishes.")
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task:         &models.Task{Title: fmt.Sprintf("Discord %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Text, 47)), CreatedVia: models.TaskOriginDiscord},
			ReplyContext: ChannelReplyContext{Source: models.TaskOriginDiscord, DiscordChannelID: msg.replyChannelID(), DiscordThreadID: msg.ThreadID, DiscordMessageID: msg.MessageID, DiscordUserID: msg.UserID},
			RuntimeToolsForTask: func(taskID string) *llmcontracts.RuntimeTools {
				return s.buildDiscordActionToolRuntimeForTask(projectID, taskID, discordActionContext{
					ChannelID: msg.replyChannelID(),
					ThreadID:  msg.ThreadID,
					MessageID: msg.MessageID,
					UserID:    msg.UserID,
				}, nil)
			},
			ChannelChatRunner: s.channelChatRunner,
			CreateTaskContext: func(ctx context.Context, taskID string) error {
				if s.discordTaskContextRepo == nil {
					return nil
				}
				return s.discordTaskContextRepo.Upsert(ctx, &models.DiscordTaskContext{TaskID: taskID, DiscordChannelID: msg.replyChannelID(), DiscordThreadID: msg.ThreadID, DiscordMessageID: msg.MessageID, DiscordUserID: msg.UserID})
			},
			CompleteExecution: channelCompletionFunc("discord", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
			LinkAttachments:   s.linkAttachmentsToExecution, AttachmentContextAndImages: discordAttachmentContextAndImages,
			ListChatHistory: func(ctx context.Context, projectID string) ([]models.Execution, error) {
				return s.execRepo.ListChatHistory(ctx, projectID, discordChatHistoryLimit)
			},
			FilterChatHistory: filterDiscordChatHistory,
			OnTaskCreateFailure: func(context.Context) {
				_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error processing your message. Please try again.")
			},
			OnTaskContextFailure: func(context.Context) {
				_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error processing your message. Please try again.")
			},
			OnExecutionCreateFailure: func(context.Context) {
				_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error processing your message. Please try again.")
			},
			OnAttachmentLinkFailure: func(_ context.Context, msgText string) {
				_ = s.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "⚠️ "+msgText)
			},
			PrepareRunner: func(ctx context.Context, taskID, execID string) int {
				discordAckID, _ = s.sendDiscordMessageWithID(msg.replyChannelID(), msg.MessageID, "Thinking...")
				if discordAckID != "" && s.discordTaskContextRepo != nil {
					if err := s.discordTaskContextRepo.Upsert(ctx, &models.DiscordTaskContext{TaskID: taskID, DiscordChannelID: msg.replyChannelID(), DiscordThreadID: msg.ThreadID, DiscordMessageID: discordAckID, DiscordUserID: msg.UserID}); err != nil {
						applog.Infof("[discord] update chat ack context failed task=%s: %v", taskID, err)
					}
				}
				return 0
			},
			OnRunnerUnavailable: func(_ context.Context, msgText string, _ int) {
				_ = s.editOrSendDiscordMessage(msg.replyChannelID(), discordAckID, msg.MessageID, msgText)
			},
		},
	})
}

func (m discordIncomingMessage) replyChannelID() string {
	if m.ThreadID != "" {
		return m.ThreadID
	}
	return m.ChannelID
}

func (s *DiscordService) recordQueuedAttachmentFailure(ctx context.Context, projectID, agentID string, msg discordIncomingMessage, msgText string) {
	if s.taskRepo == nil || s.execRepo == nil {
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Attachments) > 0})
		}
		return
	}
	task := &models.Task{
		ProjectID:  projectID,
		Title:      fmt.Sprintf("Discord %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Text, 47)),
		Prompt:     msg.Text,
		Status:     models.StatusPending,
		Category:   models.CategoryChat,
		AgentID:    &agentID,
		CreatedVia: models.TaskOriginDiscord,
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		applog.Infof("[discord] create queued attachment failure task failed: %v", err)
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Attachments) > 0})
		}
		return
	}
	if s.discordTaskContextRepo != nil {
		if err := s.discordTaskContextRepo.Upsert(ctx, &models.DiscordTaskContext{
			TaskID:           task.ID,
			DiscordChannelID: msg.replyChannelID(),
			DiscordThreadID:  msg.ThreadID,
			DiscordMessageID: msg.MessageID,
			DiscordUserID:    msg.UserID,
		}); err != nil {
			applog.Infof("[discord] create queued attachment failure context failed task=%s: %v", task.ID, err)
		}
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agentID, Status: models.ExecRunning, PromptSent: msg.Text}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		applog.Infof("[discord] create queued attachment failure execution failed task=%s: %v", task.ID, err)
		_ = s.taskRepo.Delete(ctx, task.ID)
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Attachments) > 0})
		}
		return
	}
	channelCompletionFunc("discord", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter)(ctx, exec.ID, task.ID, "", msgText, 0, 0)
	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, ExecID: exec.ID, TaskID: task.ID, Message: msg.Text, Source: msg.Source, AgentName: "", HasAttachments: len(msg.Attachments) > 0})
	}
}

func (s *DiscordService) SendTaskCompletionToThread(ctx context.Context, channelID, threadID, messageID, taskTitle, output, errMsg, userID string) {
	if !s.IsSendResponsesEnabled(ctx) || channelID == "" {
		return
	}
	message := formatDiscordTaskCompletion(taskTitle, output, errMsg)
	if err := s.sendDiscordMessage(channelID, messageID, message); err != nil {
		applog.Infof("[discord] send completion notification failed for channel=%s thread=%s user=%s: %v", channelID, threadID, userID, err)
	}
}

func (s *DiscordService) SendTaskCompletionNotification(ctx context.Context, task models.Task, output string, errMsg string) {
	if task.CreatedVia != models.TaskOriginDiscord && task.ID != "" && s.taskRepo != nil {
		loaded, err := s.taskRepo.GetByID(ctx, task.ID)
		if err == nil && loaded != nil {
			task = *loaded
		}
	}
	if task.CreatedVia != models.TaskOriginDiscord || task.Category == models.CategoryChat || !s.IsSendResponsesEnabled(ctx) || s.discordTaskContextRepo == nil {
		return
	}
	ctxRecord, err := s.discordTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || ctxRecord == nil {
		return
	}
	if err := s.sendDiscordMessage(ctxRecord.DiscordChannelID, ctxRecord.DiscordMessageID, formatDiscordTaskCompletion(task.Title, output, errMsg)); err != nil {
		applog.Infof("[discord] send completion notification failed for task=%s: %v", task.ID, err)
	}
}

func (s *DiscordService) SendChatResponse(ctx context.Context, task models.Task, output string, errMsg string) {
	if task.CreatedVia != models.TaskOriginDiscord || task.Category != models.CategoryChat || s.discordTaskContextRepo == nil || !s.IsSendResponsesEnabled(ctx) {
		return
	}
	ctxRecord, err := s.discordTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || ctxRecord == nil {
		return
	}
	message := ""
	if errMsg != "" {
		message = fmt.Sprintf("Error: %s", util.Truncate(errMsg, 220))
	} else {
		message = llmoutput.CleanChatOutputForDisplay(output)
		if message == "" {
			message = "(No response)"
		}
	}
	if err := s.editOrSendDiscordMessage(ctxRecord.DiscordChannelID, ctxRecord.DiscordMessageID, ctxRecord.DiscordMessageID, message); err != nil {
		applog.Infof("[discord] send chat response failed for task=%s: %v", task.ID, err)
	}
}

func (s *DiscordService) checkAuthorization(ctx context.Context, projectID, discordUserID string) bool {
	if s.discordAuthRepo == nil {
		return true
	}
	authorized, err := s.discordAuthRepo.IsAuthorizedAnywhere(ctx, discordUserID)
	if err != nil {
		if strings.TrimSpace(projectID) == "" {
			applog.Infof("[discord] auth check error for user=%s anywhere: %v", discordUserID, err)
		} else {
			applog.Infof("[discord] auth check error for user=%s project=%s: %v", discordUserID, projectID, err)
		}
		return false
	}
	return authorized
}

func (s *DiscordService) getActiveProject(ctx context.Context, userID string) string {
	key := strings.TrimSpace(userID)
	s.mu.RLock()
	if projectID, ok := s.userProjects[key]; ok {
		s.mu.RUnlock()
		return projectID
	}
	s.mu.RUnlock()

	if s.discordUserProjectRepo != nil {
		if saved, err := s.discordUserProjectRepo.GetUserProject(ctx, key); err == nil && saved != "" {
			s.mu.Lock()
			s.userProjects[key] = saved
			s.mu.Unlock()
			return saved
		} else if err != nil {
			applog.Infof("[discord] error loading persisted project for user=%s: %v", key, err)
		}
	}

	if s.projectRepo == nil {
		return ""
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		return ""
	}
	selected := fallbackProjectID(projects)
	s.mu.Lock()
	s.userProjects[key] = selected
	s.mu.Unlock()
	return selected
}

func (s *DiscordService) buildDiscordActionToolRuntime(projectID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildDiscordActionToolRuntimeForTask(projectID, "", actionCtx, collector)
}

func (s *DiscordService) buildDiscordActionToolRuntimeForTask(projectID, callerTaskID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	handlers := s.discordActionHandlersForTask(projectID, callerTaskID, actionCtx, collector)
	return buildFullChannelActionToolRuntime(chatcontrol.SurfaceDiscord, handlers)
}

type discordActionContext struct {
	ChannelID string
	ThreadID  string
	MessageID string
	UserID    string
}

func (s *DiscordService) discordActionHandlers(projectID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.discordActionHandlersForTask(projectID, "", actionCtx, collector)
}

func (s *DiscordService) discordActionHandlersForTask(projectID, callerTaskID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	var llmSvcForAutomation llmServiceForAutomation
	if s.llmSvc != nil {
		llmSvcForAutomation = s.llmSvc
	}
	prepareTaskCreation, createPreparedTask := buildAutomationTaskCreationCallbacks(callerTaskID, projectID, llmSvcForAutomation)
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID:          projectID,
		TaskSvc:            s.taskSvc,
		TaskRepo:           s.taskRepo,
		ExecRepo:           s.execRepo,
		ThreadInputRepo:    s.threadInputRepo,
		ExecutionStreamHub: s.executionStreamHub,
		LLMConfigRepo:      s.llmConfigRepo,
		Collector:          collector, PrepareTaskCreation: prepareTaskCreation,
		CreatePreparedTask: createPreparedTask,
		OnTasksCreated: func(ctx context.Context, _ []TaskCreationRequest, createdTasks []models.Task) error {
			for _, t := range createdTasks {
				if s.taskRepo != nil {
					if err := s.taskRepo.UpdateDiscordOrigin(ctx, t.ID); err != nil {
						applog.Infof("[discord] runtime create_task update discord origin failed for task=%s: %v", t.ID, err)
					}
				}
				if s.discordTaskContextRepo != nil {
					_ = s.discordTaskContextRepo.Upsert(ctx, &models.DiscordTaskContext{TaskID: t.ID, DiscordChannelID: actionCtx.ChannelID, DiscordThreadID: actionCtx.ThreadID, DiscordMessageID: actionCtx.MessageID, DiscordUserID: actionCtx.UserID})
				}
			}
			return nil
		},
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{ProjectID: projectID, TaskRepo: s.taskRepo, TaskGoalSvc: s.taskGoalSvc}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelThreadActionHandlers(channelThreadActionHandlerOptions{
		Platform:                 "discord",
		ProjectID:                projectID,
		Surface:                  chatcontrol.SurfaceDiscord,
		Source:                   models.TaskOriginDiscord,
		ActorID:                  actionCtx.UserID,
		TaskRepo:                 s.taskRepo,
		ExecRepo:                 s.execRepo,
		ThreadInputRepo:          s.threadInputRepo,
		LLMConfigRepo:            s.llmConfigRepo,
		SettingsRepo:             s.settingsRepo,
		CustomPersonalityRepo:    s.customPersonalityRepo,
		ChannelTaskRunner:        s.channelTaskRunner,
		QueuedTaskThreadPromoter: s.queuedTaskThreadPromoter,
		CompleteExecution:        channelCompletionFunc("discord", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
		ChannelMessageRouter:     s.channelMessageRouter,
		ReplyContext:             ChannelReplyContext{Source: models.TaskOriginDiscord, DiscordChannelID: actionCtx.ChannelID, DiscordThreadID: actionCtx.ThreadID, DiscordMessageID: actionCtx.MessageID, DiscordUserID: actionCtx.UserID},
		NewQueuedInput: func(_ *models.Task, runExecutionID, agentID string) *models.ThreadInput {
			return &models.ThreadInput{DiscordChannelID: actionCtx.ChannelID, DiscordThreadID: actionCtx.ThreadID, DiscordMessageID: actionCtx.MessageID, DiscordUserID: actionCtx.UserID}
		},
		FilterHistory: filterDiscordChatHistory,
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID:          projectID,
		CallerTaskID:       callerTaskID,
		TaskRepo:           s.taskRepo,
		ScheduleRepo:       s.scheduleRepo,
		AutomationGraphSvc: s.automationGraphSvc,
		AutomationDraftSvc: s.automationDraftSvc,
		AutomationCompiler: s.automationCompiler,
		WorkerSvc:          s.workerSvc,
		LLMConfigRepo:      s.llmConfigRepo, AgentRepo: s.agentRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		AlertSvc:              s.alertSvc,
		UsageAnalyticsSvc:     usageAnalyticsServiceFromRepos(s.usageAnalyticsSvc, s.execRepo, s.llmConfigRepo),
		UpcomingSvc:           s.upcomingSvc,
		DiscordStatus:         s.GetConnectionStatus,
		DiscordAuthRepo:       s.discordAuthRepo,
		EmailStatus:           s.emailStatus,
		EmailAuthRepo:         s.emailAuthRepo,
		WebhookRepo:           s.webhookRepo,
		ChannelTargets:        channelTargetsFromRouter(s.channelMessageRouter),
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:     projectID,
		ProjectRepo:   s.projectRepo,
		ProjectSvc:    s.projectSvc,
		LLMConfigRepo: s.llmConfigRepo,
		WorkerSvc:     s.workerSvc,
		CreateProject: CreateGitHubProjectRuntimeOptions{
			ProjectSvc:                 s.projectSvc,
			GitHubSvc:                  s.githubProjectSvc,
			MemorySvc:                  s.memorySvc,
			AgentLibraryMaintenanceSvc: s.agentLibraryMaintenanceSvc,
		},
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			if !s.checkAuthorization(ctx, project.ID, actionCtx.UserID) {
				return fmt.Errorf("Discord user %q is not authorized to use project %q", actionCtx.UserID, project.Name)
			}
			return s.setActiveProject(ctx, actionCtx.UserID, project.ID)
		},
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
		ChannelDisplayName: "Discord",
		ProjectID:          projectID,
		ProjectRepo:        s.projectRepo,
	}))
	return handlers
}

func (s *DiscordService) setActiveProject(ctx context.Context, userID, projectID string) error {
	key := strings.TrimSpace(userID)
	if s.discordUserProjectRepo != nil {
		if err := s.discordUserProjectRepo.SetUserProject(ctx, key, projectID); err != nil {
			applog.Infof("[discord] persist active project failed for user=%s: %v", key, err)
			return fmt.Errorf("persist failed: %w", err)
		}
	}
	s.mu.Lock()
	s.userProjects[key] = projectID
	s.mu.Unlock()
	return nil
}

func (s *DiscordService) SendOutboundMessage(ctx context.Context, channelID, threadID, text string) SendMessageResult {
	_ = ctx
	channelID = strings.TrimSpace(channelID)
	threadID = strings.TrimSpace(threadID)
	if channelID == "" {
		return SendMessageResult{OK: false, Platform: "discord", Error: "discord channel id is required"}
	}
	if strings.TrimSpace(text) == "" {
		return SendMessageResult{OK: false, Platform: "discord", Target: formatResolvedMessageTarget("discord", channelID, threadID), Error: "message is required"}
	}
	messageID, err := s.sendDiscordOutboundMessageWithID(channelID, threadID, text)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "discord", Target: formatResolvedMessageTarget("discord", channelID, threadID), Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "discord", Target: formatResolvedMessageTarget("discord", channelID, threadID), MessageID: messageID}
}

func (s *DiscordService) SendOutboundDirectMessage(ctx context.Context, userID, text string) SendMessageResult {
	_ = ctx
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return SendMessageResult{OK: false, Platform: "discord", Error: "discord user id is required"}
	}
	if strings.TrimSpace(text) == "" {
		return SendMessageResult{OK: false, Platform: "discord", Target: formatResolvedMessageTarget("discord", userID, ""), Error: "message is required"}
	}
	channelID, err := s.openDiscordDirectMessage(userID)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "discord", Target: formatResolvedMessageTarget("discord", userID, ""), Error: err.Error()}
	}
	messageID, err := s.sendDiscordMessageWithID(channelID, "", text)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "discord", Target: formatResolvedMessageTarget("discord", userID, ""), Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "discord", Target: formatResolvedMessageTarget("discord", userID, ""), MessageID: messageID}
}

func (s *DiscordService) openDiscordDirectMessage(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("discord user id is required")
	}
	if s.createDMChannelFunc != nil {
		return s.createDMChannelFunc(userID)
	}
	s.mu.RLock()
	session := s.session
	s.mu.RUnlock()
	if session == nil {
		botToken := strings.TrimSpace(s.getSetting(context.Background(), DiscordSettingBotToken))
		if botToken == "" {
			return "", fmt.Errorf("discord bot token is not configured")
		}
		var err error
		session, err = discordgo.New("Bot " + botToken)
		if err != nil {
			return "", err
		}
	}
	channel, err := session.UserChannelCreate(userID)
	if err != nil {
		return "", fmt.Errorf("open discord direct message: %w", err)
	}
	if channel == nil || strings.TrimSpace(channel.ID) == "" {
		return "", fmt.Errorf("open discord direct message: missing channel id")
	}
	return strings.TrimSpace(channel.ID), nil
}

func (s *DiscordService) sendDiscordMessage(channelID, messageID, text string) error {
	_, err := s.sendDiscordMessageWithID(channelID, messageID, text)
	return err
}

func (s *DiscordService) sendDiscordOutboundMessageWithID(channelID, threadID, text string) (string, error) {
	destinationID := strings.TrimSpace(channelID)
	if strings.TrimSpace(threadID) != "" {
		destinationID = strings.TrimSpace(threadID)
	}
	return s.sendDiscordMessageWithID(destinationID, "", text)
}

func (s *DiscordService) sendDiscordMessageWithID(channelID, messageID, text string) (string, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return "", nil
	}
	if s.sendMessageFunc != nil {
		return s.sendMessageFunc(channelID, messageID, text)
	}
	s.mu.RLock()
	session := s.session
	s.mu.RUnlock()
	if session == nil {
		botToken := strings.TrimSpace(s.getSetting(context.Background(), DiscordSettingBotToken))
		if botToken == "" {
			return "", fmt.Errorf("discord bot token is not configured")
		}
		var err error
		session, err = discordgo.New("Bot " + botToken)
		if err != nil {
			return "", err
		}
	}
	chunks := splitDiscordMessage(text)
	var firstID string
	for _, chunk := range chunks {
		msg, err := session.ChannelMessageSendReply(channelID, chunk, &discordgo.MessageReference{MessageID: messageID, ChannelID: channelID})
		if err != nil {
			msg, err = session.ChannelMessageSend(channelID, chunk)
		}
		if err != nil {
			return firstID, fmt.Errorf("send discord message: %w", err)
		}
		if firstID == "" && msg != nil {
			firstID = msg.ID
		}
	}
	return firstID, nil
}

func (s *DiscordService) editOrSendDiscordMessage(channelID, editMessageID, replyMessageID, text string) error {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	if editMessageID != "" && s.sendMessageFunc == nil {
		s.mu.RLock()
		session := s.session
		s.mu.RUnlock()
		if session != nil {
			chunk := text
			if len(chunk) > discordMessageLimit {
				chunk = chunk[:discordMessageLimit-3] + "..."
			}
			if _, err := session.ChannelMessageEdit(channelID, editMessageID, chunk); err == nil {
				return nil
			}
		}
	}
	return s.sendDiscordMessage(channelID, replyMessageID, text)
}

func splitDiscordMessage(text string) []string { return splitMessage(text, discordMessageLimit) }
func formatDiscordTaskCompletion(taskTitle, output, errMsg string) string {
	if errMsg != "" {
		return fmt.Sprintf("Task failed: %s\n\n%s", taskTitle, util.Truncate(errMsg, 500))
	}
	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		cleaned = "(No output)"
	}
	return fmt.Sprintf("Task completed: %s\n\n%s", taskTitle, util.Truncate(cleaned, 3500))
}
func filterDiscordChatHistory(executions []models.Execution, currentExecID string) []models.Execution {
	return filterSlackChatHistory(executions, currentExecID)
}
func sanitizeDiscordText(text, botID string) string {
	cleaned := discordMentionRegex.ReplaceAllString(text, "")
	if botID != "" {
		cleaned = strings.ReplaceAll(cleaned, "<@"+botID+">", "")
		cleaned = strings.ReplaceAll(cleaned, "<@!"+botID+">", "")
	}
	return strings.TrimSpace(cleaned)
}
func discordMentionsBot(mentions []*discordgo.User, botID string) bool {
	if botID == "" {
		return false
	}
	for _, user := range mentions {
		if user != nil && user.ID == botID {
			return true
		}
	}
	return false
}
func discordThreadID(msg *discordgo.Message) string {
	if msg == nil || msg.Thread == nil {
		return ""
	}
	return msg.Thread.ID
}
func discordDisplayName(user *discordgo.User) string {
	if user == nil {
		return ""
	}
	if user.GlobalName != "" {
		return user.GlobalName
	}
	return user.Username
}

func discordIncomingAttachmentsFromMessage(attachments []*discordgo.MessageAttachment) []discordIncomingAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]discordIncomingAttachment, 0, len(attachments))
	for _, att := range attachments {
		if att == nil {
			continue
		}
		out = append(out, discordIncomingAttachment{
			ID:          strings.TrimSpace(att.ID),
			FileName:    strings.TrimSpace(att.Filename),
			ContentType: strings.TrimSpace(att.ContentType),
			Size:        att.Size,
			URL:         strings.TrimSpace(att.URL),
			ProxyURL:    strings.TrimSpace(att.ProxyURL),
		})
	}
	return out
}

func discordAttachmentPrompt(attachments []*discordgo.MessageAttachment) string {
	names := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if att != nil && att.Filename != "" {
			names = append(names, att.Filename)
		}
	}
	if len(names) == 0 {
		return "User sent an attachment."
	}
	return "User sent attachment(s): " + strings.Join(names, ", ")
}

func (s *DiscordService) downloadDiscordAttachments(ctx context.Context, files []discordIncomingAttachment) (string, []models.Attachment, []models.ChatAttachment, error) {
	chatAttachments, err := s.downloadDiscordFiles(ctx, files)
	if err != nil {
		return "", nil, nil, err
	}
	attachmentContext, imageAttachments := discordAttachmentContextAndImages(chatAttachments)
	return attachmentContext, imageAttachments, chatAttachments, nil
}

func discordAttachmentContextAndImages(chatAttachments []models.ChatAttachment) (string, []models.Attachment) {
	return channelChatAttachmentContextAndImages(chatAttachments, discordMaxTextFileSize)
}

func discordIncomingAttachmentsRequireVision(files []discordIncomingAttachment) bool {
	for _, f := range files {
		fileName := discordSafeFileName(f)
		mediaType := discordIncomingFileMediaType(f, fileName)
		if isChannelChatImageMediaType(mediaType) {
			return true
		}
		if (mediaType == "" || mediaType == "application/octet-stream") && (strings.TrimSpace(f.URL) != "" || strings.TrimSpace(f.ProxyURL) != "") {
			return true
		}
	}
	return false
}

func (s *DiscordService) downloadDiscordFiles(ctx context.Context, files []discordIncomingAttachment) ([]models.ChatAttachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > discordMaxFilesPerMsg {
		return nil, fmt.Errorf("too many files (%d, max %d)", len(files), discordMaxFilesPerMsg)
	}
	tmpDir, err := os.MkdirTemp("", "discord-attachment-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	attachments := make([]models.ChatAttachment, 0, len(files))
	for _, f := range files {
		if f.Size > discordMaxFileSize {
			return nil, fmt.Errorf("file %q too large (%d bytes, max %d)", discordFileDisplayName(f), f.Size, discordMaxFileSize)
		}
		fileName := discordSafeFileName(f)
		mediaType := discordIncomingFileMediaType(f, fileName)
		destPath, mediaType, err := s.downloadDiscordFileCandidate(ctx, tmpDir, fileName, mediaType, f)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(destPath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat file %q: %w", fileName, err)
		}
		absPath, err := filepath.Abs(destPath)
		if err != nil {
			absPath = destPath
		}
		attachments = append(attachments, models.ChatAttachment{
			FileName:  fileName,
			FilePath:  absPath,
			MediaType: mediaType,
			FileSize:  info.Size(),
		})
		applog.Infof("[discord] downloaded attachment file=%s size=%d mime=%s path=%s", fileName, info.Size(), mediaType, absPath)
	}
	cleanup = false
	return attachments, nil
}

func (s *DiscordService) downloadDiscordFileCandidate(ctx context.Context, tmpDir, fileName, mediaType string, f discordIncomingAttachment) (string, string, error) {
	candidates := discordAttachmentDownloadURLs(f)
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("file %q has no download URL", discordFileDisplayName(f))
	}
	var lastErr error
	for _, candidateURL := range candidates {
		destPath := filepath.Join(tmpDir, uniqueChannelChatTempFilename(tmpDir, fileName))
		dest, err := os.Create(destPath)
		if err != nil {
			return "", "", fmt.Errorf("failed to create file: %w", err)
		}
		err = s.downloadDiscordFile(ctx, candidateURL, dest)
		closeErr := dest.Close()
		if err != nil {
			_ = os.Remove(destPath)
			lastErr = fmt.Errorf("failed to download file %q: %w", fileName, err)
			continue
		}
		if closeErr != nil {
			_ = os.Remove(destPath)
			return "", "", fmt.Errorf("failed to save file %q: %w", fileName, closeErr)
		}
		normalizedMediaType, err := validateChannelChatDownloadedImageFile(destPath, fileName, mediaType, "discord")
		if err == nil {
			if normalizedMediaType != "" {
				mediaType = normalizedMediaType
			}
			return destPath, mediaType, nil
		}
		_ = os.Remove(destPath)
		lastErr = err
		if !isChannelChatImageMediaType(mediaType) {
			break
		}
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("failed to download file %q", fileName)
}

func (s *DiscordService) downloadDiscordFile(ctx context.Context, downloadURL string, writer io.Writer) error {
	parsed, err := url.Parse(strings.TrimSpace(downloadURL))
	if err != nil {
		return err
	}
	if err := validateDiscordAttachmentURL(parsed); err != nil {
		return err
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	downloadClient := *client
	downloadClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	currentURL := parsed
	for redirects := 0; redirects <= discordMaxRedirects; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "OpenVibely Discord file downloader")
		resp, err := downloadClient.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			nextURL, err := discordRedirectLocation(currentURL.String(), resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			if err := validateDiscordAttachmentURL(nextURL); err != nil {
				return fmt.Errorf("discord attachment download redirected to invalid target: %w", err)
			}
			currentURL = nextURL
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("discord attachment download returned HTTP %d", resp.StatusCode)
		}
		n, err := io.Copy(writer, io.LimitReader(resp.Body, discordMaxFileSize+1))
		if err != nil {
			return err
		}
		if n > discordMaxFileSize {
			return fmt.Errorf("discord attachment download exceeded max size %d bytes", discordMaxFileSize)
		}
		return nil
	}
	return fmt.Errorf("discord attachment download exceeded redirect limit")
}

func discordRedirectLocation(currentURL, location string) (*url.URL, error) {
	if strings.TrimSpace(location) == "" {
		return nil, fmt.Errorf("discord attachment download redirect missing Location header")
	}
	base, err := url.Parse(currentURL)
	if err != nil {
		return nil, err
	}
	next, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(next), nil
}

func validateDiscordAttachmentURL(parsed *url.URL) error {
	if parsed == nil {
		return fmt.Errorf("discord attachment URL is empty")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("discord attachment URL scheme %q is not allowed", parsed.Scheme)
	}
	if !discordTrustedAttachmentHost(parsed.Hostname()) {
		return fmt.Errorf("discord attachment URL host %q is not trusted", parsed.Host)
	}
	return nil
}

func discordAttachmentDownloadURLs(f discordIncomingAttachment) []string {
	seen := make(map[string]bool)
	var urls []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		urls = append(urls, raw)
	}
	add(f.URL)
	add(f.ProxyURL)
	return urls
}

func discordTrustedAttachmentHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "cdn.discordapp.com" || host == "media.discordapp.net"
}

func (s *DiscordService) saveChatAttachmentsToPendingSession(attachments []models.ChatAttachment) (string, error) {
	return saveChannelChatAttachmentsToPendingSession(s.uploadsDir, "discord-attachment", attachments)
}

func (s *DiscordService) linkAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment) ([]models.ChatAttachment, error) {
	return linkChannelChatAttachmentsToExecution(ctx, execID, attachments, channelChatAttachmentLinkOptions{
		Platform:     "discord",
		UploadsDir:   s.uploadsDir,
		Repo:         s.chatAttachmentRepo,
		FallbackName: "discord-attachment",
	})
}

func cleanupDiscordAttachmentSourceDirs(attachments []models.ChatAttachment) {
	cleanupChannelChatAttachmentSourceDirs(attachments)
}

func discordImageAttachmentsFromChatAttachments(chatAttachments []models.ChatAttachment) []models.Attachment {
	if len(chatAttachments) == 0 {
		return nil
	}
	imageAttachments := make([]models.Attachment, 0, len(chatAttachments))
	for _, att := range chatAttachments {
		if !isChannelChatImageMediaType(att.MediaType) {
			continue
		}
		imageAttachments = append(imageAttachments, models.Attachment{
			FileName:  att.FileName,
			FilePath:  att.FilePath,
			MediaType: att.MediaType,
			FileSize:  att.FileSize,
		})
	}
	return imageAttachments
}

func discordSafeFileName(f discordIncomingAttachment) string {
	name := strings.TrimSpace(f.FileName)
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) || name == "" {
		if strings.TrimSpace(f.ID) != "" {
			name = "discord-" + strings.TrimSpace(f.ID)
		} else {
			name = "discord-attachment"
		}
	}
	return name
}

func discordFileDisplayName(f discordIncomingAttachment) string {
	return discordSafeFileName(f)
}

func discordIncomingFileMediaType(f discordIncomingAttachment, fileName string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(f.ContentType, ";")[0]))
	if mediaType == "" || mediaType == "application/octet-stream" {
		return mediaTypeFromSlackFilename(fileName)
	}
	return mediaType
}

func discordIsDM(sess *discordgo.Session, channelID, guildID string) bool {
	if guildID == "" {
		return true
	}
	if sess == nil {
		return false
	}
	ch, err := sess.State.Channel(channelID)
	if err != nil || ch == nil {
		ch, _ = sess.Channel(channelID)
	}
	return ch != nil && ch.Type == discordgo.ChannelTypeDM
}
func discordParentChannelID(sess *discordgo.Session, channelID string) string {
	if sess == nil || strings.TrimSpace(channelID) == "" {
		return ""
	}
	ch, err := sess.State.Channel(channelID)
	if err != nil || ch == nil {
		ch, _ = sess.Channel(channelID)
	}
	if ch == nil {
		return ""
	}
	return strings.TrimSpace(ch.ParentID)
}
func (s *DiscordService) requiresMentionForMessage(ctx context.Context, msg discordIncomingMessage) bool {
	return !msg.IsDM
}
func (s *DiscordService) botUserID(ctx context.Context, sess *discordgo.Session) string {
	if sess != nil && sess.State != nil && sess.State.User != nil && sess.State.User.ID != "" {
		return sess.State.User.ID
	}
	return strings.TrimSpace(s.getSetting(ctx, DiscordSettingBotUserID))
}
func (s *DiscordService) getSetting(ctx context.Context, key string) string {
	if s.settingsRepo == nil {
		return ""
	}
	val, _ := s.settingsRepo.Get(ctx, key)
	return val
}
func (s *DiscordService) setSetting(ctx context.Context, key, value string) error {
	if s.settingsRepo == nil {
		return nil
	}
	return s.settingsRepo.Set(ctx, key, value)
}
