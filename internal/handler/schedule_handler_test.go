package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// ---- CreateSchedule ----

func TestParseScheduleForm_ValidatesIntervalBeforeDate(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{
		"run_at":          {"not-a-date"},
		"repeat_interval": {"366"},
	}.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)

	_, err := parseScheduleForm(e.NewContext(req, httptest.NewRecorder()), models.RepeatDaily)
	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected interval HTTP error before date error, got %T: %v", err, err)
	}
	if httpErr.Code != http.StatusBadRequest || httpErr.Message != "repeat interval must be between 1 and 365" {
		t.Fatalf("unexpected interval error: %#v", httpErr)
	}
}

func assertSchedulesTaskDetailFragment(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, `id="task-detail-content"`) {
		t.Fatal("expected task-detail-content in HTMX response")
	}
	if !strings.Contains(body, `class="tab tab-active" data-tab="schedules"`) {
		t.Fatal("expected schedules tab to be active in HTMX response")
	}
}

func TestCreateSchedule_InvalidDate(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {"not-a-date"},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date, got %d", rec.Code)
	}
}

func TestCreateSchedule_RejectsOversizedIntervalWithoutPersistence(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()

	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {time.Now().Add(time.Hour).Format("2006-01-02T15:04")},
		"repeat_type":     {"seconds"},
		"repeat_interval": {"366"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized interval, got %d body=%s", rec.Code, rec.Body.String())
	}
	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected oversized schedule not to be persisted, got %d schedules", len(schedules))
	}
}

func TestScheduleMutationRoutesRejectForeignProjectContext(t *testing.T) {
	validScheduleForm := func(runAt time.Time) url.Values {
		return url.Values{
			"run_at":          {runAt.Format("2006-01-02T15:04")},
			"repeat_type":     {"daily"},
			"repeat_interval": {"1"},
		}
	}

	scheduleUnchanged := func(t *testing.T, got, want *models.Schedule) {
		t.Helper()
		if got == nil {
			t.Fatal("expected schedule to remain present")
		}
		if got.TaskID != want.TaskID || !got.RunAt.Equal(want.RunAt) || got.RepeatType != want.RepeatType || got.RepeatInterval != want.RepeatInterval || got.Enabled != want.Enabled || got.ClearContextOnStart != want.ClearContextOnStart {
			t.Fatalf("schedule changed unexpectedly: got=%+v want=%+v", got, want)
		}
		if (got.NextRun == nil) != (want.NextRun == nil) {
			t.Fatalf("next_run changed unexpectedly: got=%v want=%v", got.NextRun, want.NextRun)
		}
		if got.NextRun != nil && !got.NextRun.Equal(*want.NextRun) {
			t.Fatalf("next_run changed unexpectedly: got=%v want=%v", got.NextRun, want.NextRun)
		}
	}

	t.Run("create without agent assignment field", func(t *testing.T) {
		tc := NewTestContext(t)
		projectA := tc.CreateProject().WithName("Project A").Build()
		projectB := tc.CreateProject().WithName("Project B").Build()
		task := tc.CreateTask(projectA.ID).Build()

		rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule?project_id=" + projectB.ID).WithForm(validScheduleForm(time.Now().Add(time.Hour))).Execute()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
		}
		schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(schedules) != 0 {
			t.Fatalf("foreign create persisted %d schedules", len(schedules))
		}
	})

	for _, method := range []string{http.MethodPut, http.MethodPost} {
		t.Run("update "+method, func(t *testing.T) {
			tc := NewTestContext(t)
			projectA := tc.CreateProject().WithName("Project A").Build()
			projectB := tc.CreateProject().WithName("Project B").Build()
			task := tc.CreateTask(projectA.ID).Build()
			originalRunAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
			schedule := tc.CreateSchedule(task.ID).WithRunAt(originalRunAt).WithRepeatType(models.RepeatOnce).Build()
			before, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}

			path := "/schedules/" + schedule.ID + "?project_id=" + projectB.ID
			var rec *httptest.ResponseRecorder
			if method == http.MethodPut {
				rec = tc.HTTP().Put(path).WithForm(validScheduleForm(time.Now().Add(3 * time.Hour))).Execute()
			} else {
				rec = tc.HTTP().Post(path).WithForm(validScheduleForm(time.Now().Add(3 * time.Hour))).Execute()
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			after, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleUnchanged(t, after, before)
		})
	}

	for _, endpoint := range []struct {
		name               string
		path               func(string, string) string
		useSelectedProject bool
	}{
		{name: "browser toggle", path: func(scheduleID, projectID string) string {
			return "/schedules/" + scheduleID + "/toggle?project_id=" + projectID
		}},
		{name: "api toggle query", path: func(scheduleID, projectID string) string {
			return "/api/schedules/" + scheduleID + "/toggle?project_id=" + projectID
		}},
		{name: "api toggle selected project", useSelectedProject: true, path: func(scheduleID, projectID string) string {
			return "/api/schedules/" + scheduleID + "/toggle"
		}},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			tc := NewTestContext(t)
			projectA := tc.CreateProject().WithName("Project A").Build()
			projectB := tc.CreateProject().WithName("Project B").Build()
			task := tc.CreateTask(projectA.ID).Build()
			schedule := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()
			if endpoint.useSelectedProject {
				if err := tc.settingsRepo.Set(context.Background(), uiPreferenceSelectedProjectIDKey, projectB.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}

			rec := tc.HTTP().Post(endpoint.path(schedule.ID, projectB.ID)).Execute()
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			after, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduleUnchanged(t, after, before)
		})
	}

	t.Run("delete", func(t *testing.T) {
		tc := NewTestContext(t)
		projectA := tc.CreateProject().WithName("Project A").Build()
		projectB := tc.CreateProject().WithName("Project B").Build()
		task := tc.CreateTask(projectA.ID).Build()
		schedule := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()
		before, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}

		rec := tc.HTTP().Delete("/schedules/" + schedule.ID + "?project_id=" + projectB.ID).Execute()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
		}
		after, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}
		scheduleUnchanged(t, after, before)
	})

	t.Run("reschedule", func(t *testing.T) {
		tc := NewTestContext(t)
		projectA := tc.CreateProject().WithName("Project A").Build()
		projectB := tc.CreateProject().WithName("Project B").Build()
		task := tc.CreateTask(projectA.ID).Build()
		originalRunAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
		schedule := tc.CreateSchedule(task.ID).WithRunAt(originalRunAt).WithRepeatType(models.RepeatDaily).Build()
		before, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}

		rec := tc.HTMX().Patch("/schedules/" + schedule.ID + "/reschedule?project_id=" + projectB.ID).WithForm(url.Values{
			"new_date": {time.Now().AddDate(0, 0, 2).Format("2006-01-02")},
			"hour":     {"10"},
		}).Execute()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
		}
		after, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}
		scheduleUnchanged(t, after, before)
	})
}

