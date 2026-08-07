package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

// SlackAuthRepo handles database operations for Slack authorized users.
type SlackAuthRepo struct {
	db *sql.DB
}

// NewSlackAuthRepo creates a new SlackAuthRepo.
func NewSlackAuthRepo(db *sql.DB) *SlackAuthRepo {
	return &SlackAuthRepo{db: db}
}

// ListByProject returns all system-level authorized Slack users.
// projectID is accepted for UI compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.SlackAuthorizedUser, error) {
	return listAuthorizedUsers(ctx, r.db, "slack_authorized_users", "slack_user_id", "slack auth users", "slack auth user",
		func(rows *sql.Rows) (models.SlackAuthorizedUser, error) {
			var u models.SlackAuthorizedUser
			err := rows.Scan(&u.ID, &u.ProjectID, &u.SlackUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		})
}

// IsAuthorized checks whether a Slack user is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) IsAuthorized(ctx context.Context, projectID, slackUserID string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, slackUserID)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership used only for outbound DM compatibility.
func (r *SlackAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, slackUserID string) (bool, error) {
	return countWhere(ctx, r.db, "slack_authorized_users", "check slack project auth",
		`project_id = ? AND slack_user_id = ?`, projectID, slackUserID)
}

// HasAnyAuthorizedUsers checks whether any system-level Slack authorized users are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has Slack authorized users configured.
func (r *SlackAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return countAny(ctx, r.db, "slack_authorized_users", "slack auth users anywhere")
}

// IsAuthorizedAnywhere checks whether a Slack user is authorized in any project.
func (r *SlackAuthRepo) IsAuthorizedAnywhere(ctx context.Context, slackUserID string) (bool, error) {
	return countWhere(ctx, r.db, "slack_authorized_users", "check slack auth anywhere",
		`slack_user_id = ?`, slackUserID)
}

// Create adds a system-level authorized Slack user.
func (r *SlackAuthRepo) Create(ctx context.Context, u *models.SlackAuthorizedUser) error {
	return r.db.QueryRowContext(ctx,
		`INSERT INTO slack_authorized_users (project_id, slack_user_id, display_name, added_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(slack_user_id) DO UPDATE SET
			display_name = CASE WHEN excluded.display_name != '' THEN excluded.display_name ELSE slack_authorized_users.display_name END,
			added_by = excluded.added_by
		 RETURNING id, added_at`,
		u.ProjectID, u.SlackUserID, u.DisplayName, u.AddedBy).
		Scan(&u.ID, &u.AddedAt)
}

// Delete removes an authorized Slack user by ID.
func (r *SlackAuthRepo) Delete(ctx context.Context, id string) error {
	return deleteByID(ctx, r.db, "slack_authorized_users", "slack auth user", id)
}

// GetByID returns a single authorized Slack user by ID.
func (r *SlackAuthRepo) GetByID(ctx context.Context, id string) (*models.SlackAuthorizedUser, error) {
	return getAuthorizedUserByID(ctx, r.db, "slack_authorized_users", "slack_user_id", "get slack auth user", id,
		func(row *sql.Row) (models.SlackAuthorizedUser, error) {
			var u models.SlackAuthorizedUser
			err := row.Scan(&u.ID, &u.ProjectID, &u.SlackUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		})
}
