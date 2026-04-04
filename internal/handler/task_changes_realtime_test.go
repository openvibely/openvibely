package handler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestGetTaskChanges_ShowsRealtimeDiffDuringExecution verifies that the Changes tab
// displays in-progress diffs from running executions, not just completed ones.
// This is the end-to-end test for the realtime diff update feature.
func TestGetTaskChanges_ShowsRealtimeDiffDuringExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	// Create test project
	project := &models.Project{
		Name:     "test-project",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create agent
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Create task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "test realtime changes",
		Prompt:    "test prompt",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning, // Task is running
		Priority:  2,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create running execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning, // Execution is in progress
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	// BEFORE FIX: DiffOutput would be empty during execution
	// AFTER FIX: Simulate periodic diff update during execution
	testDiff := `diff --git a/main.go b/main.go
new file mode 100644
index 0000000..d3103f9
--- /dev/null
+++ b/main.go
@@ -0,0 +1,3 @@
+package main
+
+func main() {}
`

	// Update the running execution's diff output (this is what broadcastDiffSnapshots does)
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, testDiff); err != nil {
		t.Fatalf("update diff output: %v", err)
	}

	// Set up handler
	attachmentRepo := repository.NewAttachmentRepo(db)
	h := &Handler{
		projectRepo:       projectRepo,
		taskRepo:          taskRepo,
		execRepo:          execRepo,
		reviewCommentRepo: repository.NewReviewCommentRepo(db),
		taskSvc:           service.NewTaskService(taskRepo, attachmentRepo, nil),
	}

	// Create Echo instance
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	// Call the handler
	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges error: %v", err)
	}

	// Verify the response includes the in-progress diff
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got: %d", rec.Code)
	}

	body := rec.Body.String()

	// The response should contain the diff content from the running execution
	if !strings.Contains(body, "func main") {
		t.Errorf("response should contain diff content from running execution, got:\n%s", body)
	}

	// Should show the file being added
	if !strings.Contains(body, "main.go") {
		t.Errorf("response should contain filename from diff, got:\n%s", body)
	}

	// Verify the diff is visible even though execution is still running
	freshExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if freshExec.Status != models.ExecRunning {
		t.Errorf("expected execution to still be running, got: %s", freshExec.Status)
	}
}

// TestGetTaskChanges_RealtimeUpdateFlow simulates the complete realtime update flow:
// 1. Task starts running
// 2. Agent makes file changes
// 3. Diff broadcaster updates execution diff
// 4. User opens Changes tab
// 5. Changes tab shows current diff (not waiting for completion)
func TestGetTaskChanges_RealtimeUpdateFlow(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	project := &models.Project{
		Name:     "test-project",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "test realtime flow",
		Prompt:    "create a new file",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Priority:  2,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Step 1: Task starts running
	claimed, err := taskRepo.ClaimTask(ctx, task.ID)
	if err != nil || !claimed {
		t.Fatalf("claim task: claimed=%v err=%v", claimed, err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	// Step 2 & 3: Agent makes changes, diff broadcaster captures and persists
	// (Simulating what broadcastDiffSnapshots does every 2 seconds)
	initialDiff := `diff --git a/file1.txt b/file1.txt
new file mode 100644
index 0000000..ce01362
--- /dev/null
+++ b/file1.txt
@@ -0,0 +1 @@
+hello
`

	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, initialDiff); err != nil {
		t.Fatalf("first diff update: %v", err)
	}

	// Step 4: User opens Changes tab while task is running
	attachmentRepo := repository.NewAttachmentRepo(db)
	h := &Handler{
		projectRepo:       projectRepo,
		taskRepo:          taskRepo,
		execRepo:          execRepo,
		reviewCommentRepo: repository.NewReviewCommentRepo(db),
		taskSvc:           service.NewTaskService(taskRepo, attachmentRepo, nil),
	}

	e := echo.New()
	req1 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	c1.SetParamNames("taskId")
	c1.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c1); err != nil {
		t.Fatalf("GetTaskChanges (first): %v", err)
	}

	// Step 5: Verify Changes tab shows current diff
	if !strings.Contains(rec1.Body.String(), "file1.txt") {
		t.Error("expected first diff to show file1.txt")
	}

	// Step 6: Agent continues working, diff is updated again
	time.Sleep(100 * time.Millisecond) // Simulate passage of time
	updatedDiff := `diff --git a/file1.txt b/file1.txt
new file mode 100644
index 0000000..ce01362
--- /dev/null
+++ b/file1.txt
@@ -0,0 +1 @@
+hello
diff --git a/file2.txt b/file2.txt
new file mode 100644
index 0000000..b6fc4c6
--- /dev/null
+++ b/file2.txt
@@ -0,0 +1 @@
+world
`

	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, updatedDiff); err != nil {
		t.Fatalf("second diff update: %v", err)
	}

	// Step 7: User refreshes Changes tab (or SSE triggers refresh)
	req2 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetParamNames("taskId")
	c2.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c2); err != nil {
		t.Fatalf("GetTaskChanges (second): %v", err)
	}

	// Step 8: Verify updated diff is shown (now includes file2.txt)
	body := rec2.Body.String()
	if !strings.Contains(body, "file1.txt") {
		t.Error("expected updated diff to still show file1.txt")
	}
	if !strings.Contains(body, "file2.txt") {
		t.Error("expected updated diff to show new file2.txt")
	}

	// Execution should still be running (not completed)
	freshExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if freshExec.Status != models.ExecRunning {
		t.Errorf("execution should still be running, got: %s", freshExec.Status)
	}
}

