package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/stretchr/testify/require"
)

type testUsageAnalyticsToolResponse struct {
	OK                bool                       `json:"ok"`
	ProjectID         string                     `json:"project_id"`
	Totals            models.UsageTotals         `json:"totals"`
	TopModels         []models.ModelUsagePoint   `json:"top_models"`
	TopProviders      []testUsageProviderSummary `json:"top_providers"`
	AccountLimits     []testUsageAccountSummary  `json:"account_limits"`
	LastUpdatedAt     *string                    `json:"last_updated_at"`
	LocalOnly         bool                       `json:"local_only"`
	ProviderRefreshed bool                       `json:"provider_refreshed"`
	CostAvailability  string                     `json:"cost_availability"`
}

type testUsageProviderSummary struct {
	Provider    string `json:"provider"`
	TotalTokens int    `json:"total_tokens"`
}

type testUsageAccountSummary struct {
	Provider string                    `json:"provider"`
	PlanType string                    `json:"plan_type"`
	Limits   []models.AccountLimitView `json:"limits"`
}

type countingUpdateInstaller struct {
	stageCalls    int
	applyCalls    int
	validateCalls int
	rollbackCalls int
}

func (i *countingUpdateInstaller) Stage(context.Context, update.VerifiedRelease) (any, error) {
	i.stageCalls++
	return struct{}{}, nil
}

func (i *countingUpdateInstaller) Apply(context.Context, any) error {
	i.applyCalls++
	return nil
}

func (i *countingUpdateInstaller) Validate(context.Context, update.ReleaseMetadata) error {
	i.validateCalls++
	return nil
}

func (i *countingUpdateInstaller) Rollback(context.Context, any) error {
	i.rollbackCalls++
	return nil
}

