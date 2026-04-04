package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNew_InMemory(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Verify WAL mode
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	// In-memory databases may report "memory" instead of "wal"
	if journalMode != "wal" && journalMode != "memory" {
		t.Errorf("expected journal_mode=wal or memory, got %q", journalMode)
	}

	// Verify foreign keys enabled
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("querying foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("expected foreign_keys=1, got %d", fk)
	}

	// Verify busy timeout
	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("querying busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("expected busy_timeout=5000, got %d", timeout)
	}

	// Verify migrations ran - check tables exist
	tables := []string{"projects", "tasks", "agent_configs", "schedules", "executions", "worker_settings"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found after migrations: %v", table, err)
		}
	}

	// Verify default project was seeded
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil {
		t.Fatalf("counting projects: %v", err)
	}
	if count < 1 {
		t.Error("expected at least 1 default project")
	}

	// Fresh baseline should not seed a default agent config.
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_configs WHERE is_default=1").Scan(&count); err != nil {
		t.Fatalf("counting default agents: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 default agents, got %d", count)
	}

	// Verify max open connections is 1
	if db.Stats().MaxOpenConnections != 1 {
		t.Errorf("expected MaxOpenConnections=1, got %d", db.Stats().MaxOpenConnections)
	}
}

func TestNew_TimestampsAreUTC(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Insert a record with a timestamp
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title) VALUES ('test-task', 'default', 'Test Task')`)
	if err != nil {
		t.Fatalf("failed to insert test task: %v", err)
	}

	// Read back the created_at timestamp
	var createdAt time.Time
	err = db.QueryRow(`SELECT created_at FROM tasks WHERE id = 'test-task'`).Scan(&createdAt)
	if err != nil {
		t.Fatalf("failed to query created_at: %v", err)
	}

	// Verify the timestamp is in UTC location
	if createdAt.Location() != time.UTC {
		t.Errorf("expected timestamp location to be UTC, got %v", createdAt.Location())
	}

	// Verify the timestamp is reasonable (within the last minute and not in the future)
	now := time.Now().UTC()
	diff := now.Sub(createdAt)
	if diff < 0 || diff > time.Minute {
		t.Errorf("timestamp %v is not within the last minute of current time %v (diff: %v)", createdAt, now, diff)
	}
}

func TestNew_FreshOnDiskDB_DoesNotSeedLegacyDefaultAgent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("New(%q) failed: %v", dbPath, err)
	}
	defer db.Close()

	var defaultAgents int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_configs WHERE is_default=1").Scan(&defaultAgents); err != nil {
		t.Fatalf("counting default agents: %v", err)
	}
	if defaultAgents != 0 {
		t.Fatalf("expected 0 default agents on fresh on-disk DB, got %d", defaultAgents)
	}

	var legacyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_configs WHERE name='Claude Max'").Scan(&legacyCount); err != nil {
		t.Fatalf("counting legacy seeded agent rows: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("expected no legacy seeded Claude Max rows, got %d", legacyCount)
	}
}
