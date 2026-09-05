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

			b.Run("Repository", func(b *testing.B) {
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