func TestCreateAndUpdateSchedule_NativeRedirectParity(t *testing.T) {
	for _, tcse := range []struct {
		name            string
		operation       string
		explicitProject bool
	}{
		{name: "create explicit project", operation: "create", explicitProject: true},
		{name: "update explicit project", operation: "update", explicitProject: true},
		{name: "create inferred project", operation: "create"},
		{name: "update inferred project", operation: "update"},
	} {
		t.Run(tcse.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			task := tc.CreateTask(project.ID).Build()
			runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
			form := url.Values{
				"run_at":          {runAt},
				"repeat_type":     {"once"},
				"repeat_interval": {"1"},
			}
			projectQuery := ""
			if tcse.explicitProject {
				projectQuery = "?project_id=" + project.ID
			}

			var rec *httptest.ResponseRecorder
			if tcse.operation == "create" {
				rec = tc.HTTP().Post("/tasks/" + task.ID + "/schedule" + projectQuery).WithForm(form).Execute()
			} else {
				schedule := tc.CreateSchedule(task.ID).Build()
				rec = tc.HTTP().Put("/schedules/" + schedule.ID + projectQuery).WithForm(form).Execute()
			}

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected 303 redirect, got %d body=%s", rec.Code, rec.Body.String())
			}
			wantLocation := "/tasks/" + task.ID + "?tab=schedules&project_id=" + project.ID
			if location := rec.Header().Get("Location"); location != wantLocation {
				t.Fatalf("redirect location = %q, want %q", location, wantLocation)
			}
		})
	}
}

func TestCreateSchedule_Success_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}

	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if !schedules[0].Enabled {
		t.Fatal("new schedules must default Enabled to true")
	}
	if schedules[0].RepeatInterval != 1 {
		t.Fatalf("new schedules must default repeat interval to 1, got %d", schedules[0].RepeatInterval)
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("new schedules must default ClearContextOnStart to true")
	}
	if schedules[0].NextRun == nil || !schedules[0].NextRun.Equal(schedules[0].RunAt) {
		t.Fatalf("new schedules must start NextRun at RunAt, run_at=%v next_run=%v", schedules[0].RunAt, schedules[0].NextRun)
	}
}

func TestCreateSchedule_OneTimeResetsCompletedScheduledTask(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithCategory(models.CategoryScheduled).
		WithStatus(models.StatusCompleted).
		Build()

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d body=%s", rec.Code, rec.Body.String())
	}
	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one persisted schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatOnce || schedules[0].NextRun == nil {
		t.Fatalf("expected runnable one-time schedule, got repeat=%s next_run=%v", schedules[0].RepeatType, schedules[0].NextRun)
	}
	stored, err := tc.taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stored.Category != models.CategoryScheduled {
		t.Fatalf("category = %s, want %s", stored.Category, models.CategoryScheduled)
	}
	if stored.Status != models.StatusPending {
		t.Fatalf("status = %s, want %s", stored.Status, models.StatusPending)
	}
}

func TestCreateSchedule_HTMX_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTMX().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX create, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertSchedulesTaskDetailFragment(t, rec.Body.String())
}

