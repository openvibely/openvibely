package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

const (
	XSettingConsumerKey         = "x_consumer_key"
	XSettingConsumerSecret      = "x_consumer_secret"
	XSettingAccessToken         = "x_access_token"
	XSettingAccessTokenSecret   = "x_access_token_secret"
	XSettingPollIntervalSeconds = "x_poll_interval_seconds"
	XSettingSendResponses       = "x_send_responses"
	XSettingSinceID             = "x_mentions_since_id"
	XSettingAccountID           = "x_account_id"
	XSettingConfigurationID     = "x_configuration_id"
	xProcessTimeout             = 5 * time.Minute
	xReceiptLease               = 10 * time.Minute
	xMaxMentionPages            = 10
	xMaxWeightedPostLength      = 280
	xTransformedURLLength       = 23
	xDefaultCharacterWeight     = 2
)

var errXReceiptActive = errors.New("X mention receipt is actively leased")

type XConnectionStatus struct {
	Configured bool
	Connected  bool
	Running    bool
	Username   string
	LastError  string
}
type XAPI interface {
	Me(context.Context) (XUser, error)
	Mentions(context.Context, string, string, string) (XMentionsResponse, error)
	Post(context.Context, string, string) (string, error)
}

type XService struct {
	mu                       sync.RWMutex
	api                      XAPI
	credentials              XCredentials
	settingsRepo             *repository.SettingsRepo
	projectRepo              *repository.ProjectRepo
	llmConfigRepo            *repository.LLMConfigRepo
	taskRepo                 *repository.TaskRepo
	execRepo                 *repository.ExecutionRepo
	scheduleRepo             *repository.ScheduleRepo
	threadInputRepo          *repository.ThreadInputRepo
	authRepo                 *repository.XAuthRepo
	userProjectRepo          *repository.XUserProjectRepo
	taskContextRepo          *repository.XTaskContextRepo
	receiptRepo              *repository.XInboundReceiptRepo
	taskSvc                  *TaskService
	customPersonalityRepo    *repository.CustomPersonalityRepo
	agentRepo                *repository.AgentRepo
	chatBroadcaster          *events.ChatBroadcaster
	executionStreamHub       *events.ExecutionStreamHub
	channelChatRunner        ChannelChatRunner
	channelTaskRunner        ChannelTaskRunner
	queuedTurnPromoter       func(string)
	queuedTaskThreadPromoter func(string)
	channelMessageRouter     *ChannelMessageRouter
	ctx                      context.Context
	cancel                   context.CancelFunc
	runDone                  chan struct{}
	running                  bool
	me                       XUser
	configurationID          string
	connected                bool
	lastError                string
	pollInterval             time.Duration
	now                      func() time.Time
}

func NewXService(credentials XCredentials, settings *repository.SettingsRepo, projects *repository.ProjectRepo, configs *repository.LLMConfigRepo, tasks *repository.TaskRepo, execs *repository.ExecutionRepo, schedules *repository.ScheduleRepo, taskSvc *TaskService) *XService {
	ctx, cancel := context.WithCancel(context.Background())
	return &XService{api: NewXAPIClient(credentials), credentials: credentials, settingsRepo: settings, projectRepo: projects, llmConfigRepo: configs, taskRepo: tasks, execRepo: execs, scheduleRepo: schedules, taskSvc: taskSvc, ctx: ctx, cancel: cancel, pollInterval: 30 * time.Second, now: time.Now}
}
func (s *XService) SetRepositories(auth *repository.XAuthRepo, selections *repository.XUserProjectRepo, contexts *repository.XTaskContextRepo, receipts *repository.XInboundReceiptRepo, inputs *repository.ThreadInputRepo) {
	s.authRepo = auth
	s.userProjectRepo = selections
	s.taskContextRepo = contexts
	s.receiptRepo = receipts
	s.threadInputRepo = inputs
}
func (s *XService) SetRuntime(agent *repository.AgentRepo, personalities *repository.CustomPersonalityRepo, broadcaster *events.ChatBroadcaster, hub *events.ExecutionStreamHub, chatRunner ChannelChatRunner, taskRunner ChannelTaskRunner, chatPromoter, taskPromoter func(string), router *ChannelMessageRouter) {
	s.agentRepo = agent
	s.customPersonalityRepo = personalities
	s.chatBroadcaster = broadcaster
	s.executionStreamHub = hub
	s.channelChatRunner = chatRunner
	s.channelTaskRunner = taskRunner
	s.queuedTurnPromoter = chatPromoter
	s.queuedTaskThreadPromoter = taskPromoter
	s.channelMessageRouter = router
}
func (s *XService) setAPI(api XAPI) { s.api = api }

