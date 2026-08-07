package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// DiscordAuthRepo handles database operations for Discord authorized users.
type DiscordAuthRepo struct {
	db *sql.DB
}

// NewDiscordAuthRepo creates a new DiscordAuthRepo.
func NewDiscordAuthRepo(db *sql.DB) *DiscordAuthRepo {
	return &DiscordAuthRepo{db: db}
}

// ListByProject returns all system-level authorized Discord users.
// projectID is accepted for UI compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.DiscordAuthorizedUser, error) {
	return listAuthorizedUsers(ctx, r.db, "discord_authorized_users", "discord_user_id", "discord auth users", "discord auth user",
		func(rows *sql.Rows) (models.DiscordAuthorizedUser, error) {
			var u models.DiscordAuthorizedUser
			err := rows.Scan(&u.ID, &u.ProjectID, &u.DiscordUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		})
}

// IsAuthorized checks whether a Discord user is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) IsAuthorized(ctx context.Context, projectID, discordUserID string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, discordUserID)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership used only for outbound DM compatibility.
func (r *DiscordAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, discordUserID string) (bool, error) {
	return countWhere(ctx, r.db, "discord_authorized_users", "check discord project auth",
		`project_id = ? AND discord_user_id = ?`, projectID, strings.TrimSpace(discordUserID))
}

// HasAnyAuthorizedUsers checks whether any system-level Discord authorized users are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has Discord authorized users configured.
func (r *DiscordAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return countAny(ctx, r.db, "discord_authorized_users", "discord auth users anywhere")
}

// IsAuthorizedAnywhere checks whether a Discord user is authorized in any project.
func (r *DiscordAuthRepo) IsAuthorizedAnywhere(ctx context.Context, discordUserID string) (bool, error) {
	return countWhere(ctx, r.db, "discord_authorized_users", "check discord auth anywhere",
		`discord_user_id = ?`, strings.TrimSpace(discordUserID))
}

// Create adds a system-level authorized Discord user.
func (r *DiscordAuthRepo) Create(ctx context.Context, u *models.DiscordAuthorizedUser) error {
	u.DiscordUserID = strings.TrimSpace(u.DiscordUserID)
	return r.db.QueryRowContext(ctx,
		`INSERT INTO discord_authorized_users (project_id, discord_user_id, display_name, added_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(discord_user_id) DO UPDATE SET
			display_name = CASE WHEN excluded.display_name != '' THEN excluded.display_name ELSE discord_authorized_users.display_name END,
			added_by = excluded.added_by
		 RETURNING id, added_at`,
		u.ProjectID, u.DiscordUserID, u.DisplayName, u.AddedBy).
		Scan(&u.ID, &u.AddedAt)
}

// DeleteByProject removes all system-level Discord authorized users.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) DeleteByProject(ctx context.Context, projectID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM discord_authorized_users`); err != nil {
		return fmt.Errorf("delete discord auth users: %w", err)
	}
	return nil
}

// Delete removes an authorized Discord user by ID.
func (r *DiscordAuthRepo) Delete(ctx context.Context, id string) error {
	return deleteByID(ctx, r.db, "discord_authorized_users", "discord auth user", id)
}

// GetByID returns a single authorized Discord user by ID.
func (r *DiscordAuthRepo) GetByID(ctx context.Context, id string) (*models.DiscordAuthorizedUser, error) {
	return getAuthorizedUserByID(ctx, r.db, "discord_authorized_users", "discord_user_id", "get discord auth user", id,
		func(row *sql.Row) (models.DiscordAuthorizedUser, error) {
			var u models.DiscordAuthorizedUser
			err := row.Scan(&u.ID, &u.ProjectID, &u.DiscordUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		})
}
