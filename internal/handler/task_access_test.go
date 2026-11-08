package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestTaskResourcePermissionMatrix(t *testing.T) {
	tc := NewStoreTestContext(t)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	project := createProject(t, h, "Permission fixture")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	task := createTask(t, h, project.ID, "Protected task", func(t *models.Task) { t.Category = models.CategoryBacklog })
	schedule := createSchedule(t, h, task.ID, time.Now().Add(time.Hour))
	agent := createAgent(t, h.llmConfigRepo)
	execution := createExec(t, h, task.ID, agent.ID)
	attachment := &models.Attachment{TaskID: task.ID, FileName: "test.txt", FilePath: "/not-opened", MediaType: "text/plain"}
	require.NoError(t, tc.stores.Attachments.Create(ctx, attachment))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, Content: "queued"}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(ctx, input))
	cases := []struct {
		method, route, path string
		form                url.Values
		minimum             authz.Permission
	}{
		{"GET", "/tasks/:taskId/thread", "/tasks/" + task.ID + "/thread", nil, authz.View},
		{"POST", "/tasks/:taskId/changes/live", "/tasks/" + task.ID + "/changes/live", nil, authz.View},
		{"POST", "/tasks/:taskId/run", "/tasks/" + task.ID + "/run", nil, authz.Edit},
		{"DELETE", "/tasks/:taskId", "/tasks/" + task.ID, nil, authz.Edit},
		{"POST", "/tasks/:taskId/goal", "/tasks/" + task.ID + "/goal", nil, authz.Edit},
		{"GET", "/api/tasks/:id/swarm", "/api/tasks/" + task.ID + "/swarm", nil, authz.View},
		{"POST", "/api/tasks/:id/swarm/rerun-merger", "/api/tasks/" + task.ID + "/swarm/rerun-merger", nil, authz.Manage},
		{"POST", "/tasks/:taskId/worktree/merge", "/tasks/" + task.ID + "/worktree/merge", nil, authz.Manage},
		{"PATCH", "/tasks/:taskId/details/property", "/tasks/" + task.ID + "/details/property", url.Values{"field": {"priority"}, "value": {"4"}}, authz.Edit},
		{"PATCH", "/tasks/:taskId/details/property", "/tasks/" + task.ID + "/details/property", url.Values{"field": {"auto_merge"}, "value": {"true"}}, authz.Manage},
		{"PUT", "/tasks/:taskId", "/tasks/" + task.ID, url.Values{"auto_merge_present": {"1"}}, authz.Edit},
		{"PUT", "/tasks/:taskId", "/tasks/" + task.ID, url.Values{"auto_merge_present": {"1"}, "auto_merge": {"true"}}, authz.Manage},
		{"PUT", "/tasks/:taskId", "/tasks/" + task.ID, url.Values{"merge_target_branch": {"main"}}, authz.Manage},
		{"PUT", "/schedules/:id", "/schedules/" + schedule.ID, nil, authz.Edit},
		{"POST", "/api/schedules/:id/toggle", "/api/schedules/" + schedule.ID + "/toggle", nil, authz.Edit},
		{"GET", "/executions/:id", "/executions/" + execution.ID, nil, authz.View},
		{"POST", "/attachments/:id/download", "/attachments/" + attachment.ID + "/download", nil, authz.View},
		{"DELETE", "/attachments/:id", "/attachments/" + attachment.ID, nil, authz.Edit},
		{"POST", "/thread-inputs/:inputId/cancel", "/thread-inputs/" + input.ID + "/cancel", nil, authz.Edit},
	}
	// Only the terminal handlers are stubs: the production middleware and stores
	// must reject denied requests before the handler can perform any side effect.
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	for _, c := range cases {
		e.Add(c.method, c.route, func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	}
	for _, role := range []string{"", "viewer", "editor", "maintainer", "owner"} {
		principal := bob.Principal
		if role == "owner" {
			principal = models.Principal{UserID: models.BootstrapUserID}
		} else {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		}
		for _, c := range cases {
			req := httptest.NewRequest(c.method, c.path+"?project_id=default", strings.NewReader(c.form.Encode())).WithContext(models.WithPrincipal(ctx, principal))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			want := http.StatusNotFound
			if role != "" {
				want = http.StatusForbidden
			}
			if authz.Rank(role) >= authz.Minimum(c.minimum) {
				want = http.StatusNoContent
			}
			require.Equal(t, want, rec.Code, "role=%s %s %s: %s", role, c.method, c.path, rec.Body.String())
		}
	}
}

