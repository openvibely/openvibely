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

// BenchmarkUpcomingRepo_ListPendingActiveTasks compares the full-row task
// query (as ListRunningTasks/ListPendingActiveTasks/ListUpcomingScheduledTasks
// used to select, including the full t.prompt column) against the bounded
// SUBSTR(t.prompt, 1, 200) projection those methods use today. Only a 200-char
// truncated preview is ever rendered by the Pulse dashboard
// (web/templates/pages/upcoming.templ truncatePrompt(bt.Task.Prompt, 200)),
// so the full-row query is kept here only as the "before" baseline for
// comparison.
func BenchmarkUpcomingRepo_ListPendingActiveTasks(b *testing.B) {
	fixtures := []struct {
		name       string
		taskCount  int
		promptSize int
	}{
		{name: "Small20x512B", taskCount: 20, promptSize: 512},
		{name: "Large300x32KiB", taskCount: 300, promptSize: 32 * 1024},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db, projectID := newUpcomingBenchmarkDB(b, fixture.taskCount, fixture.promptSize)
			repo := NewUpcomingRepo(db)

			b.Run("FullRowBaseline", func(b *testing.B) {
				benchmarkUpcomingFullRowQuery(b, db, projectID)
			})
			b.Run("BoundedProjection", func(b *testing.B) {
				benchmarkUpcomingListPendingActiveTasks(b, repo, projectID)
			})
		})
	}
}

func benchmarkUpcomingListPendingActiveTasks(b *testing.B, repo *UpcomingRepo, projectID string) {
	b.Helper()
	ctx := context.Background()
	var promptBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := repo.ListPendingActiveTasks(ctx, projectID)
		if err != nil {
			b.Fatalf("list pending active tasks: %v", err)
		}
		promptBytes = 0
		for _, t := range tasks {
			promptBytes += int64(len(t.Task.Prompt))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(promptBytes), "prompt_bytes/op")
}

// benchmarkUpcomingFullRowQuery replays the pre-fix query shape (selecting
// the full t.prompt column for every row) directly against the fixture
// database, since the repository no longer exposes that broad projection in
// production code.
func benchmarkUpcomingFullRowQuery(b *testing.B, db *sql.DB, projectID string) {
	b.Helper()
	ctx := context.Background()
	var promptBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryContext(ctx,
			`SELECT t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt,
				t.agent_id, t.tag, t.display_order, t.created_at, t.updated_at,
				ac.name as agent_name
			 FROM tasks t
			 LEFT JOIN agent_configs ac ON ac.id = t.agent_id
			 WHERE t.project_id = ? AND t.category = 'active' AND t.status = 'pending'
			 ORDER BY t.priority DESC, t.display_order ASC`, projectID)
		if err != nil {
			b.Fatalf("query full task rows: %v", err)
		}
		promptBytes = 0
		for rows.Next() {
			var row UpcomingTaskRow
			var agentName sql.NullString
			if err := rows.Scan(
				&row.TaskID, &row.ProjectID, &row.Title, &row.Category, &row.Priority,
				&row.Status, &row.Prompt, &row.AgentID, &row.Tag, &row.DisplayOrder,
				&row.CreatedAt, &row.UpdatedAt, &agentName,
			); err != nil {
				rows.Close()
				b.Fatalf("scan full task row: %v", err)
			}
			promptBytes += int64(len(row.Prompt))
		}
		if err := rows.Err(); err != nil {
			b.Fatalf("iterate full task rows: %v", err)
		}
		rows.Close()
	}
	b.StopTimer()
	b.ReportMetric(float64(promptBytes), "prompt_bytes/op")
}

func newUpcomingBenchmarkDB(b *testing.B, taskCount, promptSize int) (*sql.DB, string) {
	b.Helper()
	b.StopTimer()
	db := testutil.NewTestDB(b)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := &models.Project{Name: "Benchmark Project", RepoPath: "/tmp/bench"}
	if err := projectRepo.Create(ctx, project); err != nil {
		b.Fatalf("creating benchmark project: %v", err)
	}

	prompt := strings.Repeat("p", promptSize)
	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     fmt.Sprintf("Benchmark Task %03d", i),
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Priority:  (i % 4) + 1,
			Prompt:    prompt,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			b.Fatalf("creating benchmark task %d: %v", i, err)
		}
	}

	b.StartTimer()
	return db, project.ID
}
