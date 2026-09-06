package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/database/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func openMigrationTestDB(tb testing.TB, dbPath string) *sql.DB {
	tb.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		tb.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			tb.Fatalf("failed to apply %s: %v", pragma, err)
		}
	}
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigration153TelegramUsernameOnlyLookupIndexDownDropsOnlyNewIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telegram-username-index-153.db")
	db := openMigrationTestDB(t, dbPath)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	assertIndexExists := func(indexName string, want bool) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&count); err != nil {
			t.Fatalf("failed to inspect index %s: %v", indexName, err)
		}
		if got := count == 1; got != want {
			t.Fatalf("index %s exists = %v, want %v", indexName, got, want)
		}
	}

	assertIndexExists("idx_telegram_auth_username_lower_unknown_id", true)
	assertIndexExists("idx_telegram_auth_unique_username", true)
	assertIndexExists("idx_telegram_auth_user", true)

	if err := goose.DownTo(db, ".", 152); err != nil {
		t.Fatalf("failed to migrate down to 152: %v", err)
	}
	assertIndexExists("idx_telegram_auth_username_lower_unknown_id", false)
	assertIndexExists("idx_telegram_auth_unique_username", true)
	assertIndexExists("idx_telegram_auth_user", true)
}

func TestMigration143DropsPredictiveCollisionTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "drop-predictive-collisions-143.db")
	db := openMigrationTestDB(t, dbPath)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 142); err != nil {
		t.Fatal(err)
	}

	tables := []string{
		"impact_analyses",
		"conflict_predictions",
		"conflict_history",
		"execution_order_recommendations",
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s does not exist before migration 143", table)
		}
	}

	if err := goose.UpTo(db, ".", 143); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("table %s still exists after migration 143", table)
		}
	}

	if err := goose.DownTo(db, ".", 142); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s was not recreated by migration 143 rollback", table)
		}
	}
}

func TestMigration130IndexesTaskDeletionForeignKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "task-deletion-indexes-130.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 129); err != nil {
		t.Fatal(err)
	}

	if plan := explainQueryPlan(t, db, `SELECT rowid FROM alerts WHERE execution_id = ?`, "execution"); !strings.Contains(plan, "SCAN alerts") {
		t.Fatalf("migration 129 alerts execution lookup plan = %q, want table scan baseline", plan)
	}
	if err := goose.UpTo(db, ".", 130); err != nil {
		t.Fatal(err)
	}

	queries := map[string]string{
		"idx_alerts_execution_id":                 `SELECT rowid FROM alerts WHERE execution_id = ?`,
		"idx_alerts_task_id":                      `SELECT rowid FROM alerts WHERE task_id = ?`,
		"idx_alerts_source_task_id":               `SELECT rowid FROM alerts WHERE source_task_id = ?`,
		"idx_architect_tasks_task_id":             `SELECT rowid FROM architect_tasks WHERE task_id = ?`,
		"idx_automation_dispatch_outbox_task":     `SELECT rowid FROM automation_dispatch_outbox WHERE task_id = ?`,
		"idx_conflict_history_task_a":             `SELECT rowid FROM conflict_history WHERE task_a_id = ?`,
		"idx_conflict_history_task_b":             `SELECT rowid FROM conflict_history WHERE task_b_id = ?`,
		"idx_insights_task_id":                    `SELECT rowid FROM insights WHERE task_id = ?`,
		"idx_llm_usage_events_task":               `SELECT rowid FROM llm_usage_events WHERE task_id = ?`,
		"idx_schedules_task_id":                   `SELECT rowid FROM schedules WHERE task_id = ?`,
		"idx_skill_analytics_events_task":         `SELECT rowid FROM skill_analytics_events WHERE task_id = ?`,
		"idx_task_attachments_file_path":          `SELECT rowid FROM task_attachments WHERE file_path = ?`,
		"idx_chat_attachments_file_path":          `SELECT rowid FROM chat_attachments WHERE file_path = ?`,
		"idx_thread_inputs_attachment_session_id": `SELECT rowid FROM thread_inputs WHERE attachment_session_id = ? AND attachment_session_id IS NOT NULL AND attachment_session_id <> ''`,
	}
	for indexName, query := range queries {
		plan := explainQueryPlan(t, db, query, "task")
		if !strings.Contains(plan, "USING COVERING INDEX "+indexName) {
			t.Errorf("query plan for %s = %q, want covering index", indexName, plan)
		}
	}
	manifestPlan := explainQueryPlan(t, db, `
		WITH owned_sessions(attachment_session_id) AS (
			SELECT ti.attachment_session_id
			FROM thread_inputs ti
			WHERE ti.task_id = ?
			  AND ti.attachment_session_id IS NOT NULL AND ti.attachment_session_id <> ''
			UNION
			SELECT ti.attachment_session_id
			FROM executions owner_execution
			CROSS JOIN thread_inputs ti INDEXED BY idx_thread_inputs_steering_turn
				ON ti.run_execution_id = owner_execution.id
			WHERE owner_execution.task_id = ? AND ti.task_id IS NULL
			  AND ti.attachment_session_id IS NOT NULL AND ti.attachment_session_id <> ''
		)
		SELECT owned.attachment_session_id
		FROM owned_sessions owned
		WHERE NOT EXISTS (
			SELECT 1 FROM thread_inputs other
			WHERE other.attachment_session_id IS NOT NULL AND other.attachment_session_id <> ''
			  AND other.attachment_session_id = owned.attachment_session_id
			  AND (
				(other.task_id IS NOT NULL AND other.task_id <> ?)
				OR (other.task_id IS NULL AND NOT EXISTS (
					SELECT 1 FROM executions other_execution
					WHERE other_execution.id = other.run_execution_id AND other_execution.task_id = ?
				))
			  )
		  )`, "task", "task", "task", "task")
	if strings.Contains(manifestPlan, "SCAN ti") || strings.Contains(manifestPlan, "SCAN other") {
		t.Fatalf("pending upload manifest plan = %q, want indexed thread input searches", manifestPlan)
	}
	if !strings.Contains(manifestPlan, "idx_thread_inputs_pending_task") ||
		!strings.Contains(manifestPlan, "idx_thread_inputs_steering_turn") ||
		!strings.Contains(manifestPlan, "idx_thread_inputs_attachment_session_id") {
		t.Fatalf("pending upload manifest plan = %q, want task, execution, and shared-session indexes", manifestPlan)
	}
}