// SetAPI replaces the provider transport before the service starts. It is used
// by settings reconfiguration so verification and the running service share one
// credential-bound client.
func (s *XService) SetAPI(api XAPI) { s.api = api }

// SetConfigurationID binds this service instance to one persisted configuration
// generation before it is made authoritative and starts polling.
func (s *XService) SetConfigurationID(id string) { s.configurationID = strings.TrimSpace(id) }

// PrepareConnection verifies both authenticated identity and mention-read
// capability, returning the newest current mention as the safe initial cursor.
func (s *XService) PrepareConnection(ctx context.Context) (XUser, string, error) {
	if !s.credentials.Ready() {
		return XUser{}, "", fmt.Errorf("X OAuth 1.0a credentials are incomplete")
	}
	me, err := s.api.Me(ctx)
	if err != nil {
		return XUser{}, "", err
	}
	page, err := s.api.Mentions(ctx, me.ID, "", "")
	if err != nil {
		return XUser{}, "", fmt.Errorf("verify X mention access: %w", err)
	}
	return me, page.Meta.NewestID, nil
}

func (s *XService) Start() error {
	me, _, err := s.PrepareConnection(s.ctx)
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		return err
	}
	if s.settingsRepo != nil {
		values, err := s.settingsRepo.GetMany(s.ctx, []string{XSettingAccountID, XSettingConfigurationID})
		if err != nil {
			return fmt.Errorf("load X configuration identity: %w", err)
		}
		accountID := values[XSettingAccountID]
		configurationID := strings.TrimSpace(values[XSettingConfigurationID])
		updates := map[string]string{}
		if strings.TrimSpace(accountID) == "" {
			updates[XSettingAccountID] = me.ID
		} else if accountID != me.ID {
			return fmt.Errorf("configured X account does not match persisted mention cursor")
		}
		if configurationID == "" {
			configurationID = repository.NewID()
			updates[XSettingConfigurationID] = configurationID
		}
		if err := s.settingsRepo.SetMany(s.ctx, updates); err != nil {
			return fmt.Errorf("save X configuration identity: %w", err)
		}
		s.SetConfigurationID(configurationID)
	}
	return s.StartVerified(me)
}

// StartVerified starts polling after PrepareConnection has succeeded. It makes
// no provider call, so a settings transaction can commit before activation
// without a second verification race.
func (s *XService) StartVerified(me XUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if !s.credentials.Ready() {
		return fmt.Errorf("X OAuth 1.0a credentials are incomplete")
	}
	if strings.TrimSpace(me.ID) == "" {
		return fmt.Errorf("X authenticated user is required")
	}
	s.me = me
	s.connected = true
	s.lastError = ""
	s.running = true
	s.runDone = make(chan struct{})
	go s.poll(s.ctx, s.runDone)
	return nil
}
func (s *XService) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.cancel()
	done := s.runDone
	s.running = false
	s.connected = false
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (s *XService) Status() XConnectionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return XConnectionStatus{Configured: s.credentials.Ready(), Connected: s.connected, Running: s.running, Username: s.me.Username, LastError: s.lastError}
}
func (s *XService) TestConnection(ctx context.Context) (XUser, error) {
	me, _, err := s.PrepareConnection(ctx)
	return me, err
}
func (s *XService) SetPollInterval(d time.Duration) {
	if d >= 15*time.Second {
		s.pollInterval = d
	}
}

func (s *XService) recordPollResult(err error) {
	if errors.Is(err, errXReceiptActive) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.connected = false
		s.lastError = err.Error()
		return
	}
	s.connected = true
	s.lastError = ""
}

