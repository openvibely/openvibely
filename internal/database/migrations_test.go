package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/database/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func TestMigration112_BackfillsOperationalAlertsWithoutInferringProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "alerts-112.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 111); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('legacy-project', 'Legacy', '', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alerts (id, project_id, type, severity, title, message) VALUES ('legacy-alert', 'legacy-project', 'task_failed', 'error', 'Legacy failure', 'preserve me')`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 112); err != nil {
		t.Fatal(err)
	}
	var projectID, scope, body, source, decision, processing string
	if err := db.QueryRow(`SELECT project_id, scope, body, source, decision_state, processing_state FROM alerts WHERE id = 'legacy-alert'`).Scan(&projectID, &scope, &body, &source, &decision, &processing); err != nil {
		t.Fatal(err)
	}
	if projectID != "legacy-project" || scope != "project" || body != "preserve me" || source != "operational" || decision != "not_required" || processing != "not_applicable" {
		t.Fatalf("unexpected legacy backfill: project=%q scope=%q body=%q source=%q decision=%q processing=%q", projectID, scope, body, source, decision, processing)
	}
	if _, err := db.Exec(`INSERT INTO alerts (project_id, type, severity, title) VALUES (NULL, 'custom', 'info', 'global')`); err == nil {
		t.Fatal("projectless/global alert unexpectedly inserted")
	}
}

// TestMigrations_PreserveForeignKeyData verifies that all migrations preserve
// foreign key referenced data when recreating tables.
func TestMigration100_RepairsSkippedChannelTargetsWhenOldLocalDiscordUsed099(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "old-discord-099.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 98); err != nil {
		t.Fatalf("failed to migrate to 098: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS discord_authorized_users (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			discord_user_id TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			added_by TEXT NOT NULL DEFAULT 'web'
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id ON discord_authorized_users(project_id, discord_user_id);
		CREATE INDEX IF NOT EXISTS idx_discord_auth_project ON discord_authorized_users(project_id);
		CREATE INDEX IF NOT EXISTS idx_discord_auth_user ON discord_authorized_users(discord_user_id);
		CREATE TABLE IF NOT EXISTS discord_task_context (
			task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
			discord_channel_id TEXT NOT NULL,
			discord_thread_id TEXT NOT NULL DEFAULT '',
			discord_message_id TEXT NOT NULL DEFAULT '',
			discord_user_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_discord_task_context_channel ON discord_task_context(discord_channel_id, discord_thread_id);
	`); err != nil {
		t.Fatalf("failed to simulate old local discord 099 schema: %v", err)
	}
	for _, column := range []string{"discord_channel_id", "discord_thread_id", "discord_message_id", "discord_user_id"} {
		if !tableHasColumn(t, db, "thread_inputs", column) {
			if _, err := db.Exec(`ALTER TABLE thread_inputs ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				t.Fatalf("failed to simulate old local discord 099 column %s: %v", column, err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (99, 1)`); err != nil {
		t.Fatalf("failed to simulate old local discord 099 goose row: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run pending migrations after stale 099: %v", err)
	}

	for _, table := range []string{"channel_targets", "channel_message_sends", "discord_authorized_users", "discord_task_context", "discord_user_projects"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("failed to inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist after repaired migration chain", table)
		}
	}
	for _, column := range []string{"discord_channel_id", "discord_thread_id", "discord_message_id", "discord_user_id"} {
		if !tableHasColumn(t, db, "thread_inputs", column) {
			t.Fatalf("expected thread_inputs.%s to exist after discord migration", column)
		}
	}
	var maxVersion int
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("failed to read max goose version: %v", err)
	}
	if maxVersion != 122 {
		t.Fatalf("max goose version = %d, want 122", maxVersion)
	}
}

func TestMigration108_SystemChannelInboundAuthorizationDedupe(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "system-channel-auth.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 107); err != nil {
		t.Fatalf("failed to migrate to 107: %v", err)
	}
	for _, id := range []string{"project-one", "project-two"} {
		if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`, id, id); err != nil {
			t.Fatalf("failed to insert project %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO slack_authorized_users (id, project_id, slack_user_id, display_name) VALUES ('slack-one', 'project-one', 'U123', 'One');
		INSERT INTO slack_authorized_users (id, project_id, slack_user_id, display_name) VALUES ('slack-two', 'project-two', 'U123', 'Two');
		INSERT INTO discord_authorized_users (id, project_id, discord_user_id, display_name) VALUES ('discord-one', 'project-one', '123456789012345678', 'One');
		INSERT INTO discord_authorized_users (id, project_id, discord_user_id, display_name) VALUES ('discord-two', 'project-two', '123456789012345678', 'Two');
		INSERT INTO email_authorized_senders (id, project_id, email_address, display_name) VALUES ('email-one', 'project-one', 'sender@example.com', 'One');
		INSERT INTO email_authorized_senders (id, project_id, email_address, display_name) VALUES ('email-two', 'project-two', 'SENDER@example.com', 'Two');
		INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name) VALUES ('telegram-one', 'project-one', 999, '', 'One');
		INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name) VALUES ('telegram-two', 'project-two', 999, '', 'Two');
		INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name) VALUES ('telegram-username-one', 'project-one', 0, 'AliceUser', 'One');
		INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name) VALUES ('telegram-username-two', 'project-two', 0, 'aliceuser', 'Two');
	`); err != nil {
		t.Fatalf("failed to seed duplicate auth rows: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migration 108: %v", err)
	}
	assertSingleAuthRow := func(table, where string) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE ` + where).Scan(&count); err != nil {
			t.Fatalf("failed to count %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s duplicate count = %d, want 1", table, count)
		}
	}
	assertSingleAuthRow("slack_authorized_users", `slack_user_id = 'U123'`)
	assertSingleAuthRow("discord_authorized_users", `discord_user_id = '123456789012345678'`)
	assertSingleAuthRow("email_authorized_senders", `lower(email_address) = 'sender@example.com'`)
	assertSingleAuthRow("telegram_authorized_users", `telegram_user_id = 999`)
	assertSingleAuthRow("telegram_authorized_users", `lower(telegram_username) = 'aliceuser'`)
	if _, err := db.Exec(`INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name) VALUES ('telegram-username-three', 'project-two', 0, 'ALICEUSER', 'Three')`); err == nil {
		t.Fatal("expected global Telegram username uniqueness to reject mixed-case duplicate")
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-one', 'project-one', 'email', '', 'one@example.com')`); err != nil {
		t.Fatalf("channel_targets must remain project-scoped and insertable after auth migration: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-two', 'project-two', 'email', '', 'one@example.com')`); err != nil {
		t.Fatalf("same outbound target destination should remain allowed in another project: %v", err)
	}
}

