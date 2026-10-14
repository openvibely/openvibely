package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestTaskDetailActionsExactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Tasks
	counter.SetEnabled(true)
	_, err := repo.GetDetailActionMetadata(t.Context(), "missing")
	counter.SetEnabled(false)
	require.NoError(t, err)
	assertTaskDetailActionsProjectionQuery(t, counter)
}
func assertTaskDetailActionsProjectionQuery(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	var taskStatements []string
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from tasks") {
			taskStatements = append(taskStatements, statement)
		}
	}
	if len(taskStatements) != 1 {
		t.Fatalf("task statements = %#v, want one detail-actions metadata query", taskStatements)
	}

	query := strings.ToLower(strings.Join(strings.Fields(taskStatements[0]), " "))
	projection, _, found := strings.Cut(query, " from tasks")
	if !found {
		t.Fatalf("task metadata query missing FROM tasks: %s", taskStatements[0])
	}
	if projection != "select id, status" {
		t.Fatalf("task metadata projection = %q, want only id and status", projection)
	}
	for _, forbidden := range []string{
		"prompt", "chain_config", "swarm_config", "worktree_path", "worktree_branch",
		"auto_merge", "merge_target_branch", "merge_status", "base_branch", "base_commit_sha", "lineage_depth",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("task metadata query selected forbidden field %q: %s", forbidden, taskStatements[0])
		}
	}
}
