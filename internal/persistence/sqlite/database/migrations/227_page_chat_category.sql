-- +goose Up
-- +goose NO TRANSACTION
-- Page side-chat tasks get their own hidden category instead of the unused legacy 'freezer'.

PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;

DROP TRIGGER IF EXISTS schedule_admission_task_withdrawn;
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;
DROP TRIGGER IF EXISTS all_projects_chat_only_insert;
DROP TRIGGER IF EXISTS all_projects_chat_only_update;
DROP TRIGGER IF EXISTS task_checkpoint_cleanup;
DROP INDEX IF EXISTS idx_tasks_project_id;
DROP INDEX IF EXISTS idx_tasks_category;
DROP INDEX IF EXISTS idx_tasks_status;
DROP INDEX IF EXISTS idx_tasks_project_title;
DROP INDEX IF EXISTS idx_tasks_display_order;
DROP INDEX IF EXISTS idx_tasks_parent_task_id;
DROP INDEX IF EXISTS idx_tasks_swarm_parent;
DROP INDEX IF EXISTS idx_tasks_discovery_order;
DROP INDEX IF EXISTS idx_tasks_active_pending_admission_order;

ALTER TABLE tasks RENAME TO tasks_old;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'active'
                 CHECK (category IN ('active', 'freezer', 'completed', 'backlog', 'scheduled', 'chat', 'page_chat')),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled', 'blocked')),
    prompt TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES agent_configs(id),
    tag TEXT NOT NULL DEFAULT ''
                 CHECK (tag IN ('', 'feature', 'bug')),
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    parent_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    chain_config TEXT NOT NULL DEFAULT '{}',
    execution_cost_cents INTEGER NOT NULL DEFAULT 0,
    execution_duration_ms INTEGER NOT NULL DEFAULT 0,
    worktree_path TEXT NOT NULL DEFAULT '',
    worktree_branch TEXT NOT NULL DEFAULT '',
    auto_merge INTEGER NOT NULL DEFAULT 0,
    merge_target_branch TEXT NOT NULL DEFAULT '',
    merge_status TEXT NOT NULL DEFAULT ''
        CHECK (merge_status IN ('', 'pending', 'merged', 'failed', 'conflict')),
    created_via TEXT NOT NULL DEFAULT '',
    telegram_chat_id INTEGER NOT NULL DEFAULT 0,
    agent_definition_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    base_commit_sha TEXT NOT NULL DEFAULT '',
    lineage_depth INTEGER NOT NULL DEFAULT 0,
    completed_at DATETIME,
    swarm_role TEXT NOT NULL DEFAULT '',
    swarm_status TEXT NOT NULL DEFAULT '',
    swarm_config TEXT NOT NULL DEFAULT '{}',
    swarm_sequence INTEGER NOT NULL DEFAULT 0,
    auto_merge_on_goal_achieved INTEGER NOT NULL DEFAULT 0,
    author_user_id TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    title_explicitly_set INTEGER NOT NULL DEFAULT 0
);

INSERT INTO tasks (id,project_id,title,category,priority,status,prompt,agent_id,tag,display_order,created_at,updated_at,parent_task_id,chain_config,execution_cost_cents,execution_duration_ms,worktree_path,worktree_branch,auto_merge,merge_target_branch,merge_status,created_via,telegram_chat_id,agent_definition_id,base_branch,base_commit_sha,lineage_depth,completed_at,swarm_role,swarm_status,swarm_config,swarm_sequence,auto_merge_on_goal_achieved,author_user_id,author_name,title_explicitly_set)
SELECT id,project_id,title,category,priority,status,prompt,agent_id,tag,display_order,created_at,updated_at,parent_task_id,chain_config,execution_cost_cents,execution_duration_ms,worktree_path,worktree_branch,auto_merge,merge_target_branch,merge_status,created_via,telegram_chat_id,agent_definition_id,base_branch,base_commit_sha,lineage_depth,completed_at,swarm_role,swarm_status,swarm_config,swarm_sequence,auto_merge_on_goal_achieved,author_user_id,author_name,title_explicitly_set FROM tasks_old;

