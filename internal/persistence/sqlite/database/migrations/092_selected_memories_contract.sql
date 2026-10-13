-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_agent;
DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_when;

CREATE TABLE agent_lifecycle_hooks_092 (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    agent_id         TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    when_slot        TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
    skill_key        TEXT NOT NULL,
    prompt_override  TEXT NOT NULL DEFAULT '',
    output_contract  TEXT NOT NULL DEFAULT '' CHECK (output_contract IN ('','selected_mode','selected_skills','selected_memories','context_block','activity_summary','learning_summary','library_update_summary')),
    blocking         INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1,
    permissions_json TEXT NOT NULL DEFAULT '{}',
    run_policy_json  TEXT NOT NULL DEFAULT '{}',
    schedule_json    TEXT,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO agent_lifecycle_hooks_092 (
    id, agent_id, when_slot, skill_key, prompt_override, output_contract,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
)
SELECT
    id, agent_id, when_slot, skill_key, prompt_override, output_contract,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
FROM agent_lifecycle_hooks;

DROP TABLE agent_lifecycle_hooks;
ALTER TABLE agent_lifecycle_hooks_092 RENAME TO agent_lifecycle_hooks;

CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_agent ON agent_lifecycle_hooks(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_when ON agent_lifecycle_hooks(when_slot, enabled);

PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_agent;
DROP INDEX IF EXISTS idx_agent_lifecycle_hooks_when;

CREATE TABLE agent_lifecycle_hooks_092_down (
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

INSERT INTO agent_lifecycle_hooks_092_down (
    id, agent_id, when_slot, skill_key, prompt_override, output_contract,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
)
SELECT
    id, agent_id, when_slot, skill_key, prompt_override,
    CASE WHEN output_contract = 'selected_memories' THEN 'context_block' ELSE output_contract END,
    blocking, enabled, permissions_json, run_policy_json, schedule_json,
    created_at, updated_at
FROM agent_lifecycle_hooks;

DROP TABLE agent_lifecycle_hooks;
ALTER TABLE agent_lifecycle_hooks_092_down RENAME TO agent_lifecycle_hooks;

CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_agent ON agent_lifecycle_hooks(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_lifecycle_hooks_when ON agent_lifecycle_hooks(when_slot, enabled);

PRAGMA foreign_keys=ON;
-- +goose StatementEnd