func TestSupportsChatActionTools(t *testing.T) {
	tests := []struct {
		name  string
		agent models.LLMConfig
		want  bool
	}{
		{
			name: "openai oauth supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodOAuth,
			},
			want: true,
		},
		{
			name: "openai cli does not support runtime action tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodCLI,
			},
			want: false,
		},
		{
			name: "anthropic api key supports tools",
			agent: models.LLMConfig{
				Provider:   models.ProviderAnthropic,
				AuthMethod: models.AuthMethodAPIKey,
			},
			want: true,
		},
		{
			name: "ollama does not support runtime action tools",
			agent: models.LLMConfig{
				Provider: models.ProviderOllama,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsChatActionTools(tt.agent); got != tt.want {
				t.Fatalf("supportsChatActionTools()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestHandlerSupportsChatActionToolsResolvesMixtureAggregator(t *testing.T) {
	h, _, repo := setupTestHandler(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		provider   models.LLMProvider
		authMethod models.AuthMethod
		want       bool
	}{
		{name: "openai api key", provider: models.ProviderOpenAI, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "openai oauth", provider: models.ProviderOpenAI, authMethod: models.AuthMethodOAuth, want: true},
		{name: "anthropic api key", provider: models.ProviderAnthropic, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "anthropic oauth", provider: models.ProviderAnthropic, authMethod: models.AuthMethodOAuth, want: true},
		{name: "openai compatible api key", provider: models.ProviderOpenAICompatible, authMethod: models.AuthMethodAPIKey, want: true},
		{name: "openai compatible oauth", provider: models.ProviderOpenAICompatible, authMethod: models.AuthMethodOAuth, want: true},
		{name: "openai cli", provider: models.ProviderOpenAI, authMethod: models.AuthMethodCLI, want: false},
		{name: "anthropic cli", provider: models.ProviderAnthropic, authMethod: models.AuthMethodCLI, want: false},
		{name: "ollama", provider: models.ProviderOllama, authMethod: models.AuthMethodAPIKey, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggregator := &models.LLMConfig{Name: "Aggregator " + tt.name, Provider: tt.provider, Model: "model", AuthMethod: tt.authMethod}
			require.NoError(t, repo.Create(ctx, aggregator))
			mixture := models.LLMConfig{
				Provider:          models.ProviderMixture,
				MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
			}
			require.Equal(t, tt.want, h.supportsChatActionTools(ctx, mixture))
		})
	}

	require.False(t, h.supportsChatActionTools(ctx, models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{invalid`}))
	require.False(t, h.supportsChatActionTools(ctx, models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"missing"}}`}))
}

func TestUpdateProjectSettingsCapabilityAndInheritedDefaultModel(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Chat Project Settings"}
	require.NoError(t, h.projectRepo.Create(ctx, project))
	model := &models.LLMConfig{Name: "Chat Project Model", Provider: models.ProviderTest, Model: "test-project-default"}
	require.NoError(t, llmConfigRepo.Create(ctx, model))

	orchestrateRT := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true),
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	capabilities, handled, isErr, err := orchestrateRT.Executor(ctx, "list_capabilities", nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, capabilities)
	require.Contains(t, capabilities, "update_project_settings")
	require.Contains(t, capabilities, "decide_alert")
	require.Contains(t, capabilities, "cancel_task")

	planRT := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	planCapabilities, handled, isErr, err := planRT.Executor(ctx, "list_capabilities", nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, planCapabilities)
	require.NotContains(t, planCapabilities, "update_project_settings")
	require.NotContains(t, planCapabilities, "decide_alert")
	require.NotContains(t, planCapabilities, "cancel_task")
	cancelOut, handled, isErr, err := planRT.Executor(ctx, "cancel_task", json.RawMessage(`{"task_id":"task-1"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, cancelOut, "not available in plan mode")
	blockedOut, handled, isErr, err := planRT.Executor(ctx, "update_project_settings", json.RawMessage(`{"max_workers":2}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, blockedOut, "not available in plan mode")

	out, handled, isErr, err := orchestrateRT.Executor(ctx, "update_project_settings", json.RawMessage(`{"default_model":"chat project model","max_workers":2}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, out)
	var resp struct {
		OK           bool `json:"ok"`
		DefaultModel struct {
			Set     bool   `json:"set"`
			ModelID string `json:"model_id"`
		} `json:"default_model"`
		WorkerLimit struct {
			Set        bool `json:"set"`
			MaxWorkers int  `json:"max_workers"`
		} `json:"worker_limit"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.True(t, resp.DefaultModel.Set)
	require.Equal(t, model.ID, resp.DefaultModel.ModelID)
	require.True(t, resp.WorkerLimit.Set)
	require.Equal(t, 2, resp.WorkerLimit.MaxWorkers)

	storedProject, err := h.projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.NotNil(t, storedProject.DefaultAgentConfigID)
	require.Equal(t, model.ID, *storedProject.DefaultAgentConfigID)
	require.NotNil(t, storedProject.MaxWorkers)
	require.Equal(t, 2, *storedProject.MaxWorkers)

	task := &models.Task{ProjectID: project.ID, Title: "Inherit Project Default", Prompt: "run", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, h.taskRepo.Create(ctx, task))
	resolved, unstartable, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.False(t, unstartable)
	require.NotNil(t, resolved)
	require.Equal(t, model.ID, resolved.ID)

	_, err = db.ExecContext(ctx, `SELECT 1`)
	require.NoError(t, err)
}

func TestSaveCustomPersonalityRuntimeToolCreatesListsAndSelects(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	customRepo := repository.NewCustomPersonalityRepo(db)
	h.SetCustomPersonalityRepo(customRepo)
	project := createProject(t, h, "Chat Personality Project")

	orchestrateRT := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true),
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := orchestrateRT.Executor(ctx, "save_custom_personality", json.RawMessage(`{"mode":"create","name":"Terse Code Review","description":"Brief code review tone","system_prompt":"You are terse in code review while still naming concrete risks.","activate":true}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, out)
	var resp struct {
		OK        bool   `json:"ok"`
		Key       string `json:"key"`
		Activated bool   `json:"activated"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.Equal(t, "terse_code_review", resp.Key)
	require.True(t, resp.Activated)

	listed, handled, isErr, err := orchestrateRT.Executor(ctx, "list_personalities", nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, listed)
	require.Contains(t, listed, "Terse Code Review")
	require.Contains(t, listed, "terse_code_review")

	setOut, handled, isErr, err := orchestrateRT.Executor(ctx, "set_personality", json.RawMessage(`{"personality":"terse_code_review"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, setOut)
	require.Contains(t, setOut, "Personality changed")
	current, err := h.settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	require.Equal(t, "terse_code_review", current)

	planRT := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	capabilities, handled, isErr, err := planRT.Executor(ctx, "list_capabilities", nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr, capabilities)
	require.NotContains(t, capabilities, "save_custom_personality")
	blocked, handled, isErr, err := planRT.Executor(ctx, "save_custom_personality", json.RawMessage(`{"mode":"create","name":"Blocked","system_prompt":"This blocked prompt is long enough."}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, blocked, "not available in plan mode")
}

func TestFormatCapabilitiesIncludesSelectedMemoryHandles(t *testing.T) {
	out := formatCapabilities([]chatcontrol.ActionSummary{{Domain: "memory", Name: "memory_view", Description: "Load selected memory.", Access: "read"}}, []string{"usage_analytics.md"})
	if !strings.Contains(out, "Selected memories for this turn") || !strings.Contains(out, "usage_analytics.md") {
		t.Fatalf("expected selected memory handles in capabilities output, got:\n%s", out)
	}
}

func TestListCapabilitiesExecutorIncludesSelectedMemoryHandles(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{IsTaskFollowup: true},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true),
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	ctx := service.WithSelectedMemoryHandles(context.Background(), []string{"chat_memory.md"})
	out, handled, isErr, err := rt.Executor(ctx, "list_capabilities", nil)
	if !handled || isErr || err != nil {
		t.Fatalf("list_capabilities failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, want := range []string{"Selected memories for this turn", "chat_memory.md", "memory_view"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected list_capabilities output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestViewSwarmRuntimeToolResolvesCurrentTaskThreadParent(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Current Swarm Project")
	parent := createTask(t, h, project.ID, "Current parent", func(task *models.Task) {
		task.SwarmRole = models.SwarmRoleParent
		task.SwarmStatus = "planning"
	})
	child := createTask(t, h, project.ID, "Current worker", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleWorker
		task.SwarmSequence = 1
		task.Status = models.StatusRunning
	})

	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	definitionNames := make([]string, 0, len(defs))
	for _, def := range defs {
		definitionNames = append(definitionNames, def.Name)
	}
	require.Contains(t, definitionNames, "view_swarm", "task-thread runtime definitions should include view_swarm")
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, TaskID: parent.ID, IsTaskFollowup: true, ChatMode: models.ChatModeOrchestrate},
		nil,
		defs,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(context.Background(), "view_swarm", json.RawMessage(`{"task_id":"current"}`))
	require.True(t, handled)
	require.False(t, isErr, out)
	require.NoError(t, err)

	var got struct {
		OK              bool   `json:"ok"`
		IsSwarm         bool   `json:"is_swarm"`
		RequestedTaskID string `json:"requested_task_id"`
		ParentTaskID    string `json:"parent_task_id"`
		Children        []struct {
			TaskID    string `json:"task_id"`
			Title     string `json:"title"`
			Status    string `json:"status"`
			SwarmRole string `json:"swarm_role"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.True(t, got.IsSwarm)
	require.Equal(t, parent.ID, got.RequestedTaskID)
	require.Equal(t, parent.ID, got.ParentTaskID)
	require.Len(t, got.Children, 1)
	require.Equal(t, child.ID, got.Children[0].TaskID)
	require.Equal(t, "Current worker", got.Children[0].Title)
	require.Equal(t, "running", got.Children[0].Status)
	require.Equal(t, "worker", got.Children[0].SwarmRole)
}

func TestListChannelsPlanModeReturnsPromptSafeStatus(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Channel Status Project")

	webhookRepo := repository.NewWebhookRepo(db)
	h.SetWebhookRepo(webhookRepo)
	channelTargetRepo := repository.NewChannelTargetRepo(db)
	h.SetChannelTargetRepo(channelTargetRepo)
	telegramAuthRepo := repository.NewTelegramAuthRepo(db)
	h.SetTelegramAuthRepo(telegramAuthRepo)

	h.SetGitHubService(&fakeGitHubService{statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
		return service.GitHubConnectionStatus{Configured: true, Connected: true, AuthMode: service.GitHubAuthModePAT, AccountLogin: "ov-user", AccountType: "User", HasPAT: true}, nil
	}})
	h.SetSlackService(&fakeSlackService{statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
		return service.SlackConnectionStatus{Configured: true, Connected: true, Running: true, TeamName: "OpenVibely", TeamID: "TSAFE", BotUserID: "BSAFE", BotTokenSource: service.SlackBotTokenSourceOAuth}, nil
	}})
	h.SetDiscordService(&fakeDiscordService{statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
		return service.DiscordConnectionStatus{Configured: true, Connected: false, Running: false, HasBotToken: true, BotUserID: "bot-safe", SendResponses: true, LastError: "gateway unavailable"}, nil
	}})

	secretValues := []string{
		"ghp_secret_pat_should_not_appear",
		"private-key-secret-should-not-appear",
		"slack-client-secret-should-not-appear",
		"xapp-secret-should-not-appear",
		"xoxb-secret-should-not-appear",
		"telegram-secret-token-should-not-appear",
		"discord-secret-token-should-not-appear",
		"email-password-secret-should-not-appear",
		"webhook-secret-should-not-appear",
		"webhook-path-token-should-not-appear",
		"CSECRETCHANNEL",
		"thread-secret-should-not-appear",
		"secret subject should not appear",
	}
	settings := map[string]string{
		service.GitHubSettingPAT:                                          secretValues[0],
		service.GitHubSettingAppPrivateKey:                                secretValues[1],
		service.SlackSettingClientSecret:                                  secretValues[2],
		service.SlackSettingAppToken:                                      secretValues[3],
		service.SlackSettingBotToken:                                      secretValues[4],
		service.TelegramSettingBotToken:                                   secretValues[5],
		service.DiscordSettingBotToken:                                    secretValues[6],
		service.EmailSettingAddress:                                       "alerts@example.com",
		service.EmailSettingPassword:                                      secretValues[7],
		service.EmailSettingIMAPHost:                                      "imap.example.com",
		service.EmailSettingSMTPHost:                                      "smtp.example.com",
		service.SendMessageAllowExplicitTargetsSetting + ":" + project.ID: "false",
	}
	for key, value := range settings {
		require.NoError(t, h.settingsRepo.Set(ctx, key, value))
	}

	require.NoError(t, h.githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", DisplayName: "Reviewer", AddedBy: "test"}))
	for _, userID := range []string{"U123", "U456"} {
		require.NoError(t, h.slackAuthRepo.Create(ctx, &models.SlackAuthorizedUser{ProjectID: project.ID, SlackUserID: userID, DisplayName: "Slack " + userID, AddedBy: "test"}))
	}
	require.NoError(t, telegramAuthRepo.Create(ctx, &models.TelegramAuthorizedUser{ProjectID: project.ID, TelegramUserID: 1001, TelegramUsername: "tguser", DisplayName: "Telegram User", AddedBy: "test"}))
	require.NoError(t, h.discordAuthRepo.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "1002", DisplayName: "Discord User", AddedBy: "test"}))
	require.NoError(t, h.emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "sender@example.com", DisplayName: "Sender", AddedBy: "test"}))

	require.NoError(t, webhookRepo.Create(ctx, &models.WebhookEndpoint{ProjectID: project.ID, Name: "Deploy Alerts", Enabled: true, PathToken: secretValues[9], Secret: secretValues[8], DefaultPriority: 2}))
	require.NoError(t, webhookRepo.Create(ctx, &models.WebhookEndpoint{ProjectID: project.ID, Name: "Disabled Hook", Enabled: false, DefaultPriority: 2}))
	require.NoError(t, channelTargetRepo.Upsert(ctx, models.ChannelTarget{ID: repository.NewID(), ProjectID: project.ID, Platform: "slack", TargetKind: "channel", Name: "ops", TargetID: secretValues[10], ThreadID: secretValues[11], Home: true}))
	require.NoError(t, channelTargetRepo.Upsert(ctx, models.ChannelTarget{ID: repository.NewID(), ProjectID: project.ID, Platform: "slack", TargetKind: "user", TargetID: "U123"}))
	require.NoError(t, channelTargetRepo.Upsert(ctx, models.ChannelTarget{ID: repository.NewID(), ProjectID: project.ID, Platform: "email", TargetKind: "email", Name: "team", TargetID: "team@example.com", DefaultSubject: secretValues[12]}))

	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "list_channels", json.RawMessage(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("list_channels failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	var summary channelStatusToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &summary))
	require.True(t, summary.OK)
	require.Equal(t, 6, summary.ConfiguredChannelCount)
	require.Equal(t, "connected", summary.GitHub.Status)
	require.Equal(t, "ov-user", summary.GitHub.AccountLogin)
	require.Equal(t, 1, summary.GitHub.AuthorizedActorCount)
	require.Equal(t, "connected", summary.Slack.Status)
	require.Equal(t, "OpenVibely", summary.Slack.TeamName)
	require.Equal(t, 2, summary.Slack.AuthorizedUserCount)
	require.Equal(t, "configured_not_running", summary.Telegram.Status)
	require.Equal(t, 1, summary.Telegram.AuthorizedUserCount)
	require.Equal(t, "gateway_offline", summary.Discord.Status)
	require.Equal(t, "gateway unavailable", summary.Discord.LastError)
	require.Equal(t, 1, summary.Email.AuthorizedSenderCount)
	require.Equal(t, "alerts@example.com", summary.Email.Address)
	require.Equal(t, 2, summary.Webhooks.Total)
	require.Equal(t, 1, summary.Webhooks.Active)
	require.Equal(t, 3, summary.OutboundTargets.Total)
	require.False(t, summary.OutboundTargets.ExplicitUnsavedTargetsAllowed)
	require.Equal(t, 2, summary.OutboundTargets.ByPlatform["slack"].Total)
	require.Equal(t, 1, summary.OutboundTargets.ByPlatform["slack"].Home)
	require.Equal(t, 1, summary.OutboundTargets.ByPlatform["slack"].Named)
	require.Equal(t, map[string]int{"channel": 1, "user": 1}, summary.OutboundTargets.ByPlatform["slack"].ByKind)
	require.Equal(t, 1, summary.OutboundTargets.ByPlatform["email"].Total)
	require.Equal(t, 1, summary.OutboundTargets.ByPlatform["email"].Named)
	require.Equal(t, map[string]int{"email": 1}, summary.OutboundTargets.ByPlatform["email"].ByKind)

	for _, secret := range secretValues {
		if strings.Contains(out, secret) {
			t.Fatalf("list_channels output exposed secret/raw credential %q in:\n%s", secret, out)
		}
	}
}

func TestListChannelsOutboundTargetAggregateSummaryMatchesMaterializedOutput(t *testing.T) {
	targets := []models.ChannelTarget{
		{Platform: "slack", TargetKind: "channel", Name: "ops", Home: true},
		{Platform: "slack", TargetKind: "user"},
		{Platform: "telegram", TargetKind: "chat", Name: "alerts", Home: true},
		{Platform: "discord", TargetKind: "channel", Name: "guild"},
		{Platform: "discord", TargetKind: "user"},
		{Platform: "email", TargetKind: "email", Name: "team"},
	}
	materialized := summarizeOutboundTargets(targets)
	aggregate := outboundTargetsStatusFromRepoSummary(repository.ChannelTargetProjectSummary{
		Total:      6,
		Configured: true,
		ByPlatform: map[string]repository.ChannelTargetPlatformSummary{
			"slack":    {Total: 2, Home: 1, Named: 1, ByKind: map[string]int{"channel": 1, "user": 1}},
			"telegram": {Total: 1, Home: 1, Named: 1, ByKind: map[string]int{"chat": 1}},
			"discord":  {Total: 2, Home: 0, Named: 1, ByKind: map[string]int{"channel": 1, "user": 1}},
			"email":    {Total: 1, Home: 0, Named: 1, ByKind: map[string]int{"email": 1}},
		},
	})
	materializedJSON, err := json.MarshalIndent(materialized, "", "  ")
	require.NoError(t, err)
	aggregateJSON, err := json.MarshalIndent(aggregate, "", "  ")
	require.NoError(t, err)
	require.Equal(t, string(materializedJSON), string(aggregateJSON))
}

func TestListChannelsEmptyOutboundTargetSummaryKeepsEmptyByPlatform(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Empty Channel Status Targets")
	h.SetChannelTargetRepo(repository.NewChannelTargetRepo(db))

	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "list_channels", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)

	var result struct {
		OutboundTargets struct {
			Configured                    bool                       `json:"configured"`
			ExplicitUnsavedTargetsAllowed bool                       `json:"explicit_unsaved_targets_allowed"`
			MessagingAvailable            bool                       `json:"messaging_available"`
			ByPlatform                    map[string]json.RawMessage `json:"by_platform"`
		} `json:"outbound_message_targets"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.False(t, result.OutboundTargets.Configured)
	require.False(t, result.OutboundTargets.ExplicitUnsavedTargetsAllowed)
	require.False(t, result.OutboundTargets.MessagingAvailable)
	require.NotNil(t, result.OutboundTargets.ByPlatform)
	require.Empty(t, result.OutboundTargets.ByPlatform)
}
func TestViewSystemUpdateRuntimeTool_NotApplicableWithoutVisibleCoordinator(t *testing.T) {
	for _, tc := range []struct {
		name        string
		coordinator *update.Coordinator
	}{
		{name: "no coordinator"},
		{name: "hidden source coordinator", coordinator: update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "source"}, "stable", nil, nil, false, "", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			h.SetUpdateCoordinator(tc.coordinator)
			handler := h.chatActionHandlers(streamingResponseParams{}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)["view_system_update"]
			require.NotNil(t, handler)

			out, err := handler(context.Background(), json.RawMessage(`{}`))
			require.NoError(t, err)
			var got systemUpdateStatusToolResponse
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			require.True(t, got.OK)
			require.False(t, got.Applicable)
			require.Contains(t, got.Message, "not currently visible or applicable")
		})
	}
}

func TestViewSystemUpdateRuntimeTool_AvailableUpdateReportsReleaseMetadata(t *testing.T) {
	h := &Handler{}
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "binary"}, "stable", nil, nil, true, "", nil)
	statePath := filepath.Join(t.TempDir(), "update-state.json")
	persisted := `{
		"state":"available",
		"release":{"metadata":{"version":"1.2.3","channel":"stable","release_notes_url":"https://example.test/releases/1.2.3"},"target":{"kind":"binary","os":"darwin","arch":"arm64","url":"https://secret.example.test/artifact","sha256":"secret-sha"},"apply_supported":true,"action":"restart"},
		"staged_release":{"metadata":{"version":"1.2.3","channel":"stable","release_notes_url":"https://example.test/releases/1.2.3"},"target":{"kind":"binary"},"apply_supported":true,"action":"restart"}
	}`
	require.NoError(t, os.WriteFile(statePath, []byte(persisted), 0o600))
	require.NoError(t, coordinator.SetPersistence(statePath))
	h.SetUpdateCoordinator(coordinator)

	handler := h.chatActionHandlers(streamingResponseParams{}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["view_system_update"]
	out, err := handler(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var got systemUpdateStatusToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.True(t, got.Applicable)
	require.Equal(t, update.StateAvailable, got.State)
	require.Equal(t, "1.0.0", got.CurrentVersion)
	require.Equal(t, "1.2.3", got.TargetVersion)
	require.Equal(t, "binary", got.Distribution)
	require.Equal(t, "stable", got.Channel)
	require.Equal(t, "https://example.test/releases/1.2.3", got.ReleaseNotesURL)
	require.True(t, got.Manual)
	require.True(t, got.Staged)
	require.True(t, got.ApplySupported)
	require.Equal(t, "restart", got.UpdateAction)
	require.NotContains(t, out, "secret.example.test")
	require.NotContains(t, out, "secret-sha")
}

func TestViewSystemUpdateRuntimeTool_WaitingForIdleReportsDrainStatus(t *testing.T) {
	drain := update.NewDrainManager(
		func() update.ActiveWork {
			return update.ActiveWork{TaskExecutions: 1, ChatExecutions: 2, AutomationActivities: 3}
		},
		func() int { return 4 },
		time.Minute,
		time.Now,
	)
	_, err := drain.BeginDrain(update.DrainRequest{Lease: 5 * time.Minute})
	require.NoError(t, err)
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "docker"}, "stable", drain, nil, true, "", nil)
	coordinator.SetManagedStateProvider(func() update.ManagedUpdateState {
		return update.ManagedUpdateState{Active: true, State: update.StateWaitingForIdle, DesiredVersion: "1.2.3", ReleaseNotesURL: "https://example.test/releases/1.2.3"}
	})
	h := &Handler{}
	h.SetUpdateCoordinator(coordinator)

	handler := h.chatActionHandlers(streamingResponseParams{}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)["view_system_update"]
	out, err := handler(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var got systemUpdateStatusToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, update.StateWaitingForIdle, got.State)
	require.Contains(t, got.Message, "waiting for active work")
	require.Equal(t, update.DrainStateDraining, got.Drain.State)
	require.Equal(t, 1, got.Drain.TaskExecutions)
	require.Equal(t, 2, got.Drain.ChatExecutions)
	require.Equal(t, 3, got.Drain.AutomationActivities)
	require.Equal(t, 6, got.Drain.ActiveTotal)
	require.Equal(t, 4, got.Drain.QueuedTotal)
	require.NotEmpty(t, got.Drain.ExpiresAt)
}

func TestViewSystemUpdateRuntimeTool_FailedReportsErrorAndConfigurationText(t *testing.T) {
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "binary"}, "beta", nil, nil, false, "missing updater signing key", nil)
	coordinator.SetManagedStateProvider(func() update.ManagedUpdateState {
		return update.ManagedUpdateState{Active: true, State: update.StateFailed, DesiredVersion: "1.2.3", Error: "artifact verification failed"}
	})
	h := &Handler{}
	h.SetUpdateCoordinator(coordinator)

	handler := h.chatActionHandlers(streamingResponseParams{}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)["view_system_update"]
	out, err := handler(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var got systemUpdateStatusToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, update.StateFailed, got.State)
	require.Equal(t, "artifact verification failed", got.Error)
	require.Equal(t, "missing updater signing key", got.ConfigurationError)
	require.Contains(t, got.Message, "failed")
}

