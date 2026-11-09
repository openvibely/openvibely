-- +goose Up
ALTER TABLE task_pull_requests ADD COLUMN head_repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE task_pull_requests ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_pull_requests DROP COLUMN base_branch;
ALTER TABLE task_pull_requests DROP COLUMN head_repo_url;
