package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const (
	SlackSettingClientID         = "slack_client_id"
	SlackSettingClientSecret     = "slack_client_secret"
	SlackSettingAppToken         = "slack_app_token"
	SlackSettingBotToken         = "slack_bot_token"
	SlackSettingBotTokenOverride = "slack_bot_token_override"
	SlackSettingBotTokenSource   = "slack_bot_token_source"
	SlackSettingBotUserID        = "slack_bot_user_id"
	SlackSettingTeamID           = "slack_team_id"
	SlackSettingTeamName         = "slack_team_name"
	SlackSettingConnectedAt      = "slack_connected_at"
	SlackSettingOAuthState       = "slack_oauth_state"
	SlackSettingSendResponses    = "slack_send_responses"

	SlackBotTokenSourceOAuth  = "oauth"
	SlackBotTokenSourceManual = "manual"

	defaultSlackAPIBaseURL  = "https://slack.com/api"
	slackPreACKTimeout      = 2500 * time.Millisecond
	slackChatHistoryLimit   = 50
	slackMaxFileSize        = 10 << 20
	slackMaxTextFileSize    = 100 * 1024
	slackMaxFilesPerMessage = 3
)

var slackMentionRegex = regexp.MustCompile(`<@[^>]+>`)

type SlackConnectionStatus struct {
	Configured bool
	Connected  bool
	Running    bool

	TeamID    string
	TeamName  string
	BotUserID string

	HasClientID         bool
	HasClientSecret     bool
	HasAppToken         bool
	HasBotToken         bool
	HasBotTokenOverride bool
	BotTokenSource      string
}

// SlackService manages Slack OAuth, Socket Mode event processing, and
// Slack-origin task completion notifications.
type SlackService struct {
	settingsRepo             *repository.SettingsRepo
	projectRepo              *repository.ProjectRepo
	llmConfigRepo            *repository.LLMConfigRepo
	taskRepo                 *repository.TaskRepo
	execRepo                 *repository.ExecutionRepo
	scheduleRepo             *repository.ScheduleRepo
	chatAttachmentRepo       *repository.ChatAttachmentRepo
	taskSvc                  *TaskService
	taskGoalSvc              *TaskGoalService
	llmSvc                   *LLMService
	workerSvc                *WorkerService
	automationGraphSvc       *AutomationGraphService
	slackUserProjectRepo     *repository.SlackUserProjectRepo
	slackTaskContextRepo     *repository.SlackTaskContextRepo
	slackInboundReceiptRepo  *repository.SlackInboundReceiptRepo
	threadInputRepo          *repository.ThreadInputRepo
	customPersonalityRepo    *repository.CustomPersonalityRepo
	slackAuthRepo            *repository.SlackAuthRepo
	agentRepo                *repository.AgentRepo
	chatBroadcaster          *events.ChatBroadcaster
	executionStreamHub       *events.ExecutionStreamHub
	queuedTurnPromoter       func(projectID string)
	queuedTaskThreadPromoter func(taskID string)
	channelChatRunner        ChannelChatRunner
	channelTaskRunner        ChannelTaskRunner
	alertSvc                 *AlertService
	channelMessageRouter     *ChannelMessageRouter
	uploadsDir               string

	httpClient   *http.Client
	oauthBaseURL string

	mu                             sync.RWMutex
	botClient                      *slack.Client
	socketClient                   *socketmode.Client
	running                        bool
	ctx                            context.Context
	cancel                         context.CancelFunc
	userProjects                   map[string]string
	processedMessageEvents         map[string]time.Time
	processingMessageEvents        map[string]struct{}
	cleanupMessageEvents           map[string]struct{}
	postMessageFn                  func(channelID, threadTS, text string) (string, error)
	openConversationFn             func(userID string) (string, error)
	processIncomingMessageFn       func(msg slackIncomingMessage)
	processIncomingMessageResultFn func(msg slackIncomingMessage) bool
	ackSocketRequestFn             func(*socketmode.Client, socketmode.Request)
	createQueuedInputFn            func(context.Context, *models.ThreadInput) (bool, error)
	createExecutionFn              func(context.Context, *models.Execution) (bool, error)
	createFirstTurnExecutionFn     func(context.Context, repository.SQLExecutor, *models.Execution) error
	deleteProvisionalTaskFn        func(context.Context, string) error
	downloadSlackAttachmentsFn     func(context.Context, []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error)
	savePendingAttachmentsFn       func([]models.ChatAttachment) (string, error)
	cleanupAttachmentSourcesFn     func([]models.ChatAttachment)
	preACKTimeout                  time.Duration
	cleanupRetryDelay              time.Duration
}

func NewSlackService(
	settingsRepo *repository.SettingsRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	execRepo *repository.ExecutionRepo,
	scheduleRepo *repository.ScheduleRepo,
	taskSvc *TaskService,
	llmSvc *LLMService,
	workerSvc *WorkerService,
	slackUserProjectRepo *repository.SlackUserProjectRepo,
	slackTaskContextRepo *repository.SlackTaskContextRepo,
	slackAuthRepo *repository.SlackAuthRepo,
) *SlackService {
	return &SlackService{
		settingsRepo:         settingsRepo,
		projectRepo:          projectRepo,
		llmConfigRepo:        llmConfigRepo,
		taskRepo:             taskRepo,
		execRepo:             execRepo,
		scheduleRepo:         scheduleRepo,
		taskSvc:              taskSvc,
		llmSvc:               llmSvc,
		workerSvc:            workerSvc,
		slackUserProjectRepo: slackUserProjectRepo,
		slackTaskContextRepo: slackTaskContextRepo,
		slackAuthRepo:        slackAuthRepo,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		oauthBaseURL:            defaultSlackAPIBaseURL,
		uploadsDir:              "uploads",
		userProjects:            make(map[string]string),
		processedMessageEvents:  make(map[string]time.Time),
		processingMessageEvents: make(map[string]struct{}),
		cleanupMessageEvents:    make(map[string]struct{}),
		preACKTimeout:           slackPreACKTimeout,
		cleanupRetryDelay:       100 * time.Millisecond,
	}
}

func (s *SlackService) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	s.customPersonalityRepo = repo
}

func (s *SlackService) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	s.chatBroadcaster = cb
}

func (s *SlackService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
}

func (s *SlackService) SetThreadInputRepo(repo *repository.ThreadInputRepo) {
	s.threadInputRepo = repo
}

func (s *SlackService) SetSlackInboundReceiptRepo(repo *repository.SlackInboundReceiptRepo) {
	s.slackInboundReceiptRepo = repo
}

func (s *SlackService) SetChatAttachmentRepo(repo *repository.ChatAttachmentRepo) {
	s.chatAttachmentRepo = repo
}

func (s *SlackService) SetUploadsDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	s.uploadsDir = dir
}

func (s *SlackService) SetAgentRepo(repo *repository.AgentRepo) {
	s.agentRepo = repo
}

func (s *SlackService) SetQueuedTurnPromoter(promoter func(projectID string)) {
	s.queuedTurnPromoter = promoter
}

func (s *SlackService) SetQueuedTaskThreadPromoter(promoter func(taskID string)) {
	s.queuedTaskThreadPromoter = promoter
}

func (s *SlackService) SetChannelChatRunner(runner ChannelChatRunner) {
	s.channelChatRunner = runner
}

func (s *SlackService) SetChannelTaskRunner(runner ChannelTaskRunner) {
	s.channelTaskRunner = runner
}

func (s *SlackService) SetAlertService(svc *AlertService) {
	s.alertSvc = svc
}

func (s *SlackService) SetAutomationGraphService(svc *AutomationGraphService) {
	s.automationGraphSvc = svc
}

func (s *SlackService) SetChannelMessageRouter(router *ChannelMessageRouter) {
	s.channelMessageRouter = router
}

// SetTaskGoalService injects the task goal service so Slack can execute
// goal-related chat-control tools with the same durable TaskGoalService
// behavior as web/API chat.
func (s *SlackService) SetTaskGoalService(svc *TaskGoalService) {
	s.taskGoalSvc = svc
}

func (s *SlackService) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *SlackService) Start() error {
	appToken := s.getSetting(context.Background(), SlackSettingAppToken)
	botToken := s.resolveBotToken(context.Background())
	if strings.TrimSpace(appToken) == "" || strings.TrimSpace(botToken) == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}

	botClient := slack.New(botToken, slack.OptionAppLevelToken(appToken), slack.OptionHTTPClient(s.httpClient))
	socketClient := socketmode.New(botClient)
	ctx, cancel := context.WithCancel(context.Background())

	s.botClient = botClient
	s.socketClient = socketClient
	s.ctx = ctx
	s.cancel = cancel
	s.running = true

	go s.runSocketLoop(ctx, socketClient)
	go socketClient.RunContext(ctx)

	applog.Infof("[slack] socket mode started")
	return nil
}

func (s *SlackService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	s.socketClient = nil
	s.botClient = nil
	applog.Infof("[slack] socket mode stopped")
}

func (s *SlackService) ReloadFromSettings(ctx context.Context) error {
	s.Stop()
	return s.Start()
}

