package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestChannelChatIngressUsesCompactSelectionAndSelectedDetail(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, db, repo, "Channel Text Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	task := &models.Task{ID: "channel-text-task", Title: "Channel text", Category: models.CategoryChat, Status: models.StatusPending}
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "slack",
		ProjectID:     "project-1",
		Message:       "hello channel",
		Source:        models.TaskOriginSlack,
		LLMConfigRepo: repo,
		TaskRepo:      repository.NewTaskRepo(db, nil),
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: task,
			CreateDurableFirstTurn: func(_ context.Context, task *models.Task, execution *models.Execution, _ []models.ChatAttachment) (bool, error) {
				execution.ID = "channel-text-execution"
				execution.TaskID = task.ID
				return false, nil
			},
			ChannelChatRunner: func(_ context.Context, request ChannelChatRunRequest) {
				runnerRequest = request
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}

	if runnerRequest.Agent.ID != model.ID {
		t.Fatalf("runner selected model ID = %q, want %q", runnerRequest.Agent.ID, model.ID)
	}
	if runnerRequest.Agent.APIKey != "channel-api-key" || runnerRequest.Agent.BaseURL != "https://channel.example/v1" ||
		runnerRequest.Agent.ExtraBodyJSON == "" || runnerRequest.Agent.CustomAuthStateJSON == "" || runnerRequest.Agent.MixtureConfigJSON == "" {
		t.Fatalf("runner did not receive full selected model configuration: %#v", runnerRequest.Agent)
	}
	for _, want := range []string{model.ID, model.Name, model.Model, string(model.Provider), "(default)"} {
		if !strings.Contains(runnerRequest.SystemContext, want) {
			t.Fatalf("runner context missing %q: %s", want, runnerRequest.SystemContext)
		}
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 2 {
		t.Fatalf("agent_configs statements = %#v, want one compact selection and one selected detail query", modelStatements)
	}
	assertChannelCompactStatement(t, modelStatements)
}

func TestChannelChatIngressQueuesUsingCompactModelIDWithoutHydration(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, db, repo, "Channel Queue Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
	counter.SetEnabled(true)

	var queued *models.ThreadInput
	runnerCalled := false
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "discord",
		ProjectID:     "project-1",
		Message:       "queue this",
		Source:        models.TaskOriginDiscord,
		LLMConfigRepo: repo,
		FindActiveExecution: func(context.Context, string) (*models.Execution, error) {
			return &models.Execution{ID: "active-channel-execution"}, nil
		},
		CreateQueuedInput: func(_ context.Context, input *models.ThreadInput) (bool, error) {
			queued = input
			return false, nil
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			ChannelChatRunner: func(_ context.Context, _ ChannelChatRunRequest) {
				runnerCalled = true
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}
	if runnerCalled {
		t.Fatal("queued channel message invoked the provider runner")
	}
	if queued == nil || queued.AgentConfigID != model.ID {
		t.Fatalf("queued input = %#v, want selected compact model ID %q", queued, model.ID)
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 1 {
		t.Fatalf("agent_configs statements = %#v, want one compact selection query", modelStatements)
	}
	assertChannelCompactStatement(t, modelStatements)
}

func TestChannelChatIngressImageSelectionExcludesLegacyAnthropicCLI(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	legacy := &models.LLMConfig{
		Name:       "Legacy Anthropic CLI",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-cli",
		AuthMethod: models.AuthMethodCLI,
	}
	vision := seedChannelRichModel(t, ctx, db, repo, "Anthropic Vision", models.ProviderAnthropic, models.AuthMethodAPIKey, true)
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy model: %v", err)
	}
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "telegram",
		ProjectID:     "project-1",
		Message:       "inspect the image",
		Source:        models.TaskOriginTelegram,
		LLMConfigRepo: repo,
		TaskRepo:      repository.NewTaskRepo(db, nil),
		DownloadAttachments: func(context.Context) (channelChatIngressDownloadResult, error) {
			return channelChatIngressDownloadResult{ImageAttachments: []models.Attachment{{FileName: "image.png", MediaType: "image/png"}}}, nil
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: &models.Task{ID: "channel-image-task", Title: "Channel image", Category: models.CategoryChat, Status: models.StatusPending},
			CreateDurableFirstTurn: func(_ context.Context, _ *models.Task, execution *models.Execution, _ []models.ChatAttachment) (bool, error) {
				execution.ID = "channel-image-execution"
				return false, nil
			},
			ChannelChatRunner: func(_ context.Context, request ChannelChatRunRequest) {
				runnerRequest = request
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}
	if runnerRequest.Agent.ID != vision.ID {
		t.Fatalf("image runner selected model ID = %q, want vision model %q", runnerRequest.Agent.ID, vision.ID)
	}
	if runnerRequest.Agent.APIKey != "channel-api-key" || len(runnerRequest.ImageAttachments) != 1 {
		t.Fatalf("image runner did not receive hydrated vision model and image: %#v", runnerRequest)
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 2 {
		t.Fatalf("agent_configs statements = %#v, want compact vision selection plus selected detail query", modelStatements)
	}
	assertChannelVisionCompactStatement(t, modelStatements)
}

func TestChannelChatAgentSelectionWithNoModelsUsesCompactQuery(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	counter.SetEnabled(true)

	if _, err := selectChannelChatAgentOptions(ctx, repo, "hello", false); err == nil || err.Error() != "no agents configured" {
		t.Fatalf("selection error = %v, want no agents configured", err)
	}
	counter.SetEnabled(false)
	statements := channelAgentConfigStatements(counter.Statements())
	if len(statements) != 1 {
		t.Fatalf("agent_configs statements = %#v, want one compact query", statements)
	}
	assertChannelCompactStatement(t, statements)
}

func BenchmarkChannelModelLoads(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	seedChannelRichModels(b, ctx, db, repo, 50)

	b.Run("FullListTwice", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			result := SelectLLM(AnalyzeComplexity("hello channel"), configs)
			if result == nil || result.LLMConfig == nil {
				b.Fatal("full selection returned no model")
			}
			contextConfigs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if got := BuildModelContextString(contextConfigs); got == "" {
				b.Fatal("full context was empty")
			}
		}
	})

	b.Run("CompactSelectionSelectedDetail", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			selection, err := selectChannelChatAgentOptions(ctx, repo, "hello channel", false)
			if err != nil {
				b.Fatal(err)
			}
			selected, err := hydrateSelectedChannelChatAgent(ctx, repo, selection)
			if err != nil {
				b.Fatal(err)
			}
			if selected.APIKey == "" || BuildModelContextString(selection.AvailableModels) == "" {
				b.Fatal("compact path did not retain selected detail and context")
			}
		}
	})
}

