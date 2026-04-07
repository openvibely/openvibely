package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

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

	defaultSlackAPIBaseURL = "https://slack.com/api"
	slackProcessTimeout    = 5 * time.Minute
	slackChatHistoryLimit  = 50
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
	settingsRepo          *repository.SettingsRepo
	projectRepo           *repository.ProjectRepo
	llmConfigRepo         *repository.LLMConfigRepo
	taskRepo              *repository.TaskRepo
	execRepo              *repository.ExecutionRepo
	scheduleRepo          *repository.ScheduleRepo
	taskSvc               *TaskService
	llmSvc                *LLMService
	workerSvc             *WorkerService
	slackUserProjectRepo  *repository.SlackUserProjectRepo
	slackTaskContextRepo  *repository.SlackTaskContextRepo
	customPersonalityRepo *repository.CustomPersonalityRepo
	slackAuthRepo         *repository.SlackAuthRepo
	chatBroadcaster       *events.ChatBroadcaster
	alertSvc              *AlertService

	httpClient   *http.Client
	oauthBaseURL string

	mu                       sync.RWMutex
	botClient                *slack.Client
	socketClient             *socketmode.Client
	running                  bool
	ctx                      context.Context
	cancel                   context.CancelFunc
	userProjects             map[string]string
	postMessageFn            func(channelID, threadTS, text string) error
	processIncomingMessageFn func(msg slackIncomingMessage)
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
		oauthBaseURL: defaultSlackAPIBaseURL,
		userProjects: make(map[string]string),
	}
}

func (s *SlackService) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	s.customPersonalityRepo = repo
}

func (s *SlackService) SetChatBroadcaster(cb *events.ChatBroadcaster) {
	s.chatBroadcaster = cb
}

func (s *SlackService) SetAlertService(svc *AlertService) {
	s.alertSvc = svc
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

	botClient := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	socketClient := socketmode.New(botClient)
	ctx, cancel := context.WithCancel(context.Background())

	s.botClient = botClient
	s.socketClient = socketClient
	s.ctx = ctx
	s.cancel = cancel
	s.running = true

	go s.runSocketLoop(ctx, socketClient)
	go socketClient.RunContext(ctx)

	log.Println("[slack] socket mode started")
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
	log.Println("[slack] socket mode stopped")
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
	v.Set("scope", "app_mentions:read,channels:history,groups:history,im:history,mpim:history,chat:write")
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
		log.Printf("[slack] send completion notification failed for task=%s: %v", task.ID, err)
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
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		if evt.Request != nil {
			client.Ack(*evt.Request)
		}

		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		if eventsAPIEvent.Type != slackevents.CallbackEvent {
			return
		}

		teamID := strings.TrimSpace(eventsAPIEvent.TeamID)
		switch e := eventsAPIEvent.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			s.handleAppMention(ctx, teamID, *e)
		case slackevents.AppMentionEvent:
			s.handleAppMention(ctx, teamID, e)
		case *slackevents.MessageEvent:
			s.handleMessageEvent(ctx, teamID, *e)
		case slackevents.MessageEvent:
			s.handleMessageEvent(ctx, teamID, e)
		}
	}
}

func (s *SlackService) handleAppMention(ctx context.Context, teamID string, event slackevents.AppMentionEvent) {
	if strings.TrimSpace(event.User) == "" {
		return
	}
	if strings.TrimSpace(event.BotID) != "" {
		return
	}
	botUserID := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotUserID))
	if botUserID != "" && strings.TrimSpace(event.User) == botUserID {
		return
	}

	text := sanitizeSlackText(event.Text)
	if text == "" {
		return
	}

	threadTS := strings.TrimSpace(event.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = strings.TrimSpace(event.TimeStamp)
	}

	s.processIncoming(slackIncomingMessage{
		TeamID:    teamID,
		ChannelID: strings.TrimSpace(event.Channel),
		ThreadTS:  threadTS,
		UserID:    strings.TrimSpace(event.User),
		Text:      text,
		Source:    "slack",
	})
}

func (s *SlackService) handleMessageEvent(ctx context.Context, teamID string, event slackevents.MessageEvent) {
	if strings.TrimSpace(event.ChannelType) != "im" {
		return
	}
	if strings.TrimSpace(event.User) == "" {
		return
	}
	if strings.TrimSpace(event.BotID) != "" || strings.TrimSpace(event.SubType) != "" {
		return
	}
	botUserID := strings.TrimSpace(s.getSetting(ctx, SlackSettingBotUserID))
	if botUserID != "" && strings.TrimSpace(event.User) == botUserID {
		return
	}

	text := sanitizeSlackText(event.Text)
	if text == "" {
		return
	}

	threadTS := strings.TrimSpace(event.ThreadTimeStamp)
	if threadTS == "" {
		threadTS = strings.TrimSpace(event.TimeStamp)
	}

	s.processIncoming(slackIncomingMessage{
		TeamID:    teamID,
		ChannelID: strings.TrimSpace(event.Channel),
		ThreadTS:  threadTS,
		UserID:    strings.TrimSpace(event.User),
		Text:      text,
		Source:    "slack",
	})
}

