package service

import (
	"bytes"
	"context"
	"database/sql"
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

	"github.com/bwmarrin/discordgo"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func newDiscordServiceForTest(t *testing.T) (*DiscordService, *sql.DB, *repository.SettingsRepo, *repository.ProjectRepo, *repository.TaskRepo, *repository.DiscordAuthRepo, *repository.DiscordTaskContextRepo) {
	t.Helper()
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	discordAuthRepo := repository.NewDiscordAuthRepo(db)
	discordTaskContextRepo := repository.NewDiscordTaskContextRepo(db)
	svc := NewDiscordService(settingsRepo, projectRepo, nil, taskRepo, nil, nil, nil, nil, nil, discordAuthRepo, discordTaskContextRepo)
	return svc, db, settingsRepo, projectRepo, taskRepo, discordAuthRepo, discordTaskContextRepo
}

func TestDiscordService_NotificationLifecycleRuntimeUsesPersistedChannelTask(t *testing.T) {
	svc, db, _, projectRepo, taskRepo, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Notification Lifecycle"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	caller := &models.Task{ProjectID: project.ID, Title: "Discord chat", Prompt: "process", Category: models.CategoryChat, Status: models.StatusPending, Priority: 2}
	if err := taskRepo.Create(ctx, caller); err != nil {
		t.Fatal(err)
	}
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	svc.SetAlertService(alertSvc)
	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Type: "suggestion", Title: "Discord suggestion", Severity: models.SeverityInfo})
	if err != nil {
		t.Fatal(err)
	}
	if err := alertSvc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatal(err)
	}
	rt := svc.buildDiscordActionToolRuntimeForTask(project.ID, caller.ID, discordActionContext{ChannelID: "channel", UserID: "user"}, nil)
	output, handled, isErr, err := rt.Executor(ctx, "claim_alert", json.RawMessage(`{"alert_id":"`+alert.ID+`"}`))
	if err != nil || !handled || isErr {
		t.Fatalf("claim_alert failed: output=%s handled=%v isErr=%v err=%v", output, handled, isErr, err)
	}
	if !strings.Contains(output, caller.ID) {
		t.Fatalf("claim output %q does not contain persisted caller task %s", output, caller.ID)
	}
}

func TestDiscordService_GetConnectionStatusRequiresRunningGateway(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, DiscordSettingBotToken, "saved-token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingBotUserID, "bot-1"); err != nil {
		t.Fatalf("set bot user: %v", err)
	}
	svc.mu.Lock()
	svc.running = false
	svc.lastStartError = "open discord gateway: websocket: close 4004: Authentication failed"
	svc.mu.Unlock()

	status, err := svc.GetConnectionStatus(ctx)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !status.Configured || !status.HasBotToken {
		t.Fatalf("expected saved token to make Discord configured, got %#v", status)
	}
	if status.Connected || status.Running {
		t.Fatalf("expected saved token without running gateway to be offline, got %#v", status)
	}
	if !strings.Contains(status.LastError, "Authentication failed") {
		t.Fatalf("expected last gateway error surfaced, got %#v", status)
	}

	svc.mu.Lock()
	svc.running = true
	svc.lastStartError = ""
	svc.mu.Unlock()
	status, err = svc.GetConnectionStatus(ctx)
	if err != nil {
		t.Fatalf("status after running failed: %v", err)
	}
	if !status.Connected || !status.Running || status.LastError != "" {
		t.Fatalf("expected running gateway to be connected, got %#v", status)
	}
}

func TestDiscordService_SendOutboundMessageUsesChannelAndThread(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	var gotChannelID, gotMessageID, gotText string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		gotChannelID, gotMessageID, gotText = channelID, messageID, text
		return "discord-msg-1", nil
	}

	res := svc.SendOutboundMessage(context.Background(), "chan-1", "thread-1", "hello discord")
	if !res.OK || res.Platform != "discord" || res.Target != "discord:chan-1:thread-1" || res.MessageID != "discord-msg-1" {
		t.Fatalf("unexpected outbound result: %#v", res)
	}
	if gotChannelID != "thread-1" || gotMessageID != "" || gotText != "hello discord" {
		t.Fatalf("unexpected outbound send channel=%q message=%q text=%q", gotChannelID, gotMessageID, gotText)
	}
}

func TestDiscordService_SendOutboundDirectMessage_CreatesDMBeforeSending(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	var gotUserID, gotChannelID, gotMessageID, gotText string
	svc.createDMChannelFunc = func(userID string) (string, error) {
		gotUserID = userID
		return "dm-channel-1", nil
	}
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		gotChannelID, gotMessageID, gotText = channelID, messageID, text
		return "discord-dm-msg-1", nil
	}

	res := svc.SendOutboundDirectMessage(context.Background(), "1518288288572641398", "hi")
	if !res.OK || res.Platform != "discord" || res.Target != "discord:1518288288572641398" || res.MessageID != "discord-dm-msg-1" {
		t.Fatalf("unexpected outbound direct result: %#v", res)
	}
	if gotUserID != "1518288288572641398" || gotChannelID != "dm-channel-1" || gotMessageID != "" || gotText != "hi" {
		t.Fatalf("unexpected dm send user=%q channel=%q message=%q text=%q", gotUserID, gotChannelID, gotMessageID, gotText)
	}
}

func TestDiscordActionHandlersCoverAdvertisedRuntimeTools(t *testing.T) {
	svc, db, _, projectRepo, taskRepo, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Full Runtime", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, execRepo, nil)
	taskSvc.SetSwarmService(swarmSvc)
	taskSvc.SetAgentRepo(repository.NewAgentRepo(db))
	svc.llmConfigRepo = llmConfigRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = taskSvc
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	svc.SetAgentRepo(repository.NewAgentRepo(db))
	svc.SetAlertService(NewAlertService(repository.NewAlertRepo(db), nil))

	rt := svc.buildDiscordActionToolRuntime(project.ID, discordActionContext{ChannelID: "chan-1", ThreadID: "thread-1", MessageID: "msg-1", UserID: "user-1"}, nil)
	if rt == nil {
		t.Fatal("expected runtime tools")
	}
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceDiscord, true)
	if len(defs) == 0 {
		t.Fatal("expected Discord runtime definitions")
	}
	for _, d := range defs {
		_, handled, _, _ := rt.Executor(ctx, d.Name, json.RawMessage(`{}`))
		if !handled {
			t.Fatalf("tool should be handled by discord runtime executor: %s", d.Name)
		}
	}

	actionCtx := discordActionContext{ChannelID: "chan-1", ThreadID: "thread-1", MessageID: "msg-1", UserID: "user-1"}
	handlers := svc.discordActionHandlers(project.ID, actionCtx, nil)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceDiscord, true, handlers); err != nil {
		t.Fatalf("discord handler coverage: %v", err)
	}
	out, err := handlers["create_swarm_task"](ctx, json.RawMessage(`{"title":"Discord Swarm Created","prompt":"Split this work","category":"backlog"}`))
	if err != nil {
		t.Fatalf("create_swarm_task handler failed: %v", err)
	}
	if !strings.Contains(out, "Created swarm task: Discord Swarm Created") {
		t.Fatalf("unexpected create_swarm_task output: %s", out)
	}
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	var swarmParent *models.Task
	for i := range tasks {
		if tasks[i].Title == "Discord Swarm Created" {
			swarmParent = &tasks[i]
			break
		}
	}
	if swarmParent == nil {
		t.Fatalf("expected Discord swarm parent to be created")
	}
	if swarmParent.SwarmRole != models.SwarmRoleParent || swarmParent.CreatedVia != models.TaskOriginDiscord {
		t.Fatalf("expected Discord swarm parent with origin, got role=%q origin=%q", swarmParent.SwarmRole, swarmParent.CreatedVia)
	}
	dtc, err := svc.discordTaskContextRepo.GetByTaskID(ctx, swarmParent.ID)
	if err != nil || dtc == nil || dtc.DiscordChannelID != "chan-1" || dtc.DiscordThreadID != "thread-1" {
		t.Fatalf("expected Discord task context for swarm parent, ctx=%#v err=%v", dtc, err)
	}
}

