-- +goose Up
CREATE TABLE IF NOT EXISTS github_pr_feedback_forwarded (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_pull_request_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    repo_full_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    feedback_kind TEXT NOT NULL CHECK (feedback_kind IN ('issue_comment', 'review', 'review_comment')),
    github_id TEXT NOT NULL,
    github_node_id TEXT,
    author_login TEXT NOT NULL,
    html_url TEXT,
    body TEXT,
    created_at TEXT NOT NULL,
    forwarded_at TEXT NOT NULL DEFAULT (datetime('now')),
    queued_thread_input_id TEXT,
    FOREIGN KEY(task_pull_request_id) REFERENCES task_pull_requests(id) ON DELETE CASCADE,
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY(queued_thread_input_id) REFERENCES thread_inputs(id) ON DELETE SET NULL,
    UNIQUE(repo_full_name, pr_number, feedback_kind, github_id)
);

CREATE INDEX IF NOT EXISTS idx_github_pr_feedback_forwarded_task_pr
    ON github_pr_feedback_forwarded(task_pull_request_id, forwarded_at);

CREATE INDEX IF NOT EXISTS idx_github_pr_feedback_forwarded_task
    ON github_pr_feedback_forwarded(task_id, forwarded_at);

-- +goose Down
DROP TABLE IF EXISTS github_pr_feedback_forwarded;
