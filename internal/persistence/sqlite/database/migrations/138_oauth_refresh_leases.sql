-- +goose Up
CREATE TABLE oauth_refresh_leases (
    config_id TEXT PRIMARY KEY REFERENCES agent_configs(id) ON DELETE CASCADE,
    owner_token TEXT NOT NULL,
    lease_expires_at INTEGER NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_oauth_refresh_leases_expiry
    ON oauth_refresh_leases(lease_expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_oauth_refresh_leases_expiry;
DROP TABLE IF EXISTS oauth_refresh_leases;