func TestCreateSchedule_HTMX_UsesMainTaskDetailExecutionOrdering(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	agent := tc.CreateLLMConfig().Build()
	task := tc.CreateTask(project.ID).
		WithStatus(models.StatusCompleted).
		WithCategory(models.CategoryCompleted).
		Build()

	older := tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecCompleted).Build()
	if err := tc.execRepo.Complete(ctx, older.ID, models.ExecCompleted, "older output", "", 10, 1000); err != nil {
		t.Fatalf("complete older execution: %v", err)
	}
	newer := tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecCompleted).Build()
	if err := tc.execRepo.Complete(ctx, newer.ID, models.ExecCompleted, "newer output", "", 10, 5000); err != nil {
		t.Fatalf("complete newer execution: %v", err)
	}
	baseStartedAt := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := tc.db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, baseStartedAt, older.ID); err != nil {
		t.Fatalf("set older execution start: %v", err)
	}
	if _, err := tc.db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, baseStartedAt.Add(time.Hour), newer.ID); err != nil {
		t.Fatalf("set newer execution start: %v", err)
	}

	mainRec := tc.HTMX().Get("/tasks/" + task.ID).Execute()
	if mainRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX task detail, got %d body=%s", mainRec.Code, mainRec.Body.String())
	}
	if !strings.Contains(mainRec.Body.String(), ">5s</span>") {
		t.Fatalf("expected main task detail to show latest execution duration; body=%s", mainRec.Body.String())
	}
	if strings.Contains(mainRec.Body.String(), ">1s</span>") {
		t.Fatalf("main task detail showed older execution duration; body=%s", mainRec.Body.String())
	}

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	scheduleRec := tc.HTMX().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()
	if scheduleRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX create, got %d body=%s", scheduleRec.Code, scheduleRec.Body.String())
	}
	body := scheduleRec.Body.String()
	assertSchedulesTaskDetailFragment(t, body)
	if !strings.Contains(body, ">5s</span>") {
		t.Fatalf("expected schedule HTMX refresh to show latest execution duration like main task detail; body=%s", body)
	}
	if strings.Contains(body, ">1s</span>") {
		t.Fatalf("schedule HTMX refresh used non-chronological execution ordering; body=%s", body)
	}
}

func TestCreateSchedule_DefaultRepeatType(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_interval": {"1"},
		// omit repeat_type — should default to "once"
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}

	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatOnce {
		t.Errorf("expected RepeatOnce default, got %q", schedules[0].RepeatType)
	}
}

func createScheduleTestAgent(t *testing.T, repo *repository.AgentRepo, name string, scope models.AgentScope, projectID string, selectable bool) *models.Agent {
	t.Helper()
	agent := &models.Agent{
		Name:                name,
		Key:                 strings.ToLower(strings.ReplaceAll(name, " ", "-")),
		SystemPrompt:        "Act as " + name,
		Model:               "inherit",
		Scope:               scope,
		ProjectID:           projectID,
		Enabled:             true,
		SelectableAsPrimary: selectable,
		GeneratedStatus:     models.AgentStatusUserEdited,
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create agent %q: %v", name, err)
	}
	return agent
}

func TestCreateSchedule_AssignsPrimaryAgent(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	agent := createScheduleTestAgent(t, agentRepo, "Schedule Runner", models.AgentScopeProject, project.ID, true)

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule?project_id=" + project.ID).WithForm(url.Values{
		"run_at":                            {runAt},
		"repeat_type":                       {"once"},
		"repeat_interval":                   {"1"},
		"schedule_agent_definition_present": {"1"},
		"agent_definition_id":               {agent.ID},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}

	stored, err := tc.taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentDefinitionID == nil || *stored.AgentDefinitionID != agent.ID {
		t.Fatalf("primary agent = %v, want %s", stored.AgentDefinitionID, agent.ID)
	}
}

