package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration232DiffInvalidation(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "diff-validity.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 231))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('t','default','T'); INSERT INTO executions(id,task_id,diff_output) VALUES('e','t','saved diff')`)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, goose.UpTo(db, ".", 232))
		var diff string
		var invalidated int
		require.NoError(t, db.QueryRow(`SELECT diff_output, diff_output_invalidated FROM executions WHERE id='e'`).Scan(&diff, &invalidated))
		require.Equal(t, "saved diff", diff)
		require.Zero(t, invalidated)
		require.NoError(t, goose.DownTo(db, ".", 231))
	}
}
