package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceChatMigrationPreservesLegacyHistoryAsAdminOnly(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "workspace-chat.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,category,author_user_id) VALUES('old-chat','__all_projects__','Shared history','chat','local-owner')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var owner, author string
	require.NoError(t, db.QueryRow(`SELECT chat_owner_user_id, author_user_id FROM tasks WHERE id='old-chat'`).Scan(&owner, &author))
	require.Empty(t, owner, "an author does not own previously shared history")
	require.Equal(t, "local-owner", author)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT chat_owner_user_id FROM tasks WHERE id='old-chat'`).Scan(&owner))
	require.Empty(t, owner)
}