func (s *SlackService) GetConnectionStatus(ctx context.Context) (SlackConnectionStatus, error) {
	clientID := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientID))
	clientSecret := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientSecret))
	appToken := strings.TrimSpace(s.getSetting(ctx, SlackSettingAppToken))
	oauthBotToken := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotToken))
	overrideBotToken := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotTokenOverride))
	botToken := strings.TrimSpace(s.resolveBotToken(ctx))
	teamID := strings.TrimSpace(s.getSetting(ctx, SlackSettingTeamID))
	teamName := strings.TrimSpace(s.getSetting(ctx, SlackSettingTeamName))
	botUserID := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotUserID))
	botTokenSource := s.getBotTokenSource(ctx)

	status := SlackConnectionStatus{
		HasClientID:         clientID != "",
		HasClientSecret:     clientSecret != "",
		HasAppToken:         appToken != "",
		HasBotToken:         botToken != "",
		HasBotTokenOverride: overrideBotToken != "",
		BotTokenSource:      botTokenSource,
		TeamID:              teamID,
		TeamName:            teamName,
		BotUserID:           botUserID,
		Running:             s.IsRunning(),
	}
	status.Configured = status.HasClientID || status.HasClientSecret || status.HasAppToken || status.HasBotToken
	status.Connected = oauthBotToken != "" || (botTokenSource == SlackBotTokenSourceManual && overrideBotToken != "")
	return status, nil
}

func (s *SlackService) ConnectURL(ctx context.Context, redirectURI string) (string, error) {
	clientID := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientID))
	clientSecret := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientSecret))
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("slack client id and client secret are required")
	}

	state, err := generateOAuthState()
	if err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	if err := s.setSetting(ctx, SlackSettingOAuthState, state); err != nil {
		return "", fmt.Errorf("save oauth state: %w", err)
	}

	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("scope", "app_mentions:read,channels:history,groups:history,im:history,mpim:history,chat:write,im:write,files:read")
	v.Set("redirect_uri", redirectURI)
	v.Set("state", state)
	return "https://slack.com/oauth/v2/authorize?" + v.Encode(), nil
}

func (s *SlackService) HandleOAuthCallback(ctx context.Context, code, state, redirectURI string) error {
	code = strings.TrimSpace(code)
	state = strings.TrimSpace(state)
	if code == "" || state == "" {
		return fmt.Errorf("missing oauth code or state")
	}

	expectedState := strings.TrimSpace(s.getSetting(ctx, SlackSettingOAuthState))
	if expectedState == "" || state != expectedState {
		return fmt.Errorf("invalid oauth state")
	}

	clientID := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientID))
	clientSecret := strings.TrimSpace(s.getSetting(ctx, SlackSettingClientSecret))
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("slack client id and client secret are required")
	}

	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", redirectURI)

	resp, err := s.httpClient.PostForm(strings.TrimRight(s.oauthBaseURL, "/")+"/oauth.v2.access", form)
	if err != nil {
		return fmt.Errorf("exchange oauth code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read oauth response: %w", err)
	}

	var payload struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
		BotUserID   string `json:"bot_user_id"`
		Team        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode oauth response: %w", err)
	}
	if !payload.OK {
		if payload.Error == "" {
			payload.Error = "oauth exchange failed"
		}
		return fmt.Errorf("slack oauth error: %s", payload.Error)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return fmt.Errorf("oauth response missing bot access token")
	}

	if err := s.setSetting(ctx, SlackSettingBotToken, strings.TrimSpace(payload.AccessToken)); err != nil {
		return err
	}
	_ = s.setSetting(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceOAuth)
	_ = s.setSetting(ctx, SlackSettingBotUserID, strings.TrimSpace(payload.BotUserID))
	_ = s.setSetting(ctx, SlackSettingTeamID, strings.TrimSpace(payload.Team.ID))
	_ = s.setSetting(ctx, SlackSettingTeamName, strings.TrimSpace(payload.Team.Name))
	_ = s.setSetting(ctx, SlackSettingConnectedAt, time.Now().UTC().Format(time.RFC3339))
	_ = s.setSetting(ctx, SlackSettingOAuthState, "")

	if strings.TrimSpace(s.getSetting(ctx, SlackSettingSendResponses)) == "" {
		_ = s.setSetting(ctx, SlackSettingSendResponses, "true")
	}

	return s.ReloadFromSettings(ctx)
}

func (s *SlackService) Disconnect(ctx context.Context) error {
	s.Stop()
	_ = s.setSetting(ctx, SlackSettingBotToken, "")
	_ = s.setSetting(ctx, SlackSettingBotTokenOverride, "")
	_ = s.setSetting(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceOAuth)
	_ = s.setSetting(ctx, SlackSettingBotUserID, "")
	_ = s.setSetting(ctx, SlackSettingTeamID, "")
	_ = s.setSetting(ctx, SlackSettingTeamName, "")
	_ = s.setSetting(ctx, SlackSettingConnectedAt, "")
	_ = s.setSetting(ctx, SlackSettingOAuthState, "")
	return nil
}

func (s *SlackService) TestConnection(ctx context.Context) error {
	botToken := strings.TrimSpace(s.resolveBotToken(ctx))
	if botToken == "" {
		return fmt.Errorf("slack bot token is not configured")
	}
	client := slack.New(botToken)
	if _, err := client.AuthTestContext(ctx); err != nil {
		return fmt.Errorf("auth test failed: %w", err)
	}
	return nil
}

func (s *SlackService) IsSendResponsesEnabled(ctx context.Context) bool {
	val := s.getSetting(ctx, SlackSettingSendResponses)
	if strings.TrimSpace(val) == "" {
		return true
	}
	return strings.TrimSpace(strings.ToLower(val)) != "false"
}

func (s *SlackService) SendTaskCompletionToThread(ctx context.Context, channelID, threadTS, taskTitle, output, errMsg, userID string) {
	if !s.IsSendResponsesEnabled(ctx) || channelID == "" {
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
	if err := s.sendSlackMessage(channelID, threadTS, message); err != nil {
		applog.Infof("[slack] send completion notification failed for channel=%s thread=%s user=%s: %v", channelID, threadTS, userID, err)
	}
}

func (s *SlackService) SendTaskCompletionNotification(ctx context.Context, task models.Task, output string, errMsg string) {
	if task.CreatedVia != models.TaskOriginSlack && task.ID != "" && s.taskRepo != nil {
		loaded, err := s.taskRepo.GetByID(ctx, task.ID)
		if err == nil && loaded != nil {
			task = *loaded
		}
	}
	if task.CreatedVia != models.TaskOriginSlack {
		return
	}
	if task.Category == models.CategoryChat {
		return
	}
	if !s.IsSendResponsesEnabled(ctx) {
		return
	}
	if s.slackTaskContextRepo == nil {
		return
	}
	ctxRecord, err := s.slackTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || ctxRecord == nil {
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

	if err := s.sendSlackMessage(ctxRecord.SlackChannelID, ctxRecord.SlackThreadTS, message); err != nil {
		applog.Infof("[slack] send completion notification failed for task=%s: %v", task.ID, err)
	}
}

func (s *SlackService) runSocketLoop(ctx context.Context, client *socketmode.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-client.Events:
			if !ok {
				return
			}
			s.handleSocketEvent(ctx, client, evt)
		}
	}
}

func (s *SlackService) handleSocketEvent(ctx context.Context, client *socketmode.Client, evt socketmode.Event) {
	if evt.Request == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithCancel(ctx)
	timeout := s.preACKTimeout
	if timeout <= 0 {
		timeout = slackPreACKTimeout
	}
	preACKTimer := time.AfterFunc(timeout, cancel)
	defer func() {
		preACKTimer.Stop()
		cancel()
	}()

	var ackOnce sync.Once
	ack := func() {
		ackOnce.Do(func() {
			preACKTimer.Stop()
			if s.ackSocketRequestFn != nil {
				s.ackSocketRequestFn(client, *evt.Request)
				return
			}
			if client != nil {
				_ = client.Ack(*evt.Request)
			}
		})
	}
	if evt.Type != socketmode.EventTypeEventsAPI {
		ack() // Unsupported request-bearing envelopes are terminally ignored.
		return
	}

	eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok || eventsAPIEvent.Type != slackevents.CallbackEvent {
		ack() // Malformed and unsupported envelopes are terminally ignored.
		return
	}

	teamID := strings.TrimSpace(eventsAPIEvent.TeamID)
	switch e := eventsAPIEvent.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		s.handleAppMention(requestCtx, teamID, *e, ack)
	case slackevents.AppMentionEvent:
		s.handleAppMention(requestCtx, teamID, e, ack)
	case *slackevents.MessageEvent:
		s.handleMessageEvent(requestCtx, teamID, *e, ack)
	case slackevents.MessageEvent:
		s.handleMessageEvent(requestCtx, teamID, e, ack)
	default:
		ack() // Events the integration intentionally does not support are terminal.
	}
}

func (s *SlackService) handleAppMention(ctx context.Context, teamID string, event slackevents.AppMentionEvent, ackFns ...func()) {
	ack := firstSlackAck(ackFns)
	if strings.TrimSpace(event.User) == "" {
		ack()
		return
	}
	if strings.TrimSpace(event.BotID) != "" {
		ack()
		return
	}
	botUserIDValue, err := s.getSettingValue(ctx, SlackSettingBotUserID)
	if err != nil {
		applog.Infof("[slack] bot user ID lookup failed: %v", err)
		return
	}
	botUserID := strings.TrimSpace(botUserIDValue)
	if botUserID != "" && strings.TrimSpace(event.User) == botUserID {
		ack()
		return
	}

	files := slackIncomingFilesFromAppMention(event)
	text := slackMessageTextOrAttachmentPrompt(sanitizeSlackText(event.Text), len(files) > 0)
	if text == "" {
		ack()
		return
	}

	threadTS := strings.TrimSpace(event.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = strings.TrimSpace(event.TimeStamp)
	}

	eventKey := slackIncomingEventKey(teamID, event.Channel, event.TimeStamp, event.User)
	if eventKey == "" {
		ack()
		return
	}

	s.processIncoming(ctx, slackIncomingMessage{
		TeamID:    teamID,
		ChannelID: strings.TrimSpace(event.Channel),
		ThreadTS:  threadTS,
		UserID:    strings.TrimSpace(event.User),
		Text:      text,
		Source:    "slack",
		EventKey:  eventKey,
		Files:     files,
	}, ack)
}

