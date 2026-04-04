package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type AlertRepo struct {
	db *sql.DB
}

func NewAlertRepo(db *sql.DB) *AlertRepo {
	return &AlertRepo{db: db}
}

func (r *AlertRepo) Create(ctx context.Context, a *models.Alert) error {
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO alerts (project_id, task_id, execution_id, type, severity, title, message)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, is_read, created_at`,
		a.ProjectID, a.TaskID, a.ExecutionID, a.Type, a.Severity, a.Title, a.Message).
		Scan(&a.ID, &a.IsRead, &a.CreatedAt)
	if err != nil {
		return fmt.Errorf("creating alert: %w", err)
	}
	return nil
}

func (r *AlertRepo) GetByID(ctx context.Context, id string) (*models.Alert, error) {
	var a models.Alert
	err := r.db.QueryRowContext(ctx,
		`SELECT id, project_id, task_id, execution_id, type, severity, title, message, is_read, created_at
		 FROM alerts WHERE id = ?`, id).
		Scan(&a.ID, &a.ProjectID, &a.TaskID, &a.ExecutionID, &a.Type, &a.Severity,
			&a.Title, &a.Message, &a.IsRead, &a.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("alert not found: %s", id)
		}
		return nil, fmt.Errorf("getting alert: %w", err)
	}
	return &a, nil
}

func (r *AlertRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Alert, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, task_id, execution_id, type, severity, title, message, is_read, created_at
		 FROM alerts WHERE project_id = ?
		 ORDER BY created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing alerts: %w", err)
	}
	defer rows.Close()

	var alerts []models.Alert
	for rows.Next() {
		var a models.Alert
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.TaskID, &a.ExecutionID, &a.Type, &a.Severity,
			&a.Title, &a.Message, &a.IsRead, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning alert: %w", err)
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

func (r *AlertRepo) CountUnread(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alerts WHERE project_id = ? AND is_read = 0`, projectID).
		Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting unread alerts: %w", err)
	}
	return count, nil
}

func (r *AlertRepo) MarkRead(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE alerts SET is_read = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("marking alert read: %w", err)
	}
	return nil
}

func (r *AlertRepo) MarkAllRead(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE alerts SET is_read = 1 WHERE project_id = ? AND is_read = 0`, projectID)
	if err != nil {
		return fmt.Errorf("marking all alerts read: %w", err)
	}
	return nil
}

func (r *AlertRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM alerts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting alert: %w", err)
	}
	return nil
}

func (r *AlertRepo) DeleteAll(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM alerts WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("deleting all alerts: %w", err)
	}
	return nil
}