func TestDiscordActionHandlersSendMessageUsesChannelRouter(t *testing.T) {
	svc, db, _, projectRepo, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Outbound"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	targetRepo := repository.NewChannelTargetRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	router := NewChannelMessageRouter(targetRepo, settingsRepo)
	router.SetDiscordService(svc)
	svc.SetChannelMessageRouter(router)
	if err := targetRepo.Upsert(ctx, models.ChannelTarget{ID: "discord-target", ProjectID: project.ID, Platform: "discord", Name: "ops", TargetID: "chan-1", ThreadID: "thread-1"}); err != nil {
		t.Fatalf("save target: %v", err)
	}
	var gotChannelID, gotMessageID, gotText string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		gotChannelID, gotMessageID, gotText = channelID, messageID, text
		return "discord-msg-2", nil
	}

	handlers := svc.discordActionHandlers(project.ID, discordActionContext{UserID: "discord-user"}, nil)
	out, err := handlers["send_message"](ctx, []byte(`{"target":"discord:#ops","message":"hello ops"}`))
	if err != nil {
		t.Fatalf("send_message handler failed: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `discord-msg-2`) {
		t.Fatalf("unexpected send_message output: %s", out)
	}
	if gotChannelID != "thread-1" || gotMessageID != "" || gotText != "hello ops" {
		t.Fatalf("unexpected routed send channel=%q message=%q text=%q", gotChannelID, gotMessageID, gotText)
	}
}

func TestDiscordHandleMessageCreateIgnoresSelfAndBotMessages(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, DiscordSettingBotUserID, "bot-1"); err != nil {
		t.Fatalf("set bot id: %v", err)
	}
	var processed int
	svc.processIncomingMessageFn = func(msg discordIncomingMessage) { processed++ }

	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", ChannelID: "c1", Content: "hello", Author: &discordgo.User{ID: "bot-1"}}})
	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "m2", ChannelID: "c1", Content: "hello", Author: &discordgo.User{ID: "other-bot", Bot: true}}})

	if processed != 0 {
		t.Fatalf("expected self/bot messages ignored, processed=%d", processed)
	}
}

func TestDiscordHandleMessageCreateRequiresMentionInGuildAndStripsMention(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, DiscordSettingBotUserID, "bot-1"); err != nil {
		t.Fatalf("set bot id: %v", err)
	}
	var got []discordIncomingMessage
	svc.processIncomingMessageFn = func(msg discordIncomingMessage) { got = append(got, msg) }

	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", ChannelID: "c1", GuildID: "g1", Content: "hello", Author: &discordgo.User{ID: "u1"}}})
	if len(got) != 0 {
		t.Fatalf("expected unmentioned guild message ignored, got %#v", got)
	}

	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "m2", ChannelID: "c1", GuildID: "g1", Content: "<@bot-1> please help", Author: &discordgo.User{ID: "u1"}, Mentions: []*discordgo.User{{ID: "bot-1"}}}})
	if len(got) != 1 {
		t.Fatalf("expected mentioned guild message processed, got %#v", got)
	}
	if got[0].Text != "please help" {
		t.Fatalf("expected mention stripped, got %q", got[0].Text)
	}
}

func TestDiscordHandleMessageCreatePreservesMentionOnlyAttachmentPrompt(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, DiscordSettingBotUserID, "bot-1"); err != nil {
		t.Fatalf("set bot id: %v", err)
	}
	var got []discordIncomingMessage
	svc.processIncomingMessageFn = func(msg discordIncomingMessage) { got = append(got, msg) }

	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "m-attach",
		ChannelID: "c1",
		GuildID:   "g1",
		Content:   "<@bot-1>",
		Author:    &discordgo.User{ID: "u1"},
		Mentions:  []*discordgo.User{{ID: "bot-1"}},
		Attachments: []*discordgo.MessageAttachment{{
			ID:          "att-1",
			Filename:    "screenshot.png",
			ContentType: "image/png",
			Size:        len(slackTestPNGBytes),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/screenshot.png",
		}},
	}})

	if len(got) != 1 {
		t.Fatalf("expected mention-only attachment message processed, got %#v", got)
	}
	if got[0].Text != "User sent attachment(s): screenshot.png" {
		t.Fatalf("unexpected attachment prompt: %q", got[0].Text)
	}
	if len(got[0].Attachments) != 1 || got[0].Attachments[0].FileName != "screenshot.png" {
		t.Fatalf("expected attachment metadata preserved, got %#v", got[0].Attachments)
	}
}

func TestDiscordHandleMessageCreateIgnoresUnmentionedGuildMessageEvenWithLegacyFreeResponseSetting(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, DiscordSettingBotUserID, "bot-1"); err != nil {
		t.Fatalf("set bot id: %v", err)
	}
	if err := settingsRepo.Set(ctx, "discord_free_response_channels", "c-free, c-other"); err != nil {
		t.Fatalf("set legacy free channels: %v", err)
	}
	var got []discordIncomingMessage
	svc.processIncomingMessageFn = func(msg discordIncomingMessage) { got = append(got, msg) }

	svc.handleMessageCreate(ctx, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", ChannelID: "c-free", GuildID: "g1", Content: "no mention", Author: &discordgo.User{ID: "u1"}}})
	if len(got) != 0 {
		t.Fatalf("expected unmentioned guild message ignored despite legacy free-response setting, got %#v", got)
	}
}

