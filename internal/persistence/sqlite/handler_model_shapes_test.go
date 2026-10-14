package sqlite

import (
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestHandlerModelQueryShapes(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := t.Context()
	c.Reset()
	c.SetEnabled(true)
	_, err := repo.ListPickerOptions(ctx)
	require.NoError(t, err)
	assertCompactAgentPickerQueries(t, c.Statements(), 1)
	_, err = repo.GetDefault(ctx)
	require.NoError(t, err)
	assertGenerateAgentLoadedFullDefaultModel(t, c.Statements())
	c.Reset()
	_, err = repo.ListCardsPageFiltered(ctx, 20, 0, persistence.ModelCardListFilter{})
	require.NoError(t, err)
	assertCompactModelsRefreshQuery(t, c.Statements())
	c.Reset()
	_, err = repo.ListRuntimeSummaries(ctx)
	require.NoError(t, err)
	_, err = repo.ListRuntimeSummaries(ctx)
	require.NoError(t, err)
	_, err = repo.GetRuntimeSummary(ctx, "", "missing")
	require.NoError(t, err)
	assertChatModelStatusStatementsCompact(t, c.Statements())
}
func assertCompactAgentPickerQueries(t *testing.T, statements []string, minQueries int) {
	t.Helper()
	queries := 0
	for _, statement := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if !strings.Contains(stmt, " from agent_configs order by is_default desc, name asc") {
			continue
		}
		queries++
		projection := strings.Split(stmt, " from agent_configs ")[0]
		if !strings.Contains(projection, "select id, name, model, provider, preset_slug, reasoning_effort, is_default") {
			t.Fatalf("agent picker projection = %q, want model identity, grouping, and effort metadata in %s", projection, statement)
		}
		for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "oauth_config_revision", "mixture_config_json", "created_at", "updated_at", "max_workers", "worker_timeout", "auto_start_tasks"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("agent picker query selected forbidden column %q: %s", forbidden, statement)
			}
		}
	}
	if queries < minQueries {
		t.Fatalf("found %d compact agent picker queries, want at least %d; statements: %#v", queries, minQueries, statements)
	}
}

func assertGenerateAgentLoadedFullDefaultModel(t *testing.T, statements []string) {
	t.Helper()
	for _, statement := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(stmt, " from agent_configs where is_default = 1 limit 1") || strings.Contains(stmt, " from agent_configs a left join oauth_connections") && strings.Contains(stmt, " where a.is_default = 1 limit 1") {
			projection := strings.Split(stmt, " from agent_configs")[0]
			if strings.Contains(projection, "api_key") && strings.Contains(projection, "extra_body_json") {
				return
			}
			t.Fatalf("GenerateAgent default-model query did not use full execution projection: %s", statement)
		}
	}
	t.Fatalf("GenerateAgent did not load the full default model; statements: %#v", statements)
}

func assertCompactModelsRefreshQuery(t *testing.T, statements []string) {
	t.Helper()
	var refreshStatements []string
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(statement), " ")
		if strings.Contains(normalized, "FROM agent_configs AS configs WHERE 1=1 ORDER BY name COLLATE NOCASE ASC, name ASC, id ASC") {
			refreshStatements = append(refreshStatements, normalized)
		}
	}
	if len(refreshStatements) != 1 {
		t.Fatalf("expected exactly one model-card refresh query, got %d in statements: %q", len(refreshStatements), statements)
	}
	refresh := refreshStatements[0]
	for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json"} {
		if strings.Contains(refresh, forbidden) {
			t.Fatalf("model refresh query selected edit-only column %q: %s", forbidden, refresh)
		}
	}
	for _, required := range []string{"CASE WHEN api_key != '' THEN 1 ELSE 0 END", "json_array_length"} {
		if !strings.Contains(refresh, required) {
			t.Fatalf("model refresh query does not look like compact card projection; missing %q in %s", required, refresh)
		}
	}
}

func assertChatModelStatusStatementsCompact(t *testing.T, statements []string) {
	t.Helper()
	var runtimeListQueries, targetedLookupQueries int
	for _, raw := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if !strings.Contains(stmt, " from agent_configs") {
			continue
		}
		projection := strings.Split(stmt, " from agent_configs")[0]
		for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("chat model/status query selected forbidden column %q: %s", forbidden, raw)
			}
		}
		if strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") && strings.Contains(stmt, "where name = ? collate nocase") {
			targetedLookupQueries++
			continue
		}
		if strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") && strings.Contains(stmt, "order by is_default desc, name asc") && !strings.Contains(stmt, " where ") {
			runtimeListQueries++
			continue
		}
		if strings.Contains(projection, "select id, name, provider, model, reasoning_effort") {
			t.Fatalf("chat model/status tool used full model list query: %s", raw)
		}
	}
	if runtimeListQueries != 2 {
		t.Fatalf("runtime list compact query count = %d, want 2; statements: %#v", runtimeListQueries, statements)
	}
	if targetedLookupQueries != 1 {
		t.Fatalf("targeted lookup compact query count = %d, want 1; statements: %#v", targetedLookupQueries, statements)
	}
}
