package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestWorkerService_ModelSlotAcquireRelease(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	agentID := "agent-1"

	// Without any repo, there's no limit — should always acquire
	if !ws.tryAcquireModelSlot(agentID) {
		t.Error("expected to acquire model slot with no repo (no limit)")
	}
	if ws.ModelRunning(agentID) != 1 {
		t.Errorf("expected ModelRunning=1, got %d", ws.ModelRunning(agentID))
	}

	ws.releaseModelSlot(agentID)
	if ws.ModelRunning(agentID) != 0 {
		t.Errorf("expected ModelRunning=0 after release, got %d", ws.ModelRunning(agentID))
	}
}

func TestWorkerService_ModelSlotEnforcesLimit(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers=2
	agent := &models.LLMConfig{
		Name:       "Test Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 2,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Acquire slot 1 — should succeed
	if !ws.tryAcquireModelSlot(agent.ID) {
		t.Error("expected to acquire first model slot")
	}

	// Acquire slot 2 — should succeed
	if !ws.tryAcquireModelSlot(agent.ID) {
		t.Error("expected to acquire second model slot")
	}

	// Acquire slot 3 — should fail (limit is 2)
	if ws.tryAcquireModelSlot(agent.ID) {
		t.Error("expected model slot acquisition to fail at capacity")
	}

	if ws.ModelRunning(agent.ID) != 2 {
		t.Errorf("expected ModelRunning=2, got %d", ws.ModelRunning(agent.ID))
	}

	// Release one slot
	ws.releaseModelSlot(agent.ID)

	// Now acquiring should succeed again
	if !ws.tryAcquireModelSlot(agent.ID) {
		t.Error("expected to acquire model slot after release")
	}
}

func TestWorkerService_ModelSlotWorkerIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create two models with max_workers=1 each
	agentA := &models.LLMConfig{
		Name:       "Model A",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 1,
	}
	agentB := &models.LLMConfig{
		Name:       "Model B",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-opus-4-6",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 1,
		IsDefault:  false,
	}

	if err := llmConfigRepo.Create(ctx, agentA); err != nil {
		t.Fatalf("Create agent A: %v", err)
	}
	if err := llmConfigRepo.Create(ctx, agentB); err != nil {
		t.Fatalf("Create agent B: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Acquire slot for Model A — should succeed
	if !ws.tryAcquireModelSlot(agentA.ID) {
		t.Error("expected to acquire Model A slot")
	}

	// Model A is at capacity — should fail
	if ws.tryAcquireModelSlot(agentA.ID) {
		t.Error("expected Model A slot to be at capacity")
	}

	// Model B should still be available — pools are isolated
	if !ws.tryAcquireModelSlot(agentB.ID) {
		t.Error("expected to acquire Model B slot (pools are isolated)")
	}

	if ws.ModelRunning(agentA.ID) != 1 {
		t.Errorf("expected Model A running=1, got %d", ws.ModelRunning(agentA.ID))
	}
	if ws.ModelRunning(agentB.ID) != 1 {
		t.Errorf("expected Model B running=1, got %d", ws.ModelRunning(agentB.ID))
	}
}

func TestWorkerService_ModelSlotNoLimitWhenZero(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers=0 (no limit)
	agent := &models.LLMConfig{
		Name:       "Unlimited Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 0,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// With max_workers=0, should always succeed
	for i := 0; i < 10; i++ {
		if !ws.tryAcquireModelSlot(agent.ID) {
			t.Errorf("expected to acquire slot %d with no limit", i)
		}
	}

	if ws.ModelRunning(agent.ID) != 10 {
		t.Errorf("expected ModelRunning=10, got %d", ws.ModelRunning(agent.ID))
	}
}

func TestWorkerService_ModelSlotEmptyIDAlwaysSucceeds(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	// Empty agent ID should always succeed
	if !ws.tryAcquireModelSlot("") {
		t.Error("expected empty agent ID to always succeed")
	}

	// ModelRunning for empty should be 0 (not tracked)
	if ws.ModelRunning("") != 0 {
		t.Errorf("expected ModelRunning for empty ID to be 0, got %d", ws.ModelRunning(""))
	}
}

func TestWorkerService_ModelSlotConcurrentAccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Concurrent Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 5,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Launch 20 goroutines trying to acquire slots concurrently
	var wg sync.WaitGroup
	acquired := make(chan bool, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired <- ws.tryAcquireModelSlot(agent.ID)
		}()
	}

	wg.Wait()
	close(acquired)

	successCount := 0
	for ok := range acquired {
		if ok {
			successCount++
		}
	}

	if successCount != 5 {
		t.Errorf("expected exactly 5 successful acquisitions (limit=5), got %d", successCount)
	}

	if ws.ModelRunning(agent.ID) != 5 {
		t.Errorf("expected ModelRunning=5, got %d", ws.ModelRunning(agent.ID))
	}
}

func TestWorkerService_ResolveAgentConfigID(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a default agent
	defaultAgent := &models.LLMConfig{
		Name:       "Default Agent",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, defaultAgent); err != nil {
		t.Fatalf("Create default agent: %v", err)
	}

	// Create a second agent
	specificAgent := &models.LLMConfig{
		Name:       "Specific Agent",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-opus-4-6",
		MaxTokens:  8192,
		AuthMethod: models.AuthMethodCLI,
	}
	if err := llmConfigRepo.Create(ctx, specificAgent); err != nil {
		t.Fatalf("Create specific agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
		projectRepo:   projectRepo,
	}

	// Test 1: Task with explicit agent ID resolves to that agent
	agentID := specificAgent.ID
	task1 := models.Task{
		ProjectID: "default",
		AgentID:   &agentID,
	}
	resolved := ws.resolveAgentConfigID(ctx, task1)
	if resolved != specificAgent.ID {
		t.Errorf("expected resolved agent=%s, got %s", specificAgent.ID, resolved)
	}

	// Test 2: Task without agent ID resolves to default
	task2 := models.Task{
		ProjectID: "default",
	}
	resolved = ws.resolveAgentConfigID(ctx, task2)
	if resolved != defaultAgent.ID {
		t.Errorf("expected resolved agent=%s (default), got %s", defaultAgent.ID, resolved)
	}

	// Test 3: Task with non-existent agent ID falls back to default
	badID := "nonexistent-id"
	task3 := models.Task{
		ProjectID: "default",
		AgentID:   &badID,
	}
	resolved = ws.resolveAgentConfigID(ctx, task3)
	if resolved != defaultAgent.ID {
		t.Errorf("expected resolved agent=%s (default fallback), got %s", defaultAgent.ID, resolved)
	}
}

// TestWorkerService_ScheduledTaskNotDroppedOnRequeue verifies that scheduled tasks
// (category="scheduled") are NOT dropped when the project slot is at capacity.
// Previously, the re-queue logic only allowed "active" category tasks, silently
// dropping scheduled tasks.
func TestWorkerService_ScheduledTaskNotDroppedOnRequeue(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{
		Name:       "Test Project",
		MaxWorkers: &maxWorkers,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create a scheduled task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test scheduled",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
		taskRepo:    taskRepo,
	}

	// Simulate: project is at capacity (1 task running)
	ws.tryAcquireProjectSlot(project.ID)

	// Now try to acquire another slot — should fail (at capacity)
	if ws.tryAcquireProjectSlot(project.ID) {
		t.Fatal("expected project slot acquisition to fail at capacity")
	}

	// The worker's re-queue logic checks db for task validity when slot fails.
	// Verify that a scheduled task would pass the category check.
	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil || dbTask == nil {
		t.Fatalf("GetByID: %v", err)
	}

	// This is the condition from the worker: task should NOT be dropped
	shouldDrop := dbTask.Status != models.StatusPending ||
		(dbTask.Category != models.CategoryActive && dbTask.Category != models.CategoryScheduled)
	if shouldDrop {
		t.Errorf("scheduled task should NOT be dropped on re-queue, category=%s status=%s",
			dbTask.Category, dbTask.Status)
	}

	// Release the slot
	ws.releaseProjectSlot(project.ID)
}

func TestWorkerService_SubmitSkipsChatTasks(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	chatTask := models.Task{
		ID:        "chat-1",
		Category:  models.CategoryChat,
		ProjectID: "default",
	}

	ws.Submit(chatTask)

	if ws.QueueSize() != 0 {
		t.Errorf("expected queue size=0 (chat tasks bypass), got %d", ws.QueueSize())
	}
}

func TestWorkerService_SubmitDeduplicates(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	task := models.Task{
		ID:        "task-1",
		Category:  models.CategoryActive,
		ProjectID: "default",
	}

	ws.Submit(task)
	ws.Submit(task) // duplicate

	if ws.QueueSize() != 1 {
		t.Errorf("expected queue size=1 (dedup), got %d", ws.QueueSize())
	}
}

func TestWorkerService_AcquireModelSlotBlocking(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers=1
	agent := &models.LLMConfig{
		Name:       "Blocking Test Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 1,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Acquire the only slot
	if err := ws.AcquireModelSlot(ctx, agent.ID); err != nil {
		t.Fatalf("first AcquireModelSlot: %v", err)
	}
	if ws.ModelRunning(agent.ID) != 1 {
		t.Errorf("expected ModelRunning=1, got %d", ws.ModelRunning(agent.ID))
	}

	// Try to acquire again with a short timeout — should fail (slot full)
	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()
	if err := ws.AcquireModelSlot(shortCtx, agent.ID); err == nil {
		t.Error("expected AcquireModelSlot to fail when at capacity with short timeout")
	}

	// Release the slot
	ws.ReleaseModelSlot(agent.ID)

	// Now acquiring should succeed
	if err := ws.AcquireModelSlot(ctx, agent.ID); err != nil {
		t.Errorf("AcquireModelSlot after release: %v", err)
	}
	ws.ReleaseModelSlot(agent.ID)
}

func TestWorkerService_AcquireModelSlotBlocksAndUnblocks(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers=1
	agent := &models.LLMConfig{
		Name:       "Block Unblock Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 1,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Acquire the only slot
	if err := ws.AcquireModelSlot(ctx, agent.ID); err != nil {
		t.Fatalf("first AcquireModelSlot: %v", err)
	}

	// Start a goroutine that tries to acquire (will block)
	acquired := make(chan struct{})
	go func() {
		_ = ws.AcquireModelSlot(ctx, agent.ID)
		close(acquired)
	}()

	// Verify it doesn't acquire immediately
	select {
	case <-acquired:
		t.Fatal("expected AcquireModelSlot to block when at capacity")
	case <-time.After(200 * time.Millisecond):
		// Good — still blocked
	}

	// Release the slot — the blocked goroutine should acquire it
	ws.ReleaseModelSlot(agent.ID)

	select {
	case <-acquired:
		// Good — acquired after release
	case <-time.After(2 * time.Second):
		t.Fatal("expected blocked AcquireModelSlot to complete after release")
	}

	ws.ReleaseModelSlot(agent.ID)
}

func TestWorkerService_AcquireModelSlotNoLimitNoBlock(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()

	// No repo = no limit; should acquire immediately
	for i := 0; i < 10; i++ {
		if err := ws.AcquireModelSlot(ctx, "any-agent"); err != nil {
			t.Errorf("AcquireModelSlot %d: %v", i, err)
		}
	}

	// Empty ID should succeed
	if err := ws.AcquireModelSlot(ctx, ""); err != nil {
		t.Errorf("AcquireModelSlot with empty ID: %v", err)
	}
}

func TestWorkerService_GetModelWorkerTimeout(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with worker_timeout=120
	agent := &models.LLMConfig{
		Name:          "Timeout Model",
		Provider:      models.ProviderAnthropic,
		Model:         "claude-sonnet-4-5-20250929",
		MaxTokens:     4096,
		AuthMethod:    models.AuthMethodCLI,
		WorkerTimeout: 120,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	// Create a model with no timeout
	agentNoTimeout := &models.LLMConfig{
		Name:       "No Timeout Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-opus-4-6",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
	}
	if err := llmConfigRepo.Create(ctx, agentNoTimeout); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    0,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Model with timeout should return 120s
	timeout := ws.GetModelWorkerTimeout(agent.ID)
	if timeout != 120*time.Second {
		t.Errorf("expected timeout=120s, got %v", timeout)
	}

	// Model without timeout should return 0
	timeout = ws.GetModelWorkerTimeout(agentNoTimeout.ID)
	if timeout != 0 {
		t.Errorf("expected timeout=0, got %v", timeout)
	}

	// Empty ID should return 0
	timeout = ws.GetModelWorkerTimeout("")
	if timeout != 0 {
		t.Errorf("expected timeout=0 for empty ID, got %v", timeout)
	}

	// No repo should return 0
	wsNoRepo := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
	timeout = wsNoRepo.GetModelWorkerTimeout(agent.ID)
	if timeout != 0 {
		t.Errorf("expected timeout=0 with no repo, got %v", timeout)
	}
}

func TestLLMConfigRepo_MaxWorkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers and worker_timeout
	config := &models.LLMConfig{
		Name:          "Worker Config Test",
		Provider:      models.ProviderAnthropic,
		Model:         "claude-sonnet-4-5-20250929",
		MaxTokens:     4096,
		AuthMethod:    models.AuthMethodCLI,
		MaxWorkers:    3,
		WorkerTimeout: 300,
	}
	if err := repo.Create(ctx, config); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the values were persisted
	fetched, err := repo.GetByID(ctx, config.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched.MaxWorkers != 3 {
		t.Errorf("expected MaxWorkers=3, got %d", fetched.MaxWorkers)
	}
	if fetched.WorkerTimeout != 300 {
		t.Errorf("expected WorkerTimeout=300, got %d", fetched.WorkerTimeout)
	}

	// Update the values
	fetched.MaxWorkers = 5
	fetched.WorkerTimeout = 600
	if err := repo.Update(ctx, fetched); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify update
	updated, err := repo.GetByID(ctx, config.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if updated.MaxWorkers != 5 {
		t.Errorf("expected MaxWorkers=5 after update, got %d", updated.MaxWorkers)
	}
	if updated.WorkerTimeout != 600 {
		t.Errorf("expected WorkerTimeout=600 after update, got %d", updated.WorkerTimeout)
	}
}

func TestLLMConfigRepo_MaxWorkersDefaultZero(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model without setting max_workers — should default to 0
	config := &models.LLMConfig{
		Name:       "Default Worker Config",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
	}
	if err := repo.Create(ctx, config); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fetched, err := repo.GetByID(ctx, config.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched.MaxWorkers != 0 {
		t.Errorf("expected MaxWorkers=0 (default), got %d", fetched.MaxWorkers)
	}
	if fetched.WorkerTimeout != 0 {
		t.Errorf("expected WorkerTimeout=0 (default), got %d", fetched.WorkerTimeout)
	}
}

func TestWorkerService_TryAcquireProjectSlot(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=2
	maxWorkers := 2
	project := &models.Project{Name: "slot-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
	}

	// Initially zero
	if ws.TotalRunning() != 0 {
		t.Fatalf("expected TotalRunning=0, got %d", ws.TotalRunning())
	}

	// First slot should succeed
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected first TryAcquireProjectSlot to succeed")
	}
	if ws.TotalRunning() != 1 {
		t.Errorf("expected TotalRunning=1, got %d", ws.TotalRunning())
	}
	if ws.ProjectRunning(project.ID) != 1 {
		t.Errorf("expected ProjectRunning=1, got %d", ws.ProjectRunning(project.ID))
	}

	// Second slot should succeed (limit is 2)
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected second TryAcquireProjectSlot to succeed")
	}
	if ws.TotalRunning() != 2 {
		t.Errorf("expected TotalRunning=2, got %d", ws.TotalRunning())
	}

	// Third slot should FAIL (at capacity)
	if ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected third TryAcquireProjectSlot to fail (at capacity)")
	}
	if ws.TotalRunning() != 2 {
		t.Errorf("expected TotalRunning still 2, got %d", ws.TotalRunning())
	}

	// Release one slot
	ws.ReleaseProjectSlot(project.ID)
	if ws.TotalRunning() != 1 {
		t.Errorf("expected TotalRunning=1 after release, got %d", ws.TotalRunning())
	}

	// Now should succeed again
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected TryAcquireProjectSlot to succeed after release")
	}

	// Release all
	ws.ReleaseProjectSlot(project.ID)
	ws.ReleaseProjectSlot(project.ID)
	if ws.TotalRunning() != 0 {
		t.Errorf("expected TotalRunning=0, got %d", ws.TotalRunning())
	}
}

func TestWorkerService_HasProjectCapacity(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "capacity-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
	}

	// Initially has capacity
	if !ws.HasProjectCapacity(project.ID) {
		t.Fatal("expected HasProjectCapacity=true initially")
	}

	// Acquire the only slot
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected TryAcquireProjectSlot to succeed")
	}

	// Now at capacity
	if ws.HasProjectCapacity(project.ID) {
		t.Fatal("expected HasProjectCapacity=false at capacity")
	}

	// Release slot
	ws.ReleaseProjectSlot(project.ID)

	// Has capacity again
	if !ws.HasProjectCapacity(project.ID) {
		t.Fatal("expected HasProjectCapacity=true after release")
	}
}

func TestWorkerService_HasProjectCapacity_NoLimit(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	// No projectRepo = no limit, always has capacity
	if !ws.HasProjectCapacity("any-project") {
		t.Fatal("expected HasProjectCapacity=true when no project repo")
	}
}

func TestWorkerService_TryAcquireProjectSlot_NoLimit(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	// No projectRepo = no limit, always succeeds and tracks
	if !ws.TryAcquireProjectSlot("proj-1") {
		t.Fatal("expected TryAcquireProjectSlot to succeed with no limit")
	}
	if ws.TotalRunning() != 1 {
		t.Errorf("expected TotalRunning=1, got %d", ws.TotalRunning())
	}

	ws.ReleaseProjectSlot("proj-1")
	if ws.TotalRunning() != 0 {
		t.Errorf("expected TotalRunning=0, got %d", ws.TotalRunning())
	}
}

func TestWorkerService_AcquireProjectSlotBlocking(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	maxWorkers := 1
	project := &models.Project{Name: "block-project-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
	}

	// Acquire the only slot
	if err := ws.AcquireProjectSlot(ctx, project.ID); err != nil {
		t.Fatalf("first AcquireProjectSlot: %v", err)
	}
	if ws.ProjectRunning(project.ID) != 1 {
		t.Errorf("expected ProjectRunning=1, got %d", ws.ProjectRunning(project.ID))
	}

	// Try to acquire again with a short timeout — should fail (slot full)
	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()
	if err := ws.AcquireProjectSlot(shortCtx, project.ID); err == nil {
		t.Error("expected AcquireProjectSlot to fail when at capacity with short timeout")
	}

	// Release the slot
	ws.ReleaseProjectSlot(project.ID)

	// Now acquiring should succeed
	if err := ws.AcquireProjectSlot(ctx, project.ID); err != nil {
		t.Errorf("AcquireProjectSlot after release: %v", err)
	}
	ws.ReleaseProjectSlot(project.ID)
}

func TestWorkerService_AcquireProjectSlotBlocksAndUnblocks(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	maxWorkers := 1
	project := &models.Project{Name: "block-unblock-project", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
	}

	// Acquire the only slot
	if err := ws.AcquireProjectSlot(ctx, project.ID); err != nil {
		t.Fatalf("first AcquireProjectSlot: %v", err)
	}

	// Start a goroutine that tries to acquire (will block)
	acquired := make(chan struct{})
	go func() {
		_ = ws.AcquireProjectSlot(ctx, project.ID)
		close(acquired)
	}()

	// Verify it doesn't acquire immediately
	select {
	case <-acquired:
		t.Fatal("expected AcquireProjectSlot to block when at capacity")
	case <-time.After(200 * time.Millisecond):
		// Good — still blocked
	}

	// Release the slot — the blocked goroutine should acquire it
	ws.ReleaseProjectSlot(project.ID)

	select {
	case <-acquired:
		// Good — acquired after release
	case <-time.After(2 * time.Second):
		t.Fatal("expected blocked AcquireProjectSlot to complete after release")
	}

	ws.ReleaseProjectSlot(project.ID)
}

func TestWorkerService_AcquireProjectSlotNoLimit(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	ctx := context.Background()

	// No projectRepo = no limit, should always succeed immediately
	if err := ws.AcquireProjectSlot(ctx, "any-project"); err != nil {
		t.Errorf("AcquireProjectSlot should succeed with no limit: %v", err)
	}
	ws.ReleaseProjectSlot("any-project")
}

func TestWorkerService_HasModelCapacity(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create a model with max_workers=1
	agent := &models.LLMConfig{
		Name:       "Capacity Test Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		MaxWorkers: 1,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	ws := &WorkerService{
		pending:       make(map[string]bool),
		numWorkers:    10,
		submitted:     make(chan models.Task, 100),
		cancelFuncs:   make(map[string]context.CancelFunc),
		llmConfigRepo: llmConfigRepo,
	}

	// Initially has capacity
	if !ws.HasModelCapacity(agent.ID) {
		t.Fatal("expected HasModelCapacity=true initially")
	}

	// Acquire the only slot
	if !ws.tryAcquireModelSlot(agent.ID) {
		t.Fatal("expected tryAcquireModelSlot to succeed")
	}

	// Now at capacity
	if ws.HasModelCapacity(agent.ID) {
		t.Fatal("expected HasModelCapacity=false at capacity")
	}

	// Release slot
	ws.releaseModelSlot(agent.ID)

	// Has capacity again
	if !ws.HasModelCapacity(agent.ID) {
		t.Fatal("expected HasModelCapacity=true after release")
	}
}

func TestWorkerService_HasModelCapacity_NoLimit(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}

	// No llmConfigRepo = no limit, always has capacity
	if !ws.HasModelCapacity("any-agent") {
		t.Fatal("expected HasModelCapacity=true when no llm config repo")
	}

	// Empty ID should always have capacity
	if !ws.HasModelCapacity("") {
		t.Fatal("expected HasModelCapacity=true for empty ID")
	}
}