func TestViewSystemUpdateRuntimeTool_RegisteredAndReadOnly(t *testing.T) {
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		defs := chatcontrol.ToolDefsForContext(mode, chatcontrol.SurfaceWeb, mode == models.ChatModeOrchestrate)
		found := false
		for _, def := range defs {
			if def.Name == "view_system_update" {
				found = true
				require.Equal(t, "read", string(def.Access))
				break
			}
		}
		require.Truef(t, found, "view_system_update definition missing in mode %s", mode)
	}

	h := &Handler{}
	installer := &countingUpdateInstaller{}
	drain := update.NewDrainManager(func() update.ActiveWork { return update.ActiveWork{} }, func() int { return 0 }, time.Minute, time.Now)
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "binary"}, "stable", drain, installer, false, "", nil)
	coordinator.SetManagedStateProvider(func() update.ManagedUpdateState {
		return update.ManagedUpdateState{Active: true, State: update.StateAvailable, DesiredVersion: "1.2.3"}
	})
	h.SetUpdateCoordinator(coordinator)
	beforeDrain := drain.Status()

	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(context.Background(), "view_system_update", nil)
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, out, `"ok":true`)
	require.Equal(t, beforeDrain.State, drain.Status().State)
	require.Zero(t, installer.stageCalls)
	require.Zero(t, installer.applyCalls)
	require.Zero(t, installer.validateCalls)
	require.Zero(t, installer.rollbackCalls)

	capabilities := filterTaskThreadCapabilitySummaries(chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb), nil, false)
	found := false
	for _, summary := range capabilities {
		if summary.Name == "view_system_update" {
			found = true
			break
		}
	}
	require.True(t, found, "task-thread capabilities should include view_system_update")
}

type testPulseToolResponse struct {
	OK             bool                 `json:"ok"`
	ProjectID      string               `json:"project_id"`
	LookaheadDays  int                  `json:"lookahead_days"`
	RunningTasks   []testPulseTaskEntry `json:"running_tasks"`
	PendingTasks   []testPulseTaskEntry `json:"pending_tasks"`
	ScheduledTasks []testPulseTaskEntry `json:"scheduled_tasks"`
	TaskSummary    struct {
		TotalPending int `json:"total_pending"`
		Priority     struct {
			Urgent int `json:"urgent"`
			High   int `json:"high"`
			Normal int `json:"normal"`
			Low    int `json:"low"`
		} `json:"priority"`
		Status struct {
			Pending   int `json:"pending"`
			Running   int `json:"running"`
			Completed int `json:"completed"`
			Failed    int `json:"failed"`
		} `json:"status"`
		Category struct {
			Active    int `json:"active"`
			Backlog   int `json:"backlog"`
			Scheduled int `json:"scheduled"`
		} `json:"category"`
		Scheduled struct {
			Overdue     int `json:"overdue"`
			DueToday    int `json:"due_today"`
			DueThisWeek int `json:"due_this_week"`
		} `json:"scheduled"`
	} `json:"task_summary"`
}

type testPulseTaskEntry struct {
	TaskID         string     `json:"task_id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	Category       string     `json:"category"`
	Priority       int        `json:"priority"`
	PromptPreview  string     `json:"prompt_preview"`
	ScheduleID     string     `json:"schedule_id"`
	NextRun        *time.Time `json:"next_run"`
	RepeatType     string     `json:"repeat_type"`
	RepeatInterval int        `json:"repeat_interval"`
	RepeatLabel    string     `json:"repeat_label"`
}

func TestViewPulseRuntimeToolReturnsUpcomingAgenda(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Pulse Runtime Project")
	foreign := createProject(t, h, "Foreign Pulse Project")
	agent := createAgent(t, h.llmConfigRepo, func(a *models.LLMConfig) { a.Name = "Pulse Agent" })
	longPrompt := strings.Repeat("x", 350)

	running := &models.Task{ProjectID: project.ID, Title: "Running implementation", Prompt: longPrompt, Category: models.CategoryActive, Status: models.StatusRunning, Priority: 4, AgentID: &agent.ID}
	require.NoError(t, h.taskRepo.Create(ctx, running))
	pending := &models.Task{ProjectID: project.ID, Title: "Queued follow-up", Prompt: "short prompt", Category: models.CategoryActive, Status: models.StatusPending, Priority: 3}
	require.NoError(t, h.taskRepo.Create(ctx, pending))
	scheduledTask := &models.Task{ProjectID: project.ID, Title: "Scheduled today", Prompt: "scheduled prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, h.taskRepo.Create(ctx, scheduledTask))
	futureTask := &models.Task{ProjectID: project.ID, Title: "Scheduled later", Prompt: "future prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 1}
	require.NoError(t, h.taskRepo.Create(ctx, futureTask))
	foreignTask := &models.Task{ProjectID: foreign.ID, Title: "Foreign scheduled", Prompt: "foreign prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 4}
	require.NoError(t, h.taskRepo.Create(ctx, foreignTask))

	now := time.Now().Local()
	todayLateLocal := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, time.Local).UTC()
	scheduled := &models.Schedule{TaskID: scheduledTask.ID, RunAt: todayLateLocal, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, h.scheduleRepo.Create(ctx, scheduled))
	future := &models.Schedule{TaskID: futureTask.ID, RunAt: now.UTC().AddDate(0, 0, 8), RepeatType: models.RepeatWeekly, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, h.scheduleRepo.Create(ctx, future))
	foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: todayLateLocal, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, h.scheduleRepo.Create(ctx, foreignSchedule))

	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "view_pulse", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.NotContains(t, out, strings.Repeat("x", 250), "full prompts must not be exposed")
	require.NotContains(t, out, foreignTask.ID)
	require.NotContains(t, out, future.ID)

	var got testPulseToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.Equal(t, project.ID, got.ProjectID)
	require.Equal(t, 7, got.LookaheadDays)
	require.Len(t, got.RunningTasks, 1)
	require.Equal(t, running.ID, got.RunningTasks[0].TaskID)
	require.Equal(t, "Running implementation", got.RunningTasks[0].Title)
	require.LessOrEqual(t, len(got.RunningTasks[0].PromptPreview), 200)
	require.Len(t, got.PendingTasks, 1)
	require.Equal(t, pending.ID, got.PendingTasks[0].TaskID)
	require.Len(t, got.ScheduledTasks, 1)
	require.Equal(t, scheduledTask.ID, got.ScheduledTasks[0].TaskID)
	require.Equal(t, scheduled.ID, got.ScheduledTasks[0].ScheduleID)
	require.Equal(t, "daily", got.ScheduledTasks[0].RepeatLabel)
	require.NotNil(t, got.ScheduledTasks[0].NextRun)
	require.Equal(t, 4, got.TaskSummary.TotalPending)
	require.Equal(t, 1, got.TaskSummary.Priority.Urgent)
	require.Equal(t, 1, got.TaskSummary.Priority.High)
	require.Equal(t, 1, got.TaskSummary.Priority.Normal)
	require.Equal(t, 1, got.TaskSummary.Priority.Low)
	require.Equal(t, 3, got.TaskSummary.Status.Pending)
	require.Equal(t, 1, got.TaskSummary.Status.Running)
	require.Equal(t, 2, got.TaskSummary.Category.Active)
	require.Equal(t, 2, got.TaskSummary.Category.Scheduled)
	require.GreaterOrEqual(t, got.TaskSummary.Scheduled.Overdue, 0)
	require.GreaterOrEqual(t, got.TaskSummary.Scheduled.DueToday, 0)
	require.GreaterOrEqual(t, got.TaskSummary.Scheduled.DueThisWeek, 0)
}

func TestViewPulseRuntimeToolEmptyProjectReturnsZeroAgenda(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Empty Pulse Project")
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "view_pulse", json.RawMessage(`{}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)

	var got testPulseToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.Empty(t, got.RunningTasks)
	require.Empty(t, got.PendingTasks)
	require.Empty(t, got.ScheduledTasks)
	require.Zero(t, got.TaskSummary.TotalPending)
	require.Zero(t, got.TaskSummary.Scheduled.Overdue)
	require.Zero(t, got.TaskSummary.Scheduled.DueToday)
	require.Zero(t, got.TaskSummary.Scheduled.DueThisWeek)
}

// TestViewTaskThreadResolvesCurrentTaskID reproduces the incident where an
// audit-only task-thread follow-up turn called view_task_thread with an
// explicit task_id of "current" (or omitted task_id/title entirely) and got
// "task current not found" instead of resolving to the persisted task
// backing the follow-up, matching resolveTaskIDForTool's handling for the
// goal and send_to_task runtime tools.
func TestViewTaskThreadResolvesCurrentTaskID(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "View Task Thread Current")
	task := &models.Task{ProjectID: project.ID, Title: "Audit target task", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "do the work", Priority: 2}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	params := streamingResponseParams{TaskID: task.ID, ProjectID: project.ID, IsTaskFollowup: true}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	t.Run("explicit current task_id", func(t *testing.T) {
		out, err := handlers["view_task_thread"](context.Background(), []byte(`{"task_id":"current"}`))
		if err != nil {
			t.Fatalf("view_task_thread with explicit current task_id failed: out=%q err=%v", out, err)
		}
	})

	t.Run("omitted task_id and title", func(t *testing.T) {
		out, err := handlers["view_task_thread"](context.Background(), []byte(`{}`))
		if err != nil {
			t.Fatalf("view_task_thread with omitted task_id/title failed: out=%q err=%v", out, err)
		}
	})

	t.Run("current outside a task-thread follow-up is rejected", func(t *testing.T) {
		nonFollowupHandlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		out, err := nonFollowupHandlers["view_task_thread"](context.Background(), []byte(`{"task_id":"current"}`))
		if err == nil || !strings.Contains(err.Error(), "only valid in a persisted task thread") {
			t.Fatalf("expected current task_id outside a follow-up to be rejected, out=%q err=%v", out, err)
		}
	})
}

type cancelTaskRuntimeToolResponse struct {
	OK               bool                `json:"ok"`
	Accepted         bool                `json:"accepted"`
	TaskID           string              `json:"task_id"`
	Title            string              `json:"title"`
	PreviousStatus   models.TaskStatus   `json:"previous_status"`
	PreviousCategory models.TaskCategory `json:"previous_category"`
	FinalStatus      models.TaskStatus   `json:"final_status"`
	FinalCategory    models.TaskCategory `json:"final_category"`
	Message          string              `json:"message"`
}

func decodeCancelTaskRuntimeToolResponse(t *testing.T, out string) cancelTaskRuntimeToolResponse {
	t.Helper()
	var resp cancelTaskRuntimeToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	return resp
}

func TestCancelTaskRuntimeToolCancelsRunningTaskByID(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).WithTitle("Runtime cancel by id").WithStatus(models.StatusRunning).WithCategory(models.CategoryActive).Build()
	exec := tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecRunning).Build()
	hub := events.NewExecutionStreamHub()
	tc.handler.SetExecutionStreamHub(hub)
	sub, _, err := hub.Subscribe(exec.ID)
	require.NoError(t, err)
	handler := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

	out, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	resp := decodeCancelTaskRuntimeToolResponse(t, out)
	require.True(t, resp.OK)
	require.True(t, resp.Accepted)
	require.Equal(t, task.ID, resp.TaskID)
	require.Equal(t, models.StatusRunning, resp.PreviousStatus)
	require.Equal(t, models.CategoryActive, resp.PreviousCategory)
	require.Equal(t, models.StatusCancelled, resp.FinalStatus)
	require.Equal(t, models.CategoryBacklog, resp.FinalCategory)

	updatedTask, err := tc.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedTask.Status)
	require.Equal(t, models.CategoryBacklog, updatedTask.Category)
	updatedExec, err := tc.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCancelled, updatedExec.Status)
	require.Equal(t, "cancelled", updatedExec.ErrorMessage)
	select {
	case event, ok := <-sub:
		require.True(t, ok)
		require.Equal(t, events.ExecutionStreamDone, event.Type)
		require.Equal(t, "cancelled", event.Status)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancelled execution stream event")
	}
}

func TestCancelTaskRuntimeToolCancelsQueuedTaskByExactTitleAndPendingInputs(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).WithTitle("Runtime cancel exact title").WithStatus(models.StatusQueued).WithCategory(models.CategoryActive).Build()
	queuedInput := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued follow-up"}
	require.NoError(t, tc.handler.threadInputRepo.CreateQueued(ctx, queuedInput))
	handler := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

	out, err := handler(ctx, json.RawMessage(`{"title":"Runtime cancel exact title"}`))
	require.NoError(t, err)
	resp := decodeCancelTaskRuntimeToolResponse(t, out)
	require.True(t, resp.Accepted)
	require.Equal(t, task.ID, resp.TaskID)
	require.Equal(t, models.StatusCancelled, resp.FinalStatus)
	require.Equal(t, models.CategoryBacklog, resp.FinalCategory)
	storedInput, err := tc.handler.threadInputRepo.GetByID(ctx, queuedInput.ID)
	require.NoError(t, err)
	require.NotNil(t, storedInput)
	require.Equal(t, models.ThreadInputCancelled, storedInput.InputStatus)

	partialTask := tc.CreateTask(project.ID).WithTitle("Runtime cancel exact title extra").WithStatus(models.StatusQueued).WithCategory(models.CategoryActive).Build()
	out, err = handler(ctx, json.RawMessage(`{"title":"Runtime cancel exact"}`))
	require.ErrorContains(t, err, "no task found with exact title")
	require.Empty(t, out)
	unchanged, err := tc.taskRepo.GetByID(ctx, partialTask.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusQueued, unchanged.Status)
}

func TestCancelTaskRuntimeToolResolvesCurrentTaskID(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).WithTitle("Current cancellable task").WithStatus(models.StatusPending).WithCategory(models.CategoryActive).Build()
	handler := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

	out, err := handler(ctx, json.RawMessage(`{"task_id":"current"}`))
	require.NoError(t, err)
	resp := decodeCancelTaskRuntimeToolResponse(t, out)
	require.True(t, resp.Accepted)
	require.Equal(t, task.ID, resp.TaskID)
	updated, err := tc.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updated.Status)

	nonFollowup := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]
	out, err = nonFollowup(ctx, json.RawMessage(`{"task_id":"current"}`))
	require.ErrorContains(t, err, "only valid in a persisted task thread")
	require.Empty(t, out)
}

