package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	return withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		for key, value := range values {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
				key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveChannelsBulk atomically resets provider settings and removes project-owned
// webhooks. Discord authorization is system-wide and is cleared only when the
// Discord provider is part of the same removal request.
func (r *SettingsRepo) RemoveChannelsBulk(ctx context.Context, projectID string, values map[string]string, webhookIDs []string, removeDiscordAuthorization bool) error {
	if len(values) == 0 && len(webhookIDs) == 0 {
		return fmt.Errorf("at least one channel is required")
	}
	return withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		if len(webhookIDs) > 0 {
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(webhookIDs)), ",")
			args := make([]any, 0, len(webhookIDs)+1)
			args = append(args, projectID)
			for _, id := range webhookIDs {
				args = append(args, id)
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_endpoints WHERE project_id = ? AND id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
				return err
			}
			if count != len(webhookIDs) {
				return ErrWebhookNotFound
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE project_id = ? AND id IN (`+placeholders+`)`, args...); err != nil {
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
		return nil
	})
}

// CompareAndSet updates one setting only when its current value and every guard
// still match. The immediate transaction makes the read/guard/write decision
// atomic with concurrent coherent settings replacement.
func (r *SettingsRepo) CompareAndSet(ctx context.Context, key, expected, value string, guards map[string]string) (bool, error) {
	updated := false
	err := withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		matches, err := r.MatchesWithExecutor(ctx, tx, map[string]string{key: expected})
		if err != nil || !matches {
			return err
		}
		matches, err = r.MatchesWithExecutor(ctx, tx, guards)
		if err != nil || !matches {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			key, value); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

// MatchesWithExecutor checks an expected settings snapshot using the caller's
// transaction. It lets durable channel handoffs assert configuration authority
// in the same commit that creates work.
func (r *SettingsRepo) MatchesWithExecutor(ctx context.Context, exec SQLExecutor, expected map[string]string) (bool, error) {
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
	_, err := execBoundSQLite(ctx, r.db,
		"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}