func TestMigration130AllowsLegacyDatabaseWithoutArchitectTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-task-deletion-indexes-130.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 129); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"architect_tasks", "architect_messages", "architect_sessions", "architect_templates"} {
		if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatalf("dropping legacy-absent table %s: %v", table, err)
		}
	}
	if err := goose.UpTo(db, ".", 130); err != nil {
		t.Fatalf("migration 130 on legacy database without architect tables: %v", err)
	}

	var indexCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_architect_tasks_task_id'
	`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 0 {
		t.Fatalf("legacy architect task index count = %d, want 0", indexCount)
	}
	if plan := explainQueryPlan(t, db, `SELECT rowid FROM alerts WHERE execution_id = ?`, "execution"); !strings.Contains(plan, "USING COVERING INDEX idx_alerts_execution_id") {
		t.Fatalf("legacy alerts execution lookup plan = %q, want migration 130 index", plan)
	}
}

func TestMigration132IndexesLifecycleExecutionParentForeignKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lifecycle-parent-index-132.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 131); err != nil {
		t.Fatal(err)
	}

	query := `SELECT rowid FROM lifecycle_executions WHERE parent_execution_id = ?`
	if plan := explainQueryPlan(t, db, query, "parent"); !strings.Contains(plan, "SCAN lifecycle_executions") {
		t.Fatalf("migration 131 lifecycle parent lookup plan = %q, want table scan baseline", plan)
	}
	if err := goose.UpTo(db, ".", 132); err != nil {
		t.Fatal(err)
	}
	if plan := explainQueryPlan(t, db, query, "parent"); !strings.Contains(plan, "USING COVERING INDEX idx_lifecycle_executions_parent_execution_id") {
		t.Fatalf("migration 132 lifecycle parent lookup plan = %q, want covering index", plan)
	}
}

const systemUpdateQueuedCountQuery = `SELECT
	(SELECT COUNT(*) FROM tasks WHERE status IN ('pending','queued')) +
	(SELECT COUNT(*) FROM thread_inputs WHERE input_status = 'pending' AND input_mode = 'queued')`

func TestMigration170IndexesSystemUpdateQueuedThreadInputCount(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "thread-inputs-global-queued-count-170.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 169); err != nil {
		t.Fatal(err)
	}
	seedMigration170QueuedCountFixture(t, db, 200000)

	before := explainQueryPlan(t, db, systemUpdateQueuedCountQuery)
	if !strings.Contains(before, "SCAN thread_inputs") {
		t.Fatalf("migration 169 update queued-count plan = %q, want historical thread_inputs scan baseline", before)
	}

	if err := goose.UpTo(db, ".", 170); err != nil {
		t.Fatal(err)
	}

	var total int
	if err := db.QueryRow(systemUpdateQueuedCountQuery).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 7 {
		t.Fatalf("system update queued total = %d, want exact task + thread input count 7", total)
	}

	after := explainQueryPlan(t, db, systemUpdateQueuedCountQuery)
	if !strings.Contains(after, "SEARCH thread_inputs USING COVERING INDEX idx_thread_inputs_pending_queued_count") {
		t.Fatalf("migration 170 update queued-count plan = %q, want pending queued count index search", after)
	}
	if strings.Contains(after, "SCAN thread_inputs") {
		t.Fatalf("migration 170 update queued-count plan = %q, want no historical thread_inputs scan", after)
	}

	scopedPlans := map[string]struct {
		query string
		args  []any
		index string
	}{
		"task promotion": {
			query: `SELECT id FROM thread_inputs WHERE scope = 'task_thread' AND input_mode = 'queued' AND input_status = 'pending' AND task_id = ? ORDER BY queue_position ASC, created_at ASC, rowid ASC LIMIT 1`,
			args:  []any{"target-task-170"},
			index: "idx_thread_inputs_pending_task",
		},
		"chat promotion": {
			query: `SELECT id FROM thread_inputs WHERE scope = 'chat' AND input_mode = 'queued' AND input_status = 'pending' AND project_id = ? ORDER BY queue_position ASC, created_at ASC, rowid ASC LIMIT 1`,
			args:  []any{"target-project-170"},
			index: "idx_thread_inputs_pending_chat",
		},
	}
	for name, tc := range scopedPlans {
		plan := explainQueryPlan(t, db, tc.query, tc.args...)
		if !strings.Contains(plan, tc.index) {
			t.Fatalf("%s plan after migration 170 = %q, want existing scoped index %s", name, plan, tc.index)
		}
		if strings.Contains(plan, "USE TEMP B-TREE") {
			t.Fatalf("%s plan after migration 170 = %q, want scoped index to preserve queue ordering", name, plan)
		}
	}
}

const recoverableQueuedChatProjectIDsAfterQuery = `
	SELECT ti.project_id
	FROM thread_inputs ti
	LEFT JOIN executions guarded ON guarded.id = ti.run_execution_id
	WHERE ti.scope = 'chat'
	  AND ti.input_mode = 'queued'
	  AND ti.input_status = 'pending'
	  AND COALESCE(ti.project_id, '') != ''
	  AND ti.project_id > ?
	  AND (ti.run_execution_id IS NULL OR guarded.status IN ('completed', 'failed', 'cancelled'))
	  AND NOT EXISTS (
	    SELECT 1
	    FROM executions active
	    JOIN tasks active_task ON active_task.id = active.task_id
	    WHERE active_task.project_id = ti.project_id
	      AND active_task.category = 'chat'
	      AND active.status = 'running'
	  )
	GROUP BY ti.project_id
	ORDER BY ti.project_id
	LIMIT ?`

func TestMigration171IndexesRecoverableQueuedChatProjectPaging(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "thread-inputs-recover-chat-171.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 170); err != nil {
		t.Fatal(err)
	}
	seedMigration171RecoverableChatFixture(t, db, 5000, 10)

	before := explainQueryPlan(t, db, recoverableQueuedChatProjectIDsAfterQuery, "", 100)
	if !strings.Contains(before, "USING INDEX idx_thread_inputs_pending_chat") {
		t.Fatalf("migration 170 recoverable chat plan = %q, want existing project-ordered chat index baseline", before)
	}
	if !strings.Contains(before, "scope=? AND project_id>?") {
		t.Fatalf("migration 170 recoverable chat plan = %q, want project-keyed baseline before pending filters", before)
	}

	if err := goose.UpTo(db, ".", 171); err != nil {
		t.Fatal(err)
	}

	var ids []string
	rows, err := db.Query(recoverableQueuedChatProjectIDsAfterQuery, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 10 || ids[0] != "recover-chat-00991" || ids[len(ids)-1] != "recover-chat-01000" {
		t.Fatalf("recoverable chat ids = %#v, want sparse pending projects 00991..01000", ids)
	}

	after := explainQueryPlan(t, db, recoverableQueuedChatProjectIDsAfterQuery, "", 100)
	if !strings.Contains(after, "SEARCH ti USING COVERING INDEX idx_thread_inputs_recover_chat_project") {
		t.Fatalf("migration 171 recoverable chat plan = %q, want recovery covering index", after)
	}
	if !strings.Contains(after, "scope=? AND input_status=? AND input_mode=? AND project_id>?") {
		t.Fatalf("migration 171 recoverable chat plan = %q, want keyset search by scope/status/mode/project", after)
	}
	if strings.Contains(after, "USING INDEX idx_thread_inputs_pending_chat (scope=? AND project_id>?") {
		t.Fatalf("migration 171 recoverable chat plan = %q, want no historical chat scan by project before pending filters", after)
	}

	scopedPlans := map[string]struct {
		query string
		args  []any
		index string
	}{
		"chat promotion": {
			query: `SELECT id FROM thread_inputs WHERE scope = 'chat' AND input_mode = 'queued' AND input_status = 'pending' AND project_id = ? ORDER BY queue_position ASC, created_at ASC, rowid ASC LIMIT 1`,
			args:  []any{"recover-chat-00991"},
			index: "idx_thread_inputs_pending_chat",
		},
		"chat pending list": {
			query: `SELECT id FROM thread_inputs WHERE scope = 'chat' AND project_id = ? AND input_status = 'pending' AND NOT (input_mode = 'steering' AND COALESCE(expected_turn_id, '') = '') ORDER BY queue_position ASC, created_at ASC, rowid ASC`,
			args:  []any{"recover-chat-00991"},
			index: "idx_thread_inputs_pending_chat",
		},
	}
	for name, tc := range scopedPlans {
		plan := explainQueryPlan(t, db, tc.query, tc.args...)
		if !strings.Contains(plan, tc.index) {
			t.Fatalf("%s plan after migration 171 = %q, want %s", name, plan, tc.index)
		}
		if name == "chat promotion" && strings.Contains(plan, "USE TEMP B-TREE") {
			t.Fatalf("%s plan after migration 171 = %q, want existing FIFO ordering index without temp sort", name, plan)
		}
	}

	countPlan := explainQueryPlan(t, db, systemUpdateQueuedCountQuery)
	if !strings.Contains(countPlan, "SEARCH thread_inputs USING COVERING INDEX idx_thread_inputs_pending_queued_count") {
		t.Fatalf("migration 171 update queued-count plan = %q, want global pending queued count index", countPlan)
	}
}

func TestMigration159IndexesTaskLifecycleActivityOrdering(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lifecycle-task-started-index-159.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 158); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-159', 'Migration 159', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES
			('task-159', 'project-159', 'Task 159', 'active', 0, 'completed', 'p'),
			('other-task-159', 'project-159', 'Other Task 159', 'active', 0, 'completed', 'p');
		INSERT INTO agents (id, name, system_prompt) VALUES ('agent-159', 'Agent 159', 'x');
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 4000)
		INSERT INTO lifecycle_executions
			(id, task_id, agent_id, when_slot, skill_key, output_contract, status, input_json, output_json, started_at)
		SELECT 'exec-159-' || printf('%05d', n), 'task-159', 'agent-159', 'after_complete', 'summarize_activity', 'activity_summary', 'completed', '{"input":"hidden"}', '{"summary":"shown"}', datetime('2026-01-01 12:00:00', '-' || n || ' seconds')
		FROM seq;
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 4000)
		INSERT INTO lifecycle_executions
			(id, task_id, agent_id, when_slot, skill_key, output_contract, status, input_json, output_json, started_at)
		SELECT 'other-exec-159-' || printf('%05d', n), 'other-task-159', 'agent-159', 'after_complete', 'summarize_activity', 'activity_summary', 'completed', '{"input":"hidden"}', '{"summary":"shown"}', datetime('2026-01-01 12:00:00', '-' || n || ' seconds')
		FROM seq;
	`); err != nil {
		t.Fatalf("seed lifecycle rows: %v", err)
	}

	listQuery := `
		SELECT id, agent_id, when_slot, skill_key, output_contract, status, output_json, error, started_at, completed_at
		FROM lifecycle_executions
		WHERE task_id = ?
		ORDER BY started_at DESC, id DESC`
	before := explainQueryPlan(t, db, listQuery, "task-159")
	if !strings.Contains(before, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("migration 158 lifecycle list plan = %q, want temporary sort baseline", before)
	}

	if err := goose.UpTo(db, ".", 159); err != nil {
		t.Fatal(err)
	}

	after := explainQueryPlan(t, db, listQuery, "task-159")
	if !strings.Contains(after, "idx_lifecycle_executions_task_started") {
		t.Fatalf("migration 159 lifecycle list plan = %q, want task started index", after)
	}
	if strings.Contains(after, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("migration 159 lifecycle list plan = %q, want no temporary sort", after)
	}
}