func TestCancelTaskRuntimeToolRejectsForeignProjectTargets(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	foreignProject := tc.CreateProject().Build()
	foreignTask := tc.CreateTask(foreignProject.ID).WithTitle("Foreign runtime cancel").WithStatus(models.StatusRunning).WithCategory(models.CategoryActive).Build()
	handler := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

	out, err := handler(ctx, json.RawMessage(`{"task_id":"`+foreignTask.ID+`"}`))
	require.ErrorContains(t, err, "belongs to a different project")
	require.Empty(t, out)
	out, err = handler(ctx, json.RawMessage(`{"title":"Foreign runtime cancel"}`))
	require.ErrorContains(t, err, "no task found")
	require.Empty(t, out)
	unchanged, err := tc.taskRepo.GetByID(ctx, foreignTask.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusRunning, unchanged.Status)
	require.Equal(t, models.CategoryActive, unchanged.Category)
}

func TestCancelTaskRuntimeToolDelegatesSwarmParentCancellation(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Runtime Swarm Cancel")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{ProjectID: project.ID, Title: "Runtime swarm parent", Prompt: "Build it", Category: models.CategoryActive, Priority: 2, AgentID: &agent.ID, MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusRunning))
	handler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

	out, err := handler(ctx, json.RawMessage(`{"task_id":"`+parent.ID+`"}`))
	require.NoError(t, err)
	resp := decodeCancelTaskRuntimeToolResponse(t, out)
	require.True(t, resp.Accepted)
	require.Equal(t, parent.ID, resp.TaskID)
	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedParent.Status)
	updatedPlanner, err := h.taskRepo.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedPlanner.Status)
}

func TestCancelTaskRuntimeToolReturnsNotCancellableWithoutMutation(t *testing.T) {
	tests := []struct {
		name     string
		status   models.TaskStatus
		category models.TaskCategory
	}{
		{name: "completed", status: models.StatusCompleted, category: models.CategoryCompleted},
		{name: "failed", status: models.StatusFailed, category: models.CategoryBacklog},
		{name: "already cancelled", status: models.StatusCancelled, category: models.CategoryBacklog},
		{name: "backlog pending", status: models.StatusPending, category: models.CategoryBacklog},
		{name: "scheduled pending", status: models.StatusPending, category: models.CategoryScheduled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().Build()
			task := tc.CreateTask(project.ID).WithTitle("Not cancellable " + tt.name).WithStatus(tt.status).WithCategory(tt.category).Build()
			handler := tc.handler.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["cancel_task"]

			out, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`"}`))
			require.NoError(t, err)
			resp := decodeCancelTaskRuntimeToolResponse(t, out)
			require.True(t, resp.OK)
			require.False(t, resp.Accepted)
			require.Equal(t, tt.status, resp.FinalStatus)
			require.Equal(t, tt.category, resp.FinalCategory)
			require.Contains(t, resp.Message, "not currently cancellable")
			updated, err := tc.taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, tt.status, updated.Status)
			require.Equal(t, tt.category, updated.Category)
		})
	}
}

func TestWebRuntimeToolsUseSharedInputDecoder(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Shared Web Runtime Decoder")
	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: "shared-web-runtime-decoder", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	ctx := context.Background()

	for _, input := range []json.RawMessage{nil, json.RawMessage(" \n\t ")} {
		out, err := handlers["set_chat_mode"](ctx, input)
		require.NoError(t, err)
		require.Contains(t, out, "Chat mode set to orchestrate")
	}

	for name, handler := range map[string]chatcontrol.RuntimeActionHandler{
		"create_swarm_task": handlers["create_swarm_task"],
		"github_get_issue":  handlers["github_get_issue"],
		"send_to_task":      handlers["send_to_task"],
		"set_chat_mode":     handlers["set_chat_mode"],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler(ctx, json.RawMessage(`{"broken":`))
			require.ErrorContains(t, err, "invalid tool input JSON:")
			require.ErrorContains(t, err, "unexpected end of JSON input")
		})
	}
}

func TestCreateTaskRuntimeToolNormalizesAndDecodesInput(t *testing.T) {
	tests := []struct {
		name            string
		input           json.RawMessage
		wantError       string
		wantStoredTitle string
		wantPriority    int
	}{
		{name: "empty", input: nil, wantError: "create_task requires title and prompt"},
		{name: "whitespace", input: json.RawMessage(" \n\t "), wantError: "create_task requires title and prompt"},
		{name: "missing title", input: json.RawMessage(`{"prompt":"Create this task"}`), wantError: "create_task requires title and prompt"},
		{name: "missing prompt", input: json.RawMessage(`{"title":"Decoded task"}`), wantError: "create_task requires title and prompt"},
		{name: "valid", input: json.RawMessage(`{"title":" Decoded task ","prompt":" Create this task ","category":"backlog"}`), wantStoredTitle: "Decoded task", wantPriority: 2},
		{name: "malformed", input: json.RawMessage(`{"title":`), wantError: "invalid tool input JSON:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _, _ := setupTestHandlerWithDB(t)
			project := createProject(t, h, "Runtime input "+tt.name)
			handler := h.chatActionHandlers(
				streamingResponseParams{ExecID: "runtime-input-exec", ProjectID: project.ID},
				nil,
				models.ChatModeOrchestrate,
				chatcontrol.SurfaceWeb,
			)["create_task"]

			out, err := handler(context.Background(), tt.input)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				if tt.name == "malformed" {
					require.ErrorContains(t, err, "unexpected end of JSON input")
				}
				return
			}
			require.NoError(t, err)
			require.Contains(t, out, "Decoded task")
			if tt.wantStoredTitle != "" {
				tasks, listErr := h.taskRepo.ListByProject(context.Background(), project.ID, "")
				require.NoError(t, listErr)
				require.Len(t, tasks, 1)
				require.Equal(t, tt.wantStoredTitle, tasks[0].Title)
				require.Equal(t, tt.wantPriority, tasks[0].Priority)
			}
		})
	}
}

func TestCreateTaskRuntimeToolUsesCompactModelSelection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, _, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	_, err := db.Exec(`DELETE FROM agent_configs`)
	require.NoError(t, err)
	project := createProject(t, h, "Runtime Compact Create Task")
	largeProviderJSON := strings.Repeat("large-provider-json", 4096)
	agent := &models.LLMConfig{
		Name: "Runtime Compact Default", Provider: models.ProviderTest, AuthMethod: models.AuthMethodAPIKey,
		Model: "claude-sonnet-runtime-create", IsDefault: true, APIKey: "secret-api-key",
		OAuthAccessToken: "secret-oauth-token", OAuthRefreshToken: "secret-refresh-token", OAuthClientSecret: "secret-client-secret",
		BaseURL: "https://example.com/v1/", ExtraHeadersJSON: `{"X-Secret":"value"}`,
		ExtraBodyJSON: largeProviderJSON, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"access":"secret"}`, MixtureConfigJSON: `{"large":"` + largeProviderJSON + `"}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	handler := h.chatActionHandlers(
		streamingResponseParams{ExecID: "runtime-compact-create-exec", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)["create_task"]

	counter.Reset()
	counter.SetEnabled(true)
	out, err := handler(ctx, json.RawMessage(`{"title":"Compact created task","prompt":"Create this task"}`))
	counter.SetEnabled(false)
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1)
	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.NotNil(t, created.AgentID)
	require.Equal(t, agent.ID, *created.AgentID)
	assertCreateTaskRuntimeUsesCompactModelSelection(t, counter.Statements())
}

func TestCreateTaskRuntimeToolDefersAutoStartUntilChatAttachmentsActivate(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	_, err := db.Exec(`DELETE FROM agent_configs`)
	require.NoError(t, err)
	project := createProject(t, h, "Runtime Attachment Deferral")
	agent := &models.LLMConfig{Name: "Attachment Auto Start", Provider: models.ProviderTest, Model: "claude-sonnet-attachments", IsDefault: true, AutoStartTasks: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	chatTask := &models.Task{ProjectID: project.ID, Title: "Chat source", Prompt: "chat", Category: models.CategoryChat, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))
	exec := &models.Execution{TaskID: chatTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "create task with attachment"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	sourceDir := t.TempDir()
	sourceFile := filepath.Join(sourceDir, "evidence.txt")
	require.NoError(t, os.WriteFile(sourceFile, []byte("attachment evidence"), 0644))
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "evidence.txt",
		FilePath:    sourceFile,
		MediaType:   "text/plain",
		FileSize:    int64(len("attachment evidence")),
	}))

	handler := h.chatActionHandlers(
		streamingResponseParams{ExecID: exec.ID, ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)["create_task"]
	out, err := handler(ctx, json.RawMessage(`{"title":"Attachment created task","prompt":"Create this task with the chat attachment"}`))
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1)
	require.Contains(t, out, `"Attachment created task" (active)`)
	require.Contains(t, out, "Attachments copied to tasks: evidence.txt")

	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, models.CategoryActive, created.Category)
	attachments, err := h.attachmentRepo.ListByTask(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)

	select {
	case submitted := <-h.workerSvc.Submitted():
		require.Equal(t, created.ID, submitted.ID)
		require.Equal(t, models.CategoryActive, submitted.Category)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected deferred active task to submit after attachment copy")
	}
}

func TestDeferActiveTasksWithAttachmentsUsesCompactAutoStartRows(t *testing.T) {
	h := &Handler{}
	agents := []models.LLMConfig{{ID: "compact-auto", Name: "Compact Auto", Provider: models.ProviderTest, Model: "custom-model", AutoStartTasks: true}}
	requests := []service.TaskCreationRequest{{Title: "Auto-start attachment task", Prompt: "Do work"}}

	deferred := h.deferActiveTasksWithAttachments(requests, []models.ChatAttachment{{ID: "attachment"}}, agents)

	require.Equal(t, map[int]bool{0: true}, deferred)
	require.Equal(t, string(models.CategoryBacklog), requests[0].Category)
}

func assertCreateTaskRuntimeUsesCompactModelSelection(t *testing.T, statements []string) {
	t.Helper()
	compactQueries := 0
	for _, raw := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if !strings.Contains(stmt, " from agent_configs") {
			continue
		}
		projection := strings.Split(stmt, " from agent_configs")[0]
		for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_id", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "transport", "preset_slug", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "max_workers", "worker_timeout"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("create_task model query selected forbidden column %q: %s", forbidden, raw)
			}
		}
		if strings.Contains(projection, "select id, name, provider, model, reasoning_effort") {
			t.Fatalf("create_task used full model list query: %s", raw)
		}
		if projection == "select id, name, provider, model, is_default, auto_start_tasks" && strings.Contains(stmt, "order by is_default desc, name asc") {
			compactQueries++
		}
	}
	if compactQueries != 1 {
		t.Fatalf("compact task creation model query count = %d, want 1; statements: %#v", compactQueries, statements)
	}
}

func TestCreateTaskRuntimeToolDecodesTypedChainConfigDirectly(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Typed Create Task Project")
	handler := h.chatActionHandlers(
		streamingResponseParams{ExecID: "typed-create-exec", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)["create_task"]

	out, err := handler(ctx, json.RawMessage(`{
		"title":"Typed chained task",
		"prompt":"Create the parent directly",
		"category":"backlog",
		"chain":{
			"enabled":true,
			"trigger":"on_completion",
			"child_title":"Typed child",
			"child_prompt_prefix":"Continue from the parent",
			"child_category":"active"
		}
	}`))
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1)

	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	chain, err := created.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, chain.Enabled)
	require.Equal(t, "on_completion", chain.Trigger)
	require.Equal(t, "Typed child", chain.ChildTitle)
	require.Equal(t, "Continue from the parent", chain.ChildPromptPrefix)
	require.Equal(t, "active", chain.ChildCategory)
}

