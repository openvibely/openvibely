package database

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMigration217PreservesProjectsAndMovesOwnerPreferences(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "org-upgrade.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 216))
	_, err := db.Exec(`INSERT INTO projects(id,name,description,repo_path) VALUES('existing','Existing','','/repo'); INSERT INTO app_settings(key,value) VALUES('ui.theme','light'),('integration.keep','secret')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 217))
	var org, owner, path, value string
	require.NoError(t, db.QueryRow(`SELECT po.org_id,po.owner_user_id,p.repo_path FROM projects p JOIN project_organizations po ON po.project_id=p.id WHERE p.id='existing'`).Scan(&org, &owner, &path))
	require.Equal(t, "default", org)
	require.Equal(t, "local-owner", owner)
	require.Equal(t, "/repo", path)
	require.NoError(t, db.QueryRow(`SELECT value FROM user_preferences WHERE user_id='local-owner' AND key='ui.theme'`).Scan(&value))
	require.Equal(t, "light", value)
	require.NoError(t, db.QueryRow(`SELECT value FROM app_settings WHERE key='integration.keep'`).Scan(&value))
	require.Equal(t, "secret", value)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM app_settings WHERE key LIKE 'ui.%'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 216))
	require.NoError(t, db.QueryRow(`SELECT value FROM app_settings WHERE key='ui.theme'`).Scan(&value))
	require.Equal(t, "light", value)
	require.NoError(t, goose.UpTo(db, ".", 217))
}