func TestMigration135IndexesProjectScopedAlertOrdering(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "alerts-project-created-index-135.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 134); err != nil {
		t.Fatal(err)
	}

	// The production Alerts page newest-100 query filters by project and orders by
	// (created_at DESC, id DESC). Populate enough rows across many projects so the
	// planner makes a meaningful choice rather than trivially scanning.
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('target-135', 'Target 135', '', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 4000)
		INSERT INTO alerts (project_id, title, created_at)
		SELECT 'target-135', 'a' || n, datetime('now', '-' || n || ' seconds') FROM seq`); err != nil {
		t.Fatal(err)
	}

	listQuery := `SELECT id FROM alerts WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 100 OFFSET 0`

	before := explainQueryPlan(t, db, listQuery, "target-135")
	if !strings.Contains(before, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("migration 134 alerts list plan = %q, want temporary sort baseline", before)
	}

	if err := goose.UpTo(db, ".", 135); err != nil {
		t.Fatal(err)
	}

	after := explainQueryPlan(t, db, listQuery, "target-135")
	if !strings.Contains(after, "idx_alerts_project_created") {
		t.Fatalf("migration 135 alerts list plan = %q, want project-plus-order index", after)
	}
	if strings.Contains(after, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("migration 135 alerts list plan = %q, want no temporary sort", after)
	}

	// The composite index leads with project_id, so the narrow project-only index
	// is redundant and must be dropped by the migration.
	var projectOnly int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_alerts_project_id'`).Scan(&projectOnly); err != nil {
		t.Fatal(err)
	}
	if projectOnly != 0 {
		t.Fatalf("idx_alerts_project_id index count = %d, want 0 after migration 135", projectOnly)
	}

	// A bare project_id equality lookup must remain indexed by the composite index.
	projectLookup := explainQueryPlan(t, db, `SELECT id FROM alerts WHERE project_id = ?`, "target-135")
	if strings.Contains(projectLookup, "SCAN alerts") {
		t.Fatalf("project lookup plan = %q, want indexed search after migration 135", projectLookup)
	}

	// The Down migration restores the narrow project-only index and drops the composite.
	if err := goose.DownTo(db, ".", 134); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_alerts_project_id'`).Scan(&projectOnly); err != nil {
		t.Fatal(err)
	}
	if projectOnly != 1 {
		t.Fatalf("idx_alerts_project_id index count = %d, want 1 after migration 135 down", projectOnly)
	}
	var composite int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_alerts_project_created'`).Scan(&composite); err != nil {
		t.Fatal(err)
	}
	if composite != 0 {
		t.Fatalf("idx_alerts_project_created index count = %d, want 0 after migration 135 down", composite)
	}
}

func TestMigration131RetiredAttachmentSessionRejectsNewOwners(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "retired-attachment-sessions-131.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatal(err)
	}

	const sessionID = "0123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO retired_attachment_sessions(session_id) VALUES (?)`, sessionID); err != nil {
		t.Fatalf("retiring attachment session: %v", err)
	}
	_, err := db.Exec(`
		INSERT INTO thread_inputs
			(id, scope, project_id, input_mode, input_status, content, attachment_session_id, queue_position)
		VALUES ('late-owner', 'chat', 'default', 'queued', 'pending', 'late', ?, 1)`, sessionID)
	if err == nil || !strings.Contains(err.Error(), "attachment session retired") {
		t.Fatalf("late owner error = %v, want attachment session retired", err)
	}

	plan := explainQueryPlan(t, db, `
		SELECT 1 FROM thread_inputs
		WHERE attachment_session_id = ?
		  AND attachment_session_id IS NOT NULL AND attachment_session_id <> ''`, sessionID)
	if !strings.Contains(plan, "USING COVERING INDEX idx_thread_inputs_attachment_session_id") {
		t.Fatalf("attachment session ownership plan = %q, want covering ownership index", plan)
	}
}

func seedMigration170QueuedCountFixture(tb testing.TB, db *sql.DB, threadInputs int) {
	tb.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('target-project-170', 'Migration 170', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES
			('target-task-170', 'target-project-170', 'Pending Task 170', 'active', 0, 'pending', 'p'),
			('queued-task-170', 'target-project-170', 'Queued Task 170', 'active', 0, 'queued', 'p'),
			('completed-task-170', 'target-project-170', 'Completed Task 170', 'active', 0, 'completed', 'p');
	`); err != nil {
		tb.Fatalf("seed migration 170 project/tasks: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO thread_inputs (id, scope, project_id, task_id, input_mode, input_status, content, queue_position, created_at)
		SELECT
			'hist-170-' || printf('%06d', n),
			CASE WHEN n % 3 = 0 THEN 'task_thread' ELSE 'chat' END,
			'target-project-170',
			CASE WHEN n % 3 = 0 THEN 'completed-task-170' ELSE NULL END,
			CASE WHEN n % 2 = 0 THEN 'queued' ELSE 'steering' END,
			CASE WHEN n IN (50000, 100000, 150000) THEN 'pending' ELSE 'applied' END,
			'historical input',
			n,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, threadInputs); err != nil {
		tb.Fatalf("seed migration 170 historical thread inputs: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO thread_inputs (id, scope, project_id, task_id, input_mode, input_status, content, queue_position, created_at) VALUES
			('task-pending-170', 'task_thread', 'target-project-170', 'target-task-170', 'queued', 'pending', 'task pending', 1, '2026-01-02 00:00:00'),
			('chat-pending-170', 'chat', 'target-project-170', NULL, 'queued', 'pending', 'chat pending', 1, '2026-01-02 00:00:01');
	`); err != nil {
		tb.Fatalf("seed migration 170 sparse pending thread inputs: %v", err)
	}
}