func TestScheduleTaskRuntimeToolExecutesTypedRequest(t *testing.T) {
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		t.Run(string(surface), func(t *testing.T) {
			h, _, _, _ := setupTestHandlerWithDB(t)
			ctx := context.Background()
			project := createProject(t, h, "Typed Schedule Project "+string(surface))
			foreignProject := createProject(t, h, "Foreign Schedule Project "+string(surface))
			task := createTask(t, h, project.ID, "Schedule directly")
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted))
			task.Status = models.StatusCompleted
			foreignTask := createTask(t, h, foreignProject.ID, "Foreign schedule")
			handlers := h.chatActionHandlers(
				streamingResponseParams{ExecID: "typed-schedule-exec", ProjectID: project.ID},
				nil,
				models.ChatModeOrchestrate,
				surface,
			)

			out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"`+task.ID+`","time":"09:30","repeat":"weekly","days":["wed"],"interval":2,"clear_context_on_start":false}`))
			require.NoError(t, err)
			require.Contains(t, out, "Scheduled task")

			schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Len(t, schedules, 1)
			require.Equal(t, models.RepeatWeekly, schedules[0].RepeatType)
			require.Equal(t, 2, schedules[0].RepeatInterval)
			require.Equal(t, time.Wednesday, schedules[0].RunAt.Local().Weekday())
			require.False(t, schedules[0].ClearContextOnStart)
			require.NotNil(t, schedules[0].NextRun)
			require.WithinDuration(t, schedules[0].RunAt, *schedules[0].NextRun, time.Second)
			updated, err := h.taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryScheduled, updated.Category)
			require.Equal(t, models.StatusPending, updated.Status)

			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":0}`))
			require.NoError(t, err)
			require.Contains(t, out, "between 1 and 365")
			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":365,"days":["fri"],"enabled":false,"clear_context_on_start":true}`))
			require.NoError(t, err)
			require.Contains(t, out, "Updated schedule")
			modified, err := h.scheduleRepo.GetByID(ctx, schedules[0].ID)
			require.NoError(t, err)
			require.Equal(t, 365, modified.RepeatInterval)
			require.Equal(t, time.Friday, modified.RunAt.Local().Weekday())
			require.False(t, modified.Enabled)
			require.True(t, modified.ClearContextOnStart)
			require.NotNil(t, modified.NextRun)
			require.WithinDuration(t, modified.RunAt, *modified.NextRun, time.Second)

			out, err = handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"`+foreignTask.ID+`","time":"09:30"}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")
			foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
			require.NoError(t, h.scheduleRepo.Create(ctx, foreignSchedule))
			out, err = handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+foreignSchedule.ID+`","enabled":false}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")
			out, err = handlers["delete_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+foreignSchedule.ID+`"}`))
			require.NoError(t, err)
			require.Contains(t, out, "different project")

			out, err = handlers["delete_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`"}`))
			require.NoError(t, err)
			require.Contains(t, out, "Deleted schedule")
			remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, remaining)
			updated, err = h.taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, updated.Category)
		})
	}
}

func TestScheduleTaskRuntimeToolDefaultsScheduleFields(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Runtime Schedule Defaults Project")
	task := createTask(t, h, project.ID, "Schedule defaults")
	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: "schedule-defaults-exec", ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)

	out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"`+task.ID+`","time":"09:30"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Scheduled task")

	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, models.RepeatDaily, schedules[0].RepeatType)
	require.Equal(t, 1, schedules[0].RepeatInterval)
	require.True(t, schedules[0].Enabled)
	require.True(t, schedules[0].ClearContextOnStart)
	require.NotNil(t, schedules[0].NextRun)
	require.WithinDuration(t, schedules[0].RunAt, *schedules[0].NextRun, time.Second)
}

func TestScheduleRuntimeToolsResolveCurrentTaskInTaskThread(t *testing.T) {
	ctx := context.Background()

	t.Run("schedule omitted task reference", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Schedule current task omitted")
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"time":"09:30"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Scheduled task")

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.Equal(t, task.ID, schedules[0].TaskID)
	})

	t.Run("schedule explicit current task_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Explicit Project")
		task := createTask(t, h, project.ID, "Schedule current task explicit")
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["schedule_task"](ctx, json.RawMessage(`{"task_id":"current","time":"09:30"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Scheduled task")

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.Equal(t, task.ID, schedules[0].TaskID)
	})

	t.Run("modify explicit current task_id without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Modify Schedule Project")
		task := createTask(t, h, project.ID, "Modify current task schedule")
		schedule := createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"task_id":"current","enabled":false}`))
		require.NoError(t, err)
		require.Contains(t, out, "Updated schedule")

		modified, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
		require.NoError(t, err)
		require.False(t, modified.Enabled)
	})

	t.Run("modify omitted task reference without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Modify Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Modify current task schedule omitted")
		schedule := createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"enabled":false}`))
		require.NoError(t, err)
		require.Contains(t, out, "Updated schedule")

		modified, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
		require.NoError(t, err)
		require.False(t, modified.Enabled)
	})

	t.Run("delete explicit current task_id without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Delete Schedule Project")
		task := createTask(t, h, project.ID, "Delete current task schedule")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["delete_schedule"](ctx, json.RawMessage(`{"task_id":"current"}`))
		require.NoError(t, err)
		require.Contains(t, out, "Deleted schedule")

		remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Empty(t, remaining)
	})

	t.Run("delete omitted task reference without schedule_id", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Delete Schedule Omitted Project")
		task := createTask(t, h, project.ID, "Delete current task schedule omitted")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		out, err := handlers["delete_schedule"](ctx, json.RawMessage(`{}`))
		require.NoError(t, err)
		require.Contains(t, out, "Deleted schedule")

		remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Empty(t, remaining)
	})

	t.Run("current outside task follow-up is rejected", func(t *testing.T) {
		h, _, _, _ := setupTestHandlerWithDB(t)
		project := createProject(t, h, "Current Schedule Outside Project")
		task := createTask(t, h, project.ID, "Outside current task schedule")
		createSchedule(t, h, task.ID, time.Now().UTC().Add(time.Hour))
		handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

		for name, input := range map[string]json.RawMessage{
			"schedule_task":   json.RawMessage(`{"task_id":"current","time":"09:30"}`),
			"modify_schedule": json.RawMessage(`{"task_id":"current","enabled":false}`),
			"delete_schedule": json.RawMessage(`{"task_id":"current"}`),
		} {
			out, err := handlers[name](ctx, input)
			require.ErrorContains(t, err, "only valid in a persisted task thread", "tool=%s out=%q", name, out)
		}

		schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		require.NoError(t, err)
		require.Len(t, schedules, 1)
		require.True(t, schedules[0].Enabled)
	})
}

func TestChatActionSummaryCollector_AppendsCreatedAndEdited(t *testing.T) {
	collector := newChatActionSummaryCollector()
	collector.addCreated("\n---\nCreated 1 task(s):\n- \"Fix login\" (active) [TASK_ID:abc123]")
	collector.addEdited("\n---\nEdited 1 task(s):\n- \"Fix login\" (backlog, updated: category) [TASK_EDITED:abc123]")

	out := collector.appendToOutput("Done.")
	if !strings.Contains(out, "Created 1 task(s):") {
		t.Fatalf("expected created summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_ID:abc123]") {
		t.Fatalf("expected task id marker, got %q", out)
	}
	if !strings.Contains(out, "Edited 1 task(s):") {
		t.Fatalf("expected edited summary, got %q", out)
	}
	if !strings.Contains(out, "[TASK_EDITED:abc123]") {
		t.Fatalf("expected task edited marker, got %q", out)
	}
}

func TestChatActionHandlers_CoverageWebAndAPI(t *testing.T) {
	h := &Handler{}
	params := streamingResponseParams{ExecID: "e", ProjectID: "p"}

	webHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true, webHandlers); err != nil {
		t.Fatalf("web handler coverage mismatch: %v", err)
	}
	if webHandlers["delete_automation"] == nil {
		t.Fatal("web handler coverage missing delete_automation")
	}

	apiHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	if err := chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceAPI, true, apiHandlers); err != nil {
		t.Fatalf("api handler coverage mismatch: %v", err)
	}
	if apiHandlers["delete_automation"] == nil {
		t.Fatal("api handler coverage missing delete_automation")
	}
}

func TestListTasksRuntimeTool_WebAndAPIExecutableAndPlanExposed(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "List Tasks Handler Project")
	target := createTask(t, h, project.ID, "Implement issue 25")
	createTask(t, h, project.ID, "Unrelated handler task")

	// Canonical registry exposes list_tasks read-only in both Plan and Orchestrate.
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
			defs := chatcontrol.ToolDefsForContext(mode, surface, false)
			found := false
			for _, def := range defs {
				if def.Name == "list_tasks" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected list_tasks definition for mode=%s surface=%s", mode, surface)
			}
		}
	}

	params := streamingResponseParams{ExecID: "exec-list-tasks", ProjectID: project.ID}
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, surface)
		handler := handlers["list_tasks"]
		if handler == nil {
			t.Fatalf("list_tasks handler missing on surface=%s", surface)
		}
		out, err := handler(context.Background(), json.RawMessage(`{"query":"issue 25"}`))
		if err != nil {
			t.Fatalf("list_tasks handler failed on surface=%s: %v", surface, err)
		}
		if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, target.ID) {
			t.Fatalf("expected list_tasks to return target id on surface=%s, got %s", surface, out)
		}
	}

	// Plan-mode handler map still wires an executable handler (never handler_missing).
	planHandlers := h.chatActionHandlers(params, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)
	if planHandlers["list_tasks"] == nil {
		t.Fatal("expected list_tasks handler in plan mode")
	}
}

func TestViewUsageAnalyticsRuntimeTool_NoUsageDataReturnsZeroSummary(t *testing.T) {
	h, _, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Usage Empty Project")
	require.NoError(t, llmConfigRepo.Create(ctx, &models.LLMConfig{
		Name:             "OpenAI OAuth Empty",
		Provider:         models.ProviderOpenAI,
		Model:            "gpt-5.3-codex",
		AuthMethod:       models.AuthMethodOAuth,
		OAuthAccessToken: "oauth-token-that-must-not-be-used",
		OAuthAccountID:   "acct-secret-empty",
	}))

	handler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)["view_usage_analytics"]
	if handler == nil {
		t.Fatal("view_usage_analytics handler missing")
	}
	out, err := handler(ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	var got testUsageAnalyticsToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.Equal(t, project.ID, got.ProjectID)
	require.Equal(t, 0, got.Totals.CallCount)
	require.Equal(t, 0, got.Totals.TotalTokens)
	require.Equal(t, "no_usage", got.CostAvailability)
	require.True(t, got.LocalOnly)
	require.False(t, got.ProviderRefreshed)
	require.NotContains(t, out, "oauth-token-that-must-not-be-used")
	require.NotContains(t, out, "acct-secret-empty")
}

func TestViewUsageAnalyticsRuntimeTool_MultipleModelsProviderFilterAndCostAvailability(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Usage Breakdown Project")
	otherProject := createProject(t, h, "Usage Other Project")
	cost := 1.25
	seedUsageEvent := func(provider, model, projectID string, inputTokens, outputTokens, cacheTokens, reasoningTokens int, costUSD *float64) {
		t.Helper()
		require.NoError(t, h.usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
			Provider:              provider,
			ProjectID:             projectID,
			Model:                 model,
			Operation:             "chat",
			Status:                "completed",
			InputTokens:           inputTokens,
			OutputTokens:          outputTokens,
			CachedInputTokens:     cacheTokens,
			ReasoningOutputTokens: reasoningTokens,
			TotalTokens:           inputTokens + outputTokens,
			CostUSD:               costUSD,
			OccurredAt:            time.Now().UTC(),
		}))
	}
	seedUsageEvent("openai", "gpt-5.3-codex", project.ID, 300, 100, 25, 10, &cost)
	seedUsageEvent("anthropic", "claude-sonnet-4-5", project.ID, 200, 50, 0, 0, nil)
	seedUsageEvent("openai", "gpt-5-mini", project.ID, 120, 30, 10, 5, nil)
	seedUsageEvent("openai", "foreign-project-model", otherProject.ID, 1000, 1000, 0, 0, &cost)

	handler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["view_usage_analytics"]
	out, err := handler(ctx, json.RawMessage(`{"range":"all","top_limit":3}`))
	require.NoError(t, err)
	var got testUsageAnalyticsToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, 800, got.Totals.TotalTokens)
	require.Equal(t, 3, got.Totals.CallCount)
	require.Equal(t, "partial", got.CostAvailability)
	require.NotNil(t, got.LastUpdatedAt)
	require.Len(t, got.TopModels, 3)
	require.Equal(t, "gpt-5.3-codex", got.TopModels[0].Model)
	require.Len(t, got.TopProviders, 2)
	require.Equal(t, "openai", got.TopProviders[0].Provider)
	require.Equal(t, 550, got.TopProviders[0].TotalTokens)
	require.NotContains(t, out, "foreign-project-model")

	filteredOut, err := handler(ctx, json.RawMessage(`{"range":"all","provider":"anthropic"}`))
	require.NoError(t, err)
	var filtered testUsageAnalyticsToolResponse
	require.NoError(t, json.Unmarshal([]byte(filteredOut), &filtered))
	require.Equal(t, 250, filtered.Totals.TotalTokens)
	require.Len(t, filtered.TopModels, 1)
	require.Equal(t, "anthropic", filtered.TopModels[0].Provider)
	require.Equal(t, "unavailable", filtered.CostAvailability)
	require.NotContains(t, filteredOut, "gpt-5.3-codex")
}

func TestViewUsageAnalyticsRuntimeTool_SanitizesAccountLimitSummaries(t *testing.T) {
	h, _, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Usage Account Project")
	cfg := &models.LLMConfig{
		Name:              "OpenAI OAuth Account",
		Provider:          models.ProviderOpenAI,
		Model:             "gpt-5.3-codex",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "oauth-secret-token",
		OAuthRefreshToken: "refresh-secret-token",
		OAuthAccountID:    "acct-secret-id",
	}
	require.NoError(t, llmConfigRepo.Create(ctx, cfg))
	used := 91.0
	window := 300
	reset := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, h.usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:             "openai",
		AccountID:            cfg.OAuthAccountID,
		AgentConfigID:        cfg.ID,
		PlanType:             "ChatGPT Pro",
		PrimaryLabel:         "5-hour session",
		PrimaryUsedPercent:   &used,
		PrimaryWindowMinutes: &window,
		PrimaryResetsAt:      &reset,
		RawJSON:              `{"account_id":"raw-provider-account","authorization":"Bearer raw-secret"}`,
		FetchedAt:            time.Now().UTC(),
	}))

	handler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModePlan, chatcontrol.SurfaceWeb)["view_usage_analytics"]
	out, err := handler(ctx, json.RawMessage(`{"range":"all","provider":"openai"}`))
	require.NoError(t, err)
	var got testUsageAnalyticsToolResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.AccountLimits, 1)
	require.Equal(t, "openai", got.AccountLimits[0].Provider)
	require.Equal(t, "ChatGPT Pro", got.AccountLimits[0].PlanType)
	require.Len(t, got.AccountLimits[0].Limits, 1)
	require.Equal(t, "5-hour session", got.AccountLimits[0].Limits[0].Label)
	require.Equal(t, "warning", got.AccountLimits[0].Limits[0].Status)
	for _, secret := range []string{cfg.ID, cfg.OAuthAccountID, cfg.OAuthAccessToken, cfg.OAuthRefreshToken, "raw-provider-account", "Bearer raw-secret"} {
		require.NotContains(t, out, secret)
	}
}

