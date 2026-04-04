package repository

import (
	"context"
	"database/sql"
	"fmt"
)

type WorkerRepo struct {
	db *sql.DB
}

func NewWorkerRepo(db *sql.DB) *WorkerRepo {
	return &WorkerRepo{db: db}
}

func (r *WorkerRepo) GetMaxWorkers(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT max_workers FROM worker_settings WHERE id = 'singleton'`).Scan(&n)
	if err != nil {
		return 1, fmt.Errorf("getting max workers: %w", err)
	}
	return n, nil
}

func (r *WorkerRepo) SetMaxWorkers(ctx context.Context, n int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE worker_settings SET max_workers = ?, updated_at = datetime('now') WHERE id = 'singleton'`, n)
	if err != nil {
		return fmt.Errorf("setting max workers: %w", err)
	}
	return nil
}
