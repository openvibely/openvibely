package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/slack-go/slack/slackevents"
	"github.com/stretchr/testify/require"
)

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

func TestSlackService_EventFilteringAcceptsDMAndAppMentions(t *testing.T) {
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

	require.Len(t, received, 2)
	require.Equal(t, "T1", received[0].TeamID)
	require.Equal(t, "C1", received[0].ChannelID)
	require.Equal(t, "hello from mention", received[0].Text)
	require.Equal(t, "D1", received[1].ChannelID)
	require.Equal(t, "hello from dm", received[1].Text)
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

func TestSlackService_ProcessChatMarkers_CreatedTasksGetSlackOriginAndContext(t *testing.T) {
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

	project := &models.Project{Name: "Slack Marker Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)

	chatTask := &models.Task{ProjectID: project.ID, Title: "chat", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "hello"}
	require.NoError(t, taskRepo.Create(ctx, chatTask))
	defaultAgent, err := llmConfigRepo.GetDefault(ctx)
	require.NoError(t, err)
	require.NotNil(t, defaultAgent)
	exec := &models.Execution{TaskID: chatTask.ID, AgentConfigID: defaultAgent.ID, Status: models.ExecRunning, PromptSent: "prompt"}
	require.NoError(t, execRepo.Create(ctx, exec))

	output := `[CREATE_TASK]{"title":"Slack Created","prompt":"Do it"}[/CREATE_TASK]`
	updated := svc.processChatMarkers(ctx, exec.ID, project.ID, output, slackMarkerContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"})
	require.NotEmpty(t, updated)
	require.Contains(t, updated, "Created 1 task(s):")

	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)

	var created *models.Task
	for i := range tasks {
		if tasks[i].Title == "Slack Created" {
			created = &tasks[i]
			break
		}
	}
	require.NotNil(t, created)
	require.Equal(t, models.TaskOriginSlack, created.CreatedVia)

	stc, err := slackTaskContextRepo.GetByTaskID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stc)
	require.Equal(t, "C1", stc.SlackChannelID)
	require.Equal(t, "1710000000.100000", stc.SlackThreadTS)
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

	svc := NewSlackService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, slackUserProjectRepo, slackTaskContextRepo, nil)

	collector := newChannelActionSummaryCollector()
	rt := svc.buildSlackActionToolRuntime(project.ID, slackMarkerContext{
		TeamID:    "T1",
		ChannelID: "C1",
		ThreadTS:  "1710000000.100000",
		UserID:    "U1",
	}, collector)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "create_task", json.RawMessage(`{"title":"Slack Tool Created","prompt":"Do it"}`))
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

	stc, err := slackTaskContextRepo.GetByTaskID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, stc)
	require.Equal(t, "C1", stc.SlackChannelID)
	require.Equal(t, "1710000000.100000", stc.SlackThreadTS)

	finalOutput := collector.appendToOutput("Done.")
	require.Contains(t, finalOutput, "[TASK_ID:")
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

	rt := svc.buildSlackActionToolRuntime(project.ID, slackMarkerContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
	require.NotNil(t, rt)

	output, handled, isErr, err := rt.Executor(ctx, "list_alerts", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, "No alerts found")
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

	rt := svc.buildSlackActionToolRuntime(project.ID, slackMarkerContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
	require.NotNil(t, rt)

	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceSlack, true)
	require.NotEmpty(t, defs)

	for _, d := range defs {
		_, handled, _, _ := rt.Executor(ctx, d.Name, json.RawMessage(`{}`))
		require.Truef(t, handled, "tool should be handled by slack runtime executor: %s", d.Name)
	}

	handlers := svc.slackActionHandlers(project.ID, slackMarkerContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "1710000000.100000", UserID: "U1"}, nil)
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
	svc.postMessageFn = func(channelID, threadTS, text string) error {
		responses = append(responses, text)
		return nil
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
	svc.postMessageFn = func(channelID, threadTS, text string) error {
		called = true
		require.Equal(t, "C1", channelID)
		require.Equal(t, "1710000000.100000", threadTS)
		require.True(t, strings.Contains(text, "Task completed") || strings.Contains(text, "Task failed"))
		return nil
	}

	svc.SendTaskCompletionNotification(ctx, *task, "completed output", "")
	require.True(t, called)

	require.NoError(t, settingsRepo.Set(ctx, SlackSettingSendResponses, "false"))
	called = false
	svc.SendTaskCompletionNotification(ctx, *task, "completed output", "")
	require.False(t, called)
}