type slackIncomingMessage struct {
	TeamID    string
	ChannelID string
	ThreadTS  string
	UserID    string
	Text      string
	Source    string
}

func (s *SlackService) processIncoming(msg slackIncomingMessage) {
	if s.processIncomingMessageFn != nil {
		s.processIncomingMessageFn(msg)
		return
	}
	s.processIncomingMessage(msg)
}

func (s *SlackService) processIncomingMessage(msg slackIncomingMessage) {
	if msg.ChannelID == "" || msg.UserID == "" || strings.TrimSpace(msg.Text) == "" {
		return
	}
	if s.taskRepo == nil || s.execRepo == nil || s.llmConfigRepo == nil || s.llmSvc == nil || s.taskSvc == nil || s.projectRepo == nil {
		log.Printf("[slack] incoming message ignored: service dependencies are not fully configured")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), slackProcessTimeout)
	defer cancel()
	start := time.Now()

	projectID := s.getActiveProject(ctx, msg.TeamID, msg.UserID)
	if projectID == "" {
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "No active project found. Please create a project first in the web UI.")
		return
	}
	if !s.checkAuthorization(ctx, projectID, msg.UserID) {
		log.Printf("[slack] unauthorized access blocked for user=%s team=%s project=%s", msg.UserID, msg.TeamID, projectID)
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "You are not authorized to use Slack access for this project. Contact the project owner to get access.")
		return
	}

	agent, err := s.autoSelectAgent(ctx, msg.Text)
	if err != nil {
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, fmt.Sprintf("Error selecting model: %v", err))
		return
	}

	selectedAgentID := agent.ID
	chatTitle := fmt.Sprintf("Slack %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Text, 47))
	task := &models.Task{
		ProjectID:  projectID,
		Title:      chatTitle,
		Prompt:     msg.Text,
		Status:     models.StatusPending,
		Category:   models.CategoryChat,
		AgentID:    &selectedAgentID,
		CreatedVia: models.TaskOriginSlack,
	}
	if err := s.taskRepo.Create(ctx, task); err != nil {
		log.Printf("[slack] create chat task failed: %v", err)
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error processing your message. Please try again.")
		return
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    msg.Text,
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		log.Printf("[slack] create execution failed: %v", err)
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, "Error processing your message. Please try again.")
		return
	}

	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Message:   msg.Text,
			Source:    msg.Source,
			AgentName: agent.Name,
		})
	}

	chatHistory, err := s.execRepo.ListChatHistory(ctx, projectID, slackChatHistoryLimit)
	if err != nil {
		chatHistory = []models.Execution{}
	}
	priorHistory := filterSlackChatHistory(chatHistory, exec.ID)

	systemContext := s.buildChatContext(ctx, projectID)
	if personalityPrompt := s.getPersonalityContext(ctx, projectID); personalityPrompt != "" {
		systemContext = systemContext + personalityPrompt
	}
	workDir := s.resolveWorkDir(ctx, projectID)

	callCtx := llmcontracts.WithChatMode(ctx, models.ChatModeOrchestrate)
	markerCtx := slackMarkerContext{
		TeamID:    msg.TeamID,
		ChannelID: msg.ChannelID,
		ThreadTS:  msg.ThreadTS,
		UserID:    msg.UserID,
	}
	if supportsRuntimeChatActionTools(*agent) {
		callCtx = llmcontracts.WithRuntimeTools(callCtx, s.buildSlackActionToolRuntime(projectID, markerCtx, nil))
	}

	output, tokensUsed, llmErr := s.llmSvc.CallAgentDirectStreaming(
		callCtx,
		msg.Text,
		nil,
		*agent,
		exec.ID,
		priorHistory,
		systemContext,
		workDir,
		false,
	)
	durationMs := time.Since(start).Milliseconds()

	if llmErr != nil {
		s.completeExecution(ctx, exec.ID, task.ID, "", llmErr.Error(), 0, durationMs)
		_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, fmt.Sprintf("Error: %s", util.Truncate(llmErr.Error(), 220)))
		return
	}

	s.completeExecution(ctx, exec.ID, task.ID, output, "", tokensUsed, durationMs)

	if s.chatBroadcaster != nil {
		s.chatBroadcaster.Publish(events.ChatEvent{
			Type:            events.ChatResponseDone,
			ProjectID:       projectID,
			ExecID:          exec.ID,
			TaskID:          task.ID,
			Source:          msg.Source,
			AgentName:       agent.Name,
			CompletedOutput: output,
		})
	}

	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		cleaned = "(No response)"
	}
	_ = s.sendSlackMessage(msg.ChannelID, msg.ThreadTS, cleaned)
}

