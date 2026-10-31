package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Reconstruct old unpublished stages for compatibility and downgrade tests.
// They are not registered with Goose or installed on fresh databases.
func legacyTranscriptTo(db *sql.DB, target int64) error {
	version, err := goose.GetDBVersion(db)
	if err != nil {
		return err
	}
	if version < 216 {
		if err = goose.UpTo(db, ".", 216); err != nil {
			return err
		}
		version = 216
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ctx := context.Background()
	if version < target {
		for v := version + 1; v <= target; v++ {
			if err = migrations.ApplyTranscriptDevelopmentStep(ctx, tx, v, true); err != nil {
				return err
			}
		}
	}
	if version > target {
		for v := version; v > target; v-- {
			if err = migrations.ApplyTranscriptDevelopmentStep(ctx, tx, v, false); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`DELETE FROM goose_db_version WHERE version_id BETWEEN 217 AND 221`); err != nil {
		return err
	}
	for v := int64(217); v <= target; v++ {
		if _, err = tx.Exec(`INSERT INTO goose_db_version(version_id,is_applied) VALUES(?,1)`, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func TestConsolidatedTranscriptMigration(t *testing.T) {
	for _, stage := range []int64{216, 217, 218, 219, 220, 221} {
		t.Run(fmt.Sprint(stage), func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "upgrade.db"))
			goose.SetBaseFS(migrations.FS)
			require.NoError(t, goose.SetDialect("sqlite3"))
			require.NoError(t, goose.UpTo(db, ".", 216))
			_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','p'); INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('t','p','t','backlog',2,'completed','test'); INSERT INTO executions(id,task_id,status,output) VALUES('e','t','completed','hello 世界');`)
			require.NoError(t, err)
			var priorSequence int64
			if stage > 216 {
				require.NoError(t, legacyTranscriptTo(db, stage))
				require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items`).Scan(&priorSequence))
			}
			if stage >= 219 {
				_, err = db.Exec(`INSERT INTO execution_stream_text VALUES('e',8,' pending')`)
				require.NoError(t, err)
			}
			require.NoError(t, initializeSQLite(db))
			if stage > 216 {
				var sequence int64
				require.NoError(t, db.QueryRow(`SELECT max(sequence) FROM conversation_items`).Scan(&sequence))
				require.Equal(t, priorSequence, sequence, "adoption must preserve journal cursors")
			}
			var version int64
			require.NoError(t, db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version))
			require.EqualValues(t, latestMigrationVersion, version)
			var n int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM goose_db_version WHERE version_id=221`).Scan(&n))
			require.Zero(t, n)
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM goose_db_version WHERE version_id IN (217,218) AND is_applied=1`).Scan(&n))
			require.Equal(t, 2, n, "main's 217/218 must apply after adoption")
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('organizations','task_model_overrides')`).Scan(&n))
			require.Equal(t, 2, n)
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='conversation_checkpoint_ranges'`).Scan(&n))
			require.Equal(t, 1, n)
			require.NoError(t, initializeSQLite(db)) // adoption is idempotent
			require.NoError(t, goose.DownTo(db, ".", 216))
			var output string
			require.NoError(t, db.QueryRow(`SELECT output FROM executions WHERE id='e'`).Scan(&output))
			expected := "hello 世界"
			if stage >= 219 {
				expected += " pending"
			}
			require.Equal(t, expected, output)
			require.NoError(t, goose.Up(db, "."))
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			require.NoError(t, err)
			defer rows.Close()
			require.False(t, rows.Next())
			require.NoError(t, rows.Err())
		})
	}
}

func TestDevelopmentTranscriptAdoptionRollsBack(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "bad.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, legacyTranscriptTo(db, 219))
	_, err := db.Exec(`DROP TABLE execution_stream_text`)
	require.NoError(t, err)
	require.Error(t, migrations.NormalizeTranscriptDevelopmentVersions(context.Background(), db))
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM goose_db_version WHERE version_id=217`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('executions') WHERE name='display_has_plan'`).Scan(&n))
	require.Zero(t, n)
}

func TestTranscriptUpgradeFromMain218(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "main.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 218))

	require.NoError(t, initializeSQLite(db))
	require.NoError(t, initializeSQLite(db))
	for _, name := range []string{"organizations", "task_model_overrides", "conversation_items", "conversation_checkpoint_ranges"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count))
		require.Equal(t, 1, count)
	}
	require.NoError(t, goose.DownTo(db, ".", 218))
	v, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.EqualValues(t, 218, v)
	require.NoError(t, initializeSQLite(db))
}

func TestConsolidatedOffsetIndexPreservesMigrationHistory(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "offset-index.db"))
	require.NoError(t, initializeSQLite(db))
	_, err := db.Exec(`INSERT INTO goose_db_version(version_id,is_applied) VALUES(220,1)`)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, initializeSQLite(db))
		version, err := goose.GetDBVersion(db)
		require.NoError(t, err)
		require.EqualValues(t, 220, version)
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='execution_stream_offsets'`).Scan(&count))
		require.Equal(t, 1, count)
	}
	// Also update an earlier consolidated development database missing the index.
	_, err = db.Exec(`DROP INDEX execution_stream_offsets`)
	require.NoError(t, err)
	require.NoError(t, initializeSQLite(db))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='execution_stream_offsets'`).Scan(&count))
	require.Equal(t, 1, count)
}

// A later migration must remain applied when startup adopts an older transcript
// schema. The offset index is also present on fresh 219 databases, so it cannot
// identify the abandoned development migration 220.
func TestTranscriptAdoptionPreservesFutureMigration(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "future.db"))
	require.NoError(t, initializeSQLite(db))
	goose.SetBaseFS(fstest.MapFS{"221_future.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE future_feature(id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE future_feature;\n")}})
	defer goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.Up(db, ".", goose.WithAllowMissing()))
	for range 2 {
		require.NoError(t, migrations.NormalizeTranscriptDevelopmentVersions(context.Background(), db))
		version, err := goose.GetDBVersion(db)
		require.NoError(t, err)
		require.EqualValues(t, 221, version)
		require.NoError(t, goose.Up(db, ".", goose.WithAllowMissing()))
	}
}

func TestTranscriptAdoptsFormer219WithMainSnapshotMigration(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "branch.db"))
	require.NoError(t, initializeSQLite(db))
	// Recreate main's pre-219 schema while retaining the consolidated transcript.
	raw, err := migrations.FS.ReadFile("219_drop_account_usage_snapshots.sql")
	require.NoError(t, err)
	_, err = db.Exec(strings.Split(string(raw), "-- +goose Down")[1])
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM goose_db_version WHERE version_id>=219; INSERT INTO goose_db_version(version_id,is_applied) VALUES(219,1)`)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, initializeSQLite(db))
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='account_usage_snapshots'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='conversation_checkpoint_ranges'`).Scan(&count))
	require.Equal(t, 1, count)
}
