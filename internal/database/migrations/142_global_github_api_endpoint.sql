-- +goose Up
-- Global GitHub API endpoint is stored in app_settings under key 'github_api_endpoint'.
-- app_settings already exists (migration 068). No schema change required.
-- The per-project github_api_endpoint column was never released to production,
-- so no data migration or ALTER TABLE is needed.
SELECT 1;

-- +goose Down
SELECT 1;
