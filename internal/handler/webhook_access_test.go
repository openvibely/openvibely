package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestWebhookSavedIdentityAndRevocation(t *testing.T) {
	tc := newWebhookTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "webhook-user", "long-password-value", "")
	require.NoError(t, err)
	userCtx := models.WithPrincipal(ctx, user.Principal)
	endpoint := &models.WebhookEndpoint{ProjectID: "default", Name: "Owned hook", Enabled: true, DefaultPriority: 2, RunAsUserID: models.BootstrapUserID}
	require.ErrorIs(t, tc.webhookRepo.Create(userCtx, endpoint), authz.ErrDenied, "editor cannot create privileged webhook")
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "maintainer"))
	require.NoError(t, tc.webhookRepo.Create(userCtx, endpoint))
	require.Equal(t, user.UserID, endpoint.RunAsUserID, "caller cannot forge run-as")
	loaded, err := tc.webhookRepo.GetByPathToken(ctx, endpoint.PathToken)
	require.NoError(t, err)
	require.Equal(t, user.UserID, loaded.RunAsUserID)
	// A signed inbound request has no browser principal; restore the endpoint owner.
	task, err := h.createWebhookTaskFromEndpoint(ctx, loaded, "test", "first", "{}")
	require.NoError(t, err)
	saved, err := tc.stores.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, user.UserID, saved)
	// Updating settings cannot transfer the saved identity.
	loaded.RunAsUserID = models.BootstrapUserID
	require.NoError(t, tc.webhookRepo.Update(ctx, loaded))
	loaded, err = tc.webhookRepo.GetByID(ctx, loaded.ID)
	require.NoError(t, err)
	require.Equal(t, user.UserID, loaded.RunAsUserID)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	denied, err := h.createWebhookTaskFromEndpoint(models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID}), loaded, "test", "denied", "{}")
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Nil(t, denied, "test endpoint must not borrow the testing owner's access")
	request := httptest.NewRequest(http.MethodPost, "/webhooks/inbound/"+loaded.PathToken, nil)
	request.Header.Set("X-Webhook-Secret", loaded.Secret)
	recorder := httptest.NewRecorder()
	inbound := echo.New().NewContext(request, recorder)
	inbound.SetParamNames("pathToken")
	inbound.SetParamValues(loaded.PathToken)
	require.NoError(t, h.HandleWebhookInbound(inbound))
	require.Equal(t, http.StatusForbidden, recorder.Code, "valid webhook secret cannot bypass revoked creator access")
	loaded.RunAsUserID = ""
	_, err = h.createWebhookTaskFromEndpoint(ctx, loaded, "test", "missing", "{}")
	require.ErrorIs(t, err, authz.ErrDenied)
	h.SetAuthMode(auth.AuthModeDisabled)
	task, err = h.createWebhookTaskFromEndpoint(ctx, loaded, "test", "auth-off", "{}")
	require.NoError(t, err)
	saved, err = tc.stores.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, saved)
}

func TestWebhookManagementBoundary(t *testing.T) {
	tc := newWebhookTestContext(t)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "hook-reader", "long-password-value", "")
	require.NoError(t, err)
	endpoint := tc.createEndpoint(t, "default", "Secret", true)
	foreignProject := createProject(t, h, "Foreign webhook project")
	foreign := tc.createEndpoint(t, foreignProject.ID, "Foreign", true)
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	c := echo.New().NewContext(httptest.NewRequest("GET", "/channels/webhooks/"+foreign.ID+"?project_id=default", nil).WithContext(ownerCtx), httptest.NewRecorder())
	c.SetPath("/channels/webhooks/:id")
	c.SetParamNames("id")
	c.SetParamValues(foreign.ID)
	require.ErrorIs(t, h.authorizeWebhookResource(c), authz.ErrDenied)
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		policy, err := tc.stores.ProjectAccess.Get(ctx, "default")
		require.NoError(t, err)
		policy.OrgRole = "viewer"
		require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, role))
		for _, method := range []string{"GET", "PUT", "DELETE", "POST"} {
			req := httptest.NewRequest(method, "/channels/webhooks/"+endpoint.ID+"?project_id=default", nil).WithContext(models.WithPrincipal(ctx, user.Principal))
			c := echo.New().NewContext(req, httptest.NewRecorder())
			c.SetPath("/channels/webhooks/:id")
			c.SetParamNames("id")
			c.SetParamValues(endpoint.ID)
			called := false
			err := h.projectAccessBoundary(func(c echo.Context) error { called = true; return c.NoContent(http.StatusNoContent) })(c)
			if role == "maintainer" {
				require.NoError(t, err)
				require.True(t, called)
			} else {
				require.Error(t, err)
				require.False(t, called)
			}
		}
	}
}
