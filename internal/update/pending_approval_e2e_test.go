package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
)

// This fixture uses the real worker, provider adapter, question broker, database,
// updater and replacement process. Only the model endpoint is simulated; declining
// the dependency request after restart avoids downloading any external software.
type pendingApprovalE2E struct {
	stores   persistence.Stores
	task     models.Task
	question persistence.ChatInputRequestRecord
	requests atomic.Int32
}

func preparePendingApprovalE2E(t *testing.T, appData string) *pendingApprovalE2E {
	t.Helper()
	if err := os.MkdirAll(appData, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := sqlite.Open(filepath.Join(appData, "openvibely.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	f := &pendingApprovalE2E{stores: backend.Stores()}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var input struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(data, &input); err != nil {
			t.Error(err)
			return
		}
		n := f.requests.Add(1)
		if !input.Stream {
			http.Error(w, "fixture expects streaming task calls", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_update_wait\",\"type\":\"function\",\"function\":{\"name\":\"skill_view\",\"arguments\":\"{\\\"handle\\\":\\\"update_wait\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			if !strings.Contains(string(data), "call_update_wait") || !strings.Contains(string(data), "declined") {
				t.Errorf("continuation lost original tool call or decision: %s", data)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"UPDATE_APPROVAL_RESUMED\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(modelServer.Close)
	ctx := context.Background()
	model := models.LLMConfig{Name: "Update E2E model", Provider: models.ProviderOpenAICompatible, Model: "fixture", APIKey: "fixture", AuthMethod: models.AuthMethodAPIKey, BaseURL: modelServer.URL + "/v1", Transport: "chat_completions", PresetSlug: "vllm", MaxTokens: 4096, ContextWindow: 128000, IsDefault: true}
	if err := f.stores.LLMConfigs.Create(ctx, &model); err != nil {
		t.Fatal(err)
	}
	project := models.Project{Name: "Update E2E project", DefaultAgentConfigID: &model.ID}
	if err := f.stores.Projects.Create(ctx, &project); err != nil {
		t.Fatal(err)
	}
	f.task = models.Task{ProjectID: project.ID, Title: "Dependency approval during update", Prompt: "Read update_wait using skill_view, then finish.", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, AgentID: &model.ID}
	if err := f.stores.Tasks.Create(ctx, &f.task); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(appData, "skills", "update_wait")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill := fmt.Sprintf(`---
kind: openvibely.agent_skill
version: 3
skill:
  key: update_wait
  scope: global
  enabled: true
dependencies:
  - name: update-e2e-missing-dependency
    version: 1.0.0
    environment_variable: OPENVIBELY_UPDATE_E2E_MISSING_DEPENDENCY
    health_check: [version]
    packages:
      %s/%s:
        url: https://example.invalid/never-downloaded.zip
        sha256: %s
        format: zip
        executable: missing-dependency
---
Fixture instructions.
`, runtime.GOOS, runtime.GOARCH, strings.Repeat("a", 64))
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appData, "skills", "SKILLS.md"), []byte("---\nalways_use: [update_wait]\n---\n\n## update_wait\n\nDependency approval fixture.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *pendingApprovalE2E) startAndWait(t *testing.T, baseURL string, logs func() string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tasks/"+f.task.ID+"/run?project_id="+f.task.ProjectID, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start worker HTTP %d: %s\n%s", resp.StatusCode, body, logs())
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		executions, err := f.stores.Executions.ListByTask(context.Background(), f.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, execution := range executions {
			questions, err := f.stores.ChatInputRequests.ListByExecutions(context.Background(), f.task.ProjectID, []string{execution.ID})
			if err != nil {
				t.Fatal(err)
			}
			for _, question := range questions {
				if question.Status == persistence.ChatInputRequestPending {
					if execution.Status != models.ExecRunning {
						t.Fatalf("worker not running at approval: %s", execution.Status)
					}
					f.question = question
					if f.requests.Load() != 1 {
						t.Fatal("model continued before approval")
					}
					t.Logf("real worker %s is waiting on persisted dependency question %s", execution.ID, question.ID)
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("worker did not request dependency approval\n%s", logs())
}

func (f *pendingApprovalE2E) answerAfterRestart(t *testing.T, baseURL string, logs func() string) {
	t.Helper()
	ctx := context.Background()
	question, err := f.stores.ChatInputRequests.Get(ctx, f.question.ID)
	if err != nil || question == nil || question.Status != persistence.ChatInputRequestPending {
		t.Fatalf("question did not survive restart: %+v %v", question, err)
	}
	execution, err := f.stores.Executions.GetByID(ctx, question.ExecutionID)
	if err != nil || execution == nil || execution.Status == models.ExecRunning {
		t.Fatalf("old worker still running after update: %+v %v", execution, err)
	}
	if f.requests.Load() != 1 {
		t.Fatal("model ran again before the saved question was answered")
	}
	t.Logf("replacement process is healthy; old execution status=%s; question remains pending", execution.Status)
	data := fmt.Sprintf(`{"project_id":%q,"answers":[{"question_id":"install","label":"Not now"}]}`, f.task.ProjectID)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/chat/input-requests/"+question.ID+"/answer?project_id="+f.task.ProjectID, strings.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("X-OpenVibely-UI", "chat-input-answer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer after restart HTTP %d: %s\n%s", resp.StatusCode, body, logs())
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		executions, err := f.stores.Executions.ListByTask(ctx, f.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range executions {
			if run.ID != question.ExecutionID && run.Status == models.ExecCompleted && strings.Contains(run.Output, "UPDATE_APPROVAL_RESUMED") {
				t.Logf("saved answer resumed task to completion in execution %s", run.ID)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task did not resume after restart\n%s", logs())
}
