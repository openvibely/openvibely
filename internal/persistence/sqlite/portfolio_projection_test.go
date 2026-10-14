package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestPortfolioCardQueriesOmitGraphPayloads(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Automations
	counter.SetEnabled(true)
	_, err := repo.ListPortfolioCards(t.Context(), "default")
	require.NoError(t, err)
	_, err = repo.PortfolioOperationalCounts(t.Context(), "default", time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	counter.SetEnabled(false)
	require.Len(t, counter.Statements(), 2)
	statements := strings.ToLower(strings.Join(counter.Statements(), "\n"))
	require.Contains(t, statements, "select count(*) from automation_nodes")
	require.Contains(t, statements, "from automation_activities")
	for _, hidden := range []string{"automation_edges", "automation_definition_resources", "schedules", "config_json", "condition_json"} {
		require.NotContains(t, statements, hidden)
	}
}
