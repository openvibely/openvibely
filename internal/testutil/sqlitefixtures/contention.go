package sqlitefixtures

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"strings"
)

func Contention(reader, writer *sql.DB) func() storefixture.BackendContention {
	snapshot := func(db *sql.DB) storefixture.IOContention {
		s := db.Stats()
		return storefixture.IOContention{Capacity: s.MaxOpenConnections, InUse: s.InUse, WaitCount: s.WaitCount, WaitDuration: s.WaitDuration}
	}
	return func() storefixture.BackendContention {
		return storefixture.BackendContention{Reads: snapshot(reader), Writes: snapshot(writer)}
	}
}

func HoldProjectRead(db *sql.DB) func(context.Context) (func() error, error) {
	return func(ctx context.Context) (func() error, error) {
		rows, err := db.QueryContext(ctx, `SELECT COUNT(*) FROM projects`)
		if err != nil {
			return nil, err
		}
		return rows.Close, nil
	}
}
func SettingsReadObservers(store persistence.SettingsStore) func(func(), func()) {
	if scoped, ok := store.(*persistence.UserSettings); ok {
		store = scoped.SettingsStore
	}
	repo := store.(*sqlite.SettingsRepo)
	wrap := func(callback func()) func(string) {
		if callback == nil {
			return nil
		}
		return func(query string) {
			if strings.Contains(strings.ToLower(query), "from app_settings") {
				callback()
			}
		}
	}
	return func(requested, acquired func()) {
		repo.SetQueryObserver(wrap(requested))
		repo.SetQueryAcquiredObserver(wrap(acquired))
	}
}
