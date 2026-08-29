package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type inboundReceiptHandoffSpec struct {
	notConfiguredError   string
	persistRequiredError string
	insertSQL            string
	insertArgs           []interface{}
	recordError          string
	rowsAffectedError    string
}

func inboundReceiptHandoff(ctx context.Context, db *sql.DB, spec inboundReceiptHandoffSpec, persist func(SQLExecutor) error) (alreadyHandedOff bool, err error) {
	if db == nil {
		return false, errors.New(spec.notConfiguredError)
	}
	if persist == nil {
		return false, errors.New(spec.persistRequiredError)
	}
	err = withImmediateTx(ctx, db, func(exec SQLExecutor) error {
		result, err := exec.ExecContext(ctx, spec.insertSQL, spec.insertArgs...)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.recordError, err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("%s: %w", spec.rowsAffectedError, err)
		}
		if inserted == 0 {
			alreadyHandedOff = true
			return nil
		}
		return persist(exec)
	})
	return alreadyHandedOff, err
}
