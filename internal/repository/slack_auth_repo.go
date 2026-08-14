package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

// SlackAuthRepo handles database operations for Slack authorized users.
type SlackAuthRepo struct {
	allowlist singleIdentifierAllowlist[models.SlackAuthorizedUser]
}

// NewSlackAuthRepo creates a new SlackAuthRepo.
func NewSlackAuthRepo(db *sql.DB) *SlackAuthRepo {
	return &SlackAuthRepo{allowlist: singleIdentifierAllowlist[models.SlackAuthorizedUser]{
		db:                          db,
		table:                       "slack_authorized_users",
		identityColumn:              "slack_user_id",
		conflictTarget:              "slack_user_id",
		matchClause:                 `slack_user_id = ?`,
		listErrLabel:                "slack auth users",
		scanErrLabel:                "slack auth user",
		getErrLabel:                 "get slack auth user",
		deleteEntityLabel:           "slack auth user",
		countAnyErrLabel:            "slack auth users anywhere",
		checkAnywhereErrLabel:       "check slack auth anywhere",
		checkProjectErrLabel:        "check slack project auth",
		updateAddedByOnEmptyDisplay: true,
		scan: func(scanner taskContextScanner) (models.SlackAuthorizedUser, error) {
			var u models.SlackAuthorizedUser
			err := scanner.Scan(&u.ID, &u.ProjectID, &u.SlackUserID, &u.DisplayName, &u.AddedAt, &u.AddedBy)
			return u, err
		},
	}}
}

// ListByProject returns all system-level authorized Slack users.
// projectID is accepted for UI compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.SlackAuthorizedUser, error) {
	return r.allowlist.List(ctx)
}

// IsAuthorized checks whether a Slack user is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) IsAuthorized(ctx context.Context, projectID, slackUserID string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, slackUserID)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership used only for outbound DM compatibility.
func (r *SlackAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, slackUserID string) (bool, error) {
	return r.allowlist.IsAuthorizedForProject(ctx, projectID, slackUserID)
}

// HasAnyAuthorizedUsers checks whether any system-level Slack authorized users are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *SlackAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has Slack authorized users configured.
func (r *SlackAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return r.allowlist.HasAny(ctx)
}

// IsAuthorizedAnywhere checks whether a Slack user is authorized in any project.
func (r *SlackAuthRepo) IsAuthorizedAnywhere(ctx context.Context, slackUserID string) (bool, error) {
	return r.allowlist.IsAuthorizedAnywhere(ctx, slackUserID)
}

// Create adds a system-level authorized Slack user.
func (r *SlackAuthRepo) Create(ctx context.Context, u *models.SlackAuthorizedUser) error {
	identity, id, addedAt, err := r.allowlist.Create(ctx, u.ProjectID, u.SlackUserID, u.DisplayName, u.AddedBy)
	if err != nil {
		return err
	}
	u.SlackUserID = identity
	u.ID = id
	u.AddedAt = addedAt
	return nil
}

// Delete removes an authorized Slack user by ID.
func (r *SlackAuthRepo) Delete(ctx context.Context, id string) error {
	return r.allowlist.Delete(ctx, id)
}

// GetByID returns a single authorized Slack user by ID.
func (r *SlackAuthRepo) GetByID(ctx context.Context, id string) (*models.SlackAuthorizedUser, error) {
	return r.allowlist.GetByID(ctx, id)
}
