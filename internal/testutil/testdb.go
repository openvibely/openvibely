package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	_ "modernc.org/sqlite"
)

var (
	schemaOnce sync.Once
	cachedDDL  string
	schemaErr  error
)

func init() {
	// Set GO_TESTING environment variable to prevent real external API/CLI calls during tests
	os.Setenv("GO_TESTING", "1")
}

// initSchema runs goose migrations once and captures the resulting schema + seed data.
func initSchema() {
	// Create a temporary DB, run all migrations, dump everything.
	db, err := database.New(":memory:")
	if err != nil {
		schemaErr = fmt.Errorf("init schema: %w", err)
		return
	}
	defer db.Close()

	// Dump all CREATE statements (tables, indexes, triggers).
	rows, err := db.Query("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		schemaErr = fmt.Errorf("dump schema: %w", err)
		return
	}
	defer rows.Close()

	var stmts []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			schemaErr = fmt.Errorf("scan schema: %w", err)
			return
		}
		stmts = append(stmts, s+";")
	}
	if err := rows.Err(); err != nil {
		schemaErr = fmt.Errorf("iterate schema: %w", err)
		return
	}

	// Dump seed data from all tables (migrations insert default project, goose versions, etc).
	tableRows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		schemaErr = fmt.Errorf("list tables: %w", err)
		return
	}
	defer tableRows.Close()

	var tables []string
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			schemaErr = fmt.Errorf("scan table name: %w", err)
			return
		}
		tables = append(tables, name)
	}

	var dataStmts []string
	for _, table := range tables {
		dRows, err := dumpTableData(db, table)
		if err != nil {
			schemaErr = fmt.Errorf("dump table %s: %w", table, err)
			return
		}
		dataStmts = append(dataStmts, dRows...)
	}

	cachedDDL = strings.Join(stmts, "\n") + "\n" + strings.Join(dataStmts, "\n")
}

// dumpTableData generates INSERT statements for all rows in a table.
func dumpTableData(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var inserts []string
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		var vals []string
		for _, v := range values {
			switch val := v.(type) {
			case nil:
				vals = append(vals, "NULL")
			case int64:
				vals = append(vals, fmt.Sprintf("%d", val))
			case float64:
				vals = append(vals, fmt.Sprintf("%g", val))
			case bool:
				if val {
					vals = append(vals, "1")
				} else {
					vals = append(vals, "0")
				}
			case []byte:
				vals = append(vals, fmt.Sprintf("'%s'", strings.ReplaceAll(string(val), "'", "''")))
			case string:
				vals = append(vals, fmt.Sprintf("'%s'", strings.ReplaceAll(val, "'", "''")))
			default:
				vals = append(vals, fmt.Sprintf("'%v'", v))
			}
		}
		inserts = append(inserts, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
			table, strings.Join(cols, ", "), strings.Join(vals, ", ")))
	}
	return inserts, rows.Err()
}

// NewTestDB creates a fresh in-memory SQLite database with all migrations applied.
// It runs goose migrations only once per test process and replays the cached schema
// for subsequent calls, which is dramatically faster.
// It automatically closes the database when the test finishes.
func NewTestDB(t *testing.T) *sql.DB {
	t.Helper()

	schemaOnce.Do(initSchema)
	if schemaErr != nil {
		t.Fatalf("failed to initialize test schema: %v", schemaErr)
	}

	// Open a raw SQLite connection (no migrations).
	dsn := ":memory:?_loc=UTC"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Apply the same pragmas as production.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			t.Fatalf("failed to set pragma: %v", err)
		}
	}
	db.SetMaxOpenConns(1)

	// Replay the cached schema + seed data.
	if _, err := db.Exec(cachedDDL); err != nil {
		db.Close()
		t.Fatalf("failed to apply cached schema: %v", err)
	}
	seedTestDefaultAgent(t, db)

	t.Cleanup(func() { db.Close() })
	return db
}

func seedTestDefaultAgent(t *testing.T, db *sql.DB) {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_configs WHERE is_default = 1`).Scan(&count); err != nil {
		t.Fatalf("failed to count default test agents: %v", err)
	}
	if count > 0 {
		return
	}

	if _, err := db.Exec(`
		INSERT INTO agent_configs (name, provider, model, is_default, auth_method)
		VALUES ('Test Default Agent', 'anthropic', 'claude-sonnet-4-5-20250929', 1, 'cli')
	`); err != nil {
		t.Fatalf("failed to seed default test agent: %v", err)
	}
}