func (s *SlackService) completeExecution(ctx context.Context, execID, taskID, output, errorMessage string, tokensUsed int, durationMs int64) {
	if s.execRepo == nil || s.taskRepo == nil {
		return
	}
	if errorMessage != "" {
		if err := s.execRepo.Complete(ctx, execID, models.ExecFailed, "", errorMessage, 0, durationMs); err != nil {
			log.Printf("[slack] complete failed execution error: %v", err)
		}
		if err := s.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
			log.Printf("[slack] update failed task status error: %v", err)
		}
		return
	}

	if err := s.execRepo.Complete(ctx, execID, models.ExecCompleted, output, "", tokensUsed, durationMs); err != nil {
		log.Printf("[slack] complete execution error: %v", err)
	}
	if err := s.taskRepo.UpdateStatus(ctx, taskID, models.StatusCompleted); err != nil {
		log.Printf("[slack] update task status error: %v", err)
	}
}

func (s *SlackService) autoSelectAgent(ctx context.Context, message string) (*models.LLMConfig, error) {
	if s.llmConfigRepo == nil {
		return nil, fmt.Errorf("no model repository configured")
	}
	agents, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents configured")
	}

	complexity := AnalyzeComplexity(message)
	if result := SelectLLMWithVision(complexity, agents, false); result != nil {
		return result.LLMConfig, nil
	}
	for i := range agents {
		if agents[i].IsDefault {
			return &agents[i], nil
		}
	}
	return &agents[0], nil
}

func (s *SlackService) buildChatContext(ctx context.Context, projectID string) string {
	existingTasks := []models.Task{}
	if s.taskSvc != nil {
		tasks, err := s.taskSvc.ListByProject(ctx, projectID, "")
		if err == nil {
			existingTasks = tasks
		}
	}
	availableModels := []models.LLMConfig{}
	if s.llmConfigRepo != nil {
		availableModels, _ = s.llmConfigRepo.List(ctx)
	}
	var schedules []models.Schedule
	if s.scheduleRepo != nil {
		var err error
		schedules, err = s.scheduleRepo.ListByProject(ctx, projectID)
		if err != nil {
			schedules = []models.Schedule{}
		}
	}
	return BuildChatContext(existingTasks, availableModels, schedules, time.Now())
}

func (s *SlackService) resolveWorkDir(ctx context.Context, projectID string) string {
	if s.projectRepo == nil {
		return ""
	}
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return ""
	}
	return project.RepoPath
}

func (s *SlackService) getPersonalityContext(ctx context.Context, projectID string) string {
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

type slackMarkerContext struct {
	TeamID    string
	ChannelID string
	ThreadTS  string
	UserID    string
}

func (s *SlackService) buildSlackActionToolRuntime(projectID string, markerCtx slackMarkerContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	handlers := s.slackActionHandlers(projectID, markerCtx, collector)
	return &llmcontracts.RuntimeTools{
		Definitions: actionToolDefinitions(chatcontrol.SurfaceSlack, true),
		Executor:    chatcontrol.BuildRuntimeToolExecutor(models.ChatModeOrchestrate, chatcontrol.SurfaceSlack, handlers),
	}
}

func (s *SlackService) slackActionHandlers(projectID string, markerCtx slackMarkerContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
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
				if s.taskRepo != nil {
					if err := s.taskRepo.UpdateSlackOrigin(ctx, t.ID); err != nil {
						log.Printf("[slack] runtime create_task update slack origin failed for task=%s: %v", t.ID, err)
					}
				}
				if s.slackTaskContextRepo != nil {
					_ = s.slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{
						TaskID:         t.ID,
						SlackTeamID:    markerCtx.TeamID,
						SlackChannelID: markerCtx.ChannelID,
						SlackThreadTS:  markerCtx.ThreadTS,
						SlackUserID:    markerCtx.UserID,
					})
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
			return s.slackViewTaskThread(ctx, projectID, input), nil
		},
		"send_to_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackSendToTask(ctx, projectID, input), nil
		},
		"schedule_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackScheduleTask(ctx, projectID, input), nil
		},
		"delete_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackDeleteSchedule(ctx, projectID, input), nil
		},
		"modify_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackModifySchedule(ctx, projectID, input), nil
		},
		"list_personalities": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackListPersonalities(ctx), nil
		},
		"set_personality": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackSetPersonality(ctx, input), nil
		},
		"list_models": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackListModels(ctx), nil
		},
		"list_agents": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackListAgents(ctx), nil
		},
		"view_settings": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackViewSettings(ctx), nil
		},
		"project_info": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackProjectInfo(ctx, projectID), nil
		},
		"create_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackCreateAlert(ctx, projectID, input), nil
		},
		"delete_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackDeleteAlert(ctx, input), nil
		},
		"toggle_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackToggleAlert(ctx, input), nil
		},
		"list_projects": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.buildProjectListResult(ctx, projectID), nil
		},
		"switch_project": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req SwitchProjectRequest
			if err := decodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			return s.switchProjectResult(ctx, markerCtx.TeamID, markerCtx.UserID, req.Project), nil
		},
		"get_personality": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackGetPersonality(ctx), nil
		},
		"get_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackGetModel(ctx, input), nil
		},
		"get_current_project": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackGetCurrentProject(ctx, projectID), nil
		},
		"list_alerts": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return s.slackListAlerts(ctx, projectID), nil
		},
		"get_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return s.slackGetAlert(ctx, input), nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Current chat mode: orchestrate", nil
		},
		"set_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "Chat mode changes are not supported on Slack. Slack always uses orchestrate mode.", nil
		},
		"list_capabilities": func(_ context.Context, _ json.RawMessage) (string, error) {
			summaries := chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceSlack)
			return formatChannelCapabilities(summaries), nil
		},
	}
}