func TestChannelModelLoadingProjectionMeetsPerformanceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped channel model-loading performance guard in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	seedChannelRichModels(t, ctx, db, repo, 50)

	full := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || BuildModelContextString(configs) == "" {
				b.Fatal("full selection fixture returned an invalid catalog")
			}
			contextConfigs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if BuildModelContextString(contextConfigs) == "" {
				b.Fatal("full context fixture returned an empty catalog")
			}
		}
	})
	compact := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListChatSelectionOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatal("compact selection fixture returned an invalid catalog")
			}
			selected, err := repo.GetByID(ctx, configs[0].ID)
			if err != nil {
				b.Fatal(err)
			}
			if selected == nil || selected.APIKey == "" || BuildModelContextString(configs) == "" {
				b.Fatal("compact path did not retain selected detail and context")
			}
		}
	})

	t.Logf("full catalog twice: %d ns/op, %d B/op, %d allocs/op", full.NsPerOp(), full.AllocedBytesPerOp(), full.AllocsPerOp())
	t.Logf("compact selection + selected detail: %d ns/op, %d B/op, %d allocs/op", compact.NsPerOp(), compact.AllocedBytesPerOp(), compact.AllocsPerOp())
	if testing.CoverMode() != "" {
		return
	}
	if compact.NsPerOp() > (200*1000) || compact.AllocedBytesPerOp() > 300*1024 {
		t.Fatalf("compact channel model loading exceeded budget: %d ns/op, %d B/op", compact.NsPerOp(), compact.AllocedBytesPerOp())
	}
	if full.NsPerOp()/compact.NsPerOp() < 50 {
		t.Fatalf("compact channel model loading latency improvement = %.1fx, want at least 50x", float64(full.NsPerOp())/float64(compact.NsPerOp()))
	}
	if full.AllocedBytesPerOp()/compact.AllocedBytesPerOp() < 40 {
		t.Fatalf("compact channel model loading allocation improvement = %.1fx, want at least 40x", float64(full.AllocedBytesPerOp())/float64(compact.AllocedBytesPerOp()))
	}
}

