package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestProjectAuthorizationMigrationGlobalSenders(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "global-senders.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`
 INSERT INTO projects(id,name) VALUES('a','A'),('b','B');
 INSERT INTO users(id,username) VALUES('alice','alice'),('bob','bob');
 INSERT INTO org_memberships VALUES('default','alice','member'),('default','bob','member');
 INSERT INTO telegram_authorized_users(id,project_id,telegram_user_id) VALUES('t1','a',123),('t2','b',123);
 INSERT INTO discord_authorized_users(id,project_id,discord_user_id) VALUES('d1','a','456'),('d2','b','456'),('d3','a','789'),('d4','b','789');
 INSERT INTO slack_authorized_users(id,project_id,slack_user_id) VALUES('s1','a','U1'),('s2','b','U1');
 INSERT INTO email_authorized_senders(id,project_id,email_address) VALUES('e1','a','Alice@example.com'),('e2','b','alice@example.com');
 `)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	for table, want := range map[string]int{
		"telegram_authorized_users": 1, "slack_authorized_users": 1,
		"discord_authorized_users": 2, "email_authorized_senders": 1,
	} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		require.Equal(t, want, count, table)
		// Legacy allowlists prove no OV user identity; every retained sender needs review.
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table+` WHERE application_user_id='' AND mapping_review=1 AND project_id=''`).Scan(&count))
		require.Equal(t, want, count, table)
	}
	for _, table := range []string{"slack_sender_identities", "discord_sender_identities"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "must not invent a sender binding")
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM channel_sender_migration_review`).Scan(&count))
	require.Equal(t, 10, count, "retain every original row for review")
	var details string
	require.NoError(t, db.QueryRow(`SELECT details FROM channel_sender_migration_review WHERE channel='discord' AND original_id='d4'`).Scan(&details))
	require.Contains(t, details, `"project_id":"b"`)
	require.Contains(t, details, `"identity":"789"`)
	_, err = db.Exec(`DELETE FROM projects WHERE id IN ('a','b')`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_authorized_users`).Scan(&count))
	require.Equal(t, 2, count)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	// The consolidated migration has one rollback boundary and can be reapplied.
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_authorized_users`).Scan(&count))
	require.Equal(t, 2, count)
}
