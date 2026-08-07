package repository

import (
	"context"
	"database/sql"
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
	return upsertUserProject(ctx, r.db, "telegram_user_projects", "telegram_user_id", telegramUserID, projectID, "user project")
}

// GetUserProject retrieves a user's saved project preference.
// Returns the project ID and nil error if found.
// Returns empty string and nil error if not found.
func (r *TelegramUserProjectRepo) GetUserProject(ctx context.Context, telegramUserID string) (string, error) {
	return getUserProject(ctx, r.db, "telegram_user_projects", "telegram_user_id", telegramUserID, "user project")
}

// DeleteUserProject removes a user's project preference.
func (r *TelegramUserProjectRepo) DeleteUserProject(ctx context.Context, telegramUserID string) error {
	return deleteUserProject(ctx, r.db, "telegram_user_projects", "telegram_user_id", telegramUserID, "user project")
}
