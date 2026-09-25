package repository

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func seedLargeCustomProviderModelConfigs(tb testing.TB, ctx context.Context, repo *LLMConfigRepo, count int, maxWorkers ...int) {
	tb.Helper()
	largeBody := strings.Repeat("x", 64*1024)
	workerLimit := 0
	if len(maxWorkers) > 0 {
		workerLimit = maxWorkers[0]
	}
	for i := 0; i < count; i++ {
		cfg := &models.LLMConfig{
			Name:                 fmt.Sprintf("Large Custom %02d", i),
			Provider:             models.ProviderOpenAICompatible,
			AuthMethod:           models.AuthMethodOAuth,
			Model:                "custom-model",
			APIKey:               "secret-key",
			OAuthAccessToken:     "secret-token",
			OAuthRefreshToken:    "secret-refresh",
			OAuthClientSecret:    "secret-client",
			BaseURL:              "https://example.com/v1/",
			Transport:            "chat_completions",
			PresetSlug:           "custom",
			ExtraHeadersJSON:     `{"secret":"header"}`,
			ExtraBodyJSON:        largeBody,
			CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
			CustomAuthStateJSON:  `{"token":"secret"}`,
			MixtureConfigJSON:    `{"large":"` + largeBody + `"}`,
			MaxWorkers:           workerLimit,
		}
		if err := repo.Create(ctx, cfg); err != nil {
			tb.Fatalf("Create large model config %d: %v", i, err)
		}
	}
}

func TestLLMConfigRepoRejectsBlankRunnableModelSlug(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	tests := []struct {
		name     string
		provider models.LLMProvider
	}{
		{name: "anthropic", provider: models.ProviderAnthropic},
		{name: "openai", provider: models.ProviderOpenAI},
		{name: "openai compatible", provider: models.ProviderOpenAICompatible},
		{name: "ollama", provider: models.ProviderOllama},
	}
	for _, tt := range tests {
		t.Run(tt.name+" create", func(t *testing.T) {
			before, err := repo.List(ctx)
			if err != nil {
				t.Fatalf("list before create: %v", err)
			}
			cfg := &models.LLMConfig{Name: "Blank " + tt.name, Provider: tt.provider, AuthMethod: models.AuthMethodAPIKey, Model: " \t\n "}
			if err := repo.Create(ctx, cfg); err != ErrLLMConfigModelRequired {
				t.Fatalf("expected ErrLLMConfigModelRequired, got %v", err)
			}
			after, err := repo.List(ctx)
			if err != nil {
				t.Fatalf("list after create: %v", err)
			}
			if len(after) != len(before) {
				t.Fatalf("blank model create mutated rows: before=%d after=%d", len(before), len(after))
			}
		})
		t.Run(tt.name+" update", func(t *testing.T) {
			cfg := &models.LLMConfig{Name: "Existing " + tt.name, Provider: tt.provider, AuthMethod: models.AuthMethodAPIKey, Model: "valid-model"}
			if err := repo.Create(ctx, cfg); err != nil {
				t.Fatalf("create existing: %v", err)
			}
			cfg.Model = ""
			if err := repo.Update(ctx, cfg); err != ErrLLMConfigModelRequired {
				t.Fatalf("expected ErrLLMConfigModelRequired, got %v", err)
			}
			updated, err := repo.GetByID(ctx, cfg.ID)
			if err != nil {
				t.Fatalf("get updated: %v", err)
			}
			if updated.Model != "valid-model" {
				t.Fatalf("blank model update mutated persisted model to %q", updated.Model)
			}
		})
	}
}

func TestLLMConfigRepo_OAuthProviderPresenceUsesCompactAggregate(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	assertPresence := func(name string, want OAuthProviderPresence) {
		t.Helper()
		counter.Reset()
		counter.SetEnabled(true)
		got, err := repo.OAuthProviderPresence(ctx)
		counter.SetEnabled(false)
		if err != nil {
			t.Fatalf("%s OAuthProviderPresence: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s presence = %#v, want %#v", name, got, want)
		}
		assertOAuthProviderPresenceStatement(t, counter.Statements())
	}

	assertPresence("empty", OAuthProviderPresence{})

	for i, cfg := range []*models.LLMConfig{
		{Name: "Anthropic API Key", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet"},
		{Name: "OpenAI CLI Legacy", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI, Model: "gpt-test"},
		{Name: "Ollama OAuth Unsupported", Provider: models.ProviderOllama, AuthMethod: models.AuthMethodOAuth, Model: "llama3"},
	} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create non-oauth config %d: %v", i, err)
		}
	}
	assertPresence("non oauth rows", OAuthProviderPresence{})

	compatible := &models.LLMConfig{Name: "Compatible OAuth", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth, Model: "custom-model"}
	if err := repo.Create(ctx, compatible); err != nil {
		t.Fatalf("create compatible oauth: %v", err)
	}
	assertPresence("compatible oauth", OAuthProviderPresence{AnyOAuth: true})

	anthropic := &models.LLMConfig{Name: "Anthropic OAuth", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, Model: "claude-sonnet"}
	if err := repo.Create(ctx, anthropic); err != nil {
		t.Fatalf("create anthropic oauth: %v", err)
	}
	assertPresence("anthropic oauth", OAuthProviderPresence{AnyOAuth: true, AnthropicOAuth: true})

	openai := &models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, Model: "gpt-test"}
	if err := repo.Create(ctx, openai); err != nil {
		t.Fatalf("create openai oauth: %v", err)
	}
	assertPresence("openai oauth", OAuthProviderPresence{AnyOAuth: true, AnthropicOAuth: true, OpenAIOAuth: true})
}

func assertOAuthProviderPresenceStatement(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact OAuth-provider aggregate query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if !strings.Contains(stmt, "from agent_configs") {
		t.Fatalf("OAuth provider presence query must read agent_configs: %s", statements[0])
	}
	if strings.Contains(stmt, "join") || strings.Contains(stmt, "oauth_connections") || strings.Contains(stmt, "order by") {
		t.Fatalf("OAuth provider presence query must not join/order: %s", statements[0])
	}
	projection := strings.Split(stmt, " from agent_configs")[0]
	if count := strings.Count(projection, "coalesce(max("); count != 3 {
		t.Fatalf("OAuth provider presence projection has %d aggregate columns, want 3: %s", count, statements[0])
	}
	for _, required := range []string{"auth_method", "provider"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("OAuth provider presence projection missing %q: %s", required, statements[0])
		}
	}
	for _, forbidden := range startupOAuthPresenceForbiddenColumns() {
		if strings.Contains(stmt, forbidden) {
			t.Fatalf("OAuth provider presence query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
}

func startupOAuthPresenceForbiddenColumns() []string {
	return []string{
		"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_id", "oauth_client_secret",
		"oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "models_url",
		"auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "default_max_tokens",
		"context_window", "compaction_threshold", "token_exchange_format", "token_refresh_format",
		"custom_auth_config_json", "custom_auth_state_json", "oauth_config_revision", "mixture_config_json",
		"max_workers", "worker_timeout", "created_at", "updated_at", "oauth_expires_at", "oauth_account_id",
		"oauth_needs_reauth", "oauth_revision",
	}
}

func TestLLMConfigRepo_OAuthProviderPresenceUsesCompactLargeFixtureQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping startup OAuth provider presence large fixture in short mode")
	}
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedMixedStartupOAuthModelConfigs(t, ctx, repo, 50)

	counter.Reset()
	counter.SetEnabled(true)
	presence, err := repo.OAuthProviderPresence(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if presence != (OAuthProviderPresence{AnyOAuth: true, AnthropicOAuth: true, OpenAIOAuth: true}) {
		t.Fatalf("presence = %#v", presence)
	}
	assertOAuthProviderPresenceStatement(t, counter.Statements())
}

func BenchmarkLLMConfigRepoStartupOAuthProviderPresence(b *testing.B) {
	for _, count := range []int{0, 1, 50, 500} {
		b.Run(fmt.Sprintf("configs_%d", count), func(b *testing.B) {
			db, counter := testutil.NewStatementCountingTestDB(b)
			repo := NewLLMConfigRepo(db)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
				b.Fatalf("clear model configs: %v", err)
			}
			seedMixedStartupOAuthModelConfigs(b, ctx, repo, count)
			counter.Reset()
			counter.SetEnabled(true)
			if _, err := repo.OAuthProviderPresence(ctx); err != nil {
				b.Fatal(err)
			}
			statements := len(counter.Statements())
			counter.SetEnabled(false)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				presence, err := repo.OAuthProviderPresence(ctx)
				if err != nil {
					b.Fatal(err)
				}
				if count == 0 {
					if presence != (OAuthProviderPresence{}) {
						b.Fatalf("expected empty presence, got %#v", presence)
					}
				} else if !presence.AnyOAuth {
					b.Fatalf("expected OAuth presence for count %d, got %#v", count, presence)
				}
			}
			b.ReportMetric(float64(statements), "sql/op")
			b.ReportMetric(3, "selected_cols/op")
		})
	}
}

func seedMixedStartupOAuthModelConfigs(tb testing.TB, ctx context.Context, repo *LLMConfigRepo, count int) {
	tb.Helper()
	largeBody := strings.Repeat("x", 64*1024)
	for i := 0; i < count; i++ {
		provider, authMethod := startupOAuthFixtureProviderAuth(i)
		cfg := &models.LLMConfig{
			Name:                  fmt.Sprintf("Startup OAuth Fixture %03d", i),
			Provider:              provider,
			AuthMethod:            authMethod,
			Model:                 startupOAuthFixtureModel(provider),
			APIKey:                "secret-key",
			OAuthAccessToken:      "secret-token",
			OAuthRefreshToken:     "secret-refresh",
			OAuthClientID:         "client-id",
			OAuthClientSecret:     "secret-client",
			OAuthAuthorizeURL:     "https://auth.example.com/authorize",
			OAuthTokenURL:         "https://auth.example.com/token",
			OAuthScopes:           "models",
			OllamaBaseURL:         "http://localhost:11434",
			BaseURL:               "https://example.com/v1/",
			Transport:             "chat_completions",
			PresetSlug:            "custom",
			ModelsURL:             "https://example.com/v1/models",
			AuthHeaderName:        "X-API-Key",
			AuthHeaderValuePrefix: "Bearer",
			ExtraHeadersJSON:      `{"secret":"header"}`,
			ExtraBodyJSON:         largeBody,
			CustomAuthConfigJSON:  `{"signing_secret":"secret"}`,
			CustomAuthStateJSON:   `{"token":"secret"}`,
			MixtureConfigJSON:     `{"large":"` + largeBody + `"}`,
			MaxWorkers:            (i % 4) + 1,
			WorkerTimeout:         60 + i,
		}
		if err := repo.Create(ctx, cfg); err != nil {
			tb.Fatalf("Create startup OAuth fixture %d: %v", i, err)
		}
	}
}

