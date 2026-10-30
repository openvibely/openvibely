package database

import (
	"database/sql"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestMigration218CombinesOverridesWithoutLosingUnsetFields(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "overrides.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 217))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES('p','P');
 INSERT INTO tasks(id,project_id,title) VALUES('t','p','T');
 INSERT INTO agent_configs(id,name,model) VALUES('both','Both','new'),('effort','Effort','new'),('tier','Tier','new'),('split','Split','new');
 INSERT INTO task_model_efforts VALUES('t','both','new','high'),('t','effort','new',''),('t','split','old','medium');
 INSERT INTO task_model_service_tiers VALUES('t','both','new','priority'),('t','tier','new',''),('t','split','new','default');`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 218))
	for _, tc := range []struct {
		id, model    string
		effort, tier sql.NullString
	}{
		{"both", "new", sql.NullString{String: "high", Valid: true}, sql.NullString{String: "priority", Valid: true}},
		{"effort", "new", sql.NullString{Valid: true}, sql.NullString{}},
		{"tier", "new", sql.NullString{}, sql.NullString{Valid: true}},
		{"split", "old", sql.NullString{String: "medium", Valid: true}, sql.NullString{}},
		{"split", "new", sql.NullString{}, sql.NullString{String: "default", Valid: true}},
	} {
		var effort, tier sql.NullString
		require.NoError(t, db.QueryRow(`SELECT effort,service_tier FROM task_model_overrides WHERE task_id='t' AND agent_config_id=? AND model=?`, tc.id, tc.model).Scan(&effort, &tier))
		require.Equal(t, tc.effort, effort)
		require.Equal(t, tc.tier, tier)
	}
	var oldTables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('task_model_efforts','task_model_service_tiers')`).Scan(&oldTables))
	require.Zero(t, oldTables)
	require.NoError(t, goose.DownTo(db, ".", 217))
	var effort, tier, model string
	require.NoError(t, db.QueryRow(`SELECT effort,model FROM task_model_efforts WHERE agent_config_id='split'`).Scan(&effort, &model))
	require.Equal(t, "medium", effort)
	require.Equal(t, "old", model)
	require.NoError(t, db.QueryRow(`SELECT service_tier FROM task_model_service_tiers WHERE agent_config_id='split'`).Scan(&tier))
	require.Equal(t, "default", tier)
	require.NoError(t, goose.UpTo(db, ".", 218))
	_, err = db.Exec(`DELETE FROM tasks WHERE id='t'`)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM task_model_overrides`).Scan(&count))
	require.Zero(t, count)
}