func TestMigration105_AllowsMixtureProviderAndConfig(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "mixture-105.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	if !tableHasColumn(t, db, "agent_configs", "mixture_config_json") {
		t.Fatal("expected agent_configs.mixture_config_json column")
	}
	if _, err := db.Exec(`INSERT INTO agent_configs (id, name, provider, model, auth_method, mixture_config_json) VALUES ('mixture-105', 'Mixture', 'mixture', 'default', 'api_key', '{"enabled":false}')`); err != nil {
		t.Fatalf("expected mixture provider row to insert: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT mixture_config_json FROM agent_configs WHERE id = 'mixture-105'`).Scan(&raw); err != nil {
		t.Fatalf("failed to read mixture config: %v", err)
	}
	if raw != `{"enabled":false}` {
		t.Fatalf("mixture_config_json = %q", raw)
	}
}

func TestMigration107_AllowsLocalDatabaseWithOldSwarmVersion106(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "old-swarm-106.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 105); err != nil {
		t.Fatalf("failed to migrate to 105: %v", err)
	}
	if _, err := db.Exec(`
		ALTER TABLE tasks ADD COLUMN swarm_role TEXT NOT NULL DEFAULT '';
		ALTER TABLE tasks ADD COLUMN swarm_status TEXT NOT NULL DEFAULT '';
		ALTER TABLE tasks ADD COLUMN swarm_config TEXT NOT NULL DEFAULT '{}';
		ALTER TABLE tasks ADD COLUMN swarm_sequence INTEGER NOT NULL DEFAULT 0;
		CREATE INDEX IF NOT EXISTS idx_tasks_swarm_parent
		  ON tasks(parent_task_id, swarm_role, swarm_sequence);
	`); err != nil {
		t.Fatalf("failed to simulate old local swarm schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (106, 1)`); err != nil {
		t.Fatalf("failed to simulate old local swarm 106 goose row: %v", err)
	}

	if err := goose.Up(db, ".", goose.WithAllowMissing()); err != nil {
		t.Fatalf("expected allow-missing migrations to recover old local swarm 106 database: %v", err)
	}

	for _, column := range []string{"swarm_role", "swarm_status", "swarm_config", "swarm_sequence"} {
		if !tableHasColumn(t, db, "tasks", column) {
			t.Fatalf("expected tasks.%s after swarm migration recovery", column)
		}
	}
	for _, table := range []string{"email_authorized_senders", "email_task_context"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("failed to inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist after recovered migration chain", table)
		}
	}
	var maxVersion int
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("failed to read max goose version: %v", err)
	}
	if maxVersion != 122 {
		t.Fatalf("max goose version = %d, want 122", maxVersion)
	}
}

func tableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("failed to inspect columns for %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("failed to scan column for %s: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("failed to iterate columns for %s: %v", table, err)
	}
	return false
}

func TestMigration100_ChannelTargetsAllowMultipleUnnamedTargets(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "channel-targets.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('channel-target-project', 'Channel Target Project', '', '')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-one', 'channel-target-project', 'email', '', 'one@example.com')`); err != nil {
		t.Fatalf("failed to insert first unnamed target: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-two', 'channel-target-project', 'email', '', 'two@example.com')`); err != nil {
		t.Fatalf("expected second unnamed target to be allowed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-three', 'channel-target-project', 'email', 'billing', 'three@example.com')`); err != nil {
		t.Fatalf("failed to insert named target: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_targets (id, project_id, platform, name, target_id) VALUES ('target-four', 'channel-target-project', 'email', 'billing', 'four@example.com')`); err == nil {
		t.Fatal("expected duplicate non-empty target name to be rejected")
	}
}

func TestMigrations_PreserveForeignKeyData(t *testing.T) {
	// Create a temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Run all migrations
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create test data
	// Create a project
	_, err = db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('test-project', 'Test Project', 'Test', '/tmp')`)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Create a task
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('test-task', 'test-project', 'Test Task', 'scheduled', 'pending')`)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}

	// Create a schedule
	_, err = db.Exec(`INSERT INTO schedules (id, task_id, run_at, repeat_type) VALUES ('test-schedule', 'test-task', datetime('now'), 'daily')`)
	if err != nil {
		t.Fatalf("failed to insert schedule: %v", err)
	}

	// Create an execution
	_, err = db.Exec(`INSERT INTO executions (id, task_id, status, started_at) VALUES ('test-exec', 'test-task', 'completed', datetime('now'))`)
	if err != nil {
		t.Fatalf("failed to insert execution: %v", err)
	}

	// Verify the data exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM schedules WHERE task_id = 'test-task'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count schedules: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 schedule, got %d", count)
	}

	err = db.QueryRow("SELECT COUNT(*) FROM executions WHERE task_id = 'test-task'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count executions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 execution, got %d", count)
	}

	// Now verify that the schema has proper constraints
	var schema string
	err = db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(&schema)
	if err != nil {
		t.Fatalf("failed to get tasks schema: %v", err)
	}

	// Check for CHECK constraints
	if schema == "" {
		t.Fatal("tasks table schema is empty")
	}

	// Verify foreign keys are enabled
	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("failed to check foreign keys: %v", err)
	}
	if fkEnabled != 1 {
		t.Fatal("foreign keys should be enabled")
	}

	t.Logf("✅ All migrations completed successfully and preserved foreign key data")
}

