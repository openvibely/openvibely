package repository

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func seedLargeCustomProviderModelConfigs(tb testing.TB, ctx context.Context, repo *LLMConfigRepo, count int) {
	tb.Helper()
	largeBody := strings.Repeat("x", 64*1024)
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
		}
		if err := repo.Create(ctx, cfg); err != nil {
			tb.Fatalf("Create large model config %d: %v", i, err)
		}
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

func TestLLMConfigRepo_HasAnyIsFasterAndLowerAllocationThanListOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark ratio assertion in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	fullList := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 {
				b.Fatalf("List returned %d configs, want 50", len(configs))
			}
		}
	})
	hasAny := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			exists, err := repo.HasAny(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if !exists {
				b.Fatal("HasAny returned false for non-empty fixture")
			}
		}
	})

	t.Logf("List: %d ns/op, %d B/op; HasAny: %d ns/op, %d B/op", fullList.NsPerOp(), fullList.AllocedBytesPerOp(), hasAny.NsPerOp(), hasAny.AllocedBytesPerOp())
	if fullList.NsPerOp() < hasAny.NsPerOp()*50 {
		t.Fatalf("HasAny is not at least 50x faster: List %d ns/op, HasAny %d ns/op", fullList.NsPerOp(), hasAny.NsPerOp())
	}
	if fullList.AllocedBytesPerOp() < hasAny.AllocedBytesPerOp()*50 {
		t.Fatalf("HasAny is not at least 50x lower allocation: List %d B/op, HasAny %d B/op", fullList.AllocedBytesPerOp(), hasAny.AllocedBytesPerOp())
	}
}

func BenchmarkLLMConfigRepoHasAnyVsListLargeCustomProviders(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.Run("full_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 {
				b.Fatalf("expected 50 configs, got %d", len(configs))
			}
		}
	})
	b.Run("has_any", func(b *testing.B) {
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
	})
}

func BenchmarkLLMConfigRepoListFullVsCardsLargeCustomProviders(b *testing.B) {
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

	b.Run("full_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) < 50 {
				b.Fatalf("expected production-shaped fixture, got %d configs", len(configs))
			}
		}
	})
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

func TestLLMConfigRepo_APIChatSelectionProjectionIsFasterAndLowerAllocationThanFullListOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark ratio assertion in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(t, ctx, repo, 50)

	fullList := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatalf("full selection fixture returned %d configs", len(configs))
			}
		}
	})
	compactThenGet := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListChatSelectionOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatalf("compact selection fixture returned %d configs", len(configs))
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
	})

	t.Logf("full List+select: %d ns/op, %d B/op; compact selection+GetByID: %d ns/op, %d B/op", fullList.NsPerOp(), fullList.AllocedBytesPerOp(), compactThenGet.NsPerOp(), compactThenGet.AllocedBytesPerOp())
	if fullList.NsPerOp() < compactThenGet.NsPerOp()*20 {
		t.Fatalf("compact selection is not at least 20x faster: full %d ns/op, compact %d ns/op", fullList.NsPerOp(), compactThenGet.NsPerOp())
	}
	if fullList.AllocedBytesPerOp() < compactThenGet.AllocedBytesPerOp()*20 {
		t.Fatalf("compact selection is not at least 20x lower allocation: full %d B/op, compact %d B/op", fullList.AllocedBytesPerOp(), compactThenGet.AllocedBytesPerOp())
	}
}

func BenchmarkAPIChatModelSelectionFullListVsCompactSelectionThenGet(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		b.Fatalf("clear model configs: %v", err)
	}
	seedLargeCustomProviderModelConfigs(b, ctx, repo, 50)

	b.Run("full_list", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatalf("full selection fixture returned %d configs", len(configs))
			}
		}
	})
	b.Run("compact_selection_plus_get_by_id", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListChatSelectionOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatalf("compact selection fixture returned %d configs", len(configs))
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
	})
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
