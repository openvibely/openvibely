-- +goose Up
CREATE TABLE user_github_connections (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    pat TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL DEFAULT '',
    api_endpoint TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    author_email TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1
);

-- Preserve the existing owner's connection. Never infer a human commit email
-- from the server's Git config; migrated owners confirm their identity in the UI.
INSERT INTO user_github_connections (user_id, pat, login, api_endpoint)
SELECT u.id, p.value, COALESCE((SELECT value FROM app_settings WHERE key='github_pat_user_login'), ''), COALESCE((SELECT value FROM app_settings WHERE key='github_api_endpoint'), '')
FROM users u JOIN app_settings p ON p.key='github_pat'
WHERE u.id='local-owner' AND length(trim(p.value)) > 0;

-- +goose Down
DROP TABLE user_github_connections;
