package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func snapshotDiscordSchedule(ctx context.Context, tx sqlExecutor, scheduleID string) error {
	identity, ok := models.DiscordIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	var taskID string
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM schedules WHERE id=?`, scheduleID).Scan(&taskID); err != nil {
		return err
	}
	if err := authorizeDiscordExecutionTask(ctx, tx, taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_schedule_provenance(schedule_id,project_id,external_user_id,user_id) VALUES(?,?,?,?)`, scheduleID, identity.ProjectID, identity.ExternalUserID, identity.UserID)
	return err
}

func discordScheduleContext(ctx context.Context, tx sqlExecutor, scheduleID, userID string) (context.Context, error) {
	var identity models.DiscordIngressIdentity
	var taskID string
	err := tx.QueryRowContext(ctx, `SELECT p.project_id,p.external_user_id,p.user_id,s.task_id FROM discord_schedule_provenance p JOIN schedules s ON s.id=p.schedule_id WHERE p.schedule_id=?`, scheduleID).Scan(&identity.ProjectID, &identity.ExternalUserID, &identity.UserID, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	if userID != identity.UserID {
		return ctx, persistence.ErrProjectDenied
	}
	saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: userID}), identity)
	if err := authorizeDiscordExecutionTask(saved, tx, taskID); err != nil {
		return ctx, err
	}
	return saved, nil
}