func (s *XService) poll(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		err := s.pollOnce(ctx)
		if ctx.Err() == nil {
			s.recordPollResult(err)
			if err != nil {
				applog.Infof("[x] mention polling failed: %v", err)
			}
		}
		timer := time.NewTimer(s.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *XService) configurationGuards() map[string]string {
	return map[string]string{
		XSettingAccountID:       s.me.ID,
		XSettingConfigurationID: s.configurationID,
	}
}

func (s *XService) pollingSettings(ctx context.Context) (map[string]string, error) {
	if s.settingsRepo == nil {
		return nil, fmt.Errorf("X settings repository is not configured")
	}
	values, err := s.settingsRepo.GetMany(ctx, []string{XSettingSinceID, XSettingAccountID, XSettingConfigurationID})
	if err != nil {
		return nil, fmt.Errorf("load X polling settings: %w", err)
	}
	for key, expected := range s.configurationGuards() {
		if values[key] != expected {
			return nil, fmt.Errorf("X configuration changed during polling")
		}
	}
	return values, nil
}

func (s *XService) requireConfigurationWithExecutor(ctx context.Context, exec repository.SQLExecutor) error {
	matches, err := s.settingsRepo.MatchesWithExecutor(ctx, exec, s.configurationGuards())
	if err != nil {
		return fmt.Errorf("verify X configuration during durable handoff: %w", err)
	}
	if !matches {
		return fmt.Errorf("X configuration changed during durable handoff")
	}
	return nil
}

func (s *XService) pollOnce(ctx context.Context) error {
	if s.receiptRepo == nil || s.authRepo == nil || s.projectRepo == nil {
		return fmt.Errorf("X channel persistence is not configured")
	}
	pollingSettings, err := s.pollingSettings(ctx)
	if err != nil {
		return err
	}
	sinceID := pollingSettings[XSettingSinceID]
	pagination := ""
	newest := ""
	var mentions []XTweet
	users := map[string]XUser{}
	for pageNumber := 0; pageNumber < xMaxMentionPages; pageNumber++ {
		page, err := s.api.Mentions(ctx, s.me.ID, sinceID, pagination)
		if err != nil {
			return err
		}
		if newest == "" {
			newest = page.Meta.NewestID
		}
		mentions = append(mentions, page.Data...)
		for _, u := range page.Includes.Users {
			users[u.ID] = u
		}
		pagination = page.Meta.NextToken
		if pagination == "" {
			break
		}
	}
	if pagination != "" {
		return fmt.Errorf("X mention pagination exceeded %d pages", xMaxMentionPages)
	}
	sort.SliceStable(mentions, func(i, j int) bool { return xTweetIDLess(mentions[i].ID, mentions[j].ID) })
	for _, tweet := range mentions {
		if strings.TrimSpace(tweet.AuthorID) == "" {
			return fmt.Errorf("X mention %s is missing immutable author_id", tweet.ID)
		}
		if tweet.AuthorID == s.me.ID {
			continue
		}
		if _, err := s.pollingSettings(ctx); err != nil {
			return err
		}
		user := users[tweet.AuthorID]
		// The immutable tweet author_id is the authorization identity. Expanded
		// profile metadata is optional and must never erase that identity.
		user.ID = tweet.AuthorID
		if ok, err := s.processMention(ctx, tweet, user); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("X mention %s was not durably handed off", tweet.ID)
		}
	}
	if newest != "" {
		updated, err := s.settingsRepo.CompareAndSet(ctx, XSettingSinceID, sinceID, newest, s.configurationGuards())
		if err != nil {
			return fmt.Errorf("save X mention cursor: %w", err)
		}
		if !updated {
			values, loadErr := s.pollingSettings(ctx)
			if loadErr != nil {
				return loadErr
			}
			if values[XSettingSinceID] != sinceID {
				// Another poller for this exact configuration advanced the cursor
				// first. Receipt deduplication makes this handoff safe.
				return nil
			}
			return fmt.Errorf("X mention cursor changed during polling")
		}
	}
	return nil
}
func xTweetIDLess(a, b string) bool {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func stripXSelfMentions(text, username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return text
	}
	for i := 0; i < len(username); i++ {
		if !isXHandleCharacter(username[i]) {
			return text
		}
	}

	var stripped strings.Builder
	last := 0
	search := 0
	removed := false
	for search < len(text) {
		relative := strings.IndexByte(text[search:], '@')
		if relative < 0 {
			break
		}
		start := search + relative
		end := start + 1 + len(username)
		if (start > 0 && isXHandleCharacter(text[start-1])) || end > len(text) {
			search = start + 1
			continue
		}
		if !strings.EqualFold(text[start+1:end], username) ||
			(end < len(text) && isXHandleCharacter(text[end])) {
			search = start + 1
			continue
		}
		if !removed {
			stripped.Grow(len(text))
			removed = true
		}
		stripped.WriteString(text[last:start])
		last = end
		search = end
	}
	if !removed {
		return text
	}
	stripped.WriteString(text[last:])
	return stripped.String()
}