func TestDiscordSendToTaskUsesConfiguredChannelTaskRunner(t *testing.T) {
	svc, db, _, projectRepo, taskRepo, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	svc.llmConfigRepo = llmConfigRepo
	svc.execRepo = execRepo

	project := &models.Project{Name: "Discord Runner"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "Test Agent", Provider: models.ProviderAnthropic, Model: "claude-test", AuthMethod: models.AuthMethodCLI, IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create model config: %v", err)
	}
	task := models.Task{ProjectID: project.ID, Title: "Discord Followup", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "start", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	var gotReq ChannelTaskRunRequest
	var called int
	svc.SetChannelTaskRunner(func(_ context.Context, req ChannelTaskRunRequest) {
		called++
		gotReq = req
	})

	handlers := svc.discordActionHandlers(project.ID, discordActionContext{ChannelID: "chan-1", ThreadID: "thread-1", MessageID: "msg-1", UserID: "user-1"}, nil)
	result, err := handlers["send_to_task"](ctx, []byte(`{"task_id":"`+task.ID+`","message":"continue from discord"}`))
	if err != nil {
		t.Fatalf("send_to_task: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected channel task runner called once, got %d; result=%q", called, result)
	}
	if !strings.Contains(result, "started processing") {
		t.Fatalf("expected started response, got %q", result)
	}
	if gotReq.TaskID != task.ID || gotReq.ProjectID != project.ID || gotReq.Message != "continue from discord" || gotReq.Surface != chatcontrol.SurfaceDiscord {
		t.Fatalf("unexpected runner request: %#v", gotReq)
	}
	if gotReq.ReplyContext.Source != models.TaskOriginDiscord || gotReq.ReplyContext.DiscordChannelID != "chan-1" || gotReq.ReplyContext.DiscordThreadID != "thread-1" || gotReq.ReplyContext.DiscordMessageID != "msg-1" || gotReq.ReplyContext.DiscordUserID != "user-1" {
		t.Fatalf("unexpected reply context: %#v", gotReq.ReplyContext)
	}
}

func TestDiscordFailedSwitchProjectPreservesPriorCacheAndInboundRouting(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()

	defaultProject := &models.Project{Name: "Alpha", IsDefault: true}
	targetProject := &models.Project{Name: "Beta"}
	if err := projectRepo.Create(ctx, defaultProject); err != nil {
		t.Fatalf("create default project: %v", err)
	}
	if err := projectRepo.Create(ctx, targetProject); err != nil {
		t.Fatalf("create target project: %v", err)
	}
	userID := "user-failed-switch"
	userProjectRepo := repository.NewDiscordUserProjectRepo(db)
	svc.SetDiscordUserProjectRepo(userProjectRepo)
	if err := userProjectRepo.SetUserProject(ctx, userID, defaultProject.ID); err != nil {
		t.Fatalf("persist default project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: defaultProject.ID, DiscordUserID: userID, DisplayName: "Discord User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize default project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: targetProject.ID, DiscordUserID: userID, DisplayName: "Discord User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize target project: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, repository.NewAttachmentRepo(db))
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	// Load the durable selection into the live cache before forcing a failed switch.
	if got := svc.getActiveProject(ctx, userID); got != defaultProject.ID {
		t.Fatalf("initial active project = %q, want %q", got, defaultProject.ID)
	}

	failedCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.setActiveProject(failedCtx, userID, targetProject.ID); err == nil {
		t.Fatal("expected failed switch persistence error")
	}
	if got := svc.userProjects[userID]; got != defaultProject.ID {
		t.Fatalf("failed switch changed cached project to %q, want %q", got, defaultProject.ID)
	}
	if got, err := userProjectRepo.GetUserProject(ctx, userID); err != nil || got != defaultProject.ID {
		t.Fatalf("failed switch changed durable project: got=%q err=%v want=%q", got, err, defaultProject.ID)
	}

	var incoming ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { incoming = req })
	svc.processIncomingMessage(discordIncomingMessage{ChannelID: "chan-1", MessageID: "msg-2", UserID: userID, Username: "Discord User", Text: "continue", Source: "discord"})
	if incoming.ProjectID != defaultProject.ID {
		t.Fatalf("inbound message used project %q after failed switch, want %q", incoming.ProjectID, defaultProject.ID)
	}

	if err := svc.setActiveProject(ctx, userID, targetProject.ID); err != nil {
		t.Fatalf("successful retry: %v", err)
	}
	if got := svc.userProjects[userID]; got != targetProject.ID {
		t.Fatalf("successful retry cached project = %q, want %q", got, targetProject.ID)
	}
	if got, err := userProjectRepo.GetUserProject(ctx, userID); err != nil || got != targetProject.ID {
		t.Fatalf("successful retry durable project: got=%q err=%v want=%q", got, err, targetProject.ID)
	}
}

func TestDiscordSetActiveProjectFailureDoesNotCreateCacheEntry(t *testing.T) {
	svc, db, _, projectRepo, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Project", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc.SetDiscordUserProjectRepo(repository.NewDiscordUserProjectRepo(db))

	failedCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.setActiveProject(failedCtx, "uncached-user", project.ID); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, ok := svc.userProjects["uncached-user"]; ok {
		t.Fatal("failed switch created a cache entry")
	}
}

func TestDiscordSwitchProjectPersistsForSubsequentMessages(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, discordTaskContextRepo := newDiscordServiceForTest(t)
	ctx := context.Background()
	defaultProject := &models.Project{Name: "Default", IsDefault: true}
	if err := projectRepo.Create(ctx, defaultProject); err != nil {
		t.Fatalf("create default project: %v", err)
	}
	targetProject := &models.Project{Name: "openvibely"}
	if err := projectRepo.Create(ctx, targetProject); err != nil {
		t.Fatalf("create target project: %v", err)
	}
	userProjectRepo := repository.NewDiscordUserProjectRepo(db)
	svc.SetDiscordUserProjectRepo(userProjectRepo)
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: targetProject.ID, DiscordUserID: "1518288288572641398", DisplayName: "James", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, repository.NewAttachmentRepo(db))
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	handlers := svc.discordActionHandlers(defaultProject.ID, discordActionContext{ChannelID: "chan-1", MessageID: "msg-1", UserID: "1518288288572641398"}, nil)
	result, err := handlers["switch_project"](ctx, []byte(`{"project":"openvibely"}`))
	if err != nil {
		t.Fatalf("switch project: %v", err)
	}
	if !strings.Contains(result, "openvibely") {
		t.Fatalf("unexpected switch result: %q", result)
	}
	saved, err := userProjectRepo.GetUserProject(ctx, "1518288288572641398")
	if err != nil {
		t.Fatalf("load saved project: %v", err)
	}
	if saved != targetProject.ID {
		t.Fatalf("saved project = %q, want %q", saved, targetProject.ID)
	}

	fresh := NewDiscordService(settingsRepo, projectRepo, agentRepo, taskRepo, execRepo, scheduleRepo, svc.taskSvc, svc.llmSvc, nil, authRepo, discordTaskContextRepo)
	fresh.SetDiscordUserProjectRepo(userProjectRepo)
	fresh.sendMessageFunc = svc.sendMessageFunc
	var got ChannelChatRunRequest
	fresh.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	fresh.processIncomingMessage(discordIncomingMessage{ChannelID: "chan-1", MessageID: "msg-1", UserID: "1518288288572641398", Username: "James", Text: "what project are you in", Source: "discord"})
	if got.ProjectID != targetProject.ID {
		t.Fatalf("subsequent Discord message used project %q, want %q", got.ProjectID, targetProject.ID)
	}
}

// TestDiscordProcessIncomingMessagePassesChannelRuntimeTools verifies that
// processIncomingMessage populates FirstTurn.RuntimeTools so the channel-specific
// switch_project executor (with persistence) is wired into the ChannelChatRunRequest
// handed to the handler runner. This is the primary regression guard for the
// channel project-switch persistence bug.
func TestDiscordProcessIncomingMessagePassesChannelRuntimeTools(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Runtime Project", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-rt", DisplayName: "RT", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, repository.NewAttachmentRepo(db))
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1", MessageID: "msg-1", UserID: "user-rt",
		Username: "RT", Text: "hello", Source: "discord",
	})

	if got.RuntimeTools == nil {
		t.Fatal("expected RuntimeTools to be non-nil on ChannelChatRunRequest")
	}
	if len(got.RuntimeTools.Definitions) == 0 {
		t.Fatal("expected RuntimeTools.Definitions to be non-empty")
	}
	if got.RuntimeTools.Executor == nil {
		t.Fatal("expected RuntimeTools.Executor to be non-nil")
	}
}

