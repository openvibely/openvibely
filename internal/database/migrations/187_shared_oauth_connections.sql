-- +goose Up
CREATE TABLE oauth_connections (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    provider            TEXT NOT NULL CHECK (provider IN ('openai', 'anthropic')),
    name                TEXT NOT NULL DEFAULT '',
    oauth_access_token  TEXT NOT NULL DEFAULT '',
    oauth_refresh_token TEXT NOT NULL DEFAULT '',
    oauth_expires_at    INTEGER NOT NULL DEFAULT 0,
    oauth_account_id    TEXT NOT NULL DEFAULT '',
    oauth_needs_reauth  INTEGER NOT NULL DEFAULT 0 CHECK (oauth_needs_reauth IN (0, 1)),
    oauth_revision      INTEGER NOT NULL DEFAULT 0,
    refresh_lease_owner TEXT NOT NULL DEFAULT '',
    refresh_lease_until INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_oauth_connections_provider_name ON oauth_connections(provider, name COLLATE NOCASE, id);

ALTER TABLE agent_configs ADD COLUMN oauth_connection_id TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL;
CREATE INDEX idx_agent_configs_oauth_connection ON agent_configs(oauth_connection_id, id);

-- Preserve every existing standard OAuth configuration as its own connection.
-- Matching account identities are intentionally not merged.
INSERT INTO oauth_connections (
    id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
    oauth_account_id, oauth_needs_reauth, oauth_revision, created_at, updated_at
)
SELECT id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
       oauth_account_id, oauth_needs_reauth, oauth_config_revision, created_at, updated_at
FROM agent_configs
WHERE auth_method = 'oauth' AND provider IN ('openai', 'anthropic');

UPDATE agent_configs
SET oauth_connection_id = id,
    oauth_access_token = '', oauth_refresh_token = '', oauth_expires_at = 0,
    oauth_account_id = '', oauth_needs_reauth = 0
WHERE auth_method = 'oauth' AND provider IN ('openai', 'anthropic');

ALTER TABLE account_usage_snapshots ADD COLUMN oauth_connection_id TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL;
CREATE INDEX idx_account_usage_snapshots_connection_revision
ON account_usage_snapshots(oauth_connection_id, oauth_config_revision, fetched_at DESC, created_at DESC, id DESC);
UPDATE account_usage_snapshots
SET oauth_connection_id = (
    SELECT oauth_connection_id FROM agent_configs WHERE agent_configs.id = account_usage_snapshots.agent_config_id
)
WHERE oauth_config_revision >= 0;

-- +goose Down
-- Rehydrate the legacy per-model ownership before removing shared connections.
-- A shared connection is intentionally duplicated onto every linked model so a
-- rollback remains usable on schema version 186.
UPDATE agent_configs
SET oauth_access_token = COALESCE((SELECT c.oauth_access_token FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), ''),
    oauth_refresh_token = COALESCE((SELECT c.oauth_refresh_token FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), ''),
    oauth_expires_at = COALESCE((SELECT c.oauth_expires_at FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), 0),
    oauth_account_id = COALESCE((SELECT c.oauth_account_id FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), ''),
    oauth_needs_reauth = COALESCE((SELECT c.oauth_needs_reauth FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), 0),
    oauth_config_revision = COALESCE((SELECT c.oauth_revision FROM oauth_connections c WHERE c.id = agent_configs.oauth_connection_id AND c.provider = agent_configs.provider), oauth_config_revision)
WHERE auth_method = 'oauth'
  AND provider IN ('openai', 'anthropic')
  AND oauth_connection_id IS NOT NULL;

DROP INDEX idx_account_usage_snapshots_connection_revision;
ALTER TABLE account_usage_snapshots DROP COLUMN oauth_connection_id;
DROP INDEX idx_agent_configs_oauth_connection;
ALTER TABLE agent_configs DROP COLUMN oauth_connection_id;
DROP TABLE oauth_connections;
