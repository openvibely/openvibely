package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestHandlerModelExactProjections(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	r := sqlite.NewLLMConfigRepo(db)
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		read  func() error
		check func(*testing.T, []string)
	}{
		{"dialog", func() error { _, e := r.ListChatSelectionOptions(ctx); return e }, assertProjectDialogModelProjection},
		{"creation", func() error { _, e := r.ListTaskCreationSelectionOptions(ctx); return e }, assertCreateTaskRuntimeUsesCompactModelSelection},
		{"worker", func() error { _, e := r.ListWorkerCapacities(ctx); return e }, assertWorkerModelProjection},
		{"schedule", func() error { _, e := r.ListBadgeOptions(ctx); return e }, assertScheduleModelProjection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.Reset()
			c.SetEnabled(true)
			require.NoError(t, tc.read())
			c.SetEnabled(false)
			tc.check(t, c.Statements())
		})
	}
	m := models.LLMConfig{Name: "projection", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, r.Create(ctx, &m))
	for _, vision := range []bool{false, true} {
		c.Reset()
		c.SetEnabled(true)
		if vision {
			_, err := r.ListVisionSelectionOptions(ctx)
			require.NoError(t, err)
		} else {
			_, err := r.ListChatSelectionOptions(ctx)
			require.NoError(t, err)
		}
		_, err := r.GetByID(ctx, m.ID)
		require.NoError(t, err)
		c.SetEnabled(false)
		assertAutoSelectionQueryShape(t, c.Statements(), vision)
		require.Equal(t, 2, countNewTaskSelectionQueries(c.Statements()))
	}
}
func assertProjectDialogModelProjection(t *testing.T, statements []string) {
	t.Helper()
	var modelQueries []string
	for _, statement := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(stmt, " from agent_configs ") {
			modelQueries = append(modelQueries, statement)
		}
	}
	if len(modelQueries) != 1 {
		t.Fatalf("agent_configs statements = %#v, want exactly one compact dialog model query", modelQueries)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(modelQueries[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if !strings.Contains(projection, "select id, name, provider, model, is_default") {
		t.Fatalf("project dialog model projection = %q, want id/name/provider/model/is_default in %s", projection, modelQueries[0])
	}
	for _, forbidden := range []string{"reasoning_effort", "api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "oauth_scopes", "ollama_base_url", "base_url", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "created_at", "updated_at", "max_workers", "worker_timeout", "auto_start_tasks"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("project dialog query selected forbidden column %q: %s", forbidden, modelQueries[0])
		}
	}
	if !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("project dialog query must preserve default/name ordering: %s", modelQueries[0])
	}
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
		if projection == "select id, name, provider, model, is_default, auto_start_tasks, auth_method" && strings.Contains(stmt, "order by is_default desc, name asc") {
			compactQueries++
		}
	}
	if compactQueries != 1 {
		t.Fatalf("compact task creation model query count = %d, want 1; statements: %#v", compactQueries, statements)
	}
}

