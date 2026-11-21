package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestGeminiProviderMigrationRoundTrip(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "gemini.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 238))

	_, err := db.Exec(`INSERT INTO agent_configs(id,name,provider,model) VALUES('gemini-like','G','openai','gpt-5')`)
	require.Error(t, func() error {
		_, err := db.Exec(`INSERT INTO agent_configs(id,name,provider,model) VALUES('bad','B','gemini','gemini-2.5-pro')`)
		return err
	}())
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, ".", 239))
	_, err = db.Exec(`
 INSERT INTO oauth_connections(id,provider,name,oauth_account_id) VALUES('gc','gemini','Google','proj-1');
 INSERT INTO agent_configs(id,name,provider,model,oauth_connection_id) VALUES('g','Gemini','gemini','gemini-2.5-pro','gc');`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO oauth_connections(id,provider,name) VALUES('bad','other','x')`)
	require.Error(t, err)

	var provider string
	require.NoError(t, db.QueryRow(`SELECT provider FROM agent_configs WHERE id='gemini-like'`).Scan(&provider))
	require.Equal(t, "openai", provider)
	assertNoForeignKeyViolations(t, db)

	require.NoError(t, goose.DownTo(db, ".", 238))
	var conn any
	require.NoError(t, db.QueryRow(`SELECT provider, oauth_connection_id FROM agent_configs WHERE id='g'`).Scan(&provider, &conn))
	require.Equal(t, "openai_compatible", provider)
	require.Nil(t, conn)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM oauth_connections WHERE provider='gemini'`).Scan(&count))
	require.Zero(t, count)
	assertNoForeignKeyViolations(t, db)

	require.NoError(t, goose.UpTo(db, ".", 239))
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
