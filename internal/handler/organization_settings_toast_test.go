package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOrganizationSettingsNavigationPermissionToasts(t *testing.T) {
	env := setupPluginPages(t)
	env.h.authMode = auth.AuthModeLocal
	org := service.NewOrganizationService(env.stores.Organizations, "secret")
	env.h.SetOrganizationService(org)
	env.h.SetProjectAccessService(service.NewProjectAccessService(env.stores.ProjectAccess))
	token, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(t.Context(), token, "settings-member", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, env.projA.ID, user.UserID, "maintainer"))
	for path, section := range map[string]string{
		"/models": "Models", "/agents": "Agents", "/skills": "Skills", "/personality": "Personalities", "/workers": "Workers", "/settings/workers": "Workers", "/settings/plugin-pages": "Custom Pages",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path+"?project_id="+env.projA.ID, nil)
			req = req.WithContext(models.WithPrincipal(t.Context(), user.Principal))
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			env.e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			require.Equal(t, "true", rec.Header().Get("X-OpenVibely-Permission-Denied"))
			require.Equal(t, "none", rec.Header().Get("HX-Reswap"), "current page must stay intact")
			require.Contains(t, rec.Header().Get("HX-Trigger"), "Organization admin access is required to open "+section+" settings.")
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/channels?project_id="+env.projA.ID, nil).WithContext(models.WithPrincipal(t.Context(), user.Principal))
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-OpenVibely-Permission-Denied"))
}