func isXHandleCharacter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func (s *XService) processMention(ctx context.Context, tweet XTweet, user XUser) (bool, error) {
	projectID, err := s.projectForUser(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if projectID == "" {
		applog.Infof("[x] ignored unauthorized user id=%s", user.ID)
		return true, nil
	}
	claim, err := s.receiptRepo.Claim(ctx, tweet.ID, projectID, s.now(), xReceiptLease)
	if err != nil {
		return false, err
	}
	switch claim.Result {
	case repository.XReceiptCompleted:
		return true, nil
	case repository.XReceiptActive:
		// Another poller for this exact configuration may still be between
		// receipt claim and durable ingress. Keep the cursor retryable without
		// degrading provider readiness, but never grant this treatment to a
		// replaced generation.
		if _, err := s.pollingSettings(ctx); err != nil {
			return false, err
		}
		return false, errXReceiptActive
	case repository.XReceiptClaimed:
	default:
		return false, fmt.Errorf("unexpected X receipt claim state %q", claim.Result)
	}
	text := strings.TrimSpace(stripXSelfMentions(tweet.Text, s.me.Username))
	if text == "" {
		_, err := s.receiptRepo.CompleteWithHandoff(ctx, tweet.ID, claim.Token, nil, func(exec repository.SQLExecutor) error {
			return s.requireConfigurationWithExecutor(ctx, exec)
		})
		if err != nil {
			_ = s.receiptRepo.Release(context.Background(), tweet.ID, claim.Token)
		}
		return err == nil, err
	}
	handed, _ := s.ingestMention(ctx, projectID, tweet, user, text, claim.Token)
	if !handed {
		_ = s.receiptRepo.Release(context.Background(), tweet.ID, claim.Token)
		return false, nil
	}
	return true, nil
}
func (s *XService) projectForUser(ctx context.Context, userID string) (string, error) {
	if s.userProjectRepo != nil {
		if id, err := s.userProjectRepo.GetUserProject(ctx, userID); err != nil {
			return "", err
		} else if id != "" {
			ok, err := s.authRepo.IsAuthorized(ctx, id, userID)
			if err != nil {
				return "", err
			}
			if ok {
				return id, nil
			}
		}
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		ok, err := s.authRepo.IsAuthorized(ctx, p.ID, userID)
		if err != nil {
			return "", err
		}
		if ok {
			return p.ID, nil
		}
	}
	return "", nil
}
func (s *XService) ingestMention(ctx context.Context, projectID string, tweet XTweet, user XUser, text, receiptToken string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, xProcessTimeout)
	defer cancel()
	handed := false
	taskID := ""
	conversationID := tweet.ConversationID
	if conversationID == "" {
		conversationID = tweet.ID
	}
	completeWithHandoff := func(ctx context.Context, taskID *string, persist func(repository.SQLExecutor) error) (bool, error) {
		return s.receiptRepo.CompleteWithHandoff(ctx, tweet.ID, receiptToken, taskID, func(exec repository.SQLExecutor) error {
			if err := s.requireConfigurationWithExecutor(ctx, exec); err != nil {
				return err
			}
			if persist != nil {
				return persist(exec)
			}
			return nil
		})
	}
	runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform: "x", ProjectID: projectID, Message: text, Source: models.TaskOriginX, Surface: chatcontrol.SurfaceX, Start: s.now(),
		TaskRepo: s.taskRepo, ExecRepo: s.execRepo, ThreadInputRepo: s.threadInputRepo, LLMConfigRepo: s.llmConfigRepo,
		ScheduleRepo: s.scheduleRepo, AgentRepo: s.agentRepo, SettingsRepo: s.settingsRepo, CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo: s.projectRepo, TaskSvc: s.taskSvc, ChatBroadcaster: s.chatBroadcaster,
		FindActiveExecution: s.execRepo.FindLatestActiveChatExecution,
		NewQueuedInput: func() *models.ThreadInput {
			return &models.ThreadInput{XAccountID: s.me.ID, XConversationID: conversationID, XReplyToTweetID: tweet.ID, XUserID: user.ID, XUsername: user.Username}
		}, CreateQueuedInput: func(ctx context.Context, input *models.ThreadInput) (bool, error) {
			if s.threadInputRepo == nil {
				return false, fmt.Errorf("thread input repository is not configured")
			}
			return completeWithHandoff(ctx, nil, func(exec repository.SQLExecutor) error {
				return s.threadInputRepo.CreateQueuedWithExecutor(ctx, exec, input)
			})
		},
		OnDurableHandoff: func() { handed = true },
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: &models.Task{Title: fmt.Sprintf("X @%s: %s", user.Username, util.Truncate(text, 48)), CreatedVia: models.TaskOriginX},
			RuntimeToolsForTask: func(string) *llmcontracts.RuntimeTools {
				return s.runtimeTools(taskID, projectID, s.me.ID, user.ID, conversationID, tweet.ID, user.Username)
			},
			ReplyContext: ChannelReplyContext{Source: models.TaskOriginX, XAccountID: s.me.ID, XConversationID: conversationID, XReplyToTweetID: tweet.ID, XUserID: user.ID, XUsername: user.Username}, ChannelChatRunner: s.channelChatRunner,
			CompleteExecution: channelCompletionFunc("x", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
			CreateDurableFirstTurn: func(ctx context.Context, task *models.Task, execution *models.Execution, _ []models.ChatAttachment) (bool, error) {
				if s.taskContextRepo == nil {
					return false, fmt.Errorf("X task context repository is not configured")
				}
				return completeWithHandoff(ctx, &task.ID, func(exec repository.SQLExecutor) error {
					if err := s.taskRepo.CreateWithExecutor(ctx, exec, task); err != nil {
						return err
					}
					taskID = task.ID
					if err := s.taskContextRepo.UpsertWithExecutor(ctx, exec, &models.XTaskContext{TaskID: task.ID, ProjectID: projectID, AccountID: s.me.ID, ConversationID: conversationID, ReplyToTweetID: tweet.ID, XUserID: user.ID, Username: user.Username}); err != nil {
						return err
					}
					execution.TaskID = task.ID
					return s.execRepo.CreateWithExecutor(ctx, exec, execution)
				})
			},
			ListChatHistory: func(ctx context.Context, pid string) ([]models.Execution, error) {
				return s.execRepo.ListChatHistory(ctx, pid, 50)
			},
			FilterChatHistory: filterXChatHistory,
		},
	})
	return handed, taskID
}
func filterXChatHistory(execs []models.Execution, current string) []models.Execution {
	out := make([]models.Execution, 0, len(execs))
	for _, e := range execs {
		if e.ID != current && e.Status != models.ExecRunning {
			out = append(out, e)
		}
	}
	return out
}

