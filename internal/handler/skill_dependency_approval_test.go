package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/update"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSkillDependencyDecisionsRequireHumanSessionAndOrigin(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithStores(t)
	h.SetAuthConfig(auth.Config{Enabled: true, Username: "admin", Password: "secret", SessionSecret: "approval-test-secret", SessionTTL: time.Hour})
	token, err := h.authCfg.SignToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		cookie         bool
		origin, header string
		want           int
	}{
		{"model headers without session", false, "http://example.com", "chat-input-answer", 401},
		{"session without UI header", true, "http://example.com", "", 403},
		{"cross origin", true, "http://attacker.test", "chat-input-answer", 403},
		{"missing origin", true, "", "chat-input-answer", 403},
		{"human same origin", true, "http://example.com", "chat-input-answer", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com/chat/input-requests/request/decision?project_id=default", strings.NewReader(`{"decision":"allow"}`))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-OpenVibely-UI", tc.header)
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
			}
			c := e.NewContext(req, httptest.NewRecorder())
			c.Set("auth_user", "admin") // model-supplied context cannot impersonate the signed browser session.
			err := h.requireDependencyHuman(c)
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			httpErr, ok := err.(*echo.HTTPError)
			if !ok || httpErr.Code != tc.want {
				t.Fatalf("got %v, want %d", err, tc.want)
			}
		})
	}
}

func TestInstallationLateAnswerContinuesWithoutFollowupQueue(t *testing.T) {
	type scenario struct {
		category  models.TaskCategory
		cancelled bool
		label     string
	}
	var scenarios []scenario
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive, models.CategoryScheduled, models.CategoryBacklog, models.CategoryCompleted} {
		scenarios = append(scenarios, scenario{category, false, "Install dependencies"}, scenario{category, false, "Not now"}, scenario{category, true, "Install dependencies"}, scenario{category, true, "Not now"})
	}
	for _, tc := range scenarios {
		category := tc.category
		t.Run(fmt.Sprintf("%s/cancelled=%t/%s", category, tc.cancelled, tc.label), func(t *testing.T) {
			h, e, configs, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "install followup")
			task := createTask(t, h, project.ID, "install", func(task *models.Task) {
				task.Category = category
				task.Status = models.StatusFailed
				task.AgentID = &model.ID
			})
			execution := createExec(t, h, task.ID, model.ID, func(execution *models.Execution) { execution.Status = models.ExecRunning })
			manifest := skilldeps.InstallationRequest{Identity: skilldeps.SkillIdentity{ProjectID: project.ID, Source: "project", Skill: "fixture"}}
			waitCtx, cancel := context.WithCancel(skilldeps.WithSkillViewArguments(ctx, json.RawMessage(`{"handle":"fixture","file_path":"scripts/help.md"}`)))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- h.requestSkillInstallation(waitCtx, contracts.AgentRequest{ExecID: execution.ID, ProjectID: project.ID, ChatMode: models.ChatModeOrchestrate}, manifest)
			}()
			var question persistence.ChatInputRequestRecord
			deadline := time.After(5 * time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for question.ID == "" {
				select {
				case <-deadline:
					t.Fatal("missing question")
				case <-ticker.C:
					records, err := h.chatInputRequestRepo.ListByExecutions(ctx, project.ID, []string{execution.ID})
					if err != nil {
						t.Fatal(err)
					}
					if len(records) > 0 {
						question = records[0]
					}
				}
			}
			cancel()
			if err := <-done; err == nil {
				t.Fatal("cancelled wait succeeded")
			}
			if tc.cancelled {
				task.Status = models.StatusCancelled
				if err := h.taskRepo.Update(ctx, task); err != nil {
					t.Fatal(err)
				}
				if err := h.execRepo.Complete(ctx, execution.ID, models.ExecCancelled, "", "cancelled", 0, 0); err != nil {
					t.Fatal(err)
				}
			}
			// A fresh broker simulates process restart; the shared record persists.
			h.chatInputRequests = newChatInputRequestBroker()
			if _, err := h.taskRepo.ResetOrphanedRunning(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.execRepo.RecoverStaleRunningTaskExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := h.chatInputRequestRepo.Get(ctx, question.ID)
			if err != nil || saved.Status != persistence.ChatInputRequestPending {
				t.Fatalf("recovery lost question: %+v %v", saved, err)
			}

			seedPendingInstallationTranscript(t, h, execution.ID, question.ID)
			calls := 0
			mock := testutil.NewMockLLMCaller()
			mock.OnCall = func(callCtx context.Context, call testutil.MockLLMCall) {
				calls++
				if call.ExecID == execution.ID {
					t.Error("restarted old execution")
				}
				want := "not in this turn" // Revalidate removed skill access on Yes.
				if tc.label == "Not now" {
					want = "human declined dependency installation"
				}
				assertInstallationToolContinuation(t, mock, want)
				if category != models.CategoryChat {
					currentTask, err := h.taskRepo.GetByID(callCtx, task.ID)
					wantCategory := category
					if category == models.CategoryBacklog || category == models.CategoryCompleted {
						wantCategory = models.CategoryActive
					}
					if err != nil || currentTask == nil || currentTask.Category != wantCategory {
						t.Fatalf("continuation category: %+v %v; want %s", currentTask, err, wantCategory)
					}
					// This ordinary lookup runs stale-execution recovery. It must
					// recognize the continuation instead of marking it failed.
					active, err := h.execRepo.HasActiveTaskThreadExecution(callCtx, task.ID, "")
					if err != nil || !active {
						t.Fatalf("continuation no longer active: %v %v", active, err)
					}
					current, err := h.execRepo.GetByID(callCtx, call.ExecID)
					if err != nil || current == nil || current.Status != models.ExecRunning {
						t.Fatalf("continuation recovered as stale: %+v %v", current, err)
					}
				}

			}
			h.llmSvc.SetLLMCaller(mock)
			// Direct continuation must work without the follow-up queue service.
			inputs := h.threadInputRepo
			h.threadInputRepo = nil
			// The skill was removed from the authorized catalog after the question.
			// The app supplies the permission error as the result, never asks the model to retry.
			h.startStreamingResponseOverride = func(params streamingResponseParams) error { h.processStreamingResponse(params); return nil }

			h.SetAuthConfig(auth.Config{Enabled: true, Username: "admin", Password: "secret", SessionSecret: "install-test", SessionTTL: time.Hour})
			token, err := h.authCfg.SignToken(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				request := httptest.NewRequest(http.MethodPost, "http://example.com/chat/input-requests/"+question.ID+"/answer?project_id="+project.ID, strings.NewReader(`{"project_id":"`+project.ID+`","answers":[{"question_id":"install","label":"`+tc.label+`"}]}`))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", "http://example.com")
				request.Header.Set("X-OpenVibely-UI", "chat-input-answer")
				request.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
				response := httptest.NewRecorder()
				c := e.NewContext(request, response)
				c.SetParamNames("id")
				c.SetParamValues(question.ID)
				if err = h.ChatInputRequestAnswer(c); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK {
					t.Fatalf("decision status %d", response.Code)
				}
			}
			if calls != 1 {
				t.Fatalf("continuation executions=%d", calls)
			}
			taskInputs, err := inputs.ListPendingForTask(ctx, task.ID)
			if err != nil || len(taskInputs) != 0 {
				t.Fatalf("unexpected task queue input: %+v %v", taskInputs, err)
			}
			chatInputs, err := inputs.ListPendingForChat(ctx, project.ID)
			if err != nil || len(chatInputs) != 0 {
				t.Fatalf("unexpected Chat queue input: %+v %v", chatInputs, err)
			}
			if tc.cancelled {
				original, err := h.execRepo.GetByID(ctx, execution.ID)
				if err != nil || original.Status != models.ExecCancelled {
					t.Fatalf("original run changed: %+v %v", original, err)
				}
			}
		})
	}
}