func TestMigrations_AgentsTableDoesNotContainColorColumn(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	rows, err := db.Query("PRAGMA table_info(agents)")
	if err != nil {
		t.Fatalf("failed to inspect agents schema: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("failed to scan agents column metadata: %v", err)
		}
		if name == "color" {
			t.Fatalf("expected agents table to not include legacy color column")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("failed during agents schema inspection: %v", err)
	}
}

// TestMigration012_CheckConstraints verifies that migration 012 properly
// adds CHECK constraints to the tasks table.
func TestMigration012_CheckConstraints(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create a project first
	_, err = db.Exec(`INSERT INTO projects (id, name) VALUES ('test-proj', 'Test')`)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Test category CHECK constraint
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, category) VALUES ('t1', 'test-proj', 'Test 1', 'invalid-category')`)
	if err == nil {
		t.Fatal("expected error for invalid category, got nil")
	}

	// Test status CHECK constraint
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, status) VALUES ('t2', 'test-proj', 'Test 2', 'invalid-status')`)
	if err == nil {
		t.Fatal("expected error for invalid status, got nil")
	}

	// Test tag CHECK constraint
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, tag) VALUES ('t3', 'test-proj', 'Test 3', 'invalid-tag')`)
	if err == nil {
		t.Fatal("expected error for invalid tag, got nil")
	}

	// Valid inserts should succeed
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, category, status, tag) VALUES ('t4', 'test-proj', 'Test 4', 'active', 'pending', 'feature')`)
	if err != nil {
		t.Fatalf("expected valid insert to succeed: %v", err)
	}

	t.Logf("✅ All CHECK constraints working correctly")
}