func TestViewUsageAnalyticsRuntimeTool_CapabilityRegistration(t *testing.T) {
	for _, mode := range []models.ChatMode{models.ChatModePlan, models.ChatModeOrchestrate} {
		defs := chatcontrol.ToolDefsForContext(mode, chatcontrol.SurfaceWeb, mode == models.ChatModeOrchestrate)
		found := false
		for _, def := range defs {
			if def.Name == "view_usage_analytics" {
				found = true
				break
			}
		}
		require.Truef(t, found, "view_usage_analytics definition missing in mode %s", mode)
	}
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Usage Capability Project")
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: project.ID, ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(context.Background(), "list_capabilities", nil)
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, out, "view_usage_analytics")

	taskThreadSummaries := filterTaskThreadCapabilitySummaries(chatcontrol.ListForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb), nil, false)
	found := false
	for _, summary := range taskThreadSummaries {
		if summary.Name == "view_usage_analytics" {
			found = true
			break
		}
	}
	require.True(t, found, "task-thread capabilities should include view_usage_analytics")
}

func TestCreateSwarmTaskRuntimeTool_StartFlagDoesNotDeferActiveSwarm(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Deferred Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm-deferred", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}
	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Plan export","prompt":"Plan export with workers","max_workers":3,"worker_isolation":"worktree","start_immediately":false}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	if !strings.Contains(out, "Planner starts when the swarm parent is Active.") {
		t.Fatalf("expected category-driven planner summary, got %q", out)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), ids[0], models.SwarmRolePlanner)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if planner == nil {
		t.Fatal("expected planner child for active runtime tool swarm")
	}
	if planner.Category != models.CategoryActive || planner.Status != models.StatusPending {
		t.Fatalf("planner not runnable: category=%s status=%s", planner.Category, planner.Status)
	}
}

func TestCreateSwarmTaskRuntimeTool_BacklogCategoryDefersPlanner(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Backlog Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm-backlog", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}

	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Backlog plan","prompt":"Plan later","category":"backlog","max_workers":2}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	if !strings.Contains(out, `(backlog)`) {
		t.Fatalf("expected backlog summary, got %q", out)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.Category != models.CategoryBacklog {
		t.Fatalf("parent category=%s, want backlog", parent.Category)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), ids[0], models.SwarmRolePlanner)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if planner != nil {
		t.Fatalf("expected backlog runtime swarm to defer planner, got %#v", planner)
	}
}

func TestCreateSwarmTaskRuntimeTool_PersistsGoalTriageReviewAndAssignments(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Swarm Tool Metadata Project")
	modelConfig := createAgent(t, llmConfigRepo, func(c *models.LLMConfig) {
		c.Name = "Explicit Swarm Model"
		c.IsDefault = false
	})
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	agentDef := &models.Agent{Name: "Swarm Reviewer", Key: "swarm-reviewer", Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agentDef))

	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm-metadata", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}
	payload, err := json.Marshal(map[string]any{
		"title":               "Metadata swarm",
		"prompt":              "Split this bug fix",
		"category":            "backlog",
		"goal":                "All tests pass",
		"priority":            4,
		"tag":                 "bug",
		"agent_id":            modelConfig.ID,
		"agent":               agentDef.Name,
		"reviewer_enabled":    false,
		"merger_enabled":      false,
		"merge_target_branch": "release/next",
	})
	require.NoError(t, err)

	out, err := createHandler(ctx, payload)
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(out)
	require.Len(t, ids, 1)
	parent, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, parent)
	require.Equal(t, models.CategoryBacklog, parent.Category)
	require.Equal(t, 4, parent.Priority)
	require.Equal(t, models.TagBug, parent.Tag)
	require.NotNil(t, parent.AgentID)
	require.Equal(t, modelConfig.ID, *parent.AgentID)
	require.NotNil(t, parent.AgentDefinitionID)
	require.Equal(t, agentDef.ID, *parent.AgentDefinitionID)
	require.Equal(t, "release/next", parent.MergeTargetBranch)
	cfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	require.False(t, cfg.ReviewerEnabled)
	require.False(t, cfg.MergerEnabled)
	goal, err := repository.NewTaskGoalRepo(db).GetByTaskID(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, goal)
	require.Equal(t, "All tests pass", goal.Objective)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)
}

func TestCreateSwarmTaskRuntimeTool_ChannelSurfaceUsesActiveProject(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	authorizedProject := createProject(t, h, "Email Authorized Swarm Project")
	foreignProject := createProject(t, h, "Email Foreign Swarm Project")

	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-email-swarm", ProjectID: authorizedProject.ID, Surface: chatcontrol.SurfaceEmail}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceEmail)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}

	payload := `{"title":"Email swarm","prompt":"Split this email work","project_id":"` + foreignProject.ID + `","start_immediately":false}`
	out, err := createHandler(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.ProjectID != authorizedProject.ID {
		t.Fatalf("channel swarm should use active project %s, got %s", authorizedProject.ID, parent.ProjectID)
	}
	foreignTasks, err := h.taskRepo.ListByProject(context.Background(), foreignProject.ID, "")
	if err != nil {
		t.Fatalf("list foreign tasks: %v", err)
	}
	if len(foreignTasks) != 0 {
		t.Fatalf("expected no tasks in foreign project, got %d", len(foreignTasks))
	}
}

func TestCreateSwarmTaskRuntimeTool_CreatesParentAndPlanner(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Swarm Tool Project")
	handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-swarm", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	createHandler := handlers["create_swarm_task"]
	if createHandler == nil {
		t.Fatal("create_swarm_task handler missing")
	}
	out, err := createHandler(context.Background(), json.RawMessage(`{"title":"Build export","prompt":"Build export with workers","max_workers":3,"worker_isolation":"worktree","start_immediately":true}`))
	if err != nil {
		t.Fatalf("create_swarm_task failed: %v", err)
	}
	ids := extractTaskIDsFromOutput(out)
	if len(ids) != 1 {
		t.Fatalf("expected one parent task id in output, got %q", out)
	}
	parent, err := h.taskRepo.GetByID(context.Background(), ids[0])
	if err != nil || parent == nil {
		t.Fatalf("parent not persisted: %v", err)
	}
	if parent.SwarmRole != models.SwarmRoleParent {
		t.Fatalf("expected swarm parent role, got %q", parent.SwarmRole)
	}
	planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner child not created: %v", err)
	}
}

func TestCreateTaskRuntimeTool_OmittedCategoryAutoStartActivatesAfterAttachmentConversion(t *testing.T) {
	h, _, llmConfigRepo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Runtime Attachment Deferral Project")
	modelConfig := createAgent(t, llmConfigRepo, func(c *models.LLMConfig) {
		c.AutoStartTasks = true
	})
	chatHostTask := createTask(t, h, project.ID, "runtime attachment host", func(tk *models.Task) {
		tk.Category = models.CategoryChat
	})
	exec := createExec(t, h, chatHostTask.ID, modelConfig.ID)

	fileName := "Screenshot 2026-07-10 at 9.49.00\u202fPM.png"
	sourceDir := filepath.Join(uploadsDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	sourcePath := filepath.Join(sourceDir, fileName)
	require.NoError(t, os.WriteFile(sourcePath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644))
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    fileName,
		FilePath:    sourcePath,
		MediaType:   "image/png",
		FileSize:    4,
	}))

	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: exec.ID, ProjectID: project.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	output, err := handlers["create_task"](ctx, json.RawMessage(`{"title":"Auto-start runtime task","prompt":"Use the screenshot"}`))
	require.NoError(t, err)
	ids := extractTaskIDsFromOutput(output)
	require.Len(t, ids, 1)

	created, err := h.taskRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, models.CategoryActive, created.Category)
	attachments, err := h.attachmentRepo.ListByTask(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	require.Equal(t, fileName, attachments[0].FileName)
	require.FileExists(t, attachments[0].FilePath)
}

// TestCreateTaskRuntimeTool_FailsLoudlyOnPersistenceFailure is the regression
// test for the phantom create_task bug: the runtime tool handler used to
// always return (summary, nil), so even if the direct task creation action failed to
// persist any task (empty project context, malformed input, or DB error) the
// model would receive isError=false and report a fake successful create_task
// to the user. The fix returns an error when no [TASK_ID:...] markers appear
// in the summary or when a referenced task ID cannot be verified in the
// current project's task store.
func TestCreateTaskRuntimeTool_FailsLoudlyOnPersistenceFailure(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := createProject(t, h, "Create Task Failure Project")
	input := json.RawMessage(`{"title":"Fix bug","prompt":"do it"}`)

	t.Run("empty project id", func(t *testing.T) {
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-empty-project", ProjectID: ""}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		createHandler := handlers["create_task"]
		if createHandler == nil {
			t.Fatal("create_task handler missing")
		}
		if _, err := createHandler(ctx, input); err == nil {
			t.Fatal("expected create_task with empty project_id to return an error")
		}
	})

	t.Run("system Memory Curator assignment", func(t *testing.T) {
		agentRepo := repository.NewAgentRepo(db)
		h.SetAgentRepo(agentRepo)
		memoryCurator := &models.Agent{
			Name:       "System: Memory Curator",
			Key:        models.AgentSystemKindMemoryCurator,
			SystemKind: models.AgentSystemKindMemoryCurator,
			Enabled:    true,
			// System identity must win even if persisted selectability drifts.
			SelectableAsPrimary: true,
			GeneratedStatus:     models.AgentStatusProtected,
		}
		if err := agentRepo.Create(ctx, memoryCurator); err != nil {
			t.Fatalf("create Memory Curator: %v", err)
		}
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-memory-curator", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		output, err := handlers["create_task"](ctx, json.RawMessage(`{"title":"Reconcile memory","prompt":"Update managed memory","agent":"Memory Curator"}`))
		if err == nil {
			t.Fatalf("expected create_task to reject system Memory Curator, got output %q", output)
		}
		if !strings.Contains(output, `Agent "Memory Curator" is not one unique enabled, selectable primary Agent definition`) {
			t.Fatalf("expected rejected Agent assignment in tool output, got %q", output)
		}
		tasks, listErr := h.taskRepo.ListByProject(ctx, project.ID, "")
		if listErr != nil {
			t.Fatalf("list project tasks: %v", listErr)
		}
		if len(tasks) != 0 {
			t.Fatalf("rejected Memory Curator assignment created fallback tasks: %#v", tasks)
		}
	})

	t.Run("summary without persisted task id", func(t *testing.T) {
		handlers := h.chatActionHandlers(streamingResponseParams{ExecID: "exec-db-failure", ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		createHandler := handlers["create_task"]
		if createHandler == nil {
			t.Fatal("create_task handler missing")
		}

		if _, err := h.taskRepo.GetByID(ctx, "sanity-check"); err != nil {
			t.Fatalf("task repo should work before closing db: %v", err)
		}
		h.execRepo = nil // Avoid best-effort execution-output updates after the DB is closed.
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}

		output, err := createHandler(ctx, input)
		if err == nil {
			t.Fatalf("expected create_task to fail when persistence fails, got nil error and output %q", output)
		}
		if !strings.Contains(output, "Failed to create") {
			t.Fatalf("expected failure summary in tool output, got %q", output)
		}
	})
}

// TestWebAPISwitchProject_IsInformationalOnly is the non-regression guard ensuring that
// the web/API switch_project tool never writes to any channel-specific persistence
// table (discord_user_projects, slack_user_projects, telegram_user_projects,
// email_sender_projects). The web/API path is informational: the frontend manages
// the active project_id, so the handler must not touch channel tables.
func TestWebAPISwitchProject_IsInformationalOnly(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()

	project1 := createProject(t, h, "Alpha")
	project2 := createProject(t, h, "Beta")
	_ = project1

	handlers := h.chatActionHandlers(
		streamingResponseParams{ExecID: "e1", ProjectID: project2.ID},
		nil,
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)

	switchHandler, ok := handlers["switch_project"]
	if !ok {
		t.Fatal("switch_project handler missing from web surface handlers")
	}

	result, err := switchHandler(ctx, json.RawMessage(`{"project":"Alpha"}`))
	if err != nil {
		t.Fatalf("switch_project returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "Alpha") {
		t.Fatalf("expected informational response mentioning Alpha, got: %q", result)
	}

	// Assert no channel-specific persistence rows were written.
	channelTables := []string{
		"discord_user_projects",
		"slack_user_projects",
		"telegram_user_projects",
		"email_sender_projects",
	}
	for _, table := range channelTables {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count(%s): %v", table, err)
		}
		if count != 0 {
			t.Errorf("web/API switch_project must not write to %s: found %d row(s)", table, count)
		}
	}
}

func TestGitHubOpenPullRequestRuntimeToolCreatesAndPersistsPR(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR Runtime", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Open PR", Prompt: "prompt", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/runtime-open-pr", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	var createdReq service.GitHubCreatePullRequestRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			if publishReq.Branch != task.WorktreeBranch {
				t.Fatalf("unexpected published branch %q", publishReq.Branch)
			}
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		getPullRequestFn: func(context.Context, *service.GitHubRepoRef, int) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 123, URL: "https://github.com/openvibely/openvibely/pull/123", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			createdReq = req
			return &service.GitHubPullRequest{Number: 123, URL: "https://github.com/openvibely/openvibely/pull/123", State: "open", HeadRef: req.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	})
	params := streamingResponseParams{ProjectID: project.ID}
	out, err := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_open_pull_request"](ctx, json.RawMessage(`{"task_id":"`+task.ID+`","pr_title":"Runtime PR","pr_body":"Runtime body","base":"develop","draft":true,"issue_number":77,"issue_url":"https://github.com/openvibely/openvibely/issues/77"}`))
	if err != nil {
		t.Fatalf("github_open_pull_request returned error: %v", err)
	}
	if !strings.Contains(out, `"created":true`) || !strings.Contains(out, `"pull_request"`) {
		t.Fatalf("expected created PR output, got %s", out)
	}
	if createdReq.Title != "Runtime PR" || createdReq.Body != "Runtime body" || createdReq.Base != "develop" || !createdReq.Draft || createdReq.Head != task.WorktreeBranch {
		t.Fatalf("unexpected create PR request: %#v", createdReq)
	}
	record, err := h.taskPullRequestRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("lookup task PR: %v", err)
	}
	if record == nil || record.PRNumber != 123 || record.IssueNumber == nil || *record.IssueNumber != 77 {
		t.Fatalf("unexpected persisted PR record: %#v", record)
	}

	titleOut, err := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_open_pull_request"](ctx, json.RawMessage(`{"title":"`+task.Title+`"}`))
	if err != nil {
		t.Fatalf("github_open_pull_request by title returned error: %v", err)
	}
	if !strings.Contains(titleOut, `"task_id":"`+task.ID+`"`) || !strings.Contains(titleOut, `"reused_existing_record":true`) {
		t.Fatalf("expected title selector to reuse task PR %s, got %s", task.ID, titleOut)
	}
}

