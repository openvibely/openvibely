-- +goose Up
ALTER TABLE task_pull_requests ADD COLUMN issue_number INTEGER;
ALTER TABLE task_pull_requests ADD COLUMN issue_url TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_task_pull_requests_issue_number ON task_pull_requests(issue_number);

-- +goose Down
DROP INDEX IF EXISTS idx_task_pull_requests_issue_number;
ALTER TABLE task_pull_requests DROP COLUMN issue_url;
ALTER TABLE task_pull_requests DROP COLUMN issue_number;
