package database

import (
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestDisplayMetadataDowngradeMaterializesSelfPointers(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "display.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, transcriptStageTo(db, migrations.TranscriptStageMetadata))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('page-project','page');
 INSERT INTO tasks(id,project_id,title,category,priority,status,prompt) VALUES('page-task','page-project','page','backlog',2,'completed','test');
 INSERT INTO executions(id,task_id,status) VALUES('page-exec','page-task','completed');
 INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes) VALUES('page-exec',0,'display','display:0',0,'put','{"type":"text","text":"preserved"}','page',9);`)
	require.NoError(t, err)
	require.NoError(t, transcriptStageTo(db, migrations.TranscriptStageStreamText))
	var text string
	require.NoError(t, db.QueryRow(`SELECT json_extract(base.payload,'$.text') FROM conversation_items head JOIN conversation_items base ON base.sequence=head.snapshot_sequence WHERE head.execution_id='page-exec'`).Scan(&text))
	require.Equal(t, "preserved", text)
	require.NoError(t, transcriptStageTo(db, migrations.TranscriptStageMetadata))
}
