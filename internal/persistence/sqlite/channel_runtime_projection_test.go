package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestChannelRuntimeModelProjections(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	r := sqlite.NewStores(db, nil).LLMConfigs
	c.SetEnabled(true)
	for i := 0; i < 2; i++ {
		if _, err := r.ListRuntimeSummaries(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.GetRuntimeSummary(t.Context(), "", "missing"); err != nil {
		t.Fatal(err)
	}
	c.SetEnabled(false)
	assertChannelModelStatusStatementsCompact(t, c.Statements())
	c.Reset()
	c.SetEnabled(true)
	if _, err := r.ListTaskCreationSelectionOptions(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.SetEnabled(false)
	assertChannelCreateTaskUsesCompactModelSelection(t, c.Statements())
}
func TestChannelExecutionPageQueryShape(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	r := sqlite.NewStores(db, nil).Executions
	c.SetEnabled(true)
	if _, err := r.CountByTask(t.Context(), "missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListByTaskChronologicalPage(t.Context(), "missing", 5, 3); err != nil {
		t.Fatal(err)
	}
	c.SetEnabled(false)
	q := c.Statements()
	if len(q) != 2 || !strings.Contains(q[0], "COUNT(*)") || !strings.Contains(q[1], "ORDER BY started_at ASC, rowid ASC LIMIT ? OFFSET ?") {
		t.Fatalf("unexpected page queries: %v", q)
	}
}
func assertChannelModelStatusStatementsCompact(t *testing.T, statements []string) {
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
				t.Fatalf("channel model/status query selected forbidden column %q: %s", forbidden, raw)
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
			t.Fatalf("channel model/status tool used full model list query: %s", raw)
		}
	}
	if runtimeListQueries != 2 {
		t.Fatalf("runtime list compact query count = %d, want 2; statements: %#v", runtimeListQueries, statements)
	}
	if targetedLookupQueries != 1 {
		t.Fatalf("targeted lookup compact query count = %d, want 1; statements: %#v", targetedLookupQueries, statements)
	}
}

func assertChannelCreateTaskUsesCompactModelSelection(t *testing.T, statements []string) {
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
				t.Fatalf("channel create_task model query selected forbidden column %q: %s", forbidden, raw)
			}
		}
		if strings.Contains(projection, "select id, name, provider, model, reasoning_effort") {
			t.Fatalf("channel create_task used full model list query: %s", raw)
		}
		if projection == "select id, name, provider, model, is_default, auto_start_tasks, auth_method" && strings.Contains(stmt, "order by is_default desc, name asc") {
			compactQueries++
		}
	}
	if compactQueries != 1 {
		t.Fatalf("compact task creation model query count = %d, want 1; statements: %#v", compactQueries, statements)
	}
}
