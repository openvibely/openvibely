package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/viewmodels"
)

func TestHandler_AgentLifecycleHooks_RejectForeignProjectWithoutMutation(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	projectA := createProject(t, h, "lifecycle project A")
	projectB := createProject(t, h, "lifecycle project B")
	agent := &models.Agent{
		Name:                "Project B Agent",
		Description:         "original description",
		SystemPrompt:        "original prompt",
		Model:               "inherit",
		Scope:               models.AgentScopeProject,
		ProjectID:           projectB.ID,
		Tools:               []string{},
		SelectableAsPrimary: true,
		Enabled:             true,
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	originalHook := &models.AgentLifecycleHook{
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "project_b_skill",
		Blocking:       true,
		Enabled:        true,
		PromptOverride: "project-b-secret-prompt",
		RunPolicyJSON:  `{"retries":2}`,
		OutputContract: models.OutputContractActivitySummary,
	}
	if err := lifecycleRepo.CreateHook(t.Context(), originalHook); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	beforeAgent, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("load original agent: %v", err)
	}
	beforeHooks, err := lifecycleRepo.HooksByAgent(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("load original hooks: %v", err)
	}

	foreignGET := httptest.NewRequest(http.MethodGet, "/agents/"+agent.ID+"/lifecycle-hooks?project_id="+projectA.ID, nil)
	foreignGET.Header.Set(echo.HeaderAccept, "application/json")
	foreignGETRecorder := httptest.NewRecorder()
	e.ServeHTTP(foreignGETRecorder, foreignGET)
	if foreignGETRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign GET status=%d body=%s", foreignGETRecorder.Code, foreignGETRecorder.Body.String())
	}
	if strings.Contains(foreignGETRecorder.Body.String(), "project_b_skill") || strings.Contains(foreignGETRecorder.Body.String(), "project-b-secret-prompt") {
		t.Fatalf("foreign GET leaked Project B hook data: %s", foreignGETRecorder.Body.String())
	}

	if err := h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, projectA.ID); err != nil {
		t.Fatalf("select Project A: %v", err)
	}
	selectedGET := httptest.NewRequest(http.MethodGet, "/agents/"+agent.ID+"/lifecycle-hooks", nil)
	selectedGETRecorder := httptest.NewRecorder()
	e.ServeHTTP(selectedGETRecorder, selectedGET)
	if selectedGETRecorder.Code != http.StatusNotFound {
		t.Fatalf("selected-project GET status=%d body=%s", selectedGETRecorder.Code, selectedGETRecorder.Body.String())
	}
	if strings.Contains(selectedGETRecorder.Body.String(), "project-b-secret-prompt") {
		t.Fatalf("selected-project GET leaked Project B hook data: %s", selectedGETRecorder.Body.String())
	}

	foreignPayload, _ := json.Marshal([]hookSavePayload{{
		When:           string(models.LifecycleAfterComplete),
		SkillKey:       "project_a_skill",
		Enabled:        true,
		PromptOverride: "project-a-replacement",
		OutputContract: string(models.OutputContractActivitySummary),
	}})
	foreignPUT := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID+"/lifecycle-hooks?project_id="+projectA.ID, bytes.NewReader(foreignPayload))
	foreignPUT.Header.Set(echo.HeaderContentType, "application/json")
	foreignPUTRecorder := httptest.NewRecorder()
	e.ServeHTTP(foreignPUTRecorder, foreignPUT)
	if foreignPUTRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign PUT status=%d body=%s", foreignPUTRecorder.Code, foreignPUTRecorder.Body.String())
	}
	if strings.Contains(foreignPUTRecorder.Body.String(), "project_b_skill") || strings.Contains(foreignPUTRecorder.Body.String(), "project-b-secret-prompt") {
		t.Fatalf("foreign PUT leaked Project B hook data: %s", foreignPUTRecorder.Body.String())
	}

	form := url.Values{}
	form.Set("name", "mutated from Project A")
	form.Set("description", "mutated description")
	form.Set("system_prompt", "mutated prompt")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "project-b-agent")
	form.Set("scope", string(models.AgentScopeProject))
	form.Set("project_id", projectA.ID)
	form.Set("selectable_as_primary", "false")
	form.Set("enabled", "false")
	form.Set("lifecycle_hooks_json", string(foreignPayload))
	foreignUpdate := performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+agent.ID+"?project_id="+projectA.ID, form)
	if foreignUpdate.Code != http.StatusNotFound {
		t.Fatalf("foreign dialog update status=%d body=%s", foreignUpdate.Code, foreignUpdate.Body.String())
	}
	if strings.Contains(foreignUpdate.Body.String(), "project_b_skill") || strings.Contains(foreignUpdate.Body.String(), "project-b-secret-prompt") {
		t.Fatalf("foreign dialog update leaked Project B hook data: %s", foreignUpdate.Body.String())
	}

	afterAgent, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("load agent after foreign requests: %v", err)
	}
	if afterAgent == nil || beforeAgent == nil {
		t.Fatalf("expected agent snapshots, before=%+v after=%+v", beforeAgent, afterAgent)
	}
	beforeAgent.CreatedAt = beforeAgent.CreatedAt.UTC()
	beforeAgent.UpdatedAt = beforeAgent.UpdatedAt.UTC()
	afterAgent.CreatedAt = afterAgent.CreatedAt.UTC()
	afterAgent.UpdatedAt = afterAgent.UpdatedAt.UTC()
	if !reflect.DeepEqual(*afterAgent, *beforeAgent) {
		t.Fatalf("foreign dialog update mutated agent:\nbefore=%+v\nafter=%+v", *beforeAgent, *afterAgent)
	}
	afterHooks, err := lifecycleRepo.HooksByAgent(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("load hooks after foreign requests: %v", err)
	}
	if !reflect.DeepEqual(afterHooks, beforeHooks) {
		t.Fatalf("foreign requests mutated hooks:\nbefore=%+v\nafter=%+v", beforeHooks, afterHooks)
	}

	unknownGET := httptest.NewRequest(http.MethodGet, "/agents/missing/lifecycle-hooks?project_id="+projectA.ID, nil)
	unknownGETRecorder := httptest.NewRecorder()
	e.ServeHTTP(unknownGETRecorder, unknownGET)
	if unknownGETRecorder.Code != http.StatusNotFound {
		t.Fatalf("unknown GET status=%d body=%s", unknownGETRecorder.Code, unknownGETRecorder.Body.String())
	}
}

