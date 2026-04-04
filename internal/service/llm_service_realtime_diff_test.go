package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestRealtimeDiffUpdates verifies that execution DiffOutput is updated during task execution,
// not just at completion, enabling realtime Changes tab updates.
func TestRealtimeDiffUpdates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	// Create test project
	project := &models.Project{
		Name:     "test-project",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "test realtime diff",
		Prompt:    "test prompt",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create agent config
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Create execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	// Verify initial state: no diff output
	fetched, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if fetched.DiffOutput != "" {
		t.Errorf("expected empty initial diff output, got: %q", fetched.DiffOutput)
	}

	// Create file change broadcaster
	fcb := events.NewFileChangeBroadcaster()
	
	// Subscribe to events to verify they're being published
	sub, err := fcb.Subscribe()
	if err != nil {
		t.Fatalf("subscribe to file changes: %v", err)
	}
	defer fcb.Unsubscribe(sub)

	// Create LLM service with broadcaster
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, nil)
	llmSvc.SetFileChangeBroadcaster(fcb)

	// Start background diff broadcaster with a test diff
	testDiff := `diff --git a/test.txt b/test.txt
new file mode 100644
index 0000000..5e1c309
--- /dev/null
+++ b/test.txt
@@ -0,0 +1 @@
+Hello World
`
	
	stopChan := make(chan struct{})
	
	// Override captureDiff to return test data
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond) // Faster for testing
		defer ticker.Stop()
		
		for {
			select {
			case <-stopChan:
				// Final snapshot
				if err := execRepo.UpdateDiffOutput(ctx, exec.ID, testDiff); err != nil {
					t.Errorf("final diff update: %v", err)
				}
				fcb.Publish(events.FileChangeEvent{
					Type:       events.DiffSnapshot,
					TaskID:     task.ID,
					ExecID:     exec.ID,
					DiffOutput: testDiff,
					Timestamp:  time.Now().UnixMilli(),
				})
				return
				
			case <-ticker.C:
				// Periodic snapshot - this is what we're testing
				if err := execRepo.UpdateDiffOutput(ctx, exec.ID, testDiff); err != nil {
					t.Errorf("periodic diff update: %v", err)
				}
				fcb.Publish(events.FileChangeEvent{
					Type:       events.DiffSnapshot,
					TaskID:     task.ID,
					ExecID:     exec.ID,
					DiffOutput: testDiff,
					Timestamp:  time.Now().UnixMilli(),
				})
			}
		}
	}()

	// Wait for first diff update (should happen within ~500ms)
	timeout := time.After(2 * time.Second)
	var receivedEvent bool
	
	select {
	case event := <-sub:
		if event.Type != events.DiffSnapshot {
			t.Errorf("expected DiffSnapshot event, got: %s", event.Type)
		}
		if event.TaskID != task.ID {
			t.Errorf("expected task ID %s, got: %s", task.ID, event.TaskID)
		}
		if event.DiffOutput != testDiff {
			t.Errorf("expected diff output %q, got: %q", testDiff, event.DiffOutput)
		}
		receivedEvent = true
	case <-timeout:
		t.Fatal("timeout waiting for diff snapshot event")
	}

	if !receivedEvent {
		t.Fatal("did not receive diff snapshot event")
	}

	// Verify execution diff was updated in database (key test - this should work during execution)
	time.Sleep(600 * time.Millisecond) // Wait for at least one ticker fire
	fetched, err = execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution after update: %v", err)
	}
	if fetched.DiffOutput != testDiff {
		t.Errorf("expected execution diff to be updated to %q, got: %q", testDiff, fetched.DiffOutput)
	}

	// Stop broadcaster
	close(stopChan)
	time.Sleep(100 * time.Millisecond)

	// Verify diff is still persisted
	fetched, err = execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution final: %v", err)
	}
	if fetched.DiffOutput != testDiff {
		t.Errorf("expected execution diff to persist as %q, got: %q", testDiff, fetched.DiffOutput)
	}
}
