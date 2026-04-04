package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// SlackUserProjectRepo persists active project selection per Slack team/user.
type SlackUserProjectRepo struct {
	db *sql.DB
}

func NewSlackUserProjectRepo(db *sql.DB) *SlackUserProjectRepo {
	return &SlackUserProjectRepo{db: db}
}

func (r *SlackUserProjectRepo) SetUserProject(ctx context.Context, teamID, userID, projectID string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO slack_user_projects (slack_team_id, slack_user_id, project_id, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(slack_team_id, slack_user_id) DO UPDATE
		 SET project_id = excluded.project_id, updated_at = datetime('now')`,
		teamID, userID, projectID)
	if err != nil {
		return fmt.Errorf("set slack user project: %w", err)
	}
	return nil
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
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM slack_user_projects WHERE slack_team_id = ? AND slack_user_id = ?`,
		teamID, userID)
	if err != nil {
		return fmt.Errorf("delete slack user project: %w", err)
	}
	return nil
}