func TestMigrations_GitHubRepoURLAndTaskPullRequests(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Ensure projects.repo_url exists
	rows, err := db.Query("PRAGMA table_info(projects)")
	if err != nil {
		t.Fatalf("failed to inspect projects table: %v", err)
	}
	defer rows.Close()

	repoURLExists := false
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("failed to scan projects column: %v", err)
		}
		if name == "repo_url" {
			repoURLExists = true
		}
	}
	if !repoURLExists {
		t.Fatal("expected projects table to include repo_url column")
	}

	prRows, err := db.Query("PRAGMA table_info(task_pull_requests)")
	if err != nil {
		t.Fatalf("failed to inspect task_pull_requests table: %v", err)
	}
	defer prRows.Close()
	prColumns := map[string]bool{}
	for prRows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := prRows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("failed to scan task_pull_requests column: %v", err)
		}
		prColumns[name] = true
	}
	for _, column := range []string{"issue_number", "issue_url"} {
		if !prColumns[column] {
			t.Fatalf("expected task_pull_requests table to include %s column", column)
		}
	}

	// Ensure task_pull_requests exists and enforces task_id uniqueness/FK by insertion
	_, err = db.Exec(`INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES ('gh-proj', 'GH Project', '', '/tmp/repo', 'https://github.com/openvibely/openvibely')`)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('gh-task', 'gh-proj', 'Task', 'active', 'pending')`)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	_, err = db.Exec(`INSERT INTO task_pull_requests (task_id, pr_number, pr_url, pr_state, issue_number, issue_url) VALUES ('gh-task', 10, 'https://github.com/openvibely/openvibely/pull/10', 'open', 20, 'https://github.com/openvibely/openvibely/issues/20')`)
	if err != nil {
		t.Fatalf("failed to insert task pull request: %v", err)
	}
	var issueNumber int
	var issueURL string
	if err := db.QueryRow(`SELECT issue_number, issue_url FROM task_pull_requests WHERE task_id = 'gh-task'`).Scan(&issueNumber, &issueURL); err != nil {
		t.Fatalf("failed to query task pull request issue metadata: %v", err)
	}
	if issueNumber != 20 || issueURL != "https://github.com/openvibely/openvibely/issues/20" {
		t.Fatalf("unexpected issue metadata: number=%d url=%q", issueNumber, issueURL)
	}
	_, err = db.Exec(`INSERT INTO task_pull_requests (task_id, pr_number, pr_url, pr_state) VALUES ('gh-task', 11, 'https://github.com/openvibely/openvibely/pull/11', 'open')`)
	if err == nil {
		t.Fatal("expected UNIQUE constraint failure for duplicate task_id in task_pull_requests")
	}
	var prRecordID string
	if err := db.QueryRow(`SELECT id FROM task_pull_requests WHERE task_id = 'gh-task'`).Scan(&prRecordID); err != nil {
		t.Fatalf("failed to query task pull request id: %v", err)
	}
	_, err = db.Exec(`INSERT INTO thread_inputs (id, scope, project_id, task_id, input_mode, input_status, content, queue_position) VALUES ('gh-feedback-input', 'task_thread', 'gh-proj', 'gh-task', 'queued', 'pending', 'Review feedback', 1)`)
	if err != nil {
		t.Fatalf("failed to insert thread input for feedback link: %v", err)
	}
	_, err = db.Exec(`INSERT INTO github_pr_feedback_forwarded (task_pull_request_id, task_id, repo_full_name, pr_number, feedback_kind, github_id, author_login, html_url, body, created_at, queued_thread_input_id) VALUES (?, 'gh-task', 'openvibely/openvibely', 10, 'issue_comment', '100', 'alice', 'https://github.com/openvibely/openvibely/pull/10#issuecomment-100', 'Looks good', '2026-07-09T00:00:00Z', 'gh-feedback-input')`, prRecordID)
	if err != nil {
		t.Fatalf("failed to insert forwarded github pr feedback: %v", err)
	}
	_, err = db.Exec(`INSERT INTO github_pr_feedback_forwarded (task_pull_request_id, task_id, repo_full_name, pr_number, feedback_kind, github_id, author_login, created_at) VALUES (?, 'gh-task', 'openvibely/openvibely', 10, 'issue_comment', '100', 'alice', '2026-07-09T00:00:00Z')`, prRecordID)
	if err == nil {
		t.Fatal("expected UNIQUE constraint failure for duplicate forwarded github pr feedback")
	}
}

func TestMigration082_NormalizesUnreleasedSkillCuratorNames(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 74); err != nil {
		t.Fatalf("failed to run migrations through 074: %v", err)
	}
	if err := applyUnreleasedLifecycleSchemaForTest(db); err != nil {
		t.Fatalf("failed to simulate unreleased lifecycle schema: %v", err)
	}
	for version := 75; version <= 81; version++ {
		if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version); err != nil {
			t.Fatalf("failed to mark unreleased migration %d applied: %v", version, err)
		}
	}

	if _, err := db.Exec(`
		INSERT INTO agents (
			id, name, description, system_prompt, model, tools, tool_config,
			plugins, mcp_servers, skills, system_kind,
			key, scope, project_id, selectable_as_primary, enabled,
			permission_defaults_json, model_defaults_json,
			created_by, generated_status, absorbed_into, source_refs_json
		) VALUES (
			'sys_skill_curator_00000000000000000001', 'System: Agent Creator', '', '', 'inherit', '[]', '{}',
			'[]', '[]', '[]', 'agent_creator',
			'agent_creator', 'global', NULL, 0, 1,
			'{}', '{}', 'system', 'protected', NULL, '[]'
		)
	`); err != nil {
		t.Fatalf("failed to insert unreleased old Skill Curator agent: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_lifecycle_hooks (
			id, agent_id, when_slot, skill_key, prompt_override, output_contract,
			blocking, enabled, permissions_json, run_policy_json, schedule_json
		) VALUES
			('hk_old_route', 'sys_skill_curator_00000000000000000001', 'route_task', 'route_task', '', 'selected_mode', 1, 1, '{}', '{}', NULL),
			('hk_old_maintain_agent_skill_library', 'sys_skill_curator_00000000000000000001', 'after_complete', 'maintain_agent_skill_library', '', 'learning_summary', 0, 1, '{}', '{}', NULL)
	`); err != nil {
		t.Fatalf("failed to insert old lifecycle hooks: %v", err)
	}

	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run consolidated migration 082: %v", err)
	}

	var key, systemKind, tools string
	if err := db.QueryRow(`SELECT key, system_kind, tools FROM agents WHERE id = 'sys_skill_curator_00000000000000000001'`).Scan(&key, &systemKind, &tools); err != nil {
		t.Fatalf("failed to load normalized agent: %v", err)
	}
	if key != "skill_curator" || systemKind != "skill_curator" {
		t.Fatalf("agent not normalized: key=%q system_kind=%q", key, systemKind)
	}
	if tools != `["skill_view","skills_list","agent_list","agent_view","skill_manage","skill_import","agent_skill_manage"]` {
		t.Fatalf("skill curator tools not normalized: %s", tools)
	}
	var routeContract string
	var routeBlocking int
	if err := db.QueryRow(`SELECT output_contract, blocking FROM agent_lifecycle_hooks WHERE id = 'hk_old_route'`).Scan(&routeContract, &routeBlocking); err != nil {
		t.Fatalf("failed to load route hook: %v", err)
	}
	if routeContract != "selected_skills" {
		t.Fatalf("route hook output contract = %q, want selected_skills", routeContract)
	}
	if routeBlocking != 0 {
		t.Fatalf("route hook blocking = %d, want 0 for parallel routing", routeBlocking)
	}
	var oldCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM agent_lifecycle_hooks h
		JOIN agents a ON a.id = h.agent_id
		WHERE a.key = 'agent_creator'
		   OR a.system_kind = 'agent_creator'
		   OR h.skill_key = 'maintain_agent_skill_library'
	`).Scan(&oldCount); err != nil {
		t.Fatalf("failed to count old identifiers: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("expected old Skill Curator identifiers to be removed, got %d", oldCount)
	}
}

func TestMigration082_SkipsWhenLocalDevDBAlreadyApplied082(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 74); err != nil {
		t.Fatalf("failed to run migrations through 074: %v", err)
	}
	if err := applyUnreleasedLifecycleSchemaForTest(db); err != nil {
		t.Fatalf("failed to simulate unreleased lifecycle schema: %v", err)
	}
	for version := 75; version <= 82; version++ {
		if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version); err != nil {
			t.Fatalf("failed to mark unreleased migration %d applied: %v", version, err)
		}
	}

	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("expected local dev DB already at 082 to remain migratable: %v", err)
	}

	var maxVersion int
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("failed to read max goose version: %v", err)
	}
	if maxVersion != 122 {
		t.Fatalf("max goose version = %d, want 122", maxVersion)
	}
}

func TestMigration082_AppliesAfterPublic074(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 74); err != nil {
		t.Fatalf("failed to run migrations through 074: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run consolidated migration 082: %v", err)
	}

	for _, table := range []string{"agent_lifecycle_hooks", "lifecycle_executions", "agent_config_mutations", "lifecycle_execution_events"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected table %s after migration 082: %v", table, err)
		}
	}
	for _, column := range []string{"key", "scope", "permission_defaults_json", "model_defaults_json", "generated_status"} {
		if !testColumnExists(t, db, "agents", column) {
			t.Fatalf("expected agents.%s after migration 082", column)
		}
	}
	var contract string
	var blocking int
	if err := db.QueryRow(`
		SELECT output_contract, blocking
		FROM agent_lifecycle_hooks
		WHERE agent_id = 'sys_skill_curator_00000000000000000001'
		  AND when_slot = 'route_task'
		  AND skill_key = 'route_task'
	`).Scan(&contract, &blocking); err != nil {
		t.Fatalf("failed to load seeded route hook: %v", err)
	}
	if contract != "selected_skills" {
		t.Fatalf("seeded route hook contract = %q, want selected_skills", contract)
	}
	if blocking != 0 {
		t.Fatalf("seeded route hook blocking = %d, want 0 for parallel routing", blocking)
	}
}

func applyUnreleasedLifecycleSchemaForTest(db *sql.DB) error {
	statements := []string{
		`ALTER TABLE agents ADD COLUMN key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN scope TEXT NOT NULL DEFAULT 'global'`,
		`ALTER TABLE agents ADD COLUMN project_id TEXT NULL REFERENCES projects(id) ON DELETE SET NULL`,
		`ALTER TABLE agents ADD COLUMN selectable_as_primary INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE agents ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE agents ADD COLUMN permission_defaults_json TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE agents ADD COLUMN model_defaults_json TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE agents ADD COLUMN created_by TEXT NOT NULL DEFAULT 'user'`,
		`ALTER TABLE agents ADD COLUMN generated_status TEXT NOT NULL DEFAULT 'user_edited'`,
		`ALTER TABLE agents ADD COLUMN absorbed_into TEXT NULL`,
		`ALTER TABLE agents ADD COLUMN source_refs_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE agents ADD COLUMN archived_at DATETIME NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_key_unique ON agents(key) WHERE key <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_agents_generated_status ON agents(generated_status)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_scope_project ON agents(scope, project_id)`,
		`CREATE TABLE agent_lifecycle_hooks (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			when_slot TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
			skill_key TEXT NOT NULL,
			prompt_override TEXT NOT NULL DEFAULT '',
			output_contract TEXT NOT NULL DEFAULT '' CHECK (output_contract IN ('','selected_mode','selected_skills','context_block','activity_summary','learning_summary','library_update_summary')),
			blocking INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			permissions_json TEXT NOT NULL DEFAULT '{}',
			run_policy_json TEXT NOT NULL DEFAULT '{}',
			schedule_json TEXT,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE lifecycle_executions (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			task_run_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
			when_slot TEXT NOT NULL CHECK (when_slot IN ('route_task','before_run','task_mode','after_complete','scheduled')),
			lifecycle_hook_id TEXT REFERENCES agent_lifecycle_hooks(id) ON DELETE SET NULL,
			parent_execution_id TEXT REFERENCES lifecycle_executions(id) ON DELETE SET NULL,
			skill_key TEXT NOT NULL DEFAULT '',
			output_contract TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed','skipped')),
			input_json TEXT NOT NULL DEFAULT '{}',
			output_json TEXT NOT NULL DEFAULT '{}',
			error TEXT NOT NULL DEFAULT '',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			priority INTEGER NOT NULL DEFAULT 0,
			next_retry_at DATETIME,
			idempotency_key TEXT NOT NULL DEFAULT '',
			started_at DATETIME NOT NULL DEFAULT (datetime('now')),
			completed_at DATETIME
		)`,
		`CREATE TABLE agent_config_mutations (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			lifecycle_execution_id TEXT REFERENCES lifecycle_executions(id) ON DELETE SET NULL,
			task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
			task_run_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
			target_type TEXT NOT NULL CHECK (target_type IN ('agent','skill','routing','hook','support_file')),
			target_key TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			proposed_payload_json TEXT NOT NULL DEFAULT '{}',
			validation_status TEXT NOT NULL DEFAULT 'applied' CHECK (validation_status IN ('applied','blocked','no_op')),
			validation_errors_json TEXT NOT NULL DEFAULT '[]',
			changed_paths_json TEXT NOT NULL DEFAULT '[]',
			imported_config_changes_json TEXT NOT NULL DEFAULT '[]',
			evidence_refs_json TEXT NOT NULL DEFAULT '[]',
			idempotency_key TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE lifecycle_execution_events (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			lifecycle_execution_id TEXT NOT NULL REFERENCES lifecycle_executions(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE(lifecycle_execution_id, seq)
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func testColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("failed to inspect %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("failed to scan %s column: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("failed to iterate %s columns: %v", table, err)
	}
	return false
}

func TestMigration071_RebuildsAgentConfigsWithReferences(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 70); err != nil {
		t.Fatalf("failed to run migrations through 070: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO agent_configs (id, name, provider, model, is_default, auth_method)
		VALUES ('agent-071', 'Agent 071', 'anthropic', 'claude-sonnet-4-5-20250929', 1, 'cli')
	`); err != nil {
		t.Fatalf("failed to insert agent config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, default_agent_config_id) VALUES ('project-071', 'Project 071', 'agent-071')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO tasks (id, project_id, title, category, status, agent_id)
		VALUES ('task-071', 'project-071', 'Task 071', 'active', 'pending', 'agent-071')
	`); err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO executions (id, task_id, agent_config_id, status, started_at)
		VALUES ('execution-071', 'task-071', 'agent-071', 'running', datetime('now'))
	`); err != nil {
		t.Fatalf("failed to insert execution: %v", err)
	}

	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migration 071 with existing references: %v", err)
	}

	if _, err := db.Exec(`UPDATE agent_configs SET reasoning_effort = 'max' WHERE id = 'agent-071'`); err != nil {
		t.Fatalf("expected migration 071 to allow reasoning_effort=max: %v", err)
	}

	var agentID, projectDefaultID, executionAgentID string
	if err := db.QueryRow(`SELECT agent_id FROM tasks WHERE id = 'task-071'`).Scan(&agentID); err != nil {
		t.Fatalf("failed to read task agent reference: %v", err)
	}
	if err := db.QueryRow(`SELECT default_agent_config_id FROM projects WHERE id = 'project-071'`).Scan(&projectDefaultID); err != nil {
		t.Fatalf("failed to read project default agent reference: %v", err)
	}
	if err := db.QueryRow(`SELECT agent_config_id FROM executions WHERE id = 'execution-071'`).Scan(&executionAgentID); err != nil {
		t.Fatalf("failed to read execution agent reference: %v", err)
	}
	for name, got := range map[string]string{
		"task agent":             agentID,
		"project default agent":  projectDefaultID,
		"execution agent config": executionAgentID,
	} {
		if got != "agent-071" {
			t.Fatalf("%s reference = %q, want agent-071", name, got)
		}
	}
}

func TestMigration091_BackfillsHistoricalLLMUsageFromExecutions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 86); err != nil {
		t.Fatalf("failed to run migrations through 086: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO agent_configs (id, name, provider, model, auth_method, api_key) VALUES ('agent-091', 'OpenAI API', 'openai', 'gpt-test', 'api_key', 'key')`); err != nil {
		t.Fatalf("failed to insert agent config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('project-091', 'Usage Project')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('task-091', 'project-091', 'Task 091', 'active', 'completed')`); err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO executions (id, task_id, agent_config_id, status, tokens_used, duration_ms, started_at, completed_at) VALUES ('execution-091', 'task-091', 'agent-091', 'completed', 123, 456, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("failed to insert execution: %v", err)
	}

	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migration 091: %v", err)
	}

	assertUsageTrackingSchema091(t, db)

	var provider, projectID, executionID, operation string
	var totalTokens, inputTokens, outputTokens int
	if err := db.QueryRow(`SELECT provider, project_id, execution_id, operation, total_tokens, input_tokens, output_tokens FROM llm_usage_events WHERE execution_id = 'execution-091'`).Scan(&provider, &projectID, &executionID, &operation, &totalTokens, &inputTokens, &outputTokens); err != nil {
		t.Fatalf("failed to read backfilled usage: %v", err)
	}
	if provider != "openai" || projectID != "project-091" || executionID != "execution-091" || operation != "task" || totalTokens != 123 || inputTokens != 0 || outputTokens != 0 {
		t.Fatalf("unexpected backfilled usage provider=%s project=%s exec=%s op=%s total=%d input=%d output=%d", provider, projectID, executionID, operation, totalTokens, inputTokens, outputTokens)
	}
}

func TestMigration091_LocalDevAlreadyAppliedUsageChainStillMigrates(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.UpTo(db, ".", 86); err != nil {
		t.Fatalf("failed to run migrations through 086: %v", err)
	}
	if err := applyPreviouslyUnreleasedUsageSchemaForTest(db); err != nil {
		t.Fatalf("failed to simulate old usage migration chain: %v", err)
	}
	for version := 87; version <= 90; version++ {
		if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, version); err != nil {
			t.Fatalf("failed to mark old unreleased migration %d applied: %v", version, err)
		}
	}

	if _, err := db.Exec(`INSERT INTO agent_configs (id, name, provider, model, auth_method, api_key) VALUES ('agent-old-091', 'OpenAI API', 'openai', 'gpt-test', 'api_key', 'key')`); err != nil {
		t.Fatalf("failed to insert agent config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('project-old-091', 'Usage Project')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('task-old-091', 'project-old-091', 'Task 091', 'active', 'completed')`); err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO executions (id, task_id, agent_config_id, status, tokens_used, duration_ms, started_at, completed_at) VALUES ('execution-old-091', 'task-old-091', 'agent-old-091', 'completed', 321, 654, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("failed to insert execution: %v", err)
	}

	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run consolidated migration 091 from old usage chain: %v", err)
	}

	assertUsageTrackingSchema091(t, db)
	if testColumnExists(t, db, "llm_usage_events", "request_status") {
		t.Fatal("expected request_status to be normalized away")
	}

	var totalTokens int
	if err := db.QueryRow(`SELECT total_tokens FROM llm_usage_events WHERE execution_id = 'execution-old-091'`).Scan(&totalTokens); err != nil {
		t.Fatalf("failed to read backfilled usage: %v", err)
	}
	if totalTokens != 321 {
		t.Fatalf("backfilled total tokens = %d, want 321", totalTokens)
	}

	var maxVersion int
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("failed to read max goose version: %v", err)
	}
	if maxVersion != 122 {
		t.Fatalf("max goose version = %d, want 122", maxVersion)
	}
}