func TestHandler_AgentLifecycleHooks_AllowsOwningProjectAndGlobalContexts(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	projectA := createProject(t, h, "same-project A")
	projectB := createProject(t, h, "same-project B")
	projectAgent := &models.Agent{
		Name:                "Project B Agent",
		Description:         "before dialog save",
		SystemPrompt:        "project work",
		Model:               "inherit",
		Scope:               models.AgentScopeProject,
		ProjectID:           projectB.ID,
		Key:                 "project-b-agent",
		Tools:               []string{},
		SelectableAsPrimary: true,
		Enabled:             true,
	}
	if err := agentRepo.Create(t.Context(), projectAgent); err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	oldHook := &models.AgentLifecycleHook{
		AgentID:        projectAgent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "old_project_skill",
		Enabled:        true,
		PromptOverride: "old project prompt",
	}
	if err := lifecycleRepo.CreateHook(t.Context(), oldHook); err != nil {
		t.Fatalf("create old project hook: %v", err)
	}

	ownerGET := httptest.NewRequest(http.MethodGet, "/agents/"+projectAgent.ID+"/lifecycle-hooks?project_id="+projectB.ID, nil)
	ownerGETRecorder := httptest.NewRecorder()
	e.ServeHTTP(ownerGETRecorder, ownerGET)
	if ownerGETRecorder.Code != http.StatusOK || !strings.Contains(ownerGETRecorder.Body.String(), "old_project_skill") {
		t.Fatalf("same-project GET status=%d body=%s", ownerGETRecorder.Code, ownerGETRecorder.Body.String())
	}

	directPayload, _ := json.Marshal([]hookSavePayload{{
		When:           string(models.LifecycleAfterComplete),
		SkillKey:       "same_project_skill",
		Enabled:        true,
		PromptOverride: "same project replacement",
		OutputContract: string(models.OutputContractActivitySummary),
	}})
	ownerPUT := httptest.NewRequest(http.MethodPut, "/agents/"+projectAgent.ID+"/lifecycle-hooks?project_id="+projectB.ID, bytes.NewReader(directPayload))
	ownerPUT.Header.Set(echo.HeaderContentType, "application/json")
	ownerPUTRecorder := httptest.NewRecorder()
	e.ServeHTTP(ownerPUTRecorder, ownerPUT)
	if ownerPUTRecorder.Code != http.StatusOK {
		t.Fatalf("same-project PUT status=%d body=%s", ownerPUTRecorder.Code, ownerPUTRecorder.Body.String())
	}
	projectHooks, err := lifecycleRepo.HooksByAgent(t.Context(), projectAgent.ID)
	if err != nil {
		t.Fatalf("load same-project hooks: %v", err)
	}
	if len(projectHooks) != 1 || projectHooks[0].SkillKey != "same_project_skill" || projectHooks[0].PromptOverride != "same project replacement" {
		t.Fatalf("same-project PUT did not reconcile omitted hook: %+v", projectHooks)
	}

	dialogPayload, _ := json.Marshal([]hookSavePayload{{
		When:           string(models.LifecycleAfterComplete),
		SkillKey:       "dialog_project_skill",
		Enabled:        true,
		OutputContract: string(models.OutputContractActivitySummary),
	}})
	form := url.Values{}
	form.Set("name", "Project B Agent")
	form.Set("description", "after same-project dialog save")
	form.Set("system_prompt", "project work")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "project-b-agent")
	form.Set("scope", string(models.AgentScopeProject))
	form.Set("project_id", projectB.ID)
	form.Set("selectable_as_primary", "true")
	form.Set("enabled", "true")
	form.Set("permission_defaults_json", `{}`)
	form.Set("source_refs_json", `[]`)
	form.Set("lifecycle_hooks_json", string(dialogPayload))
	ownerUpdate := performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+projectAgent.ID+"?project_id="+projectB.ID, form)
	if ownerUpdate.Code != http.StatusOK {
		t.Fatalf("same-project dialog update status=%d body=%s", ownerUpdate.Code, ownerUpdate.Body.String())
	}
	storedProjectAgent, err := agentRepo.GetByID(t.Context(), projectAgent.ID)
	if err != nil {
		t.Fatalf("reload same-project agent: %v", err)
	}
	if storedProjectAgent.Description != "after same-project dialog save" || storedProjectAgent.ProjectID != projectB.ID {
		t.Fatalf("same-project dialog update did not persist expected agent: %+v", storedProjectAgent)
	}
	projectHooks, err = lifecycleRepo.HooksByAgent(t.Context(), projectAgent.ID)
	if err != nil {
		t.Fatalf("reload dialog hooks: %v", err)
	}
	if len(projectHooks) != 1 || projectHooks[0].SkillKey != "dialog_project_skill" {
		t.Fatalf("same-project dialog save did not reconcile hooks: %+v", projectHooks)
	}

	globalAgent := &models.Agent{Name: "Global Agent", Scope: models.AgentScopeGlobal, Model: "inherit", Tools: []string{}, Enabled: true}
	if err := agentRepo.Create(t.Context(), globalAgent); err != nil {
		t.Fatalf("create global agent: %v", err)
	}
	if err := lifecycleRepo.CreateHook(t.Context(), &models.AgentLifecycleHook{AgentID: globalAgent.ID, When: models.LifecycleBeforeRun, SkillKey: "global_skill", Enabled: true}); err != nil {
		t.Fatalf("create global hook: %v", err)
	}
	globalGET := httptest.NewRequest(http.MethodGet, "/agents/"+globalAgent.ID+"/lifecycle-hooks?project_id="+projectA.ID, nil)
	globalGETRecorder := httptest.NewRecorder()
	e.ServeHTTP(globalGETRecorder, globalGET)
	if globalGETRecorder.Code != http.StatusOK || !strings.Contains(globalGETRecorder.Body.String(), "global_skill") {
		t.Fatalf("global Agent GET from project context status=%d body=%s", globalGETRecorder.Code, globalGETRecorder.Body.String())
	}

	unknownPUT := httptest.NewRequest(http.MethodPut, "/agents/missing/lifecycle-hooks?project_id="+projectA.ID, bytes.NewReader([]byte(`[]`)))
	unknownPUT.Header.Set(echo.HeaderContentType, "application/json")
	unknownPUTRecorder := httptest.NewRecorder()
	e.ServeHTTP(unknownPUTRecorder, unknownPUT)
	if unknownPUTRecorder.Code != http.StatusNotFound {
		t.Fatalf("unknown PUT status=%d body=%s", unknownPUTRecorder.Code, unknownPUTRecorder.Body.String())
	}
}

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
	for _, want := range []string{
		"Selected memories", "Selected skills", "badge badge-outline", "renderBadgeRow", "row.selected_skills", "row.selected_memories",
		`id="lifecycle-activity-scroll"`, `data-lifecycle-scrollport="true"`, `limit=' + pageSize`, "before=", "after=", "has_more", "next_cursor", "sse-task-event", "requestVersion", "newerPending", "task_lifecycle_execution_changed", "data-lifecycle-load-older", "data-lifecycle-no-more"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected lifecycle tab script to render lifecycle lists as badge rows containing %q, got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Selected memories used for injected context") || strings.Contains(body, "<ul class=\"space-y-1\">") {
		t.Fatalf("expected lifecycle tab script to avoid stacked selected memory list UI, got:\n%s", body)
	}
}

