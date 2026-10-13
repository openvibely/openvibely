-- +goose Up
DROP TABLE IF EXISTS conflict_history;
DROP TABLE IF EXISTS conflict_predictions;
DROP TABLE IF EXISTS execution_order_recommendations;
DROP TABLE IF EXISTS impact_analyses;

-- +goose Down
CREATE TABLE impact_analyses (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    files_impacted TEXT NOT NULL DEFAULT '[]',
    apis_impacted TEXT NOT NULL DEFAULT '[]',
    schemas_impacted TEXT NOT NULL DEFAULT '[]',
    components_impacted TEXT NOT NULL DEFAULT '[]',
    impact_summary TEXT NOT NULL DEFAULT '',
    confidence REAL NOT NULL DEFAULT 0.5,
    analysis_model TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_impact_analyses_task_id ON impact_analyses(task_id);
CREATE INDEX idx_impact_analyses_project_id ON impact_analyses(project_id);

CREATE TABLE conflict_predictions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_a_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_b_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    conflict_type TEXT NOT NULL CHECK(conflict_type IN ('file', 'api', 'schema', 'component', 'semantic')),
    severity TEXT NOT NULL CHECK(severity IN ('low', 'medium', 'high', 'critical')),
    description TEXT NOT NULL DEFAULT '',
    overlapping_resources TEXT NOT NULL DEFAULT '[]',
    resolution_strategy TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'detected' CHECK(status IN ('detected', 'acknowledged', 'resolved', 'false_positive')),
    resolved_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_conflict_predictions_project_id ON conflict_predictions(project_id);
CREATE INDEX idx_conflict_predictions_task_a ON conflict_predictions(task_a_id);
CREATE INDEX idx_conflict_predictions_task_b ON conflict_predictions(task_b_id);
CREATE INDEX idx_conflict_predictions_status ON conflict_predictions(status);

CREATE TABLE conflict_history (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_a_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_b_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    prediction_id TEXT REFERENCES conflict_predictions(id) ON DELETE SET NULL,
    was_predicted INTEGER NOT NULL DEFAULT 0,
    conflict_type TEXT NOT NULL,
    actual_files TEXT NOT NULL DEFAULT '[]',
    resolution TEXT NOT NULL DEFAULT '',
    impact_score REAL NOT NULL DEFAULT 0.0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_conflict_history_project_id ON conflict_history(project_id);
CREATE INDEX idx_conflict_history_prediction_id ON conflict_history(prediction_id);
CREATE INDEX idx_conflict_history_task_a ON conflict_history(task_a_id);
CREATE INDEX idx_conflict_history_task_b ON conflict_history(task_b_id);

CREATE TABLE execution_order_recommendations (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_ids TEXT NOT NULL DEFAULT '[]',
    reasoning TEXT NOT NULL DEFAULT '',
    conflict_count INTEGER NOT NULL DEFAULT 0,
    batch_groups TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'accepted', 'rejected', 'expired')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL DEFAULT (datetime('now', '+1 hour'))
);
CREATE INDEX idx_execution_order_project_id ON execution_order_recommendations(project_id);
CREATE INDEX idx_execution_order_status ON execution_order_recommendations(status);
