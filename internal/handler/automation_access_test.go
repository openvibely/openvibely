package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestAutomationHTTPAndRuntimePermissions(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	registration := service.NewAutomationRegistrationService(tc.stores.Automations, service.NewAutomationAdapterRegistry())
	h.SetAutomationServices(service.NewAutomationGraphService(tc.stores.Automations), registration)
	h.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(tc.stores.Automations, tc.scheduleRepo))
	project := createProject(t, h, "Automation permissions")
	task := createTask(t, h, project.ID, "Automation task", func(task *models.Task) { task.Category = models.CategoryScheduled })
	schedule := createSchedule(t, h, task.ID, time.Now().Add(time.Hour))
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/access-test",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)
	id := definition.Automation.ID
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, token, "automation-bob", "bob-password-long", "")
	require.NoError(t, err)
	routes := []struct {
		method, path string
		minimum      authz.Permission
	}{
		{"GET", "/automations", authz.View},
		{"GET", "/automations/:automationId", authz.View},
		{"GET", "/automations/:automationId/builder", authz.Manage},
		{"GET", "/automations/:automationId/duplicate", authz.Manage},
		{"POST", "/automations/builder", authz.Manage},
		{"POST", "/automations/yaml/parse", authz.Manage},
		{"POST", "/automations/:automationId/run-now", authz.Manage},
		{"POST", "/automations/:automationId/pause", authz.Manage},
		{"POST", "/automations/:automationId/resume", authz.Manage},
		{"POST", "/automations/:automationId/delete", authz.Manage},
		{"POST", "/automations/:automationId/refresh-external", authz.Manage},
		{"DELETE", "/automations/bulk", authz.Manage},
	}
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	for _, route := range routes {
		e.Add(route.method, route.path, func(c echo.Context) error { return c.NoContent(204) })
	}
	for _, role := range []string{"", "viewer", "editor", "maintainer", "owner"} {
		principal := bob.Principal
		if role == "owner" {
			principal = models.Principal{UserID: models.BootstrapUserID}
		} else {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		}
		userCtx := models.WithPrincipal(ctx, principal)
		for _, route := range routes {
			path := strings.ReplaceAll(route.path, ":automationId", id) + "?project_id=" + project.ID
			req := httptest.NewRequest(route.method, path, nil).WithContext(userCtx)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			expected := 404
			if authz.Rank(role) >= authz.Minimum(route.minimum) {
				expected = 204
			}
			require.Equal(t, expected, rec.Code, role+" "+route.path)
		}
		executor := h.chatActionExecutor(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
		_, handled, isError, err := executor(userCtx, "get_automation", json.RawMessage(`{"automation_id":"`+id+`"}`))
		require.True(t, handled)
		if role == "" {
			require.True(t, isError)
			require.ErrorIs(t, err, authz.ErrDenied)
		} else {
			require.False(t, isError)
			require.NoError(t, err)
		}
		if authz.Rank(role) < authz.Minimum(authz.Manage) {
			_, _, isError, err = executor(userCtx, "pause_automation", json.RawMessage(`{"automation_id":"`+id+`"}`))
			require.True(t, isError)
			require.ErrorIs(t, err, authz.ErrDenied)
			stored, err := tc.stores.Automations.GetDefinition(ctx, project.ID, id)
			require.NoError(t, err)
			require.Equal(t, definition.Automation.LifecycleState, stored.Automation.LifecycleState)
		}
	}
	// An accessible project is not evidence that a supplied automation belongs to it.
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "/automations/"+id+"?project_id=default", nil).WithContext(ownerCtx))
	require.Equal(t, 404, rec.Code)
	other := h.chatActionExecutor(streamingResponseParams{ProjectID: "default"}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	_, _, _, err = other(ownerCtx, "get_automation", json.RawMessage(`{"automation_id":"`+id+`"}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	// Use the actual mutation handler; revocation must stop it before any writes.
	e.POST("/automations/:automationId/pause", h.PauseAutomation)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, "maintainer"))
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, "viewer"))
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("POST", "/automations/"+id+"/pause?project_id="+project.ID, nil).WithContext(models.WithPrincipal(ctx, bob.Principal)))
	require.Equal(t, http.StatusNotFound, rec.Code)
	stored, err := tc.stores.Automations.GetDefinition(ctx, project.ID, id)
	require.NoError(t, err)
	require.Equal(t, definition.Automation.LifecycleState, stored.Automation.LifecycleState)
	// Auth-off tools retain the local owner's ability to manage automations.
	h.SetAuthMode(auth.AuthModeDisabled)
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	_, _, isError, err := executor(ctx, "pause_automation", json.RawMessage(`{"automation_id":"`+id+`"}`))
	require.NoError(t, err)
	require.False(t, isError)
	stored, err = tc.stores.Automations.GetDefinition(ctx, project.ID, id)
	require.NoError(t, err)
	require.NotEqual(t, definition.Automation.LifecycleState, stored.Automation.LifecycleState)
}

func TestAutomationRuntimePermissionClassification(t *testing.T) {
	for _, name := range []string{"save_automation", "update_automation_template", "run_automation_now", "pause_automation", "resume_automation", "delete_automation", "preview_automation_description"} {
		permission, protected := projectRuntimePermission(name)
		require.True(t, protected, name)
		require.Equal(t, authz.Manage, permission, name)
	}
	for _, name := range []string{"list_automations", "get_automation"} {
		permission, protected := projectRuntimePermission(name)
		require.True(t, protected, name)
		require.Equal(t, authz.View, permission, name)
	}
}

func TestProjectRuntimeToolsEnforceRoleBoundaries(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	project := createProject(t, h, "zq-runtime-private")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, token, "runtime-bob", "bob-password-long", "")
	require.NoError(t, err)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)

	minimum := map[string]authz.Permission{
		"project_info": authz.View, "get_current_project": authz.View,
		"request_user_input": authz.Edit, "send_message": authz.Edit,
		"update_project_settings": authz.Manage, "github_close_issue": authz.Manage, "github_open_pull_request": authz.Manage,
	}
	inputs := map[string]string{"send_message": `{"message":"hi"}`, "request_user_input": `{"question":"q"}`, "github_close_issue": `{"issue_number":1}`, "update_project_settings": `{"description":"changed"}`}
	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: project.ID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	for _, role := range []string{"", "viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		for name, permission := range minimum {
			input := inputs[name]
			if input == "" {
				input = `{}`
			}
			_, _, _, err := executor(bobCtx, name, json.RawMessage(input))
			if authz.Rank(role) >= authz.Minimum(permission) {
				require.NotErrorIs(t, err, authz.ErrDenied, role+" "+name)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied, role+" "+name)
			}
		}
	}
	stored, err := tc.stores.Projects.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "changed", stored.Description, "the maintainer run must reach the settings handler")

	// Listing from an accessible project must not reveal private projects.
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, ""))
	other := h.chatActionExecutor(streamingResponseParams{ProjectID: "default"}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceAPI)
	out, _, isError, err := other(bobCtx, "list_projects", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, isError)
	require.NotContains(t, out, "zq-runtime-private")
	ownerOut, _, _, err := other(models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID}), "list_projects", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, ownerOut, "zq-runtime-private")
}
