package database

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration231BackfillsDisplayAndKeepsSource(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "display.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 230))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','P');
INSERT INTO tasks(id,project_id,title) VALUES('t','p','T');
INSERT INTO executions(id,task_id) VALUES('e','t'),('live','t');
INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes)
VALUES('e',0,'display','plan',0,'put','{"type":"text","text":"<proposed_plan>keep</proposed_plan>"}','one',35);`)
	require.NoError(t, err)
	raw := strings.Repeat("text\n[Using tool: shell | echo]\n[Tool shell done]\nresult\n[/Tool]\n", 6000)
	_, err = db.Exec(`INSERT INTO execution_stream_text(execution_id,end_offset,text) VALUES('e',?,?)`, len(raw), raw)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO execution_stream_text(execution_id,end_offset,text) VALUES('live',?,?)`, len("<proposed_plan>live plan</proposed_plan>"), "<proposed_plan>live plan</proposed_plan>")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, goose.UpTo(db, ".", 231))
		var plan, got string
		var n int
		require.NoError(t, db.QueryRow(`SELECT content FROM execution_display_plans WHERE execution_id='e'`).Scan(&plan))
		require.Equal(t, "<proposed_plan>keep</proposed_plan>\n", plan)
		require.NoError(t, db.QueryRow(`SELECT content FROM execution_display_plans WHERE execution_id='live'`).Scan(&plan))
		require.Equal(t, "<proposed_plan>live plan</proposed_plan>\n", plan)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM execution_display_groups WHERE execution_id='e'`).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM execution_stream_groups WHERE execution_id='e'`).Scan(&n))
		require.Positive(t, n)
		require.NoError(t, goose.DownTo(db, ".", 230))
		require.NoError(t, db.QueryRow(`SELECT text FROM execution_stream_text WHERE execution_id='e'`).Scan(&got))
		require.Equal(t, raw, got)
	}
}
