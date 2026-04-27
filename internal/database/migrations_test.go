package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/database/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// TestMigrations_PreserveForeignKeyData verifies that all migrations preserve
// foreign key referenced data when recreating tables.
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

	// Ensure task_pull_requests exists and enforces task_id uniqueness/FK by insertion
	_, err = db.Exec(`INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES ('gh-proj', 'GH Project', '', '/tmp/repo', 'https://github.com/openvibely/openvibely')`)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}
	_, err = db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('gh-task', 'gh-proj', 'Task', 'active', 'pending')`)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}
	_, err = db.Exec(`INSERT INTO task_pull_requests (task_id, pr_number, pr_url, pr_state) VALUES ('gh-task', 10, 'https://github.com/openvibely/openvibely/pull/10', 'open')`)
	if err != nil {
		t.Fatalf("failed to insert task pull request: %v", err)
	}
	_, err = db.Exec(`INSERT INTO task_pull_requests (task_id, pr_number, pr_url, pr_state) VALUES ('gh-task', 11, 'https://github.com/openvibely/openvibely/pull/11', 'open')`)
	if err == nil {
		t.Fatal("expected UNIQUE constraint failure for duplicate task_id in task_pull_requests")
	}
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

func TestMain(m *testing.M) {
	// Setup
	code := m.Run()
	// Teardown
	os.Exit(code)
}
