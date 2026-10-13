package sqlite

import (
	"errors"

	"github.com/openvibely/openvibely/internal/persistence"
)

var errForeignStoreTx = errors.New("persistence transaction was not opened by the SQLite store")

func storeTx(exec sqlExecutor) persistence.Tx {
	return persistence.NewTx(exec)
}

func executorFromTx(tx persistence.Tx) (sqlExecutor, error) {
	exec, ok := tx.Impl().(sqlExecutor)
	if !ok || exec == nil {
		return nil, errForeignStoreTx
	}
	return exec, nil
}

func withStoreTx(persist func(persistence.Tx) error) func(sqlExecutor) error {
	if persist == nil {
		return nil
	}
	return func(exec sqlExecutor) error {
		return persist(storeTx(exec))
	}
}