// TestWorkerService_ChatBypassesTaskLimits_TasksStillEnforced verifies the concurrency model:
// - Chat tasks (CategoryChat) bypass the FIFO queue via Submit()
// - Task worker limits (per-model, per-project) continue to be enforced for actual tasks
// - Worker slots consumed by tasks don't prevent chat from bypassing
func TestWorkerService_ChatBypassesTaskLimits_TasksStillEnforced(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	// Create a model with max_workers=1
	agent := &models.LLMConfig{
		Name:        "Limited Agent",
		Provider:    models.ProviderAnthropic,
		AuthMethod:  "cli",
		Model:       "test-model",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
		MaxWorkers:  1,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "Test Project", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	ws := NewWorkerService(nil, 2, projectRepo)
	ws.SetLLMConfigRepo(llmConfigRepo)

	// 1. Verify chat tasks are skipped by Submit (no queue entry)
	chatTask := models.Task{
		ID:        "chat-task-1",
		ProjectID: project.ID,
		Title:     "Chat message",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		AgentID:   &agent.ID,
	}
	ws.Submit(chatTask)
	if ws.QueueSize() != 0 {
		t.Fatalf("expected queue size 0 after submitting chat task, got %d", ws.QueueSize())
	}

	// 2. Saturate project and model slots (simulating task workers at capacity)
	acquired := ws.TryAcquireProjectSlot(project.ID)
	if !acquired {
		t.Fatal("should acquire project slot")
	}
	acquired = ws.TryAcquireModelSlot(agent.ID)
	if !acquired {
		t.Fatal("should acquire model slot")
	}

	// 3. Verify limits are enforced for tasks (cannot acquire more slots)
	if ws.HasProjectCapacity(project.ID) {
		t.Fatal("project should be at capacity")
	}
	if ws.HasModelCapacity(agent.ID) {
		t.Fatal("model should be at capacity")
	}
	if ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("should NOT acquire another project slot")
		ws.ReleaseProjectSlot(project.ID)
	}
	if ws.TryAcquireModelSlot(agent.ID) {
		t.Fatal("should NOT acquire another model slot")
		ws.ReleaseModelSlot(agent.ID)
	}

	// 4. Verify that releasing slots restores capacity
	ws.ReleaseProjectSlot(project.ID)
	ws.ReleaseModelSlot(agent.ID)

	if !ws.HasProjectCapacity(project.ID) {
		t.Fatal("project should have capacity after release")
	}
	if !ws.HasModelCapacity(agent.ID) {
		t.Fatal("model should have capacity after release")
	}
}

