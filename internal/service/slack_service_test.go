package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"
)

var slackTestPNGBytes = func() []byte {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		panic(err)
	}
	return data
}()

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func slackTestURL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "https://files.slack.com" + path
}

func useSlackFileServer(t *testing.T, svc *SlackService, server *httptest.Server) {
	t.Helper()
	useSlackFileServers(t, svc, map[string]*httptest.Server{
		"files.slack.com": server,
	})
}

func useSlackFileServers(t *testing.T, svc *SlackService, servers map[string]*httptest.Server) {
	t.Helper()
	serverURLs := make(map[string]*url.URL, len(servers))
	for host, server := range servers {
		serverURL, err := url.Parse(server.URL)
		require.NoError(t, err)
		serverURLs[host] = serverURL
	}
	transport := http.DefaultTransport
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if serverURL := serverURLs[req.URL.Hostname()]; serverURL != nil {
			rewritten := req.Clone(req.Context())
			rewritten.URL = cloneURL(req.URL)
			rewritten.URL.Scheme = serverURL.Scheme
			rewritten.URL.Host = serverURL.Host
			return transport.RoundTrip(rewritten)
		}
		return transport.RoundTrip(req)
	})}
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	copied := *u
	return &copied
}

func TestSlackService_GetConnectionStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingAppToken, "xapp-1"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotToken, "xoxb-1"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingTeamName, "OpenVibely"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	status, err := svc.GetConnectionStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.Configured)
	require.True(t, status.Connected)
	require.Equal(t, "OpenVibely", status.TeamName)
}

func TestSlackService_GetConnectionStatus_ManualOverrideSource(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingAppToken, "xapp-1"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotTokenOverride, "xoxb-manual-1"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	status, err := svc.GetConnectionStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.Configured)
	require.True(t, status.Connected)
	require.Equal(t, SlackBotTokenSourceManual, status.BotTokenSource)
	require.True(t, status.HasBotTokenOverride)
}

func TestSlackService_ConnectURLStoresState(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	u, err := svc.ConnectURL(context.Background(), "http://localhost:8080/channels/slack/callback")
	require.NoError(t, err)
	require.Contains(t, u, "oauth/v2/authorize")

	state, err := settingsRepo.Get(context.Background(), SlackSettingOAuthState)
	require.NoError(t, err)
	require.NotEmpty(t, state)
	require.Contains(t, u, "state=")
}

func TestSlackService_HandleOAuthCallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingOAuthState, "state-123"))

	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/oauth.v2.access", r.URL.Path)
		_ = r.ParseForm()
		require.Equal(t, "cid", r.FormValue("client_id"))
		require.Equal(t, "secret", r.FormValue("client_secret"))
		fmt.Fprint(w, `{"ok":true,"access_token":"xoxb-123","bot_user_id":"U123","team":{"id":"T123","name":"OpenVibely"}}`)
	}))
	defer oauthSrv.Close()

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.oauthBaseURL = oauthSrv.URL

	err := svc.HandleOAuthCallback(context.Background(), "code-1", "state-123", "http://localhost:8080/channels/slack/callback")
	require.NoError(t, err)

	botToken, _ := settingsRepo.Get(context.Background(), SlackSettingBotToken)
	teamID, _ := settingsRepo.Get(context.Background(), SlackSettingTeamID)
	teamName, _ := settingsRepo.Get(context.Background(), SlackSettingTeamName)
	require.Equal(t, "xoxb-123", botToken)
	require.Equal(t, "T123", teamID)
	require.Equal(t, "OpenVibely", teamName)
}

func TestSlackService_HandleOAuthCallbackInvalidState(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingOAuthState, "expected-state"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	err := svc.HandleOAuthCallback(context.Background(), "code-1", "wrong-state", "http://localhost:8080/channels/slack/callback")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid oauth state")
}

func slackSocketMessageEvent(envelopeID, timestamp string) socketmode.Event {
	request := &socketmode.Request{EnvelopeID: envelopeID}
	return socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: request,
		Data: slackevents.EventsAPIEvent{
			Type:   slackevents.CallbackEvent,
			TeamID: "T1",
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.MessageEvent{
				ChannelType: "im",
				User:        "U1",
				Channel:     "D1",
				Text:        "persist this",
				TimeStamp:   timestamp,
			}},
		},
	}
}

func newSlackSocketIngressTestService(t *testing.T) (*SlackService, *sql.DB, *repository.TaskRepo, *repository.ExecutionRepo, *repository.ThreadInputRepo, *repository.SlackInboundReceiptRepo, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	receiptRepo := repository.NewSlackInboundReceiptRepo(db)
	project := &models.Project{Name: "Slack durable handoff"}
	require.NoError(t, projectRepo.Create(ctx, project))
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, repository.NewSlackUserProjectRepo(db), repository.NewSlackTaskContextRepo(db), nil)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetSlackInboundReceiptRepo(receiptRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	svc.postMessageFn = func(string, string, string) (string, error) { return "", nil }
	return svc, db, taskRepo, execRepo, threadInputRepo, receiptRepo, project.ID
}

func TestSlackInboundReceiptHandoffIsAtomicAndIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	receipts := repository.NewSlackInboundReceiptRepo(db)
	eventKey := "T1|D1|1710000000.100000|U1"

	alreadyHandedOff, err := receipts.WithHandoff(ctx, eventKey, func(repository.SQLExecutor) error {
		return fmt.Errorf("simulated durable row failure")
	})
	require.Error(t, err)
	require.False(t, alreadyHandedOff)
	exists, err := receipts.Exists(ctx, eventKey)
	require.NoError(t, err)
	require.False(t, exists, "failed durable work must roll back its receipt")

	persistCalls := 0
	alreadyHandedOff, err = receipts.WithHandoff(ctx, eventKey, func(repository.SQLExecutor) error {
		persistCalls++
		return nil
	})
	require.NoError(t, err)
	require.False(t, alreadyHandedOff)

	alreadyHandedOff, err = receipts.WithHandoff(ctx, eventKey, func(repository.SQLExecutor) error {
		persistCalls++
		return nil
	})
	require.NoError(t, err)
	require.True(t, alreadyHandedOff)
	require.Equal(t, 1, persistCalls)
}

func TestSlackService_SocketEventAuthorizationFailureRedelivery(t *testing.T) {
	svc, db, taskRepo, _, _, _, projectID := newSlackSocketIngressTestService(t)
	ctx := context.Background()
	svc.slackAuthRepo = repository.NewSlackAuthRepo(db)
	require.NoError(t, svc.slackAuthRepo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID: projectID, SlackUserID: "U1", DisplayName: "Authorized user", AddedBy: "test",
	}))
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	event := slackSocketMessageEvent("E-auth-retry", "1710000000.450000")

	require.NoError(t, func() error {
		_, err := db.ExecContext(ctx, `ALTER TABLE slack_authorized_users RENAME TO slack_authorized_users_unavailable`)
		return err
	}())
	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 0, acks, "authorization lookup failure must remain retryable")
	tasks, err := taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Empty(t, tasks, "authorization lookup failure must not create durable work")

	require.NoError(t, func() error {
		_, err := db.ExecContext(ctx, `ALTER TABLE slack_authorized_users_unavailable RENAME TO slack_authorized_users`)
		return err
	}())
	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 1, acks)
	tasks, err = taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 2, acks, "successful redelivery must be durably deduplicated")
	tasks, err = taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	deniedEvent := slackSocketMessageEvent("E-auth-denied", "1710000000.460000")
	deniedAPI := deniedEvent.Data.(slackevents.EventsAPIEvent)
	deniedMessage := deniedAPI.InnerEvent.Data.(slackevents.MessageEvent)
	deniedMessage.User = "U_DENIED"
	deniedAPI.InnerEvent.Data = deniedMessage
	deniedEvent.Data = deniedAPI
	svc.handleSocketEvent(ctx, nil, deniedEvent)
	require.Equal(t, 3, acks, "successful negative authorization must be terminally acknowledged")
	tasks, err = taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1, "denied event must not create durable work")
}

