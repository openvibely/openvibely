package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestRemoveKnowledgeEntriesMigration(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "knowledge-removal.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 235))
	_, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('knowledge-project', 'Preserved project');
 INSERT INTO knowledge_entries (project_id, topic, content) VALUES ('knowledge-project', 'Old learning', 'Unused content');`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 236))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('knowledge_entries', 'idx_knowledge_project', 'idx_knowledge_topic')`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = 'knowledge-project'`).Scan(&count))
	require.Equal(t, 1, count)
	// Rollback restores an empty schema, not the deleted content.
	require.NoError(t, goose.DownTo(db, ".", 235))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM knowledge_entries`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('idx_knowledge_project', 'idx_knowledge_topic')`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, goose.UpTo(db, ".", 236))
}