func (s *SlackService) handleMessageEvent(ctx context.Context, teamID string, event slackevents.MessageEvent, ackFns ...func()) {
	ack := firstSlackAck(ackFns)
	channelType := strings.TrimSpace(event.ChannelType)
	if strings.TrimSpace(event.User) == "" {
		ack()
		return
	}
	if strings.TrimSpace(event.BotID) != "" {
		ack()
		return
	}
	if subtype := strings.TrimSpace(event.SubType); subtype != "" && subtype != "file_share" {
		ack()
		return
	}
	botUserIDValue, err := s.getSettingValue(ctx, SlackSettingBotUserID)
	if err != nil {
		applog.Infof("[slack] bot user ID lookup failed: %v", err)
		return
	}
	botUserID := strings.TrimSpace(botUserIDValue)
	if botUserID != "" && strings.TrimSpace(event.User) == botUserID {
		ack()
		return
	}
	if channelType != "im" && !slackMessageMentionsBot(event, botUserID) {
		ack()
		return
	}

	files := slackIncomingFilesFromMessage(event.Message)
	text := slackMessageTextOrAttachmentPrompt(sanitizeSlackText(event.Text), len(files) > 0)
	if text == "" {
		ack()
		return
	}

	threadTS := strings.TrimSpace(event.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = strings.TrimSpace(event.TimeStamp)
	}
	eventTS := strings.TrimSpace(event.TimeStamp)
	if eventTS == "" && event.Message != nil {
		eventTS = strings.TrimSpace(event.Message.Timestamp)
	}

	eventKey := slackIncomingEventKey(teamID, event.Channel, eventTS, event.User)
	if eventKey == "" {
		ack()
		return
	}

	s.processIncoming(ctx, slackIncomingMessage{
		TeamID:    teamID,
		ChannelID: strings.TrimSpace(event.Channel),
		ThreadTS:  threadTS,
		UserID:    strings.TrimSpace(event.User),
		Text:      text,
		Source:    "slack",
		EventKey:  eventKey,
		Files:     files,
	}, ack)
}

type slackIncomingMessage struct {
	TeamID    string
	ChannelID string
	ThreadTS  string
	UserID    string
	Text      string
	Source    string
	EventKey  string
	Files     []slackIncomingFile
}

type slackIncomingFile struct {
	ID                 string
	Name               string
	Title              string
	Mimetype           string
	Size               int
	URLPrivate         string
	URLPrivateDownload string
	FileAccess         string
	Thumb360           string
	Thumb480           string
	Thumb720           string
	Thumb960           string
	Thumb1024          string
}

func firstSlackAck(ackFns []func()) func() {
	if len(ackFns) > 0 && ackFns[0] != nil {
		return ackFns[0]
	}
	return func() {}
}

func (s *SlackService) processIncoming(ctx context.Context, msg slackIncomingMessage, ack func()) {
	claimed, durableDuplicate := s.claimIncomingMessage(msg)
	if durableDuplicate {
		ack()
		return
	}
	if !claimed {
		return
	}
	if s.slackInboundReceiptRepo != nil && strings.TrimSpace(msg.EventKey) != "" {
		alreadyHandedOff, err := s.slackInboundReceiptRepo.Exists(ctx, msg.EventKey)
		if err != nil {
			applog.Infof("[slack] check durable event receipt failed key=%s: %v", msg.EventKey, err)
			s.finishIncomingMessage(msg, false)
			return
		}
		if alreadyHandedOff {
			s.finishIncomingMessage(msg, true)
			ack()
			return
		}
	}

	handedOff := false
	onDurableHandoff := func() {
		if handedOff {
			return
		}
		handedOff = true
		s.finishIncomingMessage(msg, true)
		ack()
	}
	if s.processIncomingMessageResultFn != nil {
		if s.processIncomingMessageResultFn(msg) {
			onDurableHandoff()
		}
	} else if s.processIncomingMessageFn != nil {
		s.processIncomingMessageFn(msg)
		onDurableHandoff()
	} else {
		s.processIncomingMessageWithHandoff(ctx, msg, onDurableHandoff, true)
	}
	if !handedOff {
		s.finishIncomingMessage(msg, false)
	}
}

const slackEventDedupeTTL = 10 * time.Minute

func slackIncomingEventKey(teamID, channelID, eventTS, userID string) string {
	channelID = strings.TrimSpace(channelID)
	eventTS = strings.TrimSpace(eventTS)
	userID = strings.TrimSpace(userID)
	if channelID == "" || eventTS == "" || userID == "" {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(teamID),
		channelID,
		eventTS,
		userID,
	}, "|")
}

func (s *SlackService) claimIncomingMessage(msg slackIncomingMessage) (claimed bool, durableDuplicate bool) {
	key := strings.TrimSpace(msg.EventKey)
	if key == "" {
		return true, false
	}

	now := time.Now()
	cutoff := now.Add(-slackEventDedupeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processedMessageEvents == nil {
		s.processedMessageEvents = make(map[string]time.Time)
	}
	if s.processingMessageEvents == nil {
		s.processingMessageEvents = make(map[string]struct{})
	}
	if processedAt, ok := s.processedMessageEvents[key]; ok && processedAt.After(cutoff) {
		applog.Infof("[slack] ignoring duplicate Slack message event key=%s", key)
		return false, true
	}
	if _, ok := s.processingMessageEvents[key]; ok {
		return false, false
	}
	for eventKey, processedAt := range s.processedMessageEvents {
		if processedAt.Before(cutoff) {
			delete(s.processedMessageEvents, eventKey)
		}
	}
	s.processingMessageEvents[key] = struct{}{}
	return true, false
}

func (s *SlackService) finishIncomingMessage(msg slackIncomingMessage, durable bool) {
	key := strings.TrimSpace(msg.EventKey)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !durable {
		if _, cleanupPending := s.cleanupMessageEvents[key]; cleanupPending {
			return
		}
		delete(s.processingMessageEvents, key)
		return
	}
	delete(s.processingMessageEvents, key)
	s.processedMessageEvents[key] = time.Now()
}

func (s *SlackService) beginIncomingMessageCleanup(msg slackIncomingMessage) {
	key := strings.TrimSpace(msg.EventKey)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleanupMessageEvents == nil {
		s.cleanupMessageEvents = make(map[string]struct{})
	}
	s.cleanupMessageEvents[key] = struct{}{}
}

func (s *SlackService) finishIncomingMessageCleanup(msg slackIncomingMessage) {
	key := strings.TrimSpace(msg.EventKey)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cleanupMessageEvents, key)
	delete(s.processingMessageEvents, key)
}

func (s *SlackService) cleanupProvisionalTaskUntilSuccess(msg slackIncomingMessage, taskID, reason string) {
	deleteTask := s.deleteProvisionalTaskFn
	if deleteTask == nil {
		deleteTask = s.taskRepo.Delete
	}
	delay := s.cleanupRetryDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	for {
		if err := deleteTask(context.Background(), taskID); err == nil {
			s.finishIncomingMessageCleanup(msg)
			return
		} else {
			applog.Infof("[slack] cleanup %s task failed task=%s; retrying: %v", reason, taskID, err)
		}
		time.Sleep(delay)
	}
}

func slackIncomingFilesFromAppMention(event slackevents.AppMentionEvent) []slackIncomingFile {
	files := slackIncomingFilesFromSlackFiles(event.Files)
	files = append(files, slackIncomingFilesFromAttachments(event.Attachments)...)
	files = append(files, slackIncomingFilesFromBlocks(event.Blocks)...)
	return dedupeSlackIncomingFiles(files)
}

func slackIncomingFilesFromSlackFiles(files []slack.File) []slackIncomingFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]slackIncomingFile, 0, len(files))
	for _, f := range files {
		out = append(out, slackIncomingFileFromSlackFile(f))
	}
	return out
}

func slackIncomingFileFromSlackFile(f slack.File) slackIncomingFile {
	return slackIncomingFile{
		ID:                 f.ID,
		Name:               f.Name,
		Title:              f.Title,
		Mimetype:           f.Mimetype,
		Size:               f.Size,
		URLPrivate:         f.URLPrivate,
		URLPrivateDownload: f.URLPrivateDownload,
		Thumb360:           f.Thumb360,
		Thumb480:           f.Thumb480,
		Thumb720:           f.Thumb720,
		Thumb960:           f.Thumb960,
		Thumb1024:          f.Thumb1024,
	}
}

func slackIncomingFilesFromMessage(msg *slack.Msg) []slackIncomingFile {
	if msg == nil {
		return nil
	}
	files := slackIncomingFilesFromSlackFiles(msg.Files)
	files = append(files, slackIncomingFilesFromAttachments(msg.Attachments)...)
	files = append(files, slackIncomingFilesFromBlocks(msg.Blocks)...)
	return dedupeSlackIncomingFiles(files)
}