func (s *SlackService) buildProjectListResult(ctx context.Context, projectID string) string {
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

func (s *SlackService) switchProjectResult(ctx context.Context, teamID, userID, targetProject string) string {
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

	s.setActiveProject(ctx, teamID, userID, target.ID)
	return fmt.Sprintf("Switched to project: %s", target.Name)
}

// ---- New channel action executors for Slack ----

func (s *SlackService) slackGetPersonality(ctx context.Context) string {
	if s.settingsRepo == nil {
		return "Current personality: default (no personality set)"
	}
	current, err := s.settingsRepo.Get(ctx, "personality")
	if err != nil {
		log.Printf("[slack] slackGetPersonality error: %v", err)
		return "Error retrieving personality setting."
	}
	if current == "" {
		return "Current personality: default (base, no personality modifier active)"
	}
	return fmt.Sprintf("Current personality: %s", current)
}

func (s *SlackService) slackGetModel(ctx context.Context, input json.RawMessage) string {
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

func (s *SlackService) slackGetCurrentProject(ctx context.Context, projectID string) string {
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	return fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
}

func (s *SlackService) slackListAlerts(ctx context.Context, projectID string) string {
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

func (s *SlackService) slackGetAlert(ctx context.Context, input json.RawMessage) string {
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

func (s *SlackService) slackViewTaskThread(ctx context.Context, projectID string, input json.RawMessage) string {
	var req ViewThreadRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for view_task_thread."
	}
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "view_task_thread requires task_id or title."
	}
	task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Error resolving task: %v", err)
	}
	executions, err := s.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return fmt.Sprintf("Error retrieving thread for %q: %v", task.Title, err)
	}
	return strings.TrimSpace(formatThreadTranscript(task, executions, req.Offset, req.Limit))
}

func (s *SlackService) slackSendToTask(ctx context.Context, projectID string, input json.RawMessage) string {
	var req SendToTaskRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for send_to_task."
	}
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "send_to_task requires task_id or title."
	}
	if strings.TrimSpace(req.Message) == "" {
		return "send_to_task requires message."
	}
	task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Error resolving task: %v", err)
	}
	if task.Status == models.StatusRunning {
		return fmt.Sprintf("Task %q is currently running. Wait for it to finish before sending a message.", task.Title)
	}
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed || task.Status == models.StatusCancelled {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
			return fmt.Sprintf("Error reactivating task %q: %v", task.Title, err)
		}
		if task.Category == models.CategoryCompleted {
			if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				log.Printf("[slack] runtime send_to_task error updating category for task %s: %v", task.ID, err)
			}
		}
	} else if task.Status == models.StatusPending {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
			log.Printf("[slack] runtime send_to_task error setting task running task=%s: %v", task.ID, err)
		}
	}
	var agent *models.LLMConfig
	if task.AgentID != nil {
		agent, _ = s.llmConfigRepo.GetByID(ctx, *task.AgentID)
	}
	if agent == nil {
		agent, err = s.autoSelectAgent(ctx, req.Message)
		if err != nil {
			return fmt.Sprintf("Error selecting agent for task %q: %v", task.Title, err)
		}
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: req.Message, IsFollowup: true}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		return fmt.Sprintf("Error creating follow-up execution for %q: %v", task.Title, err)
	}
	priorExecs, _ := s.execRepo.ListByTaskChronological(ctx, task.ID)
	priorHistory := filterSlackChatHistory(priorExecs, exec.ID)
	systemContext := buildTelegramTaskChatContext(task.Title, len(priorHistory) > 0)
	if pCtx := s.getPersonalityContext(ctx, task.ProjectID); pCtx != "" {
		systemContext += pCtx
	}
	workDir := s.resolveWorkDir(ctx, task.ProjectID)
	go s.processTaskFollowup(exec.ID, task.ID, req.Message, *agent, priorHistory, task.ProjectID, systemContext, workDir)
	return fmt.Sprintf("Sent message to task %q [TASK_ID:%s] and started processing.", task.Title, task.ID)
}

func (s *SlackService) slackScheduleTask(ctx context.Context, projectID string, input json.RawMessage) string {
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
	if s.scheduleRepo == nil {
		return "Error scheduling task: schedule repository not available."
	}
	task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Could not find task: %v", err)
	}
	var hourVal, minuteVal int
	if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
		return fmt.Sprintf("Invalid time %q (expected HH:MM).", req.Time)
	}
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
	repeatInterval := 1
	if req.Interval > 0 {
		repeatInterval = req.Interval
	}
	now := time.Now().Local()
	runAt := time.Date(now.Year(), now.Month(), now.Day(), hourVal, minuteVal, 0, 0, time.Local)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt.UTC(), RepeatType: repeatType, RepeatInterval: repeatInterval, Enabled: true}
	if err := s.scheduleRepo.Create(ctx, schedule); err != nil {
		return fmt.Sprintf("Error scheduling task %q: %v", task.Title, err)
	}
	if task.Category != models.CategoryScheduled {
		_ = s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled)
		if task.Status != models.StatusPending {
			_ = s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending)
		}
	}
	return fmt.Sprintf("Scheduled task %q [TASK_ID:%s] at %s (%s).", task.Title, task.ID, req.Time, FormatRepeatPattern(repeatType, repeatInterval))
}

