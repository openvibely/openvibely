-- +goose Up
CREATE TABLE IF NOT EXISTS task_goals (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  goal_id TEXT NOT NULL UNIQUE,
  objective TEXT NOT NULL,
  status TEXT NOT NULL CHECK (
    status IN ('active', 'paused', 'achieved', 'blocked', 'cleared', 'failed')
  ),
  reason TEXT NOT NULL DEFAULT '',
  blocker_key TEXT NOT NULL DEFAULT '',
  blocker_count INTEGER NOT NULL DEFAULT 0,
  blocker_reason TEXT NOT NULL DEFAULT '',
  blocker_last_seen_at TEXT,
  last_checked_at TEXT,
  achieved_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_task_goals_status ON task_goals(status);

-- +goose Down
DROP TABLE IF EXISTS task_goals;
