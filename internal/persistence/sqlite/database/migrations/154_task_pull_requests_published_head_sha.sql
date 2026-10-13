-- +goose Up
ALTER TABLE task_pull_requests ADD COLUMN published_head_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE task_pull_requests DROP COLUMN published_head_sha;
