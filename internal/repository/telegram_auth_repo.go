package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

const telegramAuthorizedUsersListQuery = `SELECT id, project_id, telegram_user_id, telegram_username, display_name, added_at, added_by
				 FROM telegram_authorized_users
				 ORDER BY added_at ASC`

const telegramIsAuthorizedAnywhereQuery = `SELECT COUNT(*) FROM telegram_authorized_users
				 WHERE telegram_user_id = ? OR (telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?))`

// TelegramAuthRepo handles database operations for Telegram authorized users.
type TelegramAuthRepo struct {
	db *sql.DB
}

// NewTelegramAuthRepo creates a new TelegramAuthRepo.
func NewTelegramAuthRepo(db *sql.DB) *TelegramAuthRepo {
	return &TelegramAuthRepo{db: db}
}

// ListByProject returns all system-level authorized Telegram users.
// projectID is accepted for UI compatibility but does not scope inbound authorization.
func (r *TelegramAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.TelegramAuthorizedUser, error) {
	rows, err := r.db.QueryContext(ctx, telegramAuthorizedUsersListQuery)
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

// IsAuthorized checks whether a Telegram user is authorized at the system channel level.
// Checks both by user ID and by username (for entries added by username before the user messaged).
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *TelegramAuthRepo) IsAuthorized(ctx context.Context, projectID string, telegramUserID int64, username string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, telegramUserID, username)
}

// BackfillUserID updates the telegram_user_id for entries that were added by username only.
// Called when a user first messages the bot, so future checks can use the numeric ID.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *TelegramAuthRepo) BackfillUserID(ctx context.Context, projectID string, username string, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE telegram_authorized_users SET telegram_user_id = ?
		 WHERE telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?)`,
		userID, username)
	if err != nil {
		return fmt.Errorf("backfill telegram user id: %w", err)
	}
	return nil
}

// HasAnyAuthorizedUsers checks whether any system-level Telegram authorized users are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *TelegramAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return countAny(ctx, r.db, "telegram_authorized_users", "telegram auth users")
}

// IsAuthorizedAnywhere checks whether a Telegram user is authorized in any project.
// Used when no project is selected yet (e.g., before /start or /project).
func (r *TelegramAuthRepo) IsAuthorizedAnywhere(ctx context.Context, telegramUserID int64, username string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, telegramIsAuthorizedAnywhereQuery, telegramUserID, username).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check telegram auth anywhere: %w", err)
	}
	return count > 0, nil
}

// Create adds a system-level authorized Telegram user.
func (r *TelegramAuthRepo) Create(ctx context.Context, u *models.TelegramAuthorizedUser) error {
	u.TelegramUsername = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(u.TelegramUsername, "@")))
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO telegram_authorized_users (project_id, telegram_user_id, telegram_username, display_name, added_by)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING
		 RETURNING id, added_at`,
		u.ProjectID, u.TelegramUserID, u.TelegramUsername, u.DisplayName, u.AddedBy).
		Scan(&u.ID, &u.AddedAt)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	where := `telegram_user_id = ?`
	arg := any(u.TelegramUserID)
	if u.TelegramUserID == 0 && u.TelegramUsername != "" {
		where = `LOWER(telegram_username) = LOWER(?)`
		arg = u.TelegramUsername
	}
	if u.DisplayName != "" {
		if _, updateErr := r.db.ExecContext(ctx, `UPDATE telegram_authorized_users SET display_name = ?, added_by = ? WHERE `+where, u.DisplayName, u.AddedBy, arg); updateErr != nil {
			return updateErr
		}
	}
	return r.db.QueryRowContext(ctx,
		`SELECT id, added_at FROM telegram_authorized_users WHERE `+where,
		arg).Scan(&u.ID, &u.AddedAt)
}

// Delete removes an authorized Telegram user by ID.
func (r *TelegramAuthRepo) Delete(ctx context.Context, id string) error {
	return deleteByID(ctx, r.db, "telegram_authorized_users", "telegram auth user", id)
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