func TestHandler_GetLifecycleExecutionEvents_ReturnsTraceEventsForSameProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetLifecycleRepo(lifecycleRepo)

	project := createProject(t, h, "trace-events-project")
	task := createTask(t, h, project.ID, "Trace Events")
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

	req := httptest.NewRequest(http.MethodGet, "/api/lifecycle-executions/"+exec.ID+"/events?project_id="+project.ID, nil)
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

func TestHandler_GetLifecycleExecutionEvents_RejectsForeignProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetLifecycleRepo(lifecycleRepo)

	projectA := createProject(t, h, "trace-project-a")
	projectB := createProject(t, h, "trace-project-b")
	task := createTask(t, h, projectA.ID, "Project A Trace Events")
	agent := &models.Agent{Name: "foreign-trace-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	exec := &models.LifecycleExecution{TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete, Status: models.LifecycleExecCompleted}
	if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	if err := lifecycleRepo.AppendExecutionEvent(t.Context(), &models.LifecycleExecutionEvent{LifecycleExecutionID: exec.ID, EventType: "tool_result", PayloadJSON: `{"secret_payload":"project-a-only"}`}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/lifecycle-executions/"+exec.ID+"/events?project_id="+projectB.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("expected foreign project request to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "project-a-only") || strings.Contains(rec.Body.String(), "secret_payload") {
		t.Fatalf("foreign lifecycle event payload leaked: %s", rec.Body.String())
	}
}

