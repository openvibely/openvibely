package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

const (
	chatHistoryBenchTargetProjectID = "chat-history-target-project"
	chatHistoryBenchTargetRows      = 50000
	chatHistoryBenchOtherRows       = 50000
	chatHistoryBenchLimit           = 50
)

func TestExecutionRepo_ListChatHistoryLatestUsesProjectHistoryIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedChatHistoryBenchFixture(t, db, true)
	repo := NewExecutionRepo(db)
	ctx := context.Background()

	baseline := explainExecutionQueryPlan(t, db, legacyChatHistorySQL(), chatHistoryBenchTargetProjectID, chatHistoryBenchLimit)
	if !strings.Contains(baseline, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("legacy plan = %s, want temporary ORDER BY sort", baseline)
	}

	optimizedQuery, optimizedArgs := chatHistoryPageSQL(chatHistoryBenchTargetProjectID, "", chatHistoryBenchLimit)
	optimized := explainExecutionQueryPlan(t, db, optimizedQuery, optimizedArgs...)
	if !strings.Contains(optimized, "idx_executions_chat_history_project_started") {
		t.Fatalf("optimized plan = %s, want chat history project/order index", optimized)
	}
	if strings.Contains(optimized, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("optimized plan = %s, want no temporary ORDER BY sort", optimized)
	}
	if strings.Contains(optimized, "idx_executions_started_at") {
		t.Fatalf("optimized plan = %s, should not scan unrelated global execution order", optimized)
	}

	history, err := repo.ListChatHistory(ctx, chatHistoryBenchTargetProjectID, chatHistoryBenchLimit)
	if err != nil {
		t.Fatalf("ListChatHistory: %v", err)
	}
	if len(history) != chatHistoryBenchLimit {
		t.Fatalf("history len = %d, want %d", len(history), chatHistoryBenchLimit)
	}
	for i, exec := range history {
		if !strings.HasPrefix(exec.TaskID, "chat-history-target-task-") {
			t.Fatalf("row %d leaked task %q", i, exec.TaskID)
		}
		if i > 0 && history[i].StartedAt.Before(history[i-1].StartedAt) {
			t.Fatalf("history not chronological at row %d", i)
		}
	}
	if got := history[0].PromptSent; got != "target prompt 49951" {
		t.Fatalf("first chronological prompt = %q, want target prompt 49951", got)
	}
	if got := history[len(history)-1].PromptSent; got != "target prompt 50000" {
		t.Fatalf("last chronological prompt = %q, want target prompt 50000", got)
	}
}

func TestExecutionRepo_ChatHistoryBeforeUsesHistoryOrderTieBreaker(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()
	agent, _ := agentRepo.GetDefault(ctx)

	created := make([]models.Execution, 0, 5)
	for i := 1; i <= 5; i++ {
		task := &models.Task{ProjectID: "default", Title: fmt.Sprintf("same-time chat %d", i), Category: models.CategoryChat, Status: models.StatusPending, Prompt: "p"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("turn-%d", i)}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec %d: %v", i, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at = '2026-01-01 00:00:00' WHERE id = ?`, exec.ID); err != nil {
			t.Fatalf("normalize started_at %d: %v", i, err)
		}
		created = append(created, *exec)
	}

	latest, err := execRepo.ListChatHistory(ctx, "default", 3)
	if err != nil {
		t.Fatalf("ListChatHistory: %v", err)
	}
	if got := promptsOf(latest); fmt.Sprint(got) != "[turn-3 turn-4 turn-5]" {
		t.Fatalf("latest prompts = %#v, want turn-3..turn-5", got)
	}
	earlier, err := execRepo.ListChatHistoryBefore(ctx, "default", created[2].ID, 2)
	if err != nil {
		t.Fatalf("ListChatHistoryBefore: %v", err)
	}
	if got := promptsOf(earlier); fmt.Sprint(got) != "[turn-1 turn-2]" {
		t.Fatalf("earlier prompts = %#v, want turn-1..turn-2", got)
	}
}