func TestTaskHTTPViewerCannotDeleteOrEnableAutoMerge(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	ctx := t.Context()
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := tc.handler.organizationSvc
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	policy, err := tc.stores.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	require.Equal(t, "editor", policy.OrgRole, "legacy baseline must not override the explicit viewer grant")
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "viewer"))
	task := createTask(t, tc.handler, "default", "Keep me", func(t *models.Task) { t.Category = models.CategoryBacklog })
	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"bob"}, "password": {"bob-password-long"}}, nil)
	cookie := orgCookie(t, login)
	create := orgRequest(e, http.MethodPost, "/tasks", url.Values{"title": {"Forbidden"}, "prompt": {"No"}, "category": {"backlog"}}, cookie)
	require.Equal(t, http.StatusForbidden, create.Code, create.Body.String())
	bulk := orgRequest(e, http.MethodDelete, "/tasks/backlog?project_id=default", nil, cookie)
	require.Equal(t, http.StatusForbidden, bulk.Code, bulk.Body.String())

	deletion := orgRequest(e, http.MethodDelete, "/tasks/"+task.ID, nil, cookie)
	require.Equal(t, http.StatusForbidden, deletion.Code)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "editor"))
	update := orgRequest(e, http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id=default", url.Values{"field": {"auto_merge"}, "value": {"true"}}, cookie)
	require.Equal(t, http.StatusForbidden, update.Code)
	saved, err := tc.stores.Tasks.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.False(t, saved.AutoMerge)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, ""))
	priority := orgRequest(e, http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id=default", url.Values{"field": {"priority"}, "value": {"4"}}, cookie)
	require.Equal(t, http.StatusNotFound, priority.Code)
	saved, err = tc.stores.Tasks.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotEqual(t, 4, saved.Priority)

}

func TestTaskHTTPAuthOffCanEditPriority(t *testing.T) {
	tc, e := organizationTestApp(t, false)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	task := createTask(t, tc.handler, "default", "Local task", func(t *models.Task) { t.Category = models.CategoryBacklog })
	result := orgRequest(e, http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id=default", url.Values{"field": {"priority"}, "value": {"4"}}, nil)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	saved, err := tc.stores.Tasks.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 4, saved.Priority)
}

func TestTaskCollectionNormalizesMissingProjectBeforeHandler(t *testing.T) {
	tc := NewStoreTestContext(t)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	e := echo.New()
	e.Use(tc.handler.projectAccessBoundary)
	e.POST("/tasks", func(c echo.Context) error {
		require.Equal(t, "default", c.QueryParam("project_id"))
		require.Equal(t, "default", c.Request().URL.Query().Get("project_id"))
		return c.NoContent(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/tasks", nil).WithContext(models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, request)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

func TestInvitedMemberNeedsExplicitProjectGrant(t *testing.T) {
	tc, app := organizationTestApp(t, true)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	project := createProject(t, h, "Explicit access project")
	task := createTask(t, h, project.ID, "Not visible to new members", func(task *models.Task) { task.Category = models.CategoryBacklog })
	org := h.organizationSvc
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := org.Accept(ctx, token, "explicit-member", "long-member-password", "")
	require.NoError(t, err)
	login := orgRequest(app, http.MethodPost, "/login", url.Values{"username": {"explicit-member"}, "password": {"long-member-password"}}, nil)
	cookie := orgCookie(t, login)
	memberCtx := models.WithPrincipal(ctx, member.Principal)
	projects, err := guarded.ListSelectorOptions(memberCtx)
	require.NoError(t, err)
	require.Empty(t, projects, "joining the org must not reveal any project")
	for _, path := range []string{"/tasks", "/organization"} {
		response := orgRequest(app, http.MethodGet, path, nil, cookie)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), task.Title)
	}
	for _, role := range []string{"", "viewer", "editor", ""} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, member.UserID, role))
		projects, err = guarded.ListSelectorOptions(memberCtx)
		require.NoError(t, err)
		if role == "" {
			require.Empty(t, projects)
		} else {
			require.Len(t, projects, 1)
		}
		if role != "" {
			board := orgRequest(app, http.MethodGet, "/tasks?project_id="+project.ID, nil, cookie)
			require.Equal(t, http.StatusOK, board.Code, board.Body.String())
			require.Contains(t, board.Body.String(), task.Title)
		}
		deniedStatus := http.StatusNotFound
		if role == "viewer" {
			deniedStatus = http.StatusForbidden
		}
		if role != "editor" {
			created := orgRequest(app, http.MethodPost, "/tasks?project_id="+project.ID, url.Values{"title": {"Forbidden task"}, "prompt": {"No"}, "category": {"backlog"}}, cookie)
			require.Equal(t, deniedStatus, created.Code, created.Body.String())
			followup := orgRequest(app, http.MethodPost, "/tasks/"+task.ID+"/thread?project_id="+project.ID, url.Values{"message": {"Forbidden followup"}}, cookie)
			require.Equal(t, deniedStatus, followup.Code, followup.Body.String())
			if role == "viewer" {
				for _, response := range []*httptest.ResponseRecorder{created, followup} {
					require.Contains(t, response.Header().Get("HX-Trigger"), "You have viewer access")
					require.Equal(t, "none", response.Header().Get("HX-Reswap"))
				}
			}
		}
		updated := orgRequest(app, http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id="+project.ID, url.Values{"field": {"priority"}, "value": {"4"}}, cookie)
		if role == "editor" {
			require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
		} else {
			require.Equal(t, deniedStatus, updated.Code, updated.Body.String())
		}
	}
}
