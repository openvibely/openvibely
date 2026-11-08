package database

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMigration226CustomTags(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "tags.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 225))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('tag-project','Tags');
 INSERT INTO tasks(id,project_id,title,tag) VALUES('tag-parent','tag-project','Parent','feature');
 INSERT INTO tasks(id,project_id,title,tag,parent_task_id) VALUES('tag-child','tag-project','Child','bug','tag-parent')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 226))
	var tag, parent string
	require.NoError(t, db.QueryRow(`SELECT tag,parent_task_id FROM tasks WHERE id='tag-child'`).Scan(&tag, &parent))
	require.Equal(t, "bug", tag)
	require.Equal(t, "tag-parent", parent)
	require.NoError(t, db.QueryRow(`SELECT tag FROM tasks WHERE id='tag-parent'`).Scan(&tag))
	require.Equal(t, "feature", tag)
	_, err = db.Exec(`UPDATE tasks SET tag='docs/security' WHERE id='tag-parent'`)
	require.NoError(t, err)
	require.NoError(t, goose.DownTo(db, ".", 225))
	require.NoError(t, goose.UpTo(db, ".", 226))
	require.NoError(t, db.QueryRow(`SELECT tag FROM tasks WHERE id='tag-parent'`).Scan(&tag))
	require.Equal(t, "docs/security", tag)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