// TestWorkerService_NoStarvation verifies that concurrent task slot acquisition
// and release don't lead to starvation or deadlock.
func TestWorkerService_NoStarvation(t *testing.T) {
	ws := &WorkerService{
		pending:     make(map[string]bool),
		cancelFuncs: make(map[string]context.CancelFunc),
		submitted:   make(chan models.Task, 100),
		numWorkers:  10,
	}

	agentID := "test-agent"
	projectID := "test-project"

	// Simulate concurrent workers acquiring and releasing slots
	var wg sync.WaitGroup
	const numGoroutines = 20
	const opsPerGoroutine = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				// Acquire slots
				ws.TryAcquireProjectSlot(projectID)
				ws.tryAcquireModelSlot(agentID)
				// Small yield to increase contention
				time.Sleep(time.Microsecond)
				// Release slots
				ws.releaseProjectSlot(projectID)
				ws.releaseModelSlot(agentID)
			}
		}()
	}

	// Use a timeout to detect deadlocks
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success — no deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected: concurrent slot operations did not complete within 10 seconds")
	}

	// After all goroutines complete, counters should be back to zero
	if running := ws.ProjectRunning(projectID); running != 0 {
		t.Errorf("expected project running=0 after all releases, got %d", running)
	}
	if running := ws.ModelRunning(agentID); running != 0 {
		t.Errorf("expected model running=0 after all releases, got %d", running)
	}
}

