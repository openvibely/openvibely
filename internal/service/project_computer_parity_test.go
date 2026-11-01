package service

import (
	"context"
	"github.com/openvibely/openvibely/internal/testutil/teststores"

	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/stretchr/testify/require"
)

func computerParityFixture(t *testing.T, handler http.HandlerFunc) (*teststores.Env, *ProjectComputerService, *models.Project, *models.Task) {
	t.Helper()
	db := teststores.New(t)
	projects := db.Projects
	p := &models.Project{Name: "Parity", RepoPath: t.TempDir()}
	require.NoError(t, projects.Create(t.Context(), p))
	task := &models.Task{ProjectID: p.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	svc := NewProjectComputerService(projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "fixture"}})
	require.NoError(t, projects.Computers().Configure(t.Context(), p.ID, "local"))
	svc.SetAttachmentDirectory(t.TempDir())
	scoped, err := svc.ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, err)
	return db, scoped, p, task
}
func TestComputerHandoffVerificationAndSingleContinuation(t *testing.T) {
	accessible := false
	checks := 0
	db, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
		if args["action"] == "verify_access" {
			checks++
			require.Equal(t, "Account dashboard", args["expected_text"])
			if !accessible {
				http.Error(w, "not signed in", 502)
				return
			}
		}
		io.WriteString(w, `{"verified":true}`)
	})
	require.NoError(t, s.repo.RequestHandoff(t.Context(), p.ID, "request_browser_login", "https://example.test/account", "Read dashboard", "Account dashboard"))
	state, err := s.Get(t.Context(), p.ID)
	require.NoError(t, err)
	require.NotEmpty(t, state.HandoffAlertID)
	alert, err := db.Alerts.GetByIDForProject(t.Context(), p.ID, state.HandoffAlertID)
	require.NoError(t, err)
	require.Equal(t, models.AlertDecisionPending, alert.DecisionState)
	require.Error(t, s.CompleteLogin(t.Context(), p.ID))
	input, err := db.ThreadInputs.FindOldestQueuedForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Nil(t, input)
	accessible = true
	require.NoError(t, s.CompleteLogin(t.Context(), p.ID))
	require.Equal(t, 2, checks)
	require.Error(t, s.CompleteLogin(t.Context(), p.ID))
	input, err = db.ThreadInputs.FindOldestQueuedForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Contains(t, input.Content, "Continue the original task")
	count, err := db.Fixtures.CountTaskThreadInputs(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	alert, err = db.Alerts.GetByIDForProject(t.Context(), p.ID, state.HandoffAlertID)
	require.NoError(t, err)
	require.Equal(t, models.AlertDecisionApproved, alert.DecisionState)
}
func TestComputerHelpDeclineQueuesHonestContinuation(t *testing.T) {
	db, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	tools := s.runtimeTools(t.Context(), p.ID, task.ID)
	_, handled, bad, err := tools.Executor(t.Context(), "request_computer_help", json.RawMessage(`{"reason":"Please solve the CAPTCHA"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.NoError(t, s.CancelHandoff(t.Context(), p.ID))
	input, err := db.ThreadInputs.FindOldestQueuedForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Contains(t, input.Content, "declined")
	require.NotContains(t, input.Content, "completed")
}
func TestComputerToolRoutingAndAttachments(t *testing.T) {
	var calls []string
	db, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/files") {
			if r.Method == "PUT" {
				body, _ := io.ReadAll(r.Body)
				expected := "uploaded"
				if strings.HasSuffix(r.URL.Query().Get("path"), "roundtrip.txt") {
					expected = "downloaded"
				}
				require.Equal(t, expected, string(body))
				io.WriteString(w, `{}`)
			} else {
				w.Header().Set("Content-Type", "application/octet-stream")
				io.WriteString(w, "downloaded")
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/shell") {
			var args map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&args))
			require.Equal(t, "git status", args["command"])
			io.WriteString(w, `{"output":"clean","exit_code":0}`)
			return
		}
		io.WriteString(w, "file from desktop")
	})
	plain := s.runtimeTools(t.Context(), p.ID, task.ID)
	_, handled, _, err := plain.Executor(t.Context(), "bash", json.RawMessage(`{"command":"git status"}`))
	require.NoError(t, err)
	require.False(t, handled)
	require.NoError(t, s.SetToolTarget(t.Context(), p.ID, "computer"))
	tools := s.runtimeTools(t.Context(), p.ID, task.ID)
	require.True(t, tools.SkipDefaultTools)
	out, handled, bad, err := tools.Executor(t.Context(), "bash", json.RawMessage(`{"command":"git status"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.Contains(t, out, "clean")
	out, _, _, err = tools.Executor(t.Context(), "read_file", json.RawMessage(`{"file_path":"fixture.txt"}`))
	require.NoError(t, err)
	require.Equal(t, "file from desktop", out)
	require.True(t, strings.HasSuffix(calls[len(calls)-1], "/native-files"))
	path := filepath.Join(t.TempDir(), "fixture.txt")
	require.NoError(t, os.WriteFile(path, []byte("uploaded"), 0600))
	att := &models.Attachment{TaskID: task.ID, FilePath: path, FileName: "fixture.txt", MediaType: "text/plain", FileSize: 8}
	require.NoError(t, db.Attachments.Create(t.Context(), att))
	data, _ := json.Marshal(map[string]string{"action": "to_computer", "attachment_id": att.ID, "computer_path": "fixture.txt"})
	_, err = s.attachmentTransfer(t.Context(), p.ID, data)
	require.NoError(t, err)
	out, err = s.attachmentTransfer(t.Context(), p.ID, json.RawMessage(`{"action":"from_computer","computer_path":"result.txt"}`))
	require.NoError(t, err)
	require.Contains(t, out, "attachment_id")
	attachments, err := db.Attachments.ListByTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, attachments, 2)
	config := &models.LLMConfig{Name: "fixture", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecCompleted, PromptSent: "with file"}
	require.NoError(t, db.Executions.Create(t.Context(), execution))
	threadFile := &models.ChatAttachment{ExecutionID: execution.ID, FileName: "thread.txt", FilePath: path, MediaType: "text/plain", FileSize: 8}
	require.NoError(t, db.ChatAttachments.Create(t.Context(), threadFile))
	threadInput, _ := json.Marshal(map[string]string{"action": "to_computer", "attachment_kind": "thread", "attachment_id": threadFile.ID, "computer_path": "thread.txt"})
	_, err = s.attachmentTransfer(t.Context(), p.ID, threadInput)
	require.NoError(t, err)
	running := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "export"}
	require.NoError(t, db.Executions.Create(t.Context(), running))
	_, _, _, err = tools.Executor(t.Context(), "release_computer", json.RawMessage(`{}`))
	require.NoError(t, err)
	source := NewTaskAttachmentContextSource(db.Attachments, db.Tasks, *task)
	require.NoError(t, source.Load(t.Context()))
	require.NotContains(t, source.References(t.Context()), "reply.txt")
	rt := s.runtimeTools(llmcontracts.WithArtifactExecutionID(t.Context(), running.ID), p.ID, task.ID)
	exported, handled, bad, err := rt.Executor(t.Context(), "computer_attachment", json.RawMessage(`{"action":"from_computer","computer_path":"reply.txt"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.Contains(t, exported, "/attachments/")
	require.NotContains(t, exported, "/chat/attachments/")
	threadAttachments, err := db.ChatAttachments.ListByExecution(t.Context(), running.ID)
	require.NoError(t, err)
	require.Empty(t, threadAttachments)
	taskFiles, err := db.Attachments.ListByExecutionIDs(t.Context(), []string{running.ID})
	require.NoError(t, err)
	require.Len(t, taskFiles[running.ID], 1)
	require.Equal(t, task.ID, taskFiles[running.ID][0].TaskID)
	require.Equal(t, "reply.txt", taskFiles[running.ID][0].FileName)
	require.Contains(t, source.References(t.Context()), "reply.txt")
	imported, _ := json.Marshal(map[string]string{"action": "to_computer", "attachment_kind": "task", "attachment_id": taskFiles[running.ID][0].ID, "computer_path": "roundtrip.txt"})
	_, err = s.attachmentTransfer(t.Context(), p.ID, imported)
	require.NoError(t, err)
	listing, err := s.attachmentTransfer(t.Context(), p.ID, json.RawMessage(`{"action":"list"}`))
	require.NoError(t, err)
	require.Contains(t, listing, "thread.txt")
	other := &models.Task{ProjectID: p.ID, Title: "Other", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), other))
	scoped, err := s.ForTask(t.Context(), p.ID, other.ID)
	require.NoError(t, err)
	_, err = scoped.attachmentTransfer(t.Context(), p.ID, threadInput)
	require.Error(t, err)
	_, err = scoped.attachmentTransfer(t.Context(), p.ID, data)
	require.Error(t, err)
}
func TestComputerShellStreamRequiresFinalResult(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "interrupted"}[complete], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "application/x-ndjson", r.Header.Get("Accept"))
				w.Header().Set("Content-Type", "application/x-ndjson")
				io.WriteString(w, "{\"type\":\"output\",\"output\":\"building\"}\n")
				if complete {
					io.WriteString(w, "{\"type\":\"result\",\"output\":\"built\",\"exit_code\":0}\n")
				}
			}))
			defer server.Close()
			s := &ProjectComputerService{client: server.Client()}
			out, bad, err := s.shell(context.Background(), ComputerConnection{Endpoint: server.URL}, []byte(`{"command":"build"}`))
			if complete {
				require.NoError(t, err)
				require.False(t, bad)
				require.Contains(t, out, "built")
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestComputerChromeGrantScopeRevocationAndCleanup(t *testing.T) {
	var cleared int
	db, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
		if args["action"] == "clear_cookies" {
			cleared++
		}
		io.WriteString(w, `{}`)
	})
	require.NoError(t, s.repo.RequestLogin(t.Context(), p.ID, "request_chrome_login", "https://example.test"))
	picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
	g, err := s.repo.CreateChromeGrant(t.Context(), p.ID, "task", picks, time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	other := &models.Task{ProjectID: p.ID, Title: "Other", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), other))
	otherSvc, err := s.ForTask(t.Context(), p.ID, other.ID)
	require.NoError(t, err)
	_, err = otherSvc.repo.ChromeGrant(t.Context(), p.ID, g.ID)
	require.Error(t, err)
	require.NoError(t, s.repo.RecordChromeInstall(t.Context(), p.ID, g.ID))
	require.NoError(t, s.RevokeChromeGrant(t.Context(), p.ID, g.ID))
	require.Greater(t, cleared, 0)
	grant, err := s.repo.ChromeGrant(t.Context(), p.ID, g.ID)
	require.NoError(t, err)
	require.True(t, grant.Revoked)
	projectGrant, err := s.repo.CreateChromeGrant(t.Context(), p.ID, "project", picks, time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	_, err = otherSvc.repo.ChromeGrant(t.Context(), p.ID, projectGrant.ID)
	require.NoError(t, err)
	require.NoError(t, s.repo.RecordChromeInstall(t.Context(), p.ID, projectGrant.ID))
	err = db.Fixtures.ExpireComputerChromeGrant(t.Context(), projectGrant.ID)
	require.NoError(t, err)
	before := cleared
	_, err = s.Connection(t.Context(), p.ID)
	require.NoError(t, err)
	require.Greater(t, cleared, before)
	_ = task
}

func TestComputerHandoffKeepsFollowupsQueuedUntilResponse(t *testing.T) {
	db, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	require.NoError(t, s.repo.RequestHandoff(t.Context(), p.ID, "request_computer_help", "", "Please approve the browser prompt", ""))
	executions := db.Executions
	input := &models.ThreadInput{Content: "Continue the work"}
	config := &models.LLMConfig{Name: "handoff fixture", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, IsFollowup: true, PromptSent: input.Content}
	started, err := executions.CreateDirectTaskFollowupOrQueue(t.Context(), execution, input)
	require.NoError(t, err)
	require.False(t, started)
	require.NotEmpty(t, input.ID)
	threads := db.ThreadInputs
	require.Error(t, threads.ClaimQueuedForTaskExecutionWithModel(t.Context(), input.ID, execution, nil, config))
	pending, err := threads.FindOldestQueuedForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, input.ID, pending.ID)
	err = db.Fixtures.SetTaskStatus(t.Context(), task.ID, models.StatusPending)
	require.NoError(t, err)
	err = db.Fixtures.SetTaskCategory(t.Context(), task.ID, models.CategoryActive)
	require.NoError(t, err)
	tasks := db.Tasks
	claimed, err := tasks.ClaimTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	_, claimed, err = tasks.ClaimTaskForDispatch(t.Context(), task.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, s.repo.FinishHandoff(t.Context(), p.ID, false))
	require.NoError(t, threads.ClaimQueuedForTaskExecutionWithModel(t.Context(), input.ID, execution, nil, config))
}

func TestComputerChatExportKeepsChatOwnership(t *testing.T) {
	db, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "chat report")
	})
	chat := &models.Task{ProjectID: project.ID, Title: "Chat", Category: models.CategoryChat, Status: models.StatusRunning, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), chat))
	config := &models.LLMConfig{Name: "chat", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	execution := &models.Execution{TaskID: chat.ID, AgentConfigID: config.ID, Status: models.ExecRunning}
	require.NoError(t, db.Executions.Create(t.Context(), execution))
	scoped, err := svc.ForTask(t.Context(), project.ID, chat.ID)
	require.NoError(t, err)
	result, err := scoped.attachmentTransfer(llmcontracts.WithArtifactExecutionID(t.Context(), execution.ID), project.ID, json.RawMessage(`{"action":"from_computer","computer_path":"chat.txt"}`))
	require.NoError(t, err)
	require.Contains(t, result, "/chat/attachments/")
	chatFiles, err := db.ChatAttachments.ListByExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	require.Len(t, chatFiles, 1)
	taskFiles, err := db.Attachments.ListByTask(t.Context(), chat.ID)
	require.NoError(t, err)
	require.Empty(t, taskFiles)
}