func TestSlackService_SocketEventFirstTurnAttachmentPersistenceFailureRedelivery(t *testing.T) {
	svc, db, taskRepo, _, _, receiptRepo, projectID := newSlackSocketIngressTestService(t)
	svc.SetUploadsDir(t.TempDir())
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	downloads := 0
	var downloadedPaths []string
	svc.downloadSlackAttachmentsFn = func(context.Context, []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
		downloads++
		dir := t.TempDir()
		path := filepath.Join(dir, "evidence.txt")
		downloadedPaths = append(downloadedPaths, path)
		require.NoError(t, os.WriteFile(path, []byte("durable evidence"), 0o600))
		return "\nFile: evidence.txt\n", nil, []models.ChatAttachment{{
			FileName: "evidence.txt", FilePath: path, MediaType: "text/plain", FileSize: 16,
		}}, nil
	}

	event := slackSocketMessageEvent("E-attachment-persist", "1710000000.400000")
	message := event.Data.(slackevents.EventsAPIEvent).InnerEvent.Data.(slackevents.MessageEvent)
	message.Message = &slack.Msg{Files: []slack.File{{ID: "F1", Name: "evidence.txt", Mimetype: "text/plain", URLPrivateDownload: "https://files.slack.com/evidence.txt"}}}
	eventsAPI := event.Data.(slackevents.EventsAPIEvent)
	eventsAPI.InnerEvent.Data = message
	event.Data = eventsAPI

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 0, acks, "attachment persistence failure must remain retryable")
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Empty(t, tasks)
	received, err := receiptRepo.Exists(context.Background(), slackIncomingEventKey("T1", "D1", "1710000000.400000", "U1"))
	require.NoError(t, err)
	require.False(t, received)
	require.Len(t, downloadedPaths, 1)
	require.NoFileExists(t, downloadedPaths[0], "failed atomic handoff must remove the published attachment")

	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)
	require.Equal(t, 2, downloads)
	tasks, err = taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	executions, err := svc.execRepo.ListByTask(context.Background(), tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	attachments, err := chatAttachmentRepo.ListByExecution(context.Background(), executions[0].ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.FileExists(t, attachments[0].FilePath)
}

func TestSlackService_SocketEventRepeatedAttachmentDownloadFailureRedelivery(t *testing.T) {
	svc, db, taskRepo, _, _, receiptRepo, projectID := newSlackSocketIngressTestService(t)
	svc.SetUploadsDir(t.TempDir())
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	downloads := 0
	recovered := false
	svc.downloadSlackAttachmentsFn = func(context.Context, []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
		downloads++
		if !recovered {
			return "", nil, nil, fmt.Errorf("transient Slack attachment download failure")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "recovered.txt")
		require.NoError(t, os.WriteFile(path, []byte("recovered evidence"), 0o600))
		return "\nFile: recovered.txt\n", nil, []models.ChatAttachment{{
			FileName: "recovered.txt", FilePath: path, MediaType: "text/plain", FileSize: 18,
		}}, nil
	}

	event := slackSocketMessageEvent("E-attachment-download", "1710000000.410000")
	message := event.Data.(slackevents.EventsAPIEvent).InnerEvent.Data.(slackevents.MessageEvent)
	message.Message = &slack.Msg{Files: []slack.File{{ID: "F-download", Name: "recovered.txt", Mimetype: "text/plain", URLPrivateDownload: "https://files.slack.com/recovered.txt"}}}
	eventsAPI := event.Data.(slackevents.EventsAPIEvent)
	eventsAPI.InnerEvent.Data = message
	event.Data = eventsAPI
	eventKey := slackIncomingEventKey("T1", "D1", "1710000000.410000", "U1")

	for attempt := 0; attempt < 2; attempt++ {
		svc.handleSocketEvent(context.Background(), nil, event)
		require.Equal(t, 0, acks)
		tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
		require.NoError(t, err)
		require.Empty(t, tasks, "download failures must not persist retry-visible failure turns")
		received, err := receiptRepo.Exists(context.Background(), eventKey)
		require.NoError(t, err)
		require.False(t, received)
	}

	recovered = true
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)
	require.Equal(t, 3, downloads)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	executions, err := svc.execRepo.ListByTask(context.Background(), tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	attachments, err := chatAttachmentRepo.ListByExecution(context.Background(), executions[0].ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.FileExists(t, attachments[0].FilePath)
	received, err := receiptRepo.Exists(context.Background(), eventKey)
	require.NoError(t, err)
	require.True(t, received)

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 2, acks)
	require.Equal(t, 3, downloads, "durable duplicate must not download or create another turn")
}

func TestSlackService_SocketEventFirstTurnPersistenceFailureRedelivery(t *testing.T) {
	svc, _, taskRepo, execRepo, _, _, projectID := newSlackSocketIngressTestService(t)
	acks := 0
	attempts := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createExecutionFn = func(ctx context.Context, execution *models.Execution) (bool, error) {
		attempts++
		if attempts == 1 {
			return false, fmt.Errorf("transient execution persistence failure")
		}
		return false, execRepo.Create(ctx, execution)
	}

	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 0, acks)
	require.Eventually(t, func() bool {
		tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
		return err == nil && len(tasks) == 0
	}, time.Second, 10*time.Millisecond, "failed first-turn cleanup must finish before retry admission")

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)
	require.Equal(t, 2, attempts)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 2, acks)
	require.Equal(t, 2, attempts)
	tasks, err = taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventAppMentionFailureAllowsMatchingMessageEventHandoff(t *testing.T) {
	svc, _, taskRepo, execRepo, _, _, projectID := newSlackSocketIngressTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.settingsRepo.Set(ctx, SlackSettingBotUserID, "UBOT"))

	acks := 0
	attempts := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createExecutionFn = func(ctx context.Context, execution *models.Execution) (bool, error) {
		attempts++
		if attempts == 1 {
			return false, fmt.Errorf("transient app-mention persistence failure")
		}
		return false, execRepo.Create(ctx, execution)
	}

	const timestamp = "1710000000.150000"
	appMention := socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "E-app-mention"},
		Data: slackevents.EventsAPIEvent{
			Type:   slackevents.CallbackEvent,
			TeamID: "T1",
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.AppMentionEvent{
				User:      "U1",
				Channel:   "C1",
				Text:      "<@UBOT> persist this",
				TimeStamp: timestamp,
			}},
		},
	}
	messageEvent := socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "E-message"},
		Data: slackevents.EventsAPIEvent{
			Type:   slackevents.CallbackEvent,
			TeamID: "T1",
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.MessageEvent{
				ChannelType: "channel",
				User:        "U1",
				Channel:     "C1",
				Text:        "<@UBOT> persist this",
				TimeStamp:   timestamp,
			}},
		},
	}

	svc.handleSocketEvent(ctx, nil, appMention)
	require.Equal(t, 0, acks)
	require.Eventually(t, func() bool {
		tasks, err := taskRepo.ListByProject(ctx, projectID, "")
		return err == nil && len(tasks) == 0
	}, time.Second, 10*time.Millisecond, "failed app-mention handoff must release the shared message key after cleanup")

	svc.handleSocketEvent(ctx, nil, messageEvent)
	require.Equal(t, 1, acks)
	require.Equal(t, 2, attempts)
	tasks, err := taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	executions, err := execRepo.ListByTask(ctx, tasks[0].ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)

	svc.handleSocketEvent(ctx, nil, appMention)
	svc.handleSocketEvent(ctx, nil, messageEvent)
	require.Equal(t, 3, acks, "both duplicate envelope forms must be terminally acknowledged")
	require.Equal(t, 2, attempts, "durable duplicates must not retry persistence")
	tasks, err = taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventQueuedTurnPersistenceFailureRedelivery(t *testing.T) {
	svc, _, taskRepo, execRepo, threadInputRepo, _, projectID := newSlackSocketIngressTestService(t)
	ctx := context.Background()
	agent, err := svc.llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	activeTask := &models.Task{ProjectID: projectID, Title: "active", Prompt: "active", Category: models.CategoryChat, Status: models.StatusRunning, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExecution := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExecution))

	acks := 0
	attempts := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createQueuedInputFn = func(ctx context.Context, input *models.ThreadInput) (bool, error) {
		attempts++
		if attempts == 1 {
			return false, fmt.Errorf("transient queued input persistence failure")
		}
		return false, threadInputRepo.CreateQueued(ctx, input)
	}

	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 0, acks)
	inputs, err := threadInputRepo.ListPendingForChat(ctx, projectID)
	require.NoError(t, err)
	require.Empty(t, inputs)

	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 1, acks)
	require.Equal(t, 2, attempts)
	inputs, err = threadInputRepo.ListPendingForChat(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)

	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 2, acks)
	require.Equal(t, 2, attempts)
	inputs, err = threadInputRepo.ListPendingForChat(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
}

func TestSlackService_SocketEventSuccessfulRedeliveryIsAcknowledgedWithoutDuplicateTurn(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	acks := 0
	accepted := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.processIncomingMessageResultFn = func(slackIncomingMessage) bool {
		accepted++
		return true
	}

	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(context.Background(), nil, event)
	svc.handleSocketEvent(context.Background(), nil, event)

	require.Equal(t, 2, acks, "a previously accepted redelivery is terminally acknowledged")
	require.Equal(t, 1, accepted)
}

func TestSlackService_SocketEventSuccessfulRedeliveryAfterRestartIsAcknowledgedWithoutDuplicateTurn(t *testing.T) {
	svc, _, taskRepo, _, threadInputRepo, receiptRepo, projectID := newSlackSocketIngressTestService(t)
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)

	restarted := NewSlackService(svc.settingsRepo, svc.projectRepo, svc.llmConfigRepo, svc.taskRepo, svc.execRepo, svc.scheduleRepo, svc.taskSvc, svc.llmSvc, svc.workerSvc, svc.slackUserProjectRepo, svc.slackTaskContextRepo, svc.slackAuthRepo)
	restarted.SetThreadInputRepo(threadInputRepo)
	restarted.SetSlackInboundReceiptRepo(receiptRepo)
	restarted.postMessageFn = func(string, string, string) (string, error) { return "", nil }
	restarted.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	restarted.handleSocketEvent(context.Background(), nil, event)

	require.Equal(t, 2, acks)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventProjectLookupFailureIsNotAcknowledgedAndCanRetry(t *testing.T) {
	svc, _, taskRepo, _, _, _, projectID := newSlackSocketIngressTestService(t)
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.mu.Lock()
	delete(svc.userProjects, slackUserProjectKey("T1", "U1"))
	svc.mu.Unlock()

	failedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(failedCtx, nil, event)
	require.Equal(t, 0, acks)

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventBotUserIDLookupFailureDoesNotAcknowledgeChannelMention(t *testing.T) {
	svc, db, taskRepo, _, _, _, projectID := newSlackSocketIngressTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.settingsRepo.Set(ctx, SlackSettingBotUserID, "UBOT"))
	svc.preACKTimeout = 30 * time.Millisecond
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }

	event := socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "E-channel-mention"},
		Data: slackevents.EventsAPIEvent{
			Type:   slackevents.CallbackEvent,
			TeamID: "T1",
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.MessageEvent{
				ChannelType: "channel",
				User:        "U1",
				Channel:     "C1",
				Text:        "<@UBOT> persist this",
				TimeStamp:   "1710000000.150000",
			}},
		},
	}

	blockingTx, err := db.Begin()
	require.NoError(t, err)
	defer blockingTx.Rollback()
	_, err = blockingTx.Exec(`SELECT 1`)
	require.NoError(t, err)

	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 0, acks, "transient bot-user-ID lookup failure must remain retryable")
	require.NoError(t, blockingTx.Rollback())
	tasks, err := taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Empty(t, tasks)

	svc.handleSocketEvent(ctx, nil, event)
	require.Equal(t, 1, acks)
	tasks, err = taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventPreACKDeadlineBoundsSettingsAndReceiptReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*SlackService)
	}{
		{
			name: "settings read",
			setup: func(svc *SlackService) {
				svc.slackInboundReceiptRepo = nil
			},
		},
		{
			name: "receipt read",
			setup: func(svc *SlackService) {
				svc.settingsRepo = nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, _, _, _, _, _ := newSlackSocketIngressTestService(t)
			tc.setup(svc)
			svc.preACKTimeout = 30 * time.Millisecond
			acks := 0
			svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }

			tx, err := db.Begin()
			require.NoError(t, err)
			defer tx.Rollback()
			_, err = tx.Exec(`SELECT 1`)
			require.NoError(t, err)

			done := make(chan struct{})
			go func() {
				svc.handleSocketEvent(context.Background(), nil, slackSocketMessageEvent("E1", "1710000000.100000"))
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(250 * time.Millisecond):
				_ = tx.Rollback()
				<-done
				t.Fatal("socket event remained blocked beyond the pre-ACK deadline")
			}
			require.Equal(t, 0, acks)
		})
	}
}

func TestSlackService_FileInfoTokenLookupRespectsRequestCancellation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	blockingTx, err := db.Begin()
	require.NoError(t, err)
	_, err = blockingTx.Exec(`SELECT 1`)
	require.NoError(t, err)

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.resolveSlackFileInfo(cancelledCtx, slackIncomingFile{
			ID:         "F1",
			Name:       "attachment.bin",
			Mimetype:   "application/octet-stream",
			FileAccess: "check_file_info",
		})
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(250 * time.Millisecond):
		require.NoError(t, blockingTx.Rollback())
		<-done
		t.Fatal("file-info bot-token lookup ignored request cancellation")
	}
	require.NoError(t, blockingTx.Rollback())
}