func TestCreateSchedule_ExplicitNoAgentClearsPrimaryAgent(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	agent := createScheduleTestAgent(t, agentRepo, "Existing Runner", models.AgentScopeGlobal, "", true)
	task := tc.CreateTask(project.ID).Build()
	if err := tc.taskRepo.UpdateAgentDefinition(context.Background(), task.ID, &agent.ID); err != nil {
		t.Fatal(err)
	}

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Post("/tasks/" + task.ID + "/schedule?project_id=" + project.ID).WithForm(url.Values{
		"run_at":                            {runAt},
		"repeat_type":                       {"once"},
		"repeat_interval":                   {"1"},
		"schedule_agent_definition_present": {"1"},
		"agent_definition_id":               {""},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := tc.taskRepo.GetByID(context.Background(), task.ID)
	if stored.AgentDefinitionID != nil {
		t.Fatalf("expected no primary agent, got %v", *stored.AgentDefinitionID)
	}
}

func TestUpdateSchedule_PersistsAndClearsPrimaryAgentWithoutChangingModel(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	model := tc.CreateLLMConfig().Build()
	taskBuilder := tc.CreateTask(project.ID)
	taskBuilder.task.AgentID = &model.ID
	task := taskBuilder.Build()
	schedule := tc.CreateSchedule(task.ID).Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	agent := createScheduleTestAgent(t, agentRepo, "Updated Runner", models.AgentScopeProject, project.ID, true)
	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")

	for _, agentID := range []string{agent.ID, ""} {
		rec := tc.HTTP().Put("/schedules/" + schedule.ID + "?project_id=" + project.ID).WithForm(url.Values{
			"run_at":                            {runAt},
			"repeat_type":                       {"daily"},
			"repeat_interval":                   {"1"},
			"schedule_agent_definition_present": {"1"},
			"agent_definition_id":               {agentID},
		}).Execute()
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("agent %q: expected 303, got %d body=%s", agentID, rec.Code, rec.Body.String())
		}
		stored, err := tc.taskRepo.GetByID(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.AgentID == nil || *stored.AgentID != model.ID {
			t.Fatalf("model assignment changed: got %v, want %s", stored.AgentID, model.ID)
		}
		if agentID == "" && stored.AgentDefinitionID != nil {
			t.Fatalf("expected no primary agent, got %v", *stored.AgentDefinitionID)
		}
		if agentID != "" && (stored.AgentDefinitionID == nil || *stored.AgentDefinitionID != agentID) {
			t.Fatalf("primary agent = %v, want %s", stored.AgentDefinitionID, agentID)
		}
	}
}

func TestUpdateSchedule_RejectsIneligibleAndCrossProjectAgents(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	otherProject := tc.CreateProject().WithName("Other Project").Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	ineligible := createScheduleTestAgent(t, agentRepo, "Maintenance Agent", models.AgentScopeGlobal, "", false)
	otherProjectAgent := createScheduleTestAgent(t, agentRepo, "Other Project Agent", models.AgentScopeProject, otherProject.ID, true)
	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")

	for _, agentID := range []string{ineligible.ID, otherProjectAgent.ID} {
		rec := tc.HTTP().Put("/schedules/" + schedule.ID + "?project_id=" + project.ID).WithForm(url.Values{
			"run_at":                            {runAt},
			"repeat_type":                       {"daily"},
			"repeat_interval":                   {"1"},
			"schedule_agent_definition_present": {"1"},
			"agent_definition_id":               {agentID},
		}).Execute()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("agent %s: expected 400, got %d body=%s", agentID, rec.Code, rec.Body.String())
		}
	}
	stored, _ := tc.taskRepo.GetByID(context.Background(), task.ID)
	if stored.AgentDefinitionID != nil {
		t.Fatalf("rejected assignment must not persist, got %v", *stored.AgentDefinitionID)
	}
}

func TestViewSchedule_PrimaryAgentOptionsAreEligibleAndProjectScoped(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	otherProject := tc.CreateProject().WithName("Other Project").Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	global := createScheduleTestAgent(t, agentRepo, "Global Runner", models.AgentScopeGlobal, "", true)
	projectAgent := createScheduleTestAgent(t, agentRepo, "Project Runner", models.AgentScopeProject, project.ID, true)
	ineligible := createScheduleTestAgent(t, agentRepo, "Protected Maintenance", models.AgentScopeGlobal, "", false)
	other := createScheduleTestAgent(t, agentRepo, "Other Runner", models.AgentScopeProject, otherProject.ID, true)
	_ = global
	_ = projectAgent

	rec := tc.HTTP().Get("/schedule?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`name="agent_id"`,
		`>Model</span>`,
		`name="agent_definition_id"`,
		`>Primary Agent</span>`,
		`>No Agent</option>`,
		global.Name,
		projectAgent.Name,
		`/tasks?project_id=` + project.ID + `&amp;from=schedule`,
		`action="/tasks?project_id=` + project.ID + `&amp;from=schedule"`,
		`grid grid-cols-1 gap-3 sm:grid-cols-2`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected schedule modal to contain %q", want)
		}
	}
	for _, excluded := range []string{ineligible.Name, other.Name} {
		if strings.Contains(body, excluded) {
			t.Fatalf("schedule modal exposed unavailable agent %q", excluded)
		}
	}
}

func TestCreateScheduledTask_NativeFormRedirectsToProjectSchedule(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	runAt := time.Now().Add(-2 * time.Minute).Format("2006-01-02T15:04")

	rec := tc.HTTP().Post("/tasks?project_id=" + project.ID + "&from=schedule").WithForm(url.Values{
		"title":           {"Native Scheduled Task"},
		"prompt":          {"Run later"},
		"category":        {"scheduled"},
		"priority":        {"2"},
		"run_at":          {runAt},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 from native schedule creation, got %d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/schedule?project_id="+project.ID {
		t.Fatalf("native redirect lost project scope: %q", location)
	}
	tasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("list tasks: count=%d err=%v", len(tasks), err)
	}
	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), tasks[0].ID)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("list schedules: count=%d err=%v", len(schedules), err)
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Fatalf("scheduled task repeat type = %q, want daily", schedules[0].RepeatType)
	}
	if schedules[0].RepeatInterval != 1 {
		t.Fatalf("scheduled task repeat interval = %d, want 1", schedules[0].RepeatInterval)
	}
	if !schedules[0].Enabled {
		t.Fatal("scheduled task schedule must default Enabled to true")
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("scheduled task schedule must default ClearContextOnStart to true")
	}
	if schedules[0].NextRun == nil || !schedules[0].NextRun.Equal(schedules[0].RunAt) {
		t.Fatalf("scheduled task schedule must start NextRun at RunAt, run_at=%v next_run=%v", schedules[0].RunAt, schedules[0].NextRun)
	}
}

func TestCreateScheduledTask_PersistsPrimaryAgentAndExplicitNoAgent(t *testing.T) {
	for _, tcse := range []struct {
		name        string
		assignAgent bool
	}{
		{name: "assigned", assignAgent: true},
		{name: "no agent", assignAgent: false},
	} {
		t.Run(tcse.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			agentRepo := repository.NewAgentRepo(tc.db)
			tc.handler.agentRepo = agentRepo
			agent := createScheduleTestAgent(t, agentRepo, "Scheduled Creator", models.AgentScopeProject, project.ID, true)
			agentID := ""
			if tcse.assignAgent {
				agentID = agent.ID
			}
			runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
			rec := tc.HTMX().Post("/tasks?project_id=" + project.ID + "&from=schedule").WithForm(url.Values{
				"title":               {"Scheduled Assignment " + tcse.name},
				"prompt":              {"Run later"},
				"category":            {"scheduled"},
				"priority":            {"2"},
				"run_at":              {runAt},
				"repeat_type":         {"daily"},
				"repeat_interval":     {"1"},
				"agent_definition_id": {agentID},
			}).Execute()
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			tasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
			if err != nil || len(tasks) != 1 {
				t.Fatalf("list tasks: count=%d err=%v", len(tasks), err)
			}
			if tcse.assignAgent && (tasks[0].AgentDefinitionID == nil || *tasks[0].AgentDefinitionID != agent.ID) {
				t.Fatalf("primary agent = %v, want %s", tasks[0].AgentDefinitionID, agent.ID)
			}
			if !tcse.assignAgent && tasks[0].AgentDefinitionID != nil {
				t.Fatalf("expected explicit no-agent assignment, got %v", *tasks[0].AgentDefinitionID)
			}
		})
	}
}

