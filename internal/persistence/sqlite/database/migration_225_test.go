package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration225GlobalSendersPreservesBindingsAndFlagsConflicts(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "global-senders.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 224))
	_, err := db.Exec(`
 INSERT INTO projects(id,name) VALUES('a','A'),('b','B');
 INSERT INTO users(id,username) VALUES('alice','alice'),('bob','bob');
 INSERT INTO org_memberships VALUES('default','alice','member'),('default','bob','member');
 INSERT INTO telegram_authorized_users(id,project_id,telegram_user_id) VALUES('t1','a',123),('t2','b',123);
 INSERT INTO discord_authorized_users(id,project_id,discord_user_id) VALUES('d1','a','456'),('d2','b','456'),('d3','a','789'),('d4','b','789');
 INSERT INTO discord_sender_identities(allowlist_id,application_user_id,verified_by) VALUES('d1','alice','local-owner'),('d2','alice','local-owner'),('d3','alice','local-owner'),('d4','bob','local-owner');
 INSERT INTO slack_authorized_users(id,project_id,slack_user_id) VALUES('s1','a','U1'),('s2','b','U1');
 INSERT INTO slack_sender_identities(allowlist_id,team_id,application_user_id,verified_by) VALUES('s1','T1','alice','local-owner'),('s2','T1','alice','local-owner');
 INSERT INTO email_authorized_senders(id,project_id,email_address) VALUES('e1','a','Alice@example.com'),('e2','b','alice@example.com');
 `)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 225))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM telegram_authorized_users`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM email_authorized_senders`).Scan(&count))
	require.Equal(t, 1, count)
	var user string
	var review bool
	require.NoError(t, db.QueryRow(`SELECT application_user_id,mapping_review FROM discord_authorized_users WHERE discord_user_id='456'`).Scan(&user, &review))
	require.Equal(t, "alice", user)
	require.False(t, review)
	require.NoError(t, db.QueryRow(`SELECT application_user_id,mapping_review FROM discord_authorized_users WHERE discord_user_id='789'`).Scan(&user, &review))
	require.Empty(t, user)
	require.True(t, review)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_sender_identities`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT application_user_id FROM slack_sender_identities`).Scan(&user))
	require.Equal(t, "alice", user)
	_, err = db.Exec(`DELETE FROM projects WHERE id IN ('a','b')`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_authorized_users`).Scan(&count))
	require.Equal(t, 2, count)
	var details string
	require.NoError(t, db.QueryRow(`SELECT details FROM channel_sender_migration_review WHERE channel='discord' AND original_id='d4'`).Scan(&details))
	require.Contains(t, details, "bob")
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
}