// The installation loader uses the same broker, records, renderer and answer
// endpoint as request_user_input, without exposing installation authority there.
func TestInstallationSharedQuestionsLiveAndCancelled(t *testing.T) {
	for _, tc := range []struct {
		category    models.TaskCategory
		authEnabled bool
	}{
		{models.CategoryChat, true}, {models.CategoryActive, true}, {models.CategoryScheduled, true},
		{models.CategoryChat, false}, {models.CategoryActive, false}, {models.CategoryScheduled, false},
	} {
		category, authEnabled := tc.category, tc.authEnabled
		for _, decision := range []string{"Install dependencies", "Not now", "cancel", "revoke skill access"} {
			t.Run(fmt.Sprintf("auth=%t/%s/%s", authEnabled, category, decision), func(t *testing.T) {
				h, e, configs, _ := setupTestHandlerWithStores(t)
				model := createAgent(t, configs)
				project := createProject(t, h, "shared question")
				task := createTask(t, h, project.ID, "question", func(task *models.Task) {
					task.Category = category
					task.Status = models.StatusRunning
					task.AgentID = &model.ID
				})
				execution := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning })
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = contracts.WithToolCallID(ctx, "original-call")
				done := make(chan error, 1)
				var downloads atomic.Int32
				cache := filepath.Join(t.TempDir(), "cache")
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					downloads.Add(1)
					w.Write([]byte("#!/bin/sh\nexit 0\n"))
				}))
				defer server.Close()
				go func() {
					prompt := func(c context.Context, question skilldeps.InstallationRequest) error {
						return h.requestSkillInstallation(c, contracts.AgentRequest{ExecID: execution.ID, ProjectID: project.ID}, question)
					}
					if decision == "revoke skill access" {
						platform := runtime.GOOS + "/" + runtime.GOARCH
						manager := skilldeps.Manager{CacheDir: cache, Platform: platform, Client: server.Client()}
						dependency := skilldeps.Dependency{Name: "fixture", Version: "1.0.0", HealthCheck: []string{"--version"}, Packages: map[string]skilldeps.Package{platform: {URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/sh\nexit 0\n"))), Format: "raw", Executable: "fixture"}}}
						_, err := manager.ResolveWithInstallPrompt(ctx, skilldeps.SkillIdentity{Skill: "fixture"}, []skilldeps.Dependency{dependency}, prompt)
						done <- err
						return
					}
					done <- prompt(ctx, skilldeps.InstallationRequest{Identity: skilldeps.SkillIdentity{Skill: "fixture"}})
				}()
				var record persistence.ChatInputRequestRecord
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for record.ID == "" {
					select {
					case <-ctx.Done():
						t.Fatal("question missing")
					case <-ticker.C:
						records, err := h.chatInputRequestRepo.ListByExecutions(ctx, project.ID, []string{execution.ID})
						if err != nil {
							t.Fatal(err)
						}
						if len(records) > 0 {
							record = records[0]
						}
					}
				}
				_, metadata, err := record.DecodeQuestions()
				if err != nil || metadata == nil {
					t.Fatalf("missing server metadata: %v", err)
				}
				if metadata.ToolCallID != "original-call" {
					t.Fatalf("missing saved call ID: %+v", metadata)
				}
				// The ordinary/model broker path cannot resolve an installation question.
				_, err = h.chatInputRequests.resolve(record.ID, project.ID, []chatInputRequestAnswer{{QuestionID: "install", Label: "Install dependencies"}})
				if err == nil {
					t.Fatal("ordinary question path authorized installation")
				}
				h.SetAuthConfig(auth.Config{Enabled: authEnabled, Username: "admin", Password: "secret", SessionSecret: "test-secret", SessionTTL: time.Hour})
				token, err := h.authCfg.SignToken(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				label := decision
				if decision == "revoke skill access" {
					label = "Install dependencies"
					project.SkillMemoryPermissions = &models.SkillMemoryPermissions{}
					if err := h.projectRepo.Update(ctx, project); err != nil {
						t.Fatal(err)
					}
				}
				if decision == "cancel" {
					label = "Install dependencies"
					if err = h.execRepo.Complete(ctx, execution.ID, models.ExecCancelled, "", "cancelled", 0, 0); err != nil {
						t.Fatal(err)
					}
				}
				body, _ := json.Marshal(chatInputRequestAnswerPayload{ProjectID: project.ID, Answers: []chatInputRequestAnswer{{QuestionID: "install", Label: label}}})
				// Forging UI headers without the session still fails on the real endpoint.
				send := func(signed bool) error {
					req := httptest.NewRequest(http.MethodPost, "http://example.com/chat/input-requests/"+record.ID+"/answer", strings.NewReader(string(body)))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Origin", "http://example.com")
					req.Header.Set("X-OpenVibely-UI", "chat-input-answer")
					if signed {
						req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
					}
					c := e.NewContext(req, httptest.NewRecorder())
					c.SetParamNames("id")
					c.SetParamValues(record.ID)
					return h.ChatInputRequestAnswer(c)
				}
				if authEnabled {
					if err = send(false); err == nil {
						t.Fatal("unauthenticated answer accepted")
					}
				}
				if err = send(authEnabled); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if decision == "revoke skill access" {
						const want = "skill_view could not read this skill because access is no longer available. Continue without this skill."
						if err == nil || err.Error() != want || downloads.Load() != 0 {
							t.Fatalf("revoked access: error=%v downloads=%d", err, downloads.Load())
						}
						if _, statErr := os.Stat(cache); !os.IsNotExist(statErr) {
							t.Fatalf("dependency setup occurred after revocation: %v", statErr)
						}
					}
					if (decision == "Install dependencies") != (err == nil) {
						t.Fatalf("decision=%s result=%v", decision, err)
					}
				case <-ctx.Done():
					t.Fatal("wait did not finish")
				}
				if err = send(authEnabled); err != nil {
					t.Fatal(err)
				}
				saved, err := h.chatInputRequestRepo.Get(context.Background(), record.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, meta, err := saved.DecodeQuestions()
				if err != nil || (meta.ContinuationExecutionID != "") != (decision == "cancel") {
					t.Fatalf("wrong delivery path for %s: %+v %v", decision, meta, err)
				}
			})
		}
	}
}

