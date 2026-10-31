package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// Exercise migration 220's individual stages without inventing Goose versions.
// Stage bookkeeping is test-only and commits atomically with each transition.
func transcriptStageTo(db *sql.DB, target migrations.TranscriptStage) error {
	if err := goose.UpTo(db, ".", 219); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS transcript_test_stage(stage INTEGER NOT NULL); INSERT INTO transcript_test_stage SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM transcript_test_stage)`); err != nil {
		return err
	}
	var current migrations.TranscriptStage
	if err = tx.QueryRow(`SELECT stage FROM transcript_test_stage`).Scan(&current); err != nil {
		return err
	}
	ctx := context.Background()
	for stage := current + 1; stage <= target; stage++ {
		if err = migrations.ApplyTranscriptStep(ctx, tx, stage, true); err != nil {
			return err
		}
	}
	for stage := current; stage > target; stage-- {
		if err = migrations.ApplyTranscriptStep(ctx, tx, stage, false); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE transcript_test_stage SET stage=?`, target); err != nil {
		return err
	}
	return tx.Commit()
}

func TestConsolidatedTranscriptMigration(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "upgrade.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 216))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','p'); INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('t','p','t','backlog',2,'completed','test'); INSERT INTO executions(id,task_id,status,output) VALUES('e','t','completed','hello 世界');`)
	require.NoError(t, err)
	require.NoError(t, initializeSQLite(db))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.EqualValues(t, latestMigrationVersion, version)
	for _, name := range []string{"organizations", "task_model_overrides", "conversation_checkpoint_ranges", "execution_stream_offsets"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, name).Scan(&count))
		require.Equal(t, 1, count, name)
	}
	_, err = db.Exec(`INSERT INTO execution_stream_text VALUES('e',8,' pending')`)
	require.NoError(t, err)
	require.NoError(t, initializeSQLite(db))
	require.NoError(t, goose.DownTo(db, ".", 216))
	var output string
	require.NoError(t, db.QueryRow(`SELECT output FROM executions WHERE id='e'`).Scan(&output))
	require.Equal(t, "hello 世界 pending", output)
	require.NoError(t, goose.Up(db, "."))
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
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