func (s *SlackService) slackDeleteSchedule(ctx context.Context, projectID string, input json.RawMessage) string {
	var req DeleteScheduleRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for delete_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "delete_schedule requires schedule_id, task_id, or title."
	}
	if s.scheduleRepo == nil {
		return "Error deleting schedule: schedule repository not available."
	}
	schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Could not find schedule: %v", err)
	}
	if err := s.scheduleRepo.Delete(ctx, schedule.ID); err != nil {
		return fmt.Sprintf("Error deleting schedule for task %q: %v", task.Title, err)
	}
	remaining, _ := s.scheduleRepo.ListByTask(ctx, task.ID)
	if len(remaining) == 0 && task.Category == models.CategoryScheduled {
		_ = s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog)
	}
	return fmt.Sprintf("Deleted schedule for task %q [TASK_ID:%s].", task.Title, task.ID)
}

func (s *SlackService) slackModifySchedule(ctx context.Context, projectID string, input json.RawMessage) string {
	var req ModifyScheduleRequest
	if err := decodeRuntimeToolInput(input, &req); err != nil {
		return "Invalid input for modify_schedule."
	}
	if strings.TrimSpace(req.ScheduleID) == "" && strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "modify_schedule requires schedule_id, task_id, or title."
	}
	if s.scheduleRepo == nil {
		return "Error modifying schedule: schedule repository not available."
	}
	schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
	if err != nil {
		return fmt.Sprintf("Could not find schedule: %v", err)
	}
	var changes []string
	if req.Time != "" {
		var hourVal, minuteVal int
		if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
			return fmt.Sprintf("Invalid time %q.", req.Time)
		}
		oldLocal := schedule.RunAt.Local()
		schedule.RunAt = time.Date(oldLocal.Year(), oldLocal.Month(), oldLocal.Day(), hourVal, minuteVal, 0, 0, time.Local).UTC()
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
			return fmt.Sprintf("Unknown repeat type %q.", req.Repeat)
		}
		changes = append(changes, fmt.Sprintf("repeat→%s", req.Repeat))
	}
	if req.Interval != nil {
		if *req.Interval < 1 {
			return fmt.Sprintf("Invalid interval %d.", *req.Interval)
		}
		schedule.RepeatInterval = *req.Interval
		changes = append(changes, fmt.Sprintf("interval→%d", *req.Interval))
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
		return fmt.Sprintf("No changes specified for schedule on task %q.", task.Title)
	}
	schedule.NextRun = schedule.ComputeNextRun(time.Now())
	if err := s.scheduleRepo.Update(ctx, schedule); err != nil {
		return fmt.Sprintf("Error updating schedule for task %q: %v", task.Title, err)
	}
	return fmt.Sprintf("Updated schedule for task %q [TASK_ID:%s]: %s.", task.Title, task.ID, strings.Join(changes, ", "))
}

func (s *SlackService) slackListPersonalities(ctx context.Context) string {
	personalities := AllPersonalitiesWithCustom(ctx, s.customPersonalityRepo)
	if len(personalities) == 0 {
		return "No personalities available."
	}
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
		if current, err := s.settingsRepo.Get(ctx, "personality"); err == nil {
			if current == "" {
				current = "default"
			}
			sb.WriteString(fmt.Sprintf("\nCurrent personality: %s", current))
		}
	}
	return strings.TrimSpace(sb.String())
}