func TestSlackService_SocketEventPreACKFailureCallbackDoesNotBlock(t *testing.T) {
	svc, _, taskRepo, _, _, _, projectID := newSlackSocketIngressTestService(t)
	svc.preACKTimeout = 30 * time.Millisecond
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createExecutionFn = func(context.Context, *models.Execution) (bool, error) {
		return false, fmt.Errorf("simulated execution persistence failure")
	}
	callbackStarted := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	svc.postMessageFn = func(string, string, string) (string, error) {
		callbackStarted <- struct{}{}
		<-releaseCallback
		return "", nil
	}

	done := make(chan struct{})
	go func() {
		svc.handleSocketEvent(context.Background(), nil, slackSocketMessageEvent("E1", "1710000000.100000"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		close(releaseCallback)
		<-done
		t.Fatal("pre-ACK failure notification blocked socket event handling")
	}
	select {
	case <-callbackStarted:
	case <-time.After(250 * time.Millisecond):
		close(releaseCallback)
		t.Fatal("expected asynchronous failure notification")
	}
	close(releaseCallback)
	require.Equal(t, 0, acks)
	require.Eventually(t, func() bool {
		tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
		return err == nil && len(tasks) == 0
	}, time.Second, 10*time.Millisecond, "asynchronous failure cleanup must remove the provisional task")
}

func TestSlackService_SocketEventAttachmentPublicationFailureCallbackDoesNotBlock(t *testing.T) {
	svc, _, taskRepo, _, _, _, projectID := newSlackSocketIngressTestService(t)
	svc.preACKTimeout = 30 * time.Millisecond
	uploadsFile := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(uploadsFile, []byte("occupied"), 0o600))
	svc.SetUploadsDir(uploadsFile)

	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.downloadSlackAttachmentsFn = func(context.Context, []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "evidence.txt")
		require.NoError(t, os.WriteFile(path, []byte("evidence"), 0o600))
		return "\nFile: evidence.txt\n", nil, []models.ChatAttachment{{
			FileName: "evidence.txt", FilePath: path, MediaType: "text/plain", FileSize: 8,
		}}, nil
	}
	callbackStarted := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	svc.postMessageFn = func(string, string, string) (string, error) {
		callbackStarted <- struct{}{}
		<-releaseCallback
		return "", nil
	}

	event := slackSocketMessageEvent("E-attachment-publication", "1710000000.420000")
	message := event.Data.(slackevents.EventsAPIEvent).InnerEvent.Data.(slackevents.MessageEvent)
	message.Message = &slack.Msg{Files: []slack.File{{ID: "F-publication", Name: "evidence.txt", Mimetype: "text/plain", URLPrivateDownload: "https://files.slack.com/evidence.txt"}}}
	eventsAPI := event.Data.(slackevents.EventsAPIEvent)
	eventsAPI.InnerEvent.Data = message
	event.Data = eventsAPI

	done := make(chan struct{})
	go func() {
		svc.handleSocketEvent(context.Background(), nil, event)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		close(releaseCallback)
		<-done
		t.Fatal("attachment publication failure notification blocked socket event handling")
	}
	select {
	case <-callbackStarted:
	case <-time.After(250 * time.Millisecond):
		close(releaseCallback)
		t.Fatal("expected attachment publication failure notification")
	}
	close(releaseCallback)
	require.Equal(t, 0, acks)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestSlackService_SocketEventDeadlineCleanupDoesNotBlockAndRemovesProvisionalTask(t *testing.T) {
	svc, db, taskRepo, execRepo, _, _, projectID := newSlackSocketIngressTestService(t)
	svc.preACKTimeout = 30 * time.Millisecond
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }

	var blockingTx *sql.Tx
	attempts := 0
	cleanupBlocked := make(chan struct{})
	svc.createExecutionFn = func(ctx context.Context, execution *models.Execution) (bool, error) {
		attempts++
		if attempts > 1 {
			return false, execRepo.Create(ctx, execution)
		}
		var err error
		blockingTx, err = db.Begin()
		require.NoError(t, err)
		_, err = blockingTx.Exec(`SELECT 1`)
		require.NoError(t, err)
		<-ctx.Done()
		close(cleanupBlocked)
		return false, ctx.Err()
	}

	done := make(chan struct{})
	started := time.Now()
	go func() {
		svc.handleSocketEvent(context.Background(), nil, slackSocketMessageEvent("E1", "1710000000.100000"))
		close(done)
	}()

	select {
	case <-cleanupBlocked:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("execution persistence did not reach deadline cleanup")
	}
	select {
	case <-done:
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("socket event returned after %s, want bounded pre-ACK handling", elapsed)
		}
	case <-time.After(250 * time.Millisecond):
		require.NoError(t, blockingTx.Rollback())
		<-done
		t.Fatal("blocked provisional-task cleanup extended socket handling beyond its deadline")
	}
	require.Equal(t, 0, acks)

	redeliveryDone := make(chan struct{})
	go func() {
		svc.handleSocketEvent(context.Background(), nil, slackSocketMessageEvent("E1-redelivery", "1710000000.100000"))
		close(redeliveryDone)
	}()
	select {
	case <-redeliveryDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("redelivery blocked while provisional-task cleanup was pending")
	}
	require.Equal(t, 0, acks)
	require.Equal(t, 1, attempts, "redelivery must not race ahead of provisional-task cleanup")

	require.NoError(t, blockingTx.Rollback())
	require.Eventually(t, func() bool {
		tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
		return err == nil && len(tasks) == 0
	}, time.Second, 10*time.Millisecond, "detached compensation must remove the provisional task")

	svc.handleSocketEvent(context.Background(), nil, slackSocketMessageEvent("E1-retry", "1710000000.100000"))
	require.Equal(t, 1, acks)
	require.Equal(t, 2, attempts)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestSlackService_SocketEventCleanupFailureRetainsRetryAdmissionUntilCompensationSucceeds(t *testing.T) {
	svc, _, taskRepo, execRepo, _, _, projectID := newSlackSocketIngressTestService(t)
	svc.cleanupRetryDelay = 5 * time.Millisecond
	acks := 0
	persistAttempts := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createExecutionFn = func(ctx context.Context, execution *models.Execution) (bool, error) {
		persistAttempts++
		if persistAttempts == 1 {
			return false, fmt.Errorf("simulated first-turn persistence failure")
		}
		return false, execRepo.Create(ctx, execution)
	}

	firstDeleteFailed := make(chan struct{})
	secondDeleteStarted := make(chan struct{})
	releaseDelete := make(chan struct{})
	deleteAttempts := 0
	svc.deleteProvisionalTaskFn = func(ctx context.Context, taskID string) error {
		deleteAttempts++
		if deleteAttempts == 1 {
			close(firstDeleteFailed)
			return fmt.Errorf("simulated cleanup failure")
		}
		close(secondDeleteStarted)
		<-releaseDelete
		return taskRepo.Delete(ctx, taskID)
	}

	event := slackSocketMessageEvent("E1", "1710000000.100000")
	svc.handleSocketEvent(context.Background(), nil, event)
	select {
	case <-firstDeleteFailed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("first cleanup attempt did not fail")
	}
	select {
	case <-secondDeleteStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cleanup was not retried autonomously")
	}

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 0, acks)
	require.Equal(t, 1, persistAttempts, "redelivery must remain excluded after cleanup failure")
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1, "failed cleanup must not release admission while the provisional task remains")

	close(releaseDelete)
	require.Eventually(t, func() bool {
		tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
		return err == nil && len(tasks) == 0
	}, time.Second, 10*time.Millisecond)

	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 1, acks)
	require.Equal(t, 2, persistAttempts)
}

func TestSlackService_SocketEventAttachmentStagingAndCleanupRespectPreACKDeadline(t *testing.T) {
	for _, tc := range []struct {
		name         string
		blockStage   bool
		blockCleanup bool
	}{
		{name: "staging", blockStage: true},
		{name: "cleanup", blockCleanup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, taskRepo, execRepo, _, _, projectID := newSlackSocketIngressTestService(t)
			ctx := context.Background()
			if tc.blockStage {
				agent, err := svc.llmConfigRepo.GetDefault(ctx)
				require.NoError(t, err)
				activeTask := &models.Task{ProjectID: projectID, Title: "active attachment turn", Prompt: "active", Category: models.CategoryChat, Status: models.StatusRunning, AgentID: &agent.ID}
				require.NoError(t, taskRepo.Create(ctx, activeTask))
				require.NoError(t, execRepo.Create(ctx, &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}))
			}

			svc.preACKTimeout = 30 * time.Millisecond
			acks := 0
			svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
			release := make(chan struct{})
			svc.downloadSlackAttachmentsFn = func(context.Context, []slackIncomingFile) (string, []models.Attachment, []models.ChatAttachment, error) {
				return "", nil, []models.ChatAttachment{{FileName: "queued.txt", FilePath: filepath.Join(t.TempDir(), "queued.txt")}}, nil
			}
			svc.savePendingAttachmentsFn = func([]models.ChatAttachment) (string, error) {
				if tc.blockStage {
					<-release
				}
				return "pending-session", nil
			}
			if tc.blockCleanup {
				svc.createFirstTurnExecutionFn = func(context.Context, repository.SQLExecutor, *models.Execution) error {
					return fmt.Errorf("simulated attachment first-turn persistence failure")
				}
				svc.cleanupAttachmentSourcesFn = func([]models.ChatAttachment) { <-release }
			}

			event := slackSocketMessageEvent("E-attachment", "1710000000.200000")
			message := event.Data.(slackevents.EventsAPIEvent).InnerEvent.Data.(slackevents.MessageEvent)
			message.Message = &slack.Msg{Files: []slack.File{{ID: "F1", Name: "queued.txt", URLPrivateDownload: "https://files.slack.com/queued.txt"}}}
			eventsAPI := event.Data.(slackevents.EventsAPIEvent)
			eventsAPI.InnerEvent.Data = message
			event.Data = eventsAPI

			done := make(chan struct{})
			started := time.Now()
			go func() {
				svc.handleSocketEvent(ctx, nil, event)
				close(done)
			}()
			select {
			case <-done:
				require.Less(t, time.Since(started), 250*time.Millisecond)
			case <-time.After(250 * time.Millisecond):
				close(release)
				<-done
				t.Fatal("attachment filesystem work outlived the pre-ACK deadline")
			}
			require.Equal(t, 0, acks)
			close(release)
		})
	}
}

func TestSlackService_SocketEventRestartAfterFirstTurnPartialPersistenceLeavesNoOrphan(t *testing.T) {
	svc, db, taskRepo, _, threadInputRepo, receiptRepo, projectID := newSlackSocketIngressTestService(t)
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.createFirstTurnExecutionFn = func(context.Context, repository.SQLExecutor, *models.Execution) error {
		return fmt.Errorf("simulated process interruption before execution persistence")
	}
	event := slackSocketMessageEvent("E-restart", "1710000000.300000")
	svc.handleSocketEvent(context.Background(), nil, event)
	require.Equal(t, 0, acks)
	tasks, err := taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Empty(t, tasks, "atomic first-turn failure must not leave a provisional task")

	restarted := NewSlackService(svc.settingsRepo, svc.projectRepo, svc.llmConfigRepo, svc.taskRepo, svc.execRepo, svc.scheduleRepo, svc.taskSvc, svc.llmSvc, svc.workerSvc, svc.slackUserProjectRepo, svc.slackTaskContextRepo, svc.slackAuthRepo)
	restarted.SetThreadInputRepo(threadInputRepo)
	restarted.SetSlackInboundReceiptRepo(receiptRepo)
	restarted.SetChatAttachmentRepo(repository.NewChatAttachmentRepo(db))
	restarted.postMessageFn = func(string, string, string) (string, error) { return "", nil }
	restarted.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	restarted.handleSocketEvent(context.Background(), nil, event)

	require.Equal(t, 1, acks)
	tasks, err = taskRepo.ListByProject(context.Background(), projectID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestRunChannelChatFirstTurnDeadlineCleanupUsesNonCancelledContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "deadline cleanup"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{ID: "agent-deadline-cleanup"}

	deadlineCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	handled, _ := runChannelChatFirstTurn(deadlineCtx, channelChatIngressFirstTurnOptions{
		Platform:  "slack",
		ProjectID: project.ID,
		Message:   "persist this",
		Source:    "slack",
		Task:      &models.Task{Title: "provisional"},
		Agent:     agent,
		TaskRepo:  taskRepo,
		CreateTaskContext: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
		CreateExecution: func(context.Context, *models.Execution) (bool, error) {
			return false, nil
		},
	})
	require.True(t, handled)
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Empty(t, tasks, "deadline cleanup must remove the provisional task")
}

func TestSlackService_SocketEventTerminallyAcknowledgesIgnoredAndMalformedEvents(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	acks := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }

	svc.handleSocketEvent(context.Background(), nil, socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "malformed"},
		Data:    "not an events API event",
	})
	svc.handleSocketEvent(context.Background(), nil, socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "ignored"},
		Data: slackevents.EventsAPIEvent{
			Type:       slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: struct{}{}},
		},
	})
	svc.handleSocketEvent(context.Background(), nil, socketmode.Event{
		Type:    socketmode.EventTypeSlashCommand,
		Request: &socketmode.Request{EnvelopeID: "unsupported-request"},
	})

	require.Equal(t, 3, acks)
}

