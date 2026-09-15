-- +goose Up
-- Repair databases that applied migration 187 before exact rotating credentials
-- were consolidated. Account identity is deliberately not part of this mapping.
CREATE TEMP TABLE oauth_connection_consolidation (
    old_id       TEXT PRIMARY KEY,
    canonical_id TEXT NOT NULL
);

INSERT INTO oauth_connection_consolidation (old_id, canonical_id)
SELECT connection.id,
       (
           SELECT candidate.id
           FROM oauth_connections candidate
           WHERE candidate.provider = connection.provider
             AND candidate.oauth_refresh_token = connection.oauth_refresh_token
           ORDER BY candidate.oauth_needs_reauth ASC,
                    candidate.oauth_expires_at DESC,
                    candidate.oauth_revision DESC,
                    candidate.updated_at DESC,
                    candidate.id ASC
           LIMIT 1
       )
FROM oauth_connections connection
WHERE connection.oauth_refresh_token != ''
  AND connection.id != (
      SELECT candidate.id
      FROM oauth_connections candidate
      WHERE candidate.provider = connection.provider
        AND candidate.oauth_refresh_token = connection.oauth_refresh_token
      ORDER BY candidate.oauth_needs_reauth ASC,
               candidate.oauth_expires_at DESC,
               candidate.oauth_revision DESC,
               candidate.updated_at DESC,
               candidate.id ASC
      LIMIT 1
  );

-- Preserve current-generation snapshot eligibility on the canonical owner. Revision
-- numbers are connection-local, so every non-current generation must use the
-- fail-closed sentinel rather than retaining a number that could collide with the
-- canonical owner's current revision.
UPDATE account_usage_snapshots AS snapshot
SET oauth_config_revision = CASE
        WHEN snapshot.oauth_config_revision = (
            SELECT old_connection.oauth_revision
            FROM oauth_connection_consolidation mapping
            JOIN oauth_connections old_connection ON old_connection.id = mapping.old_id
            WHERE mapping.old_id = snapshot.oauth_connection_id
        ) THEN (
            SELECT canonical.oauth_revision
            FROM oauth_connection_consolidation mapping
            JOIN oauth_connections canonical ON canonical.id = mapping.canonical_id
            WHERE mapping.old_id = snapshot.oauth_connection_id
        )
        ELSE -1
    END,
    oauth_connection_id = (
        SELECT mapping.canonical_id
        FROM oauth_connection_consolidation mapping
        WHERE mapping.old_id = snapshot.oauth_connection_id
    )
WHERE EXISTS (
    SELECT 1 FROM oauth_connection_consolidation mapping
    WHERE mapping.old_id = snapshot.oauth_connection_id
);

UPDATE agent_configs AS config
SET oauth_connection_id = (
    SELECT mapping.canonical_id
    FROM oauth_connection_consolidation mapping
    WHERE mapping.old_id = config.oauth_connection_id
)
WHERE EXISTS (
    SELECT 1 FROM oauth_connection_consolidation mapping
    WHERE mapping.old_id = config.oauth_connection_id
);

DELETE FROM oauth_connections
WHERE id IN (SELECT old_id FROM oauth_connection_consolidation);

DROP TABLE oauth_connection_consolidation;

-- +goose Down
-- Credential-owner consolidation cannot be reversed without recreating unsafe
-- duplicate rotating refresh tokens.
SELECT 1;