// TestDiscordProcessIncomingMessageSwitchProjectViaRuntimeToolsPersists verifies
// that the switch_project handler carried in ChannelChatRunRequest.RuntimeTools
// (built from processIncomingMessage) actually persists the selection via the
// Discord user-project repo.
func TestDiscordProcessIncomingMessageSwitchProjectViaRuntimeToolsPersists(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, discordTaskContextRepo := newDiscordServiceForTest(t)
	ctx := context.Background()

	project1 := &models.Project{Name: "Alpha", IsDefault: true}
	project2 := &models.Project{Name: "Beta"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("create project2: %v", err)
	}
	userProjectRepo := repository.NewDiscordUserProjectRepo(db)
	svc.SetDiscordUserProjectRepo(userProjectRepo)
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project1.ID, DiscordUserID: "user-rt2", DisplayName: "RT2", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user for project1: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project2.ID, DiscordUserID: "user-rt2", DisplayName: "RT2", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user for project2: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, repository.NewAttachmentRepo(db))
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1", MessageID: "msg-1", UserID: "user-rt2",
		Username: "RT2", Text: "switch project", Source: "discord",
	})

	if got.RuntimeTools == nil {
		t.Fatal("expected RuntimeTools to be non-nil on ChannelChatRunRequest from processIncomingMessage")
	}

	// Call switch_project through the channel RuntimeTools executor.
	result, handled, _, err := got.RuntimeTools.Executor(ctx, "switch_project", []byte(`{"project":"Beta"}`))
	if err != nil {
		t.Fatalf("switch_project executor error: %v", err)
	}
	if !handled {
		t.Fatal("switch_project must be handled by channel executor")
	}
	if !strings.Contains(result, "Beta") {
		t.Fatalf("result must mention the project name: %q", result)
	}
	if !strings.Contains(result, "Future messages") {
		t.Fatalf("result must include same-turn semantics message: %q", result)
	}

	// Verify the selection was persisted to the user-project repo.
	saved, err := userProjectRepo.GetUserProject(ctx, "user-rt2")
	if err != nil {
		t.Fatalf("get saved project: %v", err)
	}
	if saved != project2.ID {
		t.Fatalf("switch_project did not persist selection: got %q, want %q", saved, project2.ID)
	}

	// Suppress unused variable warning for discordTaskContextRepo.
	_ = discordTaskContextRepo
}

func TestDiscordProcessIncomingMessagePassesReplyContextToSharedChatRunner(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, discordTaskContextRepo := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Chat", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-1", DisplayName: "User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, repository.NewAttachmentRepo(db))
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }
	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) {
		got = req
	})

	svc.processIncomingMessage(discordIncomingMessage{ChannelID: "chan-1", ThreadID: "thread-1", MessageID: "msg-1", UserID: "user-1", Username: "User", Text: "hello", Source: "discord"})

	if got.Surface != chatcontrol.SurfaceDiscord {
		t.Fatalf("expected Discord surface, got %q", got.Surface)
	}
	if got.ReplyContext.Source != models.TaskOriginDiscord {
		t.Fatalf("expected Discord reply source, got %#v", got.ReplyContext)
	}
	if got.ReplyContext.DiscordChannelID != "thread-1" || got.ReplyContext.DiscordThreadID != "thread-1" || got.ReplyContext.DiscordMessageID != "msg-1" || got.ReplyContext.DiscordUserID != "user-1" {
		t.Fatalf("unexpected Discord reply context: %#v", got.ReplyContext)
	}
	stored, err := discordTaskContextRepo.GetByTaskID(ctx, got.TaskID)
	if err != nil {
		t.Fatalf("load stored context: %v", err)
	}
	if stored == nil || stored.DiscordChannelID != "thread-1" || stored.DiscordMessageID != "ack-1" {
		t.Fatalf("unexpected stored context: %#v", stored)
	}
	if task, err := taskRepo.GetByID(ctx, got.TaskID); err != nil || task == nil || task.CreatedVia != models.TaskOriginDiscord {
		t.Fatalf("expected Discord chat task, task=%#v err=%v", task, err)
	}
}

func TestDiscordProcessIncomingMessageDownloadsPersistsAndPassesImageAttachment(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Attachment Chat", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-1", DisplayName: "User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	defaultAgent := &models.LLMConfig{Name: "text-cli", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5", IsDefault: true}
	if err := agentRepo.Create(ctx, defaultAgent); err != nil {
		t.Fatalf("create default agent: %v", err)
	}
	visionAgent := &models.LLMConfig{Name: "vision", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-3-5-sonnet-20241022", APIKey: "key"}
	if err := agentRepo.Create(ctx, visionAgent); err != nil {
		t.Fatalf("create vision agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, attachmentRepo, nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	uploadRoot := t.TempDir()
	svc.SetUploadsDir(uploadRoot)
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })
	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Username:  "User",
		Text:      "what is in this screenshot?",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-1",
			FileName:    "screenshot.png",
			ContentType: "image/png",
			Size:        len(slackTestPNGBytes),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/screenshot.png",
		}},
	})

	if got.ExecID == "" {
		t.Fatal("expected channel chat runner to be called")
	}
	if len(got.ImageAttachments) != 1 {
		t.Fatalf("expected one image attachment, got %#v", got.ImageAttachments)
	}
	if got.ImageAttachments[0].FileName != "screenshot.png" || got.ImageAttachments[0].MediaType != "image/png" {
		t.Fatalf("unexpected image attachment: %#v", got.ImageAttachments[0])
	}
	if got.Agent.ID != visionAgent.ID {
		t.Fatalf("expected runner to use vision agent %q, got %q", visionAgent.ID, got.Agent.ID)
	}
	savedImage, err := os.ReadFile(got.ImageAttachments[0].FilePath)
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	if string(savedImage) != string(slackTestPNGBytes) {
		t.Fatalf("saved image mismatch")
	}
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, got.ExecID)
	if err != nil {
		t.Fatalf("list persisted attachments: %v", err)
	}
	if len(persisted) != 1 || persisted[0].FilePath != got.ImageAttachments[0].FilePath {
		t.Fatalf("unexpected persisted attachments: %#v", persisted)
	}
	persistedExec, err := execRepo.GetByID(ctx, got.ExecID)
	if err != nil {
		t.Fatalf("get persisted execution: %v", err)
	}
	if persistedExec == nil || persistedExec.AgentConfigID != visionAgent.ID {
		t.Fatalf("expected persisted execution to use vision agent %q, got %#v", visionAgent.ID, persistedExec)
	}
}