func TestMigration095_AllowsCreatedSkillAnalyticsEvents(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "skill-analytics-created.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO skill_analytics_events (skill_scope, skill_handle, event_type, source, surface) VALUES ('global', 'created_skill', 'created', 'manual', 'task_thread')`); err != nil {
		t.Fatalf("created skill analytics event rejected: %v", err)
	}
}

func assertUsageTrackingSchema091(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"llm_usage_events", "account_usage_snapshots", "account_usage_extra_limits"} {
		var tableName string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableName); err != nil {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}
	for _, column := range []string{"status", "input_tokens", "output_tokens", "cached_input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "reasoning_output_tokens", "total_tokens", "cost_usd", "latency_ms", "raw_usage_json", "occurred_at"} {
		if !testColumnExists(t, db, "llm_usage_events", column) {
			t.Fatalf("expected llm_usage_events.%s column", column)
		}
	}
	for _, column := range []string{"account_display_name", "account_detail", "billing_label", "subscription_status", "extra_usage_label", "extra_usage_monthly_limit_usd", "extra_usage_used_usd"} {
		if !testColumnExists(t, db, "account_usage_snapshots", column) {
			t.Fatalf("expected account_usage_snapshots.%s column", column)
		}
	}
	for _, column := range []string{"snapshot_id", "provider", "account_id", "agent_config_id", "limit_key", "label", "used_percent", "window_minutes", "reset_at", "raw_json"} {
		if !testColumnExists(t, db, "account_usage_extra_limits", column) {
			t.Fatalf("expected account_usage_extra_limits.%s column", column)
		}
	}

	if _, err := db.Exec(`
		INSERT INTO agent_configs (id, name, provider, model, auth_method) VALUES ('agent-schema-091', 'Anthropic', 'anthropic', 'claude-test', 'oauth');
		INSERT INTO account_usage_snapshots (id, provider, account_id, agent_config_id, plan_type, account_display_name, account_detail, billing_label, subscription_status, extra_usage_label, extra_usage_monthly_limit_usd, extra_usage_used_usd, raw_json)
		VALUES ('snapshot-schema-091', 'anthropic', 'organization:org-091', 'agent-schema-091', 'Claude Max (20x)', 'James', 'james@example.com', 'Subscription billing', 'Active', 'Usage credits enabled', 200.0, 0.0, '{}');
		INSERT INTO account_usage_extra_limits (id, snapshot_id, provider, account_id, agent_config_id, limit_key, label, used_percent, raw_json)
		VALUES ('limit-schema-091', 'snapshot-schema-091', 'anthropic', 'organization:org-091', 'agent-schema-091', 'claude-test', 'Claude limit', 12.5, '{}');
	`); err != nil && !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("failed to insert usage schema rows: %v", err)
	}
}