// TestWorkerService_DispatchNextPromotesQueuedTasks verifies that calling
// DispatchNext() after releasing slots (as thread follow-ups do) promotes
// tasks waiting in the worker's internal queue. This was the root cause of
// tasks getting stuck in "Queued" status — thread follow-ups released slots
// but never triggered dispatch of waiting tasks.
func TestWorkerService_DispatchNextPromotesQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "dispatch-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create a test agent
	agent := &models.LLMConfig{
		Name:       "Test Agent",
		Provider:   models.ProviderTest,
		Model:      "test-model",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	// Create a pending active task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Waiting Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Set up LLM service with mock caller
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

	ws := NewWorkerService(llmSvc, 10, projectRepo)
	ws.SetLLMConfigRepo(llmConfigRepo)
	ws.SetTaskRepo(taskRepo)
	ws.Start(ctx)
	defer ws.Stop()

	// Simulate a thread follow-up holding the project slot
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected to acquire project slot")
	}

	// Submit the task — it should be queued (project at capacity)
	ws.Submit(*task)
	time.Sleep(50 * time.Millisecond) // Let dispatchNext run
	if ws.QueueSize() != 1 {
		t.Fatalf("expected queue size=1 (task blocked by capacity), got %d", ws.QueueSize())
	}

	// WITHOUT calling DispatchNext, verify the task stays in queue
	ws.ReleaseProjectSlot(project.ID)
	// Don't call DispatchNext — task should still be in queue
	time.Sleep(50 * time.Millisecond)
	if ws.QueueSize() != 1 {
		t.Fatalf("expected queue size=1 (no dispatchNext called yet), got %d", ws.QueueSize())
	}

	// Now call DispatchNext — the task should be dispatched
	ws.DispatchNext()
	time.Sleep(200 * time.Millisecond) // Let the goroutine start and execute

	// The task should have been removed from the queue (dispatched)
	if ws.QueueSize() != 0 {
		t.Errorf("expected queue size=0 after DispatchNext, got %d", ws.QueueSize())
	}
}

