-- +goose Up
CREATE TABLE schedule_skips (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    schedule_id TEXT NOT NULL DEFAULT '',
    start_at INTEGER NOT NULL,
    end_at INTEGER NOT NULL CHECK (end_at > start_at),
    restored INTEGER NOT NULL DEFAULT 0 CHECK (restored IN (0, 1)),
    PRIMARY KEY (project_id, schedule_id, start_at, end_at, restored)
);
CREATE TABLE schedule_project_pauses (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER schedule_skips_delete_schedule AFTER DELETE ON schedules
BEGIN
    DELETE FROM schedule_skips WHERE schedule_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER schedule_skips_delete_schedule;
DROP TABLE schedule_project_pauses;
DROP TABLE schedule_skips;