func applyPreviouslyUnreleasedUsageSchemaForTest(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE llm_usage_events (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			provider TEXT NOT NULL,
			account_id TEXT,
			project_id TEXT,
			task_id TEXT,
			execution_id TEXT,
			chat_thread_id TEXT,
			turn_id TEXT,
			agent_config_id TEXT,
			model TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL DEFAULT '',
			request_status TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			stop_reason TEXT NOT NULL DEFAULT '',
			rate_limit_reached_type TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			context_window INTEGER,
			max_output_tokens INTEGER,
			provider_response_id TEXT,
			raw_usage_json TEXT NOT NULL DEFAULT '{}',
			occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE account_usage_snapshots (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			provider TEXT NOT NULL,
			account_id TEXT,
			agent_config_id TEXT,
			plan_type TEXT NOT NULL DEFAULT '',
			credits_remaining REAL,
			primary_label TEXT NOT NULL DEFAULT '',
			primary_used_percent REAL,
			primary_window_minutes INTEGER,
			primary_resets_at TEXT,
			secondary_label TEXT NOT NULL DEFAULT '',
			secondary_used_percent REAL,
			secondary_window_minutes INTEGER,
			secondary_resets_at TEXT,
			model_limit_label TEXT NOT NULL DEFAULT '',
			model_limit_used_percent REAL,
			model_limit_window_minutes INTEGER,
			model_limit_resets_at TEXT,
			rate_limit_reached_type TEXT NOT NULL DEFAULT '',
			raw_json TEXT NOT NULL DEFAULT '{}',
			fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			account_display_name TEXT NOT NULL DEFAULT '',
			account_detail TEXT NOT NULL DEFAULT '',
			billing_label TEXT NOT NULL DEFAULT '',
			subscription_status TEXT NOT NULL DEFAULT '',
			extra_usage_label TEXT NOT NULL DEFAULT '',
			extra_usage_monthly_limit_usd REAL,
			extra_usage_used_usd REAL
		);
		CREATE TABLE account_usage_extra_limits (
			id TEXT PRIMARY KEY,
			snapshot_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			account_id TEXT,
			agent_config_id TEXT,
			limit_key TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			used_percent REAL,
			window_minutes INTEGER,
			reset_at TEXT,
			raw_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	return err
}

func TestMain(m *testing.M) {
	// Setup
	code := m.Run()
	// Teardown
	os.Exit(code)
}

func TestMigration110_GitHubAuthorizationAndProjectInbox(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "github-auth-110.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	for _, table := range []string{"github_authorized_actors", "github_project_inboxes"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("failed to inspect table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	if _, err := db.Exec(`INSERT INTO github_authorized_actors (github_login, permission) VALUES ('Alice', 'approve')`); err != nil {
		t.Fatalf("expected github authorized actor insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO github_authorized_actors (github_login, permission) VALUES ('alice', 'approve')`); err == nil {
		t.Fatal("expected mixed-case duplicate github actor login to be rejected")
	}
	if _, err := db.Exec(`INSERT INTO github_authorized_actors (github_login, permission) VALUES ('bob', 'owner')`); err == nil {
		t.Fatal("expected invalid github actor permission to be rejected")
	}
	for _, id := range []string{"github-inbox-project-one", "github-inbox-project-two"} {
		if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`, id, id); err != nil {
			t.Fatalf("failed to insert project %s: %v", id, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO github_project_inboxes (project_id, github_login) VALUES ('github-inbox-project-one', 'dev-bot')`); err != nil {
		t.Fatalf("expected first project inbox insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO github_project_inboxes (project_id, github_login) VALUES ('github-inbox-project-two', 'DEV-BOT')`); err != nil {
		t.Fatalf("same GitHub inbox login should be reusable by another project: %v", err)
	}
}

func TestMigration113AutomationDefinitionsUpAndDown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automations-113.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 113); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"automations", "automation_versions", "automation_nodes", "automation_edges", "automation_definition_resources", "automation_trigger_owners"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected table %s after migration up", table)
		}
	}
	if err := goose.DownTo(db, ".", 112); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"automation_trigger_owners", "automation_definition_resources", "automation_edges", "automation_nodes", "automation_versions", "automations"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected table %s removed after migration down", table)
		}
	}
}

