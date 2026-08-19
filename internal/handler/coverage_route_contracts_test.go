package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutomationChatActionHelpersFormatPrincipalPlansAndJSON(t *testing.T) {
	require.Equal(t, "local", automationActionPrincipal(streamingResponseParams{}))
	require.Equal(t, "slack:user:U123", automationActionPrincipal(streamingResponseParams{PrincipalID: " slack:user:U123 "}))

	require.Empty(t, automationSavePlanForChat(nil))
	plan := &models.AutomationSavePlan{
		Effects: []models.AutomationSaveEffect{
			{Name: "Create Task [a1b2c3d4]", Operation: "create", ResourceType: "task"},
			{Name: "Keep exact suffix [not-an-id]", Operation: "noop", ResourceType: "task"},
		},
		Validation: []models.AutomationValidationIssue{{Code: "missing_trigger", Message: "missing trigger"}},
		WillNot:    []string{"start inactive automation"},
	}
	formatted := automationSavePlanForChat(plan)
	effects, ok := formatted["effects"].([]models.AutomationSaveEffect)
	require.True(t, ok)
	require.Equal(t, "Create Task", effects[0].Name)
	require.Equal(t, "Keep exact suffix [not-an-id]", effects[1].Name)
	require.Equal(t, []models.AutomationValidationIssue{{Code: "missing_trigger", Message: "missing trigger"}}, formatted["validation_errors"])
	require.Equal(t, []string{"start inactive automation"}, formatted["will_not"])

	encoded, err := marshalAutomationActionResult(map[string]any{"ok": true, "name": "automation"})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true,"name":"automation"}`, encoded)
}

func TestSystemUpdateRoutesReturnUnavailableContractsWhenCoordinatorMissing(t *testing.T) {
	tc := NewTestContext(t)

	for _, tcse := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "state hidden", method: http.MethodGet, path: "/api/system/update", want: http.StatusNoContent},
		{name: "events hidden", method: http.MethodGet, path: "/api/system/update/events", want: http.StatusNoContent},
		{name: "apply missing", method: http.MethodPost, path: "/api/system/update/apply", want: http.StatusNotFound},
		{name: "cancel missing", method: http.MethodPost, path: "/api/system/update/cancel", want: http.StatusNotFound},
	} {
		t.Run(tcse.name, func(t *testing.T) {
			req := httptest.NewRequest(tcse.method, tcse.path, nil)
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)
			require.Equal(t, tcse.want, rec.Code, rec.Body.String())
		})
	}
}

func TestSwarmRoutesValidateMissingServiceAndTaskContracts(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Swarm Route Contracts").Build()
	task := tc.CreateTask(project.ID).WithTitle("Ordinary task").WithCategory(models.CategoryBacklog).Build()

	missing := tc.HTTP().Get("/api/tasks/missing/swarm").Execute()
	require.Equal(t, http.StatusNotFound, missing.Code)

	for _, tcse := range []struct {
		name         string
		method       string
		path         string
		form         url.Values
		wantStatus   int
		wantResponse string
	}{
		{name: "start", method: http.MethodPost, path: "/api/tasks/" + task.ID + "/swarm/start", wantStatus: http.StatusOK, wantResponse: "planner_started"},
		{name: "followup", method: http.MethodPost, path: "/api/tasks/" + task.ID + "/swarm/followup", form: url.Values{"message": {"continue"}}, wantStatus: http.StatusBadRequest, wantResponse: "not part of a swarm"},
		{name: "cancel", method: http.MethodPost, path: "/api/tasks/" + task.ID + "/swarm/cancel", wantStatus: http.StatusOK, wantResponse: "cancelled"},
		{name: "rerun reviewer", method: http.MethodPost, path: "/api/tasks/" + task.ID + "/swarm/rerun-reviewer", wantStatus: http.StatusNotFound, wantResponse: "swarm role task not found"},
		{name: "rerun merger", method: http.MethodPost, path: "/api/tasks/" + task.ID + "/swarm/rerun-merger", wantStatus: http.StatusNotFound, wantResponse: "swarm role task not found"},
	} {
		t.Run(tcse.name, func(t *testing.T) {
			body := ""
			if tcse.form != nil {
				body = tcse.form.Encode()
			}
			req := httptest.NewRequest(tcse.method, tcse.path, strings.NewReader(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)
			require.Equal(t, tcse.wantStatus, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tcse.wantResponse)
		})
	}
}

func TestTaskBoardMutationRoutesValidateAndPersistExpectedState(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Task Board Mutation Contracts").Build()
	first := tc.CreateTask(project.ID).WithTitle("First backlog item").WithCategory(models.CategoryBacklog).Build()
	second := tc.CreateTask(project.ID).WithTitle("Second backlog item").WithCategory(models.CategoryBacklog).Build()
	completedActive := tc.CreateTask(project.ID).WithTitle("Finished active task").WithCategory(models.CategoryActive).Build()
	require.NoError(t, tc.taskRepo.UpdateStatus(context.Background(), completedActive.ID, models.StatusCompleted))

	invalidPriority := tc.HTTP().Post("/tasks/backlog/execute?project_id=" + project.ID + "&priority=9").Execute()
	require.Equal(t, http.StatusBadRequest, invalidPriority.Code)

	counts := tc.HTTP().Get("/tasks/backlog/priority-counts?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, counts.Code)
	require.Contains(t, counts.Body.String(), "2")

	move := tc.HTTP().Post("/tasks/move-completed?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusSeeOther, move.Code)
	moved, err := tc.taskRepo.GetByID(context.Background(), completedActive.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryCompleted, moved.Category)

	scheduledWithoutSchedule := tc.HTTP().Patch("/tasks/batch-category").WithForm(url.Values{
		"project_id": {project.ID},
		"task_ids":   {first.ID + "," + second.ID},
		"category":   {string(models.CategoryScheduled)},
	}).Execute()
	require.Equal(t, http.StatusBadRequest, scheduledWithoutSchedule.Code)
	require.Contains(t, scheduledWithoutSchedule.Body.String(), "no schedule")

	toCompleted := tc.HTTP().Patch("/tasks/batch-category").WithForm(url.Values{
		"project_id": {project.ID},
		"task_ids":   {first.ID + ", " + second.ID + ","},
		"category":   {string(models.CategoryCompleted)},
	}).Execute()
	require.Equal(t, http.StatusOK, toCompleted.Code)
	for _, id := range []string{first.ID, second.ID} {
		loaded, err := tc.taskRepo.GetByID(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, models.CategoryCompleted, loaded.Category)
	}

	invalidReorder := tc.HTTP().Patch("/tasks/" + first.ID + "/reorder").WithForm(url.Values{"position": {"not-a-number"}}).Execute()
	require.Equal(t, http.StatusBadRequest, invalidReorder.Code)
}

func TestUpdateTaskChainConfigCreatesAndRemovesBlockedChild(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Chain Route Contracts").Build()
	parent := tc.CreateTask(project.ID).WithTitle("Parent chain task").WithCategory(models.CategoryBacklog).Build()

	enable := tc.HTTP().Put("/tasks/" + parent.ID + "/chain").WithForm(url.Values{
		"chain_enabled":        {"true"},
		"chain_trigger":        {"on_completion"},
		"chain_child_model":    {"inherit"},
		"chain_child_category": {string(models.CategoryBacklog)},
	}).Execute()
	require.Equal(t, http.StatusOK, enable.Code)
	updated, err := tc.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	chain, err := updated.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, chain.Enabled)
	require.Equal(t, "on_completion", chain.Trigger)
	child, err := tc.taskRepo.FindBlockedChildByParent(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, child)
	require.Equal(t, models.StatusBlocked, child.Status)

	disable := tc.HTTP().Put("/tasks/" + parent.ID + "/chain").WithForm(url.Values{
		"chain_enabled": {"false"},
	}).Execute()
	require.Equal(t, http.StatusOK, disable.Code)
	child, err = tc.taskRepo.FindBlockedChildByParent(ctx, parent.ID)
	require.NoError(t, err)
	require.Nil(t, child)

	missing := tc.HTTP().Put("/tasks/missing/chain").WithForm(url.Values{"chain_enabled": {"true"}}).Execute()
	require.Equal(t, http.StatusNotFound, missing.Code)
}

func TestSummaryGenerationRoutesRequireProjectAndRenderServiceErrors(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Summary Route Contracts").Build()

	missingPulse := tc.HTTP().Post("/upcoming/summary").Execute()
	require.Equal(t, http.StatusBadRequest, missingPulse.Code)
	require.Contains(t, missingPulse.Body.String(), "missing project_id")
	missingReflection := tc.HTTP().Post("/history/summary").Execute()
	require.Equal(t, http.StatusBadRequest, missingReflection.Code)
	require.Contains(t, missingReflection.Body.String(), "missing project_id")

	pulse := tc.HTTP().Post("/upcoming/summary?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, pulse.Code)
	require.NotEmpty(t, pulse.Body.String())
	reflection := tc.HTTP().Post("/history/summary?project_id=" + project.ID + "&range=week").Execute()
	require.Equal(t, http.StatusOK, reflection.Code)
	require.NotEmpty(t, reflection.Body.String())
}

func TestTaskGoalRoutesReturnJSONLifecycle(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Goal Route Contracts").Build()
	task := tc.CreateTask(project.ID).WithTitle("Goal-backed task").WithCategory(models.CategoryBacklog).Build()

	getEmpty := requestWithAccept(tc, http.MethodGet, "/tasks/"+task.ID+"/goal", "application/json", "")
	require.Equal(t, http.StatusOK, getEmpty.Code)
	require.Contains(t, getEmpty.Body.String(), `"ok":true`)

	setEmpty := requestWithAccept(tc, http.MethodPost, "/tasks/"+task.ID+"/goal", "application/json", url.Values{"goal": {""}}.Encode())
	require.Equal(t, http.StatusBadRequest, setEmpty.Code)

	setGoal := requestWithAccept(tc, http.MethodPost, "/tasks/"+task.ID+"/goal", "application/json", url.Values{"goal": {"Ship reliable coverage"}}.Encode())
	require.Equal(t, http.StatusOK, setGoal.Code)
	require.Contains(t, setGoal.Body.String(), "Ship reliable coverage")

	pause := requestWithAccept(tc, http.MethodPost, "/tasks/"+task.ID+"/goal/pause", "application/json", "")
	require.Equal(t, http.StatusOK, pause.Code)
	resume := requestWithAccept(tc, http.MethodPost, "/tasks/"+task.ID+"/goal/resume", "application/json", "")
	require.Equal(t, http.StatusOK, resume.Code)
	clear := requestWithAccept(tc, http.MethodPost, "/tasks/"+task.ID+"/goal/clear", "application/json", "")
	require.Equal(t, http.StatusOK, clear.Code)

	missingPause := requestWithAccept(tc, http.MethodPost, "/tasks/missing/goal/pause", "application/json", "")
	require.Equal(t, http.StatusNotFound, missingPause.Code)
}

func TestQueuedInputRoutesValidateStaleAndMissingInputs(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Queued Input Route Contracts").Build()
	task := tc.CreateTask(project.ID).WithTitle("Queued input task").WithCategory(models.CategoryBacklog).Build()

	missingCancel := tc.HTTP().Post("/thread-inputs/missing/cancel").Execute()
	require.Equal(t, http.StatusOK, missingCancel.Code)
	require.Contains(t, missingCancel.Body.String(), "missing")

	chatSteer := tc.HTTP().Post("/chat/queued/missing/steer").Execute()
	require.Equal(t, http.StatusConflict, chatSteer.Code)
	require.Contains(t, chatSteer.Body.String(), "no longer pending")

	taskSteer := tc.HTTP().Post("/tasks/" + task.ID + "/thread/queued/missing/steer").Execute()
	require.Equal(t, http.StatusConflict, taskSteer.Code)
	require.Contains(t, taskSteer.Body.String(), "no longer pending")
}

func TestPluginMarketplaceRoutesDecodeCallHooksAndReturnJSON(t *testing.T) {
	tc := NewTestContext(t)
	origAdd := addMarketplaceFn
	origUpdate := updateMarketplaceFn
	origRemove := removeMarketplaceFn
	origReset := resetMarketplacesFn
	defer func() {
		addMarketplaceFn = origAdd
		updateMarketplaceFn = origUpdate
		removeMarketplaceFn = origRemove
		resetMarketplacesFn = origReset
	}()

	var addedSource, addedScope, updatedName, removedName string
	addMarketplaceFn = func(ctx context.Context, source, scope string) error {
		addedSource, addedScope = source, scope
		return nil
	}
	updateMarketplaceFn = func(ctx context.Context, name string) error {
		updatedName = name
		return nil
	}
	removeMarketplaceFn = func(ctx context.Context, name string) error {
		removedName = name
		return nil
	}
	resetCalled := false
	resetMarketplacesFn = func(ctx context.Context) error {
		resetCalled = true
		return nil
	}

	add := postJSON(tc, "/agents/plugins/marketplaces", `{"source":"https://example.com/marketplace.git","scope":"user"}`)
	require.Equal(t, http.StatusOK, add.Code, add.Body.String())
	require.Equal(t, "https://example.com/marketplace.git", addedSource)
	require.Equal(t, "user", addedScope)

	update := postJSON(tc, "/agents/plugins/marketplaces/community/update", ``)
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	require.Equal(t, "community", updatedName)

	remove := deleteJSON(tc, "/agents/plugins/marketplaces/community")
	require.Equal(t, http.StatusOK, remove.Code, remove.Body.String())
	require.Equal(t, "community", removedName)

	reset := postJSON(tc, "/agents/plugins/marketplaces/reset-defaults", ``)
	require.Equal(t, http.StatusOK, reset.Code, reset.Body.String())
	require.True(t, resetCalled)

	badJSON := postJSON(tc, "/agents/plugins/marketplaces", `{"source":"x","unexpected":true}`)
	require.Equal(t, http.StatusBadRequest, badJSON.Code)
	require.Contains(t, badJSON.Body.String(), "unknown field")

	addMarketplaceFn = func(ctx context.Context, source, scope string) error { return errors.New("remote unavailable") }
	failed := postJSON(tc, "/agents/plugins/marketplaces", `{"source":"https://example.com/bad.git","scope":"user"}`)
	require.Equal(t, http.StatusBadRequest, failed.Code)
	require.Contains(t, failed.Body.String(), "remote unavailable")
}

func TestGetAgentJSONReturnsAgentOrNotFound(t *testing.T) {
	tc := NewTestContext(t)
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)
	agent := &models.Agent{Name: "JSON Agent", Key: "json-agent", Model: "inherit", Scope: models.AgentScopeProject, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, agentRepo.Create(context.Background(), agent))

	found := tc.HTTP().Get("/agents/" + agent.ID + "/json").Execute()
	require.Equal(t, http.StatusOK, found.Code)
	require.Contains(t, found.Body.String(), "JSON Agent")

	missing := tc.HTTP().Get("/agents/missing/json").Execute()
	require.Equal(t, http.StatusNotFound, missing.Code)
}

func TestChatRuntimeSummaryHelpersUseRepositoriesAndPersistAlertActions(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Runtime Summary Project").Build()
	limited := 3
	project.MaxWorkers = &limited
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	task := tc.CreateTask(project.ID).WithTitle("Active runtime task").WithCategory(models.CategoryActive).Build()
	require.NoError(t, tc.workerRepo.SetMaxWorkers(ctx, 7))
	model := tc.CreateLLMConfig().WithName("Runtime Model").WithProvider(models.ProviderOpenAI).WithModel("gpt-test").WithAPIKey("key").Build()
	model.MaxWorkers = 2
	model.WorkerTimeout = 45
	require.NoError(t, tc.llmConfigRepo.Update(ctx, model))
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)
	require.NoError(t, agentRepo.Create(ctx, &models.Agent{Name: "Runtime Agent", Key: "runtime-agent", Description: "Does runtime work", Model: "gpt-test", Skills: []models.SkillConfig{{Name: "Audit"}}, MCPServers: []models.MCPServerConfig{{Name: "tools", Command: []string{"node"}}}, Scope: models.AgentScopeProject}))
	require.NoError(t, tc.settingsRepo.Set(ctx, "personality", "direct"))

	personalities := tc.handler.executeListPersonalities(ctx)
	require.Contains(t, personalities, "Available Personalities")
	require.Contains(t, personalities, "Current personality: **direct**")
	require.Contains(t, tc.handler.executeSetPersonality(ctx, service.SetPersonalityRequest{Personality: "no_nonsense_pro"}), "Personality changed")
	require.Contains(t, tc.handler.executeSetPersonality(ctx, service.SetPersonalityRequest{Personality: "does-not-exist"}), "Unknown personality")

	modelsOut := tc.handler.executeListModels(ctx)
	require.Contains(t, modelsOut, "Runtime Model")
	require.Contains(t, modelsOut, "max_workers: 2")
	require.Contains(t, tc.handler.executeListAgents(ctx), "Runtime Agent")
	settingsOut := tc.handler.executeViewSettings(ctx)
	require.Contains(t, settingsOut, "Global max workers:** 7")
	require.Contains(t, settingsOut, "Runtime Model")
	require.Contains(t, settingsOut, "max_workers=2")
	require.Contains(t, tc.handler.executeProjectInfo(ctx, project.ID), "Total tasks:** 1")
	require.Contains(t, tc.handler.executeListProjects(ctx, project.ID), "Runtime Summary Project")

	missingAlerts := (&Handler{}).executeListAlerts(ctx, project.ID)
	require.Contains(t, missingAlerts, "Alert service not available")
	createOut := tc.handler.executeCreateAlertRequests(ctx, project.ID, []service.CreateAlertRequest{
		{Title: "Runtime warning", Message: "needs attention", Severity: "warning", Type: "task_needs_followup", TaskID: task.ID},
		{Title: "Bad severity", Severity: "severe"},
		{Title: "Bad type", Type: "incident"},
	})
	require.Contains(t, createOut, "Created alert")
	require.Contains(t, createOut, "Invalid severity")
	require.Contains(t, createOut, "Invalid alert type")
	alerts, err := tc.alertRepo.ListByProject(ctx, project.ID, 10)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	listOut := tc.handler.executeListAlerts(ctx, project.ID)
	require.Contains(t, listOut, "Runtime warning")
	require.Contains(t, listOut, "unread")
	toggleOut := tc.handler.executeToggleAlertRequests(ctx, project.ID, []service.ToggleAlertRequest{{AlertID: alerts[0].ID}, {AlertID: "missing"}})
	require.Contains(t, toggleOut, "Marked alert")
	require.Contains(t, toggleOut, "Error marking alert")
	deleteOut := tc.handler.executeDeleteAlertRequests(ctx, project.ID, []service.DeleteAlertRequest{{AlertID: alerts[0].ID}, {AlertID: "missing"}})
	require.Contains(t, deleteOut, "Deleted alert")
	require.Contains(t, deleteOut, "Error deleting alert")
}

func TestChatActionReadHelpersUseRepositories(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Action Helper Project").Build()
	project.Description = "action helper description"
	project.RepoPath = t.TempDir()
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	model := tc.CreateLLMConfig().WithName("Action Model").WithProvider(models.ProviderAnthropic).WithModel("claude-test").WithAPIKey("key").Build()
	model.MaxWorkers = 4
	require.NoError(t, tc.llmConfigRepo.Update(ctx, model))
	alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityError, Title: "Action alert", Message: "inspect me"}
	require.NoError(t, tc.alertRepo.Create(ctx, alert))

	require.Contains(t, tc.handler.executeGetPersonality(ctx), "default")
	require.NoError(t, tc.settingsRepo.Set(ctx, "personality", "detailed"))
	require.Contains(t, tc.handler.executeGetPersonality(ctx), "detailed")
	require.Contains(t, tc.handler.executeGetModel(ctx, []byte(`{"model_id":"`+model.ID+`"}`)), "Action Model")
	require.Contains(t, tc.handler.executeGetModel(ctx, []byte(`{"name":"action model"}`)), "max_workers: 4")
	require.Contains(t, tc.handler.executeGetModel(ctx, []byte(`{"model_id":"missing"}`)), "not found")
	require.Contains(t, tc.handler.executeGetModel(ctx, []byte(`{"model_id":`)), "Invalid input")
	require.Contains(t, tc.handler.executeGetCurrentProject(ctx, project.ID), "action helper description")
	require.Contains(t, tc.handler.executeSwitchProject(ctx, project.ID, []byte(`{"project":"`+project.Name+`"}`)), "Switched to project")
	require.Contains(t, tc.handler.executeSwitchProject(ctx, project.ID, []byte(`{"project":"missing"}`)), "Available projects")
	require.Contains(t, tc.handler.executeSwitchProject(ctx, project.ID, []byte(`{}`)), "requires a project")
	require.Contains(t, tc.handler.executeGetAlert(ctx, project.ID, []byte(`{"alert_id":"`+alert.ID+`"}`)), "Action alert")
	require.Contains(t, tc.handler.executeGetAlert(ctx, project.ID, []byte(`{"alert_id":"missing"}`)), "not found")
	require.Contains(t, tc.handler.executeGetAlert(ctx, project.ID, []byte(`{}`)), "requires alert_id")
	require.Contains(t, (&Handler{}).executeGetAlert(ctx, project.ID, []byte(`{"alert_id":"`+alert.ID+`"}`)), "Alert service not available")
}

func TestChatActionRuntimeBuildersFiltersAndLineageHelpers(t *testing.T) {
	h := &Handler{}
	webRuntime := h.buildChatActionToolRuntime(streamingResponseParams{}, nil)
	require.True(t, runtimeToolDefinitionsInclude(webRuntime, "create_task"))
	require.False(t, runtimeToolDefinitionsInclude(webRuntime, "not_a_tool"))
	require.False(t, runtimeToolDefinitionsInclude(nil, "create_task"))

	apiRuntime := h.buildAPIChatActionToolRuntime(streamingResponseParams{ChatMode: models.ChatModePlan}, nil)
	require.NotEmpty(t, apiRuntime.Definitions)
	defs := chatActionToolDefinitions()
	require.NotEmpty(t, defs)
	fromDefs := h.buildChatActionToolRuntimeFromDefs(streamingResponseParams{}, nil, defs[:1], models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	require.Len(t, fromDefs.Definitions, 1)
	lifecycleRuntime := h.buildLifecycleChatActionToolRuntimeFromDefs(streamingResponseParams{}, nil, defs[:1], models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	require.Len(t, lifecycleRuntime.Definitions, 1)

	collector := newChatActionSummaryCollector()
	collector.addCreated("created\nTask One [TASK_ID:t1]\n- Task One [TASK_ID:t1]")
	collector.addEdited("Task One [TASK_EDITED:t1]")
	out := collector.appendToOutput("base")
	require.Contains(t, out, "Created 1 task(s)")
	require.Contains(t, out, "Edited 1 task(s)")
	require.Equal(t, out, collector.appendToOutput(out))
	require.Equal(t, "base", (*chatActionSummaryCollector)(nil).appendToOutput("base"))

	allowed := taskThreadAllowedRuntimeToolNames(&models.Agent{Tools: []string{" send_message ", "mark_task_goal_achieved", "report_task_goal_blocked", "ignored"}})
	for _, name := range []string{"send_message", "memory_view", "mark_task_goal_achieved", "report_task_goal_blocked"} {
		if name == "memory_view" {
			require.False(t, allowed[name])
			continue
		}
		require.True(t, allowed[name], name)
	}
	threadDefs := filterTaskThreadRuntimeToolDefs(defs, &models.Agent{Tools: []string{"send_message", "mark_task_goal_achieved"}}, true)
	threadRuntime := &llmcontracts.RuntimeTools{Definitions: threadDefs}
	require.True(t, runtimeToolDefinitionsInclude(threadRuntime, "memory_view"))
	require.True(t, runtimeToolDefinitionsInclude(threadRuntime, "mark_task_goal_achieved"))
	goalDefs := filterGoalAgentRuntimeToolDefs(defs)
	goalRuntime := &llmcontracts.RuntimeTools{Definitions: goalDefs}
	require.True(t, runtimeToolDefinitionsInclude(goalRuntime, "get_task_goal"))
	require.False(t, runtimeToolDefinitionsInclude(goalRuntime, "create_task"))

	summaries := []chatcontrol.ActionSummary{
		{Name: "create_task", Domain: "tasks", Description: "Create", Access: "write"},
		{Name: "get_model", Domain: "models", Description: "Read"},
		{Name: "memory_view", Domain: "memory", Description: "Read memory"},
		{Name: "mark_task_goal_achieved", Domain: "goal", Description: "Goal"},
	}
	filteredThread := filterTaskThreadCapabilitySummaries(summaries, &models.Agent{Tools: []string{"mark_task_goal_achieved"}}, true)
	require.Len(t, filteredThread, 3)
	filteredAssigned := filterAssignedAgentCapabilitySummaries(summaries, &models.Agent{})
	require.Len(t, filteredAssigned, len(summaries))
	require.JSONEq(t, `{"ok":true}`, mustGithubToolJSON(t, map[string]any{"ok": true}))

	require.NoError(t, requireGoalStatusToolGrant(lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{SystemKind: models.AgentSystemKindGoal}), streamingResponseParams{}, "mark_task_goal_achieved"))
	require.NoError(t, requireGoalStatusToolGrant(lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{Tools: []string{"report_task_goal_blocked"}}), streamingResponseParams{}, "report_task_goal_blocked"))
	require.Error(t, requireGoalStatusToolGrant(lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{Tools: []string{"other"}}), streamingResponseParams{}, "report_task_goal_blocked"))
	require.NoError(t, requireGoalStatusToolGrant(context.Background(), streamingResponseParams{AgentDefinition: &models.Agent{Tools: []string{"mark_task_goal_achieved"}}}, "mark_task_goal_achieved"))
	require.Error(t, requireGoalStatusToolGrant(context.Background(), streamingResponseParams{}, "mark_task_goal_achieved"))

	origin, agent := sanitizeSendToTaskLineage(lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{SystemKind: models.AgentSystemKindGoal}), "slack", "requested", streamingResponseParams{})
	require.Equal(t, models.TaskOriginSystemAgent, origin)
	require.Equal(t, models.AgentSystemKindGoal, agent)
	origin, agent = sanitizeSendToTaskLineage(context.Background(), "", "", streamingResponseParams{RuntimeOrigin: models.TaskOriginSystemAgent, RuntimeOriginAgent: "planner"})
	require.Equal(t, models.TaskOriginSystemAgent, origin)
	require.Equal(t, "planner", agent)
	origin, agent = sanitizeSendToTaskLineage(context.Background(), models.TaskOriginEmail, "ignored", streamingResponseParams{})
	require.Equal(t, models.TaskOriginEmail, origin)
	require.Empty(t, agent)
	origin, agent = sanitizeSendToTaskLineage(context.Background(), "unknown", "ignored", streamingResponseParams{})
	require.Equal(t, models.TaskOriginWeb, origin)
	require.Empty(t, agent)

	require.False(t, lifecycleHookHasNonCancellationExecutionError("context canceled"))
	require.True(t, lifecycleHookHasNonCancellationExecutionError("provider failed"))
	require.False(t, lifecycleHookMayContinueFromCancelledSource(lifecycle.HookAgent{TaskID: "source", ExecutionError: "task canceled"}, "source"))
	require.True(t, lifecycleHookMayContinueFromCancelledSource(lifecycle.HookAgent{TaskID: "source", ExecutionError: "provider failed"}, "source"))
}

func mustGithubToolJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	out, err := githubToolJSON(payload)
	require.NoError(t, err)
	return out
}

func TestChatActionRuntimeExecutorCoversToolClosuresAndValidation(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Runtime Executor Coverage").Build()
	task := tc.CreateTask(project.ID).WithTitle("Runtime executor task").WithCategory(models.CategoryBacklog).Build()
	tc.handler.SetGitHubAuthRepo(repository.NewGitHubAuthRepo(tc.db))
	model := tc.CreateLLMConfig().WithName("Executor Model").WithProvider(models.ProviderOpenAI).WithModel("gpt-test").WithAPIKey("key").Build()
	model.IsDefault = true
	require.NoError(t, tc.llmConfigRepo.Update(ctx, model))
	alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "Executor alert", Message: "runtime alert"}
	require.NoError(t, tc.alertRepo.Create(ctx, alert))

	collector := newChatActionSummaryCollector()
	rt := tc.handler.buildChatActionToolRuntime(streamingResponseParams{ExecID: "runtime-executor", ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}, collector)
	exec := func(name string, input string) (string, bool, bool, error) {
		return rt.Executor(ctx, name, json.RawMessage(input))
	}

	for _, call := range []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{name: "get_chat_mode", input: `{}`, want: "Current chat mode"},
		{name: "set_chat_mode", input: `{"mode":"plan"}`, want: "Chat mode set to plan"},
		{name: "list_capabilities", input: `{}`, want: "Available capabilities"},
		{name: "list_personalities", input: `{}`, want: "Available Personalities"},
		{name: "get_personality", input: `{}`, want: "Current personality"},
		{name: "set_personality", input: `{"personality":"no_nonsense_pro"}`, want: "Personality changed"},
		{name: "list_models", input: `{}`, want: "Executor Model"},
		{name: "get_model", input: `{"model_id":"` + model.ID + `"}`, want: "Executor Model"},
		{name: "list_agents", input: `{}`, want: "Configured Agents"},
		{name: "view_settings", input: `{}`, want: "App Settings"},
		{name: "list_channels", input: `{}`, want: `"outbound_message_targets"`},
		{name: "project_info", input: `{}`, want: "Runtime Executor Coverage"}, {name: "get_current_project", input: `{}`, want: "Current project"},
		{name: "list_projects", input: `{}`, want: "Runtime Executor Coverage"},
		{name: "switch_project", input: `{"project":"Runtime Executor Coverage"}`, want: "Switched to project"},
		{name: "list_tasks", input: `{}`, want: `"ok":true`},
		{name: "create_task", input: `{"title":"Created by runtime executor","prompt":"test prompt","category":"backlog"}`, want: "Created by runtime executor"},
		{name: "edit_task", input: `{"id":"` + task.ID + `","title":"Runtime executor task edited"}`, want: "Runtime executor task edited"},
		{name: "execute_tasks", input: `{"task_id":"missing"}`, want: "Failed:"},
		{name: "schedule_task", input: `{"task_id":"current","time":"09:45"}`, want: "Scheduled task"},
		{name: "list_schedules", input: `{}`, want: `"ok":true`},
		{name: "modify_schedule", input: `{"task_id":"current","enabled":false}`, want: "Updated schedule"},
		{name: "delete_schedule", input: `{"task_id":"current"}`, want: "Deleted schedule"},
		{name: "get_alert", input: `{"alert_id":"` + alert.ID + `"}`, want: "Executor alert"},
		{name: "list_alerts", input: `{}`, want: `"notifications"`},
		{name: "create_alert", input: `{"title":"Executor created alert","message":"created"}`, want: `"alert"`},
		{name: "toggle_alert", input: `{"alert_id":"` + alert.ID + `"}`, want: "Marked alert"},
		{name: "delete_alert", input: `{"alert_id":"` + alert.ID + `"}`, want: "Deleted alert"},
		{name: "set_task_goal", input: `{"task_id":"current","goal":"Finish coverage"}`, want: `"ok":true`},
		{name: "get_task_goal", input: `{"task_id":"current"}`, want: "Finish coverage"},
		{name: "pause_task_goal", input: `{"task_id":"current"}`, want: `"ok":true`},
		{name: "resume_task_goal", input: `{"task_id":"current"}`, want: `"ok":true`},
		{name: "clear_task_goal", input: `{"task_id":"current"}`, want: `"ok":true`},
		{name: "send_message", input: `{"target":"slack:#general","message":"hi"}`, wantError: true},
		{name: "github_create_issue", input: `{"title":"Issue"}`, wantError: true},
		{name: "github_get_issue", input: `{"issue_number":1}`, wantError: true},
		{name: "github_get_project_inbox", input: `{}`, want: `"ok":true`},
		{name: "github_is_actor_authorized", input: `{"github_login":"octocat"}`, want: `"authorized":false`},
		{name: "github_list_my_assigned_issues", input: `{}`, wantError: true},
		{name: "github_list_existing_automation_issues", input: `{}`, wantError: true},
		{name: "github_list_assigned_issues", input: `{"assignee":"octocat"}`, wantError: true},
		{name: "github_list_assigned_issues_with_prs", input: `{"assignee":"octocat"}`, wantError: true},
		{name: "github_comment_on_issue", input: `{"issue_number":1,"body":"comment"}`, wantError: true},
		{name: "github_add_issue_labels", input: `{"issue_number":1,"labels":["bug"]}`, wantError: true},
		{name: "github_close_issue", input: `{"issue_number":1}`, wantError: true},
		{name: "github_open_pull_request", input: `{"task_id":"current"}`, wantError: true},
		{name: "github_replace_pull_request_branch", input: `{"task_id":"current","expected_head_sha":"0123456789012345678901234567890123456789","confirm_history_rewrite":true}`, wantError: true},
		{name: "github_forward_pr_feedback_to_tasks", input: `{}`, wantError: true},
	} {
		t.Run(call.name, func(t *testing.T) {
			out, handled, isErr, err := exec(call.name, call.input)
			require.True(t, handled)
			if call.wantError {
				require.True(t, isErr, out)
				if err == nil {
					require.NotEmpty(t, out)
				}
				return
			}
			require.NoError(t, err)
			require.False(t, isErr, out)
			require.Contains(t, out, call.want)
		})
	}

	summary := collector.appendToOutput("")
	require.Contains(t, summary, "Created")
	require.Contains(t, summary, "Edited")
}

func TestTaskFormAndAgentSelectionHelpers(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Agent helper project").Build()
	otherProject := tc.CreateProject().WithName("Other helper project").Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)

	archivedAt := time.Now()
	agents := []models.Agent{
		{Name: "Global selectable", Key: "global-selectable", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true},
		{Name: "Project selectable", Key: "project-selectable", Scope: models.AgentScopeProject, ProjectID: project.ID, Enabled: true, SelectableAsPrimary: true},
		{Name: "Other project", Key: "other-project", Scope: models.AgentScopeProject, ProjectID: otherProject.ID, Enabled: true, SelectableAsPrimary: true},
		{Name: "Disabled", Key: "disabled", Scope: models.AgentScopeGlobal, Enabled: false, SelectableAsPrimary: true},
		{Name: "Not primary", Key: "not-primary", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: false},
		{Name: "Archived status", Key: "archived-status", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusArchived},
		{Name: "Archived timestamp", Key: "archived-time", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true, ArchivedAt: &archivedAt},
	}
	for i := range agents {
		require.NoError(t, agentRepo.Create(ctx, &agents[i]))
	}

	selected := selectableTaskAgentDefinitions(agents)
	require.Len(t, selected, 3)
	forProject := selectableTaskAgentDefinitionsForProject(agents, project.ID)
	require.Len(t, forProject, 2)
	currentOther := agents[2].ID
	formAgents := tc.handler.listTaskFormAgentDefinitions(ctx, project.ID, &currentOther)
	require.NotContains(t, agentIDs(formAgents), currentOther)
	require.Contains(t, agentIDs(formAgents), agents[0].ID)
	require.Contains(t, agentIDs(formAgents), agents[1].ID)
	currentProject := agents[1].ID
	formAgents = tc.handler.listTaskFormAgentDefinitions(ctx, project.ID, &currentProject)
	require.Contains(t, agentIDs(formAgents), currentProject)
	resolved, err := tc.handler.resolvePrimaryAgentDefinition(ctx, project.ID, agents[1].ID)
	require.NoError(t, err)
	require.Equal(t, agents[1].ID, *resolved)
	_, err = tc.handler.resolvePrimaryAgentDefinition(ctx, project.ID, agents[2].ID)
	require.Error(t, err)

	formContext := func(values url.Values) echo.Context {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		require.NoError(t, req.ParseForm())
		return tc.echo.NewContext(req, httptest.NewRecorder())
	}
	require.True(t, formBoolEnabled(formContext(url.Values{}), "missing", true))
	require.False(t, formBoolEnabled(formContext(url.Values{"flag": {"off", "false"}}), "flag", true))
	require.True(t, formBoolEnabled(formContext(url.Values{"flag": {"off", "yes"}}), "flag", false))
	require.False(t, swarmMergerEnabledFormValue(formContext(url.Values{"swarm_merger_enabled": {"false"}, "swarm_integrator_enabled": {"true"}})))
	require.True(t, swarmMergerEnabledFormValue(formContext(url.Values{"swarm_integrator_enabled": {"on"}})))
	require.True(t, isValidCompletedSort("completed_desc"))
	require.False(t, isValidCompletedSort("created_desc"))
}

func agentIDs(agents []models.Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.ID)
	}
	return ids
}

func requestWithAccept(tc *TestContext, method, path, accept, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	return rec
}

func postJSON(tc *TestContext, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	return rec
}

func deleteJSON(tc *TestContext, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	return rec
}
