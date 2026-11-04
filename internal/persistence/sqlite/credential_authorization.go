package sqlite

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Caller role claims are never used. This check runs under the credential write
// transaction, serializing against membership revocation on the same database.
func requireCredentialActor(ctx context.Context, tx sqlExecutor) error {
	actor, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return nil
	} // provider refresh and legacy internal callers
	var allowed int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u JOIN org_memberships m ON m.user_id=u.id WHERE u.id=? AND u.disabled=0 AND m.org_id=? AND m.role IN ('owner','admin')`, actor.UserID, models.DefaultOrgID).Scan(&allowed)
	if err != nil {
		return err
	}
	if allowed != 1 {
		return persistence.ErrProjectDenied
	}
	return nil
}

func authorizedCredentialWrite(ctx context.Context, db *sql.DB, write func(sqlExecutor) (bool, error)) (bool, error) {
	changed := false
	err := withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if err := requireCredentialActor(ctx, tx); err != nil {
			return err
		}
		var err error
		changed, err = write(tx)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}