func slackIncomingFilesFromAttachments(attachments []slack.Attachment) []slackIncomingFile {
	if len(attachments) == 0 {
		return nil
	}
	var files []slackIncomingFile
	for _, att := range attachments {
		name := strings.TrimSpace(att.Title)
		if name == "" {
			name = strings.TrimSpace(att.Fallback)
		}
		if slackIsTrustedFileURL(att.ImageURL) {
			files = append(files, slackIncomingFile{
				Name:       name,
				Title:      att.Title,
				Mimetype:   mediaTypeFromSlackFilename(name),
				Size:       att.ImageBytes,
				URLPrivate: att.ImageURL,
			})
		}
		if slackIsTrustedFileURL(att.ThumbURL) {
			files = append(files, slackIncomingFile{
				Name:       name,
				Title:      att.Title,
				Mimetype:   mediaTypeFromSlackFilename(name),
				Size:       att.ImageBytes,
				URLPrivate: att.ThumbURL,
			})
		}
		files = append(files, slackIncomingFilesFromBlocks(att.Blocks)...)
	}
	return files
}

func slackIncomingFilesFromBlocks(blocks slack.Blocks) []slackIncomingFile {
	if len(blocks.BlockSet) == 0 {
		return nil
	}
	var files []slackIncomingFile
	for _, block := range blocks.BlockSet {
		imageBlock, ok := block.(*slack.ImageBlock)
		if !ok || imageBlock == nil {
			continue
		}
		name := ""
		if imageBlock.Title != nil {
			name = strings.TrimSpace(imageBlock.Title.Text)
		}
		if name == "" {
			name = strings.TrimSpace(imageBlock.AltText)
		}
		if imageBlock.SlackFile != nil {
			file := slackIncomingFile{
				ID:       strings.TrimSpace(imageBlock.SlackFile.ID),
				Name:     name,
				Title:    name,
				Mimetype: mediaTypeFromSlackFilename(name),
			}
			if slackIsTrustedFileURL(imageBlock.SlackFile.URL) {
				file.URLPrivate = strings.TrimSpace(imageBlock.SlackFile.URL)
			}
			if file.ID != "" || file.URLPrivate != "" {
				files = append(files, file)
			}
			continue
		}
		if slackIsTrustedFileURL(imageBlock.ImageURL) {
			files = append(files, slackIncomingFile{
				Name:       name,
				Title:      name,
				Mimetype:   mediaTypeFromSlackFilename(name),
				URLPrivate: strings.TrimSpace(imageBlock.ImageURL),
			})
		}
	}
	return files
}

func dedupeSlackIncomingFiles(files []slackIncomingFile) []slackIncomingFile {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(files))
	out := make([]slackIncomingFile, 0, len(files))
	for _, f := range files {
		key := strings.TrimSpace(f.ID)
		if key == "" {
			key = strings.TrimSpace(f.URLPrivateDownload)
		}
		if key == "" {
			key = strings.TrimSpace(f.URLPrivate)
		}
		if key == "" {
			key = strings.TrimSpace(f.Name) + "|" + strings.TrimSpace(f.Title)
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, f)
	}
	return out
}

func (s *SlackService) recordQueuedAttachmentFailure(ctx context.Context, projectID, agentID string, msg slackIncomingMessage, msgText string) {
	if s.taskRepo == nil || s.execRepo == nil {
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Files) > 0})
		}
		return
	}
	task := &models.Task{
		ProjectID:  projectID,
		Title:      fmt.Sprintf("Slack %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Text, 47)),
		Prompt:     msg.Text,
		Status:     models.StatusPending,
		Category:   models.CategoryChat,
		AgentID:    &agentID,
		CreatedVia: models.TaskOriginSlack,
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		applog.Infof("[slack] create queued attachment failure task failed: %v", err)
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Files) > 0})
		}
		return
	}
	if s.slackTaskContextRepo != nil {
		if err := s.slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{
			TaskID:         task.ID,
			SlackTeamID:    msg.TeamID,
			SlackChannelID: msg.ChannelID,
			SlackThreadTS:  msg.ThreadTS,
			SlackUserID:    msg.UserID,
		}); err != nil {
			applog.Infof("[slack] create queued attachment failure context failed task=%s: %v", task.ID, err)
		}
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agentID, Status: models.ExecRunning, PromptSent: msg.Text}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		applog.Infof("[slack] create queued attachment failure execution failed task=%s: %v", task.ID, err)
		_ = s.taskRepo.Delete(ctx, task.ID)
		if s.chatBroadcaster != nil {
			s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, Message: msg.Text, Source: msg.Source, Queued: true, HasAttachments: len(msg.Files) > 0})
		}
		return
	}
	channelCompletionFunc("slack", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter)(ctx, exec.ID, task.ID, "", msgText, 0, 0)
	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: projectID, ExecID: exec.ID, TaskID: task.ID, Message: msg.Text, Source: msg.Source, HasAttachments: len(msg.Files) > 0})
	}
}

func (s *SlackService) processIncomingMessage(msg slackIncomingMessage) {
	s.processIncomingMessageWithHandoff(context.Background(), msg, func() {}, false)
}