func TestSlackService_SocketEventTerminallyAcknowledgesSupportedEventsWithoutStableTimestamp(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	acks := 0
	accepted := 0
	svc.ackSocketRequestFn = func(*socketmode.Client, socketmode.Request) { acks++ }
	svc.processIncomingMessageResultFn = func(slackIncomingMessage) bool {
		accepted++
		return true
	}

	for _, event := range []socketmode.Event{
		{
			Type:    socketmode.EventTypeEventsAPI,
			Request: &socketmode.Request{EnvelopeID: "missing-app-mention-timestamp"},
			Data: slackevents.EventsAPIEvent{
				Type:   slackevents.CallbackEvent,
				TeamID: "T1",
				InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.AppMentionEvent{
					User:    "U1",
					Channel: "C1",
					Text:    "<@UBOT> malformed mention",
				}},
			},
		},
		{
			Type:    socketmode.EventTypeEventsAPI,
			Request: &socketmode.Request{EnvelopeID: "missing-message-timestamp"},
			Data: slackevents.EventsAPIEvent{
				Type:   slackevents.CallbackEvent,
				TeamID: "T1",
				InnerEvent: slackevents.EventsAPIInnerEvent{Data: slackevents.MessageEvent{
					ChannelType: "im",
					User:        "U1",
					Channel:     "D1",
					Text:        "malformed message",
				}},
			},
		},
	} {
		svc.handleSocketEvent(context.Background(), nil, event)
	}

	require.Equal(t, 2, acks)
	require.Zero(t, accepted, "events without a stable identity must not create work")
}

func TestSlackService_EventFilteringAcceptsDMAppMentionsAndChannelMessageMentions(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var received []slackIncomingMessage
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) {
		received = append(received, msg)
	}

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		Text:      "<@UBOT> hello from mention",
		TimeStamp: "1710000000.100000",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "im",
		User:        "U2",
		Channel:     "D1",
		Text:        "hello from dm",
		TimeStamp:   "1710000001.100000",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "channel",
		User:        "U3",
		Channel:     "C2",
		Text:        "<@UBOT> hello from channel message",
		TimeStamp:   "1710000002.100000",
	})

	require.Len(t, received, 3)
	require.Equal(t, "T1", received[0].TeamID)
	require.Equal(t, "C1", received[0].ChannelID)
	require.Equal(t, "hello from mention", received[0].Text)
	require.Equal(t, "D1", received[1].ChannelID)
	require.Equal(t, "hello from dm", received[1].Text)
	require.Equal(t, "C2", received[2].ChannelID)
	require.Equal(t, "hello from channel message", received[2].Text)
}

func TestSlackService_EventFilteringDedupesAppMentionAndMessageEventForSameSlackMessage(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var received []slackIncomingMessage
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) {
		received = append(received, msg)
	}

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		Text:      "<@UBOT> look at this",
		TimeStamp: "1710000000.100000",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "channel",
		User:        "U1",
		Channel:     "C1",
		Text:        "<@UBOT> look at this",
		TimeStamp:   "1710000000.100000",
	})

	require.Len(t, received, 1)
	require.Equal(t, "look at this", received[0].Text)
}

func TestSlackService_EventFilteringIgnoresBotSelfAndNonDMMessages(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	called := false
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) {
		called = true
	}

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "UBOT",
		Channel:   "C1",
		Text:      "<@UBOT> should ignore",
		TimeStamp: "1710000000.100000",
	})
	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		BotID:     "B1",
		Text:      "<@UBOT> should ignore",
		TimeStamp: "1710000000.100000",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "channel",
		User:        "U1",
		Channel:     "C2",
		Text:        "public channel message",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "im",
		User:        "U1",
		Channel:     "D1",
		SubType:     "message_changed",
		Text:        "edited",
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "im",
		User:        "UBOT",
		Channel:     "D2",
		Text:        "bot self message",
	})

	require.False(t, called)
}

func TestSlackService_RuntimeCreateTaskTool_CreatedTasksGetSlackOriginAndContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)

	project := &models.Project{Name: "Slack Tool Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, execRepo, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	agentRepo := repository.NewAgentRepo(db)
	taskSvc.SetAgentRepo(agentRepo)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agent))

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetAgentRepo(agentRepo)

	collector := newChannelActionSummaryCollector()
	rt := svc.buildSlackActionToolRuntime(project.ID, slackActionContext{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
	}, collector)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "create_task", json.RawMessage(`{"title":"Slack Tool Created","prompt":"Do it","agent":"Reviewer"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "Created 1 task(s):")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)

	var created *models.Task
	for i := range tasks {
		if tasks[i].Title == "Slack Tool Created" {
			created = &tasks[i]
			break
		}
	}
	require.NotNil(t, created)
	require.Equal(t, models.TaskOriginSlack, created.CreatedVia)
	require.NotNil(t, created.AgentDefinitionID)
	require.Equal(t, agent.ID, *created.AgentDefinitionID)

	stc, err := slackTaskContextRepo.GetByTaskID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stc)
	require.Equal(t, "C1", stc.SlackChannelID)
	require.Equal(t, "1710000000.100000", stc.SlackThreadTS)

	swarmOutput, handled, isErr, err := rt.Executor(ctx, "create_swarm_task", json.RawMessage(`{"title":"Slack Swarm Created","prompt":"Split this work","category":"backlog"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, swarmOutput, "Created swarm task: Slack Swarm Created")

	tasks, err = taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	var swarmParent *models.Task
	for i := range tasks {
		if tasks[i].Title == "Slack Swarm Created" {
			swarmParent = &tasks[i]
			break
		}
	}
	require.NotNil(t, swarmParent)
	require.Equal(t, models.SwarmRoleParent, swarmParent.SwarmRole)
	require.Equal(t, models.TaskOriginSlack, swarmParent.CreatedVia)
	stc, err = slackTaskContextRepo.GetByTaskID(ctx, swarmParent.ID)
	require.NoError(t, err)
	require.NotNil(t, stc)
	require.Equal(t, "C1", stc.SlackChannelID)

	finalOutput := collector.appendToOutput("Done.")
	require.Contains(t, finalOutput, "[TASK_ID:")
	require.Contains(t, finalOutput, swarmParent.ID)
}

func TestSlackService_RuntimeListAlertsTool_Handled(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	alertRepo := repository.NewAlertRepo(db)

	project := &models.Project{Name: "Slack Alerts Runtime"}
	require.NoError(t, projectRepo.Create(ctx, project))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetAlertService(NewAlertService(alertRepo, nil))

	rt := svc.buildSlackActionToolRuntime(project.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "list_alerts", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, `"notifications":[]`)
}

