package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Called only with an authorized explicit run-as binding, in its transaction.
func saveSlackTaskProvenance(ctx context.Context, tx sqlExecutor, taskID string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		_, err := tx.ExecContext(ctx, `DELETE FROM slack_task_provenance WHERE task_id=?`, taskID)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_task_provenance(task_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET project_id=excluded.project_id,team_id=excluded.team_id,external_user_id=excluded.external_user_id,user_id=excluded.user_id`, taskID, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	return err
}

func slackTaskContext(ctx context.Context, tx sqlExecutor, taskID, userID string) (context.Context, bool, error) {
	var identity models.SlackIngressIdentity
	err := tx.QueryRowContext(ctx, `SELECT project_id,team_id,external_user_id,user_id FROM slack_task_provenance WHERE task_id=?`, taskID).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, err
	}
	if identity.UserID != userID {
		return ctx, false, persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	if err := authorizeSlackTaskMutation(saved, tx, taskID); err != nil {
		return ctx, false, err
	}
	return saved, true, nil
}