DROP TABLE tasks_old;

UPDATE tasks SET category = 'page_chat' WHERE id IN (SELECT task_id FROM page_conversations);

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_category ON tasks(category);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE UNIQUE INDEX idx_tasks_project_title ON tasks(project_id, title);
CREATE INDEX idx_tasks_display_order ON tasks(project_id, category, display_order);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);
CREATE INDEX idx_tasks_swarm_parent ON tasks(parent_task_id, swarm_role, swarm_sequence);
CREATE INDEX idx_tasks_discovery_order ON tasks(project_id, updated_at DESC, id ASC) WHERE category != 'chat';
CREATE INDEX idx_tasks_active_pending_admission_order
    ON tasks(category, status, priority DESC, display_order ASC, created_at ASC)
    WHERE category = 'active' AND status = 'pending';

-- +goose StatementBegin
CREATE TRIGGER schedule_admission_task_withdrawn AFTER UPDATE OF status, category ON tasks
WHEN NEW.status IN ('cancelled','failed','completed') OR NEW.category NOT IN ('active','scheduled')
BEGIN
    UPDATE tasks SET status = 'cancelled', updated_at = datetime('now')
    WHERE status = 'pending' AND id != NEW.id AND id IN (
        SELECT task_id FROM schedule_dispatch_admissions
        WHERE schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id)
    );
    DELETE FROM schedule_dispatch_admissions
    WHERE task_id = NEW.id OR schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_update
AFTER UPDATE OF project_id, category ON tasks
WHEN OLD.project_id IS NOT NEW.project_id
  OR ((OLD.category = 'chat') != (NEW.category = 'chat'))
BEGIN
    UPDATE executions
    SET task_project_id = NEW.project_id,
        task_category = NEW.category
    WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_insert BEFORE INSERT ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_update BEFORE UPDATE OF project_id, category ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_checkpoint_cleanup AFTER DELETE ON tasks
BEGIN
    DELETE FROM chat_compaction_checkpoints
    WHERE scope_type = 'task' AND scope_id = OLD.id;
END;
-- +goose StatementEnd

PRAGMA foreign_keys=ON;
PRAGMA legacy_alter_table=OFF;

-- +goose Down
-- +goose NO TRANSACTION

PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;

UPDATE tasks SET category = 'freezer' WHERE category = 'page_chat';

DROP TRIGGER IF EXISTS schedule_admission_task_withdrawn;
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;
DROP TRIGGER IF EXISTS all_projects_chat_only_insert;
DROP TRIGGER IF EXISTS all_projects_chat_only_update;
DROP TRIGGER IF EXISTS task_checkpoint_cleanup;
DROP INDEX IF EXISTS idx_tasks_project_id;
DROP INDEX IF EXISTS idx_tasks_category;
DROP INDEX IF EXISTS idx_tasks_status;
DROP INDEX IF EXISTS idx_tasks_project_title;
DROP INDEX IF EXISTS idx_tasks_display_order;
DROP INDEX IF EXISTS idx_tasks_parent_task_id;
DROP INDEX IF EXISTS idx_tasks_swarm_parent;
DROP INDEX IF EXISTS idx_tasks_discovery_order;
DROP INDEX IF EXISTS idx_tasks_active_pending_admission_order;