func TestSlackService_NotificationLifecycleRuntimeUsesPersistedChannelTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Slack Notification Lifecycle"}
	require.NoError(t, projectRepo.Create(ctx, project))
	caller := &models.Task{ProjectID: project.ID, Title: "Slack chat", Prompt: "process", Category: models.CategoryChat, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Slack suggestion", Severity: models.SeverityInfo})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved))

	svc := &SlackService{taskRepo: taskRepo, alertSvc: alertSvc}
	rt := svc.buildSlackActionToolRuntimeForTask(project.ID, caller.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", UserID: "U1"}, nil)
	output, handled, isErr, err := rt.Executor(ctx, "claim_alert", json.RawMessage(`{"alert_id":"`+alert.ID+`"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, caller.ID)
	stored, err := alertSvc.GetByID(ctx, project.ID, alert.ID)
	require.NoError(t, err)
	require.Equal(t, caller.ID, stored.Claimant)
}

func TestSlackService_RuntimeExecutorHandlesAllDefinedTools(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	alertRepo := repository.NewAlertRepo(db)

	project := &models.Project{Name: "Slack Full Runtime"}
	require.NoError(t, projectRepo.Create(ctx, project))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetAlertService(NewAlertService(alertRepo, nil))

	rt := svc.buildSlackActionToolRuntime(project.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
	require.NotNil(t, rt)

	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceSlack, true)
	require.NotEmpty(t, defs)

	for _, d := range defs {
		_, handled, _, _ := rt.Executor(ctx, d.Name, json.RawMessage(`{}`))
		require.Truef(t, handled, "tool should be handled by slack runtime executor: %s", d.Name)
	}

	handlers := svc.slackActionHandlers(project.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
	require.NoError(t, chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceSlack, true, handlers))
}

func TestSlackService_ProcessIncomingMessage_AuthorizationEnforced(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	project := &models.Project{Name: "Slack Auth Enforce"}
	require.NoError(t, projectRepo.Create(ctx, project))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, slackAuthRepo)
	svc.setActiveProject(ctx, "T1", "U2", project.ID)

	require.NoError(t, slackAuthRepo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID:   project.ID,
		SlackUserID: "U1",
		DisplayName: "Allowed",
		AddedBy:     "test",
	}))

	var responses []string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		responses = append(responses, text)
		return "", nil
	}

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U2",
		Text:      "hello",
		Source:    "slack",
	})
	require.NotEmpty(t, responses)
	require.Contains(t, responses[0], "not authorized")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 0)
}

func TestSlackService_CheckAuthorization_FallsBackToAnyProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	projectA := &models.Project{Name: "Project A"}
	projectB := &models.Project{Name: "Project B"}
	require.NoError(t, projectRepo.Create(ctx, projectA))
	require.NoError(t, projectRepo.Create(ctx, projectB))

	require.NoError(t, slackAuthRepo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID:   projectA.ID,
		SlackUserID: "U_ALLOWED",
		DisplayName: "Allowed",
		AddedBy:     "test",
	}))

	svc := NewSlackService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, slackAuthRepo)

	require.True(t, svc.checkAuthorization(ctx, projectB.ID, "U_ALLOWED"))
	require.False(t, svc.checkAuthorization(ctx, projectB.ID, "U_BLOCKED"))
}

func TestSlackService_CheckAuthorizationRejectedActiveProjectUsesSingleLookup(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	project := &models.Project{Name: "Slack Single Auth Lookup"}
	require.NoError(t, projectRepo.Create(ctx, project))
	seedChannelAuthorizedUsers(t, db, "slack_authorized_users", "slack_user_id", project.ID, "U_SEEDED", 1000)

	svc := NewSlackService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, slackAuthRepo)
	counter.Reset()
	counter.SetEnabled(true)
	authorized := svc.checkAuthorization(ctx, project.ID, "U_REJECTED")
	counter.SetEnabled(false)

	require.False(t, authorized)
	statements := counter.Statements()
	require.Len(t, statements, 1, "rejected active-project authorization should issue one statement: %q", statements)
	require.Equal(t, 1, countChannelAuthTableStatements(statements, "slack_authorized_users"), "statements: %q", statements)
}

func BenchmarkSlackRejectedAuthorizationSingleLookupLargeAllowlist(b *testing.B) {
	db := testutil.NewTestDB(b)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)
	project := &models.Project{Name: "Slack Rejected Auth Benchmark"}
	require.NoError(b, projectRepo.Create(ctx, project))
	seedChannelAuthorizedUsers(b, db, "slack_authorized_users", "slack_user_id", project.ID, "U_BENCH", 100000)
	svc := NewSlackService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, slackAuthRepo)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if svc.checkAuthorization(ctx, project.ID, "U_REJECTED") {
			b.Fatal("unexpected authorized rejected Slack user")
		}
	}
}

func seedChannelAuthorizedUsers(tb testing.TB, db *sql.DB, table, userColumn, projectID, userPrefix string, count int) {
	tb.Helper()
	tx, err := db.Begin()
	require.NoError(tb, err)
	stmt, err := tx.Prepare(fmt.Sprintf(`INSERT INTO %s (project_id, %s, display_name, added_by) VALUES (?, ?, ?, ?)`, table, userColumn))
	require.NoError(tb, err)
	for i := 0; i < count; i++ {
		userID := fmt.Sprintf("%s_%06d", userPrefix, i)
		_, err = stmt.Exec(projectID, userID, "Benchmark User", "test")
		require.NoError(tb, err)
	}
	require.NoError(tb, stmt.Close())
	require.NoError(tb, tx.Commit())
}

func countChannelAuthTableStatements(statements []string, table string) int {
	needle := " from " + strings.ToLower(table) + " where "
	count := 0
	for _, statement := range statements {
		if strings.Contains(strings.ToLower(statement), needle) {
			count++
		}
	}
	return count
}

func TestSlackService_CheckAuthorization_NoUsersConfiguredDenyAll(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackAuthRepo := repository.NewSlackAuthRepo(db)

	project := &models.Project{Name: "Project Empty Auth"}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := NewSlackService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, slackAuthRepo)

	require.False(t, svc.checkAuthorization(ctx, project.ID, "U_ANY"))
	require.False(t, svc.checkAuthorization(ctx, "", "U_ANY"))
}

func TestSlackService_SendTaskCompletionNotification(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)

	project := &models.Project{Name: "Slack Notify Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{
		ProjectID:  project.ID,
		Title:      "Slack Notify",
		Category:   models.CategoryActive,
		Status:     models.StatusCompleted,
		Prompt:     "done",
		CreatedVia: models.TaskOriginSlack,
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	require.NoError(t, slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{
		TaskID:         task.ID,
		SlackTeamID:    "T1",
		SlackChannelID: "C1",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U1",
	}))

	svc := NewSlackService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, nil, nil, nil, nil, slackTaskContextRepo, nil)
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingSendResponses, "true"))

	called := false
	var sentText string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		called = true
		sentText = text
		require.Equal(t, "C1", channelID)
		require.Equal(t, "1710000000.100000", threadTS)
		require.True(t, strings.Contains(text, "Task completed") || strings.Contains(text, "Task failed"))
		return "", nil
	}

	output := "Examples: `[TASK_ID:inline]` and `[TASK_EDITED:inline-edit]`.\n" +
		"```text\n[TASK_ID:fenced]\n[TASK_EDITED:fenced-edit]\n```\n" +
		"Actual [TASK_ID:real] [TASK_EDITED:real-edit]"
	svc.SendTaskCompletionNotification(ctx, *task, output, "")
	require.True(t, called)
	require.Contains(t, sentText, "`[TASK_ID:inline]`")
	require.Contains(t, sentText, "`[TASK_EDITED:inline-edit]`")
	require.Contains(t, sentText, "[TASK_ID:fenced]")
	require.Contains(t, sentText, "[TASK_EDITED:fenced-edit]")
	require.NotContains(t, sentText, "[TASK_ID:real]")
	require.NotContains(t, sentText, "[TASK_EDITED:real-edit]")

	require.NoError(t, settingsRepo.Set(ctx, SlackSettingSendResponses, "false"))
	called = false
	svc.SendTaskCompletionNotification(ctx, *task, "completed output", "")
	require.False(t, called)
}

func TestSlackService_ProcessIncomingMessage_QueuesWhenChatActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{Name: "Slack Queue Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var sent []string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		sent = append(sent, text)
		return "", nil
	}

	svc.processIncomingMessage(slackIncomingMessage{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1", Text: "follow up from slack", Source: "slack"})

	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, models.ThreadInputModeQueued, inputs[0].InputMode)
	require.Equal(t, activeExec.ID, inputs[0].RunExecutionID)
	require.Equal(t, models.TaskOriginSlack, inputs[0].Source)
	require.Equal(t, "C1", inputs[0].SlackChannelID)
	require.Equal(t, "1710000000.100000", inputs[0].SlackThreadTS)
	require.Contains(t, strings.Join(sent, "\n"), "Queued")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1, "queued channel follow-up must not create a second chat task immediately")
}

func TestSlackService_ProcessIncomingMessage_UsesSharedChannelChatRunner(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)

	project := &models.Project{Name: "Slack Shared Runner Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) {
		got = &req
	})

	svc.processIncomingMessage(slackIncomingMessage{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1", Text: "start from slack", Source: "slack"})

	require.NotNil(t, got, "Slack chat should use the shared steering-aware runner when wired")
	require.NotEmpty(t, got.ExecID)
	require.NotEmpty(t, got.TaskID)
	require.Equal(t, project.ID, got.ProjectID)
	require.Equal(t, "start from slack", got.Message)
	require.Equal(t, agent.ID, got.Agent.ID)
	require.Equal(t, chatcontrol.SurfaceSlack, got.Surface)
	require.Equal(t, models.TaskOriginSlack, got.ReplyContext.Source)
	require.Equal(t, "T1", got.ReplyContext.SlackTeamID)
	require.Equal(t, "C1", got.ReplyContext.SlackChannelID)
	require.Equal(t, "1710000000.100000", got.ReplyContext.SlackThreadTS)
	require.Equal(t, "U1", got.ReplyContext.SlackUserID)
	createdExec, err := execRepo.GetByID(ctx, got.ExecID)
	require.NoError(t, err)
	require.NotNil(t, createdExec)
	require.Equal(t, models.ExecRunning, createdExec.Status)
	stc, err := slackTaskContextRepo.GetByTaskID(ctx, got.TaskID)
	require.NoError(t, err)
	require.NotNil(t, stc, "Slack shared-runner chat needs reply context for final response delivery")
	require.Equal(t, "T1", stc.SlackTeamID)
	require.Equal(t, "C1", stc.SlackChannelID)
	require.Equal(t, "1710000000.100000", stc.SlackThreadTS)
	require.Equal(t, "U1", stc.SlackUserID)
}

func TestSlackService_SendToTaskUsesSharedRunnerAndQueuesActiveTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Slack Task Runner Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetThreadInputRepo(threadInputRepo)

	task := &models.Task{Title: "Slack task", Prompt: "work", Category: models.CategoryCompleted, Status: models.StatusCompleted, ProjectID: project.ID, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	var runnerReq ChannelTaskRunRequest
	runnerCalled := false
	svc.SetChannelTaskRunner(func(_ context.Context, req ChannelTaskRunRequest) {
		runnerCalled = true
		runnerReq = req
	})
	payload := []byte(fmt.Sprintf(`{"task_id":"%s","message":"do more"}`, task.ID))
	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}
	handlers := svc.slackActionHandlers(project.ID, actionCtx, nil)
	result, err := handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, result, "Sent message to task")
	require.True(t, runnerCalled, "Slack task follow-ups should use shared runner when wired")
	require.Equal(t, task.ID, runnerReq.TaskID)
	require.Equal(t, chatcontrol.SurfaceSlack, runnerReq.Surface)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotEqual(t, models.TaskOriginSlack, updatedTask.CreatedVia, "channel send_to_task must not rewrite target task origin")
	require.Equal(t, models.TaskOriginSlack, runnerReq.ReplyContext.Source)
	require.Equal(t, "C1", runnerReq.ReplyContext.SlackChannelID)
	require.Equal(t, "1710000000.100000", runnerReq.ReplyContext.SlackThreadTS)

	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active", IsFollowup: true}
	require.NoError(t, execRepo.Create(ctx, active))
	result, err = handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, result, "Queued message to task")
	inputs, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "do more", inputs[0].Content)
	require.Equal(t, active.ID, inputs[0].RunExecutionID)
	require.Equal(t, models.TaskOriginSlack, inputs[0].Source)
	require.Equal(t, "C1", inputs[0].SlackChannelID)
	require.Equal(t, "1710000000.100000", inputs[0].SlackThreadTS)
	updatedTask, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotEqual(t, models.TaskOriginSlack, updatedTask.CreatedVia, "queued channel follow-up must not hijack active task reply origin")
}

func TestSlackService_SendToTask_QueuesDuringStartingFirstTurnBeforeExecutionExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Slack Starting First Turn Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	svc.SetThreadInputRepo(threadInputRepo)
	runnerCalled := false
	svc.SetChannelTaskRunner(func(context.Context, ChannelTaskRunRequest) {
		runnerCalled = true
	})
	task := &models.Task{Title: "Slack starting task", Prompt: "tell me a story about a duck", Category: models.CategoryActive, Status: models.StatusPending, ProjectID: project.ID, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))

	payload := []byte(fmt.Sprintf(`{"task_id":"%s","message":"1+1=?"}`, task.ID))
	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}
	handlers := svc.slackActionHandlers(project.ID, actionCtx, nil)
	result, err := handlers["send_to_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, result, "Queued message to task")
	require.False(t, runnerCalled, "pre-execution first-turn send must not start a follow-up runner")
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, execs)
	inputs, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "1+1=?", inputs[0].Content)
	require.Empty(t, inputs[0].RunExecutionID)
	require.Equal(t, models.TaskOriginSlack, inputs[0].Source)
	require.Equal(t, "C1", inputs[0].SlackChannelID)
}

func TestSlackService_CompleteExecution_FailurePromotesQueuedChat(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Slack Failed Promotion Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agents, err := llmConfigRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)
	task := &models.Task{ProjectID: project.ID, Title: "chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "prompt", CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agents[0].ID, Status: models.ExecRunning, PromptSent: "prompt"}
	require.NoError(t, execRepo.Create(ctx, exec))

	promotedProject := ""
	completeExecution := channelCompletionFunc("slack", execRepo, taskRepo, nil, func(projectID string) { promotedProject = projectID })

	completeExecution(ctx, exec.ID, task.ID, "", "boom", 0, 10)

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, updatedExec.Status)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, updatedTask.Status)
	require.Equal(t, project.ID, promotedProject, "failed chat executions should still promote queued follow-ups")
}

func TestSlackService_SendChatResponse_SendsChatTaskOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)
	project := &models.Project{Name: "Slack Chat Response Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "chat", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "prompt", CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, task))
	require.NoError(t, slackTaskContextRepo.Upsert(ctx, &models.SlackTaskContext{TaskID: task.ID, SlackTeamID: "T1", SlackChannelID: "C1", SlackThreadTS: "1710000000.100000", SlackUserID: "U1"}))

	svc := NewSlackService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, nil, nil, nil, nil, slackTaskContextRepo, nil)
	var sent []string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		sent = append(sent, channelID+"|"+threadTS+"|"+text)
		return "", nil
	}

	svc.SendChatResponse(ctx, *task, "hello from queued slack", "")

	require.Equal(t, []string{"C1|1710000000.100000|hello from queued slack"}, sent)
}

// TestSlackService_GoalTools_SetGetClearPauseResume verifies that the Slack
// goal tool handlers call TaskGoalService and return JSON results, not errors.
// This is a regression test: before the fix, all goal tool handlers were stubs
// that always returned "task goal tools are unavailable on Slack".
func TestSlackService_GoalTools_SetGetClearPauseResume(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	taskGoalRepo := repository.NewTaskGoalRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	goalSvc := NewTaskGoalService(taskGoalRepo, taskRepo, nil)

	project := &models.Project{Name: "Slack Goal Test", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "slack-goal-task",
		Prompt:    "do something",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	svc := NewSlackService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, taskSvc, nil, workerSvc, nil, nil, nil)
	svc.taskGoalSvc = goalSvc

	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}
	handlers := svc.slackActionHandlers(project.ID, actionCtx, nil)

	taskIDJSON, err := json.Marshal(map[string]string{"task_id": task.ID, "goal": "All tests pass"})
	require.NoError(t, err)

	// set_task_goal
	out, err := handlers["set_task_goal"](ctx, taskIDJSON)
	require.NoError(t, err, "set_task_goal must not return an error on Slack")
	require.Contains(t, out, "ok")
	require.Contains(t, out, "All tests pass")

	// get_task_goal
	getInput, _ := json.Marshal(map[string]string{"task_id": task.ID})
	out, err = handlers["get_task_goal"](ctx, getInput)
	require.NoError(t, err, "get_task_goal must not return an error on Slack")
	require.Contains(t, out, "All tests pass")

	// pause_task_goal
	out, err = handlers["pause_task_goal"](ctx, getInput)
	require.NoError(t, err, "pause_task_goal must not return an error on Slack")
	require.Contains(t, out, "paused")

	// resume_task_goal
	out, err = handlers["resume_task_goal"](ctx, getInput)
	require.NoError(t, err, "resume_task_goal must not return an error on Slack")
	require.Contains(t, out, "active")

	// clear_task_goal
	out, err = handlers["clear_task_goal"](ctx, getInput)
	require.NoError(t, err, "clear_task_goal must not return an error on Slack")
	require.Contains(t, out, "ok")
}

// TestSlackService_GoalTools_UnavailableWithoutService verifies that when
// taskGoalSvc is nil (not yet wired), goal tools return a descriptive error
// rather than panicking.
func TestSlackService_GoalTools_UnavailableWithoutService(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)

	project := &models.Project{Name: "Slack Goal Nil Svc", RepoPath: "/tmp/test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := NewSlackService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, nil, nil, nil, nil, nil, nil)
	// taskGoalSvc intentionally not set

	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1"}
	handlers := svc.slackActionHandlers(project.ID, actionCtx, nil)

	input, _ := json.Marshal(map[string]string{"task_id": "any"})
	for _, name := range []string{"set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked"} {
		_, err := handlers[name](ctx, input)
		require.Error(t, err, "expected error when taskGoalSvc is nil for handler %s", name)
		require.Contains(t, err.Error(), "task goal service unavailable", "handler %s should report service unavailable", name)
	}
}

// TestSlackService_GoalTools_MarkAchievedReportBlocked verifies that
// mark_task_goal_achieved and report_task_goal_blocked are callable from Slack
// (previously blocked by the unavailable stub).
func TestSlackService_GoalTools_MarkAchievedReportBlocked(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	taskGoalRepo := repository.NewTaskGoalRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	goalSvc := NewTaskGoalService(taskGoalRepo, taskRepo, nil)

	project := &models.Project{Name: "Slack Goal Achieved", RepoPath: "/tmp/test", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "slack-mark-achieved-task",
		Prompt:    "do something",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	goal, err := goalSvc.SetGoal(ctx, task.ID, "ship it", GoalOptions{Actor: "test"})
	require.NoError(t, err)

	svc := NewSlackService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, taskSvc, nil, workerSvc, nil, nil, nil)
	svc.taskGoalSvc = goalSvc

	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}
	handlers := svc.slackActionHandlers(project.ID, actionCtx, nil)

	achievedInput, _ := json.Marshal(map[string]string{
		"task_id": task.ID,
		"goal_id": goal.GoalID,
		"reason":  "done",
	})
	out, err := handlers["mark_task_goal_achieved"](ctx, achievedInput)
	require.NoError(t, err, "mark_task_goal_achieved must work on Slack when goal service is wired")
	require.Contains(t, out, "achieved")
}

func TestSlackService_SendOutboundMessage_PostsChannelAndThread(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var gotChannel, gotThread, gotText string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		gotChannel, gotThread, gotText = channelID, threadTS, text
		return "1710000000.000001", nil
	}
	res := svc.SendOutboundMessage(context.Background(), "C123", "1690000000.000000", "hello")
	require.True(t, res.OK)
	require.Equal(t, "C123", gotChannel)
	require.Equal(t, "1690000000.000000", gotThread)
	require.Equal(t, "hello", gotText)
	require.Equal(t, "1710000000.000001", res.MessageID)
}

func TestSlackService_SendOutboundDirectMessage_OpensDMBeforePosting(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var gotUser, gotChannel, gotThread, gotText string
	svc.openConversationFn = func(userID string) (string, error) {
		gotUser = userID
		return "D123", nil
	}
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		gotChannel, gotThread, gotText = channelID, threadTS, text
		return "1710000000.000002", nil
	}

	res := svc.SendOutboundDirectMessage(context.Background(), "U0AQYLJR14Y", "hi")
	require.True(t, res.OK)
	require.Equal(t, "slack:U0AQYLJR14Y", res.Target)
	require.Equal(t, "1710000000.000002", res.MessageID)
	require.Equal(t, "U0AQYLJR14Y", gotUser)
	require.Equal(t, "D123", gotChannel)
	require.Empty(t, gotThread)
	require.Equal(t, "hi", gotText)
}

func TestSlackService_SendOutboundMessage_MissingTokenReturnsCleanError(t *testing.T) {
	svc := NewSlackService(repository.NewSettingsRepo(testutil.NewTestDB(t)), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	res := svc.SendOutboundMessage(context.Background(), "C123", "", "hello")
	require.False(t, res.OK)
	require.Contains(t, res.Error, "slack bot token is not configured")
}

func TestSlackService_ConnectURLIncludesFilesReadScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientID, "cid"))
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingClientSecret, "secret"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	authURL, err := svc.ConnectURL(context.Background(), "http://localhost/callback")
	require.NoError(t, err)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	scopes := strings.Split(parsed.Query().Get("scope"), ",")
	require.Contains(t, scopes, "files:read")
	require.Contains(t, scopes, "im:write")
}

func TestSlackService_EventFilteringPreservesFileMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var received []slackIncomingMessage
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) { received = append(received, msg) }

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		Text:      "<@UBOT> what is this?",
		TimeStamp: "1710000000.100000",
		Files: []slack.File{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               7,
			URLPrivateDownload: "https://files.slack.test/screenshot.png",
		}},
	})

	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "im",
		SubType:     "file_share",
		User:        "U2",
		Channel:     "D1",
		Text:        "what is this?",
		TimeStamp:   "1710000001.100000",
		Message: &slack.Msg{Files: []slack.File{{
			ID:                 "F2",
			Name:               "dm-shot.png",
			Mimetype:           "image/png",
			Size:               8,
			URLPrivateDownload: "https://files.slack.test/dm-shot.png",
		}}},
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "channel",
		SubType:     "file_share",
		User:        "U3",
		Channel:     "C2",
		Text:        "<@UBOT> what is this?",
		TimeStamp:   "1710000002.100000",
		Message: &slack.Msg{Files: []slack.File{{
			ID:                 "F3",
			Name:               "channel-shot.png",
			Mimetype:           "image/png",
			Size:               9,
			URLPrivateDownload: "https://files.slack.test/channel-shot.png",
		}}},
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "channel",
		SubType:     "file_share",
		User:        "U4",
		Channel:     "C3",
		Text:        "unmentioned screenshot",
		TimeStamp:   "1710000003.100000",
		Message: &slack.Msg{Files: []slack.File{{
			ID:                 "F4",
			Name:               "ignored-shot.png",
			Mimetype:           "image/png",
			Size:               10,
			URLPrivateDownload: "https://files.slack.test/ignored-shot.png",
		}}},
	})

	require.Len(t, received, 3)
	require.Len(t, received[0].Files, 1)
	require.Equal(t, "F1", received[0].Files[0].ID)
	require.Equal(t, "screenshot.png", received[0].Files[0].Name)
	require.Len(t, received[1].Files, 1)
	require.Equal(t, "F2", received[1].Files[0].ID)
	require.Equal(t, "dm-shot.png", received[1].Files[0].Name)
	require.Len(t, received[2].Files, 1)
	require.Equal(t, "F3", received[2].Files[0].ID)
	require.Equal(t, "channel-shot.png", received[2].Files[0].Name)
}

func TestSlackService_EventFilteringAcceptsAttachmentOnlyMessages(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var received []slackIncomingMessage
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) { received = append(received, msg) }

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		Text:      "<@UBOT>",
		TimeStamp: "1710000000.100000",
		Files: []slack.File{{
			ID:                 "F1",
			Name:               "mention-only.png",
			Mimetype:           "image/png",
			Size:               7,
			URLPrivateDownload: "https://files.slack.test/mention-only.png",
		}},
	})
	svc.handleMessageEvent(context.Background(), "T1", slackevents.MessageEvent{
		ChannelType: "im",
		SubType:     "file_share",
		User:        "U2",
		Channel:     "D1",
		Text:        "",
		TimeStamp:   "1710000001.100000",
		Message: &slack.Msg{Files: []slack.File{{
			ID:                 "F2",
			Name:               "dm-only.png",
			Mimetype:           "image/png",
			Size:               8,
			URLPrivateDownload: "https://files.slack.test/dm-only.png",
		}}},
	})

	require.Len(t, received, 2)
	require.Equal(t, "Please analyze the attachment.", received[0].Text)
	require.Equal(t, "F1", received[0].Files[0].ID)
	require.Equal(t, "Please analyze the attachment.", received[1].Text)
	require.Equal(t, "F2", received[1].Files[0].ID)
}

func TestSlackService_EventFilteringPreservesAttachmentAndBlockFileReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(context.Background(), SlackSettingBotUserID, "UBOT"))

	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var received []slackIncomingMessage
	svc.processIncomingMessageFn = func(msg slackIncomingMessage) { received = append(received, msg) }

	svc.handleAppMention(context.Background(), "T1", slackevents.AppMentionEvent{
		User:      "U1",
		Channel:   "C1",
		Text:      "<@UBOT> what is this?",
		TimeStamp: "1710000000.100000",
		Attachments: []slack.Attachment{{
			Title:    "attachment-shot.png",
			ImageURL: slackTestURL("/attachment-shot.png"),
		}},
		Blocks: slack.Blocks{BlockSet: []slack.Block{
			slack.NewImageBlockSlackFile(&slack.SlackFileObject{ID: "F-BLOCK"}, "block-shot.png", "b1", nil),
		}},
	})

	require.Len(t, received, 1)
	require.Len(t, received[0].Files, 2)
	require.Equal(t, "attachment-shot.png", received[0].Files[0].Name)
	require.Equal(t, slackTestURL("/attachment-shot.png"), received[0].Files[0].URLPrivate)
	require.Equal(t, "F-BLOCK", received[0].Files[1].ID)
	require.Equal(t, "block-shot.png", received[0].Files[1].Name)
}

func TestSlackService_EventFilteringIgnoresExternalAttachmentImageURLs(t *testing.T) {
	files := slackIncomingFilesFromAttachments([]slack.Attachment{{
		Title:    "external.png",
		ImageURL: "https://example.com/external.png",
		ThumbURL: "http://localhost/thumb.png",
		Blocks: slack.Blocks{BlockSet: []slack.Block{
			slack.NewImageBlockSlackFile(&slack.SlackFileObject{URL: "https://evil.example.com/file.png"}, "external-block.png", "b1", nil),
			slack.NewImageBlock("http://127.0.0.1/file.png", "localhost-block.png", "b2", nil),
		}},
	}})
	require.Empty(t, files)
}

func TestSlackService_ProcessIncomingMessage_DownloadsPersistsAndPassesImageAttachment(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	uploadRoot := useTempSlackUploadsDir(t, svc)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               7,
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "screenshot.png", got.ImageAttachments[0].FileName)
	require.Equal(t, "image/png", got.ImageAttachments[0].MediaType)
	require.FileExists(t, got.ImageAttachments[0].FilePath)
	require.Contains(t, got.ImageAttachments[0].FilePath, filepath.Join(uploadRoot, "chat", got.ExecID))
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, savedImage)
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, got.ExecID)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	require.Equal(t, got.ImageAttachments[0].FilePath, persisted[0].FilePath)
	select {
	case evt := <-sub:
		require.Equal(t, events.ChatNewMessage, evt.Type)
		require.Equal(t, got.ExecID, evt.ExecID)
		require.True(t, evt.HasAttachments)
	default:
		t.Fatal("expected chat new message event")
	}
}

func TestSlackService_ProcessIncomingMessage_SelectsVisionAgentForImageAttachment(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Vision Routing Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAgent)
	defaultAgent.Provider = models.ProviderAnthropic
	defaultAgent.AuthMethod = models.AuthMethodCLI
	defaultAgent.Model = "claude-sonnet-4-5"
	defaultAgent.IsDefault = true
	require.NoError(t, llmConfigRepo.Update(ctx, defaultAgent))
	visionAgent := &models.LLMConfig{
		Name:       "Vision Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-3-5-sonnet-20241022",
	}
	require.NoError(t, llmConfigRepo.Create(ctx, visionAgent))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, visionAgent.ID, got.Agent.ID)
	require.False(t, got.Agent.IsAnthropicCLI())
}

func TestSlackService_ProcessIncomingMessage_SelectsVisionAgentForIDOnlyFileInfoImage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack ID Only Vision Routing Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAgent)
	defaultAgent.Provider = models.ProviderAnthropic
	defaultAgent.AuthMethod = models.AuthMethodCLI
	defaultAgent.Model = "claude-sonnet-4-5"
	defaultAgent.IsDefault = true
	require.NoError(t, llmConfigRepo.Update(ctx, defaultAgent))
	visionAgent := &models.LLMConfig{
		Name:       "Vision Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-3-5-sonnet-20241022",
	}
	require.NoError(t, llmConfigRepo.Create(ctx, visionAgent))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	var fileInfoCalls int
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileInfoCalls++
		require.Equal(t, "/files.info", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "xoxb-test", r.Form.Get("token"))
		require.Equal(t, "FIDONLY", r.Form.Get("file"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"file":{"id":"FIDONLY","name":"id-only-shot.png","title":"id-only-shot.png","mimetype":"image/png","size":%d,"url_private_download":%q}}`, len(slackTestPNGBytes), slackTestURL("/id-only-shot.png"))
	}))
	defer apiServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.botClient = slack.New("xoxb-test", slack.OptionAPIURL(apiServer.URL+"/"), slack.OptionHTTPClient(apiServer.Client()))
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:         "FIDONLY",
			FileAccess: "check_file_info",
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "id-only-shot.png", got.ImageAttachments[0].FileName)
	require.Equal(t, visionAgent.ID, got.Agent.ID)
	require.False(t, got.Agent.IsAnthropicCLI())
	require.Equal(t, 1, fileInfoCalls)
}

