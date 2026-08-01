package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
)

type taskBoardListFunc func(context.Context, string, string, string, string) ([]models.Task, error)

type byteCountWriter struct {
	bytes int64
}

func (w *byteCountWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func BenchmarkTaskRepo_KanbanRefresh(b *testing.B) {
	fixtures := []struct {
		name       string
		taskCount  int
		promptSize int
	}{
		{name: "Small20x128B", taskCount: 20, promptSize: 128},
		{name: "Large500x64KiB", taskCount: 500, promptSize: 64 * 1024},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db := newKanbanBenchmarkDB(b, fixture.taskCount, fixture.promptSize)
			defer db.Close()
			repo := repository.NewTaskRepo(db, nil)

			implementations := []struct {
				name string
				list taskBoardListFunc
			}{
				{name: "FullPromptBaseline", list: repo.ListByProjectWithCategorySorts},
				{name: "ProjectedPrompt", list: repo.ListBoardByProjectWithCategorySorts},
			}
			for _, implementation := range implementations {
				b.Run(implementation.name, func(b *testing.B) {
					benchmarkKanbanRefresh(b, implementation.list)
				})
			}
		})
	}
}

func benchmarkKanbanRefresh(b *testing.B, list taskBoardListFunc) {
	b.Helper()
	ctx := context.Background()
	var responseBytes int64
	var promptBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := list(ctx, "default", "", "priority_desc", "completed_desc")
		if err != nil {
			b.Fatalf("list tasks: %v", err)
		}
		promptBytes = 0
		for _, task := range tasks {
			promptBytes += int64(len(task.Prompt))
		}
		tasks = service.AttachSwarmChildren(tasks)

		writer := byteCountWriter{}
		if err := components.KanbanBoard(tasks, "default", "priority_desc", "completed_desc", nil, nil).Render(ctx, &writer); err != nil {
			b.Fatalf("render Kanban board: %v", err)
		}
		responseBytes = writer.bytes
	}
	b.StopTimer()
	b.ReportMetric(float64(promptBytes), "prompt_bytes/op")
	b.ReportMetric(float64(responseBytes), "response_bytes/op")
}

func newKanbanBenchmarkDB(b *testing.B, taskCount, promptSize int) *sql.DB {
	b.Helper()
	b.StopTimer()
	db, err := database.New(":memory:")
	if err != nil {
		b.Fatalf("create benchmark database: %v", err)
	}
	prompt := strings.Repeat("p", promptSize)
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		b.Fatalf("begin fixture transaction: %v", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO tasks (
			id, project_id, title, category, priority, status, prompt,
			tag, display_order, parent_task_id, swarm_role, swarm_status
		) VALUES (?, 'default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		db.Close()
		b.Fatalf("prepare fixture insert: %v", err)
	}
	defer stmt.Close()

	for i := 0; i < taskCount; i++ {
		id := fmt.Sprintf("benchmark-task-%03d", i)
		title := fmt.Sprintf("Benchmark task %03d", taskCount-i)
		category := models.CategoryBacklog
		status := models.StatusPending
		role := models.SwarmRoleNone
		swarmStatus := ""
		var parentID any

		if taskCount >= 50 && i < 10 {
			category = models.CategoryActive
			status = models.StatusBlocked
			role = models.SwarmRoleParent
			swarmStatus = "running"
		} else if taskCount >= 50 && i >= taskCount-40 {
			category = models.CategoryActive
			status = models.StatusRunning
			role = models.SwarmRoleWorker
			swarmStatus = "running"
			parentID = fmt.Sprintf("benchmark-task-%03d", (i-(taskCount-40))/4)
		} else {
			switch i % 3 {
			case 1:
				category = models.CategoryActive
			case 2:
				category = models.CategoryCompleted
				status = models.StatusCompleted
			}
		}
		tag := models.TagFeature
		if i%2 == 0 {
			tag = models.TagBug
		}
		if _, err := stmt.Exec(id, title, category, (i%4)+1, status, prompt, tag, i, parentID, role, swarmStatus); err != nil {
			tx.Rollback()
			db.Close()
			b.Fatalf("insert fixture task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		b.Fatalf("commit fixture transaction: %v", err)
	}
	return db
}
