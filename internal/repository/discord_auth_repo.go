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
	allowlist singleIdentifierAllowlist[models.DiscordAuthorizedUser]
}

// NewDiscordAuthRepo creates a new DiscordAuthRepo.
func NewDiscordAuthRepo(db *sql.DB) *DiscordAuthRepo {
	return &DiscordAuthRepo{allowlist: singleIdentifierAllowlist[models.DiscordAuthorizedUser]{
		db:                          db,
		table:                       "discord_authorized_users",
		identityColumn:              "discord_user_id",
		conflictTarget:              "project_id, discord_user_id",
		matchClause:                 `discord_user_id = ?`,
		listErrLabel:                "discord auth users",
		scanErrLabel:                "discord auth user",
		getErrLabel:                 "get discord auth user",
		deleteEntityLabel:           "discord auth user",
		countAnyErrLabel:            "discord auth users anywhere",
		checkAnywhereErrLabel:       "check discord auth anywhere",
		checkProjectErrLabel:        "check discord project auth",
		updateAddedByOnEmptyDisplay: true,
		normalize:                   strings.TrimSpace,
		scan: func(scanner taskContextScanner) (models.DiscordAuthorizedUser, error) {
			var u models.DiscordAuthorizedUser
			err := scanner.Scan(&u.ID, &u.ProjectID, &u.DiscordUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		},
	}}
}

// ListByProject returns the authorized Discord users managed by one project.
func (r *DiscordAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.DiscordAuthorizedUser, error) {
	return r.allowlist.ListByProject(ctx, projectID)
}

// CountByProject returns the authorized Discord-user count for one project.
func (r *DiscordAuthRepo) CountByProject(ctx context.Context, projectID string) (int, error) {
	return countRowsByProject(ctx, r.allowlist.db, "discord_authorized_users", "discord auth users", projectID)
}

// IsAuthorized checks whether a Discord user is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) IsAuthorized(ctx context.Context, projectID, discordUserID string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, discordUserID)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership used only for outbound DM compatibility.
func (r *DiscordAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, discordUserID string) (bool, error) {
	return r.allowlist.IsAuthorizedForProject(ctx, projectID, discordUserID)
}

// HasAnyAuthorizedUsers checks whether any system-level Discord authorized users are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *DiscordAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has Discord authorized users configured.
func (r *DiscordAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return r.allowlist.HasAny(ctx)
}

// IsAuthorizedAnywhere checks whether a Discord user is authorized in any project.
func (r *DiscordAuthRepo) IsAuthorizedAnywhere(ctx context.Context, discordUserID string) (bool, error) {
	return r.allowlist.IsAuthorizedAnywhere(ctx, discordUserID)
}

// Create adds a system-level authorized Discord user.
func (r *DiscordAuthRepo) Create(ctx context.Context, u *models.DiscordAuthorizedUser) error {
	identity, id, addedAt, err := r.allowlist.Create(ctx, u.ProjectID, u.DiscordUserID, u.DisplayName, u.AddedBy)
	if err != nil {
		return err
	}
	u.DiscordUserID = identity
	u.ID = id
	u.AddedAt = addedAt
	return nil
}

// DeleteByProject removes all Discord authorization rows owned by one project.
func (r *DiscordAuthRepo) DeleteByProject(ctx context.Context, projectID string) error {
	if _, err := execBoundSQLite(ctx, r.allowlist.db, `DELETE FROM discord_authorized_users WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete discord auth users: %w", err)
	}
	return nil
}

// Delete removes an authorized Discord user by ID.
func (r *DiscordAuthRepo) Delete(ctx context.Context, id string) error {
	return r.allowlist.Delete(ctx, id)
}

// DeleteForProject removes an authorized Discord user only from its owning project.
func (r *DiscordAuthRepo) DeleteForProject(ctx context.Context, projectID, id string) error {
	return r.allowlist.DeleteForProject(ctx, projectID, id)
}

// GetByID returns a single authorized Discord user by ID.
func (r *DiscordAuthRepo) GetByID(ctx context.Context, id string) (*models.DiscordAuthorizedUser, error) {
	return r.allowlist.GetByID(ctx, id)
}
