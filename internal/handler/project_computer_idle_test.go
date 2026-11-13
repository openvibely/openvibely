package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerIdleSetting(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	p := createProject(t, h, "Idle timeout")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer backend.Close()
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: backend.URL}})
	e := echo.New()
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	send := func(zone, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/projects/"+p.ID+"/computer/idle-timeout", strings.NewReader(url.Values{"idle_minutes": {zone}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 403, send("60", "https://other.test").Code)
	require.Equal(t, 200, send("60", "").Code)
	require.Equal(t, 400, send("-1", "").Code)
	require.Equal(t, 400, send("invalid", "").Code)
	require.Equal(t, 400, send("10081", "").Code)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "/projects/"+p.ID+"/computer/status", nil))
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"idle_minutes":60`)
}
