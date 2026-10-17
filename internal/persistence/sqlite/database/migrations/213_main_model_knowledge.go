package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("213_main_model_knowledge.go", upMainModelKnowledge, downMainModelKnowledge)
}

func upMainModelKnowledge(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(projects)")
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		exists = exists || name == "skill_memory_permissions"
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN skill_memory_permissions TEXT NOT NULL DEFAULT '{"read_skills":true,"write_skills":true,"read_memories":true,"write_memories":true}'`); err != nil {
			return err
		}
		// Capture the old global/project curator choices before startup repairs bundled agents.
		// A denied permission or disabled recall hook must not become an implicit grant.
		for _, spec := range []struct{ kind, key, old, when, skill string }{
			{"skill_curator", "read_skills", "read_skills", "route_task", "route_task"},
			{"skill_curator", "write_skills", "write_skills", "", ""},
			{"memory_curator", "read_memories", "read_project_memory", "route_task", "recall_memory"},
			{"memory_curator", "write_memories", "write_project_memory", "after_complete", "update_memory"},
		} {
			// Explicit hook values override defaults. If old read/write paths disagreed,
			// preserve the denial: the consolidated control must not broaden access.
			query := `UPDATE projects SET skill_memory_permissions = json_set(skill_memory_permissions, ?, json('false')) WHERE EXISTS (
 SELECT 1 FROM agents a WHERE (a.system_kind = ? OR (? = 'memory_curator' AND a.system_kind IN ('memory', 'memory_consolidator'))) AND (a.project_id = '' OR a.project_id IS NULL OR a.project_id = projects.id)
 AND (a.enabled = 0 OR CASE WHEN EXISTS (
   SELECT 1 FROM agent_lifecycle_hooks h WHERE h.agent_id = a.id AND (h.when_slot = ? OR (h.when_slot = 'before_run' AND ? = 'recall_memory')) AND h.skill_key = ?
 ) THEN EXISTS (
   SELECT 1 FROM agent_lifecycle_hooks h WHERE h.agent_id = a.id AND (h.when_slot = ? OR (h.when_slot = 'before_run' AND ? = 'recall_memory')) AND h.skill_key = ? AND
   (h.enabled = 0 OR COALESCE(json_extract(h.permissions_json, ?), json_extract(a.permission_defaults_json, ?), 0) = 0)
 ) ELSE COALESCE(json_extract(a.permission_defaults_json, ?), 0) = 0 END))`
			if _, err := tx.ExecContext(ctx, query, "$."+spec.key, spec.kind, spec.kind, spec.when, spec.skill, spec.skill, spec.when, spec.skill, spec.skill, "$."+spec.old, "$."+spec.old, "$."+spec.old); err != nil {
				return err
			}
		}
	}
	// Only obsolete hooks belonging to protected built-ins are removed. Custom hooks survive.
	_, err = tx.ExecContext(ctx, `DELETE FROM agent_lifecycle_hooks WHERE
 (agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator') AND when_slot = 'route_task' AND skill_key = 'route_task') OR
 (agent_id IN (SELECT id FROM agents WHERE system_kind IN ('memory_curator', 'memory', 'memory_consolidator')) AND ((when_slot IN ('route_task','before_run') AND skill_key = 'recall_memory') OR (when_slot = 'after_complete' AND skill_key = 'update_memory')))`)
	return err
}

func downMainModelKnowledge(ctx context.Context, tx *sql.Tx) error {
	// Retain user choices on rollback; a subsequent upgrade detects and preserves them.
	_, err := tx.ExecContext(ctx, "SELECT 1")
	return err
}
