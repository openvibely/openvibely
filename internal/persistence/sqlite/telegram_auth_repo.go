package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

const telegramAuthorizedUsersListQuery = `SELECT id, project_id, telegram_user_id, telegram_username, display_name, added_at, added_by, application_user_id, mapping_review, team_id
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

// ListByProject returns the authorized Telegram users for the shared channel (projectID is retained for compatibility).
func (r *TelegramAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.TelegramAuthorizedUser, error) {
	rows, err := r.db.QueryContext(ctx, telegramAuthorizedUsersListQuery)
	if err != nil {
		return nil, fmt.Errorf("list telegram auth users: %w", err)
	}
	defer rows.Close()

	var users []models.TelegramAuthorizedUser
	for rows.Next() {
		var u models.TelegramAuthorizedUser
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.TelegramUserID, &u.TelegramUsername, &u.DisplayName, &u.AddedAt, &u.AddedBy, &u.ApplicationUserID, &u.MappingReview, &u.TeamID); err != nil {
			return nil, fmt.Errorf("scan telegram auth user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountByProject returns the global sender count; projectID is retained for compatibility.
func (r *TelegramAuthRepo) CountByProject(ctx context.Context, projectID string) (int, error) {
	return countRows(ctx, r.db, "telegram_authorized_users", "telegram auth users")
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
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE telegram_authorized_users SET telegram_user_id = ?
		 WHERE telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?) AND NOT EXISTS (SELECT 1 FROM telegram_authorized_users a WHERE a.telegram_user_id=?)`,
		userID, username, userID)
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

// Create adds an authorized Telegram user for one project.
func (r *TelegramAuthRepo) Create(ctx context.Context, u *models.TelegramAuthorizedUser) error {
	u.TelegramUsername = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(u.TelegramUsername, "@")))
	if u.ApplicationUserID != "" && u.TelegramUserID <= 0 {
		return persistence.ErrProjectDenied
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var old string
		where := `telegram_user_id=?`
		var key any = u.TelegramUserID
		if u.TelegramUserID == 0 {
			where = `telegram_user_id=0 AND lower(telegram_username)=?`
			key = u.TelegramUsername
		}
		lookup := tx.QueryRowContext(ctx, `SELECT id,application_user_id FROM telegram_authorized_users WHERE `+where, key).Scan(&u.ID, &old)
		if lookup != nil && lookup != sql.ErrNoRows {
			return lookup
		}
		actor, err := authorizeSenderMutation(ctx, tx, old, u.ApplicationUserID, lookup == nil)
		if err != nil {
			return err
		}
		if actor != "" {
			u.AddedBy = actor
		}
		if lookup == sql.ErrNoRows {
			err = tx.QueryRowContext(ctx, `INSERT INTO telegram_authorized_users(telegram_user_id,telegram_username,display_name,added_by,application_user_id) VALUES(?,?,?,?,?) RETURNING id,added_at`, u.TelegramUserID, u.TelegramUsername, u.DisplayName, u.AddedBy, u.ApplicationUserID).Scan(&u.ID, &u.AddedAt)
		} else {
			err = tx.QueryRowContext(ctx, `UPDATE telegram_authorized_users SET display_name=CASE WHEN ?<>'' THEN ? ELSE display_name END,application_user_id=?,mapping_review=0 WHERE id=? RETURNING added_at`, u.DisplayName, u.DisplayName, u.ApplicationUserID, u.ID).Scan(&u.AddedAt)
		}
		if err != nil {
			return err
		}
		u.ProjectID = ""
		return senderAudit(ctx, tx, "telegram_authorized_users", u.ID, actor, "saved", old, u.ApplicationUserID)
	})
}

// Delete removes an authorized Telegram user by ID.
func (r *TelegramAuthRepo) Delete(ctx context.Context, id string) error {
	return deleteChannelAllowlist(ctx, r.db, "telegram_authorized_users", "", id)
}

// DeleteForProject removes an authorized Telegram user only from its owning project.
func (r *TelegramAuthRepo) DeleteForProject(ctx context.Context, projectID, id string) error {
	return deleteChannelAllowlist(ctx, r.db, "telegram_authorized_users", projectID, id)
}

// GetByID returns a single authorized user by ID.
func (r *TelegramAuthRepo) GetByID(ctx context.Context, id string) (*models.TelegramAuthorizedUser, error) {
	var u models.TelegramAuthorizedUser
	err := r.db.QueryRowContext(ctx,
		`SELECT id, project_id, telegram_user_id, telegram_username, display_name, added_at, added_by, application_user_id, mapping_review, team_id
		 FROM telegram_authorized_users WHERE id = ?`, id).
		Scan(&u.ID, &u.ProjectID, &u.TelegramUserID, &u.TelegramUsername, &u.DisplayName, &u.AddedAt, &u.AddedBy, &u.ApplicationUserID, &u.MappingReview, &u.TeamID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get telegram auth user: %w", err)
	}
	return &u, nil
}
