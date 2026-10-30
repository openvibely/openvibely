package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration219DropsUsageSnapshotsAndPreservesAccountState(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "drop-usage-cache.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 218))
	_, err := db.Exec(`
 INSERT INTO oauth_connections(id,provider,name,oauth_access_token,oauth_refresh_token,oauth_revision,oauth_provider_display_name)
 VALUES ('recover','openai','Account','access','refresh',2,''),('named','anthropic','Account','a','r',1,'Existing name'),('stale','openai','Account','b','s',2,'');
 INSERT INTO agent_configs(id,name,provider,model,auth_method,oauth_connection_id) VALUES ('model','Model','openai','test','oauth','recover');
 INSERT INTO account_usage_snapshots(id,provider,oauth_connection_id,oauth_config_revision,account_display_name,fetched_at)
 VALUES ('current','openai','recover',2,'Current name','2026-01-02'),('older','openai','recover',2,'Older name','2026-01-01'),
 ('wrong-generation','openai','recover',1,'Wrong generation','2026-01-03'),('already-named','anthropic','named',1,'Snapshot name','2026-01-02'),('stale-only','openai','stale',1,'Stale name','2026-01-03');
 INSERT INTO account_usage_extra_limits(id,snapshot_id,provider,limit_key,label) VALUES ('extra','current','openai','weekly','Weekly');
 INSERT INTO llm_usage_events(id,provider,model,total_tokens) VALUES ('usage','openai','test',123);
 INSERT INTO app_settings(key,value) VALUES ('account_usage_monitor:state:test','{"sent":{"alert":true}}');
 `)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 219))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('account_usage_snapshots','account_usage_extra_limits')`).Scan(&count))
	require.Zero(t, count)
	for id, want := range map[string]string{"recover": "Current name", "named": "Existing name", "stale": ""} {
		var name string
		require.NoError(t, db.QueryRow(`SELECT oauth_provider_display_name FROM oauth_connections WHERE id=?`, id).Scan(&name))
		require.Equal(t, want, name)
	}
	var access, refresh, connection, state string
	var revision int
	require.NoError(t, db.QueryRow(`SELECT oauth_access_token,oauth_refresh_token,oauth_revision FROM oauth_connections WHERE id='recover'`).Scan(&access, &refresh, &revision))
	require.Equal(t, "access", access)
	require.Equal(t, "refresh", refresh)
	require.Equal(t, 2, revision)
	require.NoError(t, db.QueryRow(`SELECT oauth_connection_id FROM agent_configs WHERE id='model'`).Scan(&connection))
	require.Equal(t, "recover", connection)
	require.NoError(t, db.QueryRow(`SELECT total_tokens FROM llm_usage_events WHERE id='usage'`).Scan(&count))
	require.Equal(t, 123, count)
	require.NoError(t, db.QueryRow(`SELECT value FROM app_settings WHERE key='account_usage_monitor:state:test'`).Scan(&state))
	require.JSONEq(t, `{"sent":{"alert":true}}`, state)
	require.NoError(t, goose.UpTo(db, ".", 219))
	// A schema rollback recreates only empty cache tables, allowing older
	// migration steps and a subsequent upgrade to run safely.
	require.NoError(t, goose.DownTo(db, ".", 218))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_usage_snapshots`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.UpTo(db, ".", 219))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('account_usage_snapshots','account_usage_extra_limits')`).Scan(&count))
	require.Zero(t, count)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
