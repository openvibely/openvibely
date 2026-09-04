package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/pages"
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

func TestViewSchedule_UsesCompactAgentScheduleProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	project := createProject(t, h, "Schedule Agent Projection")
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	agent := &models.Agent{
		Name:                "Rich Schedule Runner",
		SystemPrompt:        strings.Repeat("schedule agent prompt ", 512),
		Model:               "sonnet",
		Tools:               []string{"Read", "Write"},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}},
		Plugins:             []string{"github@marketplace"},
		MCPServers:          []models.MCPServerConfig{{Name: "playwright", Command: []string{"npx", "server"}}},
		Skills:              []models.SkillConfig{{Name: "schedule", Content: strings.Repeat("skill content ", 256)}},
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", MaxTokens: 8192},
		SourceRefs:          []string{"agents/schedule/SKILLS.md"},
		Scope:               models.AgentScopeGlobal,
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := agentRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create rich schedule agent: %v", err)
	}

	for _, tc := range []struct {
		name string
		htmx bool
		path string
	}{
		{name: "full page", path: "/schedule?project_id=" + project.ID},
		{name: "HTMX week navigation", htmx: true, path: "/schedule?project_id=" + project.ID + "&week=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.htmx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			counter.SetEnabled(false)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, expected := range []string{agent.Name, "(" + agent.Model + ")", `>No Agent</option>`} {
				if !strings.Contains(body, expected) {
					t.Fatalf("schedule response missing %q", expected)
				}
			}

			var agentQueries []string
			for _, statement := range counter.Statements() {
				normalized := strings.Join(strings.Fields(statement), " ")
				if strings.Contains(strings.ToLower(normalized), "from agents") {
					agentQueries = append(agentQueries, normalized)
				}
			}
			if len(agentQueries) != 1 {
				t.Fatalf("expected exactly one Schedule agent query, got %d in %q", len(agentQueries), counter.Statements())
			}
			query := strings.ToLower(agentQueries[0])
			projection := strings.Split(query, " from agents ")[0]
			if projection != "select id, name, model" {
				t.Fatalf("Schedule agent query projection = %q, want only selector fields: %s", projection, agentQueries[0])
			}
			for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_by", "absorbed_into", "created_at", "updated_at"} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("Schedule agent query selected forbidden column %q: %s", forbidden, agentQueries[0])
				}
			}
			for _, requiredPredicate := range []string{"coalesce(generated_status, 'user_edited') <> 'archived'", "archived_at is null", "coalesce(enabled, 1) = 1", "coalesce(selectable_as_primary, 1) = 1", "coalesce(scope, 'global') <> 'project'", "order by name asc"} {
				if !strings.Contains(query, requiredPredicate) {
					t.Fatalf("Schedule agent query is missing predicate/order %q: %s", requiredPredicate, agentQueries[0])
				}
			}
		})
	}

	full, err := agentRepo.GetByID(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("get full schedule agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Tools) == 0 || len(full.ToolConfig.ScopedFiles) == 0 || len(full.Plugins) == 0 || len(full.MCPServers) == 0 || len(full.Skills) == 0 || len(full.SourceRefs) == 0 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func TestCreateScheduledTaskFromScheduleUsesCompactAgentProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	project := createProject(t, h, "Schedule Create Projection")
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	agent := createScheduleTestAgent(t, agentRepo, "Schedule Create Runner", models.AgentScopeGlobal, "", true)
	agent.Model = "opus"
	if err := agentRepo.Update(context.Background(), agent); err != nil {
		t.Fatalf("update schedule create agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=schedule&week=2", strings.NewReader(url.Values{
		"title":           {"Schedule Create Projection Task"},
		"prompt":          {"Run the scheduled task"},
		"category":        {"scheduled"},
		"priority":        {"2"},
		"run_at":          {time.Now().Add(time.Hour).Format("2006-01-02T15:04")},
		"repeat_type":     {"daily"},
		"repeat_interval": {"1"},
	}.Encode()))
	req.Header.Set("HX-Request", "true")
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	counter.SetEnabled(false)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), agent.Name) || !strings.Contains(rec.Body.String(), "("+agent.Model+")") {
		t.Fatalf("schedule create refresh omitted the selected Agent option: %s", rec.Body.String())
	}

	var agentQueries []string
	for _, statement := range counter.Statements() {
		normalized := strings.Join(strings.Fields(statement), " ")
		if strings.Contains(strings.ToLower(normalized), "from agents") {
			agentQueries = append(agentQueries, normalized)
		}
	}
	if len(agentQueries) != 1 {
		t.Fatalf("expected exactly one compact Agent query during schedule create refresh, got %d in %q", len(agentQueries), counter.Statements())
	}
	query := strings.ToLower(agentQueries[0])
	projection := strings.Split(query, " from agents ")[0]
	if projection != "select id, name, model" {
		t.Fatalf("schedule create query projection = %q, want only selector fields: %s", projection, agentQueries[0])
	}
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("schedule create query selected forbidden column %q: %s", forbidden, agentQueries[0])
		}
	}
}

