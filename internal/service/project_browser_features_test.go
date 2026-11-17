package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/agenttools"
)

func TestProjectBrowserActionsErrorsImagesAndPrivateHandoff(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Browser features", RepoPath: t.TempDir()}
	if err := db.Projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Browser", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending}
	if err := db.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(`{"ok":true}`)
	var browserCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/browser") {
			browserCalls.Add(1)
			io.WriteString(w, response.Load().(string))
			return
		}
		io.WriteString(w, `{"ready":true}`)
	}))
	defer server.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "fixture"}})
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	enableComputerTaskForTest(t, svc, project.ID, task.ID)
	runtime := svc.RuntimeTools(t.Context(), project.ID, task.ID)
	for _, action := range projectBrowserActions() {
		payload, _ := json.Marshal(map[string]any{"action": action, "tab_id": "fixture-tab"})
		_, handled, bad, err := runtime.Executor(t.Context(), "project_browser", payload)
		if !handled || bad || err != nil {
			t.Fatalf("action %s: %v %v", action, bad, err)
		}
	}
	for _, action := range []string{"evaluate", "import_cookies", "clear_cookies"} {
		before := browserCalls.Load()
		payload, _ := json.Marshal(map[string]any{"action": action})
		_, _, bad, err := runtime.Executor(t.Context(), "project_browser", payload)
		if !bad || err == nil || browserCalls.Load() != before {
			t.Fatalf("restricted action reached computer: %s", action)
		}
	}
	response.Store(`{"error":{"code":"multiple_matches","message":"Narrow the locator."}}`)
	output, _, bad, err := runtime.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"click"}`))
	if err != nil || !bad || !strings.Contains(output, "multiple_matches") {
		t.Fatalf("lost browser error: %s %v", output, err)
	}
	response.Store(`{"value":"content"}`)
	output, _, bad, err = runtime.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"inspect","property":"text","locator":{"role":"button"}}`))
	if err != nil || bad || output != `{"value":"content"}` {
		t.Fatalf("ordinary text mistaken for image: %s %v", output, err)
	}
	response.Store(`{"content":[{"type":"image","mimeType":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aL1sAAAAASUVORK5CYII="}]}`)
	output, _, bad, err = runtime.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"screenshot"}`))
	if _, ok := agenttools.DecodeImageResult(output); !ok || bad || err != nil {
		t.Fatalf("screenshot not encoded: %v", err)
	}
	response.Store(`{"ok":true}`)
	if _, _, _, err = runtime.Executor(t.Context(), "request_browser_login", json.RawMessage(`{"url":"https://example.test/login","expected_text":"Dashboard"}`)); err != nil {
		t.Fatal(err)
	}
	before := browserCalls.Load()
	for _, action := range projectBrowserActions() {
		payload, _ := json.Marshal(map[string]any{"action": action})
		_, _, bad, err := runtime.Executor(t.Context(), "project_browser", payload)
		if !bad || err == nil {
			t.Fatalf("private handoff allowed %s", action)
		}
	}
	if browserCalls.Load() != before {
		t.Fatal("private handoff forwarded browser requests")
	}
}
