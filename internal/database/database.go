package database

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/database/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func New(dsn string) (*sql.DB, error) {
	// Add timezone parameter to parse SQLite datetime as UTC.
	// This must apply to ALL databases including :memory: (test DBs)
	// to ensure test behavior matches production.
	if strings.Contains(dsn, "?") {
		dsn = dsn + "&_loc=UTC"
	} else {
		dsn = dsn + "?_loc=UTC"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("setting journal mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}
	// Set busy timeout to 5 seconds to avoid SQLITE_BUSY errors
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("setting busy timeout: %w", err)
	}
	// Limit to 1 open connection to prevent concurrent write conflicts
	db.SetMaxOpenConns(1)

	// Run migrations
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("setting dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}
