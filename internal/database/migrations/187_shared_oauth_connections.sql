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

-- Preserve existing standard OAuth configurations as private connections first.
-- Rows with the same provider and exact non-empty rotating refresh token are then
-- linked to one credential owner before refresh can rotate that shared token.
-- Provider account identities are intentionally never used for this decision.
INSERT INTO oauth_connections (
    id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
    oauth_account_id, oauth_needs_reauth, oauth_revision, created_at, updated_at
)
SELECT id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
       oauth_account_id, oauth_needs_reauth, oauth_config_revision, created_at, updated_at
FROM agent_configs
WHERE auth_method = 'oauth' AND provider IN ('openai', 'anthropic');

UPDATE agent_configs
SET oauth_connection_id = id
WHERE auth_method = 'oauth' AND provider IN ('openai', 'anthropic');

UPDATE agent_configs AS target
SET oauth_connection_id = (
    SELECT candidate.id
    FROM agent_configs candidate
    WHERE candidate.auth_method = 'oauth'
      AND candidate.provider = target.provider
      AND candidate.oauth_refresh_token = target.oauth_refresh_token
    ORDER BY candidate.oauth_needs_reauth ASC,
             candidate.oauth_expires_at DESC,
             candidate.oauth_config_revision DESC,
             candidate.updated_at DESC,
             candidate.id ASC
    LIMIT 1
)
WHERE target.auth_method = 'oauth'
  AND target.provider IN ('openai', 'anthropic')
  AND target.oauth_refresh_token != '';

UPDATE agent_configs
SET oauth_access_token = '', oauth_refresh_token = '', oauth_expires_at = 0,
    oauth_account_id = '', oauth_needs_reauth = 0
WHERE auth_method = 'oauth' AND provider IN ('openai', 'anthropic');

ALTER TABLE account_usage_snapshots ADD COLUMN oauth_connection_id TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL;
CREATE INDEX idx_account_usage_snapshots_connection_revision
ON account_usage_snapshots(oauth_connection_id, oauth_config_revision, fetched_at DESC, created_at DESC, id DESC);

-- Snapshot revisions are scoped to their original private connection. Translate only
-- the original owner's current generation to the selected canonical generation; all
-- other generations must fail closed so equal revision numbers cannot reactivate stale
-- snapshots after exact rotating credentials are consolidated.
UPDATE account_usage_snapshots AS snapshot
SET oauth_config_revision = CASE
        WHEN snapshot.oauth_config_revision = COALESCE(
            (SELECT original.oauth_revision
             FROM oauth_connections original
             WHERE original.id = snapshot.agent_config_id
               AND original.provider = snapshot.provider),
            -1
        )
        THEN COALESCE(
            (SELECT canonical.oauth_revision
             FROM agent_configs model
             JOIN oauth_connections canonical ON canonical.id = model.oauth_connection_id
             WHERE model.id = snapshot.agent_config_id
               AND model.provider = snapshot.provider
               AND canonical.provider = snapshot.provider),
            -1
        )
        ELSE -1
    END,
    oauth_connection_id = (
        SELECT model.oauth_connection_id
        FROM agent_configs model
        JOIN oauth_connections canonical ON canonical.id = model.oauth_connection_id
        WHERE model.id = snapshot.agent_config_id
          AND model.provider = snapshot.provider
          AND canonical.provider = snapshot.provider
    )
WHERE snapshot.oauth_config_revision >= 0;

DELETE FROM oauth_connections
WHERE NOT EXISTS (
    SELECT 1 FROM agent_configs WHERE agent_configs.oauth_connection_id = oauth_connections.id
);

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

-- Preserve snapshot ownership before removing the connection identifier. Legacy
-- no-identity snapshots used their originating model id as account_id, so clear
-- that pseudo-identity before selecting a current model for the same connection.
UPDATE account_usage_snapshots
SET account_id = NULL
WHERE oauth_connection_id IS NOT NULL
  AND account_id = agent_config_id;

UPDATE account_usage_snapshots
SET agent_config_id = (
    SELECT a.id
    FROM agent_configs a
    JOIN oauth_connections c ON c.id = a.oauth_connection_id
    WHERE c.id = account_usage_snapshots.oauth_connection_id
      AND c.provider = account_usage_snapshots.provider
      AND c.oauth_revision = account_usage_snapshots.oauth_config_revision
      AND a.provider = c.provider
      AND a.auth_method = 'oauth'
    ORDER BY a.id
    LIMIT 1
)
WHERE oauth_connection_id IS NOT NULL;

-- Snapshots with no current owner or a superseded connection generation must
-- fail closed under the version-186 model/revision matching rules.
UPDATE account_usage_snapshots
SET oauth_config_revision = -1
WHERE oauth_connection_id IS NOT NULL
  AND agent_config_id IS NULL;

DROP INDEX idx_account_usage_snapshots_connection_revision;
ALTER TABLE account_usage_snapshots DROP COLUMN oauth_connection_id;
DROP INDEX idx_agent_configs_oauth_connection;
ALTER TABLE agent_configs DROP COLUMN oauth_connection_id;
DROP TABLE oauth_connections;
