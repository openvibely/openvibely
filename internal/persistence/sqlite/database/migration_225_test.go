package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration225RepairsPreviouslyApplied224(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "current224"
		if legacy {
			name = "original224"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "migration-225.db"))
			goose.SetBaseFS(migrations.FS)
			require.NoError(t, goose.SetDialect("sqlite3"))
			require.NoError(t, goose.UpTo(db, ".", 224))
			_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,category,priority,status,prompt,chat_owner_user_id) VALUES ('repair-task','default','Preserved task','chat',0,'completed','preserved prompt','local-owner')`)
			require.NoError(t, err)
			if legacy {
				_, err = db.Exec(`
     DROP INDEX idx_tasks_chat_owner;
     DROP INDEX idx_thread_inputs_chat_owner;
     DROP INDEX idx_thread_inputs_recover_chat_project;
     ALTER TABLE tasks DROP COLUMN chat_owner_user_id;
     ALTER TABLE thread_inputs DROP COLUMN chat_owner_user_id;
     CREATE INDEX idx_thread_inputs_recover_chat_project ON thread_inputs(scope,input_status,input_mode,project_id,run_execution_id) WHERE scope='chat' AND input_status='pending' AND input_mode='queued';
    `)
				require.NoError(t, err)
			}
			for range 2 {
				require.NoError(t, goose.UpTo(db, ".", 225))
				var owner, prompt string
				require.NoError(t, db.QueryRow(`SELECT chat_owner_user_id,prompt FROM tasks WHERE id='repair-task'`).Scan(&owner, &prompt))
				require.Equal(t, "preserved prompt", prompt)
				if legacy {
					require.Empty(t, owner)
				} else {
					require.Equal(t, "local-owner", owner)
				}
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('thread_inputs') WHERE name='chat_owner_user_id' AND "notnull"=1 AND dflt_value="''"`).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_index_info('idx_thread_inputs_recover_chat_project') WHERE name='chat_owner_user_id'`).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, goose.DownTo(db, ".", 224))
			}
			require.NoError(t, goose.DownTo(db, ".", 223))
		})
	}
}
