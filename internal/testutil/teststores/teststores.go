// Package teststores opens the persistence backend for tests, selected by
// TEST_DATABASE_DRIVER the same way DATABASE_DRIVER selects it at startup.
package teststores

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
)

// DriverEnv selects the backend tests run against. Defaults to sqlite.
const DriverEnv = "TEST_DATABASE_DRIVER"

// Driver returns the normalized backend name selected by TEST_DATABASE_DRIVER.
func Driver() string {
	driver := strings.ToLower(strings.TrimSpace(os.Getenv(DriverEnv)))
	if driver == "" {
		return config.DatabaseDriverSQLite
	}
	return driver
}

// NewDB opens a fresh, isolated, migrated database for the selected backend.
// It is closed when the test finishes.
func NewDB(tb testing.TB) *sql.DB {
	tb.Helper()
	switch driver := Driver(); driver {
	case config.DatabaseDriverSQLite:
		return testutil.NewTestDB(tb)
	default:
		tb.Fatalf("unsupported %s %q (supported: %s)", DriverEnv, driver, config.DatabaseDriverSQLite)
		return nil
	}
}

type options struct {
	broadcaster *events.Broadcaster
}

type Option func(*options)

// WithBroadcaster wires b into stores that publish events.
func WithBroadcaster(b *events.Broadcaster) Option {
	return func(o *options) { o.broadcaster = b }
}

// Of builds the selected backend's stores on a database opened by NewDB.
// Each call returns new store instances.
func Of(db *sql.DB, opts ...Option) persistence.Stores {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	switch Driver() {
	case config.DatabaseDriverSQLite:
		return sqlite.NewStores(db, o.broadcaster)
	default:
		panic("teststores: unsupported " + DriverEnv + " " + Driver())
	}
}

// Env is a fresh test database and the stores built on it.
type Env struct {
	DB *sql.DB
	persistence.Stores
}

// New opens a fresh database for the selected backend and builds its stores.
func New(tb testing.TB, opts ...Option) *Env {
	tb.Helper()
	db := NewDB(tb)
	return &Env{DB: db, Stores: Of(db, opts...)}
}

// RequireSQLite skips tests that depend on SQLite internals (statement
// counting, file-backed topology, triggers, PRAGMAs) on other backends.
func RequireSQLite(tb testing.TB) {
	tb.Helper()
	if driver := Driver(); driver != config.DatabaseDriverSQLite {
		tb.Skipf("requires sqlite; %s=%s", DriverEnv, driver)
	}
}
