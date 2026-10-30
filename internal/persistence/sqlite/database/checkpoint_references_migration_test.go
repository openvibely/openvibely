package database

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestCheckpointReferencesMigrationDowngradeMaterializesPayloads(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "references.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 217))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('refs-project','migration');
 INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('refs-task','refs-project','migration','backlog',2,'completed','test');
 INSERT INTO executions(id,task_id,status) VALUES('refs-exec','refs-task','completed');
 INSERT INTO chat_compaction_checkpoints(scope_type,scope_id) VALUES('task','refs-task');
 INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,snapshot_sequence)
 VALUES('refs-exec',0,'provider','replay:item',0,'put','{"encrypted_content":"opaque"}','source',1);`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 218))
	_, err = db.Exec(`INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id,snapshot_sequence)
 VALUES('task','refs-task',0,'provider','checkpoint:item',0,'put','{}','borrower',2);
 INSERT INTO conversation_checkpoint_references(checkpoint_sequence,source_sequence) VALUES(2,1)`)
	require.NoError(t, err)
	var resolved string
	require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_resolved_items WHERE sequence=2`).Scan(&resolved))
	require.JSONEq(t, `{"encrypted_content":"opaque"}`, resolved)
	require.NoError(t, goose.DownTo(db, ".", 217))
	require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_items WHERE sequence=2`).Scan(&resolved))
	require.JSONEq(t, `{"encrypted_content":"opaque"}`, resolved)
	require.NoError(t, goose.UpTo(db, ".", 218))
	require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_resolved_items WHERE sequence=2`).Scan(&resolved))
	require.JSONEq(t, `{"encrypted_content":"opaque"}`, resolved)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}

func TestCheckpointRangesMigrationDowngrade(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprint("retire=", retire), func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "ranges.db"))
			goose.SetBaseFS(migrations.FS)
			require.NoError(t, goose.SetDialect("sqlite3"))
			require.NoError(t, goose.UpTo(db, ".", 221))
			_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('range-project','migration');
    INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('range-task','range-project','migration','backlog',2,'completed','test');
    INSERT INTO executions(id,task_id,status) VALUES('range-exec','range-task','completed');
    INSERT INTO chat_compaction_checkpoints(scope_type,scope_id) VALUES('task','range-task');`)
			require.NoError(t, err)
			var first int64
			for i := 0; i < 4; i++ {
				result, err := db.Exec(`INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id) VALUES('range-exec',0,'provider',?,?,'put',?,?)`, fmt.Sprint("item:", i), i, fmt.Sprintf(`{"text":"message %d"}`, i), fmt.Sprint("source:", i))
				require.NoError(t, err)
				seq, err := result.LastInsertId()
				require.NoError(t, err)
				if i == 0 {
					first = seq
				}
			}
			// A tombstone from the former individual-reference representation must not
			// collide with the rows recreated on downgrade.
			_, err = db.Exec(`INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id) VALUES('task','range-task',0,'provider','checkpoint:frame:000000000000',0,'remove','{}','old-remove')`)
			require.NoError(t, err)
			descriptor := fmt.Sprintf(`{"prefix":"checkpoint:frame:","first_index":0,"position":0,"first_source":%d,"last_source":%d}`, first, first+3)
			result, err := db.Exec(`INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id) VALUES('task','range-task',0,'metadata','checkpoint:range:frame:000000000000',0,'put',?,'range')`, descriptor)
			require.NoError(t, err)
			checkpoint, err := result.LastInsertId()
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO conversation_checkpoint_ranges VALUES(?,?,?)`, checkpoint, first, first+3)
			require.NoError(t, err)
			if retire {
				_, err = db.Exec(`DELETE FROM conversation_items WHERE sequence=?`, first+1)
				require.NoError(t, err)
			}
			require.NoError(t, goose.DownTo(db, ".", 220))
			for i := 0; i < 4; i++ {
				var raw string
				require.NoError(t, db.QueryRow(`SELECT payload FROM conversation_items WHERE checkpoint_scope_type='task' AND checkpoint_scope_id='range-task' AND lane='provider' AND item_id=? AND is_current=1`, fmt.Sprintf("checkpoint:frame:%012d", i)).Scan(&raw))
				require.JSONEq(t, fmt.Sprintf(`{"text":"message %d"}`, i), raw)
			}
			require.NoError(t, goose.UpTo(db, ".", 221))
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			require.NoError(t, err)
			defer rows.Close()
			require.False(t, rows.Next())
			require.NoError(t, rows.Err())
		})
	}
}
