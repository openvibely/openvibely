-- +goose Up
-- Order-covering index for the shared project selector projection.
-- Matches ORDER BY is_default DESC, name ASC, id ASC so the sidebar selector
-- and current-project fallback avoid USE TEMP B-TREE FOR ORDER BY. Including
-- name and id makes the index cover SELECT id, name, is_default without a
-- table lookup.
CREATE INDEX IF NOT EXISTS idx_projects_selector_order
ON projects(is_default DESC, name ASC, id ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_projects_selector_order;