func (s *SlackService) processIncomingMessageWithHandoff(parent context.Context, msg slackIncomingMessage, onDurableHandoff func(), asyncPreACKFailures bool) {
	msg.Text = slackMessageTextOrAttachmentPrompt(msg.Text, len(msg.Files) > 0)
	if msg.ChannelID == "" || msg.UserID == "" || strings.TrimSpace(msg.Text) == "" {
		onDurableHandoff() // Valid envelope with unusable message content is terminal.
		return
	}
	if s.taskRepo == nil || s.execRepo == nil || s.llmConfigRepo == nil || s.llmSvc == nil || s.taskSvc == nil || s.projectRepo == nil {
		applog.Infof("[slack] incoming message ignored: service dependencies are not fully configured")
		onDurableHandoff() // Configuration errors are not transient event persistence failures.
		return
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx := parent
	durableHandoff := onDurableHandoff
	notifyPreACKFailure := func(notify func()) {
		if notify == nil {
			return
		}
		if asyncPreACKFailures {
			go notify()
			return
		}
		notify()
	}
	start := time.Now()

	projectID, err := s.getActiveProject(ctx, msg.TeamID, msg.UserID)
	if err != nil {
		applog.Infof("[slack] active project lookup failed user=%s team=%s: %v", msg.UserID, msg.TeamID, err)
		return
	}
	if projectID == "" {
		durableHandoff()
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "No active project found. Please create a project first in the web UI.")
		return
	}
	authorized, authErr := s.checkAuthorizationResult(ctx, projectID, msg.UserID)
	if authErr != nil {
		applog.Infof("[slack] authorization lookup failed user=%s team=%s project=%s: %v", msg.UserID, msg.TeamID, projectID, authErr)
		return
	}
	if !authorized {
		durableHandoff()
		applog.Infof("[slack] unauthorized access blocked for user=%s team=%s project=%s", msg.UserID, msg.TeamID, projectID)
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "You are not authorized to use Slack access for this project. Contact the project owner to get access.")
		return
	}

	msg.Files = s.resolveSlackIncomingFilesForRouting(ctx, msg.Files)
	var recordAttachmentFailure func(context.Context, string, string)
	if !asyncPreACKFailures {
		recordAttachmentFailure = func(ctx context.Context, agentID, msgText string) {
			s.recordQueuedAttachmentFailure(ctx, projectID, agentID, msg, msgText)
		}
	}
	var createDurableFirstTurn func(context.Context, *models.Task, *models.Execution, []models.ChatAttachment) (bool, error)
	if s.createExecutionFn == nil && s.slackInboundReceiptRepo != nil && strings.TrimSpace(msg.EventKey) != "" {
		createDurableFirstTurn = func(ctx context.Context, task *models.Task, execution *models.Execution, attachments []models.ChatAttachment) (bool, error) {
			return s.slackInboundReceiptRepo.WithHandoff(ctx, msg.EventKey, func(exec repository.SQLExecutor) error {
				if err := s.taskRepo.CreateWithExecutor(ctx, exec, task); err != nil {
					return err
				}
				if s.slackTaskContextRepo != nil {
					if err := s.slackTaskContextRepo.UpsertWithExecutor(ctx, exec, &models.SlackTaskContext{
						TaskID:         task.ID,
						SlackTeamID:    msg.TeamID,
						SlackChannelID: msg.ChannelID,
						SlackThreadTS:  msg.ThreadTS,
						SlackUserID:    msg.UserID,
					}); err != nil {
						return err
					}
				}
				execution.TaskID = task.ID
				if s.createFirstTurnExecutionFn != nil {
					if err := s.createFirstTurnExecutionFn(ctx, exec, execution); err != nil {
						return err
					}
				} else if err := s.execRepo.CreateWithExecutor(ctx, exec, execution); err != nil {
					return err
				}
				if len(attachments) > 0 && s.chatAttachmentRepo == nil {
					return fmt.Errorf("chat attachment repository is unavailable")
				}
				for i := range attachments {
					attachments[i].ExecutionID = execution.ID
					if err := s.chatAttachmentRepo.CreateWithExecutor(ctx, exec, &attachments[i]); err != nil {
						return err
					}
				}
				return nil
			})
		}
	}
	runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:              "slack",
		ProjectID:             projectID,
		Message:               msg.Text,
		Source:                msg.Source,
		Surface:               chatcontrol.SurfaceSlack,
		HasAttachments:        len(msg.Files) > 0,
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
			if len(msg.Files) == 0 {
				return channelChatIngressDownloadResult{}, nil
			}
			attCtx, imgAtts, chatAtts, err := s.downloadSlackAttachmentsBounded(ctx, msg.Files)
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAtts}, err
		},
		IncomingAttachmentsNeedVision:     func() bool { return slackIncomingFilesRequireVision(msg.Files) },
		SavePendingAttachmentsWithContext: s.saveChatAttachmentsToPendingSessionBounded,
		CleanupAttachmentSources:          s.cleanupSlackAttachmentSourcesBounded,
		CleanupPendingSession:             s.cleanupSlackPendingSessionBounded,
		FindActiveExecution:               s.execRepo.FindLatestActiveChatExecution,
		RecordAttachmentFailure:           recordAttachmentFailure,
		NewQueuedInput: func() *models.ThreadInput {
			return &models.ThreadInput{SlackTeamID: msg.TeamID, SlackChannelID: msg.ChannelID, SlackThreadTS: msg.ThreadTS, SlackUserID: msg.UserID}
		},
		CreateQueuedInput: func(ctx context.Context, input *models.ThreadInput) (bool, error) {
			if s.createQueuedInputFn != nil {
				return s.createQueuedInputFn(ctx, input)
			}
			if s.threadInputRepo == nil {
				return false, fmt.Errorf("thread input repository is not configured")
			}
			if s.slackInboundReceiptRepo == nil || strings.TrimSpace(msg.EventKey) == "" {
				return false, s.threadInputRepo.CreateQueued(ctx, input)
			}
			return s.slackInboundReceiptRepo.WithHandoff(ctx, msg.EventKey, func(exec repository.SQLExecutor) error {
				return s.threadInputRepo.CreateQueuedWithExecutor(ctx, exec, input)
			})
		},
		OnAttachmentDownloadFailed: func(_ context.Context, msgText string) {
			notifyPreACKFailure(func() { _ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "⚠️ "+msgText) })
		},
		OnAttachmentStoreFailed: func(_ context.Context, msgText string) {
			notifyPreACKFailure(func() { _ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "⚠️ "+msgText) })
		},
		OnModelSelectionFailed: func(_ context.Context, err error) {
			notifyPreACKFailure(func() {
				_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, fmt.Sprintf("Error selecting model: %v", err))
			})
		},
		OnActiveLookupFailed: func(context.Context) {
			notifyPreACKFailure(func() {
				_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error checking active chat response. Please try again.")
			})
		},
		OnQueueFailure: func(context.Context) {
			notifyPreACKFailure(func() {
				_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error queueing your message. Please try again.")
			})
		},
		OnQueued: func(context.Context) {
			_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Queued. I'll send this after the current response finishes.")
		},
		OnDurableHandoff: durableHandoff,
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task:                      &models.Task{Title: fmt.Sprintf("Slack %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Text, 47)), CreatedVia: models.TaskOriginSlack},
			PrepareDurableAttachments: s.publishSlackFirstTurnAttachmentsBounded,
			CreateDurableFirstTurn:    createDurableFirstTurn,
			ReplyContext:              ChannelReplyContext{Source: models.TaskOriginSlack, SlackTeamID: msg.TeamID, SlackChannelID: msg.ChannelID, SlackThreadTS: msg.ThreadTS, SlackUserID: msg.UserID},
			CreateExecution: func(ctx context.Context, execution *models.Execution) (bool, error) {
				if s.createExecutionFn != nil {
					return s.createExecutionFn(ctx, execution)
				}
				if s.slackInboundReceiptRepo == nil || strings.TrimSpace(msg.EventKey) == "" {
					return false, s.execRepo.Create(ctx, execution)
				}
				return s.slackInboundReceiptRepo.WithHandoff(ctx, msg.EventKey, func(exec repository.SQLExecutor) error {
					return s.execRepo.CreateWithExecutor(ctx, exec, execution)
				})
			},
			RuntimeToolsForTask: func(taskID string) *llmcontracts.RuntimeTools {
				return s.buildSlackActionToolRuntimeForTask(projectID, taskID, slackActionContext{
					TeamID:    msg.TeamID,
					ChannelID: msg.ChannelID,
					ThreadTS:  msg.ThreadTS,
					UserID:    msg.UserID,
				}, nil)
			},
			ChannelChatRunner: s.channelChatRunner,
			CreateTaskContext: func(ctx context.Context, taskID string) error {
				if s.slackTaskContextRepo == nil {
					return nil
				}
				return s.slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{TaskID: taskID, SlackTeamID: msg.TeamID, SlackChannelID: msg.ChannelID, SlackThreadTS: msg.ThreadTS, SlackUserID: msg.UserID})
			},
			CleanupProvisionalTask: func(ctx context.Context, taskID, reason string) {
				if !asyncPreACKFailures {
					cleanupChannelChatProvisionalTask(ctx, "slack", s.taskRepo, taskID, reason)
					return
				}
				s.beginIncomingMessageCleanup(msg)
				go s.cleanupProvisionalTaskUntilSuccess(msg, taskID, reason)
			},
			CompleteExecution: channelCompletionFunc("slack", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
			LinkAttachments:   s.linkAttachmentsToExecution, AttachmentContextAndImages: slackAttachmentContextAndImages,
			ListChatHistory: func(ctx context.Context, projectID string) ([]models.Execution, error) {
				return s.execRepo.ListChatHistory(ctx, projectID, slackChatHistoryLimit)
			},
			FilterChatHistory: filterSlackChatHistory,
			OnTaskCreateFailure: func(context.Context) {
				notifyPreACKFailure(func() {
					_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error processing your message. Please try again.")
				})
			},
			OnTaskContextFailure: func(context.Context) {
				notifyPreACKFailure(func() {
					_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error processing your message. Please try again.")
				})
			},
			OnExecutionCreateFailure: func(context.Context) {
				notifyPreACKFailure(func() {
					_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error processing your message. Please try again.")
				})
			},
			OnAttachmentLinkFailure: func(_ context.Context, msgText string) {
				notifyPreACKFailure(func() {
					_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "⚠️ "+msgText)
				})
			},
			OnRunnerUnavailable: func(_ context.Context, msgText string, _ int) {
				_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, msgText)
			},
		},
	})
	return
}

type slackActionContext struct {
	TeamID    string
	ChannelID string
	ThreadTS  string
	UserID    string
}

func (s *SlackService) buildSlackActionToolRuntime(projectID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildSlackActionToolRuntimeForTask(projectID, "", actionCtx, collector)
}

func (s *SlackService) buildSlackActionToolRuntimeForTask(projectID, callerTaskID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	handlers := s.slackActionHandlersForTask(projectID, callerTaskID, actionCtx, collector)
	return buildFullChannelActionToolRuntime(chatcontrol.SurfaceSlack, handlers)
}

func (s *SlackService) slackActionHandlers(projectID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.slackActionHandlersForTask(projectID, "", actionCtx, collector)
}

func (s *SlackService) slackActionHandlersForTask(projectID, callerTaskID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
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
					if err := s.taskRepo.UpdateSlackOrigin(ctx, t.ID); err != nil {
						applog.Infof("[slack] runtime create_task update slack origin failed for task=%s: %v", t.ID, err)
					}
				}
				if s.slackTaskContextRepo != nil {
					_ = s.slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{TaskID: t.ID, SlackTeamID: actionCtx.TeamID, SlackChannelID: actionCtx.ChannelID, SlackThreadTS: actionCtx.ThreadTS, SlackUserID: actionCtx.UserID})
				}
			}
			return nil
		},
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{ProjectID: projectID, TaskRepo: s.taskRepo, TaskGoalSvc: s.taskGoalSvc}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelThreadActionHandlers(channelThreadActionHandlerOptions{
		Platform:                 "slack",
		ProjectID:                projectID,
		Surface:                  chatcontrol.SurfaceSlack,
		Source:                   models.TaskOriginSlack,
		ActorID:                  actionCtx.UserID,
		TaskRepo:                 s.taskRepo,
		ExecRepo:                 s.execRepo,
		ThreadInputRepo:          s.threadInputRepo,
		LLMConfigRepo:            s.llmConfigRepo,
		SettingsRepo:             s.settingsRepo,
		CustomPersonalityRepo:    s.customPersonalityRepo,
		ChannelTaskRunner:        s.channelTaskRunner,
		QueuedTaskThreadPromoter: s.queuedTaskThreadPromoter,
		CompleteExecution:        channelCompletionFunc("slack", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
		ChannelMessageRouter:     s.channelMessageRouter,
		ReplyContext:             ChannelReplyContext{Source: models.TaskOriginSlack, SlackTeamID: actionCtx.TeamID, SlackChannelID: actionCtx.ChannelID, SlackThreadTS: actionCtx.ThreadTS, SlackUserID: actionCtx.UserID},
		NewQueuedInput: func(_ *models.Task, runExecutionID, agentID string) *models.ThreadInput {
			return &models.ThreadInput{SlackTeamID: actionCtx.TeamID, SlackChannelID: actionCtx.ChannelID, SlackThreadTS: actionCtx.ThreadTS, SlackUserID: actionCtx.UserID}
		},
		FilterHistory: filterSlackChatHistory,
		ConfigureSendOptions: func(opts *channelTaskThreadSendOptions) {
			opts.OnBindQueuedInputSkipped = func(_ context.Context, task *models.Task, input *models.ThreadInput, err error) {
				applog.Infof("[slack] send_to_task task=%s input=%s active execution bind skipped: %v", task.ID, input.ID, err)
			}
			opts.OnPromotionRecheckSkipped = func(_ context.Context, task *models.Task, input *models.ThreadInput, err error) {
				applog.Infof("[slack] send_to_task task=%s input=%s promotion recheck skipped: %v", task.ID, input.ID, err)
			}
		},
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID:             projectID,
		CallerTaskID:          callerTaskID,
		TaskRepo:              s.taskRepo,
		ScheduleRepo:          s.scheduleRepo,
		AutomationGraphSvc:    s.automationGraphSvc,
		WorkerSvc:             s.workerSvc,
		LLMConfigRepo:         s.llmConfigRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		AlertSvc:              s.alertSvc,
		SlackStatus:           s.GetConnectionStatus,
		SlackAuthRepo:         s.slackAuthRepo,
		ChannelTargets:        channelTargetsFromRouter(s.channelMessageRouter),
		UnavailableAgentsText: "Agent listing is currently unavailable on Slack (no agent repository configured on this surface).",
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:   projectID,
		ProjectRepo: s.projectRepo,
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			authorized, err := s.checkAuthorizationResult(ctx, project.ID, actionCtx.UserID)
			if err != nil {
				return fmt.Errorf("check Slack authorization: %w", err)
			}
			if !authorized {
				return fmt.Errorf("Slack user %q is not authorized to use project %q", actionCtx.UserID, project.Name)
			}
			return s.setActiveProject(ctx, actionCtx.TeamID, actionCtx.UserID, project.ID)
		},
	}))
	mergeChannelRuntimeActionHandlers(handlers, buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
		ChannelDisplayName: "Slack",
		ProjectID:          projectID,
		ProjectRepo:        s.projectRepo,
	}))
	return handlers
}