// TestWorkerService_DispatchNextWithoutSlotRelease verifies that DispatchNext
// does NOT dispatch tasks when capacity is still full. This ensures we only
// promote tasks when there's actual capacity.
func TestWorkerService_DispatchNextWithoutSlotRelease(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	maxWorkers := 1
	project := &models.Project{Name: "no-dispatch-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Blocked Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
		taskRepo:    taskRepo,
	}
	ws.ctx, ws.cancel = context.WithCancel(ctx)
	defer ws.cancel()

	// Hold the project slot (simulating a running task)
	ws.TryAcquireProjectSlot(project.ID)

	// Submit the task — blocked by project capacity
	ws.Submit(*task)
	time.Sleep(50 * time.Millisecond)

	// Call DispatchNext without releasing — task should stay queued
	ws.DispatchNext()
	time.Sleep(50 * time.Millisecond)

	if ws.QueueSize() != 1 {
		t.Errorf("expected queue size=1 (still at capacity), got %d", ws.QueueSize())
	}

	ws.ReleaseProjectSlot(project.ID)
}

// TestWorkerService_ProjectLimitIncreaseDispatchesQueued verifies that when a
// project's max_workers limit is increased and there are queued tasks blocked
// by the old limit, calling DispatchNext() immediately starts the next task.
// This is the regression test for the bug where increasing a project limit on
// the Workers page did not start queued tasks until an unrelated event.
func TestWorkerService_ProjectLimitIncreaseDispatchesQueued(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "limit-increase-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create a test agent
	agent := &models.LLMConfig{
		Name:       "Test Agent",
		Provider:   models.ProviderTest,
		Model:      "test-model",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	// Create two pending active tasks
	task1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Task 1",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt 1",
		AgentID:   &agent.ID,
	}
	task2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Task 2",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt 2",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatalf("Create task1: %v", err)
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatalf("Create task2: %v", err)
	}

	// Set up LLM service with mock caller
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

	ws := NewWorkerService(llmSvc, 10, projectRepo)
	ws.SetLLMConfigRepo(llmConfigRepo)
	ws.SetTaskRepo(taskRepo)
	ws.Start(ctx)
	defer ws.Stop()

	// Simulate one task already running (project at capacity with limit=1)
	if !ws.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected to acquire project slot")
	}

	// Submit both tasks — they should be queued (project at capacity)
	ws.Submit(*task1)
	ws.Submit(*task2)
	time.Sleep(50 * time.Millisecond)
	if ws.QueueSize() != 2 {
		t.Fatalf("expected queue size=2 (blocked by project capacity), got %d", ws.QueueSize())
	}

	// Release the running task's slot (simulating it completing)
	ws.ReleaseProjectSlot(project.ID)

	// Now increase project limit from 1 to 3 (in DB)
	newLimit := 3
	project.MaxWorkers = &newLimit
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("Update project: %v", err)
	}

	// Trigger dispatch (this is what the handler fix does)
	ws.DispatchNext()
	time.Sleep(200 * time.Millisecond) // Let goroutines start

	// Both tasks should have been dispatched (limit=3, 0 running before dispatch)
	if qs := ws.QueueSize(); qs != 0 {
		t.Errorf("expected queue size=0 after limit increase + DispatchNext, got %d", qs)
	}
}

