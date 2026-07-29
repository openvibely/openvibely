package database

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskDeletionLargeHistoryDoesNotBlockUnrelatedQueries(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "task-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO projects(id, name) VALUES ('delete-project', 'Delete project');
		INSERT INTO tasks(id, project_id, title, category, status, swarm_role)
			VALUES ('delete-parent', 'delete-project', 'Delete parent', 'backlog', 'completed', 'parent');
		INSERT INTO tasks(id, project_id, title, category, status, parent_task_id, swarm_role)
			VALUES ('delete-child', 'delete-project', 'Delete child', 'backlog', 'completed', 'delete-parent', 'worker');
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 2000)
			INSERT INTO executions(id, task_id, status, prompt_sent, output)
			SELECT printf('delete-exec-%06d', x), 'delete-parent', 'completed', 'prompt', 'output' FROM n;
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 2000)
			INSERT INTO lifecycle_executions(id, task_id, task_run_id, when_slot, status)
			SELECT printf('delete-lifecycle-%06d', x), 'delete-parent', printf('delete-exec-%06d', x), 'route_task', 'completed' FROM n;
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 20000)
			INSERT INTO lifecycle_execution_events(id, lifecycle_execution_id, seq, event_type, payload_json)
			SELECT printf('delete-lifecycle-event-%06d', x), printf('delete-lifecycle-%06d', ((x - 1) / 10) + 1), ((x - 1) % 10) + 1, 'fixture', '{"fixture":true}' FROM n;
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 50000)
			INSERT INTO lifecycle_executions(id, task_id, task_run_id, when_slot, status)
			SELECT printf('unrelated-lifecycle-%06d', x), 'delete-child', printf('unrelated-run-%06d', x), 'route_task', 'completed' FROM n;
		INSERT INTO schedules(id, task_id, run_at, repeat_type) VALUES ('delete-schedule', 'delete-parent', CURRENT_TIMESTAMP, 'once');
		INSERT INTO task_goals(task_id, goal_id, objective, status) VALUES ('delete-parent', 'delete-goal', 'objective', 'active');
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 500)
			INSERT INTO thread_inputs(id, scope, project_id, task_id, input_mode, content, queue_position)
			SELECT printf('delete-input-%06d', x), 'task_thread', 'delete-project', 'delete-parent', 'queued', 'follow up', x FROM n;
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 100)
			INSERT INTO task_attachments(id, task_id, file_name, file_path, media_type, file_size)
			SELECT printf('delete-attachment-%06d', x), 'delete-parent', printf('fixture-%d.txt', x), printf('/tmp/fixture-%d.txt', x), 'text/plain', 7 FROM n;
		INSERT INTO alerts(id, project_id, task_id, execution_id, source_task_id, title)
			VALUES ('delete-linked-alert', 'delete-project', 'delete-parent', 'delete-exec-000001', 'delete-parent', 'linked');
		INSERT INTO llm_usage_events(id, provider, task_id, model)
			VALUES ('delete-linked-usage', 'test', 'delete-parent', 'test');
		INSERT INTO skill_analytics_events(id, project_id, task_id, execution_id, skill_scope, skill_handle, event_type, source, surface)
			VALUES ('delete-linked-skill', 'delete-project', 'delete-parent', 'delete-exec-000001', 'project', 'delete-skill', 'selected', 'manual', 'task_thread');
		WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x + 1 FROM n WHERE x < 50000)
			INSERT INTO alerts(id, project_id, title)
			SELECT printf('unrelated-alert-%06d', x), 'delete-project', 'unrelated' FROM n;
	`)
	if err != nil {
		t.Fatal(err)
	}

	queryLatency := make(chan time.Duration, 1)
	deleteStart := time.Now()
	go func() {
		time.Sleep(time.Millisecond)
		start := time.Now()
		var count int
		if queryErr := db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&count); queryErr != nil {
			queryLatency <- -1
			return
		}
		queryLatency <- time.Since(start)
	}()
	if _, err := db.Exec(`DELETE FROM tasks WHERE id = 'delete-parent'`); err != nil {
		t.Fatal(err)
	}
	deleteLatency := time.Since(deleteStart)
	unrelatedLatency := <-queryLatency
	if unrelatedLatency < 0 {
		t.Fatal("unrelated query failed during task deletion")
	}
	const responsivenessLimit = 2 * time.Second
	if deleteLatency > responsivenessLimit || unrelatedLatency > responsivenessLimit {
		t.Fatalf("indexed task deletion took %s and blocked an unrelated query for %s; both must remain below %s", deleteLatency, unrelatedLatency, responsivenessLimit)
	}
	t.Logf("deleted 2,000 execution turns, 2,000 lifecycle runs, and 20,000 lifecycle events with 50,000 unrelated lifecycle runs and alerts in %s; unrelated query latency %s", deleteLatency, unrelatedLatency)

	for table, want := range map[string]int{
		"tasks":                      1,
		"executions":                 0,
		"lifecycle_executions":       0,
		"lifecycle_execution_events": 0,
		"schedules":                  0,
		"task_goals":                 0,
		"thread_inputs":              0,
		"task_attachments":           0,
	} {
		var got int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, table, map[string]string{
			"tasks": "id IN ('delete-parent', 'delete-child')", "executions": "task_id = 'delete-parent'",
			"lifecycle_executions":       "task_id = 'delete-parent'",
			"lifecycle_execution_events": "lifecycle_execution_id LIKE 'delete-lifecycle-%'",
			"schedules":                  "task_id = 'delete-parent'", "task_goals": "task_id = 'delete-parent'",
			"thread_inputs": "task_id = 'delete-parent'", "task_attachments": "task_id = 'delete-parent'",
		}[table])
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s rows after deletion = %d, want %d", table, got, want)
		}
	}

	var parentTaskID, alertTaskID, alertExecutionID, alertSourceTaskID, usageTaskID, skillTaskID, skillExecutionID *string
	if err := db.QueryRow(`SELECT parent_task_id FROM tasks WHERE id = 'delete-child'`).Scan(&parentTaskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT task_id, execution_id, source_task_id FROM alerts WHERE id = 'delete-linked-alert'`).Scan(&alertTaskID, &alertExecutionID, &alertSourceTaskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT task_id FROM llm_usage_events WHERE id = 'delete-linked-usage'`).Scan(&usageTaskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT task_id, execution_id FROM skill_analytics_events WHERE id = 'delete-linked-skill'`).Scan(&skillTaskID, &skillExecutionID); err != nil {
		t.Fatal(err)
	}
	if parentTaskID != nil || alertTaskID != nil || alertExecutionID != nil || alertSourceTaskID != nil || usageTaskID != nil || skillTaskID != nil || skillExecutionID != nil {
		t.Fatalf("task deletion did not clear retained swarm/alert/analytics references: parent=%v alert_task=%v alert_execution=%v alert_source=%v usage_task=%v skill_task=%v skill_execution=%v", parentTaskID, alertTaskID, alertExecutionID, alertSourceTaskID, usageTaskID, skillTaskID, skillExecutionID)
	}
}

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

	var maxWorkers int
	if err := db.QueryRow("SELECT max_workers FROM worker_settings WHERE id='singleton'").Scan(&maxWorkers); err != nil {
		t.Fatalf("reading fresh global worker limit: %v", err)
	}
	if maxWorkers != 0 {
		t.Errorf("expected fresh global worker limit to be unlimited (0), got %d", maxWorkers)
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

func TestNew_PreservesExplicitGlobalWorkerLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "worker-limit.db")

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("New(%q) failed: %v", dbPath, err)
	}
	if _, err := db.Exec(`UPDATE worker_settings SET max_workers=1 WHERE id='singleton'`); err != nil {
		db.Close()
		t.Fatalf("setting explicit global worker limit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing initial database: %v", err)
	}

	db, err = New(dbPath)
	if err != nil {
		t.Fatalf("reopening %q failed: %v", dbPath, err)
	}
	defer db.Close()

	var maxWorkers int
	if err := db.QueryRow("SELECT max_workers FROM worker_settings WHERE id='singleton'").Scan(&maxWorkers); err != nil {
		t.Fatalf("reading persisted global worker limit: %v", err)
	}
	if maxWorkers != 1 {
		t.Fatalf("expected explicit global worker limit 1 to be preserved, got %d", maxWorkers)
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
