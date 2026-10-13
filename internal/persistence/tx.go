package persistence

// Tx is an open store transaction handed to callbacks whose writes must commit
// atomically with the store operation that opened it. Only the store
// implementation that created it can use it.
type Tx interface {
	storeTx()
}

// TxMarker is embedded by store implementations to satisfy Tx.
type TxMarker struct{}

func (TxMarker) storeTx() {}
