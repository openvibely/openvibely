package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func authorizeSlackTaskMutation(ctx context.Context, tx sqlExecutor, taskID string) error {
	identity, ok := models.SlackIngressIdentityFromContext(ctx)
	if !ok {
		return nil
	}
	var projectID, origin string
	var merge, mergeGoal bool
	if err := tx.QueryRowContext(ctx, `SELECT project_id,created_via,auto_merge,auto_merge_on_goal_achieved FROM tasks WHERE id=?`, taskID).Scan(&projectID, &origin, &merge, &mergeGoal); err != nil {
		return err
	}
	if projectID != identity.ProjectID {
		return persistence.ErrProjectDenied
	}
	if err := authorizeSlackHandoff(ctx, tx, identity.EventKey); err != nil {
		return err
	}
	if merge || mergeGoal || origin == models.TaskOriginWebhook || persistence.IsAutomationTaskCreatedVia(origin) {
		role, err := projectRole(ctx, tx, identity.UserID, projectID)
		if err != nil {
			return err
		}
		if role != "maintainer" && role != "owner" {
			return persistence.ErrProjectDenied
		}
	}
	return nil
}

func (r *ScheduleRepo) withScheduleAction(ctx context.Context, id, taskID, action string, apply func(sqlExecutor) error) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		identity, slack := models.SlackIngressIdentityFromContext(ctx)
		if slack {
			var actual string
			if err := tx.QueryRowContext(ctx, `SELECT task_id FROM schedules WHERE id=?`, id).Scan(&actual); err != nil {
				return err
			}
			if taskID != "" && actual != taskID {
				return persistence.ErrProjectDenied
			}
			if err := authorizeSlackTaskMutation(ctx, tx, actual); err != nil {
				return err
			}
		}
		if err := apply(tx); err != nil {
			return err
		}
		if slack {
			return projectAudit(ctx, tx, identity.ProjectID, identity.UserID, action, id, `{}`)
		}
		return nil
	})
}
