package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func snapshotSlackExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_execution_provenance(execution_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?)`, executionID, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	return err
}

// Provenance survives deletion of an allowlist binding: recovery must reject it,
// rather than treating the execution as an ordinary non-channel run.
func requireSlackExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	var identity models.SlackIngressIdentity
	var taskID, executor string
	err := tx.QueryRowContext(ctx, `SELECT s.project_id,s.team_id,s.external_user_id,s.user_id,e.task_id,COALESCE(r.user_id,'') FROM slack_execution_provenance s JOIN executions e ON e.id=s.execution_id LEFT JOIN execution_run_identities r ON r.execution_id=e.id WHERE s.execution_id=?`, executionID).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID, &taskID, &executor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if executor != identity.UserID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	return authorizeSlackTaskMutation(saved, tx, taskID)
}

func snapshotSlackInput(ctx context.Context, tx sqlExecutor, input *models.ThreadInput) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if identity.ProjectID != input.ProjectID || identity.UserID != input.AuthorUserID {
		return persistence.ErrProjectDenied
	}
	if err := authorizeSlackHandoff(ctx, tx, identity.EventKey); err != nil {
		return err
	}
	if input.TaskID != "" {
		if err := authorizeSlackTaskMutation(ctx, tx, input.TaskID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_input_provenance(input_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?)`, input.ID, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	return projectAudit(ctx, tx, identity.ProjectID, identity.UserID, "slack.input_admitted", input.ID, `{}`)
}

func promoteSlackInput(ctx context.Context, tx sqlExecutor, input models.ThreadInput, executionID string) error {
	var identity models.SlackIngressIdentity
	err := tx.QueryRowContext(ctx, `SELECT project_id,team_id,external_user_id,user_id FROM slack_input_provenance WHERE input_id=?`, input.ID).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity.ProjectID != input.ProjectID || identity.UserID != input.AuthorUserID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	if err := authorizeSlackHandoff(saved, tx, identity.EventKey); err != nil {
		return err
	}
	var taskID string
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM executions WHERE id=?`, executionID).Scan(&taskID); err != nil {
		return err
	}
	if err := authorizeSlackTaskMutation(saved, tx, taskID); err != nil {
		return err
	}
	return snapshotSlackExecution(saved, tx, executionID)
}

func requireSlackInput(ctx context.Context, tx sqlExecutor, inputID string) error {
	var identity models.SlackIngressIdentity
	var author, projectID string
	err := tx.QueryRowContext(ctx, `SELECT s.project_id,s.team_id,s.external_user_id,s.user_id,i.author_user_id,i.project_id FROM slack_input_provenance s JOIN thread_inputs i ON i.id=s.input_id WHERE s.input_id=?`, inputID).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID, &author, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity.UserID != author || identity.ProjectID != projectID {
		return persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	return authorizeSlackHandoff(saved, tx, identity.EventKey)
}