func TestDiscordProcessIncomingMessageKeepsDuplicateAttachmentFilenamesDistinct(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Duplicate Attachment Chat", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-1", DisplayName: "User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "vision", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, attachmentRepo, nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.SetUploadsDir(t.TempDir())
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) { return "ack-1", nil }

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })
	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Username:  "User",
		Text:      "compare these",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{
			{ID: "att-1", FileName: "screenshot.png", ContentType: "image/png", Size: len(slackTestPNGBytes), URL: "https://cdn.discordapp.com/attachments/chan/msg/one.png"},
			{ID: "att-2", FileName: "screenshot.png", ContentType: "image/png", Size: len(slackTestPNGBytes), URL: "https://cdn.discordapp.com/attachments/chan/msg/two.png"},
		},
	})

	if len(got.ImageAttachments) != 2 {
		t.Fatalf("expected two image attachments, got %#v", got.ImageAttachments)
	}
	if got.ImageAttachments[0].FilePath == got.ImageAttachments[1].FilePath {
		t.Fatalf("expected duplicate filenames to get distinct paths, got %q", got.ImageAttachments[0].FilePath)
	}
	if filepath.Base(got.ImageAttachments[0].FilePath) != "screenshot.png" || filepath.Base(got.ImageAttachments[1].FilePath) != "screenshot-1.png" {
		t.Fatalf("unexpected final filenames: %q %q", filepath.Base(got.ImageAttachments[0].FilePath), filepath.Base(got.ImageAttachments[1].FilePath))
	}
	persisted, err := chatAttachmentRepo.ListByExecution(ctx, got.ExecID)
	if err != nil {
		t.Fatalf("list persisted attachments: %v", err)
	}
	if len(persisted) != 2 || persisted[0].FilePath == persisted[1].FilePath {
		t.Fatalf("unexpected persisted duplicate attachments: %#v", persisted)
	}
}

func TestDiscordProcessIncomingMessageFailsWhenAttachmentLinkFails(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Text Link Failure", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-1", DisplayName: "User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, attachmentRepo, nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetChatAttachmentRepo(repository.NewChatAttachmentRepo(testutil.NewTestDB(t)))
	svc.SetUploadsDir(t.TempDir())
	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("secret text should not leak"))
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	runnerCalled := false
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runnerCalled = true })
	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Username:  "User",
		Text:      "read this",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-text",
			FileName:    "note.txt",
			ContentType: "text/plain",
			Size:        len("secret text should not leak"),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/note.txt",
		}},
	})

	if runnerCalled {
		t.Fatal("expected channel chat runner not to be called when attachment linking fails")
	}
	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "Failed to process attachment") {
		t.Fatalf("expected attachment failure warning, got %#v", sentMessages)
	}
	if strings.Contains(sentMessages[0], "FOREIGN KEY") || strings.Contains(sentMessages[0], "constraint failed") || strings.Contains(sentMessages[0], "note.txt") {
		t.Fatalf("expected generic user-facing attachment warning, got %q", sentMessages[0])
	}
	var status, errorMessage string
	if err := db.QueryRowContext(ctx, `SELECT status, error_message FROM executions ORDER BY started_at DESC, rowid DESC LIMIT 1`).Scan(&status, &errorMessage); err != nil {
		t.Fatalf("get latest execution: %v", err)
	}
	if status != string(models.ExecFailed) || !strings.Contains(errorMessage, "Failed to process attachment") {
		t.Fatalf("expected failed execution for attachment link error, status=%q error=%q", status, errorMessage)
	}
}

func TestDiscordDownloadRejectsBodyLargerThanLimit(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(make([]byte, discordMaxFileSize+1))
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	_, err := svc.downloadDiscordFiles(context.Background(), []discordIncomingAttachment{{
		ID:          "att-big",
		FileName:    "big.txt",
		ContentType: "text/plain",
		Size:        1,
		URL:         "https://cdn.discordapp.com/attachments/chan/msg/big.txt",
	}})
	if err == nil || !strings.Contains(err.Error(), "exceeded max size") {
		t.Fatalf("expected max size error, got %v", err)
	}
}

func TestDiscordProcessIncomingMessageUsesGenericDownloadError(t *testing.T) {
	svc, db, settingsRepo, projectRepo, taskRepo, authRepo, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Download Failure", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "user-1", DisplayName: "User", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize user: %v", err)
	}
	if err := settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"); err != nil {
		t.Fatalf("set responses: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.scheduleRepo = scheduleRepo
	svc.taskSvc = NewTaskService(taskRepo, attachmentRepo, nil)
	svc.llmSvc = NewLLMService(agentRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.SetUploadsDir(t.TempDir())
	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Username:  "User",
		Text:      "read this",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-bad",
			FileName:    "bad.png",
			ContentType: "image/png",
			Size:        1,
			URL:         "https://example.com/attachments/bad.png",
		}},
	})

	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "unable to download attachment") {
		t.Fatalf("expected generic download failure warning, got %#v", sentMessages)
	}
	if strings.Contains(sentMessages[0], "example.com") || strings.Contains(sentMessages[0], "not trusted") || strings.Contains(sentMessages[0], "bad.png") {
		t.Fatalf("expected generic user-facing download warning, got %q", sentMessages[0])
	}
}