func TestHandler_GetTaskLifecycleExecution_ReturnsPromptSafeViewAndRejectsForeign(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetLifecycleRepo(lifecycleRepo)

	projectA := createProject(t, h, "execution-project-a")
	projectB := createProject(t, h, "execution-project-b")
	taskA := createTask(t, h, projectA.ID, "Execution Task A")
	taskB := createTask(t, h, projectB.ID, "Execution Task B")
	agent := &models.Agent{Name: "execution-view-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	exec := &models.LifecycleExecution{
		TaskID:         taskA.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "summarize_activity",
		OutputContract: models.OutputContractActivitySummary,
		Status:         models.LifecycleExecCompleted,
		InputJSON:      `{"prompt":"sensitive input"}`,
		OutputJSON:     `{"summary":"safe summary","skills":["safe-skill"]}`,
		Error:          "safe error",
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskA.ID+"/lifecycle-executions/"+exec.ID+"?project_id="+projectA.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got viewmodels.LifecycleExecutionView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode execution view: %v", err)
	}
	if got.ID != exec.ID || got.Status != string(models.LifecycleExecCompleted) || got.Summary != "safe summary" {
		t.Fatalf("unexpected execution view: %+v", got)
	}
	if strings.Contains(rec.Body.String(), "sensitive input") || strings.Contains(rec.Body.String(), "input_json") {
		t.Fatalf("execution view leaked protected input data: %s", rec.Body.String())
	}

	for name, path := range map[string]string{
		"foreign project": "/api/tasks/" + taskA.ID + "/lifecycle-executions/" + exec.ID + "?project_id=" + projectB.ID,
		"foreign task":    "/api/tasks/" + taskB.ID + "/lifecycle-executions/" + exec.ID + "?project_id=" + projectA.ID,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "sensitive input") || strings.Contains(rec.Body.String(), "safe summary") {
				t.Fatalf("foreign execution response leaked data: %s", rec.Body.String())
			}
		})
	}
}