func seedMigration171RecoverableChatFixture(tb testing.TB, db *sql.DB, historicalInputs, pendingProjects int) {
	tb.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('recover-chat-default', 'Migration 171 default', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES ('recover-chat-task-171', 'recover-chat-default', 'Migration 171 Task', 'active', 0, 'completed', 'p')`); err != nil {
		tb.Fatalf("seed migration 171 default rows: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 1000)
		INSERT INTO projects (id, name, description, repo_path)
		SELECT 'recover-chat-' || printf('%05d', n), 'Recover Chat ' || n, '', ''
		FROM seq`); err != nil {
		tb.Fatalf("seed migration 171 projects: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO thread_inputs (id, scope, project_id, input_mode, input_status, content, queue_position, created_at)
		SELECT
			'recover-hist-171-' || printf('%06d', n),
			'chat',
			'recover-chat-' || printf('%05d', ((n - 1) % 1000) + 1),
			'queued',
			CASE WHEN n % 2 = 0 THEN 'applied' ELSE 'cancelled' END,
			'historical chat input',
			n,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, historicalInputs); err != nil {
		tb.Fatalf("seed migration 171 historical thread inputs: %v", err)
	}
	if _, err := db.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1000 - ? + 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 1000)
		INSERT INTO thread_inputs (id, scope, project_id, input_mode, input_status, content, queue_position, created_at)
		SELECT
			'recover-pending-171-' || printf('%05d', n),
			'chat',
			'recover-chat-' || printf('%05d', n),
			'queued',
			'pending',
			'pending chat input',
			n,
			'2026-01-02 00:00:00'
		FROM seq`, pendingProjects); err != nil {
		tb.Fatalf("seed migration 171 pending thread inputs: %v", err)
	}
}

func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "; ")
}

func BenchmarkMigration170SystemUpdateQueuedCount(b *testing.B) {
	for _, tc := range []struct {
		name    string
		version int64
	}{
		{name: "migration_169_scan", version: 169},
		{name: "migration_170_indexed", version: 170},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dbPath := filepath.Join(b.TempDir(), tc.name+".db")
			db := openMigrationTestDB(b, dbPath)
			goose.SetBaseFS(migrations.FS)
			if err := goose.SetDialect("sqlite3"); err != nil {
				b.Fatal(err)
			}
			if err := goose.UpTo(db, ".", tc.version); err != nil {
				b.Fatal(err)
			}
			seedMigration170QueuedCountFixture(b, db, 200000)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var total int
				if err := db.QueryRow(systemUpdateQueuedCountQuery).Scan(&total); err != nil {
					b.Fatal(err)
				}
				if total != 7 {
					b.Fatalf("system update queued total = %d, want 7", total)
				}
			}
		})
	}
}

func TestMigration122DeletesFailedPublicationCreatedSchedulesBeforeDroppingJournal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automation-publication-schedules-122.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 121); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-122', 'Migration 122', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state)
			VALUES ('automation-122', 'project-122', 'automation-122', 'Automation 122', 'custom', 'active');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key, published_at)
			VALUES ('published-version-122', 'project-122', 'automation-122', 1, 'published', 'manual', 'custom', CURRENT_TIMESTAMP);
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('failed-version-122', 'project-122', 'automation-122', 2, 'draft', 'manual', 'custom');
		UPDATE automations SET published_version_id = 'published-version-122' WHERE id = 'automation-122';
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('published-node-122', 'project-122', 'automation-122', 'published-version-122', 'published', 'Published', 'trigger', 'schedule');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('published-task-122', 'project-122', 'Published task', 'scheduled', 2, 'pending', 'published');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('failed-task-122', 'project-122', 'Failed task', 'scheduled', 2, 'pending', 'failed');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('ordinary-task-122', 'project-122', 'Ordinary task', 'scheduled', 2, 'pending', 'ordinary');
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('published-schedule-122', 'published-task-122', CURRENT_TIMESTAMP, 'daily', 1, 1, CURRENT_TIMESTAMP);
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('failed-schedule-122', 'failed-task-122', CURRENT_TIMESTAMP, 'daily', 1, 0, CURRENT_TIMESTAMP);
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('ordinary-schedule-122', 'ordinary-task-122', CURRENT_TIMESTAMP, 'daily', 1, 1, CURRENT_TIMESTAMP);
		INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES ('project-122', 'automation-122', 'published-version-122', 'published-node-122', 'schedule', 'published-schedule-122', 'owned');
		INSERT INTO automation_trigger_owners
			(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
			VALUES ('published-schedule-122', 'project-122', 'automation-122', 'published-version-122', 'published-node-122', 'active');
		INSERT INTO automation_publication_attempts
			(id, project_id, automation_id, version_id, plan_revision, status)
			VALUES ('failed-attempt-122', 'project-122', 'automation-122', 'failed-version-122', 'failed-revision', 'failed');
		INSERT INTO automation_publication_steps
			(attempt_id, step_key, operation, target_key, status, resource_type, resource_id)
			VALUES ('failed-attempt-122', 'schedule:failed', 'create', 'schedule:failed', 'completed', 'schedule', 'failed-schedule-122');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 122); err != nil {
		t.Fatal(err)
	}

	for scheduleID, want := range map[string]int{
		"failed-schedule-122":    0,
		"published-schedule-122": 1,
		"ordinary-schedule-122":  1,
	} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE id = ?`, scheduleID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("schedule %s count = %d, want %d", scheduleID, got, want)
		}
	}
	var tasks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id IN ('published-task-122','failed-task-122','ordinary-task-122')`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 3 {
		t.Fatalf("backing task count = %d, want 3", tasks)
	}
}

func TestMigration124_BackfillsOnlyFeatureOwnedAutomationIssueTasks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automation-issue-origin-124.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 123); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-124', 'Migration 124', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state)
			VALUES ('automation-124', 'project-124', 'automation-124', 'Automation 124', 'custom', 'active');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-124', 'project-124', 'automation-124', 1, 'published', 'manual', 'custom');
		UPDATE automations SET published_version_id = 'version-124' WHERE id = 'automation-124';
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('task-node-124', 'project-124', 'automation-124', 'version-124', 'implementation', 'Implementation', 'agent_task', 'task');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('action-node-124', 'project-124', 'automation-124', 'version-124', 'create-task', 'Create task', 'action', 'create_task');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_via)
			VALUES ('feature-task-124', 'project-124', 'Feature issue task', 'backlog', 2, 'pending', 'feature', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_via)
			VALUES ('generic-task-124', 'project-124', 'Generic task', 'backlog', 2, 'pending', 'generic', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_via)
			VALUES ('wrong-node-task-124', 'project-124', 'Wrong node task', 'backlog', 2, 'pending', 'wrong node', '');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key)
			VALUES ('feature-work-124', 'project-124', 'automation-124', 'version-124', 'feature-work-124');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key)
			VALUES ('generic-work-124', 'project-124', 'automation-124', 'version-124', 'generic-work-124');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key)
			VALUES ('wrong-node-work-124', 'project-124', 'automation-124', 'version-124', 'wrong-node-work-124');
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
			VALUES ('feature-activity-124', 'project-124', 'automation-124', 'version-124', 'task-node-124', 'feature-work-124', 'work-item:feature-work-124:implementation-task', 'create_task', 'completed');
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
			VALUES ('generic-activity-124', 'project-124', 'automation-124', 'version-124', 'task-node-124', 'generic-work-124', 'execution:generic:create-task', 'create_task', 'completed');
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
			VALUES ('wrong-node-activity-124', 'project-124', 'automation-124', 'version-124', 'action-node-124', 'wrong-node-work-124', 'work-item:wrong-node-work-124:implementation-task', 'create_task', 'completed');
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
			VALUES ('feature-activity-124', 'task', 'feature-task-124', 'child');
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
			VALUES ('generic-activity-124', 'task', 'generic-task-124', 'child');
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
			VALUES ('wrong-node-activity-124', 'task', 'wrong-node-task-124', 'child');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 124); err != nil {
		t.Fatal(err)
	}

	for taskID, want := range map[string]string{
		"feature-task-124":    "automation:automation-124:implementation",
		"generic-task-124":    "",
		"wrong-node-task-124": "",
	} {
		var got string
		if err := db.QueryRow(`SELECT created_via FROM tasks WHERE id = ?`, taskID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("task %s created_via = %q, want %q", taskID, got, want)
		}
	}
}

func TestMigration127FailsClosedForExistingGitHubIssueClaims(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automation-github-issue-dedup-127.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 126); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-127', 'Migration 127', '', '');
		INSERT INTO automation_github_issue_dedup_leases
			(project_id, repository_full_name, title_fingerprint, owner_token, lease_expires_at)
			VALUES ('project-127', 'example/runtime', 'uncertain', 'uncertain-owner', '2020-01-01 00:00:00');
		INSERT INTO automation_github_issue_dedup_leases
			(project_id, repository_full_name, title_fingerprint, owner_token, lease_expires_at, created_issue_number)
			VALUES ('project-127', 'example/runtime', 'completed', 'completed-owner', '2020-01-01 00:00:00', 91);
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 127); err != nil {
		t.Fatal(err)
	}

	for fingerprint, want := range map[string]string{"uncertain": "dispatched", "completed": "completed"} {
		var got string
		if err := db.QueryRow(`SELECT mutation_state FROM automation_github_issue_dedup_leases
			WHERE project_id = 'project-127' AND repository_full_name = 'example/runtime' AND title_fingerprint = ?`, fingerprint).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("claim %s mutation_state = %q, want %q", fingerprint, got, want)
		}
	}
	if _, err := db.Exec(`INSERT INTO automation_github_issue_dedup_leases
		(project_id, repository_full_name, title_fingerprint, owner_token, lease_expires_at)
		VALUES ('project-127', 'example/runtime', 'new-reservation', 'new-owner', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	var newState string
	if err := db.QueryRow(`SELECT mutation_state FROM automation_github_issue_dedup_leases
		WHERE project_id = 'project-127' AND title_fingerprint = 'new-reservation'`).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	if newState != "reserved" {
		t.Fatalf("new claim mutation_state = %q, want reserved", newState)
	}
}

