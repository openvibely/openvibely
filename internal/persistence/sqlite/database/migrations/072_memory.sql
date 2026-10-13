-- +goose NO TRANSACTION

-- +goose Up
-- Memory subsystem: per-project auto-memory storage and consolidation.
-- The memory store itself lives in the selected project repo under
-- .openvibely/memory. The DB tracks settings/run metadata. Memory
-- Consolidation itself is represented as a
-- normal system-created task plus a normal schedule row so the Schedule page
-- uses the existing task schedule functionality.

PRAGMA foreign_keys=ON;

CREATE TABLE project_memory_settings (
    project_id   TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE memory_extraction_runs (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind   TEXT NOT NULL,
    source_id     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'running'
                  CHECK (status IN ('running', 'ok', 'nothing', 'error')),
    reason        TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    touched_paths TEXT NOT NULL DEFAULT '[]',
    started_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at  DATETIME
);
CREATE INDEX idx_memory_extraction_runs_project_id ON memory_extraction_runs(project_id);
CREATE INDEX idx_memory_extraction_runs_started_at ON memory_extraction_runs(started_at);

CREATE TABLE memory_consolidation_runs (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'ok', 'nothing', 'error')),
    error_message   TEXT NOT NULL DEFAULT '',
    touched_paths   TEXT NOT NULL DEFAULT '[]',
    notes           TEXT NOT NULL DEFAULT '[]',
    started_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at    DATETIME
);
CREATE INDEX idx_memory_consolidation_runs_project_id ON memory_consolidation_runs(project_id);
CREATE INDEX idx_memory_consolidation_runs_started_at ON memory_consolidation_runs(started_at);

-- Seed: every existing project gets memory enabled. The real system task and
-- normal schedule row are created idempotently by MemoryService.EnsureProject
-- on server boot and project creation. Keeping task/schedule seeding out of the
-- migration avoids SQLite dump ordering problems because the baseline creates
-- schedules before tasks.
INSERT INTO project_memory_settings (project_id)
SELECT id FROM projects
WHERE id NOT IN (SELECT project_id FROM project_memory_settings);