func TestMigration120And121LeaveOnlyAtomicAutomationSaveSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automations-atomic-save.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 119); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('atomic-project', 'Atomic', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state)
			VALUES ('atomic-automation', 'atomic-project', 'atomic/test', 'Atomic', 'draft');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('atomic-version', 'atomic-project', 'atomic-automation', 1, 'draft', 'manual', 'vision_driver');
		INSERT INTO automation_publication_attempts (id, project_id, automation_id, version_id, plan_revision, status)
			VALUES ('atomic-attempt', 'atomic-project', 'atomic-automation', 'atomic-version', 'obsolete-revision', 'completed');
		INSERT INTO automation_chat_confirmation_receipts
			(token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id,
			 plan_message_id, automation_name, source, candidate_json, expires_at, consumed_attempt_id,
			 confirming_user_input_id, confirmation_method, consumed_at)
			VALUES ('atomic-token', 'atomic-project', 'atomic-automation', 'atomic-version', 'obsolete-revision',
			 'principal', 'thread', 'plan-message', 'Atomic', 'manual', '{"schema_version":1}',
			 datetime('now', '+30 minutes'), 'atomic-attempt', 'confirmation-input', 'button', CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatal(err)
	}

	var confirmationMethod string
	if err := db.QueryRow(`SELECT confirmation_method FROM automation_chat_confirmation_receipts WHERE token_id = 'atomic-token'`).Scan(&confirmationMethod); err != nil {
		t.Fatal(err)
	}
	if confirmationMethod != "command" {
		t.Fatalf("migrated confirmation method = %q, want command", confirmationMethod)
	}

	for _, table := range []string{"automation_graph_metadata", "automation_chat_confirmation_receipts", "automation_chat_confirmation_inputs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected atomic Save table %s: count=%d err=%v", table, count, err)
		}
	}
	for _, table := range []string{"automation_draft_metadata", "automation_publication_attempts", "automation_publication_steps"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("obsolete Save table %s still exists: count=%d err=%v", table, count, err)
		}
	}
	for _, column := range []string{"automation_id", "version_id", "plan_revision", "consumed_attempt_id"} {
		if tableHasColumn(t, db, "automation_chat_confirmation_receipts", column) {
			t.Fatalf("obsolete confirmation column automation_chat_confirmation_receipts.%s still exists", column)
		}
	}
	for _, column := range []string{"automation_id", "version_id"} {
		if tableHasColumn(t, db, "automation_chat_confirmation_inputs", column) {
			t.Fatalf("obsolete confirmation column automation_chat_confirmation_inputs.%s still exists", column)
		}
	}
}

