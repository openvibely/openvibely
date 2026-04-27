-- +goose NO TRANSACTION

-- +goose Up
-- Widen agent_configs.reasoning_effort to allow Claude Code's `max` effort.
-- SQLite cannot alter CHECK constraints in place, so rebuild the table while
-- preserving existing rows and indexes/triggers.

PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS update_agent_configs_timestamp;

CREATE TABLE agent_configs_new (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name                TEXT NOT NULL,
    provider            TEXT NOT NULL DEFAULT 'anthropic'
                        CHECK (provider IN ('anthropic', 'openai', 'ollama', 'test')),
    model               TEXT NOT NULL DEFAULT 'claude-sonnet-4-5-20250929',
    api_key             TEXT NOT NULL DEFAULT '',
    max_tokens          INTEGER NOT NULL DEFAULT 4096,
    temperature         REAL NOT NULL DEFAULT 0.0,
    is_default          INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    auth_method         TEXT NOT NULL DEFAULT 'cli'
                        CHECK (auth_method IN ('cli', 'oauth', 'api_key')),
    oauth_access_token  TEXT NOT NULL DEFAULT '',
    oauth_refresh_token TEXT NOT NULL DEFAULT '',
    oauth_expires_at    INTEGER NOT NULL DEFAULT 0,
    reasoning_effort    TEXT NOT NULL DEFAULT ''
                        CHECK (reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh', 'max')),
    max_workers         INTEGER NOT NULL DEFAULT 0,
    worker_timeout      INTEGER NOT NULL DEFAULT 0,
    oauth_client_id     TEXT NOT NULL DEFAULT '',
    oauth_client_secret TEXT NOT NULL DEFAULT '',
    oauth_authorize_url TEXT NOT NULL DEFAULT '',
    oauth_token_url     TEXT NOT NULL DEFAULT '',
    oauth_scopes        TEXT NOT NULL DEFAULT '',
    ollama_base_url     TEXT NOT NULL DEFAULT '',
    oauth_account_id    TEXT NOT NULL DEFAULT '',
    auto_start_tasks    INTEGER NOT NULL DEFAULT 0
);

INSERT INTO agent_configs_new (
    id, name, provider, model, api_key, max_tokens, temperature, is_default,
    created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token,
    oauth_expires_at, reasoning_effort, max_workers, worker_timeout,
    oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url,
    oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks
)
SELECT
    id, name, provider, model, api_key, max_tokens, temperature, is_default,
    created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token,
    oauth_expires_at, reasoning_effort, max_workers, worker_timeout,
    oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url,
    oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks
FROM agent_configs;

DROP TABLE agent_configs;
ALTER TABLE agent_configs_new RENAME TO agent_configs;

-- +goose StatementBegin
CREATE TRIGGER update_agent_configs_timestamp
AFTER UPDATE ON agent_configs
FOR EACH ROW
BEGIN
    UPDATE agent_configs SET updated_at = datetime('now') WHERE id = OLD.id;
END;
-- +goose StatementEnd

PRAGMA foreign_key_check;
PRAGMA foreign_keys=ON;

-- +goose Down
PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS update_agent_configs_timestamp;

CREATE TABLE agent_configs_old (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name                TEXT NOT NULL,
    provider            TEXT NOT NULL DEFAULT 'anthropic'
                        CHECK (provider IN ('anthropic', 'openai', 'ollama', 'test')),
    model               TEXT NOT NULL DEFAULT 'claude-sonnet-4-5-20250929',
    api_key             TEXT NOT NULL DEFAULT '',
    max_tokens          INTEGER NOT NULL DEFAULT 4096,
    temperature         REAL NOT NULL DEFAULT 0.0,
    is_default          INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    auth_method         TEXT NOT NULL DEFAULT 'cli'
                        CHECK (auth_method IN ('cli', 'oauth', 'api_key')),
    oauth_access_token  TEXT NOT NULL DEFAULT '',
    oauth_refresh_token TEXT NOT NULL DEFAULT '',
    oauth_expires_at    INTEGER NOT NULL DEFAULT 0,
    reasoning_effort    TEXT NOT NULL DEFAULT ''
                        CHECK (reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh')),
    max_workers         INTEGER NOT NULL DEFAULT 0,
    worker_timeout      INTEGER NOT NULL DEFAULT 0,
    oauth_client_id     TEXT NOT NULL DEFAULT '',
    oauth_client_secret TEXT NOT NULL DEFAULT '',
    oauth_authorize_url TEXT NOT NULL DEFAULT '',
    oauth_token_url     TEXT NOT NULL DEFAULT '',
    oauth_scopes        TEXT NOT NULL DEFAULT '',
    ollama_base_url     TEXT NOT NULL DEFAULT '',
    oauth_account_id    TEXT NOT NULL DEFAULT '',
    auto_start_tasks    INTEGER NOT NULL DEFAULT 0
);

INSERT INTO agent_configs_old (
    id, name, provider, model, api_key, max_tokens, temperature, is_default,
    created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token,
    oauth_expires_at, reasoning_effort, max_workers, worker_timeout,
    oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url,
    oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks
)
SELECT
    id, name, provider, model, api_key, max_tokens, temperature, is_default,
    created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token,
    oauth_expires_at,
    CASE WHEN reasoning_effort = 'max' THEN '' ELSE reasoning_effort END,
    max_workers, worker_timeout, oauth_client_id, oauth_client_secret,
    oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url,
    oauth_account_id, auto_start_tasks
FROM agent_configs;

DROP TABLE agent_configs;
ALTER TABLE agent_configs_old RENAME TO agent_configs;

-- +goose StatementBegin
CREATE TRIGGER update_agent_configs_timestamp
AFTER UPDATE ON agent_configs
FOR EACH ROW
BEGIN
    UPDATE agent_configs SET updated_at = datetime('now') WHERE id = OLD.id;
END;
-- +goose StatementEnd

PRAGMA foreign_key_check;
PRAGMA foreign_keys=ON;