func TestSlackService_ProcessIncomingMessage_SniffsUnknownTypeImageWhenFileInfoFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Unknown Type Vision Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAgent)
	defaultAgent.Provider = models.ProviderAnthropic
	defaultAgent.AuthMethod = models.AuthMethodCLI
	defaultAgent.Model = "claude-sonnet-4-5"
	defaultAgent.IsDefault = true
	require.NoError(t, llmConfigRepo.Update(ctx, defaultAgent))
	visionAgent := &models.LLMConfig{
		Name:       "Vision Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-3-5-sonnet-20241022",
	}
	require.NoError(t, llmConfigRepo.Create(ctx, visionAgent))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	var fileInfoCalls int
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileInfoCalls++
		require.Equal(t, "/files.info", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "FUNKNOWN", r.Form.Get("file"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
	}))
	defer apiServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.botClient = slack.New("xoxb-test", slack.OptionAPIURL(apiServer.URL+"/"), slack.OptionHTTPClient(apiServer.Client()))
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "FUNKNOWN",
			Name:               "screenshot",
			Mimetype:           "application/octet-stream",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/unknown-bytes"),
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "image/png", got.ImageAttachments[0].MediaType)
	require.Equal(t, visionAgent.ID, got.Agent.ID)
	require.False(t, got.Agent.IsAnthropicCLI())
	persistedExec, err := execRepo.GetByID(ctx, got.ExecID)
	require.NoError(t, err)
	require.NotNil(t, persistedExec)
	require.Equal(t, visionAgent.ID, persistedExec.AgentConfigID)
	require.Equal(t, 1, fileInfoCalls)
}