// ---- UpdateSchedule ----

func TestUpdateSchedule_NotFound(t *testing.T) {
	tc := NewTestContext(t)

	runAt := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Put("/schedules/nonexistent-id").WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUpdateSchedule_InvalidDate(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTTP().Put("/schedules/" + schedule.ID).WithForm(url.Values{
		"run_at":          {"bad-date"},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateSchedule_RejectsOversizedIntervalWithoutPersistence(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).WithRepeatType(models.RepeatDaily).WithRepeatInterval(2).Build()
	originalRunAt := schedule.RunAt

	rec := tc.HTTP().Put("/schedules/" + schedule.ID).WithForm(url.Values{
		"run_at":          {time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")},
		"repeat_type":     {"hours"},
		"repeat_interval": {"366"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized interval, got %d body=%s", rec.Code, rec.Body.String())
	}
	persisted, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RepeatInterval != 2 || persisted.RepeatType != models.RepeatDaily || !persisted.RunAt.Equal(originalRunAt) {
		t.Fatalf("oversized update changed persisted schedule: %+v", persisted)
	}
}

func TestUpdateSchedule_ClearsCancellationRequestWhenResettingScheduledTaskPending(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithCategory(models.CategoryScheduled).
		WithStatus(models.StatusCancelled).
		Build()
	schedule := tc.CreateSchedule(task.ID).Build()
	tc.handler.workerSvc.MarkCancellationRequested(task.ID)
	if !tc.handler.workerSvc.IsCancellationRequested(task.ID) {
		t.Fatal("expected test setup cancellation marker")
	}

	rec := tc.HTTP().Put("/schedules/" + schedule.ID).WithForm(url.Values{
		"run_at":          {time.Now().Add(time.Hour).Format("2006-01-02T15:04")},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d body=%s", rec.Code, rec.Body.String())
	}
	if tc.handler.workerSvc.IsCancellationRequested(task.ID) {
		t.Fatal("schedule edit pending reset should clear stale cancellation marker")
	}
	updated, err := tc.taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.StatusPending {
		t.Fatalf("status=%s, want pending", updated.Status)
	}
}

func TestUpdateSchedule_Success_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTTP().Put("/schedules/" + schedule.ID).WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"once"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

func TestUpdateSchedule_HTMX_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")
	rec := tc.HTMX().Put("/schedules/" + schedule.ID).WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX update, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertSchedulesTaskDetailFragment(t, rec.Body.String())
}

func TestUpdateSchedule_WithoutAgentFieldPreservesExistingAssignment(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	protected := createScheduleTestAgent(t, agentRepo, "Protected Scheduled Maintenance", models.AgentScopeGlobal, "", false)
	task := tc.CreateTask(project.ID).Build()
	if err := tc.taskRepo.UpdateAgentDefinition(context.Background(), task.ID, &protected.ID); err != nil {
		t.Fatal(err)
	}
	schedule := tc.CreateSchedule(task.ID).Build()
	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")

	rec := tc.HTTP().Put("/schedules/" + schedule.ID + "?project_id=" + project.ID).WithForm(url.Values{
		"run_at":          {runAt},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := tc.taskRepo.GetByID(context.Background(), task.ID)
	if stored.AgentDefinitionID == nil || *stored.AgentDefinitionID != protected.ID {
		t.Fatalf("timing-only update changed protected assignment: got %v, want %s", stored.AgentDefinitionID, protected.ID)
	}
}

func TestUpdateSchedule_HTMXResponseHydratesSavedPrimaryAgent(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	agent := createScheduleTestAgent(t, agentRepo, "HTMX Scheduled Runner", models.AgentScopeProject, project.ID, true)
	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")

	rec := tc.HTMX().Put("/schedules/" + schedule.ID + "?project_id=" + project.ID).WithForm(url.Values{
		"run_at":                            {runAt},
		"repeat_type":                       {"daily"},
		"repeat_interval":                   {"1"},
		"schedule_agent_definition_present": {"1"},
		"agent_definition_id":               {agent.ID},
	}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="`+agent.ID+`" selected`) {
		t.Fatalf("HTMX response did not hydrate saved primary agent %s", agent.ID)
	}
	if !strings.Contains(body, `/schedules/`+schedule.ID+`?project_id=`+project.ID) {
		t.Fatal("HTMX response lost project-scoped schedule edit URL")
	}
}

func TestUpdateSchedule_NativeFormMethodOverridePersistsPrimaryAgent(t *testing.T) {
	tc := NewTestContext(t)
	tc.echo.Pre(middleware.MethodOverride())
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.agentRepo = agentRepo
	agent := createScheduleTestAgent(t, agentRepo, "Native Scheduled Runner", models.AgentScopeProject, project.ID, true)
	runAt := time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")

	rec := tc.HTTP().Post("/schedules/" + schedule.ID + "?project_id=" + project.ID).WithForm(url.Values{
		"_method":                           {"PUT"},
		"run_at":                            {runAt},
		"repeat_type":                       {"daily"},
		"repeat_interval":                   {"1"},
		"schedule_agent_definition_present": {"1"},
		"agent_definition_id":               {agent.ID},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 from native form fallback, got %d body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := tc.taskRepo.GetByID(context.Background(), task.ID)
	if stored.AgentDefinitionID == nil || *stored.AgentDefinitionID != agent.ID {
		t.Fatalf("native form primary agent = %v, want %s", stored.AgentDefinitionID, agent.ID)
	}
	if location := rec.Header().Get("Location"); !strings.Contains(location, "project_id="+project.ID) || !strings.Contains(location, "tab=schedules") {
		t.Fatalf("native redirect lost project/schedule context: %q", location)
	}
}

// ---- DeleteSchedule ----

func TestDeleteSchedule_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTMX().Delete("/schedules/" + schedule.ID).Execute()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX delete, got %d", rec.Code)
	}
}

func TestDeleteSchedule_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTTP().Delete("/schedules/" + schedule.ID).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for non-HTMX delete, got %d", rec.Code)
	}
}

func TestDeleteSchedule_BrowserDeletePreservesScheduledTaskCategory(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithCategory(models.CategoryScheduled).
		WithStatus(models.StatusPending).
		Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTTP().Delete("/schedules/" + schedule.ID).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for non-HTMX delete, got %d", rec.Code)
	}
	stored, err := tc.taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Category != models.CategoryScheduled {
		t.Fatalf("browser delete changed category to %s, want %s", stored.Category, models.CategoryScheduled)
	}
	schedules, err := tc.scheduleRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected browser delete to remove schedule row, got %d", len(schedules))
	}
}

// ---- RescheduleTask ----

func TestRescheduleTask_InvalidDate(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTMX().Patch("/schedules/" + schedule.ID + "/reschedule").WithForm(url.Values{
		"new_date": {"not-a-date"},
		"hour":     {"10"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid date, got %d", rec.Code)
	}
}

func TestRescheduleTask_InvalidHour(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).Build()

	rec := tc.HTMX().Patch("/schedules/" + schedule.ID + "/reschedule").WithForm(url.Values{
		"new_date": {time.Now().AddDate(0, 0, 1).Format("2006-01-02")},
		"hour":     {"99"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid hour, got %d", rec.Code)
	}
}

func TestRescheduleTask_NotFound(t *testing.T) {
	tc := NewTestContext(t)

	rec := tc.HTMX().Patch("/schedules/nonexistent/reschedule").WithForm(url.Values{
		"new_date": {time.Now().AddDate(0, 0, 1).Format("2006-01-02")},
		"hour":     {"10"},
	}).Execute()

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRescheduleTask_Success_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).WithRepeatType(models.RepeatDaily).Build()

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	rec := tc.HTMX().Patch("/schedules/" + schedule.ID + "/reschedule").WithForm(url.Values{
		"new_date": {tomorrow},
		"hour":     {"10"},
	}).Execute()

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRescheduleTask_Success_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	schedule := tc.CreateSchedule(task.ID).WithRepeatType(models.RepeatDaily).Build()

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	rec := tc.HTTP().Patch("/schedules/" + schedule.ID + "/reschedule").WithForm(url.Values{
		"new_date": {tomorrow},
		"hour":     {"10"},
	}).Execute()

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

// ---- GetExecution ----

func TestGetExecution_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/executions/nonexistent").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetExecution_Success(t *testing.T) {
	tc := NewTestContext(t)
	agent := tc.CreateLLMConfig().Build()
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	exec := tc.CreateExecution(task.ID, agent.ID).WithStatus(models.ExecCompleted).WithOutput("done").Build()

	rec := tc.HTTP().Get("/executions/" + exec.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- WorkerSettings ----

func TestWorkerSettings_NonHTMX(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/workers").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	tc.Assert(rec).Contains("max_workers")
}

func TestWorkerSettings_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Get("/workers").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- UpdateWorkerSettings ----

func TestUpdateWorkerSettings_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Post("/workers").WithForm(url.Values{
		"max_workers": {"3"},
	}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX update, got %d", rec.Code)
	}
}

func TestUpdateWorkerSettings_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/workers").WithForm(url.Values{
		"max_workers": {"2"},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for non-HTMX update, got %d", rec.Code)
	}
}

func TestUpdateWorkerSettings_Clamps(t *testing.T) {
	tc := NewTestContext(t)
	// Value of 99 should be clamped to 10
	rec := tc.HTTP().Post("/workers").WithForm(url.Values{
		"max_workers": {"99"},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

// ---- UpdateProjectWorkerLimit ----

func TestUpdateProjectWorkerLimit_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Post("/workers/projects/nonexistent/limit").WithForm(url.Values{
		"max_workers": {"2"},
	}).Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUpdateProjectWorkerLimit_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()

	rec := tc.HTMX().Post("/workers/projects/" + project.ID + "/limit").WithForm(url.Values{
		"max_workers": {"3"},
	}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateProjectWorkerLimit_Zero_RemovesLimit(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()

	rec := tc.HTMX().Post("/workers/projects/" + project.ID + "/limit").WithForm(url.Values{
		"max_workers": {"0"},
	}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- GlobalWorkerStats / ProjectWorkerStats / ModelWorkerStats ----

func TestGlobalWorkerStats(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/workers/stats/global").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectWorkerStats(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().Build()
	rec := tc.HTTP().Get("/workers/stats/projects").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProjectCapacityEndpointsPreserveCountsAndTableShape(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	limit := 2
	project := tc.CreateProject().WithName("Capacity Endpoint Project").Build()
	project.MaxWorkers = &limit
	if err := tc.projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("update project worker limit: %v", err)
	}
	tc.handler.workerSvc.SetProjectRepo(tc.projectRepo)

	tc.CreateTask(project.ID).WithTitle("Capacity active pending").WithCategory(models.CategoryActive).WithStatus(models.StatusPending).Build()
	tc.CreateTask(project.ID).WithTitle("Capacity active queued").WithCategory(models.CategoryActive).WithStatus(models.StatusQueued).Build()
	tc.CreateTask(project.ID).WithTitle("Capacity active completed").WithCategory(models.CategoryActive).WithStatus(models.StatusCompleted).Build()
	tc.CreateTask(project.ID).WithTitle("Capacity backlog pending").WithCategory(models.CategoryBacklog).WithStatus(models.StatusPending).Build()
	if !tc.handler.workerSvc.TryAcquireProjectSlot(project.ID) {
		t.Fatal("expected project worker slot acquisition")
	}
	defer tc.handler.workerSvc.ReleaseProjectSlot(project.ID)

	tableRec := tc.HTTP().Get("/workers/stats/projects").Execute()
	if tableRec.Code != http.StatusOK {
		t.Fatalf("expected workers stats 200, got %d body=%s", tableRec.Code, tableRec.Body.String())
	}
	table := tableRec.Body.String()
	for _, want := range []string{
		`id="project-stats-tbody"`,
		`hx-get="/workers/stats/projects"`,
		`id="project-row-` + project.ID + `"`,
		`Capacity Endpoint Project`,
		`id="limit-input-` + project.ID + `"`,
		`value="2"`,
	} {
		if !strings.Contains(table, want) {
			t.Fatalf("workers project stats table missing %q in body:\n%s", want, table)
		}
	}

	allRec := tc.HTTP().Get("/api/capacity/projects").Execute()
	if allRec.Code != http.StatusOK {
		t.Fatalf("expected project capacities 200, got %d body=%s", allRec.Code, allRec.Body.String())
	}
	var all []ProjectCapacityResponse
	if err := json.NewDecoder(allRec.Body).Decode(&all); err != nil {
		t.Fatalf("decode project capacities: %v", err)
	}
	var fromAll *ProjectCapacityResponse
	for i := range all {
		if all[i].ID == project.ID {
			fromAll = &all[i]
			break
		}
	}
	if fromAll == nil {
		t.Fatalf("project %s missing from all capacity response: %#v", project.ID, all)
	}
	if fromAll.QueueSize != 2 || fromAll.Running != 1 || fromAll.MaxWorkers == nil || *fromAll.MaxWorkers != 2 || !fromAll.HasCapacity || fromAll.AvailableSlots == nil || *fromAll.AvailableSlots != 1 {
		t.Fatalf("all-project capacity response = %#v, want queue=2 running=1 max=2 available=1 has_capacity=true", *fromAll)
	}

	singleRec := tc.HTTP().Get("/api/capacity/projects/" + project.ID).Execute()
	if singleRec.Code != http.StatusOK {
		t.Fatalf("expected single project capacity 200, got %d body=%s", singleRec.Code, singleRec.Body.String())
	}
	var single ProjectCapacityResponse
	if err := json.NewDecoder(singleRec.Body).Decode(&single); err != nil {
		t.Fatalf("decode single project capacity: %v", err)
	}
	if single.ID != fromAll.ID || single.QueueSize != fromAll.QueueSize || single.Running != fromAll.Running || single.MaxWorkers == nil || *single.MaxWorkers != *fromAll.MaxWorkers || single.AvailableSlots == nil || *single.AvailableSlots != *fromAll.AvailableSlots || single.HasCapacity != fromAll.HasCapacity {
		t.Fatalf("single capacity response = %#v, want fields matching all-project response %#v", single, *fromAll)
	}
}

func TestModelWorkerStats(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/workers/stats/models").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- Capacity API ----

func TestGetGlobalCapacity(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/capacity/global").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp GlobalCapacityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestGetProjectCapacities(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().WithName("Cap Test Project").Build()

	rec := tc.HTTP().Get("/api/capacity/projects").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp []ProjectCapacityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) == 0 {
		t.Errorf("expected at least 1 project in capacity response")
	}
}

func TestGetProjectCapacity_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/capacity/projects/nonexistent").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetProjectCapacity_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()

	rec := tc.HTTP().Get("/api/capacity/projects/" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp ProjectCapacityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != project.ID {
		t.Errorf("expected project ID %q, got %q", project.ID, resp.ID)
	}
}

func TestGetModelCapacities(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/capacity/models").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestGetModelCapacity_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/capacity/models/nonexistent").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ---- ToggleScheduleEnabled ----

func TestToggleScheduleEnabled_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/schedules/nonexistent/toggle").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestToggleScheduleEnabled_DisableAndEnable(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	future := time.Now().Add(time.Hour)
	s := tc.CreateSchedule(task.ID).WithRunAt(future).WithRepeatType("daily").Build()

	if !s.Enabled {
		t.Fatal("expected schedule to start enabled")
	}

	// Disable
	rec := tc.HTTP().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	got, _ := tc.scheduleRepo.GetByID(context.Background(), s.ID)
	if got.Enabled {
		t.Error("expected schedule to be disabled")
	}

	// Re-enable
	rec = tc.HTTP().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	got, _ = tc.scheduleRepo.GetByID(context.Background(), s.ID)
	if !got.Enabled {
		t.Error("expected schedule to be enabled")
	}
}

func TestToggleScheduleEnabled_HTMX_Returns200(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	s := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()

	rec := tc.HTMX().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 HTMX response, got %d", rec.Code)
	}
	assertSchedulesTaskDetailFragment(t, rec.Body.String())
}

func TestToggleScheduleEnabled_NonHTMX_RedirectContainsTaskID(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	s := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()

	rec := tc.HTTP().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	expected := "/tasks/" + task.ID
	if loc != expected {
		t.Errorf("expected redirect to %q, got %q", expected, loc)
	}
}

func TestToggleScheduleEnabled_StaleNextRunRecomputed(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	// Schedule started yesterday and is daily
	past := time.Now().Add(-25 * time.Hour)
	s := tc.CreateSchedule(task.ID).WithRunAt(past).WithRepeatType("daily").Disabled().Build()

	rec := tc.HTTP().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	got, _ := tc.scheduleRepo.GetByID(context.Background(), s.ID)
	if !got.Enabled {
		t.Error("expected schedule to be re-enabled")
	}
	if got.NextRun == nil {
		t.Fatal("expected NextRun to be set after re-enable")
	}
	if !got.NextRun.After(time.Now()) {
		t.Errorf("expected NextRun to be in the future after re-enable, got %v", got.NextRun)
	}
}

func TestToggleScheduleEnabled_DisabledExcludedFromListDue(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	past := time.Now().Add(-time.Hour)
	s := tc.CreateSchedule(task.ID).WithRunAt(past).Build()

	// Confirm it's due while enabled
	due, _ := tc.scheduleRepo.ListDue(context.Background(), time.Now())
	found := false
	for _, d := range due {
		if d.ID == s.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected schedule to be due when enabled")
	}

	// Disable via toggle
	rec := tc.HTTP().Post("/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}

	// Confirm it's no longer due
	due, _ = tc.scheduleRepo.ListDue(context.Background(), time.Now())
	for _, d := range due {
		if d.ID == s.ID {
			t.Error("expected disabled schedule to be excluded from ListDue")
		}
	}
}

func TestScheduleToggleEndpoints_StaleNextRunParity(t *testing.T) {
	endpoints := []struct {
		name       string
		path       func(string) string
		statusCode int
	}{
		{
			name:       "browser",
			path:       func(id string) string { return "/schedules/" + id + "/toggle" },
			statusCode: http.StatusSeeOther,
		},
		{
			name:       "API",
			path:       func(id string) string { return "/api/schedules/" + id + "/toggle" },
			statusCode: http.StatusOK,
		},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			task := tc.CreateTask(project.ID).Build()
			stale := time.Now().Add(-25 * time.Hour)
			schedule := tc.CreateSchedule(task.ID).WithRunAt(stale).WithRepeatType("daily").Build()

			rec := tc.HTTP().Post(endpoint.path(schedule.ID)).Execute()
			if rec.Code != endpoint.statusCode {
				t.Fatalf("disable: expected %d, got %d (body: %s)", endpoint.statusCode, rec.Code, rec.Body.String())
			}
			disabled, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatalf("disable: get schedule: %v", err)
			}
			if disabled.Enabled {
				t.Fatal("disable: expected schedule to be disabled")
			}
			if disabled.NextRun == nil || !disabled.NextRun.Equal(stale) {
				t.Fatalf("disable: expected stale NextRun %v to be preserved, got %v", stale, disabled.NextRun)
			}

			rec = tc.HTTP().Post(endpoint.path(schedule.ID)).Execute()
			if rec.Code != endpoint.statusCode {
				t.Fatalf("re-enable: expected %d, got %d (body: %s)", endpoint.statusCode, rec.Code, rec.Body.String())
			}
			reEnabled, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
			if err != nil {
				t.Fatalf("re-enable: get schedule: %v", err)
			}
			if !reEnabled.Enabled {
				t.Fatal("re-enable: expected schedule to be enabled")
			}
			if reEnabled.NextRun == nil || !reEnabled.NextRun.After(time.Now()) {
				t.Fatalf("re-enable: expected NextRun to be recomputed into the future, got %v", reEnabled.NextRun)
			}
		})
	}
}

// ---- APIToggleScheduleEnabled ----

func TestAPIToggleScheduleEnabled_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/api/schedules/nonexistent/toggle").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAPIToggleScheduleEnabled_RoundTrip(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	s := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(time.Hour)).Build()

	// Toggle to disabled
	rec := tc.HTTP().Post("/api/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", resp["enabled"])
	}

	// Toggle back to enabled
	rec = tc.HTTP().Post("/api/schedules/" + s.ID + "/toggle").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	resp = nil
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", resp["enabled"])
	}
}
