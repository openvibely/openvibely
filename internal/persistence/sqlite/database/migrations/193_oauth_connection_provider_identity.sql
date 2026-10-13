-- +goose Up
ALTER TABLE oauth_connections ADD COLUMN oauth_provider_display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_connections ADD COLUMN oauth_principal_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_oauth_connections_provider_principal
ON oauth_connections(provider, oauth_principal_hash, id)
WHERE oauth_principal_hash != '';

-- Preserve an already-resolved safe provider label for existing installations.
-- Strong principal evidence is intentionally not inferred from account IDs or raw
-- Analytics payloads; it is populated only by a live provider profile response.
UPDATE oauth_connections AS connection
SET oauth_provider_display_name = COALESCE(
    (SELECT TRIM(snapshot.account_display_name)
     FROM account_usage_snapshots snapshot
     WHERE snapshot.oauth_connection_id = connection.id
       AND snapshot.oauth_config_revision = connection.oauth_revision
       AND TRIM(snapshot.account_display_name) != ''
     ORDER BY snapshot.fetched_at DESC, snapshot.created_at DESC, snapshot.rowid DESC
     LIMIT 1),
    ''
);

-- +goose Down
DROP INDEX idx_oauth_connections_provider_principal;
ALTER TABLE oauth_connections DROP COLUMN oauth_principal_hash;
ALTER TABLE oauth_connections DROP COLUMN oauth_provider_display_name;
