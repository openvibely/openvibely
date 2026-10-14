package sqlite

import (
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestProjectSwitchSelectorSQLShapeAndPlan(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewProjectRepo(db)
	counter.SetEnabled(true)
	_, err := repo.ListSelectorOptions(t.Context())
	require.NoError(t, err)
	counter.SetEnabled(false)
	statements := counter.Statements()
	require.Len(t, statements, 1)
	normalized := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	require.Equal(t, "select id, name, is_default from projects order by is_default desc, name asc, id asc", normalized)
	rows, err := db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+statements[0])
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	require.NotContains(t, plan.String(), "USE TEMP B-TREE FOR ORDER BY")
	require.Contains(t, plan.String(), "idx_projects_selector_order")
	counter.Reset()
	counter.SetEnabled(true)
	_, err = repo.List(t.Context())
	require.NoError(t, err)
	counter.SetEnabled(false)
	require.Len(t, counter.Statements(), 1)
	require.Contains(t, strings.ToLower(counter.Statements()[0]), "description")
}