func TestInstallationSharedAnswerClaimRace(t *testing.T) {
	h, _, configs, _ := setupTestHandlerWithStores(t)
	model := createAgent(t, configs)
	project := createProject(t, h, "race")
	task := createTask(t, h, project.ID, "race", func(task *models.Task) { task.Status = models.StatusFailed; task.AgentID = &model.ID })
	execution := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed })
	for i := 0; i < 12; i++ {
		q, err := h.chatInputRequests.create(project.ID, execution.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`)})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err = h.persistChatInputRequest(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, err = h.chatInputRequestRepo.Resolve(ctx, q.ID, project.ID, `[{"question_id":"install","label":"Install dependencies"}]`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan *persistence.ChatInputRequestRecord, 2)
		errors := make(chan error, 2)
		for _, queue := range []bool{false, true} {
			go func(queue bool) {
				<-start
				record, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, q.ID, project.ID, queue)
				results <- record
				errors <- err
			}(queue)
		}
		close(start)
		claims := 0
		for j := 0; j < 2; j++ {
			if record := <-results; record != nil {
				claims++
			}
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
		}
		if claims != 1 {
			t.Fatalf("answer delivered %d times", claims)
		}
		record, err := h.chatInputRequestRepo.Get(ctx, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, meta, err := record.DecodeQuestions()
		if err != nil {
			t.Fatal(err)
		}
		if meta.Handled == (meta.ContinuationExecutionID != "") {
			t.Fatal("answer must be delivered live OR queued")
		}
	}
}

func TestInstallationLateResultMatchesLiveToolResult(t *testing.T) {
	for _, label := range []string{"Install dependencies", "Not now"} {
		for _, change := range []string{"unchanged", "installed subset", "reordered", "platform", "identity", "new dependency", "version", "URL", "hash", "executable", "format", "health check", "environment"} {
			changed := change != "unchanged" && change != "installed subset" && change != "reordered"
			t.Run(label+"/"+change, func(t *testing.T) {
				h, _, configs, _ := setupTestHandlerWithStores(t)
				ctx := context.Background()
				model := createAgent(t, configs)
				project := createProject(t, h, "result")
				task := createTask(t, h, project.ID, "result", func(task *models.Task) { task.Status = models.StatusFailed; task.AgentID = &model.ID })
				original := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed })
				var next *models.Execution
				manifest := skilldeps.InstallationRequest{Identity: skilldeps.SkillIdentity{ProjectID: project.ID, Skill: "fixture"}, Platform: "linux/amd64", Dependencies: []skilldeps.Dependency{
					{Name: "already-installed", Version: "1.0.0"},
					{Name: "fixture", Version: "1.0.0", EnvironmentVariable: "FIXTURE_PATH", HealthCheck: []string{"--version"}, Packages: map[string]skilldeps.Package{"linux/amd64": {URL: "https://example.com/fixture", SHA256: strings.Repeat("a", 64), Format: "raw", Executable: "fixture"}}},
				}}
				raw, _ := json.Marshal(manifest)
				args := json.RawMessage(`{"handle":"fixture","file_path":"scripts/help.md"}`)
				q, err := h.chatInputRequests.create(project.ID, original.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: raw, ToolArguments: args})
				if err != nil {
					t.Fatal(err)
				}
				if err = h.persistChatInputRequest(ctx, q); err != nil {
					t.Fatal(err)
				}
				answer, _ := json.Marshal([]chatInputRequestAnswer{{QuestionID: "install", Label: label}})
				if _, err = h.chatInputRequestRepo.Resolve(ctx, q.ID, project.ID, string(answer)); err != nil {
					t.Fatal(err)
				}
				claimed, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, q.ID, project.ID, true)
				if err != nil || claimed == nil {
					t.Fatalf("claim: %v %v", claimed, err)
				}
				_, meta, err := claimed.DecodeQuestions()
				if err != nil {
					t.Fatal(err)
				}
				next, err = h.execRepo.GetByID(ctx, meta.ContinuationExecutionID)
				if err != nil {
					t.Fatal(err)
				}
				if err = h.execRepo.MarkRunning(ctx, next.ID); err != nil {
					t.Fatal(err)
				}

				executions := 0
				tools := &contracts.RuntimeTools{Definitions: []contracts.RuntimeToolDefinition{{Name: "skill_view"}}, Executor: func(toolCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
					executions++
					if name != "skill_view" || string(input) != string(args) {
						t.Fatalf("wrong saved call: %s %s", name, input)
					}
					_, ask := skilldeps.ApprovalFromContext(toolCtx)
					var current skilldeps.InstallationRequest
					if err := json.Unmarshal(raw, &current); err != nil {
						t.Fatal(err)
					}
					// Mutate the remaining dependency, including in subset cases, to
					// verify that a smaller list never relaxes package validation.
					dependency := &current.Dependencies[1]
					pkg := dependency.Packages[current.Platform]
					switch change {
					case "platform":
						current.Platform = "changed"
					case "identity":
						current.Identity.Skill = "other"
					case "new dependency":
						dependency.Name = "other"
					case "version":
						dependency.Version = "2.0.0"
					case "URL":
						pkg.URL = "https://other.example/fixture"
					case "hash":
						pkg.SHA256 = strings.Repeat("b", 64)
					case "executable":
						pkg.Executable = "other"
					case "format":
						pkg.Format = "zip"
					case "health check":
						dependency.HealthCheck = []string{"--other"}
					case "environment":
						dependency.EnvironmentVariable = "OTHER_PATH"
					}
					dependency.Packages[manifest.Platform] = pkg
					if change == "reordered" {
						current.Dependencies[0], current.Dependencies[1] = current.Dependencies[1], current.Dependencies[0]
					} else if change != "unchanged" {
						current.Dependencies = current.Dependencies[1:]
					}
					if err := ask(toolCtx, current); err != nil {
						return err.Error(), true, true, nil
					}
					return "original support-file contents", true, false, nil
				}}
				ctx = contracts.WithRuntimeTools(ctx, tools)
				store := h.chatInputRequestRepo
				h.chatInputRequestRepo = failingInstallationResultStore{store}
				failedResult, saveErr := h.completeSkillInstallationContinuation(ctx, contracts.AgentRequest{Ctx: ctx, ProjectID: project.ID, ExecID: next.ID})
				if saveErr == nil || failedResult != "" {
					t.Fatalf("failed save exposed result to model: %q %v", failedResult, saveErr)
				}
				pendingResult, loadErr := store.Get(ctx, q.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				_, pendingMeta, loadErr := pendingResult.DecodeQuestions()
				if loadErr != nil || pendingMeta.Handled {
					t.Fatalf("failed result marked complete: %+v %v", pendingMeta, loadErr)
				}
				h.chatInputRequestRepo = store
				result, err := h.completeSkillInstallationContinuation(ctx, contracts.AgentRequest{Ctx: ctx, ProjectID: project.ID, ExecID: next.ID})
				if err != nil {
					t.Fatal(err)
				}
				var output struct {
					Output    string          `json:"output"`
					IsError   bool            `json:"is_error"`
					Arguments json.RawMessage `json:"arguments"`
				}
				_, body, ok := strings.Cut(result, "\n")
				if !ok || json.Unmarshal([]byte(body), &output) != nil {
					t.Fatalf("invalid tool result %s", result)
				}
				if label == "Not now" {
					if executions != 0 || output.Output != installationAnswerResult(string(answer)).Error() || !output.IsError {
						t.Fatalf("denial differs from live: %+v", output)
					}
				} else if changed {
					if !output.IsError || !strings.Contains(output.Output, "changed") {
						t.Fatalf("changed dependencies executed: %+v", output)
					}
				} else if output.Output != "original support-file contents" || output.IsError || string(output.Arguments) != string(args) {
					t.Fatalf("wrong result %+v", output)
				}
				saved, saveErr := h.execRepo.GetByID(ctx, next.ID)
				if saveErr != nil || saved.PromptSent != next.PromptSent {
					t.Fatalf("follow-up history lost result: %+v %v", saved, saveErr)
				}
				again, err := h.completeSkillInstallationContinuation(ctx, contracts.AgentRequest{ProjectID: project.ID, ExecID: next.ID})
				if err != nil || again != result {
					t.Fatalf("duplicate result: %s %v", again, err)
				}
			})
		}
	}
}

func TestInstallationOldInterruptedQuestionDoesNotFailNewTaskOnRestart(t *testing.T) {
	for _, status := range []models.TaskStatus{models.StatusQueued, models.StatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			h, _, configs, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "normal recovery")
			task := createTask(t, h, project.ID, "new run", func(task *models.Task) { task.Status = status; task.AgentID = &model.ID })
			old := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed })
			if err := h.execRepo.Complete(ctx, old.ID, models.ExecFailed, "", "Installation question interrupted by restart; answer in the thread to continue.", 0, 0); err != nil {
				t.Fatal(err)
			}
			currentStatus := models.ExecQueued
			if status == models.StatusRunning {
				currentStatus = models.ExecRunning
			}
			current := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = currentStatus })
			if _, err := h.taskRepo.ResetOrphanedRunning(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := h.execRepo.RecoverStaleRunningTaskExecutions(ctx); err != nil {
				t.Fatal(err)
			}
			recovered, err := h.taskRepo.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := models.StatusQueued
			if status == models.StatusRunning {
				want = models.StatusPending
			}
			if recovered.Status != want {
				t.Fatalf("old installation failure affected new run: status=%s want=%s", recovered.Status, want)
			}
			ex, err := h.execRepo.GetByID(ctx, current.ID)
			if err != nil {
				t.Fatal(err)
			}
			if status == models.StatusQueued && ex.Status != models.ExecQueued {
				t.Fatalf("queued execution changed: %s", ex.Status)
			}
		})
	}
}

func TestInstallationExpiredQuestionDoesNotRecoverLiveChat(t *testing.T) {
	h, e, configs, _ := setupTestHandlerWithStores(t)
	ctx := context.Background()
	model := createAgent(t, configs)
	project := createProject(t, h, "live chat")
	task := createTask(t, h, project.ID, "live", func(task *models.Task) {
		task.Category = models.CategoryChat
		task.Status = models.StatusRunning
		task.AgentID = &model.ID
	})
	execution := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning })
	question, err := h.chatInputRequests.create(project.ID, execution.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.persistChatInputRequest(ctx, question); err != nil {
		t.Fatal(err)
	}
	// The tool wait ended, but its model loop is still alive in this process.
	h.chatInputRequests.expire(question.ID)
	h.SetAuthConfig(auth.Config{Enabled: true, Username: "admin", Password: "secret", SessionSecret: "live-chat-test", SessionTTL: time.Hour})
	token, err := h.authCfg.SignToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(chatInputRequestAnswerPayload{ProjectID: project.ID, Answers: []chatInputRequestAnswer{{QuestionID: "install", Label: "Not now"}}})
	req := httptest.NewRequest(http.MethodPost, "http://example.com/chat/input-requests/"+question.ID+"/answer", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("X-OpenVibely-UI", "chat-input-answer")
	req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(question.ID)
	if err = h.ChatInputRequestAnswer(c); err != nil {
		t.Fatal(err)
	}
	current, err := h.execRepo.GetByID(ctx, execution.ID)
	if err != nil || current.Status != models.ExecRunning {
		t.Fatalf("live chat was incorrectly recovered: %+v %v", current, err)
	}
	saved, err := h.chatInputRequestRepo.Get(ctx, question.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, meta, err := saved.DecodeQuestions()
	if err != nil || meta.ContinuationExecutionID != "" || saved.Status != persistence.ChatInputRequestResolved {
		t.Fatalf("answer must remain durable until live execution exits: %+v %v", meta, err)
	}
	// Completion delivers the saved answer directly, without queue service access.
	h.threadInputRepo = nil
	starts := 0
	h.startStreamingResponseOverride = func(params streamingResponseParams) error {
		if params.updateWorkDone != nil {
			defer params.updateWorkDone()
		}
		starts++
		if params.ExecID == execution.ID {
			t.Error("must not reactivate terminated execution")
		}
		return nil
	}
	if err = h.execRepo.Complete(ctx, execution.ID, models.ExecFailed, "", "wait ended", 0, 0); err != nil {
		t.Fatal(err)
	}
	h.startNextQueuedTurnAfter(ctx, streamingResponseParams{ProjectID: project.ID, TaskID: task.ID}, "")
	h.startNextQueuedTurnAfter(ctx, streamingResponseParams{ProjectID: project.ID, TaskID: task.ID}, "")
	if starts != 1 {
		t.Fatalf("direct continuations=%d", starts)
	}
}

func TestInstallationCommittedAnswerRecoveredWithoutHTTPDelivery(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive, models.CategoryScheduled} {
		for _, label := range []string{"Install dependencies", "Not now"} {
			t.Run(string(category)+"/"+label, func(t *testing.T) {
				h, _, configs, _ := setupTestHandlerWithStores(t)
				ctx := context.Background()
				model := createAgent(t, configs)
				project := createProject(t, h, "answer crash")
				task := createTask(t, h, project.ID, "question", func(task *models.Task) {
					task.Category = category
					task.Status = models.StatusRunning
					task.AgentID = &model.ID
				})
				execution := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning })
				question, err := h.chatInputRequests.create(project.ID, execution.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`), ToolArguments: json.RawMessage(`{"handle":"fixture"}`)})
				if err != nil {
					t.Fatal(err)
				}
				if err = h.persistChatInputRequest(ctx, question); err != nil {
					t.Fatal(err)
				}
				answer, _ := json.Marshal([]chatInputRequestAnswer{{QuestionID: "install", Label: label}})
				if _, err = h.chatInputRequestRepo.Resolve(ctx, question.ID, project.ID, string(answer)); err != nil {
					t.Fatal(err)
				}
				// Crash immediately after commit, before broker notification or promotion.
				h.chatInputRequests = newChatInputRequestBroker()
				if _, err = h.taskRepo.ResetOrphanedRunning(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = h.execRepo.RecoverStaleRunningTaskExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				seedPendingInstallationTranscript(t, h, execution.ID, question.ID)
				calls := 0
				mock := testutil.NewMockLLMCaller()
				mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
					calls++
					if call.ExecID == execution.ID {
						t.Error("restarted old execution")
					}
					want := "not in this turn" // Fixture skill is not in the authorized catalog.
					if label == "Not now" {
						want = "human declined dependency installation"
					}
					assertInstallationToolContinuation(t, mock, want)
				}
				h.llmSvc.SetLLMCaller(mock)
				h.startStreamingResponseOverride = func(params streamingResponseParams) error { h.processStreamingResponse(params); return nil }
				h.RecoverQueuedInputs(ctx)
				h.RecoverQueuedInputs(ctx)
				if calls != 1 {
					t.Fatalf("recovered followups=%d, want 1", calls)
				}
			})
		}
	}
}

