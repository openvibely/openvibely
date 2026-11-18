package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProjectComputerSettingsPage(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	p := createProject(t, h, "Computer resources")
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, &service.LocalComputerProvisioner{})
	e := echo.New()
	e.GET("/settings/computer", h.ProjectComputerSettings)
	e.POST("/settings/computer", h.ProjectComputerSettings)
	request := func(method, body, origin string, fragment bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/settings/computer?project_id="+p.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if fragment {
			req.Header.Set("HX-Request", "true")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	for _, fragment := range []bool{false, true} {
		rec := request(http.MethodGet, "", "", fragment)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		for _, text := range []string{"Project Computer", "Computer resources", `name="cpus"`, `name="memory_gib"`, `name="pids_limit"`, `value="4096"`} {
			require.Contains(t, rec.Body.String(), text)
		}
	}
	rec := request(http.MethodPost, "cpus=oops&memory_gib=4&pids_limit=4096", "", true)
	require.Contains(t, rec.Body.String(), "Enter whole numbers")
	rec = request(http.MethodPost, "cpus=0&memory_gib=4&pids_limit=4096", "", true)
	require.Contains(t, rec.Body.String(), "1–64 CPUs")
	rec = request(http.MethodPost, "cpus=2&memory_gib=4&pids_limit=8192", "https://other.example", true)
	require.Equal(t, 403, rec.Code)
}
