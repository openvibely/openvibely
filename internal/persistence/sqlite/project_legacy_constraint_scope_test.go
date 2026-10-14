package sqlite_test

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
)

func TestProjectDeletionRollsBackWithBrokenLegacyForeignKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := sqlite.NewProjectRepo(db)
	tasks := sqlite.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Legacy FK"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Retained", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE memory_consolidation_runs (id TEXT PRIMARY KEY);
		CREATE TABLE memory_consolidation_schedules (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			last_run_id TEXT REFERENCES memory_consolidation_runs(id) ON DELETE SET NULL
		);
		INSERT INTO memory_consolidation_schedules(project_id) VALUES (?)`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE memory_consolidation_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := projects.Delete(ctx, project.ID); err == nil || !strings.Contains(err.Error(), "no such table: main.memory_consolidation_runs") {
		t.Fatalf("unexpected deletion result: %v", err)
	}
	if got, err := projects.GetByID(ctx, project.ID); err != nil || got == nil {
		t.Fatalf("project lost: %v", err)
	}
	if got, err := tasks.GetByID(ctx, task.ID); err != nil || got == nil {
		t.Fatalf("task lost: %v", err)
	}
}
