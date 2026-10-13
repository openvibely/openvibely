package sqlite

import (
	"errors"

	"github.com/openvibely/openvibely/internal/persistence"
)

var errForeignStoreTx = errors.New("persistence transaction was not opened by the SQLite store")

type sqliteTx struct {
	persistence.TxMarker
	exec sqlExecutor
}

func storeTx(exec sqlExecutor) persistence.Tx {
	return sqliteTx{exec: exec}
}

func executorFromTx(tx persistence.Tx) (sqlExecutor, error) {
	stx, ok := tx.(sqliteTx)
	if !ok || stx.exec == nil {
		return nil, errForeignStoreTx
	}
	return stx.exec, nil
}

func withStoreTx(persist func(persistence.Tx) error) func(sqlExecutor) error {
	if persist == nil {
		return nil
	}
	return func(exec sqlExecutor) error {
		return persist(storeTx(exec))
	}
}
