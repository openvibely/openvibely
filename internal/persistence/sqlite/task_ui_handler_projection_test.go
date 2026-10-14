package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestTaskUIHandlerExactProjection(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	r := sqlite.NewStores(db, nil).Agents
	c.Reset()
	c.SetEnabled(true)
	_, err := r.ListTaskUIOptions(t.Context())
	require.NoError(t, err)
	c.SetEnabled(false)
	assertTaskUIAgentProjectionQuery(t, c)
}
func assertTaskUIAgentProjectionQuery(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	var agentStatements []string
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			agentStatements = append(agentStatements, statement)
		}
	}
	if len(agentStatements) != 1 {
		t.Fatalf("Agent statements = %#v, want one compact catalog query", agentStatements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(agentStatements[0]), " "))
	projection := strings.Split(query, " from agents ")[0]
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("Task UI query selected forbidden Agent column %q: %s", forbidden, agentStatements[0])
		}
	}
}
