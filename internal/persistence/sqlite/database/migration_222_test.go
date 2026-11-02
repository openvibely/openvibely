package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration222TaskCheckpointCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, deletion string
		remaining      int
	}{
		{"single", `DELETE FROM tasks WHERE id='a'`, 2},
		{"bulk", `DELETE FROM tasks WHERE project_id='delete-project'`, 1},
		{"project cascade", `DELETE FROM projects WHERE id='delete-project'`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "cleanup.db"))
			goose.SetBaseFS(migrations.FS)
			require.NoError(t, goose.SetDialect("sqlite3"))
			require.NoError(t, goose.UpTo(db, ".", 221))
			_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('delete-project','Delete'),('a','Keep');
INSERT INTO tasks(id,project_id,title) VALUES('a','delete-project','A'),('b','delete-project','B'),('keep','a','Keep');
INSERT INTO executions(id,task_id) VALUES('exec-a','a'),('exec-b','b'),('exec-keep','keep');
INSERT INTO chat_compaction_checkpoints(scope_type,scope_id) VALUES('task','a'),('task','b'),('task','keep'),('task','already-missing'),('chat_project','a');
INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id)
SELECT id,0,'provider','item',0,'put','{}','initial' FROM executions;
INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id)
SELECT scope_type,scope_id,0,'provider','item',0,'put','{}','initial' FROM chat_compaction_checkpoints;`)
			require.NoError(t, err)
			require.NoError(t, goose.UpTo(db, ".", 222))
			assertCount := func(query string, want int) {
				t.Helper()
				var count int
				require.NoError(t, db.QueryRow(query).Scan(&count))
				require.Equal(t, want, count, query)
			}
			// Installing the trigger does not clean up pre-existing records.
			assertCount(`SELECT count(*) FROM chat_compaction_checkpoints`, 5)
			assertCount(`SELECT count(*) FROM conversation_items`, 8)
			_, err = db.Exec(tc.deletion)
			require.NoError(t, err)
			assertCount(`SELECT count(*) FROM executions`, tc.remaining)
			assertCount(`SELECT count(*) FROM chat_compaction_checkpoints`, tc.remaining+2)
			assertCount(`SELECT count(*) FROM conversation_items`, tc.remaining*2+2)
			assertCount(`SELECT count(*) FROM conversation_items WHERE execution_id='exec-keep'`, 1)
			assertCount(`SELECT count(*) FROM conversation_items WHERE checkpoint_scope_type='task' AND checkpoint_scope_id='keep'`, 1)
			assertCount(`SELECT count(*) FROM conversation_items WHERE checkpoint_scope_type='task' AND checkpoint_scope_id='already-missing'`, 1)
			// A project scope with the same ID as the deleted task must survive.
			assertCount(`SELECT count(*) FROM conversation_items WHERE checkpoint_scope_type='chat_project' AND checkpoint_scope_id='a'`, 1)
			assertCount(`SELECT count(*) FROM pragma_foreign_key_check`, 0)
			require.NoError(t, goose.DownTo(db, ".", 221))
			assertCount(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name='task_checkpoint_cleanup'`, 0)
			require.NoError(t, goose.UpTo(db, ".", 222))
			_, err = db.Exec(`DELETE FROM tasks WHERE id='keep'`)
			require.NoError(t, err)
			assertCount(`SELECT count(*) FROM chat_compaction_checkpoints WHERE scope_type='task' AND scope_id='keep'`, 0)
		})
	}
}

func TestMigration222CheckpointOwnerGuards(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "owners.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 222))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','P');
 INSERT INTO tasks(id,project_id,title) VALUES('t','p','T'),('other','p','Other');`)
	require.NoError(t, err)
	upsert := `INSERT INTO chat_compaction_checkpoints(scope_type,scope_id,summary) VALUES('task','t','saved') ON CONFLICT(scope_type,scope_id) DO UPDATE SET summary=excluded.summary`
	for i := 0; i < 2; i++ {
		_, err = db.Exec(upsert)
		require.NoError(t, err)
	}
	_, err = db.Exec(`UPDATE chat_compaction_checkpoints SET scope_id='other' WHERE scope_type='task' AND scope_id='t';
 INSERT INTO chat_compaction_checkpoints(scope_type,scope_id) VALUES('chat_project','p');`)
	require.NoError(t, err)
	for _, query := range []string{
		`INSERT INTO chat_compaction_checkpoints(scope_type,scope_id) VALUES('task','missing')`,
		`UPDATE chat_compaction_checkpoints SET scope_id='missing' WHERE scope_type='task' AND scope_id='other'`,
		`UPDATE chat_compaction_checkpoints SET scope_type='task' WHERE scope_type='chat_project' AND scope_id='p'`,
	} {
		_, err = db.Exec(query)
		require.ErrorContains(t, err, "task checkpoint owner does not exist")
	}
	_, err = db.Exec(`DELETE FROM tasks WHERE id='t'`)
	require.NoError(t, err)
	_, err = db.Exec(upsert)
	require.ErrorContains(t, err, "task checkpoint owner does not exist")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM chat_compaction_checkpoints WHERE scope_type='task' AND scope_id='t'`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`INSERT INTO chat_compaction_checkpoints(scope_type,scope_id,summary) VALUES('chat_project','p','updated') ON CONFLICT(scope_type,scope_id) DO UPDATE SET summary=excluded.summary`)
	require.NoError(t, err)
	require.NoError(t, goose.DownTo(db, ".", 221))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ('task_checkpoint_cleanup','task_checkpoint_owner_insert','task_checkpoint_owner_update')`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(upsert)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 222))
	_, err = db.Exec(upsert)
	require.ErrorContains(t, err, "task checkpoint owner does not exist")
}