func assertAutoSelectionQueryShape(t *testing.T, statements []string, hasImages bool) {
	t.Helper()
	var modelSelects []string
	for _, stmt := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(stmt), " "))
		if strings.HasPrefix(normalized, "select ") && strings.Contains(normalized, " from agent_configs") {
			modelSelects = append(modelSelects, normalized)
		}
	}
	if len(modelSelects) != 2 {
		t.Fatalf("model select statements = %#v, want compact catalog plus selected GetByID", statements)
	}
	catalog, hydrate := modelSelects[0], modelSelects[1]
	if strings.Contains(catalog, "left join") || strings.Contains(catalog, " from agent_configs a") {
		t.Fatalf("auto-selection catalog query used full model shape: %s", catalog)
	}
	if !strings.Contains(catalog, "order by is_default desc, name asc") {
		t.Fatalf("auto-selection catalog query lost default/name ordering: %s", catalog)
	}
	if hasImages {
		wantProjection := "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '') != '' then 1 else 0 end, case when coalesce(oauth_connection_id, '') != '' then exists(select 1 from oauth_connections c where c.id = oauth_connection_id and c.oauth_access_token != '') else coalesce(oauth_access_token, '') != '' end"
		projection := strings.Split(catalog, " from agent_configs ")[0]
		if projection != wantProjection {
			t.Fatalf("vision auto-selection projection = %q, want %q", projection, wantProjection)
		}
	} else {
		wantProjection := "select id, name, provider, model, is_default, auth_method"
		projection := strings.Split(catalog, " from agent_configs ")[0]
		if projection != wantProjection {
			t.Fatalf("chat auto-selection projection = %q, want %q", projection, wantProjection)
		}
	}
	for _, forbidden := range autoSelectionCatalogForbiddenColumns(hasImages) {
		if strings.Contains(catalog, forbidden) {
			t.Fatalf("auto-selection catalog selected forbidden column %q: %s", forbidden, catalog)
		}
	}
	if !strings.Contains(hydrate, "left join oauth_connections") || !strings.Contains(hydrate, "extra_body_json") || !strings.Contains(hydrate, "mixture_config_json") || !strings.Contains(hydrate, "oauth_client_secret") {
		t.Fatalf("selected model hydration query was not full GetByID shape: %s", hydrate)
	}
}

func autoSelectionCatalogForbiddenColumns(hasImages bool) []string {
	forbidden := []string{
		"oauth_refresh_token", "oauth_client_id", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "oauth_scopes",
		"ollama_base_url", "base_url", "transport", "preset_slug", "models_url", "auth_header_name", "auth_header_value_prefix",
		"extra_headers_json", "extra_body_json", "default_max_tokens", "context_window", "compaction_threshold",
		"token_exchange_format", "token_refresh_format", "custom_auth_config_json", "custom_auth_state_json", "oauth_config_revision",
		"mixture_config_json", "auto_start_tasks", "created_at", "updated_at", "max_tokens", "temperature", "reasoning_effort",
		"max_workers", "worker_timeout",
	}
	if !hasImages {
		forbidden = append(forbidden, "api_key", "oauth_access_token", "oauth_connection_id")
	}
	return forbidden
}

func assertWorkerModelProjection(t *testing.T, statements []string) {
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact worker capacity query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if projection != "select id, name, model, max_workers" {
		t.Fatalf("model capacity projection = %q, want compact worker fields in %s", projection, statements[0])
	}
	if !strings.Contains(stmt, "where max_workers > 0") || !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("model capacity query must filter and preserve ordering: %s", statements[0])
	}
	for _, forbidden := range []string{
		"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url",
		"oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json",
		"custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "worker_timeout",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("model capacity query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
}

func assertScheduleModelProjection(t *testing.T, statements []string) {
	var modelQueries []string
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(statement), " ")
		if strings.Contains(normalized, "FROM agent_configs ORDER BY is_default DESC, name ASC") {
			modelQueries = append(modelQueries, normalized)
		}
	}
	if len(modelQueries) != 1 {
		t.Fatalf("expected exactly one schedule model query, got %d in %q", len(modelQueries), statements)
	}
	projection := strings.ToLower(strings.Split(modelQueries[0], " from agent_configs ")[0])
	if !strings.Contains(projection, "select id, name, model, is_default") {
		t.Fatalf("schedule model query does not use compact badge projection: %s", modelQueries[0])
	}
	for _, forbidden := range []string{
		"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret",
		"oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url",
		"extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json",
		"mixture_config_json", "created_at", "updated_at", "max_workers", "worker_timeout",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("schedule model query selected forbidden column %q: %s", forbidden, modelQueries[0])
		}
	}
}

func countNewTaskSelectionQueries(statements []string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.HasPrefix(normalized, "select id, name, provider, model, is_default, auth_method from agent_configs order by is_default desc, name asc") ||
			strings.HasPrefix(normalized, "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '')") ||
			strings.HasPrefix(normalized, "select a.id, a.name, a.provider, a.model, a.reasoning_effort,") {
			count++
		}
	}
	return count
}
