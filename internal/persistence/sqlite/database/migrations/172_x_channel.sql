-- +goose Up
CREATE TABLE x_authorized_users (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    x_user_id TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(project_id, x_user_id)
);
CREATE INDEX idx_x_authorized_users_project ON x_authorized_users(project_id);

CREATE TABLE x_user_projects (
    x_user_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_x_user_projects_project ON x_user_projects(project_id);

CREATE TABLE x_task_context (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL,
    reply_to_tweet_id TEXT NOT NULL,
    x_user_id TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_x_task_context_conversation ON x_task_context(project_id, conversation_id);

CREATE TABLE x_inbound_receipts (
    tweet_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('processing', 'completed')),
    lease_expires_at DATETIME,
    lease_token TEXT NOT NULL DEFAULT '',
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_x_inbound_receipts_project ON x_inbound_receipts(project_id);
CREATE INDEX idx_x_inbound_receipts_task ON x_inbound_receipts(task_id) WHERE task_id IS NOT NULL;

ALTER TABLE thread_inputs ADD COLUMN x_account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN x_conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN x_reply_to_tweet_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN x_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN x_username TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN x_username;
ALTER TABLE thread_inputs DROP COLUMN x_user_id;
ALTER TABLE thread_inputs DROP COLUMN x_reply_to_tweet_id;
ALTER TABLE thread_inputs DROP COLUMN x_conversation_id;
ALTER TABLE thread_inputs DROP COLUMN x_account_id;

DROP INDEX idx_x_inbound_receipts_task;
DROP INDEX idx_x_inbound_receipts_project;
DROP TABLE x_inbound_receipts;
DROP INDEX idx_x_task_context_conversation;
DROP TABLE x_task_context;
DROP INDEX idx_x_user_projects_project;
DROP TABLE x_user_projects;
DROP INDEX idx_x_authorized_users_project;
DROP TABLE x_authorized_users;
