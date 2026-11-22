-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS update_agent_configs_timestamp;

CREATE TABLE oauth_connections_rebuild (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    provider            TEXT NOT NULL CHECK (provider IN ('openai', 'anthropic', 'gemini')),
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
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    oauth_provider_display_name TEXT NOT NULL DEFAULT '',
    oauth_principal_hash TEXT NOT NULL DEFAULT '',
    oauth_principal_verified INTEGER NOT NULL DEFAULT 0 CHECK (oauth_principal_verified IN (0, 1))
);
INSERT INTO oauth_connections_rebuild (id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, oauth_needs_reauth, oauth_revision, refresh_lease_owner, refresh_lease_until, created_at, updated_at, oauth_provider_display_name, oauth_principal_hash, oauth_principal_verified)
SELECT id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, oauth_needs_reauth, oauth_revision, refresh_lease_owner, refresh_lease_until, created_at, updated_at, oauth_provider_display_name, oauth_principal_hash, oauth_principal_verified FROM oauth_connections;
DROP TABLE oauth_connections;
ALTER TABLE oauth_connections_rebuild RENAME TO oauth_connections;
CREATE INDEX idx_oauth_connections_provider_name ON oauth_connections(provider, name COLLATE NOCASE, id);
CREATE INDEX idx_oauth_connections_provider_verified_principal
ON oauth_connections(provider, oauth_principal_hash, id)
WHERE oauth_principal_verified = 1 AND oauth_principal_hash != '';

