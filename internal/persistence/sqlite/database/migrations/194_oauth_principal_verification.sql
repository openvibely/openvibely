-- +goose Up
ALTER TABLE oauth_connections ADD COLUMN oauth_principal_verified INTEGER NOT NULL DEFAULT 0 CHECK (oauth_principal_verified IN (0, 1));

-- Existing principal hashes have no durable proof that they were produced by a
-- signature-verified token. Keep them for display/debug continuity, but do not
-- allow them to drive account consolidation until the provider is verified again.
DROP INDEX idx_oauth_connections_provider_principal;
CREATE INDEX idx_oauth_connections_provider_verified_principal
ON oauth_connections(provider, oauth_principal_hash, id)
WHERE oauth_principal_verified = 1 AND oauth_principal_hash != '';

-- +goose Down
DROP INDEX idx_oauth_connections_provider_verified_principal;
CREATE INDEX idx_oauth_connections_provider_principal
ON oauth_connections(provider, oauth_principal_hash, id)
WHERE oauth_principal_hash != '';
ALTER TABLE oauth_connections DROP COLUMN oauth_principal_verified;
