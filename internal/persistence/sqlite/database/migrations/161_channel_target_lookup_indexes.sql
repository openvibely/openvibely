-- +goose Up
CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform_home_updated
ON channel_targets(project_id, platform, is_home, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform_name
ON channel_targets(project_id, platform, name);

CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform_target_thread
ON channel_targets(project_id, platform, target_id, thread_id);

-- +goose Down
DROP INDEX IF EXISTS idx_channel_targets_project_platform_target_thread;
DROP INDEX IF EXISTS idx_channel_targets_project_platform_name;
DROP INDEX IF EXISTS idx_channel_targets_project_platform_home_updated;
