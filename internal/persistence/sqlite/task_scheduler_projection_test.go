package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskSchedulerAdmissionQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	taskRepo := NewTaskRepo(db, nil)
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Counted active admission",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		Prompt:      strings.Repeat("large prompt ", 4096),
		ChainConfig: strings.Repeat("large chain ", 1024),
		SwarmConfig: strings.Repeat("large swarm ", 1024),
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	if _, err := taskRepo.ListActivePendingAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := taskRepo.ListStaleQueuedTasks(ctx, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)

	statements := counter.Statements()
	admissionQueries := 0
	for _, statement := range statements {
		query := strings.ToLower(statement)
		if strings.Contains(query, "from tasks where category = 'active' and status = 'pending'") {
			admissionQueries++
			projection := strings.SplitN(query, " from ", 2)[0]
			if strings.Contains(projection, "prompt") || strings.Contains(projection, "chain_config") || strings.Contains(projection, "swarm_config") {
				t.Fatalf("scheduler admission query selected full payload: %s", statement)
			}
		}
		if strings.Contains(query, "where id = ?") {
			t.Fatalf("scheduler admission introduced a per-task read: %s", statement)
		}
	}
	if admissionQueries != 1 {
		t.Fatalf("expected one scheduler admission query, got %d: %#v", admissionQueries, statements)
	}
	if len(statements) != 2 {
		t.Fatalf("expected one admission query plus one stale-queue query, got %d: %#v", len(statements), statements)
	}
}
