package handler

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestProjectComputerSharingPermissions(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	tc.handler.projectRepo = guarded
	tc.handler.computerSvc = service.NewProjectComputerService(guarded, liveComputerFixture{})
	ctx := t.Context()
	token, err := tc.handler.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := tc.handler.organizationSvc.Accept(ctx, token, "computer-member", "computer-password-long", "")
	require.NoError(t, err)
	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"computer-member"}, "password": {"computer-password-long"}}, nil)
	require.Equal(t, http.StatusFound, login.Code)
	cookie := orgCookie(t, login)
	project := &models.Project{Name: "Shared computer"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	for _, role := range []string{"", "viewer", "editor", "maintainer"} {
		t.Run(role, func(t *testing.T) {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
			status := orgRequest(e, http.MethodGet, "/projects/"+project.ID+"/computer/status", nil, cookie)
			expected := http.StatusOK
			if role == "" {
				expected = http.StatusNotFound
			} else if role == "viewer" {
				expected = http.StatusForbidden
			}
			require.Equal(t, expected, status.Code, status.Body.String())
			save := orgRequest(e, http.MethodPost, "/projects/"+project.ID+"/computer/time-zone", url.Values{"time_zone": {"UTC"}}, cookie)
			expected = http.StatusForbidden
			if role == "" {
				expected = http.StatusNotFound
			} else if role == "maintainer" {
				expected = http.StatusOK
			}
			require.Equal(t, expected, save.Code, save.Body.String())
		})
	}
}