func TestSlackService_ProcessIncomingMessage_DuplicateImageNamesGetDistinctLinkedPaths(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Duplicate Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "compare these screenshots",
		Source:    "slack",
		Files: []slackIncomingFile{
			{ID: "F1", Name: "screenshot.png", Mimetype: "image/png", Size: len(slackTestPNGBytes), URLPrivateDownload: slackTestURL("/one.png")},
			{ID: "F2", Name: "screenshot.png", Mimetype: "image/png", Size: len(slackTestPNGBytes), URLPrivateDownload: slackTestURL("/two.png")},
		},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 2)
	require.Equal(t, "screenshot.png", got.ImageAttachments[0].FileName)
	require.Equal(t, "screenshot.png", got.ImageAttachments[1].FileName)
	require.NotEqual(t, got.ImageAttachments[0].FilePath, got.ImageAttachments[1].FilePath)
	require.FileExists(t, got.ImageAttachments[0].FilePath)
	require.FileExists(t, got.ImageAttachments[1].FilePath)
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, got.ExecID)
	require.NoError(t, err)
	require.Len(t, persisted, 2)
}

func TestSlackService_LinkAttachmentsToExecutionRollsBackPartialLinksOnFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, _, _, chatAttachmentRepo, settingsRepo, _, _ := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Attachment Rollback Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Slack attachment rollback",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		Prompt:    "test",
		AgentID:   &agent.ID,
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	require.NoError(t, execRepo.Create(ctx, exec))

	sourceDir := t.TempDir()
	okPath := filepath.Join(sourceDir, "ok.png")
	require.NoError(t, os.WriteFile(okPath, slackTestPNGBytes, 0644))
	missingPath := filepath.Join(sourceDir, "missing.png")
	uploadRoot := t.TempDir()
	svc := NewSlackService(settingsRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.SetUploadsDir(uploadRoot)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)

	linked, err := svc.linkAttachmentsToExecution(ctx, exec.ID, []models.ChatAttachment{
		{FileName: "ok.png", FilePath: okPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
		{FileName: "missing.png", FilePath: missingPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
	})

	require.Error(t, err)
	require.Empty(t, linked)
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	require.NoError(t, err)
	require.Empty(t, persisted)
	execDir := filepath.Join(uploadRoot, "chat", exec.ID)
	entries, err := os.ReadDir(execDir)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestSlackService_ProcessIncomingMessage_AttachmentStoreFailurePersistsFailedTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Attachment Store Failure Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	uploadRoot := filepath.Join(t.TempDir(), "uploads-file")
	require.NoError(t, os.WriteFile(uploadRoot, []byte("not a directory"), 0644))
	svc.SetUploadsDir(uploadRoot)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	var slackReply string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		slackReply = text
		return "", nil
	}
	var runnerCalled bool
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runnerCalled = true })

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "store this screenshot",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.False(t, runnerCalled)
	require.Contains(t, slackReply, "Failed to process attachment: unable to store attachment")
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("expected failed attachment chat event")
	}
	require.Equal(t, events.ChatNewMessage, evt.Type)
	require.NotEmpty(t, evt.ExecID)
	require.True(t, evt.HasAttachments)
	createdExec, err := execRepo.GetByID(ctx, evt.ExecID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, createdExec.Status)
	require.Contains(t, createdExec.ErrorMessage, "unable to store attachment")
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, evt.ExecID)
	require.NoError(t, err)
	require.Empty(t, persisted)
}

