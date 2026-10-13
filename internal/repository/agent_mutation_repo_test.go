package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
)

var mutationTestCounter int

func TestAgentMutationRepo_CreateAndFetch(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentMutationRepo(db)
	ctx := context.Background()

	row := &models.AgentConfigMutation{
		TaskRunID:           "run-1",
		ProjectID:           "proj-1",
		TargetType:          models.MutationTargetSkill,
		TargetKey:           "backend/verify",
		Action:              "create",
		ProposedPayloadJSON: `{"agent":"backend"}`,
		ValidationStatus:    models.MutationStatusApplied,
		ChangedPathsJSON:    `["/p/SKILL.md"]`,
		IdempotencyKey:      "k-1",
	}
	if err := repo.Create(ctx, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row.ID == "" {
		t.Fatalf("ID should be filled in by RETURNING")
	}
	if row.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should be filled in")
	}

	got, err := repo.FindAppliedByIdempotencyKey(ctx, "k-1")
	if err != nil {
		t.Fatalf("FindAppliedByIdempotencyKey: %v", err)
	}
	if got.ID != row.ID || got.TargetKey != "backend/verify" || got.Action != "create" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if _, err := repo.FindAppliedByIdempotencyKey(ctx, "missing"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("missing key should return ErrNoRows, got %v", err)
	}
	if _, err := repo.FindAppliedByIdempotencyKey(ctx, ""); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("empty key should return ErrNoRows, got %v", err)
	}
}

func TestAgentMutationRepo_IdempotencyOnlyAppliesToApplied(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentMutationRepo(db)
	ctx := context.Background()

	// A blocked row may share the same idempotency key with a future applied
	// row; only two applied rows must collide.
	blocked := &models.AgentConfigMutation{
		TargetType: models.MutationTargetSkill, Action: "create",
		ValidationStatus: models.MutationStatusBlocked,
		IdempotencyKey:   "shared-key",
	}
	if err := repo.Create(ctx, blocked); err != nil {
		t.Fatalf("blocked insert: %v", err)
	}
	// Insert a second blocked row with the same key — should still succeed.
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		TargetType: models.MutationTargetSkill, Action: "patch",
		ValidationStatus: models.MutationStatusBlocked,
		IdempotencyKey:   "shared-key",
	}); err != nil {
		t.Fatalf("second blocked insert should succeed: %v", err)
	}

	// Now an applied row with the same key must succeed once.
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		TargetType: models.MutationTargetSkill, Action: "create",
		ValidationStatus: models.MutationStatusApplied,
		IdempotencyKey:   "shared-key",
	}); err != nil {
		t.Fatalf("first applied insert with shared key: %v", err)
	}
	// A second applied row with the same key must fail (uniqueness).
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		TargetType: models.MutationTargetSkill, Action: "create",
		ValidationStatus: models.MutationStatusApplied,
		IdempotencyKey:   "shared-key",
	}); err == nil {
		t.Fatalf("second applied insert with same key must fail")
	}
}

func createMutationTestExecution(t *testing.T, db *sql.DB) *models.LifecycleExecution {
	t.Helper()
	ctx := context.Background()
	agent := createLifecycleTestAgent(t, NewAgentRepo(db))
	mutationTestCounter++
	task := &models.Task{
		ProjectID: "default",
		Title:     fmt.Sprintf("Mutation Test Task %d", mutationTestCounter),
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	repo := NewLifecycleRepo(db)
	exec := &models.LifecycleExecution{
		TaskID:    task.ID,
		TaskRunID: "run-1",
		AgentID:   agent.ID,
		When:      models.LifecycleAfterComplete,
		Status:    "completed",
	}
	if err := repo.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return exec
}

func TestAgentMutationRepo_ListForExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentMutationRepo(db)
	ctx := context.Background()

	execA := createMutationTestExecution(t, db)
	execB := createMutationTestExecution(t, db)

	for i := 0; i < 3; i++ {
		if err := repo.Create(ctx, &models.AgentConfigMutation{
			LifecycleExecutionID: execA.ID,
			TargetType:           models.MutationTargetSkill,
			Action:               "create",
			ValidationStatus:     models.MutationStatusApplied,
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		LifecycleExecutionID: execB.ID,
		TargetType:           models.MutationTargetAgent,
		Action:               "archive",
		ValidationStatus:     models.MutationStatusApplied,
	}); err != nil {
		t.Fatalf("insert exec-B: %v", err)
	}
	rows, err := repo.ListForExecution(ctx, execA.ID)
	if err != nil {
		t.Fatalf("ListForExecution: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows for exec-A, got %d", len(rows))
	}
}

func TestAgentMutationRepo_ListForTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentMutationRepo(db)
	ctx := context.Background()

	exec := createMutationTestExecution(t, db)

	// One row tied to a real task, one not.
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		TaskID:           exec.TaskID,
		TargetType:       models.MutationTargetSkill,
		Action:           "create",
		ValidationStatus: models.MutationStatusApplied,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.Create(ctx, &models.AgentConfigMutation{
		TargetType: models.MutationTargetSkill, Action: "patch",
		ValidationStatus: models.MutationStatusApplied,
	}); err != nil {
		t.Fatalf("insert no task: %v", err)
	}
	rows, err := repo.ListForTask(ctx, exec.TaskID)
	if err != nil {
		t.Fatalf("ListForTask: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != "create" {
		t.Fatalf("ListForTask result: %+v", rows)
	}
}
