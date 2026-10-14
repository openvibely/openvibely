package teststores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestFixturesShareStoreStateWithoutApplyingTransitions(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "Fixture state", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "fixture"}
	if err := env.Executions.Create(ctx, &execution); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := env.Fixtures.SetExecutionStartedAt(ctx, execution.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := env.Fixtures.SetTaskUpdatedAt(ctx, task.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := env.Fixtures.SetTaskCompletedAt(ctx, task.ID, at); err != nil {
		t.Fatal(err)
	}
	stored, err := env.Tasks.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.StatusPending || !stored.UpdatedAt.Equal(at) || stored.CompletedAt == nil || !stored.CompletedAt.Equal(at) {
		t.Fatalf("fixture timestamps or task status changed unexpectedly: %+v", stored)
	}
	run, err := env.Executions.GetByID(ctx, execution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.StartedAt.Equal(at) || run.Status != models.ExecRunning {
		t.Fatalf("unexpected execution state: %+v", run)
	}
	if err := env.Fixtures.SetExecutionStartedAt(ctx, "missing", at); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("missing execution: %v", err)
	}
	count, err := env.Fixtures.CountAutomations(ctx)
	if err != nil || count != 0 {
		t.Fatalf("automation count: %d, %v", count, err)
	}
	other := teststores.New(t)
	absent, err := other.Tasks.GetByID(ctx, task.ID)
	if err != nil || absent != nil {
		t.Fatalf("test databases are not isolated: %v, %v", absent, err)
	}
}

func TestIOCounterMeasuresBackendRequestsWithinStoreCalls(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	ctx := context.Background()
	task := models.Task{ProjectID: "default", Title: "Measured task", Category: models.CategoryBacklog, Status: models.StatusPending}
	env.IO.Reset()
	env.IO.SetEnabled(true)
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	env.IO.SetEnabled(false)
	if got := env.IO.Snapshot(); got.Requests <= 1 {
		t.Fatalf("counter must capture the multiple backend requests within Create, got %+v", got)
	}
	env.IO.Reset()
	env.IO.SetEnabled(true)
	if _, err := env.Tasks.GetByID(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	env.IO.SetEnabled(false)
	measured := env.IO.Snapshot()
	if measured.Requests != 1 || measured.ReadTextBytes < len(task.Title) {
		t.Fatalf("expected one actual read with measured payload, got %+v", measured)
	}
	if _, err := env.Tasks.GetByID(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if got := env.IO.Snapshot(); got != measured {
		t.Fatalf("disabled counter changed: %+v", got)
	}
	env.IO.Reset()
	if got := env.IO.Snapshot(); got.Requests != 0 || got.ReadTextBytes != 0 {
		t.Fatalf("reset retained measurements: %+v", got)
	}
}