// ---- New channel action executors for Slack ----

func (s *SlackService) setActiveProject(ctx context.Context, teamID, userID, projectID string) error {
	key := slackUserProjectKey(teamID, userID)
	s.mu.Lock()
	s.userProjects[key] = projectID
	s.mu.Unlock()
	if s.slackUserProjectRepo != nil {
		if err := s.slackUserProjectRepo.SetUserProject(ctx, teamID, userID, projectID); err != nil {
			applog.Infof("[slack] persist active project failed: %v", err)
			return fmt.Errorf("persist failed: %w", err)
		}
	}
	return nil
}

func (s *SlackService) getActiveProject(ctx context.Context, teamID, userID string) (string, error) {
	key := slackUserProjectKey(teamID, userID)

	s.mu.RLock()
	if projectID, ok := s.userProjects[key]; ok {
		s.mu.RUnlock()
		return projectID, nil
	}
	s.mu.RUnlock()

	if s.slackUserProjectRepo != nil {
		saved, err := s.slackUserProjectRepo.GetUserProject(ctx, teamID, userID)
		if err != nil {
			return "", fmt.Errorf("load saved Slack project: %w", err)
		}
		if saved != "" {
			s.mu.Lock()
			s.userProjects[key] = saved
			s.mu.Unlock()
			return saved, nil
		}
	}

	if s.projectRepo == nil {
		return "", nil
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list Slack projects: %w", err)
	}
	if len(projects) == 0 {
		return "", nil
	}
	selected := projects[0].ID
	for _, p := range projects {
		if p.IsDefault {
			selected = p.ID
			break
		}
	}
	s.mu.Lock()
	s.userProjects[key] = selected
	s.mu.Unlock()
	return selected, nil
}

func slackUserProjectKey(teamID, userID string) string {
	return strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
}

func (s *SlackService) checkAuthorization(ctx context.Context, projectID, slackUserID string) bool {
	authorized, err := s.checkAuthorizationResult(ctx, projectID, slackUserID)
	if err != nil {
		applog.Infof("[slack] authorization lookup failed user=%s project=%s: %v", slackUserID, projectID, err)
		return false
	}
	return authorized
}

func (s *SlackService) checkAuthorizationResult(ctx context.Context, projectID, slackUserID string) (bool, error) {
	if s.slackAuthRepo == nil {
		return true, nil
	}

	authorized, err := s.slackAuthRepo.IsAuthorizedAnywhere(ctx, slackUserID)
	if err != nil {
		if strings.TrimSpace(projectID) == "" {
			return false, fmt.Errorf("check Slack authorization anywhere: %w", err)
		}
		return false, fmt.Errorf("check Slack authorization for project: %w", err)
	}
	return authorized, nil
}

func sanitizeSlackText(text string) string {
	cleaned := strings.TrimSpace(slackMentionRegex.ReplaceAllString(text, ""))
	return strings.TrimSpace(cleaned)
}

func slackMessageTextOrAttachmentPrompt(text string, hasFiles bool) string {
	text = strings.TrimSpace(text)
	if text == "" && hasFiles {
		return "Please analyze the attachment."
	}
	return text
}

func slackMessageMentionsBot(event slackevents.MessageEvent, botUserID string) bool {
	botUserID = strings.TrimSpace(botUserID)
	if botUserID == "" {
		return false
	}
	needle := "<@" + botUserID + ">"
	if strings.Contains(event.Text, needle) {
		return true
	}
	if event.Message != nil && strings.Contains(event.Message.Text, needle) {
		return true
	}
	return false
}

func (s *SlackService) downloadSlackAttachments(ctx context.Context, files []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
	chatAttachments, err := s.downloadSlackFiles(ctx, files)
	if err != nil {
		return "", nil, nil, err
	}
	attachmentContext, imageAttachments := slackAttachmentContextAndImages(chatAttachments)
	return attachmentContext, imageAttachments, chatAttachments, nil
}

func slackAttachmentContextAndImages(chatAttachments []models.ChatAttachment) (string, []models.Attachment) {
	return channelChatAttachmentContextAndImages(chatAttachments, slackMaxTextFileSize)
}

func (s *SlackService) downloadSlackFiles(ctx context.Context, files []slackIncomingFile) ([]models.ChatAttachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > slackMaxFilesPerMessage {
		return nil, fmt.Errorf("too many files (%d, max %d)", len(files), slackMaxFilesPerMessage)
	}
	botToken := strings.TrimSpace(s.resolveBotToken(ctx))
	if botToken == "" {
		return nil, fmt.Errorf("slack bot token is not configured")
	}
	tmpDir, err := os.MkdirTemp("", "slack-attachment-*")
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
		var err error
		f, err = s.resolveSlackFileInfo(ctx, f)
		if err != nil {
			return nil, err
		}
		if f.Size > slackMaxFileSize {
			return nil, fmt.Errorf("file %q too large (%d bytes, max %d)", slackFileDisplayName(f), f.Size, slackMaxFileSize)
		}
		fileName := slackSafeFileName(f)
		mediaType := slackIncomingFileMediaType(f, fileName)
		destPath, mediaType, err := s.downloadSlackFileCandidate(ctx, botToken, tmpDir, fileName, mediaType, f)
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
		applog.Infof("[slack] downloaded attachment file=%s size=%d mime=%s path=%s", fileName, info.Size(), mediaType, absPath)
	}
	cleanup = false
	return attachments, nil
}

func (s *SlackService) resolveSlackFileInfo(ctx context.Context, f slackIncomingFile) (slackIncomingFile, error) {
	mediaType := slackIncomingFileMediaType(f, slackSafeFileName(f))
	forceInfo := strings.EqualFold(strings.TrimSpace(f.FileAccess), "check_file_info") || !slackIncomingFileHasDownloadURL(f)
	optionalInfo := isChannelChatImageMediaType(mediaType) && strings.TrimSpace(f.URLPrivateDownload) == "" && strings.TrimSpace(f.URLPrivate) != ""
	needsInfo := strings.TrimSpace(f.ID) != "" && (forceInfo || optionalInfo)
	if !needsInfo {
		return f, nil
	}
	client := s.slackClientForFiles(ctx)
	if client == nil {
		if err := ctx.Err(); err != nil {
			return f, err
		}
		if optionalInfo && !forceInfo {
			return f, nil
		}
		return f, fmt.Errorf("slack bot token is not configured")
	}
	info, _, _, err := client.GetFileInfoContext(ctx, strings.TrimSpace(f.ID), 0, 0)
	if err != nil {
		if optionalInfo && !forceInfo {
			applog.Infof("[slack] optional file info refresh failed for %s; using event file URL: %v", strings.TrimSpace(f.ID), err)
			return f, nil
		}
		return f, fmt.Errorf("failed to fetch Slack file info for %s: %w", strings.TrimSpace(f.ID), err)
	}
	if info == nil || strings.TrimSpace(info.ID) == "" {
		if optionalInfo && !forceInfo {
			applog.Infof("[slack] optional file info refresh returned empty file for %s; using event file URL", strings.TrimSpace(f.ID))
			return f, nil
		}
		return f, fmt.Errorf("Slack file info for %s was empty", strings.TrimSpace(f.ID))
	}
	resolved := slackIncomingFileFromSlackFile(*info)
	return mergeSlackIncomingFile(f, resolved), nil
}

func (s *SlackService) resolveSlackIncomingFilesForRouting(ctx context.Context, files []slackIncomingFile) []slackIncomingFile {
	if len(files) == 0 {
		return files
	}
	resolved := make([]slackIncomingFile, len(files))
	copy(resolved, files)
	for i, f := range resolved {
		mediaType := slackIncomingFileMediaType(f, slackSafeFileName(f))
		if isChannelChatImageMediaType(mediaType) {
			continue
		}
		if strings.TrimSpace(f.ID) == "" {
			continue
		}
		needsFileInfo := strings.EqualFold(strings.TrimSpace(f.FileAccess), "check_file_info")
		needsFileInfo = needsFileInfo || mediaType == "" || mediaType == "application/octet-stream"
		if !needsFileInfo && slackIncomingFileHasDownloadURL(f) {
			continue
		}
		if needsFileInfo {
			f.FileAccess = "check_file_info"
		}
		fileInfo, err := s.resolveSlackFileInfo(ctx, f)
		if err != nil {
			applog.Infof("[slack] file info refresh for routing failed for %s: %v", strings.TrimSpace(f.ID), err)
			continue
		}
		fileInfo.FileAccess = ""
		resolved[i] = fileInfo
	}
	return resolved
}

