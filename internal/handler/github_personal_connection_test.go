package handler

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPersonalGitHubModalAndHandlersIsolateMembers(t *testing.T) {
	h, e, _, stores := setupTestHandlerWithStores(t)
	h.authMode = auth.AuthModeLocal
	org := service.NewOrganizationService(stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(stores.ProjectAccess))
	github := service.NewGitHubService(stores.Settings, "", "", "", "")
	github.SetCredentialAuthorization(stores.Organizations, false)
	github.SetPersonalConnections(stores.GitHubConnections)
	h.SetPersonalGitHubService(github)
	h.SetGitHubService(github)
	invite := func(name string) models.Principal {
		token, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
		require.NoError(t, err)
		user, err := org.Accept(t.Context(), token, name, "long-password-value", "")
		require.NoError(t, err)
		return user.Principal
	}
	alice, bob := invite("alice-github"), invite("bob-github")
	call := func(actor models.Principal, method, path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode())).WithContext(models.WithPrincipal(t.Context(), actor))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	form := url.Values{"github_auth_mode": {"pat"}, "github_revision": {"0"}, "github_pat": {"alice-secret"}, "github_author_name": {"Alice"}, "github_author_email": {"alice@example.com"}}
	response := call(alice, "POST", "/channels/github/configure", form)
	require.Equal(t, 200, response.Code, response.Body.String())
	c, err := stores.GitHubConnections.GetOwn(models.WithPrincipal(t.Context(), alice))
	require.NoError(t, err)
	require.Equal(t, "alice-secret", c.PAT)
	other, err := stores.GitHubConnections.GetOwn(models.WithPrincipal(t.Context(), bob))
	require.NoError(t, err)
	require.Empty(t, other.PAT)
	for _, actor := range []models.Principal{alice, bob} {
		response = call(actor, "GET", "/channels?project_id=__all_projects__", nil)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "alice-secret")
		require.Contains(t, response.Body.String(), `name="github_author_name"`)
		if actor.UserID == bob.UserID {
			require.NotContains(t, response.Body.String(), "alice@example.com")
		}
	}
	response = call(bob, "POST", "/channels/github/remove", nil)
	require.Equal(t, 200, response.Code, response.Body.String())
	c, err = stores.GitHubConnections.GetOwn(models.WithPrincipal(t.Context(), alice))
	require.NoError(t, err)
	require.Equal(t, "alice-secret", c.PAT)
	response = call(alice, "POST", "/channels/github/remove", nil)
	require.Equal(t, 200, response.Code, response.Body.String())
	response = call(alice, "POST", "/channels/github/configure", form)
	require.Equal(t, 409, response.Code, response.Body.String())
}
