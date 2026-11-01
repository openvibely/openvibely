-- +goose Up
CREATE TABLE project_computers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 provider TEXT NOT NULL CHECK(provider IN ('local','hosted')),
 held INTEGER NOT NULL DEFAULT 0,
 owner TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE project_computers;