// Force a failed result write while retaining the real question/retry behavior.
type failingInstallationResultStore struct {
	persistence.ChatInputRequestStore
}

func (f failingInstallationResultStore) SaveInstallationContinuationResult(context.Context, string, string, string) error {
	return fmt.Errorf("injected result save failure")
}

func TestInstallationDecisionAuthModes(t *testing.T) {
	for _, mode := range []auth.AuthMode{auth.AuthModeDisabled, auth.AuthModeLocal, auth.AuthModeHostedSSO} {
		for _, origin := range []string{"http://example.com", "http://attacker.test", ""} {
			for _, header := range []string{"chat-input-answer", ""} {
				t.Run(fmt.Sprintf("%s/%s/%s", mode, origin, header), func(t *testing.T) {
					h, e, _, _ := setupTestHandlerWithStores(t)
					h.SetAuthMode(mode)
					req := httptest.NewRequest(http.MethodPost, "http://example.com/chat/input-requests/question/answer", nil)
					req.Header.Set("Origin", origin)
					req.Header.Set("X-OpenVibely-UI", header)
					err := h.requireDependencyHuman(e.NewContext(req, httptest.NewRecorder()))
					want := http.StatusUnauthorized
					if mode == auth.AuthModeDisabled {
						want = http.StatusForbidden
						if origin == "http://example.com" && header == "chat-input-answer" {
							want = 0
						}
					}
					if want == 0 {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					httpErr, ok := err.(*echo.HTTPError)
					if !ok || httpErr.Code != want {
						t.Fatalf("got %v, want %d", err, want)
					}
				})
			}
		}
	}
}

func seedPendingInstallationTranscript(t *testing.T, h *Handler, execution, question string) {
	t.Helper()
	record, err := h.chatInputRequestRepo.Get(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}
	_, meta, err := record.DecodeQuestions()
	if err != nil {
		t.Fatal(err)
	}
	transcript, _ := json.Marshal([]any{map[string]any{"role": "user", "content": "original request"}, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "original-call", "type": "function", "function": map[string]any{"name": "skill_view", "arguments": string(meta.ToolArguments)}}}}, map[string]any{"role": "tool", "tool_call_id": "original-call", "content": contracts.PendingSkillInputPrefix + question}})
	if err := h.execRepo.ReplaceReasoningReplay(context.Background(), execution, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(transcript)}}); err != nil {
		t.Fatal(err)
	}
}
func assertInstallationToolContinuation(t *testing.T, mock *testutil.MockLLMCaller, want string) {
	t.Helper()
	if len(mock.AgentRequests) == 0 {
		t.Fatal("no provider request")
	}
	req := mock.AgentRequests[len(mock.AgentRequests)-1]
	if req.Message != "" || !contracts.HistoryContinuationFromContext(req.Ctx) {
		t.Errorf("added user message instead of tool result: %q", req.Message)
	}
	var transcript string
	for _, ex := range req.ChatHistory {
		for _, replay := range ex.ReplayMessages {
			transcript += replay.TranscriptJSON
		}
	}
	if !strings.Contains(transcript, want) || !strings.Contains(transcript, "original-call") {
		t.Errorf("missing original tool result: %s", transcript)
	}
}

