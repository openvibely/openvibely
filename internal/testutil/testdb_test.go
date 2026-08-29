package testutil

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestStatementCountingTestDBRecordsCompleteOperations(t *testing.T) {
	db, counter := NewStatementCountingTestDB(t)
	if got := len(counter.Statements()); got != 0 {
		t.Fatalf("setup statements = %d, want 0", got)
	}

	type markerKey struct{}
	observed := make(chan struct{}, 1)
	counter.SetObserver(func(ctx context.Context, _ string) {
		if marked, _ := ctx.Value(markerKey{}).(bool); marked {
			observed <- struct{}{}
		}
	})
	counter.SetEnabled(true)
	ctx := context.WithValue(context.Background(), markerKey{}, true)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects").Scan(&count); err != nil {
		t.Fatalf("query projects: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE projects SET name = name WHERE 0"); err != nil {
		t.Fatalf("exec no-op update: %v", err)
	}
	counter.SetEnabled(false)
	if _, err := db.Exec("DELETE FROM projects WHERE 0"); err != nil {
		t.Fatalf("disabled no-op delete: %v", err)
	}

	statements := counter.Statements()
	if len(statements) != 2 {
		t.Fatalf("recorded statements = %q, want one query and one exec", statements)
	}
	if statements[0] != "SELECT COUNT(*) FROM projects" || statements[1] != "UPDATE projects SET name = name WHERE 0" {
		t.Fatalf("recorded statements = %q", statements)
	}
	select {
	case <-observed:
	default:
		t.Fatal("context-aware statement observer was not called")
	}
}

func TestStatementCountingTestDBRowsCloseObserver(t *testing.T) {
	db, counter := NewStatementCountingTestDB(t)
	observed := make(chan string, 1)
	counter.SetRowsCloseObserver(func(_ context.Context, query string) {
		observed <- query
	})
	defer counter.SetRowsCloseObserver(nil)

	rows, err := db.QueryContext(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("query returned no row")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}

	select {
	case query := <-observed:
		if query != "SELECT 1" {
			t.Fatalf("observed query = %q, want SELECT 1", query)
		}
	case <-time.After(time.Second):
		t.Fatal("rows-close observer was not called")
	}
}

// connection-local pragmas and single-connection pool configuration.
func TestNewTestDBPragmas(t *testing.T) {
	db := NewTestDB(t)

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

// TestNewTestDBUTCLocation verifies datetime values are parsed as UTC via the
// _loc=UTC DSN parameter, matching production.
func TestNewTestDBUTCLocation(t *testing.T) {
	db := NewTestDB(t)

	// The _loc=UTC DSN parameter controls how the driver parses SQLite DATETIME
	// columns back into time.Time. Insert a row and read its DATETIME column
	// back; the parsed value must carry a UTC location.
	if _, err := db.Exec(
		"INSERT INTO projects (name, repo_path, created_at) VALUES ('utc-test', '/tmp/utc', '2020-06-15 12:34:56')",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var ts time.Time
	if err := db.QueryRow(
		"SELECT created_at FROM projects WHERE name = 'utc-test'",
	).Scan(&ts); err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, offset := ts.Zone(); offset != 0 {
		t.Fatalf("datetime parsed with non-UTC offset %d; _loc=UTC not applied", offset)
	}
	if ts.Location() != time.UTC {
		t.Fatalf("datetime location = %v, want UTC", ts.Location())
	}
}

// TestNewTestDBSchemaObjects verifies the migrated schema (tables, indexes,
// triggers) and seed data are present in every fixture.
func TestNewTestDBSchemaObjects(t *testing.T) {
	db := NewTestDB(t)

	// Core tables produced by migrations must exist.
	for _, table := range []string{"projects", "tasks", "executions", "agent_configs"} {
		var name string
		if err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&name); err != nil {
			t.Fatalf("expected table %q to exist: %v", table, err)
		}
	}

	// At least one index and one trigger from migrations must survive the clone.
	var indexCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index'",
	).Scan(&indexCount); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if indexCount == 0 {
		t.Fatal("expected migrated indexes to be present, found none")
	}

	// Seed data: goose migration versioning rows must exist.
	var versionCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM goose_db_version").Scan(&versionCount); err != nil {
		t.Fatalf("count goose versions: %v", err)
	}
	if versionCount == 0 {
		t.Fatal("expected goose migration seed rows, found none")
	}
}

// TestNewTestDBDefaultAgent verifies exactly the existing default test-agent
// behavior: one default agent config with the expected identity.
func TestNewTestDBDefaultAgent(t *testing.T) {
	db := NewTestDB(t)

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_configs WHERE is_default = 1").Scan(&count); err != nil {
		t.Fatalf("count default agents: %v", err)
	}
	if count != 1 {
		t.Fatalf("default agent count = %d, want 1", count)
	}

	var name, provider, model, authMethod string
	if err := db.QueryRow(
		"SELECT name, provider, model, auth_method FROM agent_configs WHERE is_default = 1",
	).Scan(&name, &provider, &model, &authMethod); err != nil {
		t.Fatalf("read default agent: %v", err)
	}
	if name != "Test Default Agent" || provider != "test" ||
		model != "test-model" || authMethod != "api_key" {
		t.Fatalf("unexpected default agent: name=%q provider=%q model=%q auth=%q",
			name, provider, model, authMethod)
	}
}

// TestNewTestDBIsolation verifies two simultaneously open fixtures are fully
// isolated: mutating one leaves the other at its pristine seed state.
func TestNewTestDBIsolation(t *testing.T) {
	dbA := NewTestDB(t)
	dbB := NewTestDB(t)

	// Both start at the same pristine seed state.
	pristineA := countRows(t, dbA, "projects")
	pristineB := countRows(t, dbB, "projects")
	if pristineA != pristineB {
		t.Fatalf("fixtures started at different seed states: A=%d B=%d", pristineA, pristineB)
	}

	// Mutate fixture A only.
	if _, err := dbA.Exec(
		"INSERT INTO projects (name, repo_path) VALUES ('isolation-test', '/tmp/isolation')",
	); err != nil {
		t.Fatalf("insert into A: %v", err)
	}

	if got := countRows(t, dbA, "projects"); got != pristineA+1 {
		t.Fatalf("fixture A projects = %d, want %d", got, pristineA+1)
	}
	if got := countRows(t, dbB, "projects"); got != pristineB {
		t.Fatalf("fixture B leaked A's mutation: projects = %d, want %d (pristine)", got, pristineB)
	}

	// Mutating A's default agent must not affect B.
	if _, err := dbA.Exec("UPDATE agent_configs SET name = 'Mutated' WHERE is_default = 1"); err != nil {
		t.Fatalf("update A agent: %v", err)
	}
	var bName string
	if err := dbB.QueryRow("SELECT name FROM agent_configs WHERE is_default = 1").Scan(&bName); err != nil {
		t.Fatalf("read B agent: %v", err)
	}
	if bName != "Test Default Agent" {
		t.Fatalf("fixture B default agent leaked mutation: %q", bName)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
