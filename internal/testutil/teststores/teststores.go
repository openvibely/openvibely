// Package teststores opens the persistence backend for tests, selected by
// TEST_DATABASE_DRIVER the same way DATABASE_DRIVER selects it at startup.
package teststores

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/sqlitefixtures"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
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
	countIO     bool
	broadcaster *events.Broadcaster
}

type Option func(*options)

// WithIOCounting instruments real backend requests and data read by the driver.
func WithIOCounting() Option {
	return func(o *options) { o.countIO = true }
}

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
	IO       storefixture.IOCounter
	Fixtures storefixture.Fixtures
	persistence.Stores
}

// New opens a fresh database for the selected backend and builds its stores.
func New(tb testing.TB, opts ...Option) *Env {
	tb.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	switch driver := Driver(); driver {
	case config.DatabaseDriverSQLite:
		var db *sql.DB
		var counter storefixture.IOCounter
		if o.countIO {
			var sqliteCounter *testutil.SQLStatementCounter
			db, sqliteCounter = testutil.NewStatementCountingTestDB(tb)
			counter = sqlitefixtures.NewIOCounter(sqliteCounter)
		} else {
			db = testutil.NewTestDB(tb)
		}
		return &Env{Fixtures: sqlitefixtures.New(db), Stores: sqlite.NewStores(db, o.broadcaster), IO: counter}
	default:
		tb.Fatalf("unsupported %s %q (supported: %s)", DriverEnv, driver, config.DatabaseDriverSQLite)
		return nil
	}
}

// RequireSQLite skips tests that depend on SQLite internals (statement
// counting, file-backed topology, triggers, PRAGMAs) on other backends.
func RequireSQLite(tb testing.TB) {
	tb.Helper()
	if driver := Driver(); driver != config.DatabaseDriverSQLite {
		tb.Skipf("requires sqlite; %s=%s", DriverEnv, driver)
	}
}

// ClearLLMConfigs deletes every model config through the store.
func ClearLLMConfigs(tb testing.TB, s persistence.LLMConfigStore) {
	tb.Helper()
	ctx := context.Background()
	configs, err := s.List(ctx)
	if err != nil {
		tb.Fatalf("list model configs: %v", err)
	}
	for _, c := range configs {
		if err := s.Delete(ctx, c.ID); err != nil {
			tb.Fatalf("delete model config %s: %v", c.ID, err)
		}
	}
}

// ClearAgents deletes every agent definition, including archived ones, through the store.
func ClearAgents(tb testing.TB, s persistence.AgentStore) {
	tb.Helper()
	ctx := context.Background()
	agents, err := s.ListIncludingArchived(ctx)
	if err != nil {
		tb.Fatalf("list agents: %v", err)
	}
	for _, a := range agents {
		if err := s.Delete(ctx, a.ID); err != nil {
			tb.Fatalf("delete agent %s: %v", a.ID, err)
		}
	}
}