func slackIncomingFileHasDownloadURL(f slackIncomingFile) bool {
	return strings.TrimSpace(f.URLPrivateDownload) != "" || strings.TrimSpace(f.URLPrivate) != "" || strings.TrimSpace(f.Thumb1024) != "" || strings.TrimSpace(f.Thumb960) != "" || strings.TrimSpace(f.Thumb720) != "" || strings.TrimSpace(f.Thumb480) != "" || strings.TrimSpace(f.Thumb360) != ""
}

func mergeSlackIncomingFile(original, resolved slackIncomingFile) slackIncomingFile {
	if strings.TrimSpace(resolved.ID) == "" {
		resolved.ID = original.ID
	}
	if strings.TrimSpace(resolved.Name) == "" {
		resolved.Name = original.Name
	}
	if strings.TrimSpace(resolved.Title) == "" {
		resolved.Title = original.Title
	}
	if strings.TrimSpace(resolved.Mimetype) == "" || strings.TrimSpace(resolved.Mimetype) == "application/octet-stream" {
		if mt := strings.TrimSpace(original.Mimetype); mt != "" && mt != "application/octet-stream" {
			resolved.Mimetype = mt
		}
	}
	if resolved.Size == 0 {
		resolved.Size = original.Size
	}
	if strings.TrimSpace(resolved.URLPrivate) == "" {
		resolved.URLPrivate = original.URLPrivate
	}
	if strings.TrimSpace(resolved.URLPrivateDownload) == "" {
		resolved.URLPrivateDownload = original.URLPrivateDownload
	}
	if strings.TrimSpace(resolved.FileAccess) == "" {
		resolved.FileAccess = original.FileAccess
	}
	if strings.TrimSpace(resolved.Thumb360) == "" {
		resolved.Thumb360 = original.Thumb360
	}
	if strings.TrimSpace(resolved.Thumb480) == "" {
		resolved.Thumb480 = original.Thumb480
	}
	if strings.TrimSpace(resolved.Thumb720) == "" {
		resolved.Thumb720 = original.Thumb720
	}
	if strings.TrimSpace(resolved.Thumb960) == "" {
		resolved.Thumb960 = original.Thumb960
	}
	if strings.TrimSpace(resolved.Thumb1024) == "" {
		resolved.Thumb1024 = original.Thumb1024
	}
	return resolved
}

func (s *SlackService) downloadSlackFileCandidate(ctx context.Context, botToken, tmpDir, fileName, mediaType string, f slackIncomingFile) (string, string, error) {
	candidates := slackFileDownloadURLs(f, mediaType)
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("file %q has no private download URL", slackFileDisplayName(f))
	}
	var lastErr error
	for i, candidateURL := range candidates {
		destPath := filepath.Join(tmpDir, uniqueChannelChatTempFilename(tmpDir, fileName))
		dest, err := os.Create(destPath)
		if err != nil {
			return "", "", fmt.Errorf("failed to create file: %w", err)
		}
		err = s.downloadSlackPrivateFile(ctx, botToken, candidateURL, dest)
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
		normalizedMediaType, err := validateChannelChatDownloadedImageFile(destPath, fileName, mediaType, "slack")
		if err == nil {
			if i > 0 {
				applog.Infof("[slack] attachment file=%s downloaded from fallback URL after earlier candidate failed", fileName)
			}
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

func slackFileDownloadURLs(f slackIncomingFile, mediaType string) []string {
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
	add(f.URLPrivateDownload)
	add(f.URLPrivate)
	if isChannelChatImageMediaType(mediaType) {
		add(f.Thumb1024)
		add(f.Thumb960)
		add(f.Thumb720)
		add(f.Thumb480)
		add(f.Thumb360)
	}
	return urls
}

func (s *SlackService) downloadSlackPrivateFile(ctx context.Context, botToken, downloadURL string, writer io.Writer) error {
	if strings.TrimSpace(downloadURL) == "" {
		return fmt.Errorf("received empty download URL")
	}
	if strings.TrimSpace(botToken) == "" {
		return fmt.Errorf("slack bot token is not configured")
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	downloadClient := *client
	downloadClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	parsedDownloadURL, err := url.Parse(downloadURL)
	if err != nil {
		return err
	}
	if !slackTrustedOriginalFileDownloadHost(parsedDownloadURL.Hostname()) {
		return fmt.Errorf("slack file download URL host %q is not trusted", parsedDownloadURL.Host)
	}

	currentURL := downloadURL
	for redirects := 0; redirects <= 10; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+botToken)
		req.Header.Set("User-Agent", "OpenVibely Slack file downloader")

		resp, err := downloadClient.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			nextURL, err := slackRedirectLocation(currentURL, resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			if !slackCanForwardFileAuth(downloadURL, nextURL) {
				return fmt.Errorf("slack file download redirected to untrusted host %q", nextURL.Host)
			}
			currentURL = nextURL.String()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("slack file download returned HTTP %d", resp.StatusCode)
		}
		written, err := io.Copy(writer, io.LimitReader(resp.Body, slackMaxFileSize))
		if err != nil {
			return err
		}
		if written == slackMaxFileSize {
			var extra [1]byte
			n, err := resp.Body.Read(extra[:])
			if err != nil && err != io.EOF {
				return err
			}
			if n > 0 {
				return fmt.Errorf("slack file download exceeded maximum size %d bytes", slackMaxFileSize)
			}
		}
		return nil
	}
	return fmt.Errorf("slack file download exceeded redirect limit")
}

func slackRedirectLocation(currentURL, location string) (*url.URL, error) {
	if strings.TrimSpace(location) == "" {
		return nil, fmt.Errorf("slack file download redirect missing Location header")
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

func slackCanForwardFileAuth(originalURL string, nextURL *url.URL) bool {
	if nextURL == nil {
		return false
	}
	original, err := url.Parse(originalURL)
	if err != nil {
		return false
	}
	return slackTrustedFileDownloadHost(original.Hostname(), nextURL.Hostname())
}

func slackIsTrustedFileURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return slackTrustedOriginalFileDownloadHost(parsed.Hostname())
}

func slackTrustedOriginalFileDownloadHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "slack.com" || strings.HasSuffix(host, ".slack.com") {
		return true
	}
	if host == "slack-files.com" || strings.HasSuffix(host, ".slack-files.com") {
		return true
	}
	return false
}

func slackTrustedFileDownloadHost(originalHost, nextHost string) bool {
	originalHost = strings.ToLower(strings.TrimSpace(originalHost))
	nextHost = strings.ToLower(strings.TrimSpace(nextHost))
	if nextHost == "" {
		return false
	}
	if nextHost == originalHost {
		return true
	}
	if nextHost == "slack.com" || strings.HasSuffix(nextHost, ".slack.com") {
		return true
	}
	if nextHost == "slack-files.com" || strings.HasSuffix(nextHost, ".slack-files.com") {
		return true
	}
	return false
}

func (s *SlackService) slackClientForFiles(ctx context.Context) *slack.Client {
	s.mu.RLock()
	client := s.botClient
	s.mu.RUnlock()
	if client != nil {
		return client
	}
	botToken := strings.TrimSpace(s.resolveBotToken(ctx))
	if botToken == "" {
		return nil
	}
	return slack.New(botToken, slack.OptionHTTPClient(s.httpClient))
}

func (s *SlackService) publishSlackFirstTurnAttachmentsBounded(ctx context.Context, execID string, attachments []models.ChatAttachment) ([]models.ChatAttachment, error) {
	type result struct {
		attachments []models.ChatAttachment
		err         error
	}
	results := make(chan result, 1)
	go func() {
		published, err := publishChannelChatAttachmentFiles(execID, attachments, channelChatAttachmentLinkOptions{
			Platform: "slack", UploadsDir: s.uploadsDir, FallbackName: "slack-attachment",
		})
		results <- result{attachments: published, err: err}
	}()
	select {
	case completed := <-results:
		return completed.attachments, completed.err
	case <-ctx.Done():
		go func() {
			completed := <-results
			if completed.err == nil {
				cleanupSlackAttachmentSourceDirs(completed.attachments)
			}
		}()
		return nil, ctx.Err()
	}
}

func (s *SlackService) cleanupSlackAttachmentSourcesBounded(ctx context.Context, attachments []models.ChatAttachment) {
	done := make(chan struct{})
	go func() {
		cleanup := s.cleanupAttachmentSourcesFn
		if cleanup == nil {
			cleanup = cleanupSlackAttachmentSourceDirs
		}
		cleanup(attachments)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *SlackService) cleanupSlackPendingSessionBounded(ctx context.Context, sessionID string) {
	done := make(chan struct{})
	go func() {
		_ = os.RemoveAll(filepath.Join(s.uploadsDir, "chat", "pending", sessionID))
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *SlackService) downloadSlackAttachmentsBounded(ctx context.Context, files []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
	type result struct {
		attachmentContext string
		images            []models.Attachment
		attachments       []models.ChatAttachment
		err               error
	}
	results := make(chan result, 1)
	go func() {
		download := s.downloadSlackAttachmentsFn
		if download == nil {
			download = s.downloadSlackAttachments
		}
		attachmentContext, images, attachments, err := download(ctx, files)
		results <- result{attachmentContext: attachmentContext, images: images, attachments: attachments, err: err}
	}()
	select {
	case completed := <-results:
		return completed.attachmentContext, completed.images, completed.attachments, completed.err
	case <-ctx.Done():
		go func() {
			completed := <-results
			if completed.err == nil {
				cleanupSlackAttachmentSourceDirs(completed.attachments)
			}
		}()
		return "", nil, nil, ctx.Err()
	}
}

func (s *SlackService) saveChatAttachmentsToPendingSessionBounded(ctx context.Context, attachments []models.ChatAttachment) (string, error) {
	type result struct {
		sessionID string
		err       error
	}
	results := make(chan result, 1)
	go func() {
		save := s.savePendingAttachmentsFn
		if save == nil {
			save = s.saveChatAttachmentsToPendingSession
		}
		sessionID, err := save(attachments)
		results <- result{sessionID: sessionID, err: err}
	}()
	select {
	case completed := <-results:
		return completed.sessionID, completed.err
	case <-ctx.Done():
		go func() {
			completed := <-results
			if completed.sessionID != "" {
				_ = os.RemoveAll(filepath.Join(s.uploadsDir, "chat", "pending", completed.sessionID))
			}
		}()
		return "", ctx.Err()
	}
}

func (s *SlackService) saveChatAttachmentsToPendingSession(attachments []models.ChatAttachment) (string, error) {
	return saveChannelChatAttachmentsToPendingSession(s.uploadsDir, "slack-attachment", attachments)
}

func (s *SlackService) linkAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment) ([]models.ChatAttachment, error) {
	return linkChannelChatAttachmentsToExecution(ctx, execID, attachments, channelChatAttachmentLinkOptions{
		Platform:     "slack",
		UploadsDir:   s.uploadsDir,
		Repo:         s.chatAttachmentRepo,
		FallbackName: "slack-attachment",
	})
}

func (s *SlackService) cleanupLinkedSlackAttachments(ctx context.Context, attachments []models.ChatAttachment) {
	cleanupLinkedChannelChatAttachments(ctx, s.chatAttachmentRepo, "slack", attachments)
}

func cleanupSlackAttachmentSourceDirs(attachments []models.ChatAttachment) {
	cleanupChannelChatAttachmentSourceDirs(attachments)
}

func slackSafeFileName(f slackIncomingFile) string {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = strings.TrimSpace(f.Title)
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) || name == "" {
		if strings.TrimSpace(f.ID) != "" {
			name = "slack-" + strings.TrimSpace(f.ID)
		} else {
			name = "slack-attachment"
		}
	}
	return name
}

func slackFileDisplayName(f slackIncomingFile) string {
	return slackSafeFileName(f)
}

func slackIncomingFileMediaType(f slackIncomingFile, fileName string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(f.Mimetype, ";")[0]))
	filenameMediaType := mediaTypeFromSlackFilename(fileName)
	if mediaType == "" || mediaType == "application/octet-stream" {
		return filenameMediaType
	}
	return mediaType
}

