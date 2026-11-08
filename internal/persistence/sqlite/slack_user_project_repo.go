package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// SlackUserProjectRepo persists active project selection per Slack team/user.
//
// Unlike the single-key Discord/Telegram/Email user-project repos, Slack
// selection is keyed by a composite (slack_team_id, slack_user_id), so it
// keeps its own upsert/get/delete SQL rather than delegating to the shared
// single-key helpers in auth_repo_common.go.
type SlackUserProjectRepo struct {
	db *sql.DB
}

func NewSlackUserProjectRepo(db *sql.DB) *SlackUserProjectRepo {
	return &SlackUserProjectRepo{db: db}
}

func (r *SlackUserProjectRepo) SetUserProject(ctx context.Context, teamID, userID, projectID string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		if identity, ok := models.SlackIngressIdentityFromContext(ctx); ok {
			if identity.TeamID != teamID || identity.ExternalUserID != userID {
				return persistence.ErrProjectDenied
			}
			if err := authorizeSlackHandoff(ctx, tx, identity.EventKey); err != nil {
				return err
			}
			mapped, err := resolveSlackApplicationUser(ctx, tx, projectID, teamID, userID)
			if err != nil || mapped != actor.UserID {
				return persistence.ErrProjectDenied
			}
		}
		if identified {
			role, err := projectRole(ctx, tx, actor.UserID, projectID)
			if err != nil {
				return err
			}
			if role != "editor" && role != "maintainer" && role != "owner" {
				return persistence.ErrProjectDenied
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO slack_user_projects (slack_team_id, slack_user_id, project_id, updated_at)
   VALUES (?, ?, ?, datetime('now')) ON CONFLICT(slack_team_id,slack_user_id) DO UPDATE SET project_id=excluded.project_id,updated_at=datetime('now')`, teamID, userID, projectID)
		if err != nil {
			return fmt.Errorf("set slack user project: %w", err)
		}
		if identified {
			return projectAudit(ctx, tx, projectID, actor.UserID, "slack.project_selected", projectID, `{}`)
		}
		return nil
	})
}

func (r *SlackUserProjectRepo) GetUserProject(ctx context.Context, teamID, userID string) (string, error) {
	var projectID string
	err := r.db.QueryRowContext(ctx,
		`SELECT project_id FROM slack_user_projects WHERE slack_team_id = ? AND slack_user_id = ?`,
		teamID, userID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get slack user project: %w", err)
	}
	return projectID, nil
}

func (r *SlackUserProjectRepo) DeleteUserProject(ctx context.Context, teamID, userID string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`DELETE FROM slack_user_projects WHERE slack_team_id = ? AND slack_user_id = ?`,
		teamID, userID)
	if err != nil {
		return fmt.Errorf("delete slack user project: %w", err)
	}
	return nil
}