func BenchmarkScheduleAgentOptionProjectionAndContent(b *testing.B) {
	db := testutil.NewTestDB(b)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Schedule Benchmark Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		b.Fatalf("create benchmark project: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		b.Fatalf("clear benchmark agents: %v", err)
	}
	for i := 0; i < 1000; i++ {
		agent := &models.Agent{
			Name:                fmt.Sprintf("Schedule Agent %04d", i),
			SystemPrompt:        strings.Repeat("large schedule benchmark prompt with instructions and examples. ", 320),
			Model:               "inherit",
			Tools:               []string{"Read", "Write", "Edit", "Bash", models.AgentToolScopedFiles},
			ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read", "write"}}}},
			Plugins:             []string{"github@marketplace", "playwright@claude-plugins-official"},
			MCPServers:          []models.MCPServerConfig{{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp"}}},
			Skills:              []models.SkillConfig{{Name: "schedule", Description: "schedule benchmark skill", Content: strings.Repeat("schedule skill instructions ", 256)}},
			PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
			ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
			SourceRefs:          []string{fmt.Sprintf("agents/schedule-%04d/SKILLS.md", i)},
			Enabled:             true,
			SelectableAsPrimary: true,
		}
		if err := agentRepo.Create(context.Background(), agent); err != nil {
			b.Fatalf("create benchmark agent %d: %v", i, err)
		}
	}

	ctx := context.Background()
	renderScheduleContent := func(options []repository.AgentScheduleOption) error {
		return pages.ScheduleContent(project, nil, 0, nil, options).Render(ctx, io.Discard)
	}

	b.Run("full_agent_loading", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			options, err := fullScheduleAgentOptionsForBenchmark(ctx, agentRepo, project.ID)
			if err != nil {
				b.Fatal(err)
			}
			if len(options) != 1000 {
				b.Fatalf("full schedule options len = %d, want 1000", len(options))
			}
		}
	})

	b.Run("compact_agent_loading", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			options, err := agentRepo.ListScheduleOptions(ctx, project.ID)
			if err != nil {
				b.Fatal(err)
			}
			if len(options) != 1000 {
				b.Fatalf("compact schedule options len = %d, want 1000", len(options))
			}
		}
	})

	b.Run("full_schedule_content", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			options, err := fullScheduleAgentOptionsForBenchmark(ctx, agentRepo, project.ID)
			if err != nil {
				b.Fatal(err)
			}
			if err := renderScheduleContent(options); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("compact_schedule_content", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			options, err := agentRepo.ListScheduleOptions(ctx, project.ID)
			if err != nil {
				b.Fatal(err)
			}
			if err := renderScheduleContent(options); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func fullScheduleAgentOptionsForBenchmark(ctx context.Context, repo *repository.AgentRepo, projectID string) ([]repository.AgentScheduleOption, error) {
	agents, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	eligible := selectableTaskAgentDefinitionsForProject(agents, projectID)
	options := make([]repository.AgentScheduleOption, 0, len(eligible))
	for _, agent := range eligible {
		options = append(options, repository.AgentScheduleOption{
			ID:    agent.ID,
			Name:  agent.Name,
			Model: agent.Model,
		})
	}
	return options, nil
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

func TestViewSchedule_AutoMergeOptionRendered(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()

	rec := tc.HTTP().Get("/schedule?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	modalStart := strings.Index(body, `id="new_scheduled_task_modal"`)
	if modalStart < 0 {
		t.Fatal("expected new scheduled task modal")
	}
	modal := body[modalStart:]
	for _, want := range []string{
		`type="checkbox" name="auto_merge"`,
		`Auto-merge to target branch on completion`,
	} {
		if !strings.Contains(modal, want) {
			t.Fatalf("scheduled task modal missing %q", want)
		}
	}
}

func TestCreateScheduledTask_AutoMergeIntent(t *testing.T) {
	for _, tcse := range []struct {
		name       string
		htmx       bool
		autoMerge  bool
		wantStatus int
	}{
		{name: "HTMX enabled", htmx: true, autoMerge: true, wantStatus: http.StatusOK},
		{name: "native enabled", autoMerge: true, wantStatus: http.StatusSeeOther},
		{name: "HTMX omitted", htmx: true, wantStatus: http.StatusOK},
		{name: "native omitted", wantStatus: http.StatusSeeOther},
	} {
		t.Run(tcse.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			title := "Scheduled Auto-Merge " + tcse.name
			form := url.Values{
				"title":           {title},
				"prompt":          {"Run later"},
				"category":        {"scheduled"},
				"priority":        {"2"},
				"run_at":          {time.Now().Add(time.Hour).Format("2006-01-02T15:04")},
				"repeat_type":     {"daily"},
				"repeat_interval": {"1"},
			}
			if tcse.autoMerge {
				form.Set("auto_merge", "on")
			}

			path := "/tasks?project_id=" + project.ID + "&from=schedule"
			var rec *httptest.ResponseRecorder
			if tcse.htmx {
				rec = tc.HTMX().Post(path).WithForm(form).Execute()
			} else {
				rec = tc.HTTP().Post(path).WithForm(form).Execute()
			}
			if rec.Code != tcse.wantStatus {
				t.Fatalf("expected %d, got %d body=%s", tcse.wantStatus, rec.Code, rec.Body.String())
			}

			tasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
			if err != nil {
				t.Fatalf("list tasks: %v", err)
			}
			if len(tasks) != 1 {
				t.Fatalf("expected one scheduled task, got %d", len(tasks))
			}
			created := tasks[0]
			if created.AutoMerge != tcse.autoMerge {
				t.Fatalf("AutoMerge = %t, want %t", created.AutoMerge, tcse.autoMerge)
			}

			schedules, err := tc.scheduleRepo.ListByTask(context.Background(), created.ID)
			if err != nil {
				t.Fatalf("list schedules: %v", err)
			}
			if len(schedules) != 1 {
				t.Fatalf("expected one schedule, got %d", len(schedules))
			}
			schedule := schedules[0]
			if schedule.RepeatType != models.RepeatDaily || schedule.RepeatInterval != 1 || !schedule.Enabled || !schedule.ClearContextOnStart {
				t.Fatalf("scheduled fields changed unexpectedly: %+v", schedule)
			}
			if schedule.NextRun == nil || !schedule.NextRun.Equal(schedule.RunAt) {
				t.Fatalf("scheduled task must start NextRun at RunAt, run_at=%v next_run=%v", schedule.RunAt, schedule.NextRun)
			}

			if tcse.autoMerge {
				detail := tc.HTMX().Get("/tasks/" + created.ID + "?project_id=" + project.ID).Execute()
				if detail.Code != http.StatusOK {
					t.Fatalf("expected task detail 200, got %d body=%s", detail.Code, detail.Body.String())
				}
				detailBody := detail.Body.String()
				nameIndex := strings.Index(detailBody, `name="auto_merge"`)
				if nameIndex < 0 {
					t.Fatal("task detail missing auto_merge control")
				}
				tagStart := strings.LastIndex(detailBody[:nameIndex], "<input")
				tagEnd := strings.Index(detailBody[tagStart:], ">")
				if tagStart < 0 || tagEnd < 0 || !strings.Contains(detailBody[tagStart:tagStart+tagEnd], "checked") {
					t.Fatalf("task detail auto_merge control is not checked: %s", detailBody[tagStart:])
				}
			}
		})
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
	project := tc.CreateProject().WithName("Execution Detail Project").Build()
	task := tc.CreateTask(project.ID).WithPrompt("same-project task prompt").Build()
	exec := tc.CreateExecution(task.ID, agent.ID).
		WithStatus(models.ExecRunning).
		WithPromptSent("same-project prompt sent").
		Build()
	if err := tc.execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "same-project output", "", 0, 0); err != nil {
		t.Fatalf("complete execution: %v", err)
	}

	rec := tc.HTTP().Get("/executions/" + exec.ID + "?project_id=" + url.QueryEscape(project.ID)).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	tc.Assert(rec).
		Contains("same-project prompt sent").
		Contains("same-project output")
}

func TestGetExecution_RejectsForeignProjectExecution(t *testing.T) {
	tc := NewTestContext(t)
	agent := tc.CreateLLMConfig().Build()
	projectA := tc.CreateProject().WithName("Execution Detail Project A").Build()
	projectB := tc.CreateProject().WithName("Execution Detail Project B").Build()
	foreignTask := tc.CreateTask(projectA.ID).
		WithTitle("foreign execution task").
		WithPrompt("foreign task prompt sentinel").
		Build()
	exec := tc.CreateExecution(foreignTask.ID, agent.ID).
		WithStatus(models.ExecRunning).
		WithPromptSent("foreign prompt sentinel").
		Build()
	if err := tc.execRepo.Complete(context.Background(), exec.ID, models.ExecFailed, "foreign output sentinel", "foreign error sentinel", 0, 0); err != nil {
		t.Fatalf("complete foreign execution: %v", err)
	}

	rec := tc.HTTP().Get("/executions/" + exec.ID + "?project_id=" + url.QueryEscape(projectB.ID)).Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	tc.Assert(rec).
		NotContains("foreign task prompt sentinel").
		NotContains("foreign prompt sentinel").
		NotContains("foreign output sentinel").
		NotContains("foreign error sentinel")
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
	if !strings.Contains(rec.Body.String(), `Disabled`) || !strings.Contains(rec.Body.String(), `Resume`) {
		t.Fatalf("expected refreshed paused schedule fragment, body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/schedules/"+s.ID+"/toggle?project_id="+project.ID) {
		t.Fatalf("expected project-scoped toggle URL in refreshed fragment, body=%s", rec.Body.String())
	}
	var trigger struct {
		Toast struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"openvibelyToast"`
	}
	if err := json.Unmarshal([]byte(rec.Header().Get("HX-Trigger")), &trigger); err != nil {
		t.Fatalf("expected JSON toast trigger, got %q: %v", rec.Header().Get("HX-Trigger"), err)
	}
	if trigger.Toast.Message != "Schedule paused" || trigger.Toast.Status != "success" {
		t.Fatalf("unexpected pause toast: %#v", trigger.Toast)
	}

	rec = tc.HTMX().Post("/schedules/" + s.ID + "/toggle?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 HTMX resume response, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `Disabled`) || !strings.Contains(rec.Body.String(), `Pause`) {
		t.Fatalf("expected refreshed enabled schedule fragment, body=%s", rec.Body.String())
	}
	trigger = struct {
		Toast struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"openvibelyToast"`
	}{}
	if err := json.Unmarshal([]byte(rec.Header().Get("HX-Trigger")), &trigger); err != nil {
		t.Fatalf("expected JSON resume toast trigger, got %q: %v", rec.Header().Get("HX-Trigger"), err)
	}
	if trigger.Toast.Message != "Schedule resumed" || trigger.Toast.Status != "success" {
		t.Fatalf("unexpected resume toast: %#v", trigger.Toast)
	}
}

func TestToggleScheduleEnabled_FiredOneTimeReturnsBadRequestWithoutMutation(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	s := tc.CreateSchedule(task.ID).WithRunAt(time.Now().Add(-time.Hour)).Build()
	if err := tc.scheduleRepo.MarkRan(context.Background(), s.ID, time.Now(), nil); err != nil {
		t.Fatalf("mark one-time schedule ran: %v", err)
	}
	if err := tc.scheduleRepo.ToggleEnabled(context.Background(), s.ID, false); err != nil {
		t.Fatalf("pause one-time schedule: %v", err)
	}

	rec := tc.HTMX().Post("/schedules/" + s.ID + "/toggle?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 HTMX response with visible validation feedback, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertSchedulesTaskDetailFragment(t, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "Resume") {
		t.Fatalf("expected fired schedule to remain resumable after rejected toggle, body=%s", rec.Body.String())
	}
	var trigger struct {
		Toast struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"openvibelyToast"`
	}
	if err := json.Unmarshal([]byte(rec.Header().Get("HX-Trigger")), &trigger); err != nil {
		t.Fatalf("expected JSON failure toast trigger, got %q: %v", rec.Header().Get("HX-Trigger"), err)
	}
	if trigger.Toast.Message != "one-time schedule has already run; supply a new time before resuming" || trigger.Toast.Status != "failed" {
		t.Fatalf("unexpected fired-schedule toast: %#v", trigger.Toast)
	}
	stored, err := tc.scheduleRepo.GetByID(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get schedule after rejected HTMX resume: %v", err)
	}
	if stored.Enabled || stored.NextRun != nil {
		t.Fatalf("rejected HTMX resume changed fired schedule: %#v", stored)
	}

	rec = tc.HTTP().Post("/schedules/" + s.ID + "/toggle?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for native resume of fired one-time schedule, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "supply a new time") {
		t.Fatalf("expected actionable native time error, body=%s", rec.Body.String())
	}
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
	expected := "/tasks/" + task.ID + "?tab=schedules&project_id=" + project.ID
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