func slackIncomingFilesContainImage(files []slackIncomingFile) bool {
	for _, f := range files {
		if isChannelChatImageMediaType(slackIncomingFileMediaType(f, slackSafeFileName(f))) {
			return true
		}
	}
	return false
}

func slackIncomingFilesRequireVision(files []slackIncomingFile) bool {
	for _, f := range files {
		mediaType := slackIncomingFileMediaType(f, slackSafeFileName(f))
		if isChannelChatImageMediaType(mediaType) {
			return true
		}
		if (mediaType == "" || mediaType == "application/octet-stream") && slackIncomingFileHasDownloadURL(f) {
			return true
		}
	}
	return false
}

func slackChatAttachmentsContainImage(chatAttachments []models.ChatAttachment) bool {
	for _, att := range chatAttachments {
		if isChannelChatImageMediaType(att.MediaType) {
			return true
		}
	}
	return false
}

func mediaTypeFromSlackFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md", ".csv", ".json", ".log", ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".html", ".css", ".scss", ".sql", ".sh", ".yaml", ".yml", ".xml":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

func filterSlackChatHistory(executions []models.Execution, currentExecID string) []models.Execution {
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

func (s *SlackService) sendSlackMessage(channelID, threadTS, text string) error {
	_, err := s.postSlackMessage(channelID, threadTS, text)
	return err
}

func (s *SlackService) openSlackDirectMessage(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("slack user id is required")
	}
	if s.openConversationFn != nil {
		return s.openConversationFn(userID)
	}
	s.mu.RLock()
	client := s.botClient
	s.mu.RUnlock()
	if client == nil {
		botToken := strings.TrimSpace(s.resolveBotToken(context.Background()))
		if botToken == "" {
			return "", fmt.Errorf("slack bot token is not configured")
		}
		client = slack.New(botToken)
	}
	channel, _, _, err := client.OpenConversation(&slack.OpenConversationParameters{ReturnIM: true, Users: []string{userID}})
	if err != nil {
		return "", fmt.Errorf("open slack direct message: %w", err)
	}
	if channel == nil || strings.TrimSpace(channel.ID) == "" {
		return "", fmt.Errorf("open slack direct message: missing channel id")
	}
	return strings.TrimSpace(channel.ID), nil
}

func (s *SlackService) postSlackMessage(channelID, threadTS, text string) (string, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return "", nil
	}
	if s.postMessageFn != nil {
		return s.postMessageFn(channelID, threadTS, text)
	}

	s.mu.RLock()
	client := s.botClient
	s.mu.RUnlock()
	if client == nil {
		botToken := strings.TrimSpace(s.resolveBotToken(context.Background()))
		if botToken == "" {
			return "", fmt.Errorf("slack bot token is not configured")
		}
		client = slack.New(botToken)
	}

	params := slack.PostMessageParameters{}
	if strings.TrimSpace(threadTS) != "" {
		params.ThreadTimestamp = threadTS
	}
	_, ts, err := client.PostMessage(channelID,
		slack.MsgOptionPostMessageParameters(params),
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		return "", fmt.Errorf("post slack message: %w", err)
	}
	return ts, nil
}

func (s *SlackService) SendOutboundMessage(ctx context.Context, channelID, threadTS, text string) SendMessageResult {
	_ = ctx
	if strings.TrimSpace(channelID) == "" {
		return SendMessageResult{OK: false, Platform: "slack", Error: "slack channel id is required"}
	}
	if strings.TrimSpace(text) == "" {
		return SendMessageResult{OK: false, Platform: "slack", Target: formatResolvedMessageTarget("slack", channelID, threadTS), Error: "message is required"}
	}
	messageID, err := s.postSlackMessage(channelID, threadTS, text)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "slack", Target: formatResolvedMessageTarget("slack", channelID, threadTS), Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "slack", Target: formatResolvedMessageTarget("slack", channelID, threadTS), MessageID: messageID}
}

func (s *SlackService) SendOutboundDirectMessage(ctx context.Context, userID, text string) SendMessageResult {
	_ = ctx
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return SendMessageResult{OK: false, Platform: "slack", Error: "slack user id is required"}
	}
	if strings.TrimSpace(text) == "" {
		return SendMessageResult{OK: false, Platform: "slack", Target: formatResolvedMessageTarget("slack", userID, ""), Error: "message is required"}
	}
	channelID, err := s.openSlackDirectMessage(userID)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "slack", Target: formatResolvedMessageTarget("slack", userID, ""), Error: err.Error()}
	}
	messageID, err := s.postSlackMessage(channelID, "", text)
	if err != nil {
		return SendMessageResult{OK: false, Platform: "slack", Target: formatResolvedMessageTarget("slack", userID, ""), Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "slack", Target: formatResolvedMessageTarget("slack", userID, ""), MessageID: messageID}
}

func generateOAuthState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *SlackService) getSettingValue(ctx context.Context, key string) (string, error) {
	if s.settingsRepo == nil {
		return "", nil
	}
	return s.settingsRepo.Get(ctx, key)
}

func (s *SlackService) getSetting(ctx context.Context, key string) string {
	val, _ := s.getSettingValue(ctx, key)
	return val
}

func (s *SlackService) setSetting(ctx context.Context, key, value string) error {
	if s.settingsRepo == nil {
		return nil
	}
	return s.settingsRepo.Set(ctx, key, value)
}

func (s *SlackService) getBotTokenSource(ctx context.Context) string {
	source := strings.TrimSpace(strings.ToLower(s.getSetting(ctx, SlackSettingBotTokenSource)))
	switch source {
	case SlackBotTokenSourceManual:
		return SlackBotTokenSourceManual
	default:
		return SlackBotTokenSourceOAuth
	}
}

func (s *SlackService) resolveBotToken(ctx context.Context) string {
	source := s.getBotTokenSource(ctx)
	if source == SlackBotTokenSourceManual {
		overrideToken := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotTokenOverride))
		if overrideToken != "" {
			return overrideToken
		}
	}
	return strings.TrimSpace(s.getSetting(ctx, SlackSettingBotToken))
}

// SendChatResponse sends a completed chat-orchestrator response back to the
// originating Slack thread. Unlike task completion notifications, this is only
// for chat-category tasks that were promoted from queued channel input.
func (s *SlackService) SendChatResponse(ctx context.Context, task models.Task, output string, errMsg string) {
	if task.CreatedVia != models.TaskOriginSlack || task.Category != models.CategoryChat || s.slackTaskContextRepo == nil {
		return
	}
	if !s.IsSendResponsesEnabled(ctx) {
		return
	}
	ctxRecord, err := s.slackTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || ctxRecord == nil {
		return
	}
	var message string
	if errMsg != "" {
		message = fmt.Sprintf("❌ Error: %s", util.Truncate(errMsg, 220))
	} else {
		cleaned := llmoutput.CleanChatOutputForDisplay(output)
		if cleaned == "" {
			cleaned = "(No response)"
		}
		message = cleaned
	}
	if err := s.sendSlackMessage(ctxRecord.SlackChannelID, ctxRecord.SlackThreadTS, message); err != nil {
		applog.Infof("[slack] send chat response failed for task=%s: %v", task.ID, err)
	}
}