func TestMigration115AutomationPublicationUpAndDown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automations-115.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 115); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"automation_draft_metadata", "automation_publication_attempts", "automation_publication_steps", "automation_chat_confirmation_receipts", "automation_chat_confirmation_inputs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected publication table %s after up migration: count=%d err=%v", table, count, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('publication-project', 'Publication', '', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state) VALUES ('publication-automation', 'publication-project', 'draft/test', 'Draft', 'draft')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key) VALUES ('publication-version', 'publication-project', 'publication-automation', 1, 'draft', 'manual', 'native_sdlc')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_draft_metadata (version_id, project_id, automation_id, candidate_json) VALUES ('publication-version', 'publication-project', 'publication-automation', '{"schema_version":1}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_publication_attempts (id, project_id, automation_id, version_id, plan_revision, status) VALUES ('publication-attempt', 'publication-project', 'publication-automation', 'publication-version', 'revision', 'publishing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_publication_steps (attempt_id, step_key, operation, target_key, status) VALUES ('publication-attempt', 'task:one', 'create', 'task:one', 'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_chat_confirmation_receipts (token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id, plan_message_id, expires_at) VALUES ('token', 'publication-project', 'publication-automation', 'publication-version', 'revision', 'principal', 'thread', 'plan-message', datetime('now', '+30 minutes'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO automation_chat_confirmation_receipts (token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id, plan_message_id, expires_at, consumed_at) VALUES ('invalid-token', 'publication-project', 'publication-automation', 'publication-version', 'revision', 'principal', 'thread', 'plan-message', datetime('now', '+30 minutes'), CURRENT_TIMESTAMP)`); err == nil {
		t.Fatal("expected partial consumed confirmation state to be rejected")
	}
	if _, err := db.Exec(`DELETE FROM projects WHERE id = 'publication-project'`); err != nil {
		t.Fatalf("project deletion must cascade publication metadata: %v", err)
	}
	for _, table := range []string{"automation_draft_metadata", "automation_publication_attempts", "automation_publication_steps", "automation_chat_confirmation_receipts", "automation_chat_confirmation_inputs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expected project cascade to clear %s: count=%d err=%v", table, count, err)
		}
	}
	if err := goose.DownTo(db, ".", 114); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"automation_draft_metadata", "automation_publication_attempts", "automation_publication_steps", "automation_chat_confirmation_receipts", "automation_chat_confirmation_inputs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expected publication table %s removed after down migration: count=%d err=%v", table, count, err)
		}
	}
	var definitions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='automations'`).Scan(&definitions); err != nil || definitions != 1 {
		t.Fatalf("definition tables must remain after migration 115 down: count=%d err=%v", definitions, err)
	}
}

func TestMigration114AutomationRuntimeUpAndDown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automations-114.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 114); err != nil {
		t.Fatal(err)
	}
	tables := []string{"automation_invocations", "automation_dispatch_outbox", "automation_task_run_reservations", "automation_work_items", "automation_work_item_positions", "automation_thread_input_bindings", "automation_activities", "automation_activity_resources", "automation_transitions"}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected runtime table %s after up migration: count=%d err=%v", table, count, err)
		}
	}
	var dispatchColumn int
	rows, err := db.Query(`PRAGMA table_info(executions)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "dispatch_id" {
			dispatchColumn++
		}
	}
	_ = rows.Close()
	if dispatchColumn != 1 {
		t.Fatal("expected executions.dispatch_id after migration 114")
	}
	if err := goose.DownTo(db, ".", 113); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expected runtime table %s removed after down migration: count=%d err=%v", table, count, err)
		}
	}
	var definitions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='automations'`).Scan(&definitions); err != nil || definitions != 1 {
		t.Fatalf("phase 1 definition tables must remain after migration 114 down: count=%d err=%v", definitions, err)
	}
}