func TestExecutionRepo_ChatHistoryMetadataTracksTaskCategoryUpdates(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()
	agent, _ := agentRepo.GetDefault(ctx)

	task := &models.Task{ProjectID: "default", Title: "mutable chat", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "chat prompt"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	history, err := execRepo.ListChatHistory(ctx, "default", 10)
	if err != nil {
		t.Fatalf("ListChatHistory before update: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len before category update = %d, want 1", len(history))
	}

	if _, err := db.ExecContext(ctx, `UPDATE tasks SET category = 'active' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("update task category: %v", err)
	}
	history, err = execRepo.ListChatHistory(ctx, "default", 10)
	if err != nil {
		t.Fatalf("ListChatHistory after update: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history len after category update = %d, want 0", len(history))
	}
}

func TestExecutionRepo_FindLatestActiveChatExecutionUsesStatusOrderIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedChatHistoryBenchFixture(t, db, true)

	plan := explainExecutionQueryPlan(t, db, latestActiveChatExecutionSQL(), chatHistoryBenchTargetProjectID)
	if !strings.Contains(plan, "idx_executions_chat_active_project_status_started") {
		t.Fatalf("active chat plan = %s, want project/status/order index", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("active chat plan = %s, want no temporary ORDER BY sort", plan)
	}
}

func BenchmarkExecutionRepoListChatHistory50kProject(b *testing.B) {
	for _, tc := range []struct {
		name        string
		targetOlder bool
	}{
		{name: "dense_target_newest", targetOlder: false},
		{name: "sparse_target_older_than_other_projects", targetOlder: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			db := testutil.NewTestDB(b)
			seedChatHistoryBenchFixture(b, db, tc.targetOlder)
			repo := NewExecutionRepo(db)
			ctx := context.Background()
			legacyQuery := legacyChatHistorySQL()
			optimizedQuery, optimizedArgs := chatHistoryPageSQL(chatHistoryBenchTargetProjectID, "", chatHistoryBenchLimit)

			legacyPlan := explainExecutionQueryPlan(b, db, legacyQuery, chatHistoryBenchTargetProjectID, chatHistoryBenchLimit)
			if !strings.Contains(legacyPlan, "USE TEMP B-TREE FOR ORDER BY") {
				b.Fatalf("legacy plan = %s, want temporary ORDER BY sort", legacyPlan)
			}
			optimizedPlan := explainExecutionQueryPlan(b, db, optimizedQuery, optimizedArgs...)
			if strings.Contains(optimizedPlan, "USE TEMP B-TREE FOR ORDER BY") {
				b.Fatalf("optimized plan = %s, want no temporary ORDER BY sort", optimizedPlan)
			}
			if !strings.Contains(optimizedPlan, "idx_executions_chat_history_project_started") {
				b.Fatalf("optimized plan = %s, want chat history project/order index", optimizedPlan)
			}

			b.Run("legacy_join_sort", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					rows, err := db.QueryContext(ctx, legacyQuery, chatHistoryBenchTargetProjectID, chatHistoryBenchLimit)
					if err != nil {
						b.Fatal(err)
					}
					if got := drainExecutionRows(b, rows); got != chatHistoryBenchLimit {
						b.Fatalf("rows = %d, want %d", got, chatHistoryBenchLimit)
					}
				}
			})
			b.Run("optimized_repository", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					history, err := repo.ListChatHistory(ctx, chatHistoryBenchTargetProjectID, chatHistoryBenchLimit)
					if err != nil {
						b.Fatal(err)
					}
					if len(history) != chatHistoryBenchLimit {
						b.Fatalf("rows = %d, want %d", len(history), chatHistoryBenchLimit)
					}
				}
			})
		})
	}
}

func BenchmarkExecutionRepoViewTaskThreadPage(b *testing.B) {
	db := testutil.NewTestDB(b)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default",
		Title:     "View task thread benchmark",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted,
		Prompt:    "benchmark prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		b.Fatalf("create task: %v", err)
	}
	prompt := strings.Repeat("p", 4*1024)
	output := strings.Repeat("o", 64*1024)
	for i := 0; i < 200; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO executions (id, task_id, status, prompt_sent, output)
			VALUES (?, ?, ?, ?, ?)`, fmt.Sprintf("thread-bench-exec-%03d", i), task.ID, models.ExecCompleted, prompt, output); err != nil {
			b.Fatalf("insert execution %d: %v", i, err)
		}
	}

	b.Run("unbounded_history", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			executions, err := repo.ListByTaskChronological(ctx, task.ID)
			if err != nil {
				b.Fatal(err)
			}
			if len(executions) != 200 {
				b.Fatalf("executions = %d, want 200", len(executions))
			}
		}
	})
	b.Run("count_plus_20_row_page", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			total, err := repo.CountByTask(ctx, task.ID)
			if err != nil {
				b.Fatal(err)
			}
			page, err := repo.ListByTaskChronologicalPage(ctx, task.ID, 0, 20)
			if err != nil {
				b.Fatal(err)
			}
			if total != 200 || len(page) != 20 {
				b.Fatalf("total/page = %d/%d, want 200/20", total, len(page))
			}
		}
	})
}

func legacyChatHistorySQL() string {
	return `SELECT ` + executionSelectColumnsAliasLight + `
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ? AND t.category = 'chat'
		 ORDER BY e.started_at DESC, e.rowid DESC LIMIT ?`
}

