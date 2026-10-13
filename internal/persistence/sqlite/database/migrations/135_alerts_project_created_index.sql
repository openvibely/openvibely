-- +goose Up
-- The Alerts page and alert repository list path filter alerts by project_id and
-- order every response by (created_at DESC, id DESC) before applying LIMIT/OFFSET.
-- The existing idx_alerts_project_id and idx_alerts_created_at indexes cover only
-- the project equality or the global creation order, and the wider lifecycle index
-- leads with decision/processing state, so none of them can satisfy the
-- project-scoped stable order. SQLite therefore filters by project and builds a
-- temporary B-tree to sort each page. Add a composite index whose leading column
-- is the project equality followed by the exact list order so the ordered scan is
-- served directly from the index.
CREATE INDEX IF NOT EXISTS idx_alerts_project_created
    ON alerts(project_id, created_at DESC, id DESC);

-- The new composite index leads with project_id, so it also satisfies every
-- project_id equality lookup that the narrower idx_alerts_project_id served.
-- Dropping the redundant single-column index removes its write and storage cost
-- without leaving any project-scoped alert query unindexed.
DROP INDEX IF EXISTS idx_alerts_project_id;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_alerts_project_id ON alerts(project_id);
DROP INDEX IF EXISTS idx_alerts_project_created;