func mergeSelectedChannelRuntimeActionHandlers(dst, src map[string]chatcontrol.RuntimeActionHandler, names ...string) {
	for _, name := range names {
		if handler := src[name]; handler != nil {
			dst[name] = handler
		}
	}
}

// RuntimeTools builds the X-identity-sensitive runtime overrides. The Handler
// composes these ahead of its complete generic runtime so unrelated actions keep
// their full application dependencies.
func (s *XService) RuntimeTools(callerTaskID, projectID, accountID, userID, conversationID, replyToTweetID, username string) *llmcontracts.RuntimeTools {
	return s.runtimeTools(callerTaskID, projectID, accountID, userID, conversationID, replyToTweetID, username)
}

func (s *XService) runtimeTools(callerTaskID, projectID, accountID, userID, conversationID, replyToTweetID, username string) *llmcontracts.RuntimeTools {
	handlers := map[string]chatcontrol.RuntimeActionHandler{}
	taskHandlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID: projectID,
		TaskSvc:   s.taskSvc, TaskRepo: s.taskRepo, ExecRepo: s.execRepo,
		ThreadInputRepo: s.threadInputRepo, ExecutionStreamHub: s.executionStreamHub,
		LLMConfigRepo: s.llmConfigRepo, SwarmSvc: swarmFromTaskService(s.taskSvc),
		OnTasksCreated: func(ctx context.Context, _ []TaskCreationRequest, tasks []models.Task) error {
			if s.taskContextRepo == nil {
				return fmt.Errorf("X task context repository is not configured")
			}
			for _, task := range tasks {
				if err := s.taskRepo.UpdateXOrigin(ctx, task.ID); err != nil {
					return err
				}
				if err := s.taskContextRepo.Upsert(ctx, &models.XTaskContext{TaskID: task.ID, ProjectID: projectID, AccountID: accountID, ConversationID: conversationID, ReplyToTweetID: replyToTweetID, XUserID: userID, Username: username}); err != nil {
					return err
				}
			}
			return nil
		},
	})
	mergeSelectedChannelRuntimeActionHandlers(handlers, taskHandlers, "create_task", "create_swarm_task")
	threadHandlers := buildChannelThreadActionHandlers(channelThreadActionHandlerOptions{
		Platform: "x", ProjectID: projectID, Surface: chatcontrol.SurfaceX, Source: models.TaskOriginX, ActorID: userID,
		TaskRepo: s.taskRepo, ExecRepo: s.execRepo, ThreadInputRepo: s.threadInputRepo, LLMConfigRepo: s.llmConfigRepo,
		SettingsRepo: s.settingsRepo, CustomPersonalityRepo: s.customPersonalityRepo, ChannelTaskRunner: s.channelTaskRunner,
		RuntimeToolsForTask: func(taskID string) *llmcontracts.RuntimeTools {
			return s.runtimeTools(taskID, projectID, accountID, userID, conversationID, replyToTweetID, username)
		},
		QueuedTaskThreadPromoter: s.queuedTaskThreadPromoter,
		CompleteExecution:        channelCompletionFunc("x", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
		ChannelMessageRouter:     s.channelMessageRouter,
		ReplyContext:             ChannelReplyContext{Source: models.TaskOriginX, XAccountID: accountID, XConversationID: conversationID, XReplyToTweetID: replyToTweetID, XUserID: userID, XUsername: username},
		NewQueuedInput: func(_ *models.Task, _, _ string) *models.ThreadInput {
			return &models.ThreadInput{XAccountID: accountID, XConversationID: conversationID, XReplyToTweetID: replyToTweetID, XUserID: userID, XUsername: username}
		},
		FilterHistory: filterXChatHistory,
	})
	mergeSelectedChannelRuntimeActionHandlers(handlers, threadHandlers, "send_to_task")
	projectHandlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID: projectID, ProjectRepo: s.projectRepo,
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			if project == nil || s.authRepo == nil || s.userProjectRepo == nil {
				return fmt.Errorf("X project switching is unavailable")
			}
			allowed, err := s.authRepo.IsAuthorized(ctx, project.ID, userID)
			if err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("X user is not authorized for project %s", project.Name)
			}
			return s.userProjectRepo.SetUserProject(ctx, userID, project.ID)
		},
	})
	mergeSelectedChannelRuntimeActionHandlers(handlers, projectHandlers, "switch_project")
	mergeChannelRuntimeActionHandlers(handlers, buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{ChannelDisplayName: "X", ProjectID: projectID, ProjectRepo: s.projectRepo}))

	defs := make([]llmcontracts.RuntimeToolDefinition, 0, len(handlers))
	for _, def := range actionToolDefinitions(chatcontrol.SurfaceX, true) {
		if _, ok := handlers[def.Name]; ok {
			defs = append(defs, def)
		}
	}
	return &llmcontracts.RuntimeTools{Definitions: defs, Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		handler, ok := handlers[name]
		if !ok {
			return "", false, false, nil
		}
		out, err := handler(ctx, input)
		return out, true, err != nil, err
	}}
}