func seedChatHistoryBenchFixture(tb testing.TB, db *sql.DB, targetOlder bool) {
	tb.Helper()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		tb.Fatalf("disable foreign keys: %v", err)
	}
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			tb.Fatalf("re-enable foreign keys: %v", err)
		}
	}()

	targetBase := "2026-01-02 00:00:00"
	otherBase := "2026-01-01 00:00:00"
	if targetOlder {
		targetBase = "2026-01-01 00:00:00"
		otherBase = "2026-01-02 00:00:00"
	}

	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin fixture tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Chat History Target', '', '')`, chatHistoryBenchTargetProjectID); err != nil {
		tb.Fatalf("insert target project: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('chat-history-other-project', 'Chat History Other', '', '')`); err != nil {
		tb.Fatalf("insert other project: %v", err)
	}

	if _, err := tx.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_at, updated_at)
		SELECT
			'chat-history-target-task-' || printf('%05d', n),
			?,
			'chat history target ' || printf('%05d', n),
			'chat',
			1,
			'pending',
			'target prompt ' || n,
			datetime(?, '+' || n || ' seconds'),
			datetime(?, '+' || n || ' seconds')
		FROM seq`, chatHistoryBenchTargetRows, chatHistoryBenchTargetProjectID, targetBase, targetBase); err != nil {
		tb.Fatalf("insert target tasks: %v", err)
	}
	if _, err := tx.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO executions (id, task_id, status, prompt_sent, output, started_at, completed_at, task_project_id, task_category, history_order)
		SELECT
			'chat-history-target-exec-' || printf('%05d', n),
			'chat-history-target-task-' || printf('%05d', n),
			'completed',
			'target prompt ' || n,
			'target output ' || n,
			datetime(?, '+' || n || ' seconds'),
			datetime(?, '+' || n || ' seconds'),
			?,
			'chat',
			n
		FROM seq`, chatHistoryBenchTargetRows, targetBase, targetBase, chatHistoryBenchTargetProjectID); err != nil {
		tb.Fatalf("insert target executions: %v", err)
	}
	if _, err := tx.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_at, updated_at)
		SELECT
			'chat-history-other-task-' || printf('%05d', n),
			'chat-history-other-project',
			'chat history other ' || printf('%05d', n),
			'chat',
			1,
			'pending',
			'other prompt ' || n,
			datetime(?, '+' || n || ' seconds'),
			datetime(?, '+' || n || ' seconds')
		FROM seq`, chatHistoryBenchOtherRows, otherBase, otherBase); err != nil {
		tb.Fatalf("insert other tasks: %v", err)
	}
	if _, err := tx.Exec(`
		WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO executions (id, task_id, status, prompt_sent, output, started_at, completed_at, task_project_id, task_category, history_order)
		SELECT
			'chat-history-other-exec-' || printf('%05d', n),
			'chat-history-other-task-' || printf('%05d', n),
			CASE WHEN n = ? THEN 'running' ELSE 'completed' END,
			'other prompt ' || n,
			'other output ' || n,
			datetime(?, '+' || n || ' seconds'),
			datetime(?, '+' || n || ' seconds'),
			'chat-history-other-project',
			'chat',
			? + n
		FROM seq`, chatHistoryBenchOtherRows, chatHistoryBenchOtherRows, otherBase, otherBase, chatHistoryBenchTargetRows); err != nil {
		tb.Fatalf("insert other executions: %v", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt)
		VALUES ('chat-history-target-active-task', ?, 'non-chat active', 'active', 1, 'pending', 'active prompt')`, chatHistoryBenchTargetProjectID); err != nil {
		tb.Fatalf("insert non-chat task: %v", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO executions (id, task_id, status, prompt_sent, output, started_at, task_project_id, task_category, history_order)
		VALUES ('chat-history-target-active-exec', 'chat-history-target-active-task', 'completed', 'active prompt', 'active output', datetime(?, '+999999 seconds'), ?, 'active', ?)`, targetBase, chatHistoryBenchTargetProjectID, chatHistoryBenchTargetRows+chatHistoryBenchOtherRows+1); err != nil {
		tb.Fatalf("insert non-chat execution: %v", err)
	}
	if _, err := tx.Exec(`UPDATE executions SET status = 'running' WHERE id = 'chat-history-target-exec-50000'`); err != nil {
		tb.Fatalf("mark latest target running: %v", err)
	}

	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit fixture: %v", err)
	}
}

func explainExecutionQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("explain rows: %v", err)
	}
	return strings.Join(details, " | ")
}

func drainExecutionRows(tb testing.TB, rows *sql.Rows) int {
	tb.Helper()
	defer rows.Close()
	count := 0
	for rows.Next() {
		if _, err := scanExecutionRow(rows); err != nil {
			tb.Fatalf("scan execution row: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("execution rows: %v", err)
	}
	return count
}
