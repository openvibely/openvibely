package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestProjectManagementHTTPPermissions(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	tc.handler.projectRepo = guarded
	tc.handler.projectSvc = service.NewProjectService(guarded)
	ctx := t.Context()
	org := tc.handler.organizationSvc
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, token, "bob", "bob-password-long", "")
	require.NoError(t, err)
	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"bob"}, "password": {"bob-password-long"}}, nil)
	require.Equal(t, http.StatusFound, login.Code, login.Body.String())
	cookie := orgCookie(t, login)
	project := &models.Project{Name: "Private fixture"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	list := orgRequest(e, http.MethodGet, "/projects", nil, cookie)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.NotContains(t, list.Body.String(), project.ID)
	api := orgRequest(e, http.MethodGet, "/api/projects", nil, cookie)
	require.Equal(t, http.StatusOK, api.Code, api.Body.String())
	require.NotContains(t, api.Body.String(), project.ID)

	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, role))
		expected := http.StatusNotFound
		if role == "maintainer" {
			expected = http.StatusOK
		}
		edit := orgRequest(e, http.MethodGet, "/projects/"+project.ID+"/edit", nil, cookie)
		require.Equal(t, expected, edit.Code, edit.Body.String())
		recovery := orgRequest(e, http.MethodPost, "/projects/"+project.ID+"/reclone", nil, cookie)
		recoveryExpected := http.StatusNotFound
		if role == "maintainer" {
			recoveryExpected = http.StatusConflict
		}
		require.Equal(t, recoveryExpected, recovery.Code, recovery.Body.String())
		deletion := orgRequest(e, http.MethodDelete, "/projects/"+project.ID, nil, cookie)
		require.Equal(t, http.StatusNotFound, deletion.Code, deletion.Body.String())
		if role != "maintainer" {
			update := orgRequest(e, http.MethodPut, "/projects/"+project.ID, url.Values{"name": {"unauthorized change"}}, cookie)
			require.Equal(t, http.StatusNotFound, update.Code, update.Body.String())
		}
	}
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, ""))
	edit := orgRequest(e, http.MethodGet, "/projects/"+project.ID+"/edit", nil, cookie)
	require.Equal(t, http.StatusNotFound, edit.Code)
	unchanged, err := tc.stores.Projects.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "Private fixture", unchanged.Name)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	require.NoError(t, tc.handler.settingsRepo.Set(bobCtx, uiPreferenceSelectedProjectIDKey, project.ID))
	request := httptest.NewRequest(http.MethodGet, "/chat", nil).WithContext(bobCtx)
	selected, err := tc.handler.getCurrentProjectID(e.NewContext(request, httptest.NewRecorder()))
	require.NoError(t, err)
	require.Equal(t, models.AllProjectsID, selected)
	request = httptest.NewRequest(http.MethodGet, "/chat?project_id="+project.ID, nil).WithContext(bobCtx)
	_, err = tc.handler.getCurrentProjectID(e.NewContext(request, httptest.NewRecorder()))
	require.ErrorIs(t, err, authz.ErrDenied)

}

func TestProjectManagementAuthOffUsesOwner(t *testing.T) {
	tc, e := organizationTestApp(t, false)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := &models.Project{Name: "Auth off"}
	require.NoError(t, tc.stores.Projects.Create(t.Context(), project))
	edit := orgRequest(e, http.MethodGet, "/projects/"+project.ID+"/edit", nil, nil)
	require.Equal(t, http.StatusOK, edit.Code, edit.Body.String())
}