func (s *SlackService) slackSetPersonality(ctx context.Context, input json.RawMessage) string {
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

func (s *SlackService) slackListModels(ctx context.Context) string {
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

func (s *SlackService) slackListAgents(ctx context.Context) string {
	return "Agent listing is currently unavailable on Slack (no agent repository configured on this surface)."
}

func (s *SlackService) slackViewSettings(ctx context.Context) string {
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
	if configs, err := s.llmConfigRepo.List(ctx); err == nil {
		sb.WriteString(fmt.Sprintf("- Configured models: %d\n", len(configs)))
	}
	if s.projectRepo != nil {
		if projects, err := s.projectRepo.List(ctx); err == nil {
			sb.WriteString(fmt.Sprintf("- Projects: %d\n", len(projects)))
		}
	}
	return strings.TrimSpace(sb.String())
}

func (s *SlackService) slackProjectInfo(ctx context.Context, projectID string) string {
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

func (s *SlackService) slackCreateAlert(ctx context.Context, projectID string, input json.RawMessage) string {
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

func (s *SlackService) slackDeleteAlert(ctx context.Context, input json.RawMessage) string {
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

func (s *SlackService) slackToggleAlert(ctx context.Context, input json.RawMessage) string {
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

func (s *SlackService) processTaskFollowup(execID, taskID, message string, agent models.LLMConfig, chatHistory []models.Execution, projectID, systemContext, workDir string) {
	if s.llmSvc == nil {
		log.Printf("[slack] processTaskFollowup exec=%s task=%s skipping: llmSvc is nil", execID, taskID)
		return
	}
	timeout := slackProcessTimeout
	agentConfigID := agent.ID
	if s.workerSvc != nil {
		if modelTimeout := s.workerSvc.GetModelWorkerTimeout(agentConfigID); modelTimeout > 0 {
			timeout = modelTimeout
		}
		if !s.workerSvc.TryAcquireProjectSlot(projectID) {
			s.completeExecution(context.Background(), execID, taskID, "", "project worker limit reached — please wait for a running task to complete", 0, 0)
			return
		}
		defer s.workerSvc.ReleaseProjectSlot(projectID)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), timeout)
		if err := s.workerSvc.AcquireModelSlot(waitCtx, agentConfigID); err != nil {
			waitCancel()
			s.completeExecution(context.Background(), execID, taskID, "", fmt.Sprintf("model %s at capacity, timed out waiting for slot", agent.Name), 0, 0)
			return
		}
		waitCancel()
		defer s.workerSvc.ReleaseModelSlot(agentConfigID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if s.workerSvc != nil {
		s.workerSvc.RegisterCancel(taskID, cancel)
		defer s.workerSvc.DeregisterCancel(taskID)
	}
	start := time.Now()
	output, tokensUsed, err := s.llmSvc.CallAgentDirectStreaming(ctx, message, nil, agent, execID, chatHistory, systemContext, workDir, true)
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		s.completeExecution(ctx, execID, taskID, "", err.Error(), 0, durationMs)
		return
	}
	s.completeExecution(ctx, execID, taskID, output, "", tokensUsed, durationMs)
}

func (s *SlackService) resolveTaskReference(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
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

func (s *SlackService) processScheduleTask(ctx context.Context, execID, projectID, output string) string {
	requests := ParseScheduleTask(output)
	if len(requests) == 0 {
		return output
	}
	var results []string
	for _, req := range requests {
		task, err := s.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			continue
		}
		if s.scheduleRepo == nil {
			results = append(results, fmt.Sprintf("- Error scheduling task %q: schedule repository not available", task.Title))
			continue
		}
		var hourVal, minuteVal int
		if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
			results = append(results, fmt.Sprintf("- Invalid time %q for task %q (expected HH:MM)", req.Time, task.Title))
			continue
		}
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
		repeatInterval := 1
		if req.Interval > 0 {
			repeatInterval = req.Interval
		}
		now := time.Now().Local()
		runAt := time.Date(now.Year(), now.Month(), now.Day(), hourVal, minuteVal, 0, 0, time.Local)
		runAtUTC := runAt.UTC()
		schedule := &models.Schedule{TaskID: task.ID, RunAt: runAtUTC, RepeatType: repeatType, RepeatInterval: repeatInterval, Enabled: true}
		if err := s.scheduleRepo.Create(ctx, schedule); err != nil {
			results = append(results, fmt.Sprintf("- Error scheduling task %q: %v", task.Title, err))
			continue
		}
		if task.Category != models.CategoryScheduled {
			_ = s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled)
			if task.Status != models.StatusPending {
				_ = s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending)
			}
		}
		repeatDesc := FormatRepeatPattern(repeatType, repeatInterval)
		results = append(results, fmt.Sprintf("- Scheduled task %q [TASK_ID:%s] at %s (%s)", task.Title, task.ID, req.Time, repeatDesc))
	}
	if len(results) > 0 {
		output += "\n\n---\nSchedule Results:\n" + strings.Join(results, "\n")
	}
	return output
}

func (s *SlackService) processDeleteSchedule(ctx context.Context, execID, projectID, output string) string {
	requests := ParseDeleteSchedule(output)
	if len(requests) == 0 {
		return output
	}
	if s.scheduleRepo == nil {
		return output + "\n\n---\nSchedule Delete Results:\n- Error: schedule repository not available"
	}
	var results []string
	for _, req := range requests {
		schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}
		if err := s.scheduleRepo.Delete(ctx, schedule.ID); err != nil {
			results = append(results, fmt.Sprintf("- Error deleting schedule for task %q: %v", task.Title, err))
			continue
		}
		remaining, _ := s.scheduleRepo.ListByTask(ctx, task.ID)
		if len(remaining) == 0 && task.Category == models.CategoryScheduled {
			_ = s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog)
		}
		results = append(results, fmt.Sprintf("- Deleted schedule for task %q [TASK_ID:%s]", task.Title, task.ID))
	}
	if len(results) > 0 {
		output += "\n\n---\nSchedule Delete Results:\n" + strings.Join(results, "\n")
	}
	return output
}

