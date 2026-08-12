package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/viewmodels"
)

func TestHandler_GetAgentLifecycleHooks_ReturnsAgentScopedHooks(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	agent := &models.Agent{Name: "test-agent", SystemPrompt: "you do work"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	hook := &models.AgentLifecycleHook{
		AgentID:        agent.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "recall",
		Blocking:       true,
		Enabled:        true,
		OutputContract: models.OutputContractContextBlock,
	}
	if err := lifecycleRepo.CreateHook(t.Context(), hook); err != nil {
		t.Fatalf("create hook: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents/"+agent.ID+"/lifecycle-hooks", nil)
	req.Header.Set(echo.HeaderAccept, "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []models.AgentLifecycleHook
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].SkillKey != "recall" {
		t.Fatalf("expected one recall hook, got %+v", got)
	}
}

func TestHandler_SaveAgentLifecycleHooks_ReconcilesSet(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	agent := &models.Agent{Name: "save-hooks", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Seed: one existing hook that the save payload omits — should be deleted.
	existing := &models.AgentLifecycleHook{
		AgentID:  agent.ID,
		When:     models.LifecycleAfterComplete,
		SkillKey: "old-skill",
		Enabled:  true,
	}
	if err := lifecycleRepo.CreateHook(t.Context(), existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	payload := []hookSavePayload{
		{When: "before_run", SkillKey: "load_project_context", Blocking: true, Enabled: true, OutputContract: "context_block"},
		{When: "after_complete", SkillKey: "summarize_activity", Blocking: false, Enabled: true, OutputContract: "activity_summary"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID+"/lifecycle-hooks", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	hooks, err := lifecycleRepo.HooksByAgent(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks after save, got %d", len(hooks))
	}
	have := map[string]bool{}
	for _, h := range hooks {
		have[string(h.When)+"/"+h.SkillKey] = true
	}
	if !have["before_run/load_project_context"] || !have["after_complete/summarize_activity"] {
		t.Fatalf("unexpected hooks after save: %+v", hooks)
	}
	if have["after_complete/old-skill"] {
		t.Fatalf("stale hook should have been deleted")
	}
}

func TestHandler_SaveAgentLifecycleHooks_RejectsInvalidWhenAndTaskMode(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	agent := &models.Agent{Name: "bad-when", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	body, _ := json.Marshal([]hookSavePayload{{When: "task_mode", SkillKey: "x", Enabled: true}})
	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID+"/lifecycle-hooks", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad when, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_SaveAgentLifecycleHooks_RejectsProtectedAgent(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	// Use the seeded built-in agent from migration 078.
	all, err := agentRepo.List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var protectedID string
	for _, a := range all {
		if a.GeneratedStatus == models.AgentStatusProtected {
			protectedID = a.ID
			break
		}
	}
	if protectedID == "" {
		t.Fatalf("expected at least one protected built-in agent from seed")
	}

	body, _ := json.Marshal([]hookSavePayload{{When: "before_run", SkillKey: "x", Enabled: true}})
	req := httptest.NewRequest(http.MethodPut, "/agents/"+protectedID+"/lifecycle-hooks", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskDetailLifecycleTabRendersSelectedMemoryClientUI(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Lifecycle UI Project")
	task := createTask(t, h, project.ID, "Lifecycle UI Task")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=lifecycle", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId")
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTask(c); err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Selected memories", "Selected skills", "badge badge-outline", "renderBadgeRow", "r.selected_skills", "r.selected_memories"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected lifecycle tab script to render lifecycle lists as badge rows containing %q, got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Selected memories used for injected context") || strings.Contains(body, "<ul class=\"space-y-1\">") {
		t.Fatalf("expected lifecycle tab script to avoid stacked selected memory list UI, got:\n%s", body)
	}
}

func TestHandler_GetLifecycleExecutionEvents_ReturnsTraceEvents(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetLifecycleRepo(lifecycleRepo)

	task := &models.Task{ProjectID: "default", Title: "Trace Events", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(t.Context(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent := &models.Agent{Name: "trace-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	exec := &models.LifecycleExecution{TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete, Status: models.LifecycleExecCompleted}
	if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	if err := lifecycleRepo.AppendExecutionEvent(t.Context(), &models.LifecycleExecutionEvent{LifecycleExecutionID: exec.ID, EventType: "tool_call", PayloadJSON: `{"name":"skills_list"}`}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/lifecycle-executions/"+exec.ID+"/events", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []viewmodels.LifecycleExecutionEventView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].EventType != "tool_call" || got[0].Payload["name"] != "skills_list" {
		t.Fatalf("unexpected trace events: %+v", got)
	}
}

func TestHandler_GetTaskLifecycleExecutions_ReturnsPromptSafeView(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	// Create a task and agent to back the FKs.
	project := createProject(t, h, "lifecycle-activity")
	task := &models.Task{ProjectID: project.ID, Title: "demo", Status: models.StatusPending, Category: "active"}
	if err := h.taskRepo.Create(t.Context(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent := &models.Agent{Name: "activity-stub", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	exec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "summarize_activity",
		OutputContract: models.OutputContractActivitySummary,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"summary":"Recorded user preference","skipped":false}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	routeExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleRouteTask,
		SkillKey:       "route_task",
		OutputContract: models.OutputContractSelectedSkills,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"skills":["openvibely_agent_skill_architecture","debug_go_tests"],"confidence":0.9,"reason":"matches prompt"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), routeExec); err != nil {
		t.Fatalf("create route exec: %v", err)
	}
	recallExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleRouteTask,
		SkillKey:       "recall_memory",
		OutputContract: models.OutputContractSelectedMemories,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"memories":[{"file":"provider_architecture.md","topic":"Provider lifecycle","summary":"Use mode-driven provider routing.","snippet":"Verify stale memory against current code before relying on it."},{"file":"provider_architecture.md","topic":"Provider lifecycle","summary":"Duplicate summary should collapse by file."},{"file":"/tmp/secret.md","summary":"must not expose"}],"content":"private full injected memory context that should not be returned raw","confidence":0.8,"reason":"matches prompt"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), recallExec); err != nil {
		t.Fatalf("create recall exec: %v", err)
	}
	beforeRecallExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "recall_memory",
		OutputContract: models.OutputContractContextBlock,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"content":"full memory body hidden from response","selected_memories":[{"file":"runbook.md","topic":"Runbook","summary":"Use the runbook.","snippet":"Follow the ordered steps."}],"sources":["extra_source.md"],"confidence":0.9}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), beforeRecallExec); err != nil {
		t.Fatalf("create before-run recall exec: %v", err)
	}
	modeExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleTaskMode,
		SkillKey:       "select_mode",
		OutputContract: models.OutputContractSelectedMode,
		Status:         models.LifecycleExecCompleted,
		OutputJSON:     `{"mode":"coding"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), modeExec); err != nil {
		t.Fatalf("create mode exec: %v", err)
	}
	failedExec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "observe_task_for_learning",
		OutputContract: models.OutputContractLearningSummary,
		Status:         models.LifecycleExecFailed,
		Error:          "hook failed safely",
		OutputJSON:     `{"summary":"not used after failure"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), failedExec); err != nil {
		t.Fatalf("create failed exec: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/lifecycle-executions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []viewmodels.LifecycleExecutionView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 views, got %d", len(got))
	}
	summaries := map[string]string{}
	byKey := map[string]viewmodels.LifecycleExecutionView{}
	var route viewmodels.LifecycleExecutionView
	var recall viewmodels.LifecycleExecutionView
	var beforeRecall viewmodels.LifecycleExecutionView
	for _, row := range got {
		key := row.When + "/" + row.SkillKey
		summaries[key] = row.Summary
		byKey[key] = row
		if row.When == "route_task" && row.SkillKey == "route_task" {
			route = row
		}
		if row.When == "route_task" && row.SkillKey == "recall_memory" {
			recall = row
		}
		if row.When == "before_run" && row.SkillKey == "recall_memory" {
			beforeRecall = row
		}
	}
	if summaries["after_complete/summarize_activity"] != "Recorded user preference" {
		t.Fatalf("expected human summary extracted from activity_summary contract, got %+v", summaries)
	}
	if summaries["route_task/route_task"] != "Selected skills: openvibely_agent_skill_architecture, debug_go_tests" {
		t.Fatalf("expected selected skill summary, got %+v", summaries)
	}
	if summaries["route_task/recall_memory"] != "" {
		t.Fatalf("expected selected memory route summary to stay badge-only, got %+v", summaries)
	}
	if summaries["task_mode/select_mode"] != "coding" {
		t.Fatalf("expected selected mode summary, got %+v", summaries)
	}
	if byKey["after_complete/observe_task_for_learning"].Status != "failed" || byKey["after_complete/observe_task_for_learning"].Error != "hook failed safely" {
		t.Fatalf("expected failed lifecycle status and error, got %+v", byKey["after_complete/observe_task_for_learning"])
	}
	if len(route.SelectedSkills) != 2 || route.SelectedSkills[0] != "openvibely_agent_skill_architecture" || route.SelectedSkills[1] != "debug_go_tests" {
		t.Fatalf("expected selected skills exposed as badge identifiers, got %+v", route.SelectedSkills)
	}
	if len(recall.SelectedMemories) != 1 {
		t.Fatalf("expected deduped sanitized selected memory identifiers, got %+v", recall.SelectedMemories)
	}
	if recall.SelectedMemories[0].File != "provider_architecture.md" || recall.SelectedMemories[0].Summary != "Use mode-driven provider routing." || recall.SelectedMemories[0].Snippet == "" {
		t.Fatalf("expected compact selected memory metadata, got %+v", recall.SelectedMemories)
	}
	if len(beforeRecall.SelectedMemories) != 2 {
		t.Fatalf("expected before-run recall memories from selected metadata and sources, got %+v", beforeRecall.SelectedMemories)
	}
	if beforeRecall.SelectedMemories[0].File != "runbook.md" || beforeRecall.SelectedMemories[0].Summary != "Use the runbook." || beforeRecall.SelectedMemories[1].File != "extra_source.md" {
		t.Fatalf("expected compact before-run selected memory metadata, got %+v", beforeRecall.SelectedMemories)
	}
	// Prompt-safe view must not leak the raw OutputJSON or any internal fields
	// that the dialog should never display.
	for _, forbidden := range [][]byte{[]byte("skipped"), []byte("private full injected memory context"), []byte("full memory body hidden from response"), []byte("/tmp/secret.md"), []byte("../escape.md"), []byte("must not expose")} {
		if bytes.Contains(rec.Body.Bytes(), forbidden) {
			t.Fatalf("raw or unsafe lifecycle output leaked through prompt-safe view: %s", forbidden)
		}
	}
}
