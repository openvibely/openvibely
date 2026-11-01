package handler

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
)

func (h *Handler) ProjectComputerJobs(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	if c.Request().Method == "POST" {
		if !computerSameOrigin(c.Request()) {
			return echo.NewHTTPError(403)
		}
		var args struct {
			ID string `json:"job_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1024)).Decode(&args) != nil {
			return echo.NewHTTPError(400, "Choose a job")
		}
		if err := h.computerService(c).CancelJob(c.Request().Context(), c.Param("id"), args.ID); err != nil {
			return echo.NewHTTPError(400, err.Error())
		}
	}
	data, err := h.computerService(c).Jobs(c.Request().Context(), c.Param("id"), "")
	if err != nil {
		return echo.NewHTTPError(503, "Computer jobs unavailable")
	}
	return c.Blob(http.StatusOK, "application/json", data)
}
