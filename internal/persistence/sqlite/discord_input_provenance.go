package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func snapshotDiscordInput(ctx context.Context, tx sqlExecutor, input *models.ThreadInput) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	if identity.ProjectID != input.ProjectID || identity.UserID != input.AuthorUserID || input.TaskID == "" {
		return persistence.ErrProjectDenied
	}
	if err := authorizeDiscordExecutionTask(ctx, tx, input.TaskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO discord_input_provenance(input_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, input.ID, identity.ProjectID, identity.ExternalUserID, identity.UserID); err != nil {
		return err
	}
	return projectAudit(ctx, tx, identity.ProjectID, identity.UserID, "discord.input_admitted", input.ID, `{}`)
}

func discordInputContext(ctx context.Context, tx sqlExecutor, inputID string) (context.Context, bool, error) {
	var identity models.DiscordIngressIdentity
	var project, author, taskID string
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.external_user_id,d.user_id,i.project_id,i.author_user_id,i.task_id FROM discord_input_provenance d JOIN thread_inputs i ON i.id=d.input_id WHERE d.input_id=?`, inputID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &project, &author, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, err
	}
	if identity.ProjectID != project || identity.UserID != author {
		return ctx, false, persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
	if err := authorizeDiscordExecutionTask(saved, tx, taskID); err != nil {
		return ctx, false, err
	}
	return saved, true, nil
}
func promoteDiscordInput(ctx context.Context, tx sqlExecutor, input models.ThreadInput, executionID string) error {
	saved, found, err := discordInputContext(ctx, tx, input.ID)
	if err != nil || !found {
		return err
	}
	var taskID, userID string
	if err := tx.QueryRowContext(ctx, `SELECT e.task_id,r.user_id FROM executions e JOIN execution_run_identities r ON r.execution_id=e.id WHERE e.id=?`, executionID).Scan(&taskID, &userID); err != nil {
		return err
	}
	identity, _ := models.DiscordIngressIdentityFromContext(saved)
	if taskID != input.TaskID || userID != identity.UserID {
		return persistence.ErrProjectDenied
	}
	return snapshotDiscordExecution(saved, tx, executionID)
}
