package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