func TestHandler_GetLifecycleActivity_UnknownIDsReturnControlledNotFound(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetLifecycleRepo(lifecycleRepo)
	project := createProject(t, h, "unknown-lifecycle-project")

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "task lifecycle list", path: "/api/tasks/missing-task/lifecycle-executions?project_id=" + project.ID, want: "task not found"},
		{name: "execution events", path: "/api/lifecycle-executions/missing-exec/events?project_id=" + project.ID, want: "lifecycle execution not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("expected controlled not found message %q, got %s", tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandler_GetTaskLifecycleExecutions_RejectsForeignProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	projectA := createProject(t, h, "lifecycle-project-a")
	projectB := createProject(t, h, "lifecycle-project-b")
	task := createTask(t, h, projectA.ID, "Project A Lifecycle Task")
	agent := &models.Agent{Name: "foreign-activity-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	exec := &models.LifecycleExecution{
		TaskID:         task.ID,
		AgentID:        agent.ID,
		When:           models.LifecycleRouteTask,
		SkillKey:       "route_task",
		OutputContract: models.OutputContractSelectedSkills,
		Status:         models.LifecycleExecFailed,
		Error:          "project-a-error",
		OutputJSON:     `{"skills":["project_a_skill"],"memories":[{"file":"project_a_memory.md"}],"summary":"project-a-summary"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/lifecycle-executions?project_id="+projectB.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("expected foreign project request to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{"project_a_skill", "project_a_memory.md", "project-a-summary", "project-a-error"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("foreign lifecycle content %q leaked: %s", forbidden, rec.Body.String())
		}
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

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/lifecycle-executions?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got viewmodels.LifecycleExecutionPageView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 6 {
		t.Fatalf("expected 6 views, got %d", len(got.Items))
	}
	summaries := map[string]string{}
	byKey := map[string]viewmodels.LifecycleExecutionView{}
	var route viewmodels.LifecycleExecutionView
	var recall viewmodels.LifecycleExecutionView
	var beforeRecall viewmodels.LifecycleExecutionView
	for _, row := range got.Items {
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

func TestHandler_GetTaskLifecycleExecutions_PaginatesAndPreservesProjectBoundary(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	project := createProject(t, h, "lifecycle-page-handler")
	task := createTask(t, h, project.ID, "Paged lifecycle task")
	agent := &models.Agent{Name: "paged-lifecycle-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	rows := []struct {
		id string
		at string
	}{
		{id: "handler-a", at: "2000-01-01 10:00:00"},
		{id: "handler-b", at: "2000-01-01 11:00:00"},
		{id: "handler-c", at: "2000-01-01 12:00:00"},
		{id: "handler-d", at: "2000-01-01 13:00:00"},
	}
	for _, row := range rows {
		exec := &models.LifecycleExecution{
			TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete,
			SkillKey: row.id, OutputContract: models.OutputContractActivitySummary,
			Status: models.LifecycleExecCompleted, InputJSON: strings.Repeat("private input ", 500),
			OutputJSON: `{"summary":"visible summary"}`,
		}
		if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
			t.Fatalf("create execution %s: %v", row.id, err)
		}
		if _, err := db.ExecContext(t.Context(), `UPDATE lifecycle_executions SET id = ?, started_at = ? WHERE id = ?`, row.id, row.at, exec.ID); err != nil {
			t.Fatalf("set execution %s: %v", row.id, err)
		}
	}

	getPage := func(path string) viewmodels.LifecycleExecutionPageView {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d: %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "private input") {
			t.Fatalf("paged lifecycle response leaked raw input payload")
		}
		var page viewmodels.LifecycleExecutionPageView
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return page
	}

	basePath := "/api/tasks/" + task.ID + "/lifecycle-executions?project_id=" + url.QueryEscape(project.ID) + "&limit=2"
	first := getPage(basePath)
	if got := lifecycleViewIDs(first.Items); !reflect.DeepEqual(got, []string{"handler-d", "handler-c"}) {
		t.Fatalf("first page IDs = %v, want newest-first page", got)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want continuation cursor", first)
	}
	for _, item := range first.Items {
		if item.Summary != "visible summary" {
			t.Fatalf("page item lost compact summary: %+v", item)
		}
	}

	second := getPage(basePath + "&before=" + url.QueryEscape(first.NextCursor))
	if got := lifecycleViewIDs(second.Items); !reflect.DeepEqual(got, []string{"handler-b", "handler-a"}) {
		t.Fatalf("second page IDs = %v, want older rows", got)
	}
	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want no more results", second)
	}

	newExec := &models.LifecycleExecution{
		TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete,
		SkillKey: "handler-new", OutputContract: models.OutputContractActivitySummary,
		Status: models.LifecycleExecRunning, OutputJSON: `{"summary":"new insert"}`,
	}
	if err := lifecycleRepo.CreateExecution(t.Context(), newExec); err != nil {
		t.Fatalf("create newer execution: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE lifecycle_executions SET id = ?, started_at = ? WHERE id = ?`, "handler-new", "2000-01-01 14:00:00", newExec.ID); err != nil {
		t.Fatalf("set newer execution: %v", err)
	}
	newer := getPage(basePath + "&after=" + url.QueryEscape(first.Items[0].ID))
	if got := lifecycleViewIDs(newer.Items); !reflect.DeepEqual(got, []string{"handler-new"}) {
		t.Fatalf("newer page IDs = %v, want live insert in fetch order", got)
	}

	for _, path := range []string{
		basePath + "&before=not-a-cursor",
		basePath + "&before=" + url.QueryEscape(first.NextCursor) + "&after=" + url.QueryEscape(first.Items[0].ID),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s returned %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
	}

	foreignProject := createProject(t, h, "lifecycle-page-foreign")
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/lifecycle-executions?project_id="+url.QueryEscape(foreignProject.ID), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "handler-") {
		t.Fatalf("foreign lifecycle page response = %d %s, want isolated 404", rec.Code, rec.Body.String())
	}
}

