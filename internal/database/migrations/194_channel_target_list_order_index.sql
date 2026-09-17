-- +goose Up
CREATE INDEX IF NOT EXISTS idx_channel_targets_project_list_order
ON channel_targets(project_id, platform ASC, is_home DESC, name ASC, target_id ASC, target_kind, thread_id, default_subject);

-- +goose Down
DROP INDEX IF EXISTS idx_channel_targets_project_list_order;
