package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
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
	statePath := filepath.Join(t.TempDir(), "updates.json")
	require.NoError(t, coordinator.SetPersistence(statePath))
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
	require.Contains(t, rec.Body.String(), `class="toggle toggle-sm"`)
	require.NotContains(t, rec.Body.String(), "Unsaved changes")
	require.NotContains(t, rec.Body.String(), "Enabled by default")
	require.NotContains(t, rec.Body.String(), "Updates already in progress")
	rec = request(http.MethodPost, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Update settings saved")
	require.False(t, coordinator.Snapshot().AutomaticUpdates)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPost, "automatic_updates=invalid", "").Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodPost, "automatic_updates=true", "https://other.example").Code)
	require.False(t, coordinator.Snapshot().AutomaticUpdates)
	require.Equal(t, http.StatusOK, request(http.MethodPost, "automatic_updates=true", "").Code)
	require.True(t, coordinator.Snapshot().AutomaticUpdates)

	// An unwritable state destination must preserve the stored preference while
	// rendering the attempted toggle selection with an error toast and no success.
	require.NoError(t, os.Remove(statePath))
	require.NoError(t, os.Mkdir(statePath, 0700))
	rec = request(http.MethodPost, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, coordinator.Snapshot().AutomaticUpdates)
	require.Contains(t, rec.Body.String(), "data-update-settings-error")
	require.Contains(t, rec.Body.String(), "Please try again.")
	require.Contains(t, rec.Body.String(), "window.showToast")
	require.NotContains(t, rec.Body.String(), "Update settings saved.")
	require.NotContains(t, rec.Body.String(), " checked")
	require.NoError(t, os.Remove(statePath))
	rec = request(http.MethodPost, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, coordinator.Snapshot().AutomaticUpdates)
	require.Contains(t, rec.Body.String(), "Update settings saved.")
	require.NotContains(t, rec.Body.String(), "<span hidden data-update-settings-error")
}
