package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// ProjectComputerError records client-visible failures, including errors that
// originate in the viewer rather than in a server operation.
func (h *Handler) ProjectComputerError(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 8192)).Decode(&input); err != nil {
		return echo.NewHTTPError(400, "Invalid computer error")
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" || len(input.Message) > 4000 {
		return echo.NewHTTPError(400, "Invalid computer error")
	}
	if h.alertSvc == nil {
		return echo.NewHTTPError(503, "Alerts unavailable")
	}
	alert := &models.Alert{ProjectID: c.Param("id"), Type: models.AlertCustom, Severity: models.SeverityError, Source: "project_computer", Title: "Project computer error", Message: input.Message}
	if task := c.QueryParam("task_id"); task != "" {
		alert.TaskID = &task
	}
	if err := h.alertSvc.Create(c.Request().Context(), alert); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