func TestDiscordLinkAttachmentsReturnsOnlySuccessfullyPersistedAttachments(t *testing.T) {
	svc, db, _, projectRepo, taskRepo, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	svc.SetChatAttachmentRepo(repository.NewChatAttachmentRepo(db))
	uploadRoot := t.TempDir()
	svc.SetUploadsDir(uploadRoot)
	project := &models.Project{Name: "Discord Link Attachments"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Discord Link", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "attach", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "attach"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	tmpDir := t.TempDir()
	goodPath := filepath.Join(tmpDir, "ok.png")
	if err := os.WriteFile(goodPath, slackTestPNGBytes, 0644); err != nil {
		t.Fatalf("write good attachment: %v", err)
	}
	missingPath := filepath.Join(tmpDir, "missing.png")

	linked, err := svc.linkAttachmentsToExecution(ctx, exec.ID, []models.ChatAttachment{
		{FileName: "ok.png", FilePath: goodPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
		{FileName: "missing.png", FilePath: missingPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
	})

	if err == nil || !strings.Contains(err.Error(), "storing Discord attachment failed") {
		t.Fatalf("expected partial link error, got %v", err)
	}
	if len(linked) != 0 {
		t.Fatalf("expected partial links to be rolled back, got %#v", linked)
	}
	persisted, err := svc.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(persisted) != 0 {
		t.Fatalf("expected no persisted attachments after rollback, got %#v", persisted)
	}
	movedPath := filepath.Join(svc.uploadsDir, "chat", exec.ID, "ok.png")
	if _, err := os.Stat(movedPath); !os.IsNotExist(err) {
		t.Fatalf("expected moved file cleanup after rollback, stat err=%v", err)
	}
}

func TestDiscordLinkAttachmentsFailsWhenAttachmentRepoMissing(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	svc.SetUploadsDir(t.TempDir())

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "orphan.png")
	if err := os.WriteFile(srcPath, slackTestPNGBytes, 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	linked, err := svc.linkAttachmentsToExecution(ctx, "exec-1", []models.ChatAttachment{{
		FileName:  "orphan.png",
		FilePath:  srcPath,
		MediaType: "image/png",
		FileSize:  int64(len(slackTestPNGBytes)),
	}})

	if err == nil || !strings.Contains(err.Error(), "repository is unavailable") {
		t.Fatalf("expected missing repo error, got linked=%#v err=%v", linked, err)
	}
	if len(linked) != 0 {
		t.Fatalf("expected no linked attachments, got %#v", linked)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("expected source temp dir cleanup after missing repo error, stat err=%v", err)
	}
}

func TestDiscordLinkAttachmentsCleansUpSourceWhenExecDirCreateFails(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	svc.SetChatAttachmentRepo(repository.NewChatAttachmentRepo(testutil.NewTestDB(t)))
	uploadRootFile := filepath.Join(t.TempDir(), "uploads-file")
	if err := os.WriteFile(uploadRootFile, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write upload root file: %v", err)
	}
	svc.SetUploadsDir(uploadRootFile)

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "orphan.png")
	if err := os.WriteFile(srcPath, slackTestPNGBytes, 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	linked, err := svc.linkAttachmentsToExecution(ctx, "exec-1", []models.ChatAttachment{{
		FileName:  "orphan.png",
		FilePath:  srcPath,
		MediaType: "image/png",
		FileSize:  int64(len(slackTestPNGBytes)),
	}})

	if err == nil || !strings.Contains(err.Error(), "storing Discord attachment") {
		t.Fatalf("expected exec dir create error, got linked=%#v err=%v", linked, err)
	}
	if len(linked) != 0 {
		t.Fatalf("expected no linked attachments, got %#v", linked)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("expected source temp dir cleanup after exec dir create error, stat err=%v", err)
	}
}

func TestDiscordLinkAttachmentsCleansUpMovedFileWhenPersistFails(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	svc.SetChatAttachmentRepo(repository.NewChatAttachmentRepo(testutil.NewTestDB(t)))
	uploadRoot := t.TempDir()
	svc.SetUploadsDir(uploadRoot)

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "orphan.png")
	if err := os.WriteFile(srcPath, slackTestPNGBytes, 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	linked, err := svc.linkAttachmentsToExecution(ctx, "missing-exec", []models.ChatAttachment{{
		FileName:  "orphan.png",
		FilePath:  srcPath,
		MediaType: "image/png",
		FileSize:  int64(len(slackTestPNGBytes)),
	}})

	if err == nil || !strings.Contains(err.Error(), "storing Discord attachment failed") {
		t.Fatalf("expected persist error, got %v", err)
	}
	if len(linked) != 0 {
		t.Fatalf("expected no linked attachments, got %#v", linked)
	}
	destPath := filepath.Join(uploadRoot, "chat", "missing-exec", "orphan.png")
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Fatalf("expected moved file to be removed after persist failure, stat err=%v", err)
	}
}

func runDiscordQueueChatIngressForTest(ctx context.Context, svc *DiscordService, projectID, activeExecID string, msg discordIncomingMessage) bool {
	return runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:        "discord",
		ProjectID:       projectID,
		Message:         msg.Text,
		Source:          msg.Source,
		Surface:         chatcontrol.SurfaceDiscord,
		HasAttachments:  len(msg.Attachments) > 0,
		ThreadInputRepo: svc.threadInputRepo,
		LLMConfigRepo:   svc.llmConfigRepo,
		ChatBroadcaster: svc.chatBroadcaster,
		UploadsDir:      svc.uploadsDir,
		DownloadAttachments: func(ctx context.Context) (channelChatIngressDownloadResult, error) {
			if len(msg.Attachments) == 0 {
				return channelChatIngressDownloadResult{}, nil
			}
			chatAtts, err := svc.downloadDiscordFiles(ctx, msg.Attachments)
			if err != nil {
				return channelChatIngressDownloadResult{}, err
			}
			attCtx, imgAtts := discordAttachmentContextAndImages(chatAtts)
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAtts}, nil
		},
		IncomingAttachmentsNeedVision: func() bool { return discordIncomingAttachmentsRequireVision(msg.Attachments) },
		AttachmentDownloadFailureMessage: func(error, bool) string {
			return "Failed to process attachment: unable to download attachment. Please try again."
		},
		SavePendingAttachments: svc.saveChatAttachmentsToPendingSession,
		FindActiveExecution: func(context.Context, string) (*models.Execution, error) {
			return &models.Execution{ID: activeExecID}, nil
		},
		RecordAttachmentFailure: func(ctx context.Context, agentID, msgText string) {
			svc.recordQueuedAttachmentFailure(ctx, projectID, agentID, msg, msgText)
		},
		NewQueuedInput: func() *models.ThreadInput {
			return &models.ThreadInput{DiscordChannelID: msg.replyChannelID(), DiscordThreadID: msg.ThreadID, DiscordMessageID: msg.MessageID, DiscordUserID: msg.UserID}
		},
		OnQueuedAttachmentDownloadFailed: func(_ context.Context, msgText string) {
			_ = svc.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "⚠️ "+msgText)
		},
		OnAttachmentStoreFailed: func(_ context.Context, msgText string) {
			_ = svc.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "⚠️ "+msgText)
		},
		OnModelSelectionFailed: func(_ context.Context, err error) {
			_ = svc.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, fmt.Sprintf("Error selecting model: %v", err))
		},
		OnQueueFailure: func(context.Context) {
			_ = svc.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Error queueing your message. Please try again.")
		},
		OnQueued: func(context.Context) {
			_ = svc.sendDiscordMessage(msg.replyChannelID(), msg.MessageID, "Queued. I'll send this after the current response finishes.")
		},
	})
}