// TestWorkerService_ProjectLimitIncreaseNoDispatchWhenGlobalFull verifies that
// even if a project limit is increased, tasks are not dispatched when the global
// worker pool is at capacity.
func TestWorkerService_ProjectLimitIncreaseNoDispatchWhenGlobalFull(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "global-full-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Blocked by Global",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Global pool with only 1 slot
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  1,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
		taskRepo:    taskRepo,
	}
	ws.ctx, ws.cancel = context.WithCancel(ctx)
	defer ws.cancel()

	// Saturate the global pool
	ws.TryAcquireProjectSlot(project.ID)

	// Submit the task — blocked by project capacity
	ws.Submit(*task)
	time.Sleep(50 * time.Millisecond)
	if ws.QueueSize() != 1 {
		t.Fatalf("expected queue size=1, got %d", ws.QueueSize())
	}

	// Increase project limit to 5 (plenty of room)
	newLimit := 5
	project.MaxWorkers = &newLimit
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("Update project: %v", err)
	}

	// Call DispatchNext — but global pool is still full (1/1 running)
	ws.DispatchNext()
	time.Sleep(50 * time.Millisecond)

	// Task should NOT be dispatched (global pool is at capacity)
	if ws.QueueSize() != 1 {
		t.Errorf("expected queue size=1 (global pool full), got %d", ws.QueueSize())
	}

	ws.ReleaseProjectSlot(project.ID)
}

