package database

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMainModelKnowledgeMigrationPreservesDenialsAndCustomHooksTwice(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "migration.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 212))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('knowledge-project','Knowledge');
 UPDATE agents SET permission_defaults_json = '{"read_skills":true,"write_skills":false}' WHERE system_kind='skill_curator';
 UPDATE agent_lifecycle_hooks SET enabled=0 WHERE agent_id IN (SELECT id FROM agents WHERE system_kind='skill_curator') AND when_slot='route_task';
 INSERT INTO agents(id,name,key,system_kind,enabled,permission_defaults_json) VALUES ('memory-agent-213','Memory Curator','memory_curator','memory_curator',1,'{"read_project_memory":true,"write_project_memory":false}');
 INSERT INTO agent_lifecycle_hooks(id,agent_id,when_slot,skill_key,enabled,permissions_json) VALUES ('recall-213','memory-agent-213','route_task','recall_memory',1,'{"read_project_memory":false}'),('update-213','memory-agent-213','after_complete','update_memory',1,'{}');
 INSERT INTO agents(id,name,key,enabled) VALUES ('custom-213','Custom','custom_213',1);
 INSERT INTO agent_lifecycle_hooks(id,agent_id,when_slot,skill_key,enabled) VALUES ('custom-hook-213','custom-213','route_task','recall_memory',1);`)
	require.NoError(t, err)
	require.NoError(t, goose.Up(db, "."))
	var raw string
	require.NoError(t, db.QueryRow(`SELECT skill_memory_permissions FROM projects WHERE id='knowledge-project'`).Scan(&raw))
	var permissions models.SkillMemoryPermissions
	require.NoError(t, json.Unmarshal([]byte(raw), &permissions))
	require.Equal(t, models.SkillMemoryPermissions{}, permissions)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM agent_lifecycle_hooks WHERE id IN ('recall-213','update-213')`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM agent_lifecycle_hooks WHERE id='custom-hook-213' AND enabled=1`).Scan(&count))
	require.Equal(t, 1, count)
	// Running the migration again must not replace stored project choices with defaults.
	require.NoError(t, goose.DownTo(db, ".", 212))
	require.NoError(t, goose.Up(db, "."))
	var second string
	require.NoError(t, db.QueryRow(`SELECT skill_memory_permissions FROM projects WHERE id='knowledge-project'`).Scan(&second))
	require.Equal(t, raw, second)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM agent_lifecycle_hooks WHERE id='custom-hook-213' AND enabled=1`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestMainModelKnowledgeMigrationScopeAndExplicitOverrides(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "scopes.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 212))
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('one','One'),('two','Two'),('legacy','Legacy');
 UPDATE agents SET permission_defaults_json='{"read_skills":false,"write_skills":true}' WHERE system_kind='skill_curator';
 UPDATE agent_lifecycle_hooks SET enabled=1,permissions_json='{"read_skills":true}' WHERE agent_id IN (SELECT id FROM agents WHERE system_kind='skill_curator') AND when_slot='route_task';
 INSERT INTO agents(id,name,key,system_kind,project_id,enabled,permission_defaults_json) VALUES ('scoped-memory','Scoped memory','scoped_memory','memory_curator','one',1,'{"read_project_memory":true,"write_project_memory":false}');
 INSERT INTO agent_lifecycle_hooks(id,agent_id,when_slot,skill_key,enabled) VALUES ('unrelated-hook-213','scoped-memory','route_task','custom_route',0);
 INSERT INTO agents(id,name,key,system_kind,project_id,enabled,permission_defaults_json) VALUES ('legacy-memory','Legacy memory','legacy_memory','memory','legacy',1,'{"read_project_memory":true,"write_project_memory":false}');
 INSERT INTO agent_lifecycle_hooks(id,agent_id,when_slot,skill_key,enabled) VALUES ('legacy-hook-213','legacy-memory','before_run','recall_memory',0);`)
	require.NoError(t, err)
	require.NoError(t, goose.Up(db, "."))
	var legacyRaw string
	require.NoError(t, db.QueryRow(`SELECT skill_memory_permissions FROM projects WHERE id='legacy'`).Scan(&legacyRaw))
	var legacy models.SkillMemoryPermissions
	require.NoError(t, json.Unmarshal([]byte(legacyRaw), &legacy))
	require.False(t, legacy.ReadMemories)
	require.False(t, legacy.WriteMemories)
	var legacyHooks int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM agent_lifecycle_hooks WHERE id='legacy-hook-213'`).Scan(&legacyHooks))
	require.Zero(t, legacyHooks)
	for _, id := range []string{"one", "two"} {
		var raw string
		require.NoError(t, db.QueryRow(`SELECT skill_memory_permissions FROM projects WHERE id=?`, id).Scan(&raw))
		var permissions models.SkillMemoryPermissions
		require.NoError(t, json.Unmarshal([]byte(raw), &permissions))
		require.True(t, permissions.ReadSkills, "explicit hook permission overrides the default")
		require.True(t, permissions.WriteSkills, "disabled default after-completion learning is not a write-skills denial")
		require.True(t, permissions.ReadMemories)
		require.Equal(t, id == "two", permissions.WriteMemories, "project denial stays within its scope")
	}
}
