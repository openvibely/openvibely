package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestRemovePersonalitiesMigration(t *testing.T) {
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	require.NoError(t, goose.SetDialect("sqlite3"))
	for _, existing := range []bool{false, true} {
		name := "fresh"
		if existing {
			name = "existing personalities"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "personalities.db"))
			if existing {
				require.NoError(t, goose.UpTo(db, ".", 240))
				_, err := db.Exec(`
INSERT INTO custom_personalities (name,key,system_prompt) VALUES
 ('Custom','custom','Custom prompt'),('Override','pirate_captain','Preset override');
INSERT INTO app_settings (key,value) VALUES ('personality','custom'),('unrelated_setting','keep');
`)
				require.NoError(t, err)
			}
			assertRemoved := func() {
				t.Helper()
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='custom_personalities'`).Scan(&count))
				require.Zero(t, count, "table and unique index must be removed")
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key='personality'`).Scan(&count))
				require.Zero(t, count, "saved selection must be removed")
				if existing {
					var value string
					require.NoError(t, db.QueryRow(`SELECT value FROM app_settings WHERE key='unrelated_setting'`).Scan(&value))
					require.Equal(t, "keep", value)
				}
			}
			require.NoError(t, goose.Up(db, "."))
			assertRemoved()
			require.NoError(t, goose.Up(db, "."))
			assertRemoved()
			require.NoError(t, goose.DownTo(db, ".", 240))
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM custom_personalities`).Scan(&count))
			require.Zero(t, count, "rollback cannot recover deleted data")
			_, err := db.Exec(`INSERT INTO custom_personalities (name,key,system_prompt) VALUES ('After rollback','new','Prompt'); INSERT INTO app_settings (key,value) VALUES ('personality','new')`)
			require.NoError(t, err)
			require.NoError(t, goose.Up(db, "."))
			assertRemoved()
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			require.NoError(t, err)
			defer rows.Close()
			require.False(t, rows.Next())
			require.NoError(t, rows.Err())
		})
	}
}