ALTER TABLE tasks RENAME TO tasks_old;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'active'
                 CHECK (category IN ('active', 'freezer', 'completed', 'backlog', 'scheduled', 'chat')),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled', 'blocked')),
    prompt TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES agent_configs(id),
    tag TEXT NOT NULL DEFAULT ''
                 CHECK (tag IN ('', 'feature', 'bug')),
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    parent_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    chain_config TEXT NOT NULL DEFAULT '{}',
    execution_cost_cents INTEGER NOT NULL DEFAULT 0,
    execution_duration_ms INTEGER NOT NULL DEFAULT 0,
    worktree_path TEXT NOT NULL DEFAULT '',
    worktree_branch TEXT NOT NULL DEFAULT '',
    auto_merge INTEGER NOT NULL DEFAULT 0,
    merge_target_branch TEXT NOT NULL DEFAULT '',
    merge_status TEXT NOT NULL DEFAULT ''
        CHECK (merge_status IN ('', 'pending', 'merged', 'failed', 'conflict')),
    created_via TEXT NOT NULL DEFAULT '',
    telegram_chat_id INTEGER NOT NULL DEFAULT 0,
    agent_definition_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    base_commit_sha TEXT NOT NULL DEFAULT '',
    lineage_depth INTEGER NOT NULL DEFAULT 0,
    completed_at DATETIME,
    swarm_role TEXT NOT NULL DEFAULT '',
    swarm_status TEXT NOT NULL DEFAULT '',
    swarm_config TEXT NOT NULL DEFAULT '{}',
    swarm_sequence INTEGER NOT NULL DEFAULT 0,
    auto_merge_on_goal_achieved INTEGER NOT NULL DEFAULT 0,
    author_user_id TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    title_explicitly_set INTEGER NOT NULL DEFAULT 0
);

INSERT INTO tasks (id,project_id,title,category,priority,status,prompt,agent_id,tag,display_order,created_at,updated_at,parent_task_id,chain_config,execution_cost_cents,execution_duration_ms,worktree_path,worktree_branch,auto_merge,merge_target_branch,merge_status,created_via,telegram_chat_id,agent_definition_id,base_branch,base_commit_sha,lineage_depth,completed_at,swarm_role,swarm_status,swarm_config,swarm_sequence,auto_merge_on_goal_achieved,author_user_id,author_name,title_explicitly_set)
SELECT id,project_id,title,category,priority,status,prompt,agent_id,tag,display_order,created_at,updated_at,parent_task_id,chain_config,execution_cost_cents,execution_duration_ms,worktree_path,worktree_branch,auto_merge,merge_target_branch,merge_status,created_via,telegram_chat_id,agent_definition_id,base_branch,base_commit_sha,lineage_depth,completed_at,swarm_role,swarm_status,swarm_config,swarm_sequence,auto_merge_on_goal_achieved,author_user_id,author_name,title_explicitly_set FROM tasks_old;

DROP TABLE tasks_old;

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_category ON tasks(category);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE UNIQUE INDEX idx_tasks_project_title ON tasks(project_id, title);
CREATE INDEX idx_tasks_display_order ON tasks(project_id, category, display_order);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);
CREATE INDEX idx_tasks_swarm_parent ON tasks(parent_task_id, swarm_role, swarm_sequence);
CREATE INDEX idx_tasks_discovery_order ON tasks(project_id, updated_at DESC, id ASC) WHERE category != 'chat';
CREATE INDEX idx_tasks_active_pending_admission_order
    ON tasks(category, status, priority DESC, display_order ASC, created_at ASC)
    WHERE category = 'active' AND status = 'pending';

-- +goose StatementBegin
CREATE TRIGGER schedule_admission_task_withdrawn AFTER UPDATE OF status, category ON tasks
WHEN NEW.status IN ('cancelled','failed','completed') OR NEW.category NOT IN ('active','scheduled')
BEGIN
    UPDATE tasks SET status = 'cancelled', updated_at = datetime('now')
    WHERE status = 'pending' AND id != NEW.id AND id IN (
        SELECT task_id FROM schedule_dispatch_admissions
        WHERE schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id)
    );
    DELETE FROM schedule_dispatch_admissions
    WHERE task_id = NEW.id OR schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_update
AFTER UPDATE OF project_id, category ON tasks
WHEN OLD.project_id IS NOT NEW.project_id
  OR ((OLD.category = 'chat') != (NEW.category = 'chat'))
BEGIN
    UPDATE executions
    SET task_project_id = NEW.project_id,
        task_category = NEW.category
    WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_insert BEFORE INSERT ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_update BEFORE UPDATE OF project_id, category ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_checkpoint_cleanup AFTER DELETE ON tasks
BEGIN
    DELETE FROM chat_compaction_checkpoints
    WHERE scope_type = 'task' AND scope_id = OLD.id;
END;
-- +goose StatementEnd

PRAGMA foreign_keys=ON;
PRAGMA legacy_alter_table=OFF;