func lifecycleViewIDs(rows []viewmodels.LifecycleExecutionView) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestHandler_GetTaskLifecycleExecutions_BoundsPromptSafeDetailPayload(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	project := createProject(t, h, "lifecycle-payload-bounds")
	task := createTask(t, h, project.ID, "Bound lifecycle payload")
	agent := &models.Agent{Name: "payload-agent", SystemPrompt: "x"}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	memories := make([]map[string]string, maxLifecycleMemoryViews+8)
	for i := range memories {
		memories[i] = map[string]string{"file": "memory-" + strconv.Itoa(i) + ".md", "summary": strings.Repeat("detail ", 80)}
	}
	memoryOutput, err := json.Marshal(map[string]any{"memories": memories})
	if err != nil {
		t.Fatalf("marshal memories: %v", err)
	}
	longSummary := strings.Repeat("summary ", 100)
	for _, exec := range []*models.LifecycleExecution{
		{
			TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete,
			SkillKey: "large-summary", OutputContract: models.OutputContractActivitySummary,
			Status: models.LifecycleExecFailed, Error: strings.Repeat("error ", 100),
			OutputJSON: `{"summary":"` + longSummary + `"}`,
		},
		{
			TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleRouteTask,
			SkillKey: "large-memory", OutputContract: models.OutputContractSelectedMemories,
			Status: models.LifecycleExecCompleted, OutputJSON: string(memoryOutput),
		},
	} {
		if err := lifecycleRepo.CreateExecution(t.Context(), exec); err != nil {
			t.Fatalf("create execution: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/lifecycle-executions?project_id="+url.QueryEscape(project.ID)+"&limit=2", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var page viewmodels.LifecycleExecutionPageView
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected two bounded detail views, got %d", len(page.Items))
	}
	for _, item := range page.Items {
		if len(item.Summary) > maxLifecycleDisplayTextLen+3 || len(item.Error) > maxLifecycleDisplayTextLen+3 {
			t.Fatalf("unbounded summary/error in lifecycle view: summary=%d error=%d", len(item.Summary), len(item.Error))
		}
		if len(item.SelectedMemories) > maxLifecycleMemoryViews {
			t.Fatalf("unbounded selected memories in lifecycle view: %d", len(item.SelectedMemories))
		}
	}
	if len(rec.Body.Bytes()) > 25000 {
		t.Fatalf("lifecycle detail page payload is too large: %d bytes", len(rec.Body.Bytes()))
	}
}
