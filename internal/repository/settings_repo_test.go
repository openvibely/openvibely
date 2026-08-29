package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSettingsRepoSetManyRollsBackWholeSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewSettingsRepo(db)
	require.NoError(t, repo.Set(ctx, "first", "old-first"))
	require.NoError(t, repo.Set(ctx, "fail", "old-fail"))
	_, err := db.ExecContext(ctx, `CREATE TRIGGER reject_failed_setting BEFORE UPDATE ON app_settings
		WHEN NEW.key = 'fail' BEGIN SELECT RAISE(ABORT, 'forced settings failure'); END`)
	require.NoError(t, err)

	err = repo.SetMany(ctx, map[string]string{"first": "new-first", "fail": "new-fail"})
	require.Error(t, err)
	values, err := repo.GetMany(ctx, []string{"first", "fail"})
	require.NoError(t, err)
	require.Equal(t, "old-first", values["first"])
	require.Equal(t, "old-fail", values["fail"])
}
