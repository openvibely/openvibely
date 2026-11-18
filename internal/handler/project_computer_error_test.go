package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProjectComputerErrorAlert(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	p := createProject(t, h, "Computer errors")
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, &service.LocalComputerProvisioner{})
	e := echo.New()
	e.POST("/projects/:id/computer/error", h.ProjectComputerError)
	request := func(query, body, origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/projects/"+p.ID+"/computer/error"+query, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, 204, request("", `{"message":"Desktop authentication failed."}`, ""))
	require.Equal(t, 403, request("", `{"message":"Blocked"}`, "https://other.example"))
	require.Equal(t, 404, request("?task_id=foreign", `{"message":"Blocked"}`, ""))
	require.Equal(t, 400, request("", `{"message":" "}`, ""))
	alerts, err := h.alertSvc.ListByProject(t.Context(), p.ID, 10)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, models.SeverityError, alerts[0].Severity)
	require.Equal(t, "Desktop authentication failed.", alerts[0].Message)
}
