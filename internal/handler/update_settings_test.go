package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsSaveAndRender(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	project := createProject(t, h, "Updates")
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Distribution: "binary"}, "stable", nil, nil, false, "", nil)
	require.NoError(t, coordinator.SetPersistence(filepath.Join(t.TempDir(), "updates.json")))
	h.SetUpdateCoordinator(coordinator)
	e := echo.New()
	e.GET("/settings/updates", h.UpdateSettings)
	e.POST("/settings/updates", h.UpdateSettings)
	request := func(method, body, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/settings/updates?project_id="+project.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	rec := request(http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "App updates")
	rec = request(http.MethodPost, "automatic_updates=false", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Update settings saved")
	require.False(t, coordinator.Snapshot().AutomaticUpdates)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPost, "automatic_updates=invalid", "").Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodPost, "automatic_updates=true", "https://other.example").Code)
	require.False(t, coordinator.Snapshot().AutomaticUpdates)
	require.Equal(t, http.StatusOK, request(http.MethodPost, "automatic_updates=true", "").Code)
	require.True(t, coordinator.Snapshot().AutomaticUpdates)
}