func (s *SlackService) processModifySchedule(ctx context.Context, execID, projectID, output string) string {
	requests := ParseModifySchedule(output)
	if len(requests) == 0 {
		return output
	}
	if s.scheduleRepo == nil {
		return output + "\n\n---\nSchedule Modify Results:\n- Error: schedule repository not available"
	}
	var results []string
	for _, req := range requests {
		schedule, task, err := s.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}
		var changes []string
		if req.Time != "" {
			var hourVal, minuteVal int
			if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
				results = append(results, fmt.Sprintf("- Invalid time %q for task %q", req.Time, task.Title))
				continue
			}
			oldLocal := schedule.RunAt.Local()
			schedule.RunAt = time.Date(oldLocal.Year(), oldLocal.Month(), oldLocal.Day(), hourVal, minuteVal, 0, 0, time.Local).UTC()
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
				results = append(results, fmt.Sprintf("- Unknown repeat type %q for task %q", req.Repeat, task.Title))
				continue
			}
			changes = append(changes, fmt.Sprintf("repeat→%s", req.Repeat))
		}
		if req.Interval != nil {
			if *req.Interval < 1 {
				results = append(results, fmt.Sprintf("- Invalid interval %d for task %q", *req.Interval, task.Title))
				continue
			}
			schedule.RepeatInterval = *req.Interval
			changes = append(changes, fmt.Sprintf("interval→%d", *req.Interval))
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
			results = append(results, fmt.Sprintf("- No changes specified for schedule on task %q", task.Title))
			continue
		}
		schedule.NextRun = schedule.ComputeNextRun(time.Now())
		if err := s.scheduleRepo.Update(ctx, schedule); err != nil {
			results = append(results, fmt.Sprintf("- Error updating schedule for task %q: %v", task.Title, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Updated schedule for task %q [TASK_ID:%s]: %s", task.Title, task.ID, strings.Join(changes, ", ")))
	}
	if len(results) > 0 {
		output += "\n\n---\nSchedule Modify Results:\n" + strings.Join(results, "\n")
	}
	return output
}

func (s *SlackService) resolveScheduleReference(ctx context.Context, projectID, scheduleID, taskID, title string) (*models.Schedule, *models.Task, error) {
	if scheduleID != "" {
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
	schedules, err := s.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("error listing schedules for task %s: %w", task.ID, err)
	}
	if len(schedules) == 0 {
		return nil, nil, fmt.Errorf("no schedules found for task %q", task.Title)
	}
	return &schedules[0], task, nil
}

func (s *SlackService) processChatMarkers(ctx context.Context, execID, projectID, output string, markerCtx slackMarkerContext) string {
	if output == "" {
		return output
	}
	originalOutput := output
	if s.taskSvc == nil {
		return output
	}

	taskRequests := ParseTaskCreations(output)
	if len(taskRequests) > 0 {
		agents := []models.LLMConfig{}
		if s.llmConfigRepo != nil {
			agents, _ = s.llmConfigRepo.List(ctx)
		}
		createdTasks, summary := ExecuteTaskCreationsWithReturn(ctx, taskRequests, projectID, s.taskSvc, agents)
		if summary != "" {
			output += summary
		}
		for _, t := range createdTasks {
			if s.taskRepo != nil {
				if err := s.taskRepo.UpdateSlackOrigin(ctx, t.ID); err != nil {
					log.Printf("[slack] update slack origin failed for task=%s: %v", t.ID, err)
				}
			}
			if s.slackTaskContextRepo != nil {
				_ = s.slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{
					TaskID:         t.ID,
					SlackTeamID:    markerCtx.TeamID,
					SlackChannelID: markerCtx.ChannelID,
					SlackThreadTS:  markerCtx.ThreadTS,
					SlackUserID:    markerCtx.UserID,
				})
			}
		}
	}

	editRequests := ParseTaskEdits(output)
	if len(editRequests) > 0 {
		if editSummary := ExecuteTaskEdits(ctx, editRequests, projectID, s.taskSvc, nil, ""); editSummary != "" {
			output += editSummary
		}
	}

	execRequests := ParseTaskExecutions(output)
	if len(execRequests) > 0 {
		if execSummary := ExecuteTaskExecutions(ctx, execRequests, projectID, s.taskSvc); execSummary != "" {
			output += execSummary
		}
	}

	if s.projectRepo != nil {
		if newOutput := s.processListProjects(ctx, projectID, output); newOutput != output {
			output = newOutput
		}
	}
	if s.projectRepo != nil {
		if newOutput := s.processSwitchProject(ctx, output, markerCtx.TeamID, markerCtx.UserID); newOutput != output {
			output = newOutput
		}
	}

	if warnings := DetectMissingMarkers(originalOutput); len(warnings) > 0 {
		for _, w := range warnings {
			log.Printf("[slack] processChatMarkers exec=%s warning promised action=%s without marker=%s", execID, w.Action, w.MarkerName)
		}
		output += "\n\n---\nWarning: The assistant appeared to promise an action but did not include the required marker. The following actions were NOT performed:\n"
		for _, w := range warnings {
			output += fmt.Sprintf("- %s (no %s marker found)\n", w.Action, w.MarkerName)
		}
		output += "\nPlease retry your request."
	}

	if output != originalOutput && s.execRepo != nil {
		if updateErr := s.execRepo.UpdateOutput(ctx, execID, output); updateErr != nil {
			log.Printf("[slack] processChatMarkers update output failed: %v", updateErr)
		}
	}

	return output
}

