package database

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
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

	if err := enableIncrementalVacuum(db); err != nil {
		db.Close()
		return nil, err
	}

	// Run migrations
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("setting dialect: %w", err)
	}
	if err := goose.Up(db, ".", goose.WithAllowMissing()); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

// enableIncrementalVacuum turns on incremental auto-vacuum so freed pages can be
// released back to the OS by the background reclaimer. The mode is stored in the
// database header and can only be changed from "none" by a full VACUUM, so this
// runs at most once per database. On a new (empty) database the VACUUM is
// instant; on an existing large database it rebuilds the file and can take
// minutes.
func enableIncrementalVacuum(db *sql.DB) error {
	var mode int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return fmt.Errorf("reading auto_vacuum mode: %w", err)
	}
	if mode == 2 { // already INCREMENTAL
		return nil
	}
	if _, err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return fmt.Errorf("setting auto_vacuum mode: %w", err)
	}

	var freelist int
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&freelist); err != nil {
		return fmt.Errorf("reading freelist: %w", err)
	}
	if freelist > 1000 {
		applog.Infof("database: rebuilding to enable incremental vacuum (%d free pages, this may take several minutes)", freelist)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuuming database: %w", err)
	}
	return nil
}