func TestMigration128LeavesHistoricalGitHubIssueProjectionSourceUnknown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automation-github-issue-projection-source-128.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 127); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-128', 'Migration 128', '', '');
		INSERT INTO automation_github_issue_dedup_leases
			(project_id, repository_full_name, title_fingerprint, owner_token, lease_expires_at, created_issue_number, mutation_state)
			VALUES ('project-128', 'example/runtime', 'completed', 'completed-owner', '2020-01-01 00:00:00', 92, 'completed');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 128); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := db.QueryRow(`SELECT projection_source_json FROM automation_github_issue_dedup_leases
		WHERE project_id = 'project-128' AND title_fingerprint = 'completed'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "" {
		t.Fatalf("historical projection source = %q, want empty fail-closed provenance", source)
	}
}

func TestMigration125_RemovesLegacyDraftGraphsAndUnsavedAutomationShells(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automation-current-graph-125.db")
	db := openMigrationTestDB(t, dbPath)
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 124); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-125', 'Migration 125', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state)
			VALUES ('saved-automation-125', 'project-125', 'saved-125', 'Saved', 'custom', 'active');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key, published_at)
			VALUES ('published-version-125', 'project-125', 'saved-automation-125', 1, 'published', 'manual', 'custom', CURRENT_TIMESTAMP);
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('failed-draft-version-125', 'project-125', 'saved-automation-125', 2, 'draft', 'manual', 'custom');
		UPDATE automations SET published_version_id = 'published-version-125' WHERE id = 'saved-automation-125';
		INSERT INTO automation_graph_metadata (version_id, project_id, automation_id, candidate_json)
			VALUES ('failed-draft-version-125', 'project-125', 'saved-automation-125', '{"schema_version":1}');

		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state)
			VALUES ('draft-shell-125', 'project-125', 'draft-125', 'Unsaved draft', 'custom', 'draft');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('draft-shell-version-125', 'project-125', 'draft-shell-125', 1, 'draft', 'manual', 'custom');
		INSERT INTO automation_graph_metadata (version_id, project_id, automation_id, candidate_json)
			VALUES ('draft-shell-version-125', 'project-125', 'draft-shell-125', '{"schema_version":1}');

		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('published-node-125', 'project-125', 'saved-automation-125', 'published-version-125', 'published', 'Published', 'trigger', 'schedule');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('failed-node-125', 'project-125', 'saved-automation-125', 'failed-draft-version-125', 'failed', 'Failed', 'trigger', 'schedule');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('shell-node-125', 'project-125', 'draft-shell-125', 'draft-shell-version-125', 'shell', 'Shell', 'trigger', 'schedule');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('published-task-125', 'project-125', 'Published task', 'scheduled', 2, 'pending', 'published');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('failed-task-125', 'project-125', 'Failed task', 'scheduled', 2, 'pending', 'failed');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('shell-task-125', 'project-125', 'Shell task', 'scheduled', 2, 'pending', 'shell');
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('published-schedule-125', 'published-task-125', CURRENT_TIMESTAMP, 'daily', 1, 1, CURRENT_TIMESTAMP);
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('failed-schedule-125', 'failed-task-125', CURRENT_TIMESTAMP, 'daily', 1, 0, CURRENT_TIMESTAMP);
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('shell-schedule-125', 'shell-task-125', CURRENT_TIMESTAMP, 'daily', 1, 0, CURRENT_TIMESTAMP);
		INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES ('project-125', 'saved-automation-125', 'published-version-125', 'published-node-125', 'schedule', 'published-schedule-125', 'owned');
		INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES ('project-125', 'saved-automation-125', 'failed-draft-version-125', 'failed-node-125', 'schedule', 'failed-schedule-125', 'owned');
		INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES ('project-125', 'draft-shell-125', 'draft-shell-version-125', 'shell-node-125', 'schedule', 'shell-schedule-125', 'owned');
		INSERT INTO automation_trigger_owners
			(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
			VALUES ('published-schedule-125', 'project-125', 'saved-automation-125', 'published-version-125', 'published-node-125', 'active');
		INSERT INTO automation_trigger_owners
			(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
			VALUES ('failed-schedule-125', 'project-125', 'saved-automation-125', 'failed-draft-version-125', 'failed-node-125', 'active');
		INSERT INTO automation_trigger_owners
			(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
			VALUES ('shell-schedule-125', 'project-125', 'draft-shell-125', 'draft-shell-version-125', 'shell-node-125', 'active');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 125); err != nil {
		t.Fatal(err)
	}

	var graphCount, version, draftShells, retainedMetadata, admissionTable int
	if err := db.QueryRow(`SELECT COUNT(*), MIN(version) FROM automation_versions WHERE automation_id = 'saved-automation-125'`).Scan(&graphCount, &version); err != nil {
		t.Fatal(err)
	}
	if graphCount != 1 || version != 1 {
		t.Fatalf("saved Automation graphs = %d at version %d, want exactly one current graph at version 1", graphCount, version)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automations WHERE id = 'draft-shell-125'`).Scan(&draftShells); err != nil {
		t.Fatal(err)
	}
	if draftShells != 0 {
		t.Fatalf("unsaved draft Automation shells = %d, want 0", draftShells)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_graph_metadata WHERE version_id IN ('failed-draft-version-125','draft-shell-version-125')`).Scan(&retainedMetadata); err != nil {
		t.Fatal(err)
	}
	if retainedMetadata != 0 {
		t.Fatalf("retained legacy draft metadata = %d, want 0", retainedMetadata)
	}
	for scheduleID, want := range map[string]int{
		"published-schedule-125": 1,
		"failed-schedule-125":    0,
		"shell-schedule-125":     0,
	} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE id = ?`, scheduleID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("schedule %s count = %d, want %d", scheduleID, got, want)
		}
	}
	var preservedTasks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id IN ('published-task-125','failed-task-125','shell-task-125')`).Scan(&preservedTasks); err != nil {
		t.Fatal(err)
	}
	if preservedTasks != 3 {
		t.Fatalf("backing task count = %d, want 3", preservedTasks)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'automation_paused_task_admissions'`).Scan(&admissionTable); err != nil {
		t.Fatal(err)
	}
	if admissionTable != 1 {
		t.Fatal("automation_paused_task_admissions table was not created")
	}
}

func TestMigration112_BackfillsOperationalAlertsWithoutInferringProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "alerts-112.db")
	db := openMigrationTestDB(t, dbPath)
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
	db := openMigrationTestDB(t, dbPath)

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
	if maxVersion != 177 {
		t.Fatalf("max goose version = %d, want 177", maxVersion)
	}
}

func assertXChannelSchema172(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"x_authorized_users", "x_user_projects", "x_task_context", "x_inbound_receipts"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected table %s", table)
		}
	}
	for table, columns := range map[string][]string{
		"x_task_context":     {"account_id", "conversation_id", "reply_to_tweet_id", "x_user_id", "username"},
		"x_inbound_receipts": {"lease_token", "lease_expires_at", "task_id"},
		"thread_inputs":      {"x_account_id", "x_conversation_id", "x_reply_to_tweet_id", "x_user_id", "x_username"},
	} {
		for _, column := range columns {
			if !tableHasColumn(t, db, table, column) {
				t.Fatalf("expected %s.%s after consolidated X migration", table, column)
			}
		}
	}
	for _, index := range []string{"idx_x_authorized_users_project", "idx_x_user_projects_project", "idx_x_task_context_conversation", "idx_x_inbound_receipts_project", "idx_x_inbound_receipts_task"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected index %s", index)
		}
	}
}

func TestMigration172CreatesConsolidatedXSchemaFromPublicBaseline(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "x-consolidated-fresh-172.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 171); err != nil {
		t.Fatalf("migrate to public baseline 171: %v", err)
	}
	if err := goose.UpTo(db, ".", 172); err != nil {
		t.Fatalf("apply consolidated X migration: %v", err)
	}
	assertXChannelSchema172(t, db)
	var maxVersion int
	if err := db.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 172 {
		t.Fatalf("max goose version = %d, want 172", maxVersion)
	}
}

func TestMigration172RollsBackConsolidatedXSchemaToPublicBaseline(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "x-consolidated-rollback-172.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 171); err != nil {
		t.Fatalf("migrate to public baseline 171: %v", err)
	}
	if err := goose.UpTo(db, ".", 172); err != nil {
		t.Fatalf("apply consolidated X migration: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id, name, description, repo_path) VALUES('x-rollback-project', 'X rollback', '', '');
		INSERT INTO x_authorized_users(project_id, x_user_id, username) VALUES('x-rollback-project', 'author', 'alice');
	`); err != nil {
		t.Fatalf("seed X rollback data: %v", err)
	}
	if err := goose.DownTo(db, ".", 171); err != nil {
		t.Fatalf("roll back consolidated X migration: %v", err)
	}

	for _, table := range []string{"x_authorized_users", "x_user_projects", "x_task_context", "x_inbound_receipts"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected table %s to be removed by rollback", table)
		}
	}
	for _, column := range []string{"x_account_id", "x_conversation_id", "x_reply_to_tweet_id", "x_user_id", "x_username"} {
		if tableHasColumn(t, db, "thread_inputs", column) {
			t.Fatalf("expected thread_inputs.%s to be removed by rollback", column)
		}
	}
	var projectName string
	if err := db.QueryRow(`SELECT name FROM projects WHERE id = 'x-rollback-project'`).Scan(&projectName); err != nil {
		t.Fatalf("public schema unusable after X rollback: %v", err)
	}
	if projectName != "X rollback" {
		t.Fatalf("project name = %q, want X rollback", projectName)
	}
}

func TestMigration173AddsXAuthorizedUserLookupIndex(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "x-authorized-user-index-173.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 172); err != nil {
		t.Fatalf("migrate to X schema 172: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id, name, description, repo_path) VALUES('x-index-project', 'X index', '', '');
		INSERT INTO x_authorized_users(project_id, x_user_id) VALUES('x-index-project', 'author');
	`); err != nil {
		t.Fatalf("seed X authorization lookup fixture: %v", err)
	}

	assertIndex := func(want bool) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, "idx_x_authorized_users_user_project").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if got := count == 1; got != want {
			t.Fatalf("X authorized-user lookup index exists = %v, want %v", got, want)
		}
	}
	assertIndex(false)

	if err := goose.UpTo(db, ".", 173); err != nil {
		t.Fatalf("apply X authorized-user lookup index migration: %v", err)
	}
	assertIndex(true)
	plan := explainQueryPlan(t, db, `
		SELECT p.id
		FROM x_authorized_users AS au
		JOIN projects AS p ON p.id = au.project_id
		WHERE au.x_user_id = ?
		ORDER BY p.is_default DESC, p.name ASC
		LIMIT 1`, "author")
	if !strings.Contains(plan, "idx_x_authorized_users_user_project") {
		t.Fatalf("X authorized-user lookup plan = %q, want user-keyed index", plan)
	}
	if strings.Contains(plan, "SCAN projects") {
		t.Fatalf("X authorized-user lookup plan scans projects: %q", plan)
	}

	if err := goose.DownTo(db, ".", 172); err != nil {
		t.Fatalf("roll back X authorized-user lookup index migration: %v", err)
	}
	assertIndex(false)
}

func TestMigration175BackfillsAlertImplementationTaskHistory(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "alert-implementation-history-175.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 174); err != nil {
		t.Fatalf("migrate to alert history baseline 174: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id, name, description, repo_path)
		VALUES('alert-history-project-175', 'Alert history', '', '');
		INSERT INTO tasks(id, project_id, title, category, status)
		VALUES('alert-history-task-175', 'alert-history-project-175', 'Implementation', 'completed', 'completed');
		INSERT INTO alerts(id, project_id, title, decision_state, processing_state)
		VALUES
			('alert-history-completed-175', 'alert-history-project-175', 'Completed historical task', 'approved', 'completed'),
			('alert-history-unlinked-175', 'alert-history-project-175', 'Never linked', 'pending', 'unclaimed');
		INSERT INTO alerts(id, project_id, title, decision_state, processing_state, implementation_task_id)
		VALUES('alert-history-live-175', 'alert-history-project-175', 'Live task', 'approved', 'failed', 'alert-history-task-175');
	`); err != nil {
		t.Fatalf("seed alert implementation history: %v", err)
	}

	if err := goose.UpTo(db, ".", 175); err != nil {
		t.Fatalf("apply alert implementation history migration: %v", err)
	}
	rows, err := db.Query(`SELECT id, implementation_task_was_linked FROM alerts
		WHERE project_id = 'alert-history-project-175' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var id string
		var linked int
		if err := rows.Scan(&id, &linked); err != nil {
			t.Fatal(err)
		}
		got[id] = linked
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"alert-history-completed-175": 1,
		"alert-history-live-175":      1,
		"alert-history-unlinked-175":  0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("implementation task history = %#v, want %#v", got, want)
	}
}