func TestGitHubReplacePullRequestBranchRuntimeToolUsesLeaseGuard(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR Replacement", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Replace PR branch", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/runtime-replace-pr"}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	if err := h.taskPullRequestRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}

	var got service.GitHubReplaceBranchHeadRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, _, _ string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		getPullRequestFn: func(_ context.Context, _ *service.GitHubRepoRef, number int) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: number, HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		replaceBranchHeadFn: func(_ context.Context, _ *service.GitHubRepoRef, req service.GitHubReplaceBranchHeadRequest) error {
			got = req
			return nil
		},
	})
	expected := strings.Repeat("a", 40)
	params := streamingResponseParams{ProjectID: project.ID}
	handler := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)["github_replace_pull_request_branch"]
	if _, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`","expected_head_sha":"`+expected+`"}`)); err == nil || !strings.Contains(err.Error(), "confirm_history_rewrite") {
		t.Fatalf("expected explicit history rewrite confirmation error, got %v", err)
	}
	out, err := handler(ctx, json.RawMessage(`{"task_id":"`+task.ID+`","expected_head_sha":"`+expected+`","confirm_history_rewrite":true}`))
	if err != nil {
		t.Fatalf("github_replace_pull_request_branch returned error: %v", err)
	}
	if got.WorktreePath != task.WorktreePath || got.Branch != task.WorktreeBranch || got.ExpectedHead != expected {
		t.Fatalf("unexpected replacement request: %#v", got)
	}
	if !strings.Contains(out, `"replaced_branch":"`+task.WorktreeBranch+`"`) || !strings.Contains(out, `"expected_head_sha":"`+expected+`"`) {
		t.Fatalf("unexpected tool output: %s", out)
	}

	titleOut, err := handler(ctx, json.RawMessage(`{"title":"`+task.Title+`","expected_head_sha":"`+expected+`","confirm_history_rewrite":true}`))
	if err != nil {
		t.Fatalf("github_replace_pull_request_branch by title returned error: %v", err)
	}
	if !strings.Contains(titleOut, `"task_id":"`+task.ID+`"`) || !strings.Contains(titleOut, `"replaced_branch":"`+task.WorktreeBranch+`"`) {
		t.Fatalf("expected title selector to replace task PR branch %s, got %s", task.ID, titleOut)
	}
}