func (s *XService) SendOutboundMessage(ctx context.Context, targetID, threadID, text string) SendMessageResult {
	if threadID != "" {
		return sendMessageError("X outbound targets do not support thread IDs")
	}
	if targetID != "me" {
		return sendMessageError("X outbound targets only support the authenticated account target x:me")
	}
	if xWeightedPostLength(text) > xMaxWeightedPostLength {
		return sendMessageError("X posts are limited to 280 weighted characters")
	}
	id, err := s.api.Post(ctx, text, "")
	if err != nil {
		return sendMessageError(err.Error())
	}
	return SendMessageResult{OK: true, Platform: "x", Target: "x:" + targetID, MessageID: id}
}
func (s *XService) SendReplyForAccount(ctx context.Context, accountID, replyTo, output, errMsg string) bool {
	s.mu.RLock()
	matches := strings.TrimSpace(accountID) != "" && accountID == s.me.ID
	s.mu.RUnlock()
	if !matches {
		return false
	}
	s.SendReply(ctx, replyTo, output, errMsg)
	return true
}

func (s *XService) SendReply(ctx context.Context, replyTo, output, errMsg string) {
	if s.settingsRepo == nil {
		return
	}
	enabled, err := s.settingsRepo.Get(ctx, XSettingSendResponses)
	if err != nil || strings.EqualFold(strings.TrimSpace(enabled), "false") {
		return
	}
	text := strings.TrimSpace(output)
	if errMsg != "" {
		text = "Error: " + strings.TrimSpace(errMsg)
	}
	text = truncateXPost(text)
	if text == "" {
		return
	}
	if _, err := s.api.Post(ctx, text, replyTo); err != nil {
		applog.Infof("[x] failed to send reply: %v", err)
	}
}
func (s *XService) SendChatResponse(ctx context.Context, task models.Task, output, errMsg string) {
	if s.taskContextRepo == nil {
		return
	}
	meta, err := s.taskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || meta == nil {
		return
	}
	s.SendReplyForAccount(ctx, meta.AccountID, meta.ReplyToTweetID, output, errMsg)
}