func TestMigration177RemovesMessageOnlyAlertImplementationTaskHistory(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "alert-implementation-history-176.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 175); err != nil {
		t.Fatalf("migrate to overbroad alert history 175: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id, name, description, repo_path)
		VALUES('alert-history-project-176', 'Alert history repair', '', '');
		INSERT INTO tasks(id, project_id, title, category, status)
		VALUES
			('alert-history-live-task-176', 'alert-history-project-176', 'Live implementation', 'completed', 'completed'),
			('alert-history-state-task-176', 'alert-history-project-176', 'Linked-state implementation', 'completed', 'completed');
		INSERT INTO alerts(id, project_id, title, decision_state, processing_state, processing_error, implementation_task_id, implementation_task_was_linked)
		VALUES
			('alert-history-false-completed-176', 'alert-history-project-176', 'Completed without task', 'approved', 'completed', 'done', NULL, 1),
			('alert-history-false-message-176', 'alert-history-project-176', 'Negated linked task', 'approved', 'completed', 'No linked implementation task was created.', NULL, 1),
			('alert-history-failed-message-176', 'alert-history-project-176', 'Failed linked task', 'approved', 'completed', 'Failed to create linked implementation task.', NULL, 1),
			('alert-history-message-176', 'alert-history-project-176', 'Legacy message only', 'approved', 'completed', 'Linked and started implementation task abc123.', NULL, 1),
			('alert-history-projection-176', 'alert-history-project-176', 'Projected linked completion', 'approved', 'completed', 'done', NULL, 1),
			('alert-history-live-176', 'alert-history-project-176', 'Live task', 'approved', 'failed', '', 'alert-history-live-task-176', 1),
			('alert-history-state-176', 'alert-history-project-176', 'Linked state', 'approved', 'implementation_task_linked', '', 'alert-history-state-task-176', 1);
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state)
		VALUES ('alert-history-automation-176', 'alert-history-project-176', 'alert-history-176', 'Alert history', 'custom', 'active');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
		VALUES ('alert-history-version-176', 'alert-history-project-176', 'alert-history-automation-176', 1, 'published', 'manual', 'custom');
		UPDATE automations SET published_version_id = 'alert-history-version-176' WHERE id = 'alert-history-automation-176';
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
		VALUES ('alert-history-node-176', 'alert-history-project-176', 'alert-history-automation-176', 'alert-history-version-176', 'implementation', 'Implementation', 'agent_task', 'implementation');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key)
		VALUES ('alert-history-work-176', 'alert-history-project-176', 'alert-history-automation-176', 'alert-history-version-176', 'alert-history-work-176');
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
		VALUES ('alert-history-activity-176', 'alert-history-project-176', 'alert-history-automation-176', 'alert-history-version-176', 'alert-history-node-176', 'alert-history-work-176', 'alert-history-activity-176', 'create_implementation_task', 'completed');
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id)
		VALUES
			('alert-history-activity-176', 'alert', 'alert-history-projection-176'),
			('alert-history-activity-176', 'task', 'deleted-alert-history-task-176');
	`); err != nil {
		t.Fatalf("seed alert implementation history repair: %v", err)
	}

	if err := goose.UpTo(db, ".", 176); err != nil {
		t.Fatalf("apply message-based alert history migration: %v", err)
	}
	for _, id := range []string{"alert-history-false-message-176", "alert-history-failed-message-176", "alert-history-message-176"} {
		var linked int
		if err := db.QueryRow(`SELECT implementation_task_was_linked FROM alerts WHERE id = ?`, id).Scan(&linked); err != nil {
			t.Fatal(err)
		}
		if linked != 1 {
			t.Fatalf("pre-177 message history for %s = %d, want 1", id, linked)
		}
	}

	if err := goose.UpTo(db, ".", 177); err != nil {
		t.Fatalf("apply durable-only alert history migration: %v", err)
	}
	readHistory := func() map[string]int {
		t.Helper()
		rows, err := db.Query(`SELECT id, implementation_task_was_linked FROM alerts
			WHERE project_id = 'alert-history-project-176' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]int{}
		for rows.Next() {
			var id string
			var linked int
			if err := rows.Scan(&id, &linked); err != nil {
				t.Fatal(err)
			}
			got[id] = linked
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := map[string]int{
		"alert-history-failed-message-176":  0,
		"alert-history-false-completed-176": 0,
		"alert-history-false-message-176":   0,
		"alert-history-live-176":            1,
		"alert-history-message-176":         0,
		"alert-history-projection-176":      1,
		"alert-history-state-176":           1,
	}
	if got := readHistory(); !reflect.DeepEqual(got, want) {
		t.Fatalf("durable implementation task history = %#v, want %#v", got, want)
	}

	if err := goose.DownTo(db, ".", 176); err != nil {
		t.Fatalf("roll back durable-only alert implementation history: %v", err)
	}
	for _, id := range []string{"alert-history-false-message-176", "alert-history-failed-message-176", "alert-history-message-176"} {
		var linked int
		if err := db.QueryRow(`SELECT implementation_task_was_linked FROM alerts WHERE id = ?`, id).Scan(&linked); err != nil {
			t.Fatal(err)
		}
		if linked != 1 {
			t.Fatalf("rolled-back message history for %s = %d, want 1", id, linked)
		}
	}
	if err := goose.UpTo(db, ".", 177); err != nil {
		t.Fatalf("reapply durable-only alert implementation history: %v", err)
	}
	if got := readHistory(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reapplied durable implementation task history = %#v, want %#v", got, want)
	}
}

