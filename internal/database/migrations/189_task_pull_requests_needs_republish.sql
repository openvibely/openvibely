-- +goose Up
ALTER TABLE task_pull_requests ADD COLUMN needs_republish INTEGER NOT NULL DEFAULT 0 CHECK (needs_republish IN (0, 1));

-- +goose Down
ALTER TABLE task_pull_requests DROP COLUMN needs_republish;