func startupOAuthFixtureProviderAuth(i int) (models.LLMProvider, models.AuthMethod) {
	switch i % 6 {
	case 0:
		return models.ProviderOpenAICompatible, models.AuthMethodOAuth
	case 1:
		return models.ProviderAnthropic, models.AuthMethodOAuth
	case 2:
		return models.ProviderOpenAI, models.AuthMethodOAuth
	case 3:
		return models.ProviderOpenAICompatible, models.AuthMethodAPIKey
	case 4:
		return models.ProviderAnthropic, models.AuthMethodAPIKey
	default:
		return models.ProviderOllama, models.AuthMethodOAuth
	}
}

func startupOAuthFixtureModel(provider models.LLMProvider) string {
	switch provider {
	case models.ProviderAnthropic:
		return "claude-sonnet"
	case models.ProviderOpenAI:
		return "gpt-test"
	case models.ProviderOllama:
		return "llama3"
	default:
		return "custom-model"
	}
}

func TestLLMConfigRepo_HasAny(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	hasAny, err := repo.HasAny(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("HasAny empty: %v", err)
	}
	if hasAny {
		t.Fatal("HasAny empty = true, want false")
	}
	assertHasAnyStatement(t, counter.Statements())

	planRows, err := db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT EXISTS(SELECT 1 FROM agent_configs)`)
	if err != nil {
		t.Fatalf("explain HasAny query: %v", err)
	}
	defer planRows.Close()
	var planDetails []string
	for planRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		planDetails = append(planDetails, detail)
	}
	if err := planRows.Err(); err != nil {
		t.Fatalf("read plan rows: %v", err)
	}
	plan := strings.ToLower(strings.Join(planDetails, " | "))
	if !strings.Contains(plan, "scan agent_configs") {
		t.Fatalf("HasAny query plan = %q, want scan of agent_configs", plan)
	}

	cfg := &models.LLMConfig{Name: "Exists", Provider: models.ProviderTest, Model: "test-model"}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create model config: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	hasAny, err = repo.HasAny(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("HasAny non-empty: %v", err)
	}
	if !hasAny {
		t.Fatal("HasAny non-empty = false, want true")
	}
	assertHasAnyStatement(t, counter.Statements())
}

func assertHasAnyStatement(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one existence query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if stmt != "select exists(select 1 from agent_configs)" {
		t.Fatalf("HasAny statement = %q, want SELECT EXISTS query", statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "order by"} {
		if strings.Contains(stmt, forbidden) {
			t.Fatalf("HasAny statement selected forbidden data %q: %s", forbidden, statements[0])
		}
	}
}

func TestLLMConfigRepo_HasAnyWorksOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HasAny large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	exists, err := repo.HasAny(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("HasAny returned false for non-empty fixture")
	}
}

func BenchmarkLLMConfigRepoHasAnyLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		exists, err := repo.HasAny(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if !exists {
			b.Fatal("expected HasAny to return true")
		}
	}
}

func TestLLMConfigRepo_RuntimeSummariesWorkOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runtime model summary large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)
	targets, err := repo.ListRuntimeSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 50 {
		t.Fatalf("expected 50 summaries, got %d", len(targets))
	}
	targetID := targets[len(targets)-1].ID
	targetName := targets[len(targets)-1].Name

	byID, err := repo.GetRuntimeSummary(ctx, targetID, "")
	if err != nil {
		t.Fatal(err)
	}
	if byID == nil || byID.ID != targetID {
		t.Fatalf("expected target %s, got %#v", targetID, byID)
	}
	byName, err := repo.GetRuntimeSummary(ctx, "", targetName)
	if err != nil {
		t.Fatal(err)
	}
	if byName == nil || byName.Name != targetName {
		t.Fatalf("expected target %s, got %#v", targetName, byName)
	}
}

func BenchmarkLLMConfigRepoRuntimeSummariesLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)
	targets, err := repo.ListRuntimeSummaries(ctx)
	if err != nil {
		b.Fatal(err)
	}
	if len(targets) != 50 {
		b.Fatalf("expected 50 summaries, got %d", len(targets))
	}
	targetID := targets[len(targets)-1].ID
	targetName := targets[len(targets)-1].Name

	b.Run("runtime_summary_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListRuntimeSummaries(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 {
				b.Fatalf("expected 50 configs, got %d", len(configs))
			}
		}
	})
	b.Run("runtime_summary_get_by_id", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cfg, err := repo.GetRuntimeSummary(ctx, targetID, "")
			if err != nil {
				b.Fatal(err)
			}
			if cfg == nil || cfg.ID != targetID {
				b.Fatalf("expected target %s, got %#v", targetID, cfg)
			}
		}
	})
	b.Run("runtime_summary_get_by_name", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cfg, err := repo.GetRuntimeSummary(ctx, "", targetName)
			if err != nil {
				b.Fatal(err)
			}
			if cfg == nil || cfg.Name != targetName {
				b.Fatalf("expected target %s, got %#v", targetName, cfg)
			}
		}
	})
}

func TestLLMConfigRepo_ListWorkerCapacitiesUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Worker Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "worker-alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientID: "client-id", OAuthClientSecret: "secret-client",
		OAuthAuthorizeURL: "https://auth.example.com/authorize", OAuthTokenURL: "https://auth.example.com/token",
		OAuthScopes: "models", OllamaBaseURL: "http://localhost:11434", BaseURL: "https://example.com/v1/",
		Transport: "chat_completions", PresetSlug: "custom", ModelsURL: "https://example.com/v1/models",
		AuthHeaderName: "X-API-Key", AuthHeaderValuePrefix: "Bearer", ExtraHeadersJSON: `{"secret":"header"}`,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
		MaxWorkers: 2,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	unlimited := &models.LLMConfig{
		Name: "Worker Unlimited", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey,
		Model: "worker-unlimited-model", MaxWorkers: 0,
	}
	if err := repo.Create(ctx, unlimited); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Worker Zulu Default", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
		Model: "worker-zulu-model", APIKey: "secret-key", IsDefault: true, MaxWorkers: 1,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	workers, err := repo.ListWorkerCapacities(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("worker capacities len = %d, want 2: %#v", len(workers), workers)
	}
	if workers[0].ID != zuluDefault.ID || workers[1].ID != alpha.ID {
		t.Fatalf("worker capacity ordering = [%s, %s], want default first then name asc", workers[0].Name, workers[1].Name)
	}
	if workers[0].Name != "Worker Zulu Default" || workers[0].Model != "worker-zulu-model" || workers[0].MaxWorkers != 1 {
		t.Fatalf("default worker fields not preserved: %#v", workers[0])
	}
	if workers[1].Name != "Worker Alpha" || workers[1].Model != "worker-alpha-model" || workers[1].MaxWorkers != 2 {
		t.Fatalf("worker fields not preserved: %#v", workers[1])
	}
	for _, worker := range workers {
		assertWorkerCapacityProjectionOmitsConfigBlobs(t, worker)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact worker capacity query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if projection != "select id, name, model, max_workers" {
		t.Fatalf("worker capacity projection = %q, want id/name/model/max_workers in %s", projection, statements[0])
	}
	if !strings.Contains(stmt, "where max_workers > 0") {
		t.Fatalf("worker capacity query must filter max_workers in SQL: %s", statements[0])
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("worker capacity query must preserve default/name ordering: %s", statements[0])
	}
	for _, forbidden := range workerCapacityForbiddenColumns() {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("worker capacity query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
}

func TestLLMConfigRepo_WorkerCapacitiesUseCompactLargeFixtureProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping worker capacity large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50, 2)

	workers, err := repo.ListWorkerCapacities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 50 {
		t.Fatalf("expected 50 worker capacity rows, got %d", len(workers))
	}
	for _, worker := range workers {
		assertWorkerCapacityProjectionOmitsConfigBlobs(t, worker)
	}
}

func BenchmarkLLMConfigRepoWorkerCapacitiesLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50, 2)

	workers, err := repo.ListWorkerCapacities(ctx)
	if err != nil {
		b.Fatal(err)
	}
	if len(workers) != 50 {
		b.Fatalf("expected 50 worker capacity rows, got %d", len(workers))
	}
	for _, worker := range workers {
		assertWorkerCapacityProjectionOmitsConfigBlobs(b, worker)
	}

	b.Run("worker_capacity_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListWorkerCapacities(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 {
				b.Fatalf("expected 50 worker capacity rows, got %d", len(configs))
			}
		}
	})
}

func assertWorkerCapacityProjectionOmitsConfigBlobs(tb testing.TB, cfg models.LLMConfig) {
	tb.Helper()
	if cfg.Provider != "" || cfg.AuthMethod != "" || cfg.APIKey != "" || cfg.OAuthAccessToken != "" ||
		cfg.OAuthRefreshToken != "" || cfg.OAuthClientID != "" || cfg.OAuthClientSecret != "" ||
		cfg.OAuthAuthorizeURL != "" || cfg.OAuthTokenURL != "" || cfg.OAuthScopes != "" ||
		cfg.OllamaBaseURL != "" || cfg.BaseURL != "" || cfg.ModelsURL != "" ||
		cfg.AuthHeaderName != "" || cfg.AuthHeaderValuePrefix != "" || cfg.ExtraHeadersJSON != "" ||
		cfg.ExtraBodyJSON != "" || cfg.CustomAuthConfigJSON != "" || cfg.CustomAuthStateJSON != "" ||
		cfg.MixtureConfigJSON != "" || !cfg.CreatedAt.IsZero() || !cfg.UpdatedAt.IsZero() || cfg.WorkerTimeout != 0 {
		tb.Fatalf("worker capacity projection materialized credential/config fields: %#v", cfg)
	}
}

func workerCapacityForbiddenColumns() []string {
	return []string{
		"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_id", "oauth_client_secret",
		"oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "models_url",
		"auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json",
		"custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "worker_timeout",
	}
}

func BenchmarkLLMConfigRepoAgentPickerValidationLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	picker, err := repo.ListPickerOptions(ctx)
	if err != nil {
		b.Fatal(err)
	}
	if len(picker) < 50 {
		b.Fatalf("expected production-shaped fixture, got %d configs", len(picker))
	}
	for _, option := range picker {
		if option.APIKey != "" || option.OAuthAccessToken != "" || option.OAuthRefreshToken != "" ||
			option.OAuthClientSecret != "" || option.BaseURL != "" || option.ExtraHeadersJSON != "" ||
			option.ExtraBodyJSON != "" || option.CustomAuthConfigJSON != "" || option.CustomAuthStateJSON != "" ||
			option.MixtureConfigJSON != "" || !option.CreatedAt.IsZero() || !option.UpdatedAt.IsZero() ||
			option.MaxWorkers != 0 || option.WorkerTimeout != 0 {
			b.Fatalf("agent picker projection materialized execution/edit-only fields: %#v", option)
		}
	}

	const (
		sampleOps        = 100
		maxBytesPerOp    = 200 * 1024
		maxDurationPerOp = 200 * time.Microsecond
	)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	allocBefore := ms.TotalAlloc
	startedAt := time.Now()
	for i := 0; i < sampleOps; i++ {
		configs, err := repo.ListPickerOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) < 50 {
			b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
		}
	}
	runtime.ReadMemStats(&ms)
	bytesPerOp := (ms.TotalAlloc - allocBefore) / sampleOps
	durationPerOp := time.Since(startedAt) / sampleOps
	b.ReportMetric(float64(bytesPerOp), "guarded_bytes/op")
	b.ReportMetric(float64(durationPerOp.Nanoseconds()), "guarded_ns/op")
	if bytesPerOp > maxBytesPerOp {
		b.Fatalf("agent picker projection allocated %d bytes/op, want <= %d", bytesPerOp, maxBytesPerOp)
	}
	if durationPerOp > maxDurationPerOp {
		b.Fatalf("agent picker projection took %s/op, want <= %s/op", durationPerOp, maxDurationPerOp)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		configs, err := repo.ListPickerOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) < 50 {
			b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
		}
	}
}

func BenchmarkLLMConfigRepoCardsAndPickerLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	largeBody := strings.Repeat("x", 64*1024)
	for i := 0; i < 50; i++ {
		cfg := &models.LLMConfig{
			Name: fmt.Sprintf("Large Custom %02d", i), Provider: models.ProviderOpenAICompatible,
			AuthMethod: models.AuthMethodOAuth, Model: "custom-model", APIKey: "secret-key",
			OAuthAccessToken: "secret-token", OAuthRefreshToken: "secret-refresh",
			OAuthClientSecret: "secret-client", BaseURL: "https://example.com/v1/",
			Transport: "chat_completions", PresetSlug: "custom",
			ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
			CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
			MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
		}
		if err := repo.Create(ctx, cfg); err != nil {
			b.Fatal(err)
		}
	}
	cards, err := repo.ListCards(ctx)
	if err != nil {
		b.Fatal(err)
	}
	for _, card := range cards {
		if card.ExtraBodyJSON != "" || card.CustomAuthConfigJSON != "" || card.CustomAuthStateJSON != "" || card.OAuthRefreshToken != "" {
			b.Fatalf("card projection materialized edit-only fields: %#v", card)
		}
	}

	b.Run("card_projection", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListCards(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) < 50 {
				b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
			}
		}
	})
	b.Run("picker_projection", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListPickerOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) < 50 {
				b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
			}
		}
	})
}

// BenchmarkTaskBoardRefreshModelProjection asserts that the badge-projection
// call site allocates at most 200 KB/op on a 50-model large-config fixture,
// matching the acceptance criterion from GitHub issue #633.
func BenchmarkTaskBoardRefreshModelProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	largeBody := strings.Repeat("x", 64*1024)
	for i := 0; i < 50; i++ {
		cfg := &models.LLMConfig{
			Name: fmt.Sprintf("Badge Bench Model %02d", i), Provider: models.ProviderOpenAICompatible,
			AuthMethod: models.AuthMethodOAuth, Model: "custom-model", APIKey: "secret-key",
			OAuthAccessToken: "secret-token", OAuthRefreshToken: "secret-refresh",
			OAuthClientSecret: "secret-client", BaseURL: "https://example.com/v1/",
			Transport: "chat_completions", PresetSlug: "custom",
			ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
			CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
			MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
		}
		if err := repo.Create(ctx, cfg); err != nil {
			b.Fatal(err)
		}
	}

	// Verify no credential or large fields are materialized by the badge projection.
	badges, err := repo.ListBadgeOptions(ctx)
	if err != nil {
		b.Fatal(err)
	}
	for _, badge := range badges {
		if badge.APIKey != "" || badge.OAuthAccessToken != "" || badge.OAuthRefreshToken != "" ||
			badge.OAuthClientSecret != "" || badge.ExtraBodyJSON != "" ||
			badge.CustomAuthConfigJSON != "" || badge.CustomAuthStateJSON != "" ||
			badge.MixtureConfigJSON != "" {
			b.Fatalf("badge projection materialized credential or large-body fields: %#v", badge)
		}
	}

	const maxAllocsPerOp = 200 * 1024 // 200 KB
	b.ReportAllocs()
	b.ResetTimer()
	var totalAllocs uint64
	for i := 0; i < b.N; i++ {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		before := ms.TotalAlloc
		configs, err := repo.ListBadgeOptions(ctx)
		runtime.ReadMemStats(&ms)
		after := ms.TotalAlloc
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) < 50 {
			b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
		}
		totalAllocs += after - before
	}
	allocsPerOp := totalAllocs / uint64(b.N)
	if allocsPerOp > maxAllocsPerOp {
		b.Fatalf("badge projection allocated %d bytes/op, want <= %d", allocsPerOp, maxAllocsPerOp)
	}
}

func TestLLMConfigRepo_ListBadgeOptionsUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Badge Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "badge-alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		BaseURL: "https://example.com/v1/", Transport: "chat_completions", PresetSlug: "custom",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Badge Zulu Default", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
		Model: "badge-zulu-model", APIKey: "secret-key", IsDefault: true,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	badges, err := repo.ListBadgeOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(badges) != len(full) {
		t.Fatalf("badge len = %d, full len = %d", len(badges), len(full))
	}
	for i := range full {
		if badges[i].ID != full[i].ID || badges[i].Name != full[i].Name ||
			badges[i].Model != full[i].Model || badges[i].IsDefault != full[i].IsDefault {
			t.Fatalf("badge[%d] = %#v, full[%d] = %#v", i, badges[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(badges))
	for _, b := range badges {
		byID[b.ID] = b
	}
	customBadge := byID[alpha.ID]
	if customBadge.Name != "Badge Alpha" || customBadge.Model != "badge-alpha-model" {
		t.Fatalf("badge label fields not preserved: %#v", customBadge)
	}
	if customBadge.APIKey != "" || customBadge.OAuthAccessToken != "" || customBadge.OAuthRefreshToken != "" ||
		customBadge.OAuthClientSecret != "" || customBadge.BaseURL != "" || customBadge.ExtraHeadersJSON != "" ||
		customBadge.ExtraBodyJSON != "" || customBadge.CustomAuthConfigJSON != "" ||
		customBadge.CustomAuthStateJSON != "" || customBadge.MixtureConfigJSON != "" {
		t.Fatalf("badge projection materialized credential or large-body fields: %#v", customBadge)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact badge query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, model, is_default") {
		t.Fatalf("badge projection = %q, want id/name/model/is_default in %s", projection, statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("badge query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("badge query must preserve default/name ordering: %s", statements[0])
	}
}

func TestLLMConfigRepo_ListRuntimeSummariesUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Runtime Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "runtime-alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		BaseURL: "https://example.com/v1/", OllamaBaseURL: "http://localhost:11434",
		ModelsURL: "https://example.com/models", OAuthAuthorizeURL: "https://example.com/auth",
		OAuthTokenURL: "https://example.com/token", Transport: "chat_completions", PresetSlug: "custom",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`, MaxWorkers: 3, WorkerTimeout: 45,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Runtime Zulu Default", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey,
		Model: "runtime-zulu-model", APIKey: "secret-key", IsDefault: true, MaxWorkers: 5,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	summaries, err := repo.ListRuntimeSummaries(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != len(full) {
		t.Fatalf("summary len = %d, full len = %d", len(summaries), len(full))
	}
	for i := range full {
		if summaries[i].ID != full[i].ID || summaries[i].Name != full[i].Name ||
			summaries[i].Provider != full[i].Provider || summaries[i].Model != full[i].Model ||
			summaries[i].IsDefault != full[i].IsDefault || summaries[i].AuthMethod != full[i].AuthMethod ||
			summaries[i].MaxWorkers != full[i].MaxWorkers || summaries[i].WorkerTimeout != full[i].WorkerTimeout {
			t.Fatalf("summary[%d] = %#v, full[%d] = %#v", i, summaries[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	customSummary := byID[alpha.ID]
	if customSummary.APIKey != "" || customSummary.OAuthAccessToken != "" || customSummary.OAuthRefreshToken != "" ||
		customSummary.OAuthClientSecret != "" || customSummary.BaseURL != "" || customSummary.OllamaBaseURL != "" ||
		customSummary.ModelsURL != "" || customSummary.OAuthAuthorizeURL != "" || customSummary.OAuthTokenURL != "" ||
		customSummary.ExtraHeadersJSON != "" || customSummary.ExtraBodyJSON != "" ||
		customSummary.CustomAuthConfigJSON != "" || customSummary.CustomAuthStateJSON != "" || customSummary.MixtureConfigJSON != "" {
		t.Fatalf("runtime summary materialized credential, endpoint, or large-body fields: %#v", customSummary)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact runtime summary query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") {
		t.Fatalf("runtime summary projection = %q, want compact display fields in %s", projection, statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("runtime summary query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("runtime summary query must preserve default/name ordering: %s", statements[0])
	}
}

func TestLLMConfigRepo_GetRuntimeSummaryUsesTargetedBoundedLookup(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	largeBody := strings.Repeat("x", 1024*1024)
	target := &models.LLMConfig{
		Name: "Target Runtime Model", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "target-runtime-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client", BaseURL: "https://example.com/v1/",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`, MaxWorkers: 7,
	}
	if err := repo.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	other := &models.LLMConfig{Name: "Other Runtime Model", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "other-runtime-model", IsDefault: true, ExtraBodyJSON: largeBody}
	if err := repo.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	byID, err := repo.GetRuntimeSummary(ctx, target.ID, "")
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if byID == nil || byID.ID != target.ID || byID.Name != target.Name || byID.MaxWorkers != 7 {
		t.Fatalf("lookup by id = %#v, want target summary", byID)
	}
	assertRuntimeSummaryLookupStatement(t, counter.Statements(), "id = ?")

	counter.Reset()
	counter.SetEnabled(true)
	byName, err := repo.GetRuntimeSummary(ctx, "", " target runtime model ")
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if byName == nil || byName.ID != target.ID {
		t.Fatalf("lookup by normalized name = %#v, want target", byName)
	}
	assertRuntimeSummaryLookupStatement(t, counter.Statements(), "name = ? collate nocase")

	missing, err := repo.GetRuntimeSummary(ctx, "missing", "")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("missing lookup = %#v, want nil", missing)
	}
}

func assertRuntimeSummaryLookupStatement(t *testing.T, statements []string, requiredWhere string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one targeted runtime summary query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") {
		t.Fatalf("runtime lookup projection = %q, want compact display fields in %s", projection, statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("runtime lookup selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, requiredWhere) {
		t.Fatalf("runtime lookup query %q does not contain required predicate %q", statements[0], requiredWhere)
	}
}

func TestLLMConfigRepo_ListPickerOptionsUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Alpha Custom", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		BaseURL: "https://example.com/v1/", Transport: "chat_completions", PresetSlug: "custom",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Zulu Default", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
		Model: "zulu-model", APIKey: "secret-key", IsDefault: true,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	picker, err := repo.ListPickerOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(picker) != len(full) {
		t.Fatalf("picker len = %d, full len = %d", len(picker), len(full))
	}
	for i := range full {
		if picker[i].ID != full[i].ID || picker[i].Name != full[i].Name || picker[i].Model != full[i].Model {
			t.Fatalf("picker[%d] = %#v, full[%d] = %#v", i, picker[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(picker))
	for _, option := range picker {
		byID[option.ID] = option
	}
	custom := byID[alpha.ID]
	if custom.Name != "Alpha Custom" || custom.Model != "alpha-model" {
		t.Fatalf("picker label fields not preserved: %#v", custom)
	}
	if custom.Provider != "" || custom.APIKey != "" || custom.OAuthAccessToken != "" || custom.OAuthRefreshToken != "" ||
		custom.OAuthClientSecret != "" || custom.BaseURL != "" || custom.ExtraHeadersJSON != "" || custom.ExtraBodyJSON != "" ||
		custom.CustomAuthConfigJSON != "" || custom.CustomAuthStateJSON != "" || custom.MixtureConfigJSON != "" {
		t.Fatalf("picker materialized execution/edit-only fields: %#v", custom)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact picker query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, model") {
		t.Fatalf("picker projection = %q, want id/name/model in %s", projection, statements[0])
	}
	for _, forbidden := range []string{"provider", "api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("picker query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("picker query must preserve default/name ordering: %s", statements[0])
	}

	fullCustom, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullCustom == nil || fullCustom.APIKey != "secret-key" || fullCustom.OAuthAccessToken != "secret-token" || fullCustom.ExtraBodyJSON != largeBody || fullCustom.CustomAuthStateJSON == "" || fullCustom.MixtureConfigJSON == "" {
		t.Fatalf("full detail path lost provider fields: %#v", fullCustom)
	}
}

func TestLLMConfigRepo_ListChatSelectionOptionsUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Chat Selection Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		BaseURL: "https://example.com/v1/", Transport: "chat_completions", PresetSlug: "custom",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Chat Selection Zulu Default", Provider: models.ProviderTest, AuthMethod: models.AuthMethodAPIKey,
		Model: "zulu-model", APIKey: "secret-key", IsDefault: true,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	selection, err := repo.ListChatSelectionOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection) != len(full) {
		t.Fatalf("selection len = %d, full len = %d", len(selection), len(full))
	}
	for i := range full {
		if selection[i].ID != full[i].ID || selection[i].Name != full[i].Name ||
			selection[i].Provider != full[i].Provider || selection[i].Model != full[i].Model ||
			selection[i].IsDefault != full[i].IsDefault {
			t.Fatalf("selection[%d] = %#v, full[%d] = %#v", i, selection[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(selection))
	for _, option := range selection {
		byID[option.ID] = option
	}
	custom := byID[alpha.ID]
	if custom.Name != "Chat Selection Alpha" || custom.Provider != models.ProviderOpenAICompatible || custom.Model != "alpha-model" {
		t.Fatalf("selection context fields not preserved: %#v", custom)
	}
	if custom.APIKey != "" || custom.OAuthAccessToken != "" || custom.OAuthRefreshToken != "" ||
		custom.OAuthClientSecret != "" || custom.BaseURL != "" || custom.ExtraHeadersJSON != "" ||
		custom.ExtraBodyJSON != "" || custom.CustomAuthConfigJSON != "" || custom.CustomAuthStateJSON != "" ||
		custom.MixtureConfigJSON != "" {
		t.Fatalf("chat selection materialized execution/edit-only fields: %#v", custom)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact chat-selection query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, provider, model, is_default") {
		t.Fatalf("chat selection projection = %q, want id/name/provider/model/is_default in %s", projection, statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("chat selection query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("chat selection query must preserve default/name ordering: %s", statements[0])
	}

	fullCustom, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullCustom == nil || fullCustom.APIKey != "secret-key" || fullCustom.OAuthAccessToken != "secret-token" || fullCustom.ExtraBodyJSON != largeBody || fullCustom.CustomAuthStateJSON == "" || fullCustom.MixtureConfigJSON == "" {
		t.Fatalf("full detail path lost provider fields: %#v", fullCustom)
	}
}

func TestLLMConfigRepo_ListVisionSelectionOptionsUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	largeBody := strings.Repeat("x", 64*1024)
	legacyCLI := &models.LLMConfig{
		Name:       "Legacy CLI",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodCLI,
		Model:      "claude-cli",
	}
	apiKey := &models.LLMConfig{
		Name:                 "Anthropic API",
		Provider:             models.ProviderAnthropic,
		AuthMethod:           models.AuthMethodAPIKey,
		Model:                "claude-sonnet-4-5-20250929",
		APIKey:               "api-secret",
		OAuthRefreshToken:    "refresh-secret",
		OAuthClientSecret:    "client-secret",
		BaseURL:              "https://api.example.com",
		ExtraHeadersJSON:     `{"header":"secret"}`,
		ExtraBodyJSON:        largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON:  `{"token":"secret"}`,
		MixtureConfigJSON:    `{"large":"` + largeBody + `"}`,
		IsDefault:            true,
	}
	oauth := &models.LLMConfig{
		Name:              "Anthropic OAuth",
		Provider:          models.ProviderAnthropic,
		AuthMethod:        models.AuthMethodOAuth,
		Model:             "claude-opus-5-20250929",
		OAuthAccessToken:  "oauth-secret",
		OAuthRefreshToken: "oauth-refresh-secret",
	}
	otherProvider := &models.LLMConfig{
		Name:       "OpenAI API",
		Provider:   models.ProviderOpenAI,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "gpt-5",
		APIKey:     "openai-secret",
	}
	for _, cfg := range []*models.LLMConfig{legacyCLI, apiKey, oauth, otherProvider} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	selection, err := repo.ListVisionSelectionOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListVisionSelectionOptions: %v", err)
	}
	if len(selection) != len(full) {
		t.Fatalf("selection len = %d, full len = %d", len(selection), len(full))
	}
	for i := range full {
		if selection[i].ID != full[i].ID || selection[i].Name != full[i].Name ||
			selection[i].Provider != full[i].Provider || selection[i].Model != full[i].Model ||
			selection[i].AuthMethod != full[i].AuthMethod || selection[i].IsDefault != full[i].IsDefault {
			t.Fatalf("selection[%d] = %#v, full[%d] = %#v", i, selection[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(selection))
	for _, option := range selection {
		byID[option.ID] = option
	}
	legacySelection := byID[legacyCLI.ID]
	apiSelection := byID[apiKey.ID]
	oauthSelection := byID[oauth.ID]
	if !legacySelection.IsAnthropicCLI() {
		t.Fatalf("legacy CLI selection row should remain CLI-only: %#v", legacySelection)
	}
	if apiSelection.APIKey != "present" || apiSelection.OAuthAccessToken != "" || apiSelection.IsAnthropicCLI() {
		t.Fatalf("API-key presence was not preserved as a non-secret sentinel: %#v", apiSelection)
	}
	if oauthSelection.OAuthAccessToken != "present" || oauthSelection.APIKey != "" || oauthSelection.IsAnthropicCLI() {
		t.Fatalf("OAuth presence was not preserved as a non-secret sentinel: %#v", oauthSelection)
	}
	for _, option := range selection {
		if option.OAuthRefreshToken != "" || option.OAuthClientSecret != "" || option.BaseURL != "" ||
			option.ExtraHeadersJSON != "" || option.ExtraBodyJSON != "" || option.CustomAuthConfigJSON != "" ||
			option.CustomAuthStateJSON != "" || option.MixtureConfigJSON != "" || !option.CreatedAt.IsZero() || !option.UpdatedAt.IsZero() {
			t.Fatalf("vision selection materialized execution/edit-only fields: %#v", option)
		}
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact vision-selection query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	wantProjection := "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '') != '' then 1 else 0 end, case when coalesce(oauth_connection_id, '') != '' then exists(select 1 from oauth_connections c where c.id = oauth_connection_id and c.oauth_access_token != '') else coalesce(oauth_access_token, '') != '' end"
	if projection != wantProjection {
		t.Fatalf("vision selection projection = %q, want %q", projection, wantProjection)
	}
	for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_id", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "transport", "preset_slug", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "default_max_tokens", "token_exchange_format", "token_refresh_format", "custom_auth_config_json", "custom_auth_state_json", "oauth_config_revision", "mixture_config_json", "auto_start_tasks", "created_at", "updated_at", "max_tokens", "temperature", "reasoning_effort", "max_workers", "worker_timeout"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("vision selection query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("vision selection query must preserve default/name ordering: %s", statements[0])
	}

	fullSelected, err := repo.GetByID(ctx, apiKey.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fullSelected == nil || fullSelected.APIKey != "api-secret" || fullSelected.OAuthRefreshToken != "refresh-secret" ||
		fullSelected.OAuthClientSecret != "client-secret" || fullSelected.BaseURL != "https://api.example.com" ||
		fullSelected.ExtraBodyJSON != largeBody || fullSelected.CustomAuthStateJSON == "" || fullSelected.MixtureConfigJSON == "" {
		t.Fatalf("full detail path lost provider fields: %#v", fullSelected)
	}
}

func TestLLMConfigRepo_ListTaskCreationSelectionOptionsUsesBoundedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	largeBody := strings.Repeat("x", 1024*1024)
	alpha := &models.LLMConfig{
		Name: "Task Creation Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "alpha-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientID: "client-id", OAuthClientSecret: "secret-client",
		OAuthAuthorizeURL: "https://auth.example.com/authorize", OAuthTokenURL: "https://auth.example.com/token",
		OAuthScopes: "models", OllamaBaseURL: "http://localhost:11434", BaseURL: "https://example.com/v1/",
		Transport: "chat_completions", PresetSlug: "custom", ModelsURL: "https://example.com/v1/models",
		AuthHeaderName: "X-API-Key", AuthHeaderValuePrefix: "Bearer", ExtraHeadersJSON: `{"secret":"header"}`,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
		AutoStartTasks: true,
	}
	if err := repo.Create(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	zuluDefault := &models.LLMConfig{
		Name: "Task Creation Zulu Default", Provider: models.ProviderTest, AuthMethod: models.AuthMethodAPIKey,
		Model: "zulu-model", APIKey: "secret-key", IsDefault: true,
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, zuluDefault); err != nil {
		t.Fatal(err)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counter.Reset()
	counter.SetEnabled(true)
	selection, err := repo.ListTaskCreationSelectionOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection) != len(full) {
		t.Fatalf("selection len = %d, full len = %d", len(selection), len(full))
	}
	for i := range full {
		if selection[i].ID != full[i].ID || selection[i].Name != full[i].Name ||
			selection[i].Provider != full[i].Provider || selection[i].Model != full[i].Model ||
			selection[i].IsDefault != full[i].IsDefault || selection[i].AutoStartTasks != full[i].AutoStartTasks {
			t.Fatalf("task creation selection[%d] = %#v, full[%d] = %#v", i, selection[i], i, full[i])
		}
	}

	byID := make(map[string]models.LLMConfig, len(selection))
	for _, option := range selection {
		byID[option.ID] = option
	}
	custom := byID[alpha.ID]
	if custom.Name != "Task Creation Alpha" || custom.Provider != models.ProviderOpenAICompatible || custom.Model != "alpha-model" || !custom.AutoStartTasks {
		t.Fatalf("task creation selection fields not preserved: %#v", custom)
	}
	assertTaskCreationSelectionProjectionOmitsConfigBlobs(t, custom)

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact task-creation selection query", statements)
	}
	assertTaskCreationSelectionStatement(t, statements[0])
}

func TestLLMConfigRepo_TaskCreationSelectionProjectionOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping task creation selection large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	configs, err := repo.ListTaskCreationSelectionOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 50 || configs[0].ID == "" {
		t.Fatalf("task creation fixture returned %d configs", len(configs))
	}
}

func BenchmarkTaskCreationModelSelectionProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		configs, err := repo.ListTaskCreationSelectionOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) != 50 || configs[0].ID == "" {
			b.Fatalf("task creation fixture returned %d configs", len(configs))
		}
	}
}

func assertTaskCreationSelectionProjectionOmitsConfigBlobs(tb testing.TB, cfg models.LLMConfig) {
	tb.Helper()
	if cfg.AuthMethod != "" || cfg.APIKey != "" || cfg.OAuthAccessToken != "" || cfg.OAuthRefreshToken != "" ||
		cfg.OAuthClientID != "" || cfg.OAuthClientSecret != "" || cfg.OAuthAuthorizeURL != "" || cfg.OAuthTokenURL != "" ||
		cfg.OAuthScopes != "" || cfg.OllamaBaseURL != "" || cfg.BaseURL != "" || cfg.Transport != "" || cfg.PresetSlug != "" ||
		cfg.ModelsURL != "" || cfg.AuthHeaderName != "" || cfg.AuthHeaderValuePrefix != "" || cfg.ExtraHeadersJSON != "" ||
		cfg.ExtraBodyJSON != "" || cfg.CustomAuthConfigJSON != "" || cfg.CustomAuthStateJSON != "" || cfg.MixtureConfigJSON != "" ||
		cfg.MaxWorkers != 0 || cfg.WorkerTimeout != 0 || !cfg.CreatedAt.IsZero() || !cfg.UpdatedAt.IsZero() {
		tb.Fatalf("task creation selection materialized credential/config fields: %#v", cfg)
	}
}

func assertTaskCreationSelectionStatement(tb testing.TB, raw string) {
	tb.Helper()
	stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if projection != "select id, name, provider, model, is_default, auto_start_tasks" {
		tb.Fatalf("task creation selection projection = %q, want compact selection fields in %s", projection, raw)
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_id", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "transport", "preset_slug", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "worker_timeout", "max_workers"} {
		if strings.Contains(projection, forbidden) {
			tb.Fatalf("task creation selection query selected forbidden column %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		tb.Fatalf("task creation selection query must preserve default/name ordering: %s", raw)
	}
}

func TestLLMConfigRepo_APIChatSelectionProjectionOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping API Chat selection large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	configs, err := repo.ListChatSelectionOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 50 || configs[0].ID == "" {
		t.Fatalf("compact selection fixture returned %d configs", len(configs))
	}
	full, err := repo.GetByID(ctx, configs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if full == nil || full.APIKey == "" || full.ExtraBodyJSON == "" || full.MixtureConfigJSON == "" {
		t.Fatalf("selected full model was not hydrated: %#v", full)
	}
}

func BenchmarkAPIChatModelSelectionThenGet(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		configs, err := repo.ListChatSelectionOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) != 50 || configs[0].ID == "" {
			b.Fatalf("selection fixture returned %d configs", len(configs))
		}
		selectedID := configs[0].ID
		full, err := repo.GetByID(ctx, selectedID)
		if err != nil {
			b.Fatal(err)
		}
		if full == nil || full.APIKey == "" || full.ExtraBodyJSON == "" || full.MixtureConfigJSON == "" {
			b.Fatalf("selected full model was not hydrated: %#v", full)
		}
	}
}

func prepareLargeVisionSelectionFixture(tb testing.TB, db *sql.DB, repo *LLMConfigRepo, ctx context.Context) string {
	tb.Helper()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		tb.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(tb, ctx, repo, 50)
	if _, err := db.Exec(`UPDATE agent_configs SET provider = ?, auth_method = ?, model = ?`, models.ProviderAnthropic, models.AuthMethodAPIKey, "claude-sonnet-4-5-20250929"); err != nil {
		tb.Fatalf("configure vision model fixture: %v", err)
	}
	var selectedID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM agent_configs ORDER BY is_default DESC, name ASC LIMIT 1`).Scan(&selectedID); err != nil {
		tb.Fatalf("select vision fixture model: %v", err)
	}
	return selectedID
}

func TestLLMConfigRepo_VisionSelectionProjectionOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping vision selection large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	selectedID := prepareLargeVisionSelectionFixture(t, db, repo, ctx)

	options, err := repo.ListVisionSelectionOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 50 || options[0].ID == "" {
		t.Fatalf("compact vision selection fixture returned %d options", len(options))
	}
	full, err := repo.GetByID(ctx, selectedID)
	if err != nil {
		t.Fatal(err)
	}
	if full == nil || full.APIKey == "" || full.ExtraBodyJSON == "" || full.MixtureConfigJSON == "" {
		t.Fatalf("selected full vision model was not hydrated: %#v", full)
	}
}

func BenchmarkVisionModelSelectionThenGetByID(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	selectedID := prepareLargeVisionSelectionFixture(b, db, repo, ctx)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		options, err := repo.ListVisionSelectionOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(options) != 50 || options[0].ID == "" {
			b.Fatalf("vision selection fixture returned %d options", len(options))
		}
		full, err := repo.GetByID(ctx, selectedID)
		if err != nil {
			b.Fatal(err)
		}
		if full == nil || full.APIKey == "" || full.ExtraBodyJSON == "" || full.MixtureConfigJSON == "" {
			b.Fatalf("selected full vision model was not hydrated: %#v", full)
		}
	}
}

func BenchmarkVisionModelSelectionSingleConnectionContention(b *testing.B) {
	db, counter := testutil.NewStatementCountingTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	selectedID := prepareLargeVisionSelectionFixture(b, db, repo, ctx)
	lightweightLookup := func() error {
		var projectID string
		return db.QueryRowContext(context.Background(), `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID)
	}

	benchmarkVisionSelectionWithContention(b, db, counter, 2, func() error {
		options, err := repo.ListVisionSelectionOptions(ctx)
		if err != nil {
			return err
		}
		if len(options) != 50 || options[0].ID == "" {
			return fmt.Errorf("compact vision selection fixture returned %d options", len(options))
		}
		full, err := repo.GetByID(ctx, selectedID)
		if err != nil {
			return err
		}
		if full == nil || full.APIKey == "" || full.ExtraBodyJSON == "" || full.MixtureConfigJSON == "" {
			return fmt.Errorf("selected full vision model was not hydrated")
		}
		return nil
	}, lightweightLookup)
}

func benchmarkVisionSelectionWithContention(b *testing.B, db *sql.DB, counter *testutil.SQLStatementCounter, expectedModelQueryCloses int, lookup func() error, lightweightLookup func() error) {
	b.Helper()
	b.ReportAllocs()
	if expectedModelQueryCloses < 1 {
		b.Fatal("expected at least one model query close")
	}

	var totalLightweightWait time.Duration
	for i := 0; i < b.N; i++ {
		func() {
			releaseRows := make(chan struct{})
			var releaseOnce sync.Once
			release := func() {
				releaseOnce.Do(func() { close(releaseRows) })
			}
			defer func() {
				release()
				counter.SetRowsCloseObserver(nil)
			}()

			finalModelRowsClosing := make(chan struct{})
			modelQueryCloses := 0
			counter.SetRowsCloseObserver(func(_ context.Context, query string) {
				if !strings.Contains(strings.ToLower(query), "from agent_configs") {
					return
				}
				modelQueryCloses++
				if modelQueryCloses == expectedModelQueryCloses {
					// The row set is fully materialized. The observer runs before the
					// wrapped rows.Close calls the underlying driver, so the single
					// pool connection is still held while the competing query waits.
					close(finalModelRowsClosing)
					<-releaseRows
				}
			})

			lookupResult := make(chan error, 1)
			go func() { lookupResult <- lookup() }()

			var lookupErr error
			select {
			case <-finalModelRowsClosing:
			case lookupErr = <-lookupResult:
				b.Fatalf("vision model lookup completed before its final rows-close boundary: %v", lookupErr)
			case <-time.After(2 * time.Second):
				b.Fatalf("vision model lookup did not reach its final rows-close boundary")
			}

			waitCountBefore := db.Stats().WaitCount
			waitDurationBefore := db.Stats().WaitDuration
			lightweightResult := make(chan error, 1)
			go func() { lightweightResult <- lightweightLookup() }()

			waitObserved := false
			waitDeadline := time.NewTimer(2 * time.Second)
			waitPoll := time.NewTicker(time.Millisecond)
			for !waitObserved {
				if db.Stats().WaitCount > waitCountBefore {
					waitObserved = true
					break
				}
				select {
				case err := <-lightweightResult:
					b.Fatalf("competing lightweight query completed before observing a pool wait: %v", err)
				case <-waitPoll.C:
				case <-waitDeadline.C:
					b.Fatalf("competing lightweight query did not enter the single-connection pool wait")
				}
			}
			waitPoll.Stop()
			if !waitDeadline.Stop() {
				select {
				case <-waitDeadline.C:
				default:
				}
			}

			release()
			if lookupErr = <-lookupResult; lookupErr != nil {
				b.Fatal(lookupErr)
			}
			if err := <-lightweightResult; err != nil {
				b.Fatalf("lightweight project lookup: %v", err)
			}
			if modelQueryCloses != expectedModelQueryCloses {
				b.Fatalf("model query closes = %d, want %d", modelQueryCloses, expectedModelQueryCloses)
			}
			totalLightweightWait += db.Stats().WaitDuration - waitDurationBefore
		}()
	}
	b.StopTimer()
	b.ReportMetric(float64(totalLightweightWait.Nanoseconds())/float64(b.N), "lightweight_db_wait_ns/op")
}

func TestLLMConfigRepo_BrowserChatContextModelLoadingProjectionOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser Chat context model-loading large fixture in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	configs, err := repo.ListChatSelectionOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 50 || configs[0].ID == "" {
		t.Fatalf("browser Chat context fixture returned %d configs", len(configs))
	}
}

func BenchmarkBrowserChatContextModelLoading(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		configs, err := repo.ListChatSelectionOptions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) != 50 || configs[0].ID == "" {
			b.Fatalf("compact browser Chat context fixture returned %d configs", len(configs))
		}
	}
}

func BenchmarkProjectDialogModelProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	configs, err := repo.ListChatSelectionOptions(ctx)
	if err != nil {
		b.Fatal(err)
	}
	if len(configs) != 50 || configs[0].ID == "" {
		b.Fatalf("project dialog model fixture returned %d configs", len(configs))
	}
	for _, cfg := range configs {
		if cfg.APIKey != "" || cfg.OAuthAccessToken != "" || cfg.OAuthRefreshToken != "" ||
			cfg.OAuthClientSecret != "" || cfg.BaseURL != "" || cfg.ExtraHeadersJSON != "" ||
			cfg.ExtraBodyJSON != "" || cfg.CustomAuthConfigJSON != "" || cfg.CustomAuthStateJSON != "" ||
			cfg.MixtureConfigJSON != "" || cfg.MaxWorkers != 0 || cfg.WorkerTimeout != 0 ||
			!cfg.CreatedAt.IsZero() || !cfg.UpdatedAt.IsZero() {
			b.Fatalf("project dialog projection materialized hidden fields: %#v", cfg)
		}
	}

	const maxAllocsPerOp = 200 * 1024
	const maxDurationPerOp = 200 * time.Microsecond
	b.ReportAllocs()
	b.ResetTimer()
	var totalAllocs uint64
	for i := 0; i < b.N; i++ {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		before := ms.TotalAlloc
		configs, err := repo.ListChatSelectionOptions(ctx)
		runtime.ReadMemStats(&ms)
		after := ms.TotalAlloc
		if err != nil {
			b.Fatal(err)
		}
		if len(configs) != 50 || configs[0].ID == "" {
			b.Fatalf("project dialog model projection returned %d configs", len(configs))
		}
		totalAllocs += after - before
	}
	allocsPerOp := totalAllocs / uint64(b.N)
	if allocsPerOp > maxAllocsPerOp {
		b.Fatalf("project dialog model projection allocated %d bytes/op, want <= %d", allocsPerOp, maxAllocsPerOp)
	}
	if elapsedPerOp := b.Elapsed() / time.Duration(b.N); elapsedPerOp > maxDurationPerOp {
		b.Fatalf("project dialog model projection took %s/op, want <= %s/op", elapsedPerOp, maxDurationPerOp)
	}
}

func TestLLMConfigRepo_ListCardsUsesBoundedProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	largeBody := strings.Repeat("x", 1024*1024)
	aggregator := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	if err := repo.Create(ctx, aggregator); err != nil {
		t.Fatal(err)
	}
	config := &models.LLMConfig{
		Name: "Large Custom", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "custom-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	mixture := &models.LLMConfig{
		Name: "Summary Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"aggregator":{"agent_config_id":"` + aggregator.ID + `","label":"Aggregator label"},"reference_models":[{"agent_config_id":"a"},{"agent_config_id":"b"}],"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, mixture); err != nil {
		t.Fatal(err)
	}

	cards, err := repo.ListCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]models.LLMConfig, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}
	custom := byID[config.ID]
	if custom.APIKey != "present" || custom.OAuthAccessToken != "present" {
		t.Fatalf("credential-presence summaries not preserved: %#v", custom)
	}
	if custom.ExtraBodyJSON != "" || custom.ExtraHeadersJSON != "" || custom.OAuthRefreshToken != "" ||
		custom.OAuthClientSecret != "" || custom.CustomAuthConfigJSON != "" || custom.CustomAuthStateJSON != "" || custom.MixtureConfigJSON != "" {
		t.Fatalf("edit-only data materialized by card query: %#v", custom)
	}
	summary := byID[mixture.ID]
	if summary.MixtureAggregatorID != aggregator.ID || summary.MixtureAggregatorLabel != "Aggregator label" || summary.MixtureReferenceCount != 2 {
		t.Fatalf("mixture summary = %#v", summary)
	}
	for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json"} {
		if strings.Contains(llmConfigCardColumns, forbidden) {
			t.Fatalf("card projection contains edit-only column %q", forbidden)
		}
	}
}

func TestLLMConfigRepo_ListMixtureDefinitionsUsesBoundedProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	largeBody := strings.Repeat("x", 1024*1024)
	custom := &models.LLMConfig{
		Name: "Large Custom", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "custom-model", APIKey: "secret-key", OAuthRefreshToken: "secret-refresh",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, custom); err != nil {
		t.Fatal(err)
	}
	mixture := &models.LLMConfig{
		Name: "Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"aggregator":{"agent_config_id":"` + custom.ID + `"},"reference_models":[]}`,
	}
	if err := repo.Create(ctx, mixture); err != nil {
		t.Fatal(err)
	}

	defs, err := repo.ListMixtureDefinitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].ID != mixture.ID || defs[0].Name != mixture.Name || defs[0].MixtureConfigJSON != mixture.MixtureConfigJSON {
		t.Fatalf("mixture definitions = %#v", defs)
	}
	if defs[0].ExtraBodyJSON != "" || defs[0].ExtraHeadersJSON != "" || defs[0].OAuthRefreshToken != "" ||
		defs[0].CustomAuthConfigJSON != "" || defs[0].CustomAuthStateJSON != "" {
		t.Fatalf("mixture definition query materialized unrelated edit-only fields: %#v", defs[0])
	}
}

func TestLLMConfigRepoCustomOAuthConnectionAndRefreshLease(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{
		Name: "Custom OAuth", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth, Model: "model",
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	firstRevision, advanced, err := repo.AdvanceCustomOAuthRevision(ctx, cfg.ID)
	if err != nil || !advanced || firstRevision != 1 {
		t.Fatalf("first OAuth generation = %d, %v, %v", firstRevision, advanced, err)
	}
	secondRevision, advanced, err := repo.AdvanceCustomOAuthRevision(ctx, cfg.ID)
	if err != nil || !advanced || secondRevision != 2 {
		t.Fatalf("second OAuth generation = %d, %v, %v", secondRevision, advanced, err)
	}
	if err := repo.UpdateCustomOAuthConnection(ctx, cfg.ID, "access", "refresh", 1234, `{"instance_id":"instance"}`); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OAuthAccessToken != "access" || stored.OAuthRefreshToken != "refresh" ||
		stored.OAuthExpiresAt != 1234 || stored.CustomAuthStateJSON != `{"instance_id":"instance"}` {
		t.Fatalf("custom OAuth connection not persisted atomically: %#v", stored)
	}
	revision := stored.OAuthConfigRevision
	stored.Name = "Changed during OAuth"
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.UpdateCustomOAuthConnectionIfRevision(
		ctx, stored.ID, revision, "stale-access", "stale-refresh", 5678, `{"instance_id":"stale"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("stale OAuth callback updated a newer model configuration")
	}
	updated, err = repo.UpdateCustomOAuthTokensIfRevision(
		ctx, stored.ID, stored.OAuthConfigRevision, "fresh-access", "fresh-refresh", 9012,
	)
	if err != nil || !updated {
		t.Fatalf("current-revision token update = %v, %v", updated, err)
	}

	now := time.Now()
	acquired, err := repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-1", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first lease acquire = %v, %v", acquired, err)
	}
	acquired, err = repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-2", now, time.Minute)
	if err != nil || acquired {
		t.Fatalf("competing lease acquire = %v, %v; want busy", acquired, err)
	}
	if err := repo.ReleaseOAuthRefreshLease(ctx, cfg.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
	acquired, err = repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-2", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease acquire after release = %v, %v", acquired, err)
	}
}

func TestLLMConfigRepo_MixtureConfigJSONPersists(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	mixtureJSON := `{"enabled":true,"reference_models":[{"agent_config_id":"ref"}],"aggregator":{"agent_config_id":"agg"}}`
	cfg := &models.LLMConfig{
		Name:              "Research Mixture",
		Provider:          models.ProviderMixture,
		Model:             "research-heavy",
		MixtureConfigJSON: mixtureJSON,
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create mixture: %v", err)
	}
	got, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID mixture: %v", err)
	}
	if got == nil || got.Provider != models.ProviderMixture || got.MixtureConfigJSON != mixtureJSON {
		t.Fatalf("mixture fields not persisted: %+v", got)
	}
	got.MixtureConfigJSON = `{"enabled":false,"aggregator":{"agent_config_id":"agg"}}`
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update mixture: %v", err)
	}
	updated, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID updated mixture: %v", err)
	}
	if updated.MixtureConfigJSON != got.MixtureConfigJSON {
		t.Fatalf("updated mixture_config_json = %q", updated.MixtureConfigJSON)
	}
}

func TestLLMConfigRepo_OpenAICompatibleFieldsDoNotBleedAcrossRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	openRouter := &models.LLMConfig{
		Name:                  "OpenRouter",
		Provider:              models.ProviderOpenAICompatible,
		AuthMethod:            models.AuthMethodAPIKey,
		Model:                 "nvidia/nemotron-3-ultra-550b-a55b:free",
		APIKey:                "sk-or",
		BaseURL:               "https://openrouter.ai/api/v1/",
		Transport:             "chat_completions",
		PresetSlug:            "openrouter",
		ModelsURL:             "https://openrouter.ai/api/v1/models",
		AuthHeaderName:        "Authorization",
		AuthHeaderValuePrefix: "Bearer ",
		DefaultMaxTokens:      1234,
	}
	custom := &models.LLMConfig{
		Name:       "Custom Gateway",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "custom-model",
		APIKey:     "sk-custom",
		BaseURL:    "http://127.0.0.1:8000/v1/",
		Transport:  "chat_completions",
		PresetSlug: "custom",
	}

	if err := repo.Create(ctx, openRouter); err != nil {
		t.Fatalf("Create openRouter: %v", err)
	}
	if err := repo.Create(ctx, custom); err != nil {
		t.Fatalf("Create custom: %v", err)
	}

	gotOpenRouter, err := repo.GetByID(ctx, openRouter.ID)
	if err != nil {
		t.Fatalf("GetByID openRouter: %v", err)
	}
	gotCustom, err := repo.GetByID(ctx, custom.ID)
	if err != nil {
		t.Fatalf("GetByID custom: %v", err)
	}

	if gotOpenRouter.BaseURL != openRouter.BaseURL || gotOpenRouter.PresetSlug != "openrouter" || gotOpenRouter.DefaultMaxTokens != 1234 {
		t.Fatalf("openrouter fields not persisted: %+v", gotOpenRouter)
	}
	if gotCustom.BaseURL != custom.BaseURL || gotCustom.PresetSlug != "custom" {
		t.Fatalf("custom fields not persisted: %+v", gotCustom)
	}
	if gotCustom.BaseURL == gotOpenRouter.BaseURL {
		t.Fatalf("custom row reused stale base URL %q", gotCustom.BaseURL)
	}
}

func TestLLMConfigRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:        "Test Model",
		Provider:    models.ProviderAnthropic,
		Model:       "claude-sonnet-4-5-20250929",
		MaxTokens:   4096,
		Temperature: 0.5,
		IsDefault:   false,
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected ID to be set after Create")
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected model config, got nil")
	}
	if got.Name != "Test Model" {
		t.Errorf("expected Name=Test Model, got %q", got.Name)
	}
	if got.Provider != models.ProviderAnthropic {
		t.Errorf("expected Provider=anthropic, got %q", got.Provider)
	}
	if got.Temperature != 0.5 {
		t.Errorf("expected Temperature=0.5, got %f", got.Temperature)
	}
}

func TestLLMConfigRepo_Create_FirstModelAutoDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	a := &models.LLMConfig{
		Name:      "First Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected model config, got nil")
	}
	if !got.IsDefault {
		t.Fatal("expected first created model to be default")
	}

	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def == nil || def.ID != a.ID {
		t.Fatalf("expected default model ID %s, got %+v", a.ID, def)
	}
}

func TestLLMConfigRepo_GetDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Test fixtures seed a hermetic default model config.
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def == nil {
		t.Fatal("expected seeded default model config, got nil")
	}
	if def.Provider != models.ProviderTest {
		t.Errorf("expected default Provider=test, got %q", def.Provider)
	}
	if !def.IsDefault {
		t.Error("expected IsDefault=true")
	}
}

func TestLLMConfigRepo_CreateDefault_UnsetsOthers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a new default model config
	newConfig := &models.LLMConfig{
		Name:      "New Default",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-test",
		MaxTokens: 2048,
		IsDefault: true,
	}
	if err := repo.Create(ctx, newConfig); err != nil {
		t.Fatalf("Create new default: %v", err)
	}

	// Old default should no longer be default
	oldConfig, _ := repo.GetByID(ctx, original.ID)
	if oldConfig.IsDefault {
		t.Error("expected old model config IsDefault=false after new default created")
	}

	// New config should be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != newConfig.ID {
		t.Errorf("expected new default ID=%s, got %s", newConfig.ID, def.ID)
	}
}

func TestLLMConfigRepo_List(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Should have seeded default
	configs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(configs) < 1 {
		t.Fatal("expected at least 1 seeded model config")
	}

	// Add another
	repo.Create(ctx, &models.LLMConfig{
		Name:      "Second",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 1024,
	})

	configs, _ = repo.List(ctx)
	if len(configs) < 2 {
		t.Errorf("expected at least 2 model configs, got %d", len(configs))
	}
}

func TestLLMConfigRepo_Update(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:      "Original",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
	}
	repo.Create(ctx, a)

	a.Name = "Updated"
	a.MaxTokens = 8192
	if err := repo.Update(ctx, a); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := repo.GetByID(ctx, a.ID)
	if got.Name != "Updated" {
		t.Errorf("expected Name=Updated, got %q", got.Name)
	}
	if got.MaxTokens != 8192 {
		t.Errorf("expected MaxTokens=8192, got %d", got.MaxTokens)
	}
}

func TestLLMConfigRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:     "ToDelete",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	repo.Create(ctx, a)

	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(ctx, a.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestLLMConfigRepo_Delete_OnlyModelAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	only := &models.LLMConfig{
		Name:      "Only",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
	}
	if err := repo.Create(ctx, only); err != nil {
		t.Fatalf("Create only model: %v", err)
	}

	if err := repo.Delete(ctx, only.ID); err != nil {
		t.Fatalf("Delete only model: %v", err)
	}

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no models after delete, got %d", count)
	}
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def != nil {
		t.Fatal("expected no default model after deleting only model")
	}
}

func TestLLMConfigRepo_UpdateDefault_UnsetsOthers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a non-default model config
	second := &models.LLMConfig{
		Name:      "Second",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update second to be the default
	second.IsDefault = true
	if err := repo.Update(ctx, second); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Second should now be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != second.ID {
		t.Errorf("expected new default ID=%s, got %s", second.ID, def.ID)
	}

	// Original should no longer be default
	orig, _ := repo.GetByID(ctx, original.ID)
	if orig.IsDefault {
		t.Error("expected original model config IsDefault=false after update")
	}
}

func TestLLMConfigRepo_CreateNonDefault_PreservesExisting(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a non-default model config
	newConfig := &models.LLMConfig{
		Name:      "Non-Default",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-test",
		MaxTokens: 2048,
		IsDefault: false,
	}
	if err := repo.Create(ctx, newConfig); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Original should still be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != original.ID {
		t.Errorf("expected original default ID=%s preserved, got %s", original.ID, def.ID)
	}
}

func TestLLMConfigRepo_GetByID_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByID should not error on not found: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestLLMConfigRepo_Delete_WithTaskReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmRepo := NewLLMConfigRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a non-default model config to delete
	config := &models.LLMConfig{
		Name:     "Config To Delete",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	if err := llmRepo.Create(ctx, config); err != nil {
		t.Fatalf("Create model config: %v", err)
	}

	// Create a project for the task
	proj := &models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, proj); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create a task referencing the model config
	configID := config.ID
	task := &models.Task{
		ProjectID: proj.ID,
		Title:     "Test Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		AgentID:   &configID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Verify task has agent_id set
	gotTask, _ := taskRepo.GetByID(ctx, task.ID)
	if gotTask.AgentID == nil || *gotTask.AgentID != config.ID {
		t.Fatal("expected task to have agent_id set")
	}

	// Delete the model config - should succeed by nullifying references
	if err := llmRepo.Delete(ctx, config.ID); err != nil {
		t.Fatalf("Delete model config with task references: %v", err)
	}

	// Verify model config is deleted
	gotConfig, _ := llmRepo.GetByID(ctx, config.ID)
	if gotConfig != nil {
		t.Error("expected model config to be deleted")
	}

	// Verify task still exists but agent_id is NULL
	gotTask, _ = taskRepo.GetByID(ctx, task.ID)
	if gotTask == nil {
		t.Fatal("expected task to still exist after model config deletion")
	}
	if gotTask.AgentID != nil {
		t.Errorf("expected task agent_id to be NULL after model config deletion, got %v", *gotTask.AgentID)
	}
}

func TestLLMConfigRepo_Delete_WithExecutionReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmRepo := NewLLMConfigRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	// Create a non-default model config
	config := &models.LLMConfig{
		Name:     "Config With Executions",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	if err := llmRepo.Create(ctx, config); err != nil {
		t.Fatalf("Create model config: %v", err)
	}

	// Create a project and task
	proj := &models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, proj); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	task := &models.Task{
		ProjectID: proj.ID,
		Title:     "Test Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Create an execution referencing the model config
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: config.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create execution: %v", err)
	}

	// Delete the model config - should succeed by nullifying execution references
	if err := llmRepo.Delete(ctx, config.ID); err != nil {
		t.Fatalf("Delete model config with execution references: %v", err)
	}

	// Verify model config is deleted
	gotConfig, _ := llmRepo.GetByID(ctx, config.ID)
	if gotConfig != nil {
		t.Error("expected model config to be deleted")
	}

	// Verify execution still exists but agent_config_id is empty
	gotExec, _ := execRepo.GetByID(ctx, exec.ID)
	if gotExec == nil {
		t.Fatal("expected execution to still exist after model config deletion")
	}
	if gotExec.AgentConfigID != "" {
		t.Errorf("expected execution agent_config_id to be empty, got %v", gotExec.AgentConfigID)
	}
}

func TestLLMConfigRepo_DeleteResetsProtectedAgentOverridesAcrossPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		delete func(t *testing.T, repo *LLMConfigRepo, target, replacement *models.LLMConfig) error
	}{
		{
			name: "single",
			delete: func(t *testing.T, repo *LLMConfigRepo, target, _ *models.LLMConfig) error {
				return repo.Delete(context.Background(), target.ID)
			},
		},
		{
			name: "bulk",
			delete: func(t *testing.T, repo *LLMConfigRepo, target, _ *models.LLMConfig) error {
				return repo.DeleteBulk(context.Background(), []string{target.ID})
			},
		},
		{
			name: "default transfer",
			delete: func(t *testing.T, repo *LLMConfigRepo, target, replacement *models.LLMConfig) error {
				return repo.TransferDefaultAndDelete(context.Background(), target.ID, replacement.ID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := NewLLMConfigRepo(db)
			agentRepo := NewAgentRepo(db)
			ctx := context.Background()

			target, err := repo.GetDefault(ctx)
			if err != nil || target == nil {
				t.Fatalf("get default model: %v", err)
			}
			if tc.name != "default transfer" {
				target = &models.LLMConfig{Name: "Agent override target", Provider: models.ProviderTest, Model: "target-model"}
				if err := repo.Create(ctx, target); err != nil {
					t.Fatalf("create target model: %v", err)
				}
			}
			replacement := &models.LLMConfig{Name: "Replacement default", Provider: models.ProviderTest, Model: "replacement-model"}
			if err := repo.Create(ctx, replacement); err != nil {
				t.Fatalf("create replacement model: %v", err)
			}

			agents := []*models.Agent{
				{Key: "goal_override_1168", Name: "Goal Agent Override", SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, Model: target.ID},
				{Key: "skill_curator_override_1168", Name: "Skill Curator Override", SystemKind: models.AgentSystemKindSkillCurator, GeneratedStatus: models.AgentStatusProtected, Model: target.ID},
				{Key: "memory_curator_override_1168", Name: "Memory Curator Override", SystemKind: models.AgentSystemKindMemoryCurator, GeneratedStatus: models.AgentStatusProtected, Model: target.ID},
			}
			for _, agent := range agents {
				if err := agentRepo.Create(ctx, agent); err != nil {
					t.Fatalf("create %s: %v", agent.Name, err)
				}
			}

			if err := tc.delete(t, repo, target, replacement); err != nil {
				t.Fatalf("delete path: %v", err)
			}
			for _, agent := range agents {
				got, err := agentRepo.GetByID(ctx, agent.ID)
				if err != nil {
					t.Fatalf("get %s: %v", agent.Name, err)
				}
				if got == nil {
					t.Fatalf("%s was not found after model deletion", agent.Name)
				}
				if got.Model != "inherit" {
					t.Fatalf("%s model = %q, want inherit", agent.Name, got.Model)
				}
			}
		})
	}
}

func TestLLMConfigRepo_Count(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Should have at least the seeded default
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count < 1 {
		t.Fatalf("expected count >= 1, got %d", count)
	}

	// Add another model
	repo.Create(ctx, &models.LLMConfig{
		Name:     "Extra",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	})

	count2, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count after create: %v", err)
	}
	if count2 != count+1 {
		t.Errorf("expected count=%d, got %d", count+1, count2)
	}
}

func TestLLMConfigRepo_TransferDefaultAndDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Get the seeded default
	original, err := repo.GetDefault(ctx)
	if err != nil || original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a second non-default model
	second := &models.LLMConfig{
		Name:      "Second Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create second model: %v", err)
	}

	// Transfer default from original to second, then delete original
	if err := repo.TransferDefaultAndDelete(ctx, original.ID, second.ID); err != nil {
		t.Fatalf("TransferDefaultAndDelete: %v", err)
	}

	// Original should be deleted
	got, _ := repo.GetByID(ctx, original.ID)
	if got != nil {
		t.Error("expected original model to be deleted")
	}

	// Second should now be the default
	newDefault, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after transfer: %v", err)
	}
	if newDefault == nil {
		t.Fatal("expected a default model after transfer")
	}
	if newDefault.ID != second.ID {
		t.Errorf("expected new default ID=%s, got %s", second.ID, newDefault.ID)
	}
	if !newDefault.IsDefault {
		t.Error("expected new default IsDefault=true")
	}
}

func TestLLMConfigRepo_Delete_DefaultAutoReassigns(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	originalDefault, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if originalDefault == nil {
		t.Fatal("expected seeded default model config")
	}

	replacement := &models.LLMConfig{
		Name:      "Replacement",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 1024,
		IsDefault: false,
	}
	if err := repo.Create(ctx, replacement); err != nil {
		t.Fatalf("Create replacement model: %v", err)
	}

	if err := repo.Delete(ctx, originalDefault.ID); err != nil {
		t.Fatalf("Delete original default: %v", err)
	}

	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after delete: %v", err)
	}
	if def == nil {
		t.Fatal("expected default model after deleting previous default")
	}
	if def.ID != replacement.ID {
		t.Fatalf("expected replacement model %s to become default, got %s", replacement.ID, def.ID)
	}
}

func TestLLMConfigRepo_GetByIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create two configs
	a := &models.LLMConfig{Name: "Alpha", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-5-20250929"}
	b := &models.LLMConfig{Name: "Beta", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-5-20250929"}
	repo.Create(ctx, a)
	repo.Create(ctx, b)

	// Batch get both
	result, err := repo.GetByIDs(ctx, []string{a.ID, b.ID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result))
	}
	if result[a.ID].Name != "Alpha" {
		t.Errorf("expected Alpha, got %q", result[a.ID].Name)
	}
	if result[b.ID].Name != "Beta" {
		t.Errorf("expected Beta, got %q", result[b.ID].Name)
	}

	// Nonexistent ID should not appear
	result2, err := repo.GetByIDs(ctx, []string{a.ID, "nonexistent"})
	if err != nil {
		t.Fatalf("GetByIDs with nonexistent: %v", err)
	}
	if len(result2) != 1 {
		t.Fatalf("expected 1 config, got %d", len(result2))
	}

	// Empty input
	result3, err := repo.GetByIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("GetByIDs empty: %v", err)
	}
	if len(result3) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(result3))
	}
}

func TestLLMConfigRepo_TransferDefaultAndDelete_PreservesOtherModels(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Get the seeded default
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create two more models
	second := &models.LLMConfig{
		Name:     "Second",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	third := &models.LLMConfig{
		Name:     "Third",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	repo.Create(ctx, second)
	repo.Create(ctx, third)

	// Transfer default to second, delete original
	if err := repo.TransferDefaultAndDelete(ctx, original.ID, second.ID); err != nil {
		t.Fatalf("TransferDefaultAndDelete: %v", err)
	}

	// Third should still exist and not be default
	gotThird, _ := repo.GetByID(ctx, third.ID)
	if gotThird == nil {
		t.Fatal("expected third model to still exist")
	}
	if gotThird.IsDefault {
		t.Error("expected third model to not be default")
	}

	// Total count should be 2
	count, _ := repo.Count(ctx)
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}
