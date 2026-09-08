-- +goose Up
CREATE TABLE x_reply_deliveries (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL,
    reply_to_tweet_id TEXT NOT NULL,
    text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'posting', 'sent')),
    provider_post_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(execution_id, reply_to_tweet_id)
);
CREATE INDEX idx_x_reply_deliveries_pending_account ON x_reply_deliveries(account_id, status, created_at) WHERE status = 'pending';

-- +goose Down
DROP INDEX idx_x_reply_deliveries_pending_account;
DROP TABLE x_reply_deliveries;
