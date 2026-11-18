package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
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
		require.NotContains(t, rec.Body.String(), "Open project computer")
		for _, text := range []string{"Project Computer", "Computer resources", `name="cpus"`, `name="memory_gib"`, `name="pids_limit"`, `value="4096"`, `name="enabled"`, `name="idle_minutes"`, `name="time_zone"`, "data-computer-grants", "computer-settings-project"} {
			require.Contains(t, rec.Body.String(), text)
		}
	}
	rec := request(http.MethodPost, "cpus=oops&memory_gib=4&pids_limit=4096", "", true)
	require.Contains(t, rec.Body.String(), "Enter whole numbers")
	rec = request(http.MethodPost, "cpus=0&memory_gib=4&pids_limit=4096&idle_minutes=30&time_zone=UTC", "", true)
	require.Contains(t, rec.Body.String(), "1–64 CPUs")
	rec = request(http.MethodPost, "cpus=2&memory_gib=4&pids_limit=8192", "https://other.example", true)
	require.Equal(t, 403, rec.Code)
}

func TestComputerSettingsSaveAndProjectScope(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h, _, _ := setupTestHandler(t)
	first := createProject(t, h, "First computer")
	second := createProject(t, h, "Second computer")
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, &service.LocalComputerProvisioner{})
	e := echo.New()
	e.GET("/settings/computer", h.ProjectComputerSettings)
	e.POST("/settings/computer", h.ProjectComputerSettings)
	req := httptest.NewRequest(http.MethodPost, "/settings/computer?project_id="+first.ID, strings.NewReader("enabled=on&cpus=2&memory_gib=4&pids_limit=8192&idle_minutes=60&time_zone=UTC&preview_quality=low"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "Settings saved")
	settings, err := h.computerSvc.Settings(t.Context(), first.ID)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, "low", settings.PreviewQuality)
	require.Equal(t, 8192, settings.Resources.PidsLimit)
	require.Equal(t, 60, settings.IdleMinutes)
	other, err := h.computerSvc.Settings(t.Context(), second.ID)
	require.NoError(t, err)
	require.False(t, other.Enabled)
	require.Equal(t, "balanced", other.PreviewQuality)
	require.Equal(t, 4096, other.Resources.PidsLimit)
	req = httptest.NewRequest(http.MethodGet, "/settings/computer?project_id="+models.AllProjectsID, nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "Select a project to configure")
	require.Contains(t, rec.Body.String(), "First computer")
	require.Contains(t, rec.Body.String(), "Second computer")
	require.NotContains(t, rec.Body.String(), "data-computer-settings-form")
}
