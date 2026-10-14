package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

// Exact projection checks belong to the backend; application tests measure physical reads.
func TestTaskDetailReadShapes(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	checks := []struct {
		name      string
		read      func() error
		prefix    string
		forbidden []string
	}{
		{"thread metadata", func() error { _, err := NewTaskRepo(db, nil).GetThreadRenderMetadata(ctx, "missing"); return err }, "select id, project_id, category, status, agent_id, agent_definition_id", []string{"prompt", "chain_config", "swarm_config"}},
		{"agent picker", func() error { _, err := NewAgentRepo(db).ListPickerOptionsForProject(ctx, "default"); return err }, "select id, name", []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"}},
		{"execution metrics", func() error { _, err := NewExecutionRepo(db).GetTaskExecutionMetrics(ctx, "missing"); return err }, "select", []string{"prompt_sent", "output", "error_message", "reasoning_content", "diff_output", "order by started_at asc, rowid asc"}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			require.NoError(t, check.read())
			counter.SetEnabled(false)
			statements := counter.Statements()
			require.Len(t, statements, 1)
			query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
			require.True(t, strings.HasPrefix(query, check.prefix), query)
			projection := strings.Split(query, " from agents ")[0]
			for _, field := range check.forbidden {
				require.NotContains(t, projection, field)
			}
			if check.name == "execution metrics" {
				require.Contains(t, query, "latest_started_at")
				require.Contains(t, query, "latest_duration_ms")
			}
		})
	}
}
