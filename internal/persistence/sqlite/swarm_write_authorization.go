package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func authorizeSwarmWrite(ctx context.Context, tx sqlExecutor, target string) error {
	identities := persistence.SwarmWriteIdentities(ctx)
	if len(identities) == 0 {
		return nil
	}
	found := false
	for _, identity := range identities {
		if identity.TaskID == target {
			found = true
		}
		var user string
		err := tx.QueryRowContext(ctx, `SELECT user_id FROM task_run_identities WHERE task_id=?`, identity.TaskID).Scan(&user)
		if errors.Is(err, sql.ErrNoRows) && identity.AllowLegacyOwner && identity.UserID == models.BootstrapUserID {
			user = identity.UserID
			err = nil
		}
		if err != nil || user != identity.UserID {
			return persistence.ErrProjectDenied
		}
		saved, _, err := slackTaskContext(ctx, tx, identity.TaskID, user)
		if err != nil {
			return err
		}
		project, err := authorizeExecutionActor(saved, tx, identity.TaskID, user)
		if err != nil {
			return err
		}
		if project != identity.ProjectID {
			return persistence.ErrProjectDenied
		}
		if err := requireDiscordTaskProvenance(saved, tx, identity.TaskID, user); err != nil {
			return err
		}
	}
	if !found {
		return persistence.ErrProjectDenied
	}
	return nil
}

func auditSwarmWrite(ctx context.Context, tx sqlExecutor, target, action string) error {
	for _, identity := range persistence.SwarmWriteIdentities(ctx) {
		if identity.TaskID == target {
			return projectAudit(ctx, tx, identity.ProjectID, identity.UserID, action, target, `{}`)
		}
	}
	return nil
}
