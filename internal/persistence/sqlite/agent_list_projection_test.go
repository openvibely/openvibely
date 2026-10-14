package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestAgentListProjectionOmitsDetailsAndExtractsSkillNames(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Agents
	counter.SetEnabled(true)
	if _, err := repo.ListAgentListSummaries(t.Context()); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	statement := strings.ToLower(statements[0])
	projection := strings.SplitN(statement, "from agents", 2)[0]
	for _, forbidden := range []string{
		"system_prompt", "model", "tools", "tool_config", "plugins", "mcp_servers",
		"permission_defaults_json", "model_defaults_json", "source_refs_json",
		"created_at", "updated_at",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("compact agent-list query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	for _, required := range []string{"json_each", "json_extract", "json_group_array"} {
		if !strings.Contains(statement, required) {
			t.Fatalf("compact agent-list query must derive skill names with %s: %s", required, statements[0])
		}
	}

}