func seedChannelRichModel(tb testing.TB, ctx context.Context, db *sql.DB, repo *repository.LLMConfigRepo, name string, provider models.LLMProvider, authMethod models.AuthMethod, isDefault bool) *models.LLMConfig {
	tb.Helper()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		tb.Fatalf("clear model configs: %v", err)
	}
	largePayload := strings.Repeat("x", 56*1024)
	model := &models.LLMConfig{
		Name:                 name,
		Provider:             provider,
		Model:                "rich-channel-model",
		AuthMethod:           authMethod,
		APIKey:               "channel-api-key",
		OAuthAccessToken:     "channel-oauth-token",
		OAuthRefreshToken:    "channel-refresh-token",
		OAuthClientID:        "channel-client-id",
		OAuthClientSecret:    "channel-client-secret",
		BaseURL:              "https://channel.example/v1",
		ExtraBodyJSON:        `{"large":"` + largePayload + `"}`,
		CustomAuthConfigJSON: `{"secret":"channel-custom-auth"}`,
		CustomAuthStateJSON:  `{"token":"channel-custom-state"}`,
		MixtureConfigJSON:    `{"large":"` + largePayload + `"}`,
		IsDefault:            isDefault,
	}
	if err := repo.Create(ctx, model); err != nil {
		tb.Fatalf("create rich model: %v", err)
	}
	return model
}

func seedChannelRichModels(tb testing.TB, ctx context.Context, db *sql.DB, repo *repository.LLMConfigRepo, count int) {
	tb.Helper()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		tb.Fatalf("clear model configs: %v", err)
	}
	largePayload := strings.Repeat("x", 56*1024)
	for i := 0; i < count; i++ {
		model := &models.LLMConfig{
			Name:                 fmt.Sprintf("Channel Rich %02d", i),
			Provider:             models.ProviderOpenAICompatible,
			Model:                fmt.Sprintf("rich-channel-model-%02d", i),
			AuthMethod:           models.AuthMethodAPIKey,
			APIKey:               fmt.Sprintf("channel-api-key-%02d", i),
			BaseURL:              "https://channel.example/v1",
			ExtraBodyJSON:        `{"large":"` + largePayload + `"}`,
			CustomAuthConfigJSON: `{"secret":"channel-custom-auth"}`,
			CustomAuthStateJSON:  `{"token":"channel-custom-state"}`,
			MixtureConfigJSON:    `{"large":"` + largePayload + `"}`,
			IsDefault:            i == 0,
		}
		if err := repo.Create(ctx, model); err != nil {
			tb.Fatalf("create rich model %d: %v", i, err)
		}
	}
}

func channelAgentConfigStatements(statements []string) []string {
	var modelStatements []string
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "from agent_configs") {
			modelStatements = append(modelStatements, normalized)
		}
	}
	return modelStatements
}

func assertChannelCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	compact := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if projection == "select id, name, provider, model, is_default" {
			compact++
			continue
		}
		if !strings.Contains(statement, "where id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if compact != 1 {
		t.Fatalf("compact chat selection query count = %d, statements = %#v", compact, statements)
	}
}

func assertChannelVisionCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	vision := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if strings.HasPrefix(projection, "select id, name, provider, model, auth_method, is_default,") {
			vision++
			for _, forbidden := range []string{
				"oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url",
				"base_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json",
			} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("vision compact query selected forbidden column %q: %s", forbidden, statement)
				}
			}
			continue
		}
		if !strings.Contains(statement, "where id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if vision != 1 {
		t.Fatalf("compact vision selection query count = %d, statements = %#v", vision, statements)
	}
}
