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

-- Preserve legacy PAT precedence when App settings also exist but no explicit
-- mode was saved. The old resolver chose a configured PAT first.
INSERT INTO app_settings(key,value)
SELECT 'github_auth_mode','pat' WHERE EXISTS(SELECT 1 FROM user_github_connections WHERE user_id='local-owner' AND pat<>'')
ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE trim(app_settings.value)='';

DELETE FROM app_settings WHERE key IN ('github_pat', 'github_pat_user_login');

-- +goose Down
INSERT INTO app_settings(key,value) SELECT 'github_pat',pat FROM user_github_connections WHERE user_id='local-owner' ON CONFLICT(key) DO UPDATE SET value=excluded.value;
INSERT INTO app_settings(key,value) SELECT 'github_pat_user_login',login FROM user_github_connections WHERE user_id='local-owner' ON CONFLICT(key) DO UPDATE SET value=excluded.value;
DROP TABLE user_github_connections;
