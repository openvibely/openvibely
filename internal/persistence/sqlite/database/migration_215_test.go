package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration215BackfillsChatAttachmentProjects(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "migration-215.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 214))
	_, err := db.Exec(`
 INSERT INTO projects (id, name, description, repo_path) VALUES ('p215', 'Chat', '', ''), ('other215', 'Other', '', '');
 INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES
 ('t215a', 'p215', 'First turn', 'chat', 0, 'completed', 'p'),
 ('t215b', 'p215', 'Next turn', 'chat', 0, 'completed', 'p'),
 ('t215c', 'other215', 'Other chat', 'chat', 0, 'completed', 'p');
 INSERT INTO executions (id, task_id, task_project_id, status, started_at) VALUES
 ('e215a', 't215a', 'p215', 'completed', datetime('now')),
 ('e215b', 't215b', 'p215', 'completed', datetime('now')),
 ('e215c', 't215c', 'other215', 'completed', datetime('now'));
 INSERT INTO chat_attachments (id, execution_id, file_name, file_path, media_type, file_size) VALUES
 ('a215', 'e215a', 'a.pdf', '/a.pdf', 'application/pdf', 1),
 ('b215', 'e215b', 'b.pdf', '/b.pdf', 'application/pdf', 2),
 ('c215', 'e215c', 'c.pdf', '/c.pdf', 'application/pdf', 3);
 `)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 215))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM chat_attachments WHERE project_id = 'p215'`).Scan(&count))
	require.Equal(t, 2, count)
	var projectID, path string
	require.NoError(t, db.QueryRow(`SELECT project_id, file_path FROM chat_attachments WHERE id = 'c215'`).Scan(&projectID, &path))
	require.Equal(t, "other215", projectID)
	require.Equal(t, "/c.pdf", path)
	require.NoError(t, goose.DownTo(db, ".", 214))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM chat_attachments`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, goose.UpTo(db, ".", 215))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM chat_attachments WHERE project_id = 'p215'`).Scan(&count))
	require.Equal(t, 2, count)
}
