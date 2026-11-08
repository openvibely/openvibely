package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

// POST routes that only read project state; viewers may use them.
var viewerReadRoutes = map[string]bool{}

// Per-user preferences and project-creation helpers are not project mutations.
var orgLevelRoutes = map[string]bool{"/ui/preferences": true, "/ui/model-favorites": true, "/projects/pick-folder": true}

// Every registered route is driven by an authenticated non-member against a
// private project's resources, so newly added routes are covered automatically.
func TestEveryRegisteredRouteDeniesNonMemberPrivateProjectResources(t *testing.T) {
	sweepRegisteredRoutes(t, "")
}

// Viewers may read, but no mutating route may change project state.
func TestEveryRegisteredMutationRouteDeniesProjectViewer(t *testing.T) {
	sweepRegisteredRoutes(t, "viewer")
}

func sweepRegisteredRoutes(t *testing.T, role string) {
	tc, e := organizationTestApp(t, true)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	tc.handler.SetThreadInputRepo(tc.stores.ThreadInputs)
	tc.handler.SetProjectFolderPicker(func(context.Context) (string, bool, error) { return "", true, nil })
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	tc.handler.projectRepo = guarded
	tc.handler.projectSvc = service.NewProjectService(guarded)
	ctx := t.Context()
	org := tc.handler.organizationSvc
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	sweeper, err := org.Accept(ctx, token, "sweeper", "sweeper-password-long", "")
	require.NoError(t, err)

	const marker = "zq-private-marker"
	project := &models.Project{Name: marker + " project"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	if role != "" {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, sweeper.UserID, role))
	}
	task := &models.Task{ProjectID: project.ID, Title: marker + " task", Prompt: marker + " prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, tc.stores.Tasks.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.stores.Schedules.Create(ctx, schedule))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted, Output: marker + " output"}
	require.NoError(t, tc.stores.Executions.Create(ctx, execution))
	input := &models.ThreadInput{ProjectID: project.ID, TaskID: task.ID, Scope: models.ThreadInputScopeTask, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: marker + " input"}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(ctx, input))
	alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityWarning, Title: marker + " alert", Message: marker}
	require.NoError(t, tc.alertRepo.Create(ctx, alert))
	webhook := &models.WebhookEndpoint{ProjectID: project.ID, Name: marker + " webhook", Enabled: true}
	require.NoError(t, tc.stores.Webhooks.Create(ctx, webhook))

	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"sweeper"}, "password": {"sweeper-password-long"}}, nil)
	require.Equal(t, http.StatusFound, login.Code, login.Body.String())
	cookie := orgCookie(t, login)

	skip := map[string]bool{
		"/logout": true, "/login": true, "/invite": true, "/logged-out": true, "/events/ws": true,
		"/tasks/:taskId/changes/live": true, "/api/lifecycle-executions/:id/events": true,
		"/webhooks/inbound/:pathToken": true, // signature-authenticated; covered by webhook tests
	}
	resource := func(path, param string) string {
		switch param {
		case ":taskId":
			return task.ID
		case ":scheduleId":
			return schedule.ID
		case ":execId", ":executionID":
			return execution.ID
		case ":inputId":
			return input.ID
		case ":projectId":
			return project.ID
		case ":id":
			switch {
			case strings.HasPrefix(path, "/tasks/"), strings.HasPrefix(path, "/api/tasks/"):
				return task.ID
			case strings.HasPrefix(path, "/schedules/"), strings.HasPrefix(path, "/api/schedules/"):
				return schedule.ID
			case strings.HasPrefix(path, "/executions/"), strings.HasPrefix(path, "/api/chat/message/"), strings.HasPrefix(path, "/chat/output/"):
				return execution.ID
			case strings.HasPrefix(path, "/alerts/"):
				return alert.ID
			case strings.HasPrefix(path, "/channels/webhooks/"):
				return webhook.ID
			case strings.HasPrefix(path, "/projects/"):
				return project.ID
			}
		}
		return "unknown-resource"
	}
	count := 0
	for _, route := range e.Routes() {
		readOnly := route.Method == http.MethodGet || route.Method == http.MethodHead || viewerReadRoutes[route.Method+" "+route.Path]
		if role != "" && readOnly {
			continue
		}
		if route.Method == "echo_route_not_found" || skip[route.Path] || strings.HasPrefix(route.Path, "/static") || strings.HasPrefix(route.Path, "/swagger") || strings.HasPrefix(route.Path, "/auth/") || strings.HasSuffix(route.Path, "/callback") {
			continue
		}
		count++
		route := route
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			segments := strings.Split(route.Path, "/")
			scoped := false
			for i, segment := range segments {
				if strings.HasPrefix(segment, ":") {
					segments[i] = resource(route.Path, segment)
					scoped = scoped || segments[i] != "unknown-resource"
				}
			}
			path := strings.Join(segments, "/") + "?project_id=" + project.ID
			form := url.Values{"project_id": {project.ID}, "task_id": {task.ID}, "name": {"changed"}, "title": {"changed"}, "content": {"changed"}, "message": {"changed"}}
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			req := httptest.NewRequest(route.Method, "http://example.com"+path, strings.NewReader(form.Encode())).WithContext(reqCtx)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://example.com")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if role == "" {
				require.NotContains(t, rec.Body.String(), marker, "private resource leaked")
				for _, header := range rec.Header().Values("HX-Trigger") {
					require.NotContains(t, header, marker)
				}
			}
			if scoped || role != "" && rec.Code < 300 && !orgLevelRoutes[route.Path] {
				require.GreaterOrEqual(t, rec.Code, 400, "non-member reached a private resource handler")
			}
		})
	}
	if role == "" {
		require.Greater(t, count, 300, "the sweep must walk every registered route")
	} else {
		require.Greater(t, count, 150, "the sweep must walk every registered mutation route")
	}

	storedTask, err := tc.stores.Tasks.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.Title, storedTask.Title)
	require.Equal(t, models.StatusPending, storedTask.Status)
	require.Equal(t, models.CategoryBacklog, storedTask.Category)
	storedSchedule, err := tc.stores.Schedules.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSchedule)
	require.True(t, storedSchedule.Enabled)
	storedInput, err := tc.stores.ThreadInputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, input.Content, storedInput.Content)
	require.Equal(t, models.ThreadInputPending, storedInput.InputStatus)
	storedWebhook, err := tc.stores.Webhooks.GetByID(ctx, webhook.ID)
	require.NoError(t, err)
	require.Equal(t, webhook.Name, storedWebhook.Name)
	require.Equal(t, webhook.Secret, storedWebhook.Secret)
	storedProject, err := tc.stores.Projects.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, project.Name, storedProject.Name)
	storedPolicy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "private", storedPolicy.Visibility)
	require.Equal(t, models.BootstrapUserID, storedPolicy.OwnerUserID)
	alerts, err := tc.alertRepo.ListByProject(ctx, project.ID, 10)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.False(t, alerts[0].IsRead)
}
