package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// TelegramAuthRepo handles database operations for Telegram authorized users.
type TelegramAuthRepo struct {
	db *sql.DB
}

// NewTelegramAuthRepo creates a new TelegramAuthRepo.
func NewTelegramAuthRepo(db *sql.DB) *TelegramAuthRepo {
	return &TelegramAuthRepo{db: db}
}

// ListByProject returns all authorized Telegram users for a project.
func (r *TelegramAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.TelegramAuthorizedUser, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, telegram_user_id, telegram_username, display_name, added_at, added_by
		 FROM telegram_authorized_users
		 WHERE project_id = ?
		 ORDER BY added_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list telegram auth users: %w", err)
	}
	defer rows.Close()

	var users []models.TelegramAuthorizedUser
	for rows.Next() {
		var u models.TelegramAuthorizedUser
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.TelegramUserID, &u.TelegramUsername, &u.DisplayName, &u.AddedAt, &u.AddedBy); err != nil {
			return nil, fmt.Errorf("scan telegram auth user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// IsAuthorized checks whether a Telegram user is authorized for a given project.
// Checks both by user ID and by username (for entries added by username before the user messaged).
func (r *TelegramAuthRepo) IsAuthorized(ctx context.Context, projectID string, telegramUserID int64, username string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM telegram_authorized_users
		 WHERE project_id = ?
		   AND (telegram_user_id = ? OR (telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?)))`,
		projectID, telegramUserID, username).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check telegram auth: %w", err)
	}
	return count > 0, nil
}

// BackfillUserID updates the telegram_user_id for entries that were added by username only.
// Called when a user first messages the bot, so future checks can use the numeric ID.
func (r *TelegramAuthRepo) BackfillUserID(ctx context.Context, projectID string, username string, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE telegram_authorized_users SET telegram_user_id = ?
		 WHERE project_id = ? AND telegram_user_id = 0 AND LOWER(telegram_username) = LOWER(?)`,
		userID, projectID, username)
	if err != nil {
		return fmt.Errorf("backfill telegram user id: %w", err)
	}
	return nil
}

// HasAnyAuthorizedUsers checks whether a project has any authorized Telegram users configured.
func (r *TelegramAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM telegram_authorized_users WHERE project_id = ?`,
		projectID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count telegram auth users: %w", err)
	}
	return count > 0, nil
}

// IsAuthorizedAnywhere checks whether a Telegram user is authorized in any project.
// Used when no project is selected yet (e.g., before /start or /project).
func (r *TelegramAuthRepo) IsAuthorizedAnywhere(ctx context.Context, telegramUserID int64, username string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM telegram_authorized_users
		 WHERE telegram_user_id = ? OR (telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?))`,
		telegramUserID, username).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check telegram auth anywhere: %w", err)
	}
	return count > 0, nil
}

// Create adds a new authorized Telegram user to a project.
func (r *TelegramAuthRepo) Create(ctx context.Context, u *models.TelegramAuthorizedUser) error {
	return r.db.QueryRowContext(ctx,
		`INSERT INTO telegram_authorized_users (project_id, telegram_user_id, telegram_username, display_name, added_by)
		 VALUES (?, ?, ?, ?, ?)
		 RETURNING id, added_at`,
		u.ProjectID, u.TelegramUserID, u.TelegramUsername, u.DisplayName, u.AddedBy).
		Scan(&u.ID, &u.AddedAt)
}

// Delete removes an authorized Telegram user by ID.
func (r *TelegramAuthRepo) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM telegram_authorized_users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete telegram auth user: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("telegram auth user not found")
	}
	return nil
}

// GetByID returns a single authorized user by ID.
func (r *TelegramAuthRepo) GetByID(ctx context.Context, id string) (*models.TelegramAuthorizedUser, error) {
	var u models.TelegramAuthorizedUser
	err := r.db.QueryRowContext(ctx,
		`SELECT id, project_id, telegram_user_id, telegram_username, display_name, added_at, added_by
		 FROM telegram_authorized_users WHERE id = ?`, id).
		Scan(&u.ID, &u.ProjectID, &u.TelegramUserID, &u.TelegramUsername, &u.DisplayName, &u.AddedAt, &u.AddedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get telegram auth user: %w", err)
	}
	return &u, nil
}
