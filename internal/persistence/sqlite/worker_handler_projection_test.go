package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestWorkerHandlerExactProjection(t *testing.T) {
	db, c := testutil.NewStatementCountingTestDB(t)
	r := sqlite.NewStores(db, nil)
	c.Reset()
	c.SetEnabled(true)
	_, err := r.Projects.ListWorkerCapacityProjects(t.Context())
	require.NoError(t, err)
	_, err = r.Tasks.CountPendingByProject(t.Context())
	require.NoError(t, err)
	c.SetEnabled(false)
	assertWorkerCapacityProjectProjection(t, c)
	require.Len(t, c.Statements(), 2)
	require.Equal(t, "select project_id, count(*) from tasks where category = 'active' and status in ('pending', 'queued') group by project_id", strings.ToLower(strings.Join(strings.Fields(c.Statements()[1]), " ")))
}
func assertWorkerCapacityProjectProjection(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	const compact = "select id, name, max_workers from projects order by is_default desc, name asc"
	const selector = "select id, name, is_default from projects order by is_default desc, name asc, id asc"

	compactCount := 0
	for _, statement := range counter.Statements() {
		query := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if !strings.Contains(query, "from projects") || !strings.Contains(query, "order by is_default desc, name asc") {
			continue
		}
		switch query {
		case compact:
			compactCount++
		case selector:
			// The initial full-page render separately loads the sidebar selector.
		default:
			t.Fatalf("Workers capacity render used an unexpected ordered project query: %q", query)
		}
	}
	if compactCount != 1 {
		t.Fatalf("Workers capacity render used %d compact project queries, want 1; statements: %q", compactCount, counter.Statements())
	}
}
