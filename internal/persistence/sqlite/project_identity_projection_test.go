package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestProjectIdentityExactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Projects
	for _, id := range []string{"default", "missing-project"} {
		counter.Reset()
		counter.SetEnabled(true)
		_, err := repo.GetIdentityByID(t.Context(), id)
		counter.SetEnabled(false)
		require.NoError(t, err)
		statements := counter.Statements()
		require.Len(t, statements, 1)
		require.Equal(t, "select id, name from projects where id = ?", strings.ToLower(strings.Join(strings.Fields(statements[0]), " ")))
	}
}

func TestProjectSelectorExactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Projects
	counter.Reset()
	counter.SetEnabled(true)
	_, err := repo.ListSelectorOptions(t.Context())
	counter.SetEnabled(false)
	require.NoError(t, err)
	statements := counter.Statements()
	require.Len(t, statements, 1)
	require.Equal(t, "select id, name, is_default from projects order by is_default desc, name asc, id asc", strings.ToLower(strings.Join(strings.Fields(statements[0]), " ")))
}
