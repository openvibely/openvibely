-- +goose Up
-- Retire grading storage, including all saved grades and table indexes.
DROP TABLE health_checks;
DROP TABLE idea_grades;

-- +goose Down
-- Restore the schema only. Deleted grades cannot be recovered by rollback.
CREATE TABLE health_checks (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grade TEXT NOT NULL DEFAULT '',
    strengths TEXT NOT NULL DEFAULT '',
    improvements TEXT NOT NULL DEFAULT '',
    assessment TEXT NOT NULL DEFAULT '',
    how_to_improve TEXT NOT NULL DEFAULT '',
    tasks_total INTEGER NOT NULL DEFAULT 0,
    tasks_completed INTEGER NOT NULL DEFAULT 0,
    tasks_failed INTEGER NOT NULL DEFAULT 0,
    tasks_pending INTEGER NOT NULL DEFAULT 0,
    backlog_size INTEGER NOT NULL DEFAULT 0,
    avg_completion_pct REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_health_checks_project_id ON health_checks(project_id);
CREATE INDEX idx_health_checks_created_at ON health_checks(project_id, created_at);

CREATE TABLE idea_grades (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grade TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    strengths TEXT NOT NULL DEFAULT '',
    improvements TEXT NOT NULL DEFAULT '',
    how_to_next_grade TEXT NOT NULL DEFAULT '',
    next_grade TEXT NOT NULL DEFAULT '',
    tasks_evaluated INTEGER NOT NULL DEFAULT 0,
    clarity_score REAL NOT NULL DEFAULT 0,
    ambition_score REAL NOT NULL DEFAULT 0,
    follow_through REAL NOT NULL DEFAULT 0,
    diversity_score REAL NOT NULL DEFAULT 0,
    strategy_score REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_idea_grades_project_id ON idea_grades(project_id);
CREATE INDEX idx_idea_grades_created_at ON idea_grades(project_id, created_at);
