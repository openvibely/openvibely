-- +goose Up
ALTER TABLE task_computer_sessions ADD COLUMN enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1));

-- +goose Down
ALTER TABLE task_computer_sessions DROP COLUMN enabled;
