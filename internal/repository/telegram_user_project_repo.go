package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// TelegramUserProjectRepo handles persisting Telegram user project selections.
type TelegramUserProjectRepo struct {
	db *sql.DB
}

// NewTelegramUserProjectRepo creates a new TelegramUserProjectRepo.
func NewTelegramUserProjectRepo(db *sql.DB) *TelegramUserProjectRepo {
	return &TelegramUserProjectRepo{db: db}
}

// SetUserProject saves or updates a user's project preference.
func (r *TelegramUserProjectRepo) SetUserProject(ctx context.Context, telegramUserID, projectID string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO telegram_user_projects (telegram_user_id, project_id, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(telegram_user_id) DO UPDATE SET project_id = ?, updated_at = datetime('now')`,
		telegramUserID, projectID, projectID)
	if err != nil {
		return fmt.Errorf("set user project: %w", err)
	}
	return nil
}

// GetUserProject retrieves a user's saved project preference.
// Returns the project ID and nil error if found.
// Returns empty string and nil error if not found.
func (r *TelegramUserProjectRepo) GetUserProject(ctx context.Context, telegramUserID string) (string, error) {
	var projectID string
	err := r.db.QueryRowContext(ctx,
		`SELECT project_id FROM telegram_user_projects WHERE telegram_user_id = ?`,
		telegramUserID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get user project: %w", err)
	}
	return projectID, nil
}

// DeleteUserProject removes a user's project preference.
func (r *TelegramUserProjectRepo) DeleteUserProject(ctx context.Context, telegramUserID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM telegram_user_projects WHERE telegram_user_id = ?`,
		telegramUserID)
	if err != nil {
		return fmt.Errorf("delete user project: %w", err)
	}
	return nil
}
