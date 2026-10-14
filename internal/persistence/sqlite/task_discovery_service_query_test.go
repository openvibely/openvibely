package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskDiscoveryQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	taskRepo := sqlite.NewTaskRepo(db, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		task := &models.Task{
			ProjectID: "default",
			Title:     fmt.Sprintf("Count and page task %d", i),
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "not selected",
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	counter.Reset()
	counter.SetEnabled(true)
	_, err := service.ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":50}`))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ExecuteListTasksTool: %v", err)
	}
	statements := counter.Statements()
	if len(statements) != 2 {
		t.Fatalf("list_tasks used %d SQL operations, want count plus page: %#v", len(statements), statements)
	}
	if !strings.Contains(strings.ToUpper(statements[0]), "SELECT COUNT(*)") {
		t.Fatalf("first list_tasks operation = %q, want total count", statements[0])
	}
	if !strings.Contains(strings.ToUpper(statements[1]), "SELECT ID") || !strings.Contains(strings.ToUpper(statements[1]), "LIMIT") || !strings.Contains(strings.ToUpper(statements[1]), "OFFSET") {
		t.Fatalf("second list_tasks operation = %q, want bounded compact page", statements[1])
	}
}