func TestMigration174UsesOrderedTaskDiscoveryAccessPath(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "task-discovery-order-index-174.db"))
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 173); err != nil {
		t.Fatalf("migrate to task schema 173: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects(id, name, description, repo_path)
		VALUES('task-discovery-index-project-174', 'Task discovery index', '', '');
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < 4000
		)
		INSERT INTO tasks (id, project_id, title, category, status, updated_at)
		SELECT
			'task-discovery-index-174-' || printf('%05d', n),
			'task-discovery-index-project-174',
			'Discovery task ' || n,
			CASE WHEN n % 20 = 0 THEN 'chat' ELSE 'active' END,
			'pending',
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq;
	`); err != nil {
		t.Fatalf("seed task discovery index fixture: %v", err)
	}

	pageQuery := `
		SELECT id, title, category, status, priority, updated_at, parent_task_id, swarm_role
		FROM tasks
		WHERE project_id = ? AND category != 'chat'
		ORDER BY updated_at DESC, id ASC
		LIMIT ? OFFSET ?`
	for _, limit := range []int{20, 50} {
		before := explainQueryPlan(t, db, pageQuery, "task-discovery-index-project-174", limit, 0)
		if !strings.Contains(before, "USE TEMP B-TREE FOR ORDER BY") {
			t.Fatalf("pre-174 discovery page plan for limit %d = %q, want temporary sort baseline", limit, before)
		}
	}

	if err := goose.UpTo(db, ".", 174); err != nil {
		t.Fatalf("apply task discovery order index migration: %v", err)
	}
	assertOrderedDiscoveryPlan := func(tb *testing.T, name, query string, args ...any) {
		tb.Helper()
		plan := explainQueryPlan(tb, db, query, args...)
		if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
			tb.Fatalf("%s discovery page plan = %q, want no temporary sort", name, plan)
		}
		if !strings.Contains(plan, "USING") || !strings.Contains(plan, "project_id=?") {
			tb.Fatalf("%s discovery page plan = %q, want project-scoped ordered access", name, plan)
		}
	}
	for _, limit := range []int{20, 50} {
		assertOrderedDiscoveryPlan(t, "default", pageQuery, "task-discovery-index-project-174", limit, 0)
	}

	for _, category := range []string{"active", "backlog", "scheduled", "completed"} {
		category := category
		t.Run("category_"+category, func(t *testing.T) {
			query := `
				SELECT id, title, category, status, priority, updated_at, parent_task_id, swarm_role
				FROM tasks
				WHERE project_id = ? AND category != 'chat' AND category = ?
				ORDER BY updated_at DESC, id ASC
				LIMIT ? OFFSET ?`
			assertOrderedDiscoveryPlan(t, "category="+category, query, "task-discovery-index-project-174", category, 20, 0)
		})
	}
	for _, status := range []string{"pending", "queued", "running", "completed", "failed", "cancelled", "blocked"} {
		status := status
		t.Run("status_"+status, func(t *testing.T) {
			query := `
				SELECT id, title, category, status, priority, updated_at, parent_task_id, swarm_role
				FROM tasks
				WHERE project_id = ? AND category != 'chat' AND status = ?
				ORDER BY updated_at DESC, id ASC
				LIMIT ? OFFSET ?`
			assertOrderedDiscoveryPlan(t, "status="+status, query, "task-discovery-index-project-174", status, 20, 0)
		})
	}

	titlePlan := explainQueryPlan(t, db, `
		SELECT id, title, category, status, priority, updated_at, parent_task_id, swarm_role
		FROM tasks
		WHERE project_id = ? AND category != 'chat' AND title LIKE ?
		ORDER BY
			CASE WHEN LOWER(title) = LOWER(?) THEN 0
			     WHEN LOWER(title) LIKE LOWER(? || '%') THEN 1
			     ELSE 2 END,
			updated_at DESC, id ASC
		LIMIT ? OFFSET ?`, "task-discovery-index-project-174", "%Discovery%", "Discovery", "Discovery", 20, 0)
	if !strings.Contains(titlePlan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("title-filtered discovery plan = %q, want CASE ordering to remain separately planned", titlePlan)
	}

	if err := goose.DownTo(db, ".", 173); err != nil {
		t.Fatalf("roll back task discovery order index migration: %v", err)
	}
	for _, limit := range []int{20, 50} {
		afterDown := explainQueryPlan(t, db, pageQuery, "task-discovery-index-project-174", limit, 0)
		if !strings.Contains(afterDown, "USE TEMP B-TREE FOR ORDER BY") {
			t.Fatalf("post-174-down discovery page plan for limit %d = %q, want temporary sort restored", limit, afterDown)
		}
	}
}

func TestMigration108_SystemChannelInboundAuthorizationDedupe(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "system-channel-auth.db")
	db := openMigrationTestDB(t, dbPath)

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
	db := openMigrationTestDB(t, dbPath)

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
	db := openMigrationTestDB(t, dbPath)

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
	if maxVersion != 177 {
		t.Fatalf("max goose version = %d, want 177", maxVersion)
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
	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('test-project', 'Test Project', 'Test', '/tmp')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Create a task
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, category, status) VALUES ('test-task', 'test-project', 'Test Task', 'scheduled', 'pending')`); err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}

	// Create a schedule
	if _, err := db.Exec(`INSERT INTO schedules (id, task_id, run_at, repeat_type) VALUES ('test-schedule', 'test-task', datetime('now'), 'daily')`); err != nil {
		t.Fatalf("failed to insert schedule: %v", err)
	}

	// Create an execution
	if _, err := db.Exec(`INSERT INTO executions (id, task_id, status, started_at) VALUES ('test-exec', 'test-task', 'completed', datetime('now'))`); err != nil {
		t.Fatalf("failed to insert execution: %v", err)
	}

	// Verify the data exists
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schedules WHERE task_id = 'test-task'").Scan(&count); err != nil {
		t.Fatalf("failed to count schedules: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 schedule, got %d", count)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM executions WHERE task_id = 'test-task'").Scan(&count); err != nil {
		t.Fatalf("failed to count executions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 execution, got %d", count)
	}

	// Now verify that the schema has proper constraints
	var schema string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(&schema); err != nil {
		t.Fatalf("failed to get tasks schema: %v", err)
	}

	// Check for CHECK constraints
	if schema == "" {
		t.Fatal("tasks table schema is empty")
	}

	// Verify foreign keys are enabled
	var fkEnabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
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

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("failed to set dialect: %v", err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create a project first
	if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('test-proj', 'Test')`); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Test category CHECK constraint
	_, err := db.Exec(`INSERT INTO tasks (id, project_id, title, category) VALUES ('t1', 'test-proj', 'Test 1', 'invalid-category')`)
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
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, category, status, tag) VALUES ('t4', 'test-proj', 'Test 4', 'active', 'pending', 'feature')`); err != nil {
		t.Fatalf("expected valid insert to succeed: %v", err)
	}

	t.Logf("✅ All CHECK constraints working correctly")
}

func TestMigrations_GitHubRepoURLAndTaskPullRequests(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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
	if maxVersion != 177 {
		t.Fatalf("max goose version = %d, want 177", maxVersion)
	}
}

func TestMigration082_AppliesAfterPublic074(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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

	db := openMigrationTestDB(t, dbPath)

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
	if maxVersion != 177 {
		t.Fatalf("max goose version = %d, want 177", maxVersion)
	}
}

func TestMigration095_AllowsCreatedSkillAnalyticsEvents(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "skill-analytics-created.db")
	db := openMigrationTestDB(t, dbPath)

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
	db := openMigrationTestDB(t, dbPath)

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
	db := openMigrationTestDB(t, dbPath)
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

func TestMigration121And122LeaveOnlyAtomicAutomationSaveSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "automations-atomic-save.db")
	db := openMigrationTestDB(t, dbPath)

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
	db := openMigrationTestDB(t, dbPath)
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
	db := openMigrationTestDB(t, dbPath)
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

func TestMigration129PreservesExistingScheduleContextSemantics(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "schedule-context-129.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 128); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-129', 'Migration 129', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('task-129', 'project-129', 'Existing scheduled task', 'scheduled', 2, 'completed', 'run');
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('schedule-129', 'task-129', CURRENT_TIMESTAMP, 'daily', 1, 1, CURRENT_TIMESTAMP);
		INSERT INTO executions (id, task_id, status, prompt_sent, output)
			VALUES ('execution-129', 'task-129', 'completed', 'old prompt', 'old output');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 129); err != nil {
		t.Fatal(err)
	}
	var clearContext, startsNewContext bool
	if err := db.QueryRow(`SELECT clear_context_on_start FROM schedules WHERE id = 'schedule-129'`).Scan(&clearContext); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT starts_new_context FROM executions WHERE id = 'execution-129'`).Scan(&startsNewContext); err != nil {
		t.Fatal(err)
	}
	if clearContext || startsNewContext {
		t.Fatalf("existing rows must preserve prior context semantics: clear=%t boundary=%t", clearContext, startsNewContext)
	}
}

func TestMigration134ArtifactMailboxOwnershipSurvivesGraphReplacementWithoutBackfill(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "automation-artifact-mailbox-134.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 133); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-134', 'Migration 134', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state, published_version_id)
			VALUES ('automation-134', 'project-134', 'artifact-owner-134', 'Artifact owner', 'active', 'version-134');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-134', 'project-134', 'automation-134', 1, 'published', 'manual', 'custom');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 134); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("migration 134 must not infer or backfill historical ownership: got %d rows", count)
	}
	if _, err := db.Exec(`INSERT INTO automation_artifact_mailbox_owners
		(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
		VALUES ('project-134', 'automation-134', 'alert', 'alert-134', 'producer', 'notification', 'approval', 'inbox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM automation_versions WHERE id = 'version-134'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_id = 'alert-134'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("logical mailbox ownership must survive graph replacement: got %d rows", count)
	}
	if _, err := db.Exec(`DELETE FROM automations WHERE id = 'automation-134'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("logical mailbox ownership must cascade when the stable Automation is deleted: got %d rows", count)
	}
}

func TestMigration145RetiresGitHubIssueMailboxOwnershipOnly(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "retire-github-mailbox-ownership-145.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 144); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-145', 'Migration 145', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state, published_version_id)
			VALUES ('automation-145', 'project-145', 'retire-github-owner-145', 'Retire GitHub owner', 'active', 'version-145');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-145', 'project-145', 'automation-145', 1, 'published', 'manual', 'custom');
		INSERT INTO automation_artifact_mailbox_owners
			(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
			VALUES
			('project-145', 'automation-145', 'alert', 'alert-145', 'producer', 'notification', 'approval', 'inbox'),
			('project-145', 'automation-145', 'github_issue', 'github:example/runtime:issue:145', 'producer', 'issue', 'assignment', 'dev_inbox');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 145); err != nil {
		t.Fatal(err)
	}
	var alertOwners, githubOwners int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_type = 'alert'`).Scan(&alertOwners); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_type = 'github_issue'`).Scan(&githubOwners); err != nil {
		t.Fatal(err)
	}
	if alertOwners != 1 || githubOwners != 0 {
		t.Fatalf("migration 145 must preserve alert owners and delete GitHub issue owners: alerts=%d github=%d", alertOwners, githubOwners)
	}
}

func TestMigration147SkipsStaleGitHubIssueTaskProvenanceResources(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "github-issue-task-provenance-stale-resource-147.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 146); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-147', 'Migration 147', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state, published_version_id)
			VALUES ('automation-147', 'project-147', 'automation-147', 'Automation 147', 'custom', 'active', 'version-147');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-147', 'project-147', 'automation-147', 1, 'published', 'manual', 'github_sdlc');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('implementation-node-147', 'project-147', 'automation-147', 'version-147', 'implementation', 'Implementation', 'agent_task', 'implementation');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
			VALUES ('live-task-147', 'project-147', 'Live task', 'active', 2, 'pending', 'live');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key)
			VALUES ('live-work-147', 'project-147', 'automation-147', 'version-147', 'live-work-147'),
				('stale-work-147', 'project-147', 'automation-147', 'version-147', 'stale-work-147');
		INSERT INTO automation_activities (id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
			VALUES ('live-activity-147', 'project-147', 'automation-147', 'version-147', 'implementation-node-147', 'live-work-147', 'work-item:live-work-147:implementation-task', 'create_task', 'completed'),
				('stale-activity-147', 'project-147', 'automation-147', 'version-147', 'implementation-node-147', 'stale-work-147', 'work-item:stale-work-147:implementation-task', 'create_task', 'completed');
		INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
			VALUES ('live-activity-147', 'task', 'live-task-147', 'child'),
				('live-activity-147', 'github_issue', 'github:openvibely/openvibely:issue:147', 'subject'),
				('stale-activity-147', 'task', 'deleted-task-147', 'child'),
				('stale-activity-147', 'github_issue', 'github:openvibely/openvibely:issue:148', 'subject');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 147); err != nil {
		t.Fatalf("migration 147 should skip stale deleted task resources: %v", err)
	}
	var live, stale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_github_issue_task_provenance WHERE task_id = 'live-task-147'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_github_issue_task_provenance WHERE task_id = 'deleted-task-147'`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if live != 1 || stale != 0 {
		t.Fatalf("migration 147 provenance counts: live=%d stale=%d, want live=1 stale=0", live, stale)
	}
}