var xURLPattern = regexp.MustCompile(`(?i)(https?://|www\.)[^\s<>"']+|([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(:[0-9]+)?([/?#][^\s<>"']*)?`)

// xWeightedPostLength follows the current twitter-text v3 configuration:
// https://github.com/twitter/twitter-text/blob/master/config/v3.json
func xWeightedPostLength(text string) int {
	urls := xURLPattern.FindAllStringIndex(text, -1)
	weighted := 0
	offset := 0
	for _, match := range urls {
		start, end := match[0], trimXURL(text, match[0], match[1])
		if start < offset || end <= start || !isXURLStart(text, start) {
			continue
		}
		weighted += xWeightedCharacters(text[offset:start])
		weighted += xTransformedURLLength
		offset = end
	}
	return weighted + xWeightedCharacters(text[offset:])
}

func isXURLStart(text string, start int) bool {
	if start == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(text[:start])
	if unicode.IsLetter(previous) || unicode.IsDigit(previous) {
		return false
	}
	switch previous {
	case '_', '@', '＠', '$', '#', '＃':
		return false
	default:
		return true
	}
}

func trimXURL(text string, start, end int) int {
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		switch r {
		case '.', ',', '!', '?', ':', ';':
			end -= size
			continue
		case ')':
			if strings.Count(text[start:end], ")") > strings.Count(text[start:end], "(") {
				end -= size
				continue
			}
		case ']':
			if strings.Count(text[start:end], "]") > strings.Count(text[start:end], "[") {
				end -= size
				continue
			}
		case '}':
			if strings.Count(text[start:end], "}") > strings.Count(text[start:end], "{") {
				end -= size
				continue
			}
		}
		break
	}
	return end
}

func xWeightedCharacters(text string) int {
	weighted := 0
	for offset := 0; offset < len(text); {
		if end := xEmojiSequenceEnd(text, offset); end > offset {
			weighted += xDefaultCharacterWeight
			offset = end
			continue
		}
		r, size := utf8.DecodeRuneInString(text[offset:])
		if size == 0 {
			break
		}
		weighted += xCharacterWeight(r)
		offset += size
	}
	return weighted
}

