package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestAutomationAgentValidationProjection(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Agents
	c.SetEnabled(true)
	if _, err := repo.ListSelectableReferencesForProject(t.Context(), "default", 50); err != nil {
		t.Fatal(err)
	}
	c.SetEnabled(false)
	statements := c.Statements()
	if len(statements) != 1 {
		t.Fatalf("queries: %v", statements)
	}
	assertAutomationValidationProjection(t, statements[0])
	query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.TrimSpace(strings.SplitN(query, " from agents", 2)[0])
	for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("validation projection = %q, want %q in %s", projection, required, statements[0])
		}
	}
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("validation projection selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	for _, requiredPredicate := range []string{"coalesce(generated_status, 'user_edited') <> 'archived'", "archived_at is null", "coalesce(enabled, 1) = 1", "coalesce(selectable_as_primary, 1) = 1", "project_id is null or project_id = '' or project_id = ?", "order by name asc, id asc", "limit ?"} {
		if !strings.Contains(query, requiredPredicate) {
			t.Fatalf("validation query is missing %q: %s", requiredPredicate, statements[0])
		}
	}
}
func assertAutomationValidationProjection(t *testing.T, statement string) {
	t.Helper()
	query := strings.ToLower(strings.Join(strings.Fields(statement), " "))
	projection := strings.TrimSpace(strings.SplitN(query, " from agents", 2)[0])
	for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("validation projection = %q, want %q in %s", projection, required, statement)
		}
	}
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("validation projection selected forbidden column %q: %s", forbidden, statement)
		}
	}
}