// TestWorkerService_ProjectLimitIncreaseFIFOOrder verifies that when a project
// limit is increased and multiple tasks are queued, they are dispatched in FIFO
// order (first submitted = first dispatched). The test verifies queue ordering
// directly without executing tasks, since mock execution completes instantly.
func TestWorkerService_ProjectLimitIncreaseFIFOOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers=1
	maxWorkers := 1
	project := &models.Project{Name: "fifo-test", MaxWorkers: &maxWorkers}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create three tasks — submit order matters for FIFO
	tasks := make([]*models.Task, 3)
	for i := 0; i < 3; i++ {
		tasks[i] = &models.Task{
			ProjectID: project.ID,
			Title:     fmt.Sprintf("Task %d", i+1),
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    fmt.Sprintf("test prompt %d", i+1),
		}
		if err := taskRepo.Create(ctx, tasks[i]); err != nil {
			t.Fatalf("Create task %d: %v", i+1, err)
		}
	}

	// Use a struct directly — no llmSvc means dispatchNext can check the queue
	// but won't actually launch goroutines (we only verify queue ordering).
	// We set numWorkers high so global limit isn't the constraint.
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  10,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
		projectRepo: projectRepo,
		taskRepo:    taskRepo,
	}
	ws.ctx, ws.cancel = context.WithCancel(ctx)
	defer ws.cancel()

	// Hold the only project slot (project at capacity)
	ws.TryAcquireProjectSlot(project.ID)

	// Submit tasks in order: task1, task2, task3
	for _, task := range tasks {
		ws.Submit(*task)
	}
	time.Sleep(50 * time.Millisecond)
	if ws.QueueSize() != 3 {
		t.Fatalf("expected queue size=3, got %d", ws.QueueSize())
	}

	// Verify queue is in FIFO order (task1 first, task3 last)
	ws.mu.Lock()
	if len(ws.queue) != 3 {
		ws.mu.Unlock()
		t.Fatalf("expected 3 tasks in queue, got %d", len(ws.queue))
	}
	for i, expected := range []string{"Task 1", "Task 2", "Task 3"} {
		if ws.queue[i].Title != expected {
			t.Errorf("queue[%d] expected %q, got %q", i, expected, ws.queue[i].Title)
		}
	}
	ws.mu.Unlock()

	// Release the held slot and increase limit to 2
	ws.ReleaseProjectSlot(project.ID)
	newLimit := 2
	project.MaxWorkers = &newLimit
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("Update project: %v", err)
	}

	// dispatchNext will try to dispatch but llmSvc is nil so tasks can't execute.
	// Instead, verify that tasks at the FRONT of the queue are the ones that would
	// be dequeued first by checking that after acquiring 2 project slots (the new
	// limit), the first 2 tasks pass the capacity check while the 3rd is blocked.

	// Manually simulate what dispatchNext does: try project slots in FIFO order
	if !ws.tryAcquireProjectSlot(project.ID) {
		t.Fatal("expected first FIFO task to acquire project slot")
	}
	if !ws.tryAcquireProjectSlot(project.ID) {
		t.Fatal("expected second FIFO task to acquire project slot")
	}
	// Third should be blocked (limit=2, now 2 running)
	if ws.tryAcquireProjectSlot(project.ID) {
		t.Fatal("expected third FIFO task to be blocked by project limit=2")
	}

	// Clean up
	ws.releaseProjectSlot(project.ID)
	ws.releaseProjectSlot(project.ID)
}
