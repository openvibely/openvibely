package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigration230LatestTaskDiff(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "diffs.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 229))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','P');
 INSERT INTO tasks(id,project_id,title) VALUES('a','p','A'),('b','p','B'),('empty','p','Empty');
 INSERT INTO executions(id,task_id,started_at,diff_output,prompt_sent,reasoning_content,error_message,tokens_used,duration_ms) VALUES
 ('old','a','2026-01-01','old','prompt','reason','error',42,123),
 ('tie-first','a','2026-01-02','first','','','',0,0),
 ('winner','a','2026-01-02','latest','','','',0,0),
 ('late-insert','a','2025-01-01','late','','','',0,0),
 ('empty-newest','a','2026-01-03','','','','',0,0),
 ('other','b','2026-01-01','other','','','',0,0),
 ('empty','empty','2026-01-01','','','','',0,0);`)
	require.NoError(t, err)
	// Compare every column other than diff_output, including timestamps and IDs.
	_, err = db.Exec(`CREATE TEMP TABLE before AS SELECT * FROM executions`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 230))
	rows, err := db.Query(`SELECT id,diff_output FROM executions`)
	require.NoError(t, err)
	got := map[string]string{}
	for rows.Next() {
		var id, diff string
		require.NoError(t, rows.Scan(&id, &diff))
		got[id] = diff
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, map[string]string{"old": "", "tie-first": "", "winner": "latest", "late-insert": "", "empty-newest": "", "other": "other", "empty": ""}, got)
	_, err = db.Exec(`UPDATE before SET diff_output=(SELECT diff_output FROM executions WHERE executions.id=before.id)`)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM (SELECT * FROM before EXCEPT SELECT * FROM executions)`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 229))
	require.NoError(t, goose.UpTo(db, ".", 230))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE diff_output != ''`).Scan(&count))
	require.Equal(t, 2, count)
}