func TestGitHubPRRuntimeToolsShareTargetRejections(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub PR target", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create current project: %v", err)
	}
	otherProject := &models.Project{Name: "Other GitHub PR target", RepoURL: "https://github.com/example/other"}
	if err := h.projectSvc.Create(ctx, otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherTask := &models.Task{ProjectID: otherProject.ID, Title: "Cross-project PR task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := h.taskRepo.Create(ctx, otherTask); err != nil {
		t.Fatalf("create cross-project task: %v", err)
	}
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	params := streamingResponseParams{ProjectID: project.ID}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, action := range []struct {
		name  string
		input func(taskID string) json.RawMessage
	}{
		{name: "github_open_pull_request", input: func(taskID string) json.RawMessage {
			return json.RawMessage(`{"task_id":"` + taskID + `"}`)
		}},
		{name: "github_replace_pull_request_branch", input: func(taskID string) json.RawMessage {
			return json.RawMessage(`{"task_id":"` + taskID + `","confirm_history_rewrite":true}`)
		}},
	} {
		t.Run(action.name+"/missing_task", func(t *testing.T) {
			_, err := handlers[action.name](ctx, action.input("missing-task"))
			if err == nil || err.Error() != "task missing-task not found" {
				t.Fatalf("expected shared missing-task rejection, got %v", err)
			}
		})
		t.Run(action.name+"/cross_project_task", func(t *testing.T) {
			_, err := handlers[action.name](ctx, action.input(otherTask.ID))
			want := "task " + otherTask.ID + " belongs to a different project"
			if err == nil || err.Error() != want {
				t.Fatalf("expected shared cross-project rejection %q, got %v", want, err)
			}
		})
	}

	automationProject := &models.Project{Name: "Automation PR target"}
	if err := h.projectSvc.Create(ctx, automationProject); err != nil {
		t.Fatalf("create Automation project: %v", err)
	}
	automationTask := &models.Task{ProjectID: automationProject.ID, Title: "Automation PR task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := h.taskRepo.Create(ctx, automationTask); err != nil {
		t.Fatalf("create Automation task: %v", err)
	}
	automationCtx := service.WithAutomationContext(ctx, models.AutomationContext{ProjectID: automationProject.ID, OriginTask: true})
	automationHandlers := h.chatActionHandlers(streamingResponseParams{ProjectID: automationProject.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, action := range []struct {
		name  string
		input json.RawMessage
	}{
		{name: "github_open_pull_request", input: json.RawMessage(`{"task_id":"` + automationTask.ID + `"}`)},
		{name: "github_replace_pull_request_branch", input: json.RawMessage(`{"task_id":"` + automationTask.ID + `","confirm_history_rewrite":true}`)},
	} {
		t.Run(action.name+"/automation_repository_required", func(t *testing.T) {
			_, err := automationHandlers[action.name](automationCtx, action.input)
			if err == nil || err.Error() != "Automation GitHub runtime requires a project repository URL or local Git checkout" {
				t.Fatalf("expected shared Automation repository rejection, got %v", err)
			}
		})
	}

	_, err := handlers["github_replace_pull_request_branch"](ctx, json.RawMessage(`{"task_id":"missing-task"}`))
	if err == nil || err.Error() != "confirm_history_rewrite must be true to replace pull request branch history" {
		t.Fatalf("expected confirmation rejection before target resolution, got %v", err)
	}
}

func TestInteractiveGitHubRuntimeExplicitRepositoryUsesProjectEndpointByParsedHost(t *testing.T) {
	h, _, _, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	const enterpriseEndpoint = "https://github.example.com/api/v3"
	project := &models.Project{
		Name:    "Enterprise Chat runtime",
		RepoURL: "https://github.example.com/acme/widgets.git",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	providerCalls := 0
	h.SetGitHubService(&fakeGitHubService{
		globalAPIEndpoint: enterpriseEndpoint,
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			if strings.TrimSpace(repoPath) != "" {
				t.Fatalf("explicit repo_url unexpectedly used local path %q", repoPath)
			}
			parsed, err := service.ParseGitHubRepoURL(repoURL)
			if err != nil {
				return nil, err
			}
			return &parsed, nil
		},
		createIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			providerCalls++
			if repo.FullName != "acme/sibling" || repo.APIBaseURL != enterpriseEndpoint {
				t.Fatalf("unexpected resolved repository: %+v", repo)
			}
			return &service.GitHubIssue{Number: 1, Title: req.Title}, nil
		},
	})
	handlers := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	if _, err := handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Sibling issue","repo_url":"git@github.example.com:acme/sibling.git"}`)); err != nil {
		t.Fatalf("same-host Enterprise repository failed: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls)
	}

	if _, err := handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Unsafe issue","repo_url":"https://github.com/acme/widgets.git"}`)); err == nil || !strings.Contains(err.Error(), "repository host") {
		t.Fatalf("expected cross-host repository rejection, got %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("cross-host request reached provider; calls = %d", providerCalls)
	}
}

func TestGitHubAuthAndInboxRuntimeToolsUseConfiguredRepository(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "GitHub Inbox", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	authRepo := repository.NewGitHubAuthRepo(db)
	h.SetGitHubAuthRepo(authRepo)
	if err := authRepo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "Dev-Bot", Enabled: true}); err != nil {
		t.Fatalf("configure github inbox: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Alice", Permission: "approve"}); err != nil {
		t.Fatalf("configure authorized actor: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Dev-Bot", Permission: "triage"}); err != nil {
		t.Fatalf("configure dev-bot authorized actor: %v", err)
	}
	var sawMyAssignedIssues bool
	var sawAssignedIssuesWithPRs bool
	var myAssignedIssueCalls int
	var assignedIssueCalls int
	var assignedIssuesWithPRCalls int
	var createdRepo, commentedRepo, labeledRepo, closedRepo string
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			switch repoURL {
			case project.RepoURL:
				return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
			case "https://github.com/example/other":
				if strings.TrimSpace(repoPath) != "" {
					t.Fatalf("expected explicit repo_url lookup to avoid local repo path, got %q", repoPath)
				}
				return &service.GitHubRepoRef{Owner: "example", Name: "other", FullName: "example/other", HTMLURL: "https://github.com/example/other"}, nil
			default:
				t.Fatalf("unexpected repo URL %q", repoURL)
				return nil, nil
			}
		},
		listMyAssignedIssuesFn: func(_ context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error) {
			sawMyAssignedIssues = true
			myAssignedIssueCalls++
			if repo.Owner == "example" && repo.Name == "other" {
				return &service.GitHubAuthenticatedUser{Login: "channel-user", Source: service.GitHubAuthModePAT}, []service.GitHubIssue{{Number: 7, URL: "https://github.com/example/other/issues/7", Title: "Explicit URL", State: "open", Assignees: []string{"channel-user"}}}, nil
			}
			return &service.GitHubAuthenticatedUser{Login: "channel-user", Source: service.GitHubAuthModePAT}, []service.GitHubIssue{{Number: 5, URL: "https://github.com/openvibely/openvibely/issues/5", Title: "Testing", State: "open", Assignees: []string{"channel-user"}}}, nil
		},
		listAssignedIssuesFn: func(_ context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssue, error) {
			assignedIssueCalls++
			if assignee != "Dev-Bot" {
				t.Fatalf("expected explicit assignee Dev-Bot, got %q", assignee)
			}
			if repo.Owner == "example" && repo.Name == "other" {
				return []service.GitHubIssue{{Number: 8, URL: "https://github.com/example/other/issues/8", Title: "Explicit assignee URL", State: "open", Assignees: []string{"dev-bot"}}}, nil
			}
			return []service.GitHubIssue{{Number: 6, URL: "https://github.com/openvibely/openvibely/issues/6", Title: "Override", State: "open", Assignees: []string{"dev-bot"}}}, nil
		},
		listAssignedIssuesPRFn: func(_ context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssueWithPullRequest, error) {
			sawAssignedIssuesWithPRs = true
			assignedIssuesWithPRCalls++
			if assignee != "Dev-Bot" || repo.FullName != "openvibely/openvibely" {
				t.Fatalf("unexpected PR-filtered assigned issue request repo=%q assignee=%q", repo.FullName, assignee)
			}
			return []service.GitHubIssueWithPullRequest{
				{Issue: service.GitHubIssue{Number: 10, Title: "First PR issue"}, PullRequest: service.GitHubPullRequest{Number: 110}},
				{Issue: service.GitHubIssue{Number: 20, Title: "Second PR issue"}, PullRequest: service.GitHubPullRequest{Number: 120}},
			}, nil
		},
		createIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, req service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			createdRepo = repo.FullName
			return &service.GitHubIssue{Number: 9, URL: "https://github.com/example/other/issues/9", Title: req.Title, State: "open", Labels: req.Labels, Assignees: req.Assignees}, nil
		},
		commentOnIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, issueNumber int, body string) error {
			commentedRepo = repo.FullName
			if issueNumber != 9 || body != "Looks good" {
				t.Fatalf("unexpected comment input issue=%d body=%q", issueNumber, body)
			}
			return nil
		},
		addLabelsToIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, issueNumber int, labels []string) error {
			labeledRepo = repo.FullName
			if issueNumber != 9 || len(labels) != 1 || labels[0] != "approved" {
				t.Fatalf("unexpected labels input issue=%d labels=%v", issueNumber, labels)
			}
			return nil
		},
		closeIssueFn: func(_ context.Context, repo *service.GitHubRepoRef, issueNumber int) error {
			closedRepo = repo.FullName
			if issueNumber != 9 {
				t.Fatalf("unexpected close input issue=%d", issueNumber)
			}
			return nil
		},
	})

	params := streamingResponseParams{ProjectID: project.ID}
	handlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	out, err := handlers["github_get_project_inbox"](ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("github_get_project_inbox returned error: %v", err)
	}
	if !strings.Contains(out, `"configured":true`) || !strings.Contains(out, `"alice"`) || !strings.Contains(out, `"dev-bot"`) || !strings.Contains(out, `"legacy_inbox"`) || !strings.Contains(out, `"github_login":"dev-bot"`) {
		t.Fatalf("expected authorized-user assignee output with legacy inbox metadata, got %s", out)
	}
	out, err = handlers["github_is_actor_authorized"](ctx, json.RawMessage(`{"github_login":"ALICE"}`))
	if err != nil {
		t.Fatalf("github_is_actor_authorized returned error: %v", err)
	}
	if !strings.Contains(out, `"authorized":true`) || !strings.Contains(out, `"github_login":"alice"`) {
		t.Fatalf("expected authorized actor output, got %s", out)
	}
	out, err = handlers["github_is_actor_authorized"](ctx, json.RawMessage(`{"github_login":"bob"}`))
	if err != nil {
		t.Fatalf("github_is_actor_authorized unknown returned error: %v", err)
	}
	if !strings.Contains(out, `"authorized":false`) {
		t.Fatalf("expected deny-by-default output for unknown actor, got %s", out)
	}
	out, err = handlers["github_list_my_assigned_issues"](ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("github_list_my_assigned_issues returned error: %v", err)
	}
	if !sawMyAssignedIssues || !strings.Contains(out, `"login":"channel-user"`) || !strings.Contains(out, `"number":5`) || !strings.Contains(out, `"returned":1`) {
		t.Fatalf("expected authenticated assigned issues output, saw=%v out=%s", sawMyAssignedIssues, out)
	}
	out, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"Dev-Bot"}`))
	if err != nil {
		t.Fatalf("github_list_assigned_issues returned error: %v", err)
	}
	if !strings.Contains(out, `"assignee":"dev-bot"`) || !strings.Contains(out, `"number":6`) || !strings.Contains(out, `"returned":1`) {
		t.Fatalf("expected explicit-assignee assigned issues output, got %s", out)
	}
	out, err = handlers["github_list_assigned_issues_with_prs"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","limit":1,"offset":1}`))
	if err != nil {
		t.Fatalf("github_list_assigned_issues_with_prs returned error: %v", err)
	}
	if !sawAssignedIssuesWithPRs || !strings.Contains(out, `"returned":1`) || !strings.Contains(out, `"total":2`) || !strings.Contains(out, `"offset":1`) || !strings.Contains(out, `"next_offset":0`) || !strings.Contains(out, `"Number":20`) || strings.Contains(out, `"Number":10`) {
		t.Fatalf("expected paginated PR-filtered assigned issues output, saw=%v out=%s", sawAssignedIssuesWithPRs, out)
	}
	apiHandlers := h.chatActionHandlers(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	apiOut, apiErr := apiHandlers["github_list_assigned_issues_with_prs"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","limit":1,"offset":1}`))
	if apiErr != nil {
		t.Fatalf("API github_list_assigned_issues_with_prs returned error: %v", apiErr)
	}
	if !strings.Contains(apiOut, `"returned":1`) || !strings.Contains(apiOut, `"total":2`) || !strings.Contains(apiOut, `"offset":1`) || !strings.Contains(apiOut, `"next_offset":0`) || !strings.Contains(apiOut, `"Number":20`) || strings.Contains(apiOut, `"Number":10`) {
		t.Fatalf("expected API paginated PR-filtered assigned issues output: %s", apiOut)
	}
	callsBeforeInvalid := assignedIssuesWithPRCalls
	if _, err = handlers["github_list_assigned_issues_with_prs"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","Limit":0}`)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
		t.Fatalf("Web case-variant invalid pagination error=%v, want validation error", err)
	}
	if _, err = apiHandlers["github_list_assigned_issues_with_prs"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","LIMIT":0}`)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
		t.Fatalf("API case-variant invalid pagination error=%v, want validation error", err)
	}
	if assignedIssuesWithPRCalls != callsBeforeInvalid {
		t.Fatalf("Web/API provider calls after invalid pagination=%d, want %d", assignedIssuesWithPRCalls, callsBeforeInvalid)
	}

	callsBeforeInvalidMyAssigned := myAssignedIssueCalls
	callsBeforeInvalidAssigned := assignedIssueCalls
	for _, surface := range []struct {
		name     string
		handlers map[string]chatcontrol.RuntimeActionHandler
	}{
		{name: "Web", handlers: handlers},
		{name: "API", handlers: apiHandlers},
	} {
		for _, input := range []string{
			`{"limit":0}`,
			`{"Limit":0}`,
			`{"LIMIT":0}`,
			`{"limit":101}`,
			`{"offset":-1}`,
			`{"Offset":-1}`,
		} {
			if _, err := surface.handlers["github_list_my_assigned_issues"](ctx, json.RawMessage(input)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
				t.Fatalf("%s my-assigned invalid pagination input %s error=%v, want validation error", surface.name, input, err)
			}
			assignedInput := strings.TrimSuffix(input, "}") + `,"assignee":"Dev-Bot"}`
			if _, err := surface.handlers["github_list_assigned_issues"](ctx, json.RawMessage(assignedInput)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
				t.Fatalf("%s assigned invalid pagination input %s error=%v, want validation error", surface.name, assignedInput, err)
			}
		}
	}
	if myAssignedIssueCalls != callsBeforeInvalidMyAssigned || assignedIssueCalls != callsBeforeInvalidAssigned {
		t.Fatalf("Web/API ordinary provider calls after invalid pagination=%d/%d, want %d/%d", myAssignedIssueCalls, assignedIssueCalls, callsBeforeInvalidMyAssigned, callsBeforeInvalidAssigned)
	}

	if _, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"mallory"}`)); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected unauthorized explicit-assignee error, got %v", err)
	}
	out, err = handlers["github_list_my_assigned_issues"](ctx, json.RawMessage(`{"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_list_my_assigned_issues with repo_url returned error: %v", err)
	}
	if !strings.Contains(out, `"number":7`) || !strings.Contains(out, `"https://github.com/example/other/issues/7"`) {
		t.Fatalf("expected explicit repo_url my-assigned issues output, got %s", out)
	}
	out, err = handlers["github_list_assigned_issues"](ctx, json.RawMessage(`{"assignee":"Dev-Bot","repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_list_assigned_issues with repo_url returned error: %v", err)
	}
	if !strings.Contains(out, `"number":8`) || !strings.Contains(out, `"https://github.com/example/other/issues/8"`) {
		t.Fatalf("expected explicit repo_url assigned issues output, got %s", out)
	}
	out, err = handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"URL issue","body":"Created by URL","labels":["bug"],"assignees":["dev-bot"],"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_create_issue with repo_url returned error: %v", err)
	}
	if createdRepo != "example/other" || !strings.Contains(out, `"Number":9`) || !strings.Contains(out, `"https://github.com/example/other/issues/9"`) {
		t.Fatalf("expected explicit repo_url create output repo=%q out=%s", createdRepo, out)
	}
	out, err = handlers["github_comment_on_issue"](ctx, json.RawMessage(`{"issue_number":9,"body":"Looks good","repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_comment_on_issue with repo_url returned error: %v", err)
	}
	if commentedRepo != "example/other" || !strings.Contains(out, `"issue_number":9`) {
		t.Fatalf("expected explicit repo_url comment output repo=%q out=%s", commentedRepo, out)
	}
	out, err = handlers["github_add_issue_labels"](ctx, json.RawMessage(`{"issue_number":9,"labels":["approved"],"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_add_issue_labels with repo_url returned error: %v", err)
	}
	if labeledRepo != "example/other" || !strings.Contains(out, `"labels":["approved"]`) {
		t.Fatalf("expected explicit repo_url label output repo=%q out=%s", labeledRepo, out)
	}
	out, err = handlers["github_close_issue"](ctx, json.RawMessage(`{"issue_number":9,"repo_url":"https://github.com/example/other"}`))
	if err != nil {
		t.Fatalf("github_close_issue with repo_url returned error: %v", err)
	}
	if closedRepo != "example/other" || !strings.Contains(out, `"state":"closed"`) {
		t.Fatalf("expected explicit repo_url close output repo=%q out=%s", closedRepo, out)
	}
}

func TestWebChatCreateProjectRuntimeCreatesGitHubBackedProject(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	current := createProject(t, h, "Current Runtime Project")
	h.SetGitHubService(&fakeGitHubService{cloneFn: func(ctx context.Context, projectID, repoURL string) (string, string, error) {
		return "/repos/" + projectID, "https://github.com/acme/runtime", nil
	}})

	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ProjectID: current.ID, ChatMode: models.ChatModeOrchestrate},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true),
		models.ChatModeOrchestrate,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "create_project", json.RawMessage(`{"name":"Created From Chat","repo_url":"https://github.com/acme/runtime","switch_after_create":true}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)

	var resp struct {
		OK              bool   `json:"ok"`
		ProjectID       string `json:"project_id"`
		Name            string `json:"name"`
		RepoURL         string `json:"repo_url"`
		RepoPathPresent bool   `json:"repo_path_present"`
		Switched        bool   `json:"switched"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.Equal(t, "Created From Chat", resp.Name)
	require.Equal(t, "https://github.com/acme/runtime", resp.RepoURL)
	require.True(t, resp.RepoPathPresent)
	require.False(t, resp.Switched, "web/API Chat has no persisted project switch callback")

	projectRepo := repository.NewProjectRepo(db)
	created, err := projectRepo.GetByID(ctx, resp.ProjectID)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, "/repos/"+resp.ProjectID, created.RepoPath)
}

func TestWebChatCreateProjectRuntimePlanModeBlocked(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ChatMode: models.ChatModePlan},
		nil,
		chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true),
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "create_project", json.RawMessage(`{"name":"Nope","repo_url":"https://github.com/acme/nope"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, out, "not available")
}

func TestCreateAgentRuntimeTool_WebAPICreatesProjectScopedAgentAndMaterializes(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetAgentSkillRoot(t.TempDir())
	project := createProject(t, h, "Runtime Agent Project")
	project.RepoPath = t.TempDir()
	require.NoError(t, h.projectSvc.Update(ctx, project))

	model := &models.LLMConfig{Name: "Runtime Agent Model", Provider: models.ProviderOpenAI, Model: "runtime-agent-model", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, llmConfigRepo.Create(ctx, model))

	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceWeb, chatcontrol.SurfaceAPI} {
		t.Run(string(surface), func(t *testing.T) {
			name := "Accessibility Runtime Reviewer " + string(surface)
			handler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, surface)["create_agent"]
			require.NotNil(t, handler)

			out, err := handler(ctx, json.RawMessage(fmt.Sprintf(`{"name":%q,"description":"Reviews docs and UI accessibility.","system_prompt":"Review accessibility evidence with concise recommendations.","model":"Runtime Agent Model","tools":["Read","Grep"],"scoped_files":[{"directory":"docs","permissions":["read"]}],"scope":"project"}`, name)))
			require.NoError(t, err, out)
			require.Contains(t, out, `"ok":true`)

			stored, err := agentRepo.GetUniqueSelectableByName(ctx, name)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, project.ID, stored.ProjectID)
			require.Equal(t, models.AgentScopeProject, stored.Scope)
			require.True(t, stored.Enabled)
			require.True(t, stored.SelectableAsPrimary)
			require.Equal(t, "runtime-agent-model", stored.Model)
			require.Contains(t, stored.Tools, models.AgentToolScopedFiles)
			require.Equal(t, []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read"}}}, stored.ToolConfig.ScopedFiles)

			declPath := filepath.Join(project.RepoPath, ".openvibely", "agents", stored.Key, "SKILLS.md")
			body, readErr := os.ReadFile(declPath)
			require.NoError(t, readErr)
			require.Contains(t, string(body), name)

			updateHandler := h.chatActionHandlers(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, surface)["update_agent"]
			require.NotNil(t, updateHandler)
			out, err = updateHandler(ctx, json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"system_prompt":"Tightened prompt.","model":"inherit","tools":["Read"],"selectable_as_primary":false}`, stored.ID)))
			require.NoError(t, err, out)
			require.Contains(t, out, `"ok":true`)

			updated, err := agentRepo.GetByID(ctx, stored.ID)
			require.NoError(t, err)
			require.Equal(t, "Tightened prompt.", updated.SystemPrompt)
			require.Equal(t, "inherit", updated.Model)
			require.Equal(t, []string{"Read"}, updated.Tools)
			require.False(t, updated.SelectableAsPrimary)
			body, readErr = os.ReadFile(declPath)
			require.NoError(t, readErr)
			require.Contains(t, string(body), "Tightened prompt.")
			require.NotContains(t, string(body), "Bash")
		})
	}
}

func TestCreateAgentRuntimeTool_PlanModeDoesNotExposeWriteAction(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	defs := chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true)
	for _, def := range defs {
		require.NotEqual(t, "create_agent", def.Name)
		require.NotEqual(t, "update_agent", def.Name)
	}
	rt := h.buildChatActionToolRuntimeFromDefs(
		streamingResponseParams{ChatMode: models.ChatModePlan},
		nil,
		defs,
		models.ChatModePlan,
		chatcontrol.SurfaceWeb,
	)
	out, handled, isErr, err := rt.Executor(ctx, "create_agent", json.RawMessage(`{"name":"Nope","system_prompt":"No write in plan."}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, out, "not available")

	out, handled, isErr, err = rt.Executor(ctx, "update_agent", json.RawMessage(`{"agent_id":"nope","description":"No write in plan."}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	require.Contains(t, out, "not available")
}
