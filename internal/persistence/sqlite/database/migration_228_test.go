package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration228PreservesLegacyPATForOwnerOnly(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "migration-228.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 227))
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('new-member','new-member'); INSERT INTO org_memberships VALUES('default','new-member','member'); INSERT INTO app_settings(key,value) VALUES('github_pat','legacy-secret'),('github_pat_user_login','legacy-login'),('github_api_endpoint','https://github.example/api/v3') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, goose.UpTo(db, ".", 228))
		var id, pat, login, endpoint, name, email string
		require.NoError(t, db.QueryRow(`SELECT user_id,pat,login,api_endpoint,author_name,author_email FROM user_github_connections`).Scan(&id, &pat, &login, &endpoint, &name, &email))
		require.Equal(t, "local-owner", id)
		require.Equal(t, "legacy-secret", pat)
		require.Equal(t, "legacy-login", login)
		require.Equal(t, "https://github.example/api/v3", endpoint)
		require.Empty(t, name)
		require.Empty(t, email)
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_github_connections`).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, goose.DownTo(db, ".", 227))
	}
}