// TestGetTaskChanges_StableDOMOnUnchangedDiff verifies that repeated requests to the
// Changes endpoint with the same diff produce identical HTML. This is a regression test
// for the viewport-jump bug: the frontend uses a content fingerprint to skip DOM
// replacement when the diff hasn't changed, so stable server responses are critical.
func TestGetTaskChanges_StableDOMOnUnchangedDiff(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	project := &models.Project{
		Name:     "test-project",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "stable DOM test",
		Prompt:    "test",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Priority:  2,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	diffContent := `diff --git a/main.go b/main.go
new file mode 100644
--- /dev/null
+++ b/main.go
@@ -0,0 +1,5 @@
+package main
+
+func main() {
+	println("hello")
+}
diff --git a/util.go b/util.go
new file mode 100644
--- /dev/null
+++ b/util.go
@@ -0,0 +1,3 @@
+package main
+
+func helper() {}
`
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, diffContent); err != nil {
		t.Fatalf("update diff: %v", err)
	}

	attachmentRepo := repository.NewAttachmentRepo(db)
	h := &Handler{
		projectRepo:       projectRepo,
		taskRepo:          taskRepo,
		execRepo:          execRepo,
		reviewCommentRepo: repository.NewReviewCommentRepo(db),
		taskSvc:           service.NewTaskService(taskRepo, attachmentRepo, nil),
	}

	e := echo.New()

	// Make 3 identical requests with the same unchanged diff
	var hashes []string
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("taskId")
		c.SetParamValues(task.ID)

		if err := h.GetTaskChanges(c); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}

		body := rec.Body.String()
		// Verify content is present
		if !strings.Contains(body, "main.go") {
			t.Fatalf("request %d: expected main.go in response", i)
		}
		if !strings.Contains(body, "util.go") {
			t.Fatalf("request %d: expected util.go in response", i)
		}

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		hashes = append(hashes, hash)
	}

	// All responses must be byte-identical (stable DOM) so the frontend
	// fingerprint can reliably detect no-change and skip DOM replacement.
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("response %d differs from response 0: server returns non-deterministic HTML for the same diff; this causes unnecessary DOM replacement and viewport jumps", i)
		}
	}
}

// TestGetTaskChanges_DiffUpdateProducesDifferentDOM verifies that when the diff changes,
// the server returns different HTML. This complements TestGetTaskChanges_StableDOMOnUnchangedDiff
// and ensures the frontend fingerprint correctly detects actual content changes.
func TestGetTaskChanges_DiffUpdateProducesDifferentDOM(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	project := &models.Project{
		Name:     "test-project",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "diff update DOM test",
		Prompt:    "test",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Priority:  2,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	attachmentRepo := repository.NewAttachmentRepo(db)
	h := &Handler{
		projectRepo:       projectRepo,
		taskRepo:          taskRepo,
		execRepo:          execRepo,
		reviewCommentRepo: repository.NewReviewCommentRepo(db),
		taskSvc:           service.NewTaskService(taskRepo, attachmentRepo, nil),
	}

	e := echo.New()

	// First request with initial diff
	diff1 := `diff --git a/file1.go b/file1.go
new file mode 100644
--- /dev/null
+++ b/file1.go
@@ -0,0 +1 @@
+package main
`
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, diff1); err != nil {
		t.Fatalf("update diff 1: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	c1.SetParamNames("taskId")
	c1.SetParamValues(task.ID)
	if err := h.GetTaskChanges(c1); err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1 := rec1.Body.String()

	// Second request with updated diff (new file added)
	diff2 := diff1 + `diff --git a/file2.go b/file2.go
new file mode 100644
--- /dev/null
+++ b/file2.go
@@ -0,0 +1 @@
+package util
`
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, diff2); err != nil {
		t.Fatalf("update diff 2: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetParamNames("taskId")
	c2.SetParamValues(task.ID)
	if err := h.GetTaskChanges(c2); err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2 := rec2.Body.String()

	// Responses must differ (new file added)
	hash1 := fmt.Sprintf("%x", sha256.Sum256([]byte(body1)))
	hash2 := fmt.Sprintf("%x", sha256.Sum256([]byte(body2)))
	if hash1 == hash2 {
		t.Error("expected different HTML when diff content changes; same content would hide updates from users")
	}

	// Second response should contain the new file
	if !strings.Contains(body2, "file2.go") {
		t.Error("second response should contain file2.go")
	}
}
