package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

type SettingsRepo struct {
	db                    *sql.DB
	queryObserver         func(string)
	queryAcquiredObserver func(string)
}

func NewSettingsRepo(db *sql.DB) *SettingsRepo {
	return &SettingsRepo{db: db}
}

// Get retrieves a setting value by key. Returns empty string if not found.
func (r *SettingsRepo) Get(ctx context.Context, key string) (string, error) {
	const query = "SELECT value FROM app_settings WHERE key = ?"
	r.observeQuery(query)
	rows, err := r.db.QueryContext(ctx, query, key)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	r.observeQueryAcquired(query)
	if !rows.Next() {
		return "", rows.Err()
	}
	var value string
	if err := rows.Scan(&value); err != nil {
		return "", err
	}
	return value, rows.Err()
}

// GetMany retrieves a coherent snapshot of the requested settings in one query.
// Missing keys are omitted from the returned map.
func (r *SettingsRepo) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return values, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	query := "SELECT key, value FROM app_settings WHERE key IN (" + placeholders + ")"
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	r.observeQuery(query)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	r.observeQueryAcquired(query)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

// SetQueryObserver installs statement instrumentation used by tests and benchmarks.
func (r *SettingsRepo) SetQueryObserver(observer func(string)) {
	r.queryObserver = observer
}

// SetQueryAcquiredObserver installs test instrumentation that runs after a query
// acquires a database connection and before its rows are consumed.
func (r *SettingsRepo) SetQueryAcquiredObserver(observer func(string)) {
	r.queryAcquiredObserver = observer
}

func (r *SettingsRepo) observeQuery(query string) {
	if r.queryObserver != nil {
		r.queryObserver(query)
	}
}

func (r *SettingsRepo) observeQueryAcquired(query string) {
	if r.queryAcquiredObserver != nil {
		r.queryAcquiredObserver(query)
	}
}

// SetMany atomically replaces a coherent settings snapshot.
func (r *SettingsRepo) SetMany(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		keys := channelCredentialKeys(values)
		if identified && len(keys) > 0 {
			if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
				return err
			}
		}
		for key, value := range values {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
				key, value); err != nil {
				return err
			}
		}
		if identified && len(keys) > 0 {
			details, _ := json.Marshal(keys)
			return projectAudit(ctx, tx, "default", actor.UserID, "channel.credentials_changed", "instance", string(details))
		}
		return nil
	})
}

// RemoveChannelsBulk atomically resets provider settings and removes project-owned
// webhooks. Discord authorization is system-wide and is cleared only when the
// Discord provider is part of the same removal request.
func (r *SettingsRepo) RemoveChannelsBulk(ctx context.Context, projectID string, values map[string]string, webhookIDs []string, removeDiscordAuthorization bool) error {
	return r.removeChannels(ctx, projectID, values, webhookIDs, removeDiscordAuthorization)
}

func (r *SettingsRepo) RemoveDiscordChannel(ctx context.Context, projectID string, values map[string]string) error {
	return r.removeChannels(ctx, projectID, values, nil, true)
}

