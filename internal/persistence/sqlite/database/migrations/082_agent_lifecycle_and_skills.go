package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("082_agent_lifecycle_and_skills.go", upAgentLifecycleAndSkills082, downAgentLifecycleAndSkills082)
}

func upAgentLifecycleAndSkills082(ctx context.Context, tx *sql.Tx) error {
	if err := addColumnIfMissing082(ctx, tx, "agents", "key", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "scope", "TEXT NOT NULL DEFAULT 'global'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "project_id", "TEXT NULL REFERENCES projects(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "selectable_as_primary", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "enabled", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "permission_defaults_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "model_defaults_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "created_by", "TEXT NOT NULL DEFAULT 'user'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "generated_status", "TEXT NOT NULL DEFAULT 'user_edited'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "absorbed_into", "TEXT NULL"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "source_refs_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := addColumnIfMissing082(ctx, tx, "agents", "archived_at", "DATETIME NULL"); err != nil {
		return err
	}

	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_key_unique ON agents(key) WHERE key <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_agents_generated_status ON agents(generated_status)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_scope_project ON agents(scope, project_id)`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	if err := ensureAgentLifecycleHooks082(ctx, tx); err != nil {
		return err
	}
	if err := ensureLifecycleExecutions082(ctx, tx); err != nil {
		return err
	}
	if err := ensureAgentConfigMutations082(ctx, tx); err != nil {
		return err
	}
	if err := ensureLifecycleExecutionEvents082(ctx, tx); err != nil {
		return err
	}
	if err := seedSkillCurator082(ctx, tx); err != nil {
		return err
	}
	if err := normalizeSkillCurator082(ctx, tx); err != nil {
		return err
	}
	return nil
}

func downAgentLifecycleAndSkills082(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX IF EXISTS idx_lifecycle_execution_events_exec_seq`,
		`DROP TABLE IF EXISTS lifecycle_execution_events`,
		`DROP INDEX IF EXISTS idx_agent_config_mutations_idempotency`,
		`DROP INDEX IF EXISTS idx_agent_config_mutations_target`,
		`DROP INDEX IF EXISTS idx_agent_config_mutations_task`,
		`DROP INDEX IF EXISTS idx_agent_config_mutations_exec`,
		`DROP TABLE IF EXISTS agent_config_mutations`,
		`DROP INDEX IF EXISTS idx_lifecycle_executions_idempotency`,
		`DROP INDEX IF EXISTS idx_lifecycle_executions_hook`,
		`DROP INDEX IF EXISTS idx_lifecycle_executions_when`,
		`DROP INDEX IF EXISTS idx_lifecycle_executions_task_run`,
		`DROP INDEX IF EXISTS idx_lifecycle_executions_task`,
		`DROP TABLE IF EXISTS lifecycle_executions`,
		`DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_when`,
		`DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_agent`,
		`DROP TABLE IF EXISTS agent_lifecycle_hooks`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func ensureAgentLifecycleHooks082(ctx context.Context, tx *sql.Tx) error {
	if exists, err := tableExists082(ctx, tx, "agent_lifecycle_hooks"); err != nil {
		return err
	} else if !exists {
		_, err := tx.ExecContext(ctx, `
CREATE TABLE agent_lifecycle_hooks (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    agent_id         TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    when_slot        TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
    skill_key        TEXT NOT NULL,
    prompt_override  TEXT NOT NULL DEFAULT '',
    output_contract  TEXT NOT NULL DEFAULT '' CHECK (output_contract IN ('','selected_mode','selected_skills','context_block','activity_summary','learning_summary','library_update_summary')),
    blocking         INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1,
    permissions_json TEXT NOT NULL DEFAULT '{}',
    run_policy_json  TEXT NOT NULL DEFAULT '{}',
    schedule_json    TEXT,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
)`)
		if err != nil {
			return err
		}
	} else {
		if err := rebuildAgentLifecycleHooks082(ctx, tx); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_agent ON agent_lifecycle_hooks(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_when ON agent_lifecycle_hooks(when_slot, enabled);
`)
	return err
}

func rebuildAgentLifecycleHooks082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE agent_lifecycle_hooks_082 (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    agent_id         TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    when_slot        TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
    skill_key        TEXT NOT NULL,
    prompt_override  TEXT NOT NULL DEFAULT '',
    output_contract  TEXT NOT NULL DEFAULT '' CHECK (output_contract IN ('','selected_mode','selected_skills','context_block','activity_summary','learning_summary','library_update_summary')),
    blocking         INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1,
    permissions_json TEXT NOT NULL DEFAULT '{}',
    run_policy_json  TEXT NOT NULL DEFAULT '{}',
    schedule_json    TEXT,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO agent_lifecycle_hooks_082 (
    id, agent_id, when_slot, skill_key, prompt_override, output_contract,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
)
SELECT
    id, agent_id, when_slot, skill_key, prompt_override,
    CASE
        WHEN agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator')
             AND when_slot = 'route_task'
             AND skill_key = 'route_task'
        THEN 'selected_skills'
        WHEN output_contract = 'selected_skills' THEN 'selected_skills'
        ELSE output_contract
    END,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
FROM agent_lifecycle_hooks;
DROP TABLE agent_lifecycle_hooks;
ALTER TABLE agent_lifecycle_hooks_082 RENAME TO agent_lifecycle_hooks;
`)
	return err
}

func ensureLifecycleExecutions082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS lifecycle_executions (
    id                   TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id              TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_run_id          TEXT NOT NULL DEFAULT '',
    agent_id             TEXT REFERENCES agents(id) ON DELETE SET NULL,
    when_slot            TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
    lifecycle_hook_id    TEXT REFERENCES agent_lifecycle_hooks(id) ON DELETE SET NULL,
    parent_execution_id  TEXT REFERENCES lifecycle_executions(id) ON DELETE SET NULL,
    skill_key            TEXT NOT NULL DEFAULT '',
    output_contract      TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed','skipped')),
    input_json           TEXT NOT NULL DEFAULT '{}',
    output_json          TEXT NOT NULL DEFAULT '{}',
    error                TEXT NOT NULL DEFAULT '',
    attempt_count        INTEGER NOT NULL DEFAULT 0,
    priority             INTEGER NOT NULL DEFAULT 0,
    next_retry_at        DATETIME,
    idempotency_key      TEXT NOT NULL DEFAULT '',
    started_at           DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at         DATETIME
);
CREATE INDEX IF NOT EXISTS idx_lifecycle_executions_task ON lifecycle_executions(task_id);
CREATE INDEX IF NOT EXISTS idx_lifecycle_executions_task_run ON lifecycle_executions(task_run_id);
CREATE INDEX IF NOT EXISTS idx_lifecycle_executions_when ON lifecycle_executions(when_slot);
CREATE INDEX IF NOT EXISTS idx_lifecycle_executions_hook ON lifecycle_executions(lifecycle_hook_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lifecycle_executions_idempotency
    ON lifecycle_executions(idempotency_key)
    WHERE idempotency_key <> '' AND status = 'completed';
`)
	return err
}

func ensureAgentConfigMutations082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS agent_config_mutations (
    id                       TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    lifecycle_execution_id   TEXT REFERENCES lifecycle_executions(id) ON DELETE SET NULL,
    task_id                  TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    task_run_id              TEXT NOT NULL DEFAULT '',
    project_id               TEXT NOT NULL DEFAULT '',
    actor_agent_id           TEXT REFERENCES agents(id) ON DELETE SET NULL,
    target_type              TEXT NOT NULL CHECK (target_type IN ('agent','skill','routing','hook','support_file')),
    target_key               TEXT NOT NULL DEFAULT '',
    action                   TEXT NOT NULL,
    proposed_payload_json    TEXT NOT NULL DEFAULT '{}',
    validation_status        TEXT NOT NULL DEFAULT 'applied' CHECK (validation_status IN ('applied','blocked','no_op')),
    validation_errors_json   TEXT NOT NULL DEFAULT '[]',
    changed_paths_json       TEXT NOT NULL DEFAULT '[]',
    imported_config_changes_json TEXT NOT NULL DEFAULT '[]',
    evidence_refs_json       TEXT NOT NULL DEFAULT '[]',
    idempotency_key          TEXT NOT NULL DEFAULT '',
    created_at               DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_agent_config_mutations_exec ON agent_config_mutations(lifecycle_execution_id);
CREATE INDEX IF NOT EXISTS idx_agent_config_mutations_task ON agent_config_mutations(task_id);
CREATE INDEX IF NOT EXISTS idx_agent_config_mutations_target ON agent_config_mutations(target_type, target_key);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_config_mutations_idempotency
    ON agent_config_mutations(idempotency_key)
    WHERE idempotency_key <> '' AND validation_status = 'applied';
`)
	return err
}

func ensureLifecycleExecutionEvents082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS lifecycle_execution_events (
    id                     TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    lifecycle_execution_id TEXT NOT NULL REFERENCES lifecycle_executions(id) ON DELETE CASCADE,
    seq                    INTEGER NOT NULL,
    event_type             TEXT NOT NULL,
    payload_json           TEXT NOT NULL DEFAULT '{}',
    created_at             DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(lifecycle_execution_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_lifecycle_execution_events_exec_seq
    ON lifecycle_execution_events(lifecycle_execution_id, seq);
`)
	return err
}

func seedSkillCurator082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO agents (
  id, name, description, system_prompt, model, tools, tool_config,
  plugins, mcp_servers, skills, system_kind,
  key, scope, project_id, selectable_as_primary, enabled,
  permission_defaults_json, model_defaults_json,
  created_by, generated_status, absorbed_into, source_refs_json
) VALUES (
  'sys_skill_curator_00000000000000000001',
  'System: Skill Curator',
  'Built-in system agent that selects relevant skills for task turns and maintains the skill library on a schedule.',
  '',
  'inherit',
  '["skill_view","skills_list","agent_list","agent_view","skill_manage","skill_import","agent_skill_manage"]',
  '{}', '[]', '[]', '[]',
  'skill_curator',
  'skill_curator', 'global', NULL, 0, 1,
  '{"read_task_prompt":true,"read_task_execution":true,"read_agents":true,"read_skills":true,"write_skills":true}',
  '{}',
  'system', 'protected', NULL, '[]'
) ON CONFLICT(id) DO NOTHING;

INSERT INTO agent_lifecycle_hooks (
  id, agent_id, when_slot, skill_key, prompt_override, output_contract,
  blocking, enabled, permissions_json, run_policy_json, schedule_json
) VALUES
 ('hk_skill_curator_route_0000000001',
  'sys_skill_curator_00000000000000000001', 'route_task',
  'route_task', '', 'selected_skills',
  0, 1, '{"read_agents":true,"read_skills":true,"read_task_prompt":true}', '{"when":"auto_routing_enabled"}', NULL),
 ('hk_skill_curator_after_complete_01',
  'sys_skill_curator_00000000000000000001', 'after_complete',
  'observe_task_for_learning', '', 'learning_summary',
  0, 1, '{"read_agents":true,"read_skills":true,"write_skills":true,"read_task_execution":true}', '{"when":"throttled","batch_size":5}', NULL)
ON CONFLICT(id) DO NOTHING;

UPDATE agents
SET tools = '["skill_view","skills_list","agent_list","agent_view","skill_manage","skill_import","agent_skill_manage"]',
    tool_config = CASE WHEN tool_config IS NULL OR tool_config = '' THEN '{}' ELSE tool_config END,
    permission_defaults_json = '{"read_task_prompt":true,"read_task_execution":true,"read_agents":true,"read_skills":true,"write_skills":true}',
    name = 'System: Skill Curator',
    description = 'Built-in system agent that selects relevant skills for task turns and maintains the skill library on a schedule.',
    key = 'skill_curator',
    scope = 'global',
    selectable_as_primary = 0,
    enabled = 1,
    created_by = 'system',
    generated_status = 'protected',
    updated_at = datetime('now')
WHERE system_kind = 'skill_curator' OR id = 'sys_skill_curator_00000000000000000001';

UPDATE agent_lifecycle_hooks
SET output_contract = 'selected_skills',
    blocking = 0,
    permissions_json = '{"read_agents":true,"read_skills":true,"read_task_prompt":true}',
    updated_at = datetime('now')
WHERE when_slot = 'route_task'
  AND skill_key = 'route_task'
  AND agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator' OR key = 'skill_curator');

UPDATE agent_lifecycle_hooks
SET permissions_json = '{"read_agents":true,"read_skills":true,"write_skills":true,"read_task_execution":true}',
    updated_at = datetime('now')
WHERE when_slot = 'after_complete'
  AND skill_key = 'observe_task_for_learning'
  AND agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator' OR key = 'skill_curator');
`)
	return err
}

func normalizeSkillCurator082(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
UPDATE agent_lifecycle_hooks
SET skill_key = 'maintain_skill_library',
    updated_at = datetime('now')
WHERE skill_key = 'maintain_agent_skill_library'
  AND agent_id IN (SELECT id FROM agents WHERE system_kind = 'agent_creator' OR key = 'agent_creator' OR system_kind = 'skill_curator' OR key = 'skill_curator');

UPDATE agents
SET name = 'System: Skill Curator',
    description = 'Built-in system agent that selects relevant skills for task turns and maintains the skill library on a schedule.',
    system_kind = 'skill_curator',
    key = 'skill_curator',
    updated_at = datetime('now')
WHERE (system_kind = 'agent_creator' OR key = 'agent_creator' OR name = 'System: Agent Creator')
  AND NOT EXISTS (
    SELECT 1 FROM agents clean
    WHERE clean.id <> agents.id
      AND (clean.system_kind = 'skill_curator' OR clean.key = 'skill_curator')
  );

DELETE FROM agents
WHERE (system_kind = 'agent_creator' OR key = 'agent_creator' OR name = 'System: Agent Creator')
  AND EXISTS (
    SELECT 1 FROM agents clean
    WHERE clean.id <> agents.id
      AND (clean.system_kind = 'skill_curator' OR clean.key = 'skill_curator')
  );

DELETE FROM agent_lifecycle_hooks
WHERE agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator' OR key = 'skill_curator')
  AND rowid NOT IN (
    SELECT MIN(rowid)
    FROM agent_lifecycle_hooks
    WHERE agent_id IN (SELECT id FROM agents WHERE system_kind = 'skill_curator' OR key = 'skill_curator')
    GROUP BY agent_id, when_slot, skill_key
  );

UPDATE agents
SET tools = '["skill_view","skills_list","agent_list","agent_view","skill_manage","skill_import","agent_skill_manage"]',
    updated_at = datetime('now')
WHERE system_kind = 'skill_curator'
  AND (tools IS NULL OR tools = '' OR tools = '[]');
`)
	return err
}

func addColumnIfMissing082(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	exists, err := columnExists082(ctx, tx, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func tableExists082(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func columnExists082(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteIdent082(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func quoteIdent082(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