func (s *SlackService) processListProjects(ctx context.Context, projectID, output string) string {
	if !HasListProjects(output) {
		return output
	}
	if s.projectRepo == nil {
		return output + "\n\n---\nAvailable Projects:\n- Error retrieving projects: project repository not configured"
	}

	projects, err := s.projectRepo.List(ctx)
	if err != nil {
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
				marker = " <- current"
			}
			desc := ""
			if p.Description != "" {
				desc = " - " + p.Description
			}
			sb.WriteString(fmt.Sprintf("- %s%s%s\n", p.Name, desc, marker))
		}
	}
	sb.WriteString("\nAsk me to switch projects by name when needed.\n")
	return output + sb.String()
}

func (s *SlackService) processSwitchProject(ctx context.Context, output, teamID, userID string) string {
	requests := ParseSwitchProject(output)
	if len(requests) == 0 {
		return output
	}
	if s.projectRepo == nil {
		return output + "\n\n---\nProject Switch Results:\n- Error loading projects: project repository not configured"
	}

	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return output + "\n\n---\nProject Switch Results:\n- Error loading projects: " + err.Error()
	}

	var results []string
	for _, req := range requests {
		var target *models.Project
		for i := range projects {
			if strings.EqualFold(projects[i].Name, req.Project) || projects[i].ID == req.Project {
				target = &projects[i]
				break
			}
		}
		if target == nil {
			var names []string
			for _, p := range projects {
				names = append(names, p.Name)
			}
			results = append(results, fmt.Sprintf("- Project not found: %q. Available projects: %s", req.Project, strings.Join(names, ", ")))
			continue
		}
		s.setActiveProject(ctx, teamID, userID, target.ID)
		results = append(results, fmt.Sprintf("- Switched to project: %s", target.Name))
	}

	if len(results) == 0 {
		return output
	}
	return output + "\n\n---\nProject Switch Results:\n" + strings.Join(results, "\n")
}

func (s *SlackService) setActiveProject(ctx context.Context, teamID, userID, projectID string) {
	key := slackUserProjectKey(teamID, userID)
	s.mu.Lock()
	s.userProjects[key] = projectID
	s.mu.Unlock()
	if s.slackUserProjectRepo != nil {
		if err := s.slackUserProjectRepo.SetUserProject(ctx, teamID, userID, projectID); err != nil {
			log.Printf("[slack] persist active project failed: %v", err)
		}
	}
}

func (s *SlackService) getActiveProject(ctx context.Context, teamID, userID string) string {
	key := slackUserProjectKey(teamID, userID)

	s.mu.RLock()
	if projectID, ok := s.userProjects[key]; ok {
		s.mu.RUnlock()
		return projectID
	}
	s.mu.RUnlock()

	if s.slackUserProjectRepo != nil {
		if saved, err := s.slackUserProjectRepo.GetUserProject(ctx, teamID, userID); err == nil && saved != "" {
			s.mu.Lock()
			s.userProjects[key] = saved
			s.mu.Unlock()
			return saved
		}
	}

	if s.projectRepo == nil {
		return ""
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		return ""
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
	return selected
}

func slackUserProjectKey(teamID, userID string) string {
	return strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
}

func (s *SlackService) checkAuthorization(ctx context.Context, projectID, slackUserID string) bool {
	if s.slackAuthRepo == nil {
		return true
	}

	if strings.TrimSpace(projectID) == "" {
		authorized, err := s.slackAuthRepo.IsAuthorizedAnywhere(ctx, slackUserID)
		if err != nil {
			log.Printf("[slack] auth check error for user=%s anywhere: %v", slackUserID, err)
			return true
		}
		return authorized
	}

	authorized, err := s.slackAuthRepo.IsAuthorized(ctx, projectID, slackUserID)
	if err != nil {
		log.Printf("[slack] auth check error for user=%s project=%s: %v", slackUserID, projectID, err)
		return true
	}
	if authorized {
		return true
	}

	authorizedAnywhere, err := s.slackAuthRepo.IsAuthorizedAnywhere(ctx, slackUserID)
	if err != nil {
		log.Printf("[slack] auth check error for user=%s fallback-anywhere: %v", slackUserID, err)
		return true
	}
	return authorizedAnywhere
}

func sanitizeSlackText(text string) string {
	cleaned := strings.TrimSpace(slackMentionRegex.ReplaceAllString(text, ""))
	return strings.TrimSpace(cleaned)
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
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return nil
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
			return fmt.Errorf("slack bot token is not configured")
		}
		client = slack.New(botToken)
	}

	params := slack.PostMessageParameters{}
	if strings.TrimSpace(threadTS) != "" {
		params.ThreadTimestamp = threadTS
	}
	_, _, err := client.PostMessage(channelID,
		slack.MsgOptionPostMessageParameters(params),
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		return fmt.Errorf("post slack message: %w", err)
	}
	return nil
}

func generateOAuthState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *SlackService) getSetting(ctx context.Context, key string) string {
	if s.settingsRepo == nil {
		return ""
	}
	val, _ := s.settingsRepo.Get(ctx, key)
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
