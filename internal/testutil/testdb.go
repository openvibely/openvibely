package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	_ "modernc.org/sqlite"
)

var (
	schemaOnce     sync.Once
	cachedTemplate []byte
	schemaErr      error
)

func init() {
	// Set GO_TESTING environment variable to prevent real external API/CLI calls during tests
	os.Setenv("GO_TESTING", "1")
}

// serializer is implemented by the modernc.org/sqlite driver connection. It
// exposes SQLite's native serialize/deserialize mechanism, which lets us clone
// a fully migrated in-memory database as raw bytes instead of replaying the
// full schema-and-seed SQL batch for every fixture.
type serializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// initSchema runs goose migrations once and captures the resulting migrated
// database (schema + seed data) as a native SQLite serialization. Each fresh
// fixture is then produced by deserializing this immutable template into an
// isolated in-memory database, which is dramatically cheaper than reparsing and
// re-executing hundreds of DDL and seed statements.
func initSchema() {
	// Create a temporary DB and run all migrations.
	db, err := database.New(":memory:")
	if err != nil {
		schemaErr = fmt.Errorf("init schema: %w", err)
		return
	}
	defer db.Close()

	conn, err := db.Conn(context.Background())
	if err != nil {
		schemaErr = fmt.Errorf("acquire template connection: %w", err)
		return
	}
	defer conn.Close()

	if err := conn.Raw(func(driverConn any) error {
		s, ok := driverConn.(serializer)
		if !ok {
			return fmt.Errorf("sqlite driver connection does not support serialization")
		}
		buf, err := s.Serialize()
		if err != nil {
			return err
		}
		if len(buf) == 0 {
			return fmt.Errorf("serialized template is empty")
		}
		// Copy the buffer: the driver frees its backing memory when Serialize
		// returns, so retain an independent copy for the process lifetime.
		cachedTemplate = append([]byte(nil), buf...)
		return nil
	}); err != nil {
		schemaErr = fmt.Errorf("serialize template: %w", err)
		return
	}
}

// NewTestDB creates a fresh, fully isolated in-memory SQLite database with all
// migrations applied. It runs goose migrations only once per test process and
// clones the resulting database template for each subsequent call using
// SQLite's native deserialize mechanism, which is dramatically faster than
// replaying the schema-and-seed SQL for every fixture.
// It automatically closes the database when the test finishes.
func NewTestDB(t testing.TB) *sql.DB {
	t.Helper()

	db := buildTestDB(t)
	t.Cleanup(func() { db.Close() })
	return db
}

// buildTestDB constructs a fresh isolated fixture and fails tb on error. It does
// not register cleanup; callers own closing the returned database. NewTestDB
// wraps it with a t.Cleanup close, while the benchmark closes each fixture
// explicitly to measure per-iteration create/close cost accurately.
func buildTestDB(tb testing.TB) *sql.DB {
	tb.Helper()

	schemaOnce.Do(initSchema)
	if schemaErr != nil {
		tb.Fatalf("failed to initialize test schema: %v", schemaErr)
	}

	// Open a raw SQLite connection (no migrations).
	dsn := ":memory:?_loc=UTC"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		tb.Fatalf("failed to open test database: %v", err)
	}

	// Pin the pool to a single connection before doing any work so the
	// deserialized database and the connection-local pragmas below all bind to
	// the same underlying connection, matching production behavior.
	db.SetMaxOpenConns(1)

	// Clone the migrated template into this database's main schema.
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		tb.Fatalf("failed to acquire test connection: %v", err)
	}
	if err := conn.Raw(func(driverConn any) error {
		s, ok := driverConn.(serializer)
		if !ok {
			return fmt.Errorf("sqlite driver connection does not support deserialization")
		}
		return s.Deserialize(cachedTemplate)
	}); err != nil {
		conn.Close()
		db.Close()
		tb.Fatalf("failed to clone test schema: %v", err)
	}
	conn.Close()

	// Apply the same connection-local pragmas as production. These are not part
	// of the serialized database, so they must be (re)applied on the pooled
	// connection after deserialization.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			tb.Fatalf("failed to set pragma: %v", err)
		}
	}

	seedTestDefaultAgent(tb, db)

	return db
}

func seedTestDefaultAgent(tb testing.TB, db *sql.DB) {
	tb.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_configs WHERE is_default = 1`).Scan(&count); err != nil {
		tb.Fatalf("failed to count default test agents: %v", err)
	}
	if count > 0 {
		return
	}

	if _, err := db.Exec(`
		INSERT INTO agent_configs (name, provider, model, is_default, auth_method)
		VALUES ('Test Default Agent', 'anthropic', 'claude-sonnet-4-5-20250929', 1, 'cli')
	`); err != nil {
		tb.Fatalf("failed to seed default test agent: %v", err)
	}
}
