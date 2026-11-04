package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func snapshotSlackSchedule(ctx context.Context, tx sqlExecutor, scheduleID string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slack_schedule_provenance(schedule_id,project_id,team_id,external_user_id,user_id) VALUES(?,?,?,?,?)`, scheduleID, identity.ProjectID, identity.TeamID, identity.ExternalUserID, identity.UserID)
	return err
}

func slackScheduleContext(ctx context.Context, tx sqlExecutor, scheduleID, userID string) (context.Context, error) {
	var identity models.SlackIngressIdentity
	var taskID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.team_id,p.external_user_id,p.user_id,s.task_id FROM slack_schedule_provenance p JOIN schedules s ON s.id=p.schedule_id WHERE p.schedule_id=?`, scheduleID).Scan(&identity.ProjectID, &identity.TeamID, &identity.ExternalUserID, &identity.UserID, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if userID != identity.UserID {
		return ctx, persistence.ErrProjectDenied
	}
	saved := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	if err := authorizeSlackTaskMutation(saved, tx, taskID); err != nil {
		return ctx, err
	}
	return saved, nil
}