func TestDiscordQueueChatInputSelectsVisionAgentFromDownloadedAttachment(t *testing.T) {
	svc, db, _, projectRepo, taskRepo, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Queued Attachment Chat", IsDefault: true}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	defaultAgent := &models.LLMConfig{Name: "text-cli", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5", IsDefault: true}
	if err := agentRepo.Create(ctx, defaultAgent); err != nil {
		t.Fatalf("create default agent: %v", err)
	}
	visionAgent := &models.LLMConfig{Name: "vision", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-3-5-sonnet-20241022", APIKey: "key"}
	if err := agentRepo.Create(ctx, visionAgent); err != nil {
		t.Fatalf("create vision agent: %v", err)
	}
	svc.llmConfigRepo = agentRepo
	svc.SetUploadsDir(t.TempDir())
	threadInputRepo := repository.NewThreadInputRepo(db)
	svc.SetThreadInputRepo(threadInputRepo)

	activeTask := &models.Task{ProjectID: project.ID, Title: "Active chat", Prompt: "active", Status: models.StatusRunning, Category: models.CategoryChat, AgentID: &defaultAgent.ID, CreatedVia: models.TaskOriginDiscord}
	if err := taskRepo.Create(ctx, activeTask); err != nil {
		t.Fatalf("create active chat task: %v", err)
	}
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: defaultAgent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := repository.NewExecutionRepo(db).Create(ctx, activeExec); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	handled := runDiscordQueueChatIngressForTest(ctx, svc, project.ID, activeExec.ID, discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Text:      "queue this screenshot",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-1",
			FileName:    "queued.bin",
			ContentType: "application/octet-stream",
			Size:        len(slackTestPNGBytes),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/queued.bin",
		}},
	})
	if !handled {
		t.Fatal("expected queued input to be handled")
	}
	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "Queued") {
		t.Fatalf("expected queued acknowledgement, got %#v", sentMessages)
	}
	pending, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	if err != nil {
		t.Fatalf("list pending chat inputs: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one queued chat input, got %#v", pending)
	}
	if pending[0].AgentConfigID != visionAgent.ID {
		t.Fatalf("expected queued input to use vision agent %q, got %#v", visionAgent.ID, pending[0])
	}
	if pending[0].AttachmentSessionID == "" {
		t.Fatalf("expected queued input to reference pending attachment session: %#v", pending[0])
	}
	pendingDir := filepath.Join(svc.uploadsDir, "chat", "pending", pending[0].AttachmentSessionID)
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		t.Fatalf("read pending attachment dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one staged attachment, got %d", len(entries))
	}
}

func TestDiscordQueueChatInputCleansPendingAttachmentsWhenQueueInsertFails(t *testing.T) {
	svc, db, _, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	svc.llmConfigRepo = agentRepo
	uploadRoot := t.TempDir()
	svc.SetUploadsDir(uploadRoot)
	queueDB := testutil.NewTestDB(t)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(queueDB))
	if err := queueDB.Close(); err != nil {
		t.Fatalf("close queue db: %v", err)
	}

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	handled := runDiscordQueueChatIngressForTest(context.Background(), svc, "project-1", "exec-1", discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Text:      "queue this",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-1",
			FileName:    "queued.png",
			ContentType: "image/png",
			Size:        len(slackTestPNGBytes),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/queued.png",
		}},
	})

	if !handled {
		t.Fatal("expected queue input failure to be handled")
	}
	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "Error queueing your message") {
		t.Fatalf("expected queue failure warning, got %#v", sentMessages)
	}
	pendingRoot := filepath.Join(uploadRoot, "chat", "pending")
	entries, err := os.ReadDir(pendingRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read pending root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected staged pending attachments to be cleaned up after queue insert failure, got %d entries", len(entries))
	}
}

func TestDiscordQueueChatInputUsesGenericDownloadError(t *testing.T) {
	svc, db, _, projectRepo, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Queued Download Failure"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe chat broadcaster: %v", err)
	}
	defer svc.chatBroadcaster.Unsubscribe(sub)
	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	handled := runDiscordQueueChatIngressForTest(ctx, svc, project.ID, "exec-1", discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Text:      "queue this",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-bad",
			FileName:    "bad.png",
			ContentType: "image/png",
			Size:        1,
			URL:         "https://example.com/attachments/bad.png",
		}},
	})

	if !handled {
		t.Fatal("expected queue download failure to be handled")
	}
	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "unable to download attachment") {
		t.Fatalf("expected generic queued download warning, got %#v", sentMessages)
	}
	if strings.Contains(sentMessages[0], "example.com") || strings.Contains(sentMessages[0], "not trusted") || strings.Contains(sentMessages[0], "bad.png") {
		t.Fatalf("expected generic user-facing queued download warning, got %q", sentMessages[0])
	}
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued download failure chat event")
	}
	if evt.Type != events.ChatNewMessage || evt.ProjectID != project.ID || evt.ExecID == "" || evt.Queued || !evt.HasAttachments {
		t.Fatalf("expected persisted failed chat event with attachments, got %#v", evt)
	}
	exec, err := execRepo.GetByID(ctx, evt.ExecID)
	if err != nil {
		t.Fatalf("get failed execution: %v", err)
	}
	if exec == nil || exec.Status != models.ExecFailed || !strings.Contains(exec.ErrorMessage, "unable to download attachment") {
		t.Fatalf("expected failed queued attachment execution, got %#v", exec)
	}
}

