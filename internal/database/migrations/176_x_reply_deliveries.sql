-- +goose Up
CREATE TABLE x_reply_deliveries (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL,
    reply_to_tweet_id TEXT NOT NULL,
    response_text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'posting', 'sent')),
    provider_post_id TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(task_id, reply_to_tweet_id)
);
CREATE INDEX idx_x_reply_deliveries_pending ON x_reply_deliveries(account_id, created_at) WHERE status = 'pending';
CREATE INDEX idx_x_reply_deliveries_project ON x_reply_deliveries(project_id, created_at DESC);

-- +goose Down
DROP INDEX idx_x_reply_deliveries_project;
DROP INDEX idx_x_reply_deliveries_pending;
DROP TABLE x_reply_deliveries;
