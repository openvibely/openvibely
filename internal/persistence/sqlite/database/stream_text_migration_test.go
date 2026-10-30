package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestStreamTextDowngradePreservesPendingSuffix(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "stream.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, legacyTranscriptTo(db, 219))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('stream-project','migration');
 INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('stream-task','stream-project','migration','backlog',2,'running','test');
 INSERT INTO executions(id,task_id,status) VALUES('stream-exec','stream-task','running');
 INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes) VALUES('stream-exec',0,'display','display:0',0,'put','{"type":"text","text":"prefix "}','prefix',7);
 UPDATE conversation_items SET snapshot_sequence=sequence;
 INSERT INTO execution_stream_text VALUES('stream-exec',3,'世'),('stream-exec',6,'界');`)
	require.NoError(t, err)
	require.NoError(t, legacyTranscriptTo(db, 218))
	var output string
	require.NoError(t, db.QueryRow(`SELECT group_concat(text,'') FROM (SELECT json_extract(payload,'$.text') text FROM conversation_items WHERE execution_id='stream-exec' AND lane='display' AND is_current=1 ORDER BY position,item_id)`).Scan(&output))
	require.Equal(t, "prefix 世界", output)
	require.NoError(t, legacyTranscriptTo(db, 219))
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}
