package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGlobalSenderMappingHandlersLimitAssignments(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.authMode = auth.AuthModeLocal
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetTelegramAuthRepo(tc.stores.TelegramAuth)
	invite := func(name string) models.Principal {
		token, err := h.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
		require.NoError(t, err)
		u, err := h.organizationSvc.Accept(ctx, token, name, "long-password-value", "")
		require.NoError(t, err)
		return u.Principal
	}
	alice, bob := invite("alice"), invite("bob")
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	e.GET("/channels/telegram/authorized-users", h.ListTelegramAuthorizedUsers)
	e.POST("/channels/telegram/authorized-users", h.AddTelegramAuthorizedUser)
	e.DELETE("/channels/telegram/authorized-users/:id", h.RemoveTelegramAuthorizedUser)
	e.POST("/channels/:channel/senders/:id", h.BindChannelSender)
	call := func(p models.Principal, method, path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(models.WithPrincipal(ctx, p))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	res := call(alice, http.MethodGet, "/channels/telegram/authorized-users", nil)
	require.Equal(t, 200, res.Code)
	require.Contains(t, res.Body.String(), alice.UserID)
	require.NotContains(t, res.Body.String(), bob.UserID)
	require.NotContains(t, res.Body.String(), `name="project_id"`)
	form := url.Values{"user_id_or_username": {"123"}, "application_user_id": {bob.UserID}}
	require.NotEqual(t, 200, call(alice, http.MethodPost, "/channels/telegram/authorized-users", form).Code)
	form.Set("application_user_id", alice.UserID)
	require.Equal(t, 200, call(alice, http.MethodPost, "/channels/telegram/authorized-users", form).Code)
	rows, err := tc.stores.TelegramAuth.ListByProject(ctx, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotEqual(t, 200, call(bob, http.MethodPost, "/channels/telegram/senders/"+rows[0].ID, url.Values{"application_user_id": {bob.UserID}}).Code)
	require.NotEqual(t, 200, call(bob, http.MethodDelete, "/channels/telegram/authorized-users/"+rows[0].ID, nil).Code)
	owner := models.Principal{UserID: models.BootstrapUserID}
	require.Equal(t, 200, call(owner, http.MethodPost, "/channels/telegram/senders/"+rows[0].ID, url.Values{"application_user_id": {bob.UserID}}).Code)
	require.NotEqual(t, 200, call(alice, http.MethodDelete, "/channels/telegram/authorized-users/"+rows[0].ID, nil).Code)
	require.Equal(t, 200, call(bob, http.MethodDelete, "/channels/telegram/authorized-users/"+rows[0].ID, nil).Code)
	// Credentials remain a separate org-admin operation.
	e.POST("/channels/telegram", func(c echo.Context) error { return c.NoContent(204) })
	require.NotEqual(t, 204, call(alice, http.MethodPost, "/channels/telegram", url.Values{"bot_token": {"fake"}}).Code)
}