func (r *SettingsRepo) removeChannels(ctx context.Context, projectID string, values map[string]string, webhookIDs []string, removeDiscordAuthorization bool) error {
	if len(values) == 0 && len(webhookIDs) == 0 {
		return fmt.Errorf("at least one channel is required")
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		if identified {
			if len(values) > 0 || removeDiscordAuthorization {
				if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
					return err
				}
			}
			if len(webhookIDs) > 0 {
				role, err := projectRole(ctx, tx, actor.UserID, projectID)
				if err != nil {
					return err
				}
				if role != "maintainer" && role != "owner" {
					return persistence.ErrProjectDenied
				}
			}
		}

		if len(webhookIDs) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(webhookIDs)), ",")
			args := make([]any, 0, len(webhookIDs)+1)
			args = append(args, projectID)
			for _, id := range webhookIDs {
				args = append(args, id)
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_endpoints WHERE `+analyticsProjectPredicate("project_id", projectID)+` AND id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
				return err
			}
			if count != len(webhookIDs) {
				return persistence.ErrWebhookNotFound
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE `+analyticsProjectPredicate("project_id", projectID)+` AND id IN (`+placeholders+`)`, args...); err != nil {
				return err
			}
		}
		for key, value := range values {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
				key, value); err != nil {
				return err
			}
		}
		if removeDiscordAuthorization {
			if _, err := tx.ExecContext(ctx, `DELETE FROM discord_authorized_users`); err != nil {
				return err
			}
		}
		if identified {
			details, _ := json.Marshal(struct {
				Keys     []string `json:"credential_keys"`
				Webhooks []string `json:"webhook_ids"`
				Discord  bool     `json:"discord_authorization_removed"`
			}{channelCredentialKeys(values), webhookIDs, removeDiscordAuthorization})
			return projectAudit(ctx, tx, projectID, actor.UserID, "channels.removed", "", string(details))
		}
		return nil
	})
}

// CompareAndSet updates one setting only when its current value and every guard
// still match. The immediate transaction makes the read/guard/write decision
// atomic with concurrent coherent settings replacement.
func (r *SettingsRepo) CompareAndSet(ctx context.Context, key, expected, value string, guards map[string]string) (bool, error) {
	return r.CompareAndSetMany(ctx, map[string]string{key: expected}, map[string]string{key: value}, guards)
}

// CompareAndSetMany validates identity before reading guards, and records an
// audit only when the comparison succeeds and the change commits.
func (r *SettingsRepo) CompareAndSetMany(ctx context.Context, expected, values, guards map[string]string) (bool, error) {
	updated := false
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		keys := channelCredentialKeys(values)
		if identified && len(keys) > 0 {
			if _, err := orgActor(ctx, tx, actor.UserID); err != nil {
				return err
			}
		}
		matches, err := matchesSettingsWithExecutor(ctx, tx, expected)
		if err != nil || !matches {
			return err
		}
		matches, err = matchesSettingsWithExecutor(ctx, tx, guards)
		if err != nil || !matches {
			return err
		}
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, "INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value); err != nil {
				return err
			}
		}
		if identified && len(keys) > 0 {
			details, _ := json.Marshal(keys)
			if err := projectAudit(ctx, tx, "default", actor.UserID, "channel.credentials_changed", "instance", string(details)); err != nil {
				return err
			}
		}
		updated = true
		return nil
	})
	return updated && err == nil, err
}

// MatchesInTx checks an expected settings snapshot using the caller's store
// transaction. It lets durable channel handoffs assert configuration authority
// in the same commit that creates work.
func (r *SettingsRepo) MatchesInTx(ctx context.Context, tx persistence.Tx, expected map[string]string) (bool, error) {
	exec, err := executorFromTx(tx)
	if err != nil {
		return false, err
	}
	return matchesSettingsWithExecutor(ctx, exec, expected)
}

func matchesSettingsWithExecutor(ctx context.Context, exec sqlExecutor, expected map[string]string) (bool, error) {
	for key, value := range expected {
		var current string
		if err := exec.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM app_settings WHERE key = ?), '')`, key).Scan(&current); err != nil {
			return false, err
		}
		if current != value {
			return false, nil
		}
	}
	return true, nil
}

// Set upserts a setting value.
func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	if len(channelCredentialKeys(map[string]string{key: value})) > 0 {
		return r.SetMany(ctx, map[string]string{key: value})
	}
	_, err := execBoundSQLite(ctx, r.db,
		"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}

// Audit only setting names, never tokens, state, secrets or credential values.
func channelCredentialKeys(values map[string]string) []string {
	var keys []string
	for key := range values {
		switch key {
		case "github_app_id", "github_app_slug", "github_app_private_key", "github_auth_mode", "github_pat", "github_pat_user_login", "github_api_endpoint", "github_app_installation_id", "github_app_account_login", "github_app_account_type", "github_app_connected_at",
			"slack_client_id", "slack_client_secret", "slack_app_token", "slack_bot_token", "slack_bot_token_override", "slack_bot_token_source", "slack_bot_user_id", "slack_team_id", "slack_team_name", "slack_connected_at", "slack_oauth_state", "slack_send_responses",
			"telegram_bot_token", "telegram_send_responses", "telegram_rich_messages_v2",
			"discord_bot_token", "discord_bot_user_id", "discord_send_responses",
			"x_consumer_key", "x_consumer_secret", "x_access_token", "x_access_token_secret", "x_poll_interval_seconds", "x_send_responses", "x_account_id", "x_configuration_id", "x_run_as_user_id",
			"email_provider", "email_address", "email_password", "email_imap_host", "email_imap_port", "email_smtp_host", "email_smtp_port", "email_poll_interval_seconds", "email_send_responses", "email_skip_attachments", "email_mark_existing_seen_on_start":
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
