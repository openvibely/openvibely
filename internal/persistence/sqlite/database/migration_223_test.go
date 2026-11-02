package database

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMigration223TaskTitleEdits(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "titles.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 222))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('title-project','Titles'); INSERT INTO tasks(id,project_id,title) VALUES('title-task','title-project','Existing title')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 223))
	var flag int
	var title string
	require.NoError(t, db.QueryRow(`SELECT title,title_explicitly_set FROM tasks WHERE id='title-task'`).Scan(&title, &flag))
	require.Equal(t, "Existing title", title)
	require.Zero(t, flag)
	require.NoError(t, goose.DownTo(db, ".", 222))
	require.NoError(t, db.QueryRow(`SELECT title FROM tasks WHERE id='title-task'`).Scan(&title))
	require.Equal(t, "Existing title", title)
	require.NoError(t, goose.UpTo(db, ".", 223))
}
