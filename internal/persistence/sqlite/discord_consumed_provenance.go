package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Outcome recording must survive revocation during delivery. Local commit has
// already authorized the input; provider callbacks record effects already sent.
func copyDiscordConsumedInput(ctx context.Context, tx sqlExecutor, inputID, executionID string) error {
	var identity models.DiscordIngressIdentity
	var taskID, runID, author string
	err := tx.QueryRowContext(ctx, `SELECT d.project_id,d.external_user_id,d.user_id,i.task_id,COALESCE(i.run_execution_id,''),i.author_user_id FROM discord_input_provenance d JOIN thread_inputs i ON i.id=d.input_id WHERE d.input_id=?`, inputID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &taskID, &runID, &author)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if executionID == "" {
		executionID = runID
	}
	var actualTask, project string
	if err := tx.QueryRowContext(ctx, `SELECT e.task_id,t.project_id FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=?`, executionID).Scan(&actualTask, &project); err != nil {
		return err
	}
	if taskID != actualTask || project != identity.ProjectID || author != identity.UserID || (runID != "" && runID != executionID) {
		return persistence.ErrProjectDenied
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO discord_consumed_provenance(execution_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, executionID, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return nil
	}
	return projectAudit(ctx, tx, identity.ProjectID, identity.UserID, "discord.input_consumed", inputID, `{}`)
}
func requireDiscordConsumedExecution(ctx context.Context, tx sqlExecutor, executionID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT project_id,external_user_id,user_id FROM discord_consumed_provenance WHERE execution_id=?`, executionID)
	if err != nil {
		return err
	}
	var identities []models.DiscordIngressIdentity
	for rows.Next() {
		var identity models.DiscordIngressIdentity
		if err := rows.Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID); err != nil {
			rows.Close()
			return err
		}
		identities = append(identities, identity)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(identities) == 0 {
		return nil
	}
	var taskID string
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM executions WHERE id=?`, executionID).Scan(&taskID); err != nil {
		return err
	}
	for _, identity := range identities {
		saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
		if err := authorizeDiscordExecutionTask(saved, tx, taskID); err != nil {
			return err
		}
	}
	return nil
}
