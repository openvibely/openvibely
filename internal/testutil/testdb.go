package testutil

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	"modernc.org/sqlite"
)

var (
	schemaOnce       sync.Once
	cachedTemplate   []byte
	schemaErr        error
	countingDriverID atomic.Uint64
)

// SQLStatementCounter records complete database/sql query and exec operations.
// Counting can be disabled around timed benchmarks to avoid measurement noise.
type SQLStatementCounter struct {
	mu                sync.Mutex
	enabled           bool
	statements        []string
	observer          func(context.Context, string)
	rowsCloseObserver func(context.Context, string)
}

func (c *SQLStatementCounter) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
}

func (c *SQLStatementCounter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = nil
}

func (c *SQLStatementCounter) Statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.statements...)
}

func (c *SQLStatementCounter) SetObserver(observer func(context.Context, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observer = observer
}

// SetRowsCloseObserver installs a test-only callback invoked immediately before
// an instrumented query releases its database connection through rows.Close.
// The callback is not installed into rows that were created before this method
// was called, and callers must clear it after use.
func (c *SQLStatementCounter) SetRowsCloseObserver(observer func(context.Context, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rowsCloseObserver = observer
}

func (c *SQLStatementCounter) hasRowsCloseObserver() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rowsCloseObserver != nil
}

func (c *SQLStatementCounter) recordRowsClose(ctx context.Context, query string) {
	c.mu.Lock()
	observer := c.rowsCloseObserver
	c.mu.Unlock()
	if observer != nil {
		observer(ctx, query)
	}
}

func (c *SQLStatementCounter) record(ctx context.Context, query string) {
	c.mu.Lock()
	if c.enabled {
		c.statements = append(c.statements, strings.TrimSpace(query))
	}
	observer := c.observer
	c.mu.Unlock()
	if observer != nil {
		observer(ctx, query)
	}
}

type statementCountingDriver struct {
	inner   driver.Driver
	counter *SQLStatementCounter
}

func (d *statementCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &statementCountingConn{Conn: conn, counter: d.counter}, nil
}

type statementCountingConn struct {
	driver.Conn
	counter *SQLStatementCounter
}

func (c *statementCountingConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &statementCountingStmt{Stmt: stmt, query: query, counter: c.counter}, nil
}

func (c *statementCountingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if conn, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := conn.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &statementCountingStmt{Stmt: stmt, query: query, counter: c.counter}, nil
	}
	return c.Prepare(query)
}

func (c *statementCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	conn, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.counter.record(ctx, query)
	return conn.ExecContext(ctx, query, args)
}

func (c *statementCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	conn, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.counter.record(ctx, query)
	rows, err := conn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if !c.counter.hasRowsCloseObserver() {
		return rows, nil
	}
	return &statementCountingRows{Rows: rows, ctx: ctx, query: query, counter: c.counter}, nil
}

func (c *statementCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if conn, ok := c.Conn.(driver.ConnBeginTx); ok {
		return conn.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *statementCountingConn) Ping(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.Pinger); ok {
		return conn.Ping(ctx)
	}
	return nil
}

func (c *statementCountingConn) CheckNamedValue(value *driver.NamedValue) error {
	if conn, ok := c.Conn.(driver.NamedValueChecker); ok {
		return conn.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *statementCountingConn) ResetSession(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.SessionResetter); ok {
		return conn.ResetSession(ctx)
	}
	return nil
}

func (c *statementCountingConn) IsValid() bool {
	if conn, ok := c.Conn.(driver.Validator); ok {
		return conn.IsValid()
	}
	return true
}

func (c *statementCountingConn) Serialize() ([]byte, error) {
	return c.Conn.(serializer).Serialize()
}

func (c *statementCountingConn) Deserialize(data []byte) error {
	return c.Conn.(serializer).Deserialize(data)
}

type statementCountingStmt struct {
	driver.Stmt
	query   string
	counter *SQLStatementCounter
}

func (s *statementCountingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.counter.record(context.Background(), s.query)
	return s.Stmt.Exec(args)
}

func (s *statementCountingStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.counter.record(context.Background(), s.query)
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	if !s.counter.hasRowsCloseObserver() {
		return rows, nil
	}
	return &statementCountingRows{Rows: rows, ctx: context.Background(), query: s.query, counter: s.counter}, nil
}

func (s *statementCountingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if stmt, ok := s.Stmt.(driver.StmtExecContext); ok {
		s.counter.record(ctx, s.query)
		return stmt.ExecContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

func (s *statementCountingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if stmt, ok := s.Stmt.(driver.StmtQueryContext); ok {
		s.counter.record(ctx, s.query)
		rows, err := stmt.QueryContext(ctx, args)
		if err != nil {
			return nil, err
		}
		if !s.counter.hasRowsCloseObserver() {
			return rows, nil
		}
		return &statementCountingRows{Rows: rows, ctx: ctx, query: s.query, counter: s.counter}, nil
	}
	return nil, driver.ErrSkip
}

type statementCountingRows struct {
	driver.Rows
	ctx       context.Context
	query     string
	counter   *SQLStatementCounter
	closeOnce sync.Once
}

func (r *statementCountingRows) Close() error {
	r.closeOnce.Do(func() {
		r.counter.recordRowsClose(r.ctx, r.query)
	})
	return r.Rows.Close()
}

func init() {
	// Set GO_TESTING environment variable to prevent real external provider calls during tests.
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

	db := buildTestDB(t, "sqlite")
	t.Cleanup(func() { db.Close() })
	return db
}

// NewStatementCountingTestDB creates a normal isolated SQLite test fixture and
// exposes statement instrumentation for complete-path tests and benchmarks.
func NewStatementCountingTestDB(t testing.TB) (*sql.DB, *SQLStatementCounter) {
	t.Helper()

	counter := &SQLStatementCounter{}
	driverName := fmt.Sprintf("sqlite_statement_counter_%d", countingDriverID.Add(1))
	sql.Register(driverName, &statementCountingDriver{inner: &sqlite.Driver{}, counter: counter})
	db := buildTestDB(t, driverName)
	t.Cleanup(func() { db.Close() })
	return db, counter
}

// buildTestDB constructs a fresh isolated fixture and fails tb on error. It does
// not register cleanup; callers own closing the returned database. NewTestDB
// wraps it with a t.Cleanup close, while the benchmark closes each fixture
// explicitly to measure per-iteration create/close cost accurately.
func buildTestDB(tb testing.TB, driverName string) *sql.DB {
	tb.Helper()

	schemaOnce.Do(initSchema)
	if schemaErr != nil {
		tb.Fatalf("failed to initialize test schema: %v", schemaErr)
	}

	// Open a raw SQLite connection (no migrations).
	dsn := ":memory:?_loc=UTC"
	db, err := sql.Open(driverName, dsn)
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
		INSERT INTO agent_configs (name, provider, model, api_key, is_default, auth_method)
			VALUES ('Test Default Agent', 'test', 'test-model', 'test-key', 1, 'api_key')
	`); err != nil {
		tb.Fatalf("failed to seed default test agent: %v", err)
	}
}
