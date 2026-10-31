package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/web/templates/components"
)

type countingDiffStore struct {
	persistence.ExecutionStore
	reads int
}

func (s *countingDiffStore) GetLatestNonEmptyDiffOutput(ctx context.Context, taskID string) (string, error) {
	s.reads++
	return s.ExecutionStore.GetLatestNonEmptyDiffOutput(ctx, taskID)
}

func TestHandler_DiffChunkSnapshotCache(t *testing.T) {
	h, e, _, db := setupTestHandlerWithStores(t)
	defer db.Close()
	ctx := context.Background()
	projects, err := db.Projects.List(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: projects[0].ID, Title: "cached diff", Prompt: "test", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatal(err)
	}
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agents[0].ID, Status: models.ExecCompleted, PromptSent: "test"}
	if err := h.execRepo.Create(ctx, execution); err != nil {
		t.Fatal(err)
	}
	store := &countingDiffStore{ExecutionStore: h.execRepo}
	h.execRepo = store
	diff := "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,600 @@\n" + strings.Repeat("+original\n", 600)
	save := func(text string) string {
		t.Helper()
		if err := store.UpdateDiffOutput(ctx, execution.ID, text); err != nil {
			t.Fatal(err)
		}
		return components.DiffFileRevision(components.ParseDiffOutput(text)[0], nil)
	}
	revision := save(diff)
	request := func(rev, view, path string, chunk int, status int) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%s/changes/file?file_index=0&chunk=%d&view=%s&revision=%s&file_path=%s", task.ID, chunk, view, rev, path), nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("taskId")
		c.SetParamValues(task.ID)
		if err := h.GetTaskChangesFile(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		if rec.Code != status {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
		}
		return rec.Body.String()
	}
	for _, view := range []string{"inline", "split"} {
		for chunk := 0; chunk < 3; chunk++ {
			body := request(revision, view, "big.txt", chunk, 200)
			if !strings.Contains(body, "original") {
				t.Fatal("missing content")
			}
		}
	}
	if store.reads != 1 {
		t.Fatalf("scrolling and layout switch read source %d times, want 1", store.reads)
	}
	request(revision, "inline", "wrong.txt", 0, 409)
	request(revision, "inline", "big.txt", 99, 400)
	// Existing readers finish the immutable version; refreshed viewers get new code.
	next := save(strings.ReplaceAll(diff, "original", "updated"))
	if !strings.Contains(request(revision, "inline", "big.txt", 2, 200), "original") {
		t.Fatal("mixed versions in old snapshot")
	}
	if !strings.Contains(request(next, "inline", "big.txt", 2, 200), "updated") {
		t.Fatal("new revision reused old snapshot")
	}
	if store.reads != 2 {
		t.Fatalf("new revision: reads=%d want 2", store.reads)
	}
	request("unknown", "inline", "big.txt", 0, 409)
	// An evicted version is revalidated against the source, never relabeled.
	h.diffChunks.mu.Lock()
	h.diffChunks.entries = nil
	h.diffChunks.bytes = 0
	h.diffChunks.mu.Unlock()
	request(revision, "inline", "big.txt", 0, 409)
}