CREATE TABLE agent_configs_rebuild (
    id                       TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name                     TEXT NOT NULL,
    provider                 TEXT NOT NULL DEFAULT 'anthropic'
                             CHECK (provider IN ('anthropic', 'openai', 'openai_compatible', 'ollama', 'test', 'mixture', 'gemini')),
    model                    TEXT NOT NULL DEFAULT 'claude-sonnet-4-5-20250929',
    api_key                  TEXT NOT NULL DEFAULT '',
    max_tokens               INTEGER NOT NULL DEFAULT 4096,
    temperature              REAL NOT NULL DEFAULT 0.0,
    is_default               INTEGER NOT NULL DEFAULT 0,
    created_at               DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at               DATETIME NOT NULL DEFAULT (datetime('now')),
    auth_method              TEXT NOT NULL DEFAULT 'cli'
                             CHECK (auth_method IN ('cli', 'oauth', 'api_key')),
    oauth_access_token       TEXT NOT NULL DEFAULT '',
    oauth_refresh_token      TEXT NOT NULL DEFAULT '',
    oauth_expires_at         INTEGER NOT NULL DEFAULT 0,
    reasoning_effort         TEXT NOT NULL DEFAULT ''
                             CHECK (reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh', 'max')),
    max_workers              INTEGER NOT NULL DEFAULT 0,
    worker_timeout           INTEGER NOT NULL DEFAULT 0,
    oauth_client_id          TEXT NOT NULL DEFAULT '',
    oauth_client_secret      TEXT NOT NULL DEFAULT '',
    oauth_authorize_url      TEXT NOT NULL DEFAULT '',
    oauth_token_url          TEXT NOT NULL DEFAULT '',
    oauth_scopes             TEXT NOT NULL DEFAULT '',
    ollama_base_url          TEXT NOT NULL DEFAULT '',
    oauth_account_id         TEXT NOT NULL DEFAULT '',
    auto_start_tasks         INTEGER NOT NULL DEFAULT 0,
    base_url                 TEXT NOT NULL DEFAULT '',
    transport                TEXT NOT NULL DEFAULT '',
    preset_slug              TEXT NOT NULL DEFAULT '',
    models_url               TEXT NOT NULL DEFAULT '',
    auth_header_name         TEXT NOT NULL DEFAULT '',
    auth_header_value_prefix TEXT NOT NULL DEFAULT '',
    extra_headers_json       TEXT NOT NULL DEFAULT '',
    extra_body_json          TEXT NOT NULL DEFAULT '',
    default_max_tokens       INTEGER NOT NULL DEFAULT 0,
    token_exchange_format    TEXT NOT NULL DEFAULT '',
    token_refresh_format     TEXT NOT NULL DEFAULT '',
    mixture_config_json      TEXT NOT NULL DEFAULT '',
    custom_auth_config_json  TEXT NOT NULL DEFAULT '',
    custom_auth_state_json   TEXT NOT NULL DEFAULT '',
    oauth_config_revision    INTEGER NOT NULL DEFAULT 0,
    context_window           INTEGER NOT NULL DEFAULT 0,
    compaction_threshold     INTEGER NOT NULL DEFAULT 0,
    oauth_needs_reauth       INTEGER NOT NULL DEFAULT 0 CHECK (oauth_needs_reauth IN (0, 1)),
    oauth_connection_id      TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL,
    provider_context_window  INTEGER NOT NULL DEFAULT 0,
    provider_max_output_tokens INTEGER NOT NULL DEFAULT 0,
    service_tier             TEXT NOT NULL DEFAULT ''
);
INSERT INTO agent_configs_rebuild (id, name, provider, model, api_key, max_tokens, temperature, is_default, created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, reasoning_effort, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, mixture_config_json, custom_auth_config_json, custom_auth_state_json, oauth_config_revision, context_window, compaction_threshold, oauth_needs_reauth, oauth_connection_id, provider_context_window, provider_max_output_tokens, service_tier)
SELECT id, name, provider, model, api_key, max_tokens, temperature, is_default, created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, reasoning_effort, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, mixture_config_json, custom_auth_config_json, custom_auth_state_json, oauth_config_revision, context_window, compaction_threshold, oauth_needs_reauth, oauth_connection_id, provider_context_window, provider_max_output_tokens, service_tier FROM agent_configs;
DROP TABLE agent_configs;
ALTER TABLE agent_configs_rebuild RENAME TO agent_configs;
CREATE INDEX idx_agent_configs_oauth_connection ON agent_configs(oauth_connection_id, id);

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
-- +goose NO TRANSACTION

PRAGMA foreign_keys=OFF;

DROP TRIGGER IF EXISTS update_agent_configs_timestamp;

CREATE TABLE oauth_connections_rebuild (
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
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    oauth_provider_display_name TEXT NOT NULL DEFAULT '',
    oauth_principal_hash TEXT NOT NULL DEFAULT '',
    oauth_principal_verified INTEGER NOT NULL DEFAULT 0 CHECK (oauth_principal_verified IN (0, 1))
);
INSERT INTO oauth_connections_rebuild (id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, oauth_needs_reauth, oauth_revision, refresh_lease_owner, refresh_lease_until, created_at, updated_at, oauth_provider_display_name, oauth_principal_hash, oauth_principal_verified)
SELECT id, provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, oauth_needs_reauth, oauth_revision, refresh_lease_owner, refresh_lease_until, created_at, updated_at, oauth_provider_display_name, oauth_principal_hash, oauth_principal_verified FROM oauth_connections WHERE provider != 'gemini';
DROP TABLE oauth_connections;
ALTER TABLE oauth_connections_rebuild RENAME TO oauth_connections;
CREATE INDEX idx_oauth_connections_provider_name ON oauth_connections(provider, name COLLATE NOCASE, id);
CREATE INDEX idx_oauth_connections_provider_verified_principal
ON oauth_connections(provider, oauth_principal_hash, id)
WHERE oauth_principal_verified = 1 AND oauth_principal_hash != '';

CREATE TABLE agent_configs_rebuild (
    id                       TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name                     TEXT NOT NULL,
    provider                 TEXT NOT NULL DEFAULT 'anthropic'
                             CHECK (provider IN ('anthropic', 'openai', 'openai_compatible', 'ollama', 'test', 'mixture')),
    model                    TEXT NOT NULL DEFAULT 'claude-sonnet-4-5-20250929',
    api_key                  TEXT NOT NULL DEFAULT '',
    max_tokens               INTEGER NOT NULL DEFAULT 4096,
    temperature              REAL NOT NULL DEFAULT 0.0,
    is_default               INTEGER NOT NULL DEFAULT 0,
    created_at               DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at               DATETIME NOT NULL DEFAULT (datetime('now')),
    auth_method              TEXT NOT NULL DEFAULT 'cli'
                             CHECK (auth_method IN ('cli', 'oauth', 'api_key')),
    oauth_access_token       TEXT NOT NULL DEFAULT '',
    oauth_refresh_token      TEXT NOT NULL DEFAULT '',
    oauth_expires_at         INTEGER NOT NULL DEFAULT 0,
    reasoning_effort         TEXT NOT NULL DEFAULT ''
                             CHECK (reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh', 'max')),
    max_workers              INTEGER NOT NULL DEFAULT 0,
    worker_timeout           INTEGER NOT NULL DEFAULT 0,
    oauth_client_id          TEXT NOT NULL DEFAULT '',
    oauth_client_secret      TEXT NOT NULL DEFAULT '',
    oauth_authorize_url      TEXT NOT NULL DEFAULT '',
    oauth_token_url          TEXT NOT NULL DEFAULT '',
    oauth_scopes             TEXT NOT NULL DEFAULT '',
    ollama_base_url          TEXT NOT NULL DEFAULT '',
    oauth_account_id         TEXT NOT NULL DEFAULT '',
    auto_start_tasks         INTEGER NOT NULL DEFAULT 0,
    base_url                 TEXT NOT NULL DEFAULT '',
    transport                TEXT NOT NULL DEFAULT '',
    preset_slug              TEXT NOT NULL DEFAULT '',
    models_url               TEXT NOT NULL DEFAULT '',
    auth_header_name         TEXT NOT NULL DEFAULT '',
    auth_header_value_prefix TEXT NOT NULL DEFAULT '',
    extra_headers_json       TEXT NOT NULL DEFAULT '',
    extra_body_json          TEXT NOT NULL DEFAULT '',
    default_max_tokens       INTEGER NOT NULL DEFAULT 0,
    token_exchange_format    TEXT NOT NULL DEFAULT '',
    token_refresh_format     TEXT NOT NULL DEFAULT '',
    mixture_config_json      TEXT NOT NULL DEFAULT '',
    custom_auth_config_json  TEXT NOT NULL DEFAULT '',
    custom_auth_state_json   TEXT NOT NULL DEFAULT '',
    oauth_config_revision    INTEGER NOT NULL DEFAULT 0,
    context_window           INTEGER NOT NULL DEFAULT 0,
    compaction_threshold     INTEGER NOT NULL DEFAULT 0,
    oauth_needs_reauth       INTEGER NOT NULL DEFAULT 0 CHECK (oauth_needs_reauth IN (0, 1)),
    oauth_connection_id      TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL,
    provider_context_window  INTEGER NOT NULL DEFAULT 0,
    provider_max_output_tokens INTEGER NOT NULL DEFAULT 0,
    service_tier             TEXT NOT NULL DEFAULT ''
);
INSERT INTO agent_configs_rebuild (id, name, provider, model, api_key, max_tokens, temperature, is_default, created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, reasoning_effort, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, mixture_config_json, custom_auth_config_json, custom_auth_state_json, oauth_config_revision, context_window, compaction_threshold, oauth_needs_reauth, oauth_connection_id, provider_context_window, provider_max_output_tokens, service_tier)
SELECT id, name, CASE WHEN provider = 'gemini' THEN 'openai_compatible' ELSE provider END, model, api_key, max_tokens, temperature, is_default, created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, reasoning_effort, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, oauth_account_id, auto_start_tasks, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, mixture_config_json, custom_auth_config_json, custom_auth_state_json, oauth_config_revision, context_window, compaction_threshold, oauth_needs_reauth, CASE WHEN provider = 'gemini' THEN NULL ELSE oauth_connection_id END, provider_context_window, provider_max_output_tokens, service_tier FROM agent_configs;
DROP TABLE agent_configs;
ALTER TABLE agent_configs_rebuild RENAME TO agent_configs;
CREATE INDEX idx_agent_configs_oauth_connection ON agent_configs(oauth_connection_id, id);

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