func TestInstallationContinuationFailureRetriesOriginalUserPrompt(t *testing.T) {
	for _, label := range []string{"Install dependencies", "Not now"} {
		t.Run(label, func(t *testing.T) {
			h, _, configs, _ := setupTestHandlerWithStores(t)
			h.workerSvc = nil
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "ordinary retry")
			task := createTask(t, h, project.ID, "original task", func(task *models.Task) {
				task.Category = models.CategoryActive
				task.Status = models.StatusFailed
				task.AgentID = &model.ID
				task.Prompt = "original task prompt"
			})
			const prompt = "Fix the PDF export formatting"
			original := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) {
				ex.Status = models.ExecFailed
				ex.IsFollowup = true
				ex.PromptSent = prompt
			})
			q, err := h.chatInputRequests.create(project.ID, original.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`), ToolArguments: json.RawMessage(`{"handle":"fixture"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err = h.persistChatInputRequest(ctx, q); err != nil {
				t.Fatal(err)
			}
			seedPendingInstallationTranscript(t, h, original.ID, q.ID)
			answer, _ := json.Marshal([]chatInputRequestAnswer{{QuestionID: "install", Label: label}})
			if _, err = h.chatInputRequestRepo.Resolve(ctx, q.ID, project.ID, string(answer)); err != nil {
				t.Fatal(err)
			}
			mock := testutil.NewMockLLMCaller()
			mock.Err = fmt.Errorf("provider transport failed")
			h.llmSvc.SetLLMCaller(mock)
			h.startStreamingResponseOverride = func(params streamingResponseParams) error { h.processStreamingResponse(params); return nil }
			h.startInstallationContinuations(ctx, project.ID, task.ID)
			if mock.CallCount() != 1 {
				t.Fatalf("continuation calls=%d", mock.CallCount())
			}
			first := mock.LastAgentRequest()
			if !contracts.HistoryContinuationFromContext(first.Ctx) || first.Message != "" {
				t.Fatal("late answer did not complete original tool call")
			}
			failed, err := h.execRepo.GetByID(ctx, first.ExecID)
			if err != nil || failed.Status != models.ExecFailed || failed.PromptSent != prompt {
				t.Fatalf("failed continuation lost user prompt: %+v %v", failed, err)
			}
			mock.Err = nil
			started, err := h.RetryLatestFailedTaskThreadFollowup(ctx, task.ID)
			if err != nil || !started || mock.CallCount() != 2 {
				t.Fatalf("ordinary retry: started=%v calls=%d error=%v", started, mock.CallCount(), err)
			}
			retried := mock.LastAgentRequest()
			if retried.Message != prompt || contracts.HistoryContinuationFromContext(retried.Ctx) || retried.RetrySourceExecutionID != first.ExecID {
				t.Fatalf("retry did not use original user prompt: %q continuation=%v source=%s", retried.Message, contracts.HistoryContinuationFromContext(retried.Ctx), retried.RetrySourceExecutionID)
			}
			bound, err := h.chatInputRequestRepo.LoadInstallationContinuation(ctx, project.ID, retried.ExecID, "")
			if err != nil || bound != nil {
				t.Fatalf("retry reclaimed installation answer: %+v %v", bound, err)
			}
		})
	}
}

