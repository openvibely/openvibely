-- +goose Up
UPDATE agent_configs
SET auth_method = CASE
        WHEN COALESCE(oauth_access_token, '') != ''
          OR COALESCE(oauth_refresh_token, '') != ''
        THEN 'oauth'
        ELSE 'api_key'
    END,
    updated_at = CURRENT_TIMESTAMP
WHERE provider IN ('openai', 'anthropic')
  AND auth_method = 'cli';

-- +goose Down
-- CLI auth is retired. Downgrades keep the normalized OAuth/API-key auth method.