func TestSlackService_ProcessIncomingMessage_ResolvesSlackFileInfoPlaceholder(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack FileInfo Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/files.info", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "xoxb-test", r.Form.Get("token"))
		require.Equal(t, "FPLACEHOLDER", r.Form.Get("file"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"file":{"id":"FPLACEHOLDER","name":"resolved-shot.png","title":"resolved-shot.png","mimetype":"image/png","size":%d,"url_private_download":%q}}`, len(slackTestPNGBytes), slackTestURL("/resolved-shot.png"))
	}))
	defer apiServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.botClient = slack.New("xoxb-test", slack.OptionAPIURL(apiServer.URL+"/"), slack.OptionHTTPClient(apiServer.Client()))
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:         "FPLACEHOLDER",
			Name:       "placeholder.png",
			Mimetype:   "image/png",
			FileAccess: "check_file_info",
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "resolved-shot.png", got.ImageAttachments[0].FileName)
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, savedImage)
}

func TestSlackService_ProcessIncomingMessage_UsesURLPrivateWhenOptionalFileInfoFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Optional FileInfo Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/files.info", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "FURLPRIVATE", r.Form.Get("file"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
	}))
	defer apiServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.botClient = slack.New("xoxb-test", slack.OptionAPIURL(apiServer.URL+"/"), slack.OptionHTTPClient(apiServer.Client()))
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:         "FURLPRIVATE",
			Name:       "url-private-shot.png",
			Mimetype:   "image/png",
			Size:       len(slackTestPNGBytes),
			URLPrivate: slackTestURL("/url-private-shot.png"),
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	require.Equal(t, "url-private-shot.png", got.ImageAttachments[0].FileName)
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, savedImage)
}

func TestSlackService_ProcessIncomingMessage_FallsBackToSlackImageThumbnailWhenDownloadURLReturnsHTML(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Thumbnail Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/download.png":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>Slack file viewer</html>"))
		case "/thumb.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(slackTestPNGBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/download.png"),
			Thumb1024:          slackTestURL("/thumb.png"),
		}},
	})

	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, savedImage)
}

func TestSlackService_ProcessIncomingMessage_AttachmentFailureStillPersistsChatTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, _, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Attachment Failure Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>sign in to Slack</html>"))
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var runnerCalled bool
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runnerCalled = true })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               1024,
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.False(t, runnerCalled, "failed attachment processing should not start the LLM runner")
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	default:
		t.Fatal("expected chat new message event")
	}
	require.Equal(t, events.ChatNewMessage, evt.Type)
	require.Equal(t, project.ID, evt.ProjectID)
	require.True(t, evt.HasAttachments)
	require.NotEmpty(t, evt.ExecID)

	createdExec, err := execRepo.GetByID(ctx, evt.ExecID)
	require.NoError(t, err)
	require.NotNil(t, createdExec)
	require.Equal(t, models.ExecFailed, createdExec.Status)
	require.Contains(t, createdExec.ErrorMessage, "Failed to process attachment")

	createdTask, err := taskRepo.GetByID(ctx, evt.TaskID)
	require.NoError(t, err)
	require.NotNil(t, createdTask)
	require.Equal(t, models.StatusFailed, createdTask.Status)
	require.Equal(t, models.TaskOriginSlack, createdTask.CreatedVia)
}

func TestSlackService_ProcessIncomingMessage_PreservesBotTokenAcrossSlackFileRedirect(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Redirect Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	var cdnSawAuth string
	cdnServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnSawAuth = r.Header.Get("Authorization")
		if cdnSawAuth != "Bearer xoxb-test" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>sign in to Slack</html>"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer cdnServer.Close()
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		http.Redirect(w, r, "https://downloads.slack-files.com/screenshot.png", http.StatusFound)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServers(t, svc, map[string]*httptest.Server{
		"files.slack.com":           fileServer,
		"downloads.slack-files.com": cdnServer,
	})
	svc.SetUploadsDir(t.TempDir())
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = &req })
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.Equal(t, "Bearer xoxb-test", cdnSawAuth)
	require.NotNil(t, got)
	require.Len(t, got.ImageAttachments, 1)
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, savedImage)
}

func TestSlackService_DownloadSlackPrivateFileRejectsUntrustedOriginalHost(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, rawURL := range []string{
		"https://example.com/screenshot.png",
		"http://localhost/screenshot.png",
		"http://127.0.0.1/screenshot.png",
		"http://[::1]/screenshot.png",
	} {
		err := svc.downloadSlackPrivateFile(context.Background(), "xoxb-secret", rawURL, io.Discard)
		require.Error(t, err, rawURL)
		require.Contains(t, err.Error(), "not trusted", rawURL)
	}
}

func TestSlackService_DownloadSlackPrivateFileRejectsUntrustedRedirect(t *testing.T) {
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("untrusted redirect target should not be requested")
	}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)

	redirectingTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{targetServer.URL + "/steal"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.httpClient = &http.Client{Transport: redirectingTransport}

	err = svc.downloadSlackPrivateFile(context.Background(), "xoxb-secret", "https://files.slack.com/files-pri/T-F/download.png", io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "untrusted host")
	require.Contains(t, err.Error(), targetURL.Host)
}

func TestSlackService_DownloadSlackPrivateFileEnforcesBodySizeLimit(t *testing.T) {
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bytes.Repeat([]byte("a"), slackMaxFileSize+1))
	}))
	defer fileServer.Close()

	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	useSlackFileServer(t, svc, fileServer)
	dest, err := os.CreateTemp(t.TempDir(), "slack-large-*")
	require.NoError(t, err)
	defer dest.Close()

	err = svc.downloadSlackPrivateFile(context.Background(), "xoxb-secret", slackTestURL("/too-large.bin"), dest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeded maximum size")
	info, statErr := dest.Stat()
	require.NoError(t, statErr)
	require.EqualValues(t, slackMaxFileSize, info.Size())
}

func TestSlackService_ProcessIncomingMessage_RejectsInvalidSlackImageBytes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, _, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	project := &models.Project{Name: "Slack Invalid Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	var slackReply string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		slackReply = text
		return "", nil
	}
	runnerCalled := false
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runnerCalled = true })

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "screenshot.png",
			Mimetype:           "image/png",
			Size:               24,
			URLPrivateDownload: slackTestURL("/screenshot.png"),
		}},
	})

	require.False(t, runnerCalled)
	require.Contains(t, slackReply, "Failed to process attachment")
	require.Contains(t, slackReply, "not a valid supported image")
}

func TestSlackService_SetUploadsDir_NormalizesConfiguredRoot(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	relativeRoot := filepath.Join(".", "configured-slack-uploads")
	expected, err := filepath.Abs(relativeRoot)
	require.NoError(t, err)
	svc.SetUploadsDir(relativeRoot)
	require.Equal(t, expected, svc.uploadsDir)
}

func TestSlackService_ProcessIncomingMessage_QueuesSlackFilesInAttachmentSession(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, _, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Slack Queued Attachment Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	uploadRoot := useTempSlackUploadsDir(t, svc)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) { return "", nil }

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "queued.png",
			Mimetype:           "image/png",
			Size:               10,
			URLPrivateDownload: slackTestURL("/queued.png"),
		}, {
			ID:                 "F2",
			Name:               "queued-second.png",
			Mimetype:           "image/png",
			Size:               10,
			URLPrivateDownload: slackTestURL("/queued-second.png"),
		}},
	})

	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.NotEmpty(t, inputs[0].AttachmentSessionID)
	require.Equal(t, models.TaskOriginSlack, inputs[0].Source)
	entries, err := os.ReadDir(filepath.Join(uploadRoot, "chat", "pending", inputs[0].AttachmentSessionID))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		staged, err := os.ReadFile(filepath.Join(uploadRoot, "chat", "pending", inputs[0].AttachmentSessionID, entry.Name()))
		require.NoError(t, err)
		require.Equal(t, slackTestPNGBytes, staged)
	}
	select {
	case evt := <-sub:
		require.Equal(t, events.ChatNewMessage, evt.Type)
		require.True(t, evt.Queued)
		require.True(t, evt.HasAttachments)
	default:
		t.Fatal("expected queued chat event")
	}
}

func TestSlackService_SaveChatAttachmentsToPendingSessionCleansSourceWhenFirstStageFails(t *testing.T) {
	svc := NewSlackService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.SetUploadsDir(t.TempDir())
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "queued.png")

	sessionID, err := svc.saveChatAttachmentsToPendingSession([]models.ChatAttachment{{
		FileName:  "queued.png",
		FilePath:  sourcePath,
		MediaType: "image/png",
		FileSize:  int64(len(slackTestPNGBytes)),
	}})

	require.Error(t, err)
	require.Empty(t, sessionID)
	require.NoDirExists(t, sourceDir)
}

func TestSlackService_ProcessIncomingMessage_QueuedAttachmentDownloadFailurePersistsChatTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, _, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Slack Queued Attachment Failure Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>sign in to Slack</html>"))
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	svc.SetUploadsDir(t.TempDir())
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	var slackReply string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		slackReply = text
		return "", nil
	}

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "what is this screenshot?",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "queued-bad.png",
			Mimetype:           "image/png",
			Size:               10,
			URLPrivateDownload: slackTestURL("/queued-bad.png"),
		}},
	})

	require.Contains(t, slackReply, "Failed to process attachment: unable to download attachment")
	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, inputs)
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("expected failed queued attachment chat event")
	}
	require.Equal(t, events.ChatNewMessage, evt.Type)
	require.Equal(t, project.ID, evt.ProjectID)
	require.NotEmpty(t, evt.ExecID)
	require.False(t, evt.Queued)
	require.True(t, evt.HasAttachments)
	createdExec, err := execRepo.GetByID(ctx, evt.ExecID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, createdExec.Status)
	require.Contains(t, createdExec.ErrorMessage, "unable to download attachment")
}

func TestSlackService_ProcessIncomingMessage_QueuedAttachmentStagingFailurePersistsChatTurn(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, _, settingsRepo, slackUserProjectRepo, slackTaskContextRepo := newSlackAttachmentTestRepos(t, db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Slack Queued Attachment Staging Failure Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
	require.NoError(t, settingsRepo.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
	agent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, agent)
	activeTask := &models.Task{ProjectID: project.ID, Title: "active", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active", AgentID: &agent.ID, CreatedVia: models.TaskOriginSlack}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer xoxb-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	useSlackFileServer(t, svc, fileServer)
	uploadRoot := filepath.Join(t.TempDir(), "uploads-file")
	require.NoError(t, os.WriteFile(uploadRoot, []byte("not a directory"), 0644))
	svc.SetUploadsDir(uploadRoot)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer svc.chatBroadcaster.Unsubscribe(sub)
	svc.setActiveProject(ctx, "T1", "U1", project.ID)
	var slackReply string
	svc.postMessageFn = func(channelID, threadTS, text string) (string, error) {
		slackReply = text
		return "", nil
	}

	svc.processIncomingMessage(slackIncomingMessage{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
		Text:      "store this screenshot",
		Source:    "slack",
		Files: []slackIncomingFile{{
			ID:                 "F1",
			Name:               "queued.png",
			Mimetype:           "image/png",
			Size:               len(slackTestPNGBytes),
			URLPrivateDownload: slackTestURL("/queued.png"),
		}},
	})

	require.Contains(t, slackReply, "Failed to process attachment: unable to store attachment")
	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, inputs)
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("expected failed queued attachment chat event")
	}
	require.Equal(t, events.ChatNewMessage, evt.Type)
	require.NotEmpty(t, evt.ExecID)
	require.True(t, evt.HasAttachments)
	createdExec, err := execRepo.GetByID(ctx, evt.ExecID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, createdExec.Status)
	require.Contains(t, createdExec.ErrorMessage, "unable to store attachment")
}

func newSlackAttachmentTestRepos(t *testing.T, db interface{}) (*repository.ProjectRepo, *repository.TaskRepo, *repository.LLMConfigRepo, *repository.ExecutionRepo, *repository.ScheduleRepo, *repository.AttachmentRepo, *repository.ChatAttachmentRepo, *repository.SettingsRepo, *repository.SlackUserProjectRepo, *repository.SlackTaskContextRepo) {
	t.Helper()
	sqlDB, ok := db.(*sql.DB)
	require.True(t, ok)
	projectRepo := repository.NewProjectRepo(sqlDB)
	taskRepo := repository.NewTaskRepo(sqlDB, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(sqlDB)
	execRepo := repository.NewExecutionRepo(sqlDB)
	scheduleRepo := repository.NewScheduleRepo(sqlDB)
	attachmentRepo := repository.NewAttachmentRepo(sqlDB)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(sqlDB)
	settingsRepo := repository.NewSettingsRepo(sqlDB)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(sqlDB)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(sqlDB)
	return projectRepo, taskRepo, llmConfigRepo, execRepo, scheduleRepo, attachmentRepo, chatAttachmentRepo, settingsRepo, slackUserProjectRepo, slackTaskContextRepo
}

func useTempSlackUploadsDir(t *testing.T, svc *SlackService) string {
	t.Helper()
	dir := t.TempDir()
	svc.SetUploadsDir(dir)
	return dir
}

func TestSlackService_RuntimeSwitchProject_PersistsToRepo(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	slackUserProjectRepo := repository.NewSlackUserProjectRepo(db)
	slackTaskContextRepo := repository.NewSlackTaskContextRepo(db)

	project1 := &models.Project{Name: "Alpha", RepoPath: "/tmp/alpha", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project1))
	project2 := &models.Project{Name: "Beta", RepoPath: "/tmp/beta"}
	require.NoError(t, projectRepo.Create(ctx, project2))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// nil slackAuthRepo means checkAuthorization always returns true.
	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)

	actionCtx := slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}
	rt := svc.buildSlackActionToolRuntime(project1.ID, actionCtx, nil)
	require.NotNil(t, rt)

	// Execute switch_project via the runtime tool executor.
	output, handled, isErr, err := rt.Executor(ctx, "switch_project", json.RawMessage(`{"project":"Beta"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "Beta")

	// Assert persistence: the repo row must exist for (T1, U1) → project2.
	savedID, err := slackUserProjectRepo.GetUserProject(ctx, "T1", "U1")
	require.NoError(t, err)
	require.Equal(t, project2.ID, savedID, "switch_project must persist selection to slack_user_projects")

	// Assert getActiveProject reflects the change (loads from DB when cache is cold after the switch).
	svc2 := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)
	activeProjectID, err := svc2.getActiveProject(ctx, "T1", "U1")
	require.NoError(t, err)
	require.Equal(t, project2.ID, activeProjectID,
		"getActiveProject must return the newly-persisted project on next session")
}