func TestInstallationClaimedContinuationFailsNormallyOnRestart(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive, models.CategoryScheduled} {
		for _, state := range []string{"claimed", "running", "cancelled"} {
			t.Run(fmt.Sprintf("%s/%s", category, state), func(t *testing.T) {
				h, _, configs, _ := setupTestHandlerWithStores(t)
				ctx := context.Background()
				model := createAgent(t, configs)
				project := createProject(t, h, "interrupted continuation")
				task := createTask(t, h, project.ID, "interrupted", func(task *models.Task) {
					task.Category, task.Status, task.AgentID = category, models.StatusFailed, &model.ID
				})
				original := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed })
				question, err := h.chatInputRequests.create(project.ID, original.ID,
					[]chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}}}},
					&persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`), ToolArguments: json.RawMessage(`{"handle":"fixture"}`)})
				if err != nil {
					t.Fatal(err)
				}
				if err = h.persistChatInputRequest(ctx, question); err != nil {
					t.Fatal(err)
				}
				if _, err = h.chatInputRequestRepo.Resolve(ctx, question.ID, project.ID, `[{"question_id":"install","label":"Install dependencies"}]`); err != nil {
					t.Fatal(err)
				}
				claimed, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, question.ID, project.ID, true)
				if err != nil || claimed == nil {
					t.Fatalf("claim: %+v %v", claimed, err)
				}
				_, meta, err := claimed.DecodeQuestions()
				if err != nil {
					t.Fatal(err)
				}
				if state != "claimed" {
					if err = h.execRepo.MarkRunning(ctx, meta.ContinuationExecutionID); err != nil {
						t.Fatal(err)
					}
					if err = h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
						t.Fatal(err)
					}
				}
				wantStatus := models.StatusFailed
				wantExecutionStatus := models.ExecFailed
				if state == "cancelled" {
					wantStatus = models.StatusCancelled
					wantExecutionStatus = models.ExecCancelled
					current, e := h.taskRepo.GetByID(ctx, task.ID)
					if e != nil {
						t.Fatal(e)
					}
					current.Status = models.StatusCancelled
					if category != models.CategoryChat {
						current.Category = models.CategoryBacklog
					}
					if e = h.taskRepo.Update(ctx, current); e != nil {
						t.Fatal(e)
					}
				}
				// Simulate process loss after claiming, either before the runner starts
				// or while it is running. Use the actual startup recovery sequence.
				h.chatInputRequests = newChatInputRequestBroker()
				if _, err = h.taskRepo.ResetOrphanedRunning(ctx); err != nil {
					t.Fatal(err)
				}
				if count, err := h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil || count != 1 {
					t.Fatalf("recovery count=%d error=%v", count, err)
				}
				if _, err = h.execRepo.RecoverStaleRunningTaskExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				starts := 0
				h.startStreamingResponseOverride = func(params streamingResponseParams) error {
					starts++
					if params.updateWorkDone != nil {
						params.updateWorkDone()
					}
					return nil
				}
				h.RecoverQueuedInputs(ctx)
				if starts != 0 {
					t.Fatal("restart replayed the installation answer")
				}
				recovered, err := h.execRepo.GetByID(ctx, meta.ContinuationExecutionID)
				if err != nil || recovered == nil || recovered.Status != wantExecutionStatus || recovered.CompletedAt == nil {
					t.Fatalf("execution not terminal: %+v %v", recovered, err)
				}
				current, err := h.taskRepo.GetByID(ctx, task.ID)
				wantCategory := models.CategoryBacklog
				if category == models.CategoryChat {
					wantCategory = models.CategoryChat
				}
				if err != nil || current == nil || current.Status != wantStatus || current.Category != wantCategory {
					t.Fatalf("task not recovered: %+v %v", current, err)
				}
				if category == models.CategoryChat {
					active, err := h.execRepo.FindLatestActiveChatExecution(ctx, project.ID)
					if err != nil || active != nil {
						t.Fatalf("Chat still blocked: %+v %v", active, err)
					}
				}
				// A historical interrupted answer must not affect a later run.
				if category != models.CategoryChat {
					current.Category = models.CategoryActive
				}
				current.Status = models.StatusQueued
				if err = h.taskRepo.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
				later := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecQueued })
				if count, err := h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil || count != 0 {
					t.Fatalf("historical recovery count=%d error=%v", count, err)
				}
				later, err = h.execRepo.GetByID(ctx, later.ID)
				if err != nil || later.Status != models.ExecQueued {
					t.Fatalf("later execution changed: %+v %v", later, err)
				}
				current, err = h.taskRepo.GetByID(ctx, task.ID)
				if err != nil || current.Status != models.StatusQueued {
					t.Fatalf("later task changed: %+v %v", current, err)
				}
			})
		}
	}
}

func TestInstallationRestartRequeuesUndeliveredSteering(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive, models.CategoryScheduled} {
		t.Run(string(category), func(t *testing.T) {
			h, _, configs, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "steering recovery")
			task := createTask(t, h, project.ID, "interrupted continuation", func(task *models.Task) {
				task.Category, task.Status, task.AgentID = category, models.StatusFailed, &model.ID
			})
			original := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed })
			question, err := h.chatInputRequests.create(project.ID, original.ID,
				[]chatInputRequestQuestion{{ID: "install", Question: "Install?", Options: []chatInputRequestOption{{Label: "Install dependencies"}}}},
				&persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"fixture"}}`), ToolArguments: json.RawMessage(`{"handle":"fixture"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err = h.persistChatInputRequest(ctx, question); err != nil {
				t.Fatal(err)
			}
			if _, err = h.chatInputRequestRepo.Resolve(ctx, question.ID, project.ID, `[{"question_id":"install","label":"Install dependencies"}]`); err != nil {
				t.Fatal(err)
			}
			claimed, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, question.ID, project.ID, true)
			if err != nil || claimed == nil {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			_, meta, err := claimed.DecodeQuestions()
			if err != nil {
				t.Fatal(err)
			}
			if err = h.execRepo.MarkRunning(ctx, meta.ContinuationExecutionID); err != nil {
				t.Fatal(err)
			}
			if err = h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
				t.Fatal(err)
			}
			scope := models.ThreadInputScopeTask
			if category == models.CategoryChat {
				scope = models.ThreadInputScopeChat
			}
			steering := &models.ThreadInput{Scope: scope, ProjectID: project.ID, TaskID: task.ID,
				RunExecutionID: meta.ContinuationExecutionID, ExpectedTurnID: meta.ContinuationExecutionID,
				Content: "Also check the output", AgentConfigID: model.ID}
			if err = h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, meta.ContinuationExecutionID); err != nil {
				t.Fatal(err)
			}
			// Run startup cleanup twice: preserve the same queued message without
			// replaying the installation answer or changing its continuation link.
			for attempt := 0; attempt < 2; attempt++ {
				if _, err = h.taskRepo.ResetOrphanedRunning(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = h.execRepo.RecoverPreRestartRunningTaskExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = h.execRepo.RecoverStaleRunningTaskExecutions(ctx); err != nil {
					t.Fatal(err)
				}
				saved, err := h.threadInputRepo.GetByID(ctx, steering.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved == nil || saved.InputMode != models.ThreadInputModeQueued || saved.InputStatus != models.ThreadInputPending || saved.TurnID != "" || saved.ExpectedTurnID != "" || saved.Content != steering.Content || saved.RunExecutionID != meta.ContinuationExecutionID {
					t.Fatalf("steering not recovered: %+v", saved)
				}
			}
			var ids []string
			want := task.ID
			if category == models.CategoryChat {
				ids, err = h.threadInputRepo.ListRecoverableQueuedChatProjectIDsAfter(ctx, "", 100)
				want = project.ID
			} else {
				ids, err = h.threadInputRepo.ListRecoverableQueuedTaskIDsAfter(ctx, "", 100)
			}
			if err != nil || len(ids) != 1 || ids[0] != want {
				t.Fatalf("ordinary queue recovery cannot find steering: %v %v", ids, err)
			}
		})
	}
}

func TestInstallationDecisionDesktopOrigins(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithStores(t)
	h.SetAuthConfig(auth.Config{Enabled: true, Username: "admin", Password: "secret", SessionSecret: "desktop-install-test", SessionTTL: time.Hour})
	token, err := h.authCfg.SignToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, host, origin           string
		desktop, native, ui, session bool
		want                         int
	}{
		{"macOS Wails", "localhost", "wails://localhost", true, true, true, true, 0},
		{"opaque WebKit origin", "localhost", "null", true, true, true, true, 0},
		{"omitted WebKit origin", "localhost", "", true, true, true, true, 0},
		{"Windows Wails", "wails.localhost", "http://wails.localhost", true, true, true, true, 0},
		{"external origin", "localhost", "https://attacker.test", true, true, true, true, 403},
		{"wrong host", "attacker.test", "wails://localhost", true, true, true, true, 403},
		{"not native proxy", "localhost", "wails://localhost", true, false, true, true, 403},
		{"server mode", "localhost", "wails://localhost", false, true, true, true, 403},
		{"missing UI header", "localhost", "wails://localhost", true, true, false, true, 403},
		{"missing session", "localhost", "wails://localhost", true, true, true, false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.SetDesktopMode(tc.desktop)
			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/chat/input-requests/question/answer", strings.NewReader(`{}`))
			req.Header.Set("Origin", tc.origin)
			if tc.native {
				req.Header.Set("X-OpenVibely-Native-Window", "1")
			}
			if tc.ui {
				req.Header.Set("X-OpenVibely-UI", "chat-input-answer")
			}
			if tc.session {
				req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
			}
			err := h.requireDependencyHuman(e.NewContext(req, httptest.NewRecorder()))
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			he, ok := err.(*echo.HTTPError)
			if !ok || he.Code != tc.want {
				t.Fatalf("got %v want %d", err, tc.want)
			}
		})
	}
	// Desktop defaults to auth disabled; native decisions must also work there.
	h.SetAuthConfig(auth.Config{Enabled: false})
	h.SetDesktopMode(true)
	req := httptest.NewRequest(http.MethodPost, "http://localhost/chat/input-requests/question/answer", strings.NewReader(`{}`))
	req.Header.Set("Origin", "null")
	req.Header.Set("X-OpenVibely-Native-Window", "1")
	req.Header.Set("X-OpenVibely-UI", "chat-input-answer")
	if err := h.requireDependencyHuman(e.NewContext(req, httptest.NewRecorder())); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationToastOpensThreadFragment(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryChat} {
		t.Run(string(category), func(t *testing.T) {
			h, e, configs, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "toast destination")
			task := createTask(t, h, project.ID, "approval toast task", func(task *models.Task) {
				task.Category = category
				task.Status = models.StatusFailed
				task.AgentID = &model.ID
			})
			execution := createExec(t, h, task.ID, model.ID, func(ex *models.Execution) { ex.Status = models.ExecFailed; ex.PromptSent = "original question" })
			question, err := h.chatInputRequests.create(project.ID, execution.ID, []chatInputRequestQuestion{{ID: "install", Question: "Install Example?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{"identity":{"skill":"example"}}`), ToolArguments: json.RawMessage(`{"handle":"example"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err = h.persistChatInputRequest(ctx, question); err != nil {
				t.Fatal(err)
			}
			for _, htmx := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodGet, "http://localhost/chat/input-requests/"+question.ID+"/open?project_id="+project.ID, nil)
				if htmx {
					req.Header.Set("HX-Request", "true")
					req.Header.Set("HX-Target", "main-content")
				}
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetParamNames("id")
				c.SetParamValues(question.ID)
				if err = h.OpenChatInputRequest(c); err != nil {
					t.Fatal(err)
				}
				destination := rec.Header().Get("Location")
				if htmx {
					if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
						t.Fatalf("fragment redirects: %d %v", rec.Code, rec.Header())
					}
					destination = rec.Header().Get("HX-Push-Url")
					marker := `id="task-detail-content"`
					if category == models.CategoryChat {
						marker = `id="chat-page-root"`
					}
					if !strings.Contains(rec.Body.String(), marker) {
						t.Fatalf("missing page content %s: %s", marker, rec.Body.String())
					}
				} else if rec.Code != http.StatusSeeOther {
					t.Fatalf("normal navigation status %d", rec.Code)
				}
				prefix := "/tasks/" + task.ID + "?"
				if category == models.CategoryChat {
					prefix = "/chat?"
				}
				if !strings.HasPrefix(destination, prefix) || !strings.Contains(destination, "approval_execution_id="+execution.ID) || !strings.HasSuffix(destination, "#chat-input-request-"+question.ID) {
					t.Fatalf("wrong destination: %s", destination)
				}
			}
		})
	}
}

func TestSkillInstallationWaitYieldsForUpdate(t *testing.T) {
	for _, alreadyDraining := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyDraining), func(t *testing.T) {
			h, _, configs, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			model := createAgent(t, configs)
			project := createProject(t, h, "update installation wait")
			task := createTask(t, h, project.ID, "install")
			execution := createExec(t, h, task.ID, model.ID, func(e *models.Execution) { e.Status = models.ExecRunning })
			h.drainManager = update.NewDrainManager(nil, nil, 0, time.Now)
			begin := func() {
				t.Helper()
				if _, err := h.drainManager.BeginDrain(update.DrainRequest{Lease: time.Minute}); err != nil {
					t.Fatal(err)
				}
			}
			if alreadyDraining {
				begin()
			}
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- h.requestSkillInstallation(waitCtx, contracts.AgentRequest{ExecID: execution.ID, ProjectID: project.ID}, skilldeps.InstallationRequest{Identity: skilldeps.SkillIdentity{Skill: "fixture"}})
			}()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			var question persistence.ChatInputRequestRecord
			for question.ID == "" {
				select {
				case <-deadline.C:
					t.Fatal("question not persisted")
				case <-ticker.C:
					records, err := h.chatInputRequestRepo.ListByExecutions(ctx, project.ID, []string{execution.ID})
					if err != nil {
						t.Fatal(err)
					}
					if len(records) > 0 {
						question = records[0]
					}
				}
			}
			if !alreadyDraining {
				begin()
			}
			select {
			case err := <-done:
				if err == nil || !strings.HasPrefix(err.Error(), contracts.PendingSkillInputPrefix) {
					t.Fatalf("expected resumable pending input, got %v", err)
				}
			case <-deadline.C:
				t.Fatal("update blocked on human input")
			}
			saved, err := h.chatInputRequestRepo.Get(ctx, question.ID)
			if err != nil || saved == nil || saved.Status != persistence.ChatInputRequestPending {
				t.Fatalf("lost pending question: %+v %v", saved, err)
			}
			if len(h.chatInputRequests.listProject(project.ID)) != 1 {
				t.Fatal("question no longer available to answer")
			}
		})
	}
}
