package repository

import (
	"context"
	"database/sql"
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

// CompareAndSet updates one setting only when its current value and every guard
// still match. The immediate transaction makes the read/guard/write decision
// atomic with concurrent coherent settings replacement.
func (r *SettingsRepo) CompareAndSet(ctx context.Context, key, expected, value string, guards map[string]string) (bool, error) {
	updated := false
	err := withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		read := func(settingKey string) (string, error) {
			var current string
			err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM app_settings WHERE key = ?), '')`, settingKey).Scan(&current)
			return current, err
		}
		current, err := read(key)
		if err != nil {
			return err
		}
		if current != expected {
			return nil
		}
		for guardKey, guardValue := range guards {
			currentGuard, err := read(guardKey)
			if err != nil {
				return err
			}
			if currentGuard != guardValue {
				return nil
			}
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

// Set upserts a setting value.
func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	_, err := execBoundSQLite(ctx, r.db,
		"INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value)
	return err
}