func TestDiscordQueueChatInputRecordsFailedMessageWhenAttachmentStagingFails(t *testing.T) {
	svc, db, _, projectRepo, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Queued Staging Failure"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-4o", APIKey: "key", IsDefault: true}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	execRepo := repository.NewExecutionRepo(db)
	svc.llmConfigRepo = agentRepo
	svc.execRepo = execRepo
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	uploadRootFile := filepath.Join(t.TempDir(), "uploads-file")
	if err := os.WriteFile(uploadRootFile, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write upload root file: %v", err)
	}
	svc.SetUploadsDir(uploadRootFile)
	svc.SetChatBroadcaster(events.NewChatBroadcaster())
	sub, err := svc.chatBroadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe chat broadcaster: %v", err)
	}
	defer svc.chatBroadcaster.Unsubscribe(sub)

	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(slackTestPNGBytes)
	}))
	defer fileServer.Close()
	useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": fileServer})

	var sentMessages []string
	svc.sendMessageFunc = func(channelID, messageID, text string) (string, error) {
		sentMessages = append(sentMessages, text)
		return "ack-1", nil
	}

	handled := runDiscordQueueChatIngressForTest(ctx, svc, project.ID, "exec-1", discordIncomingMessage{
		ChannelID: "chan-1",
		MessageID: "msg-1",
		UserID:    "user-1",
		Text:      "queue this",
		Source:    "discord",
		Attachments: []discordIncomingAttachment{{
			ID:          "att-1",
			FileName:    "queued.png",
			ContentType: "image/png",
			Size:        len(slackTestPNGBytes),
			URL:         "https://cdn.discordapp.com/attachments/chan/msg/queued.png",
		}},
	})

	if !handled {
		t.Fatal("expected queued staging failure to be handled")
	}
	if len(sentMessages) != 1 || !strings.Contains(sentMessages[0], "unable to store attachment") {
		t.Fatalf("expected generic queued staging warning, got %#v", sentMessages)
	}
	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued staging failure chat event")
	}
	if evt.Type != events.ChatNewMessage || evt.ProjectID != project.ID || evt.ExecID == "" || evt.Queued || !evt.HasAttachments {
		t.Fatalf("expected persisted failed chat event with attachments, got %#v", evt)
	}
	exec, err := execRepo.GetByID(ctx, evt.ExecID)
	if err != nil {
		t.Fatalf("get failed execution: %v", err)
	}
	if exec == nil || exec.Status != models.ExecFailed || !strings.Contains(exec.ErrorMessage, "unable to store attachment") {
		t.Fatalf("expected failed queued attachment execution, got %#v", exec)
	}
}

func TestDiscordSaveChatAttachmentsToPendingSessionCleansUpSourceOnStageFailure(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	uploadRoot := t.TempDir()
	svc.SetUploadsDir(uploadRoot)

	tmpDir := t.TempDir()
	okPath := filepath.Join(tmpDir, "ok.png")
	if err := os.WriteFile(okPath, slackTestPNGBytes, 0644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	missingPath := filepath.Join(tmpDir, "missing.png")

	sessionID, err := svc.saveChatAttachmentsToPendingSession([]models.ChatAttachment{
		{FileName: "ok.png", FilePath: okPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
		{FileName: "missing.png", FilePath: missingPath, MediaType: "image/png", FileSize: int64(len(slackTestPNGBytes))},
	})

	if err == nil || sessionID != "" {
		t.Fatalf("expected staging error with no session, session=%q err=%v", sessionID, err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("expected source temp dir cleanup after staging failure, stat err=%v", err)
	}
	pendingRoot := filepath.Join(uploadRoot, "chat", "pending")
	entries, err := os.ReadDir(pendingRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read pending root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no pending session dirs after staging failure, got %d", len(entries))
	}
}

func TestDiscordDownloadFollowsTrustedRedirect(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Hostname() {
		case "cdn.discordapp.com":
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://media.discordapp.net/attachments/file.txt"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		case "media.discordapp.net":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("redirected")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected host requested: %s", req.URL.Host)
			return nil, nil
		}
	})}

	var got bytes.Buffer
	if err := svc.downloadDiscordFile(context.Background(), "https://cdn.discordapp.com/attachments/file.txt", &got); err != nil {
		t.Fatalf("download with trusted redirect failed: %v", err)
	}
	if got.String() != "redirected" {
		t.Fatalf("unexpected redirected body: %q", got.String())
	}
}

func TestDiscordDownloadRejectsHTTPInitialURL(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	var got bytes.Buffer
	err := svc.downloadDiscordFile(context.Background(), "http://cdn.discordapp.com/attachments/file.txt", &got)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestDiscordDownloadRejectsHTTPRedirectToTrustedHost(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "cdn.discordapp.com" {
			t.Fatalf("http redirect target should not be requested: %s", req.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://media.discordapp.net/attachments/file.txt"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}

	var got bytes.Buffer
	err := svc.downloadDiscordFile(context.Background(), "https://cdn.discordapp.com/attachments/file.txt", &got)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestDiscordDownloadRejectsUntrustedRedirect(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "cdn.discordapp.com" {
			t.Fatalf("untrusted redirect target should not be requested: %s", req.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://example.com/file.txt"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}

	var got bytes.Buffer
	err := svc.downloadDiscordFile(context.Background(), "https://cdn.discordapp.com/attachments/file.txt", &got)
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("expected untrusted redirect error, got %v", err)
	}
}

func TestDiscordDownloadRejectsSuffixMatchingDiscordHost(t *testing.T) {
	svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
	var got bytes.Buffer
	err := svc.downloadDiscordFile(context.Background(), "https://example.discordapp.com/attachments/file.txt", &got)
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("expected untrusted suffix host error, got %v", err)
	}
}

func useDiscordFileServers(t *testing.T, svc *DiscordService, servers map[string]*httptest.Server) {
	t.Helper()
	serverURLs := make(map[string]*url.URL, len(servers))
	for host, server := range servers {
		serverURL, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse test server URL: %v", err)
		}
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

func TestDiscordRequiresMentionForGuildThreadsEvenWithLegacyFreeResponseSetting(t *testing.T) {
	svc, _, settingsRepo, _, _, _, _ := newDiscordServiceForTest(t)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, "discord_free_response_channels", "parent-1"); err != nil {
		t.Fatalf("set legacy free channels: %v", err)
	}

	if !svc.requiresMentionForMessage(ctx, discordIncomingMessage{ChannelID: "thread-1", ParentChannelID: "parent-1"}) {
		t.Fatalf("expected guild thread to require a mention despite legacy free-response setting")
	}
}

func TestDiscordSendTaskCompletionNotificationRoutesToContext(t *testing.T) {
	svc, _, _, projectRepo, taskRepo, _, discordTaskContextRepo := newDiscordServiceForTest(t)
	ctx := context.Background()
	project := &models.Project{Name: "Discord Completion"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := models.Task{ProjectID: project.ID, Title: "Discord Task", Category: models.CategoryActive, Status: models.StatusCompleted, CreatedVia: models.TaskOriginDiscord, Prompt: "do it"}
	if err := taskRepo.Create(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := discordTaskContextRepo.Upsert(ctx, &models.DiscordTaskContext{TaskID: task.ID, DiscordChannelID: "chan-1", DiscordThreadID: "thread-1", DiscordMessageID: "msg-1", DiscordUserID: "user-1"}); err != nil {
		t.Fatalf("upsert context: %v", err)
	}
	var channelID, messageID, text string
	svc.sendMessageFunc = func(ch, msg, body string) (string, error) {
		channelID, messageID, text = ch, msg, body
		return "reply-1", nil
	}

	svc.SendTaskCompletionNotification(ctx, task, "finished", "")
	time.Sleep(10 * time.Millisecond)

	if channelID != "chan-1" || messageID != "msg-1" || !strings.Contains(text, "Discord Task") || !strings.Contains(text, "finished") {
		t.Fatalf("completion routed incorrectly channel=%q message=%q text=%q", channelID, messageID, text)
	}
}