func xCharacterWeight(r rune) int {
	// These ranges are the one-unit ranges from twitter-text's v3 config;
	// characters outside them have the two-unit default weight.
	if r >= 0 && r <= 4351 ||
		r >= 8192 && r <= 8205 ||
		r >= 8208 && r <= 8223 ||
		r >= 8242 && r <= 8247 {
		return 1
	}
	return xDefaultCharacterWeight
}

func xEmojiSequenceEnd(text string, start int) int {
	r, size := utf8.DecodeRuneInString(text[start:])
	if size == 0 {
		return 0
	}
	if isXRegionalIndicator(r) {
		end := start + size
		next, nextSize := utf8.DecodeRuneInString(text[end:])
		if isXRegionalIndicator(next) {
			end += nextSize
		}
		return end
	}
	if isXKeycapBase(r) {
		end := consumeXEmojiModifiers(text, start+size)
		next, nextSize := utf8.DecodeRuneInString(text[end:])
		if next == '\u20e3' {
			return end + nextSize
		}
		return 0
	}
	end := xEmojiBaseEnd(text, start)
	if end == 0 {
		return 0
	}
	for end < len(text) {
		joiner, joinerSize := utf8.DecodeRuneInString(text[end:])
		if joiner != '\u200d' {
			break
		}
		nextEnd := xEmojiBaseEnd(text, end+joinerSize)
		if nextEnd == 0 {
			break
		}
		end = nextEnd
	}
	return end
}

func xEmojiBaseEnd(text string, start int) int {
	r, size := utf8.DecodeRuneInString(text[start:])
	if !isXEmojiBase(r) {
		return 0
	}
	return consumeXEmojiModifiers(text, start+size)
}

func consumeXEmojiModifiers(text string, start int) int {
	for start < len(text) {
		r, size := utf8.DecodeRuneInString(text[start:])
		if r != '\ufe0e' && r != '\ufe0f' && !isXEmojiModifier(r) && !(r >= 0xe0020 && r <= 0xe007f) {
			break
		}
		start += size
	}
	return start
}

func isXEmojiBase(r rune) bool {
	switch {
	case r >= 0x1f000 && r <= 0x1faff:
	case r >= 0x2600 && r <= 0x27bf:
	case r == 0x00a9 || r == 0x00ae || r == 0x203c || r == 0x2049:
	case r == 0x2122 || r == 0x2139:
	case r >= 0x2194 && r <= 0x2199:
	case r == 0x21a9 || r == 0x21aa:
	case r >= 0x231a && r <= 0x231b:
	case r == 0x2328 || r == 0x23cf:
	case r >= 0x23e9 && r <= 0x23f3:
	case r >= 0x23f8 && r <= 0x23fa:
	case r == 0x24c2:
	case r >= 0x25aa && r <= 0x25ab:
	case r == 0x25b6 || r == 0x25c0:
	case r >= 0x25fb && r <= 0x25fe:
	case r >= 0x2934 && r <= 0x2935:
	case r >= 0x2b05 && r <= 0x2b07:
	case r == 0x2b1b || r == 0x2b1c || r == 0x2b50 || r == 0x2b55:
	case r == 0x3030 || r == 0x303d || r == 0x3297 || r == 0x3299:
	default:
		return false
	}
	return true
}

func isXEmojiModifier(r rune) bool {
	return r >= 0x1f3fb && r <= 0x1f3ff
}

func isXRegionalIndicator(r rune) bool {
	return r >= 0x1f1e6 && r <= 0x1f1ff
}

func isXKeycapBase(r rune) bool {
	return r == '#' || r == '*' || r >= '0' && r <= '9'
}

func truncateXPost(v string) string {
	text := strings.TrimSpace(v)
	if xWeightedPostLength(text) <= xMaxWeightedPostLength {
		return text
	}

	end := 0
	for end < len(text) {
		_, size := utf8.DecodeRuneInString(text[end:])
		next := end + size
		if emojiEnd := xEmojiSequenceEnd(text, end); emojiEnd > end {
			next = emojiEnd
		}
		if xWeightedPostLength(text[:next]+"…") > xMaxWeightedPostLength {
			break
		}
		end = next
	}
	return text[:end] + "…"
}
