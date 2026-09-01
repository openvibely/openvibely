package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/database/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const (
	fileDatabasePoolSize       = 2
	fileDatabaseReaderPoolSize = 1
	sqliteBusyTimeoutMS        = 5000
)

type Connections struct {
	Reader *sql.DB
	Writer *sql.DB
}

func (c *Connections) Close() error {
	if c == nil {
		return nil
	}
	var firstErr error
	if c.Reader != nil && c.Reader != c.Writer {
		firstErr = c.Reader.Close()
	}
	if c.Writer != nil {
		if err := c.Writer.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func NewReadWrite(dsn string) (*Connections, error) {
	writer, err := New(dsn)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	_, inMemory, err := configureSQLiteDSN(dsn)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	if inMemory {
		return &Connections{Reader: writer, Writer: writer}, nil
	}

	readerDSN, _, err := configureSQLiteDSNForRole(dsn, true)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	reader, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("opening read database: %w", err)
	}
	reader.SetMaxOpenConns(fileDatabaseReaderPoolSize)
	reader.SetMaxIdleConns(fileDatabaseReaderPoolSize)
	if err := reader.Ping(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("opening read database connection: %w", err)
	}
	return &Connections{Reader: reader, Writer: writer}, nil
}

func New(dsn string) (*sql.DB, error) {
	return newSQLiteDatabase(dsn, initializeSQLite)
}

func newSQLiteDatabase(dsn string, initialize func(*sql.DB) error) (*sql.DB, error) {
	configuredDSN, inMemory, err := configureSQLiteDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", configuredDSN)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Bootstrap, auto-vacuum initialization, and migrations are deliberately
	// serialized. The pool expands only after startup has completed successfully.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initialize(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if !inMemory {
		// Two is intentionally small: benchmarks show meaningful concurrent-read
		// gains without creating excess connections competing for SQLite's writer.
		db.SetMaxOpenConns(fileDatabasePoolSize)
		db.SetMaxIdleConns(fileDatabasePoolSize)
	}
	return db, nil
}

func initializeSQLite(db *sql.DB) error {
	// WAL permits readers on other physical connections while SQLite's single
	// writer commits. Connection-local settings are enforced by the DSN below.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("setting journal mode: %w", err)
	}

	if err := enableIncrementalVacuum(db); err != nil {
		return err
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("setting dialect: %w", err)
	}
	if err := goose.Up(db, ".", goose.WithAllowMissing()); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

func configureSQLiteDSN(dsn string) (configured string, inMemory bool, err error) {
	return configureSQLiteDSNForRole(dsn, false)
}

func configureSQLiteDSNForRole(dsn string, queryOnly bool) (configured string, inMemory bool, err error) {
	base, rawQuery, found := strings.Cut(dsn, "?")
	inMemory = base == ":memory:"
	values := make(url.Values)
	if found {
		values, err = url.ParseQuery(rawQuery)
		if err != nil {
			return "", false, fmt.Errorf("parsing database DSN query: %w", err)
		}
	}

	// _pragma is applied by modernc.org/sqlite whenever it opens a physical
	// connection. Preserve unrelated caller PRAGMAs while enforcing invariants.
	pragmas := values["_pragma"][:0]
	for _, pragma := range values["_pragma"] {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pragma), " ", ""))
		if strings.HasPrefix(normalized, "foreign_keys") || strings.HasPrefix(normalized, "busy_timeout") || strings.HasPrefix(normalized, "query_only") {
			continue
		}
		pragmas = append(pragmas, pragma)
	}
	values["_pragma"] = append(pragmas, "foreign_keys(1)", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	if queryOnly {
		values["_pragma"] = append(values["_pragma"], "query_only(1)")
	}
	values.Set("_loc", "UTC")
	return base + "?" + values.Encode(), inMemory, nil
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
	applog.Infof("database: rebuilding to enable incremental vacuum (%d free pages, this may take several minutes for large databases)", freelist)
	if _, err := db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuuming database: %w", err)
	}
	return nil
}
