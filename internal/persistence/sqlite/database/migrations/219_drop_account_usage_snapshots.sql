-- +goose Up
-- Preserve current display labels before removing the legacy snapshot fallback.
UPDATE oauth_connections AS c
SET oauth_provider_display_name = COALESCE((
    SELECT NULLIF(TRIM(s.account_display_name), '')
    FROM account_usage_snapshots s
    WHERE s.oauth_connection_id = c.id
      AND s.oauth_config_revision = c.oauth_revision
      AND TRIM(s.account_display_name) != ''
    ORDER BY s.fetched_at DESC, s.created_at DESC, s.rowid DESC
    LIMIT 1
), '')
WHERE TRIM(c.oauth_provider_display_name) = '';

-- Live provider responses are no longer persisted. Usage events, OAuth
-- credentials, and the monitor's settings-backed alert state remain intact.
DROP TABLE IF EXISTS account_usage_extra_limits;
DROP TABLE IF EXISTS account_usage_snapshots;

-- +goose Down
-- Recreate empty cache tables for older versions; historical cache data is not restored.
CREATE TABLE account_usage_snapshots (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  account_id TEXT,
  agent_config_id TEXT,
  plan_type TEXT NOT NULL DEFAULT '',
  credits_remaining REAL,
  primary_label TEXT NOT NULL DEFAULT '',
  primary_used_percent REAL,
  primary_window_minutes INTEGER,
  primary_resets_at TEXT,
  secondary_label TEXT NOT NULL DEFAULT '',
  secondary_used_percent REAL,
  secondary_window_minutes INTEGER,
  secondary_resets_at TEXT,
  model_limit_label TEXT NOT NULL DEFAULT '',
  model_limit_used_percent REAL,
  model_limit_window_minutes INTEGER,
  model_limit_resets_at TEXT,
  rate_limit_reached_type TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL DEFAULT '{}',
  fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  account_display_name TEXT NOT NULL DEFAULT '',
  account_detail TEXT NOT NULL DEFAULT '',
  billing_label TEXT NOT NULL DEFAULT '',
  subscription_status TEXT NOT NULL DEFAULT '',
  extra_usage_label TEXT NOT NULL DEFAULT '',
  extra_usage_monthly_limit_usd REAL,
  extra_usage_used_usd REAL,
  oauth_config_revision INTEGER NOT NULL DEFAULT -1,
  oauth_connection_id TEXT REFERENCES oauth_connections(id) ON DELETE SET NULL,
  FOREIGN KEY(agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
);

CREATE TABLE account_usage_extra_limits (
  id TEXT PRIMARY KEY,
  snapshot_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  account_id TEXT,
  agent_config_id TEXT,
  limit_key TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  used_percent REAL,
  window_minutes INTEGER,
  reset_at TEXT,
  raw_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY(snapshot_id) REFERENCES account_usage_snapshots(id) ON DELETE CASCADE,
  FOREIGN KEY(agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
);

CREATE INDEX idx_account_usage_extra_limits_account ON account_usage_extra_limits(provider, account_id, limit_key);

CREATE INDEX idx_account_usage_extra_limits_snapshot ON account_usage_extra_limits(snapshot_id);

CREATE UNIQUE INDEX idx_account_usage_extra_limits_snapshot_key ON account_usage_extra_limits(snapshot_id, limit_key);

CREATE INDEX idx_account_usage_snapshots_agent_time ON account_usage_snapshots(agent_config_id, fetched_at);

CREATE INDEX idx_account_usage_snapshots_connection_revision
ON account_usage_snapshots(oauth_connection_id, oauth_config_revision, fetched_at DESC, created_at DESC, id DESC);

CREATE INDEX idx_account_usage_snapshots_provider_time ON account_usage_snapshots(provider, fetched_at);