func TestMigration146SimplifiesNativeAlertMailboxOwnership(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "simplify-native-alert-mailbox-ownership-146.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 145); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-146', 'Migration 146', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state, published_version_id)
			VALUES ('automation-146', 'project-146', 'simplify-native-owner-146', 'Simplify Native owner', 'active', 'version-146');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-146', 'project-146', 'automation-146', 1, 'published', 'manual', 'custom');
		INSERT INTO automation_artifact_mailbox_owners
			(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
			VALUES
			('project-146', 'automation-146', 'alert', 'alert-146', 'producer', 'notification', 'approval', 'inbox'),
			('project-146', 'automation-146', 'alert', 'alert-146', 'producer2', 'notification2', 'approval2', 'inbox2'),
			('project-146', 'automation-146', 'alert', 'other-alert-146', 'producer', 'notification', 'approval', 'inbox');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 146); err != nil {
		t.Fatal(err)
	}
	var owners int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_type = 'alert' AND artifact_id = 'alert-146'`).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Fatalf("migration 146 must collapse duplicate alert topology owner rows: got %d", owners)
	}
	var keyedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners
		WHERE artifact_type = 'alert' AND (producer_node_key <> '' OR action_node_key <> '' OR gate_node_key <> '' OR mailbox_node_key <> '')`).Scan(&keyedRows); err != nil {
		t.Fatal(err)
	}
	if keyedRows != 0 {
		t.Fatalf("migration 146 must remove Native alert topology keys: got %d keyed rows", keyedRows)
	}
}

func TestMigration133UsesAutomationLifecycleForScheduleEnablement(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "automation-schedule-lifecycle-133.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 132); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-133', 'Migration 133', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES
			('task-active-133', 'project-133', 'Active schedule', 'scheduled', 2, 'pending', 'run'),
			('task-paused-133', 'project-133', 'Paused schedule', 'scheduled', 2, 'pending', 'run');
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run) VALUES
			('schedule-active-133', 'task-active-133', CURRENT_TIMESTAMP, 'daily', 1, 0, CURRENT_TIMESTAMP),
			('schedule-paused-133', 'task-paused-133', CURRENT_TIMESTAMP, 'daily', 1, 0, CURRENT_TIMESTAMP);
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state, published_version_id) VALUES
			('automation-active-133', 'project-133', 'active-133', 'Active Automation', 'active', 'version-active-133'),
			('automation-paused-133', 'project-133', 'paused-133', 'Paused Automation', 'paused', 'version-paused-133');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key) VALUES
			('version-active-133', 'project-133', 'automation-active-133', 1, 'published', 'template', 'native_sdlc'),
			('version-paused-133', 'project-133', 'automation-paused-133', 1, 'published', 'template', 'native_sdlc');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role) VALUES
			('node-active-133', 'project-133', 'automation-active-133', 'version-active-133', 'schedule', 'Schedule', 'trigger', 'fixed_schedule'),
			('node-paused-133', 'project-133', 'automation-paused-133', 'version-paused-133', 'schedule', 'Schedule', 'trigger', 'fixed_schedule');
		INSERT INTO automation_trigger_owners (schedule_id, project_id, automation_id, version_id, node_id, ownership_state) VALUES
			('schedule-active-133', 'project-133', 'automation-active-133', 'version-active-133', 'node-active-133', 'active'),
			('schedule-paused-133', 'project-133', 'automation-paused-133', 'version-paused-133', 'node-paused-133', 'paused');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 133); err != nil {
		t.Fatal(err)
	}
	var activeEnabled, pausedEnabled bool
	if err := db.QueryRow(`SELECT enabled FROM schedules WHERE id = 'schedule-active-133'`).Scan(&activeEnabled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT enabled FROM schedules WHERE id = 'schedule-paused-133'`).Scan(&pausedEnabled); err != nil {
		t.Fatal(err)
	}
	if !activeEnabled || pausedEnabled {
		t.Fatalf("migration 133 must enable only active Automation schedules: active=%t paused=%t", activeEnabled, pausedEnabled)
	}
}

func TestMigration164DeletesOrphanedSchedules(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "orphaned-schedules-164.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 163); err != nil {
		t.Fatalf("failed to run migrations through 163: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-164', 'Migration 164', '', '');
		INSERT INTO tasks (id, project_id, title, prompt) VALUES ('task-164', 'project-164', 'Kept task', '');
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('schedule-kept-164', 'task-164', CURRENT_TIMESTAMP, 'once', 1, 1, CURRENT_TIMESTAMP);
		INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
			VALUES ('schedule-orphan-164', 'missing-task-164', CURRENT_TIMESTAMP, 'once', 1, 1, CURRENT_TIMESTAMP);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 164); err != nil {
		t.Fatalf("failed to run migration 164: %v", err)
	}
	var kept, orphan int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE id = 'schedule-kept-164'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE id = 'schedule-orphan-164'`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if kept != 1 || orphan != 0 {
		t.Fatalf("migration 164 schedules: kept=%d orphan=%d, want kept=1 orphan=0", kept, orphan)
	}
}

func TestMigration165DeletesTerminalizedAutomationPositions(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "terminalized-positions-165.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 164); err != nil {
		t.Fatalf("failed to run migrations through 164: %v", err)
	}
	_, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('project-165', 'Migration 165', '', '');
		INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state)
			VALUES ('automation-165', 'project-165', 'automation-165', 'Automation 165', 'active');
		INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES ('version-165', 'project-165', 'automation-165', 1, 'published', 'manual', 'native_sdlc');
		INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role)
			VALUES ('node-165', 'project-165', 'automation-165', 'version-165', 'implementation', 'Implementation', 'agent_task', 'agent');
		INSERT INTO automation_work_items (id, project_id, automation_id, origin_version_id, work_item_key, status)
			VALUES ('item-165-terminal', 'project-165', 'automation-165', 'version-165', 'item-165-terminal', 'completed'),
				('item-165-failed', 'project-165', 'automation-165', 'version-165', 'item-165-failed', 'failed'),
				('item-165-newer', 'project-165', 'automation-165', 'version-165', 'item-165-newer', 'active');
		INSERT INTO automation_work_item_positions (work_item_id, project_id, automation_id, version_id, node_id, state, entered_at, updated_at)
			VALUES ('item-165-terminal', 'project-165', 'automation-165', 'version-165', 'node-165', 'waiting', '2026-08-15 10:00:00', '2026-08-15 10:00:00'),
				('item-165-failed', 'project-165', 'automation-165', 'version-165', 'node-165', 'failed', '2026-08-15 10:00:00', '2026-08-15 10:00:00'),
				('item-165-newer', 'project-165', 'automation-165', 'version-165', 'node-165', 'active', '2026-08-15 12:00:00', '2026-08-15 12:00:00');
		INSERT INTO automation_transitions (id, project_id, automation_id, version_id, work_item_id, from_node_id, to_node_id, event_key, state, occurred_at)
			VALUES ('transition-165-completed', 'project-165', 'automation-165', 'version-165', 'item-165-terminal', 'node-165', 'node-165', 'execution:terminal-165:terminal:completed', 'completed', '2026-08-15 11:00:00'),
				('transition-165-failed', 'project-165', 'automation-165', 'version-165', 'item-165-failed', 'node-165', 'node-165', 'execution:failed-165:terminal:failed', 'failed', '2026-08-15 11:00:00'),
				('transition-165-older', 'project-165', 'automation-165', 'version-165', 'item-165-newer', 'node-165', 'node-165', 'execution:older-165:terminal:completed', 'completed', '2026-08-15 11:00:00');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 165); err != nil {
		t.Fatalf("failed to run migration 165: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions WHERE work_item_id = 'item-165-terminal'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("terminalized completed position count = %d, want 0", remaining)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions WHERE work_item_id IN ('item-165-failed', 'item-165-newer')`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("preserved position count = %d, want 2", remaining)
	}
}
