package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestProjectPermissionToast(t *testing.T) {
	for _, tt := range []struct {
		role       string
		permission authz.Permission
		message    string
	}{
		{"viewer", authz.Edit, "Editor access is required"},
		{"editor", authz.Manage, "Maintainer access is required"},
		{"maintainer", authz.Own, "Only the project owner"},
	} {
		t.Run(tt.role, func(t *testing.T) {
			e := echo.New()
			r := httptest.NewRequest(http.MethodPost, "/tasks/example/followup", nil)
			r.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()
			c := e.NewContext(r, w)
			err := projectAccessResponse(c, &authz.InsufficientRole{Role: tt.role, Permission: tt.permission})
			e.HTTPErrorHandler(err, c)
			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Header().Get("HX-Trigger"), "openvibelyToast")
			require.Contains(t, w.Header().Get("HX-Trigger"), tt.message)
			require.Contains(t, w.Body.String(), "You have "+tt.role+" access")
			require.Equal(t, "none", w.Header().Get("HX-Reswap"))
		})
	}
	t.Run("hidden project stays hidden", func(t *testing.T) {
		e := echo.New()
		w := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/tasks/hidden", nil), w)
		e.HTTPErrorHandler(projectAccessResponse(c, authz.ErrDenied), c)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Empty(t, w.Header().Get("HX-Trigger"))
	})
}
