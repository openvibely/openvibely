package persistence

// Tx is an open store transaction handed to callbacks whose writes must commit
// atomically with the store operation that opened it. It is opaque outside the
// store implementation that created it.
type Tx struct {
	impl any
}

// NewTx wraps a store implementation's transaction handle.
func NewTx(impl any) Tx {
	return Tx{impl: impl}
}

// Impl returns the wrapped store implementation handle.
func (tx Tx) Impl() any {
	return tx.impl
}
