package handler

import (
	"context"
	"encoding/json"
	"github.com/labstack/echo/v4"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"net/http"
	"sync"
	"time"
)

func (h *Handler) ProjectComputerImportFile(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	result, err := h.computerService(c).ImportHumanFile(c.Request().Context(), c.Param("id"), c.QueryParam("path"))
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	return c.Blob(200, "application/json", []byte(result))
}
func (h *Handler) ProjectComputerTeach(c echo.Context) error {
	if h.llmSvc == nil {
		return echo.NewHTTPError(503, "Model service unavailable")
	}
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	var demo service.ComputerDemonstration
	if err := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 28<<20)).Decode(&demo); err != nil {
		return echo.NewHTTPError(400, "Invalid demonstration")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Minute)
	defer cancel()
	if c.Request().Header.Get("Accept") == "application/x-ndjson" {
		c.Response().Header().Set("Content-Type", "application/x-ndjson")
		c.Response().Header().Set("Cache-Control", "no-store")
		c.Response().Header().Set("X-Accel-Buffering", "no")
		var mu sync.Mutex
		send := func(value any) {
			mu.Lock()
			defer mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if json.NewEncoder(c.Response()).Encode(value) != nil {
				cancel()
				return
			}
			c.Response().Flush()
		}
		ctx = llmcontracts.WithTextStream(ctx, func(text string) { send(map[string]string{"text": text}) })
		workflow, err := h.computerService(c).GenerateWorkflow(ctx, c.Param("id"), h.llmSvc, demo)
		if err != nil {
			send(map[string]string{"error": err.Error()})
		} else {
			send(map[string]any{"draft": workflow})
		}
		return nil
	}
	workflow, err := h.computerService(c).GenerateWorkflow(ctx, c.Param("id"), h.llmSvc, demo)
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	return c.JSON(200, workflow)
}

func (h *Handler) ProjectComputerWorkflows(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	svc := h.computerService(c)
	ctx := c.Request().Context()
	id := c.Param("id")
	c.Response().Header().Set("Cache-Control", "no-store")
	if c.Request().Method == http.MethodGet {
		items, err := svc.Workflows(ctx, id)
		if err != nil {
			return err
		}
		return c.JSON(200, items)
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	var input struct {
		Action string `json:"action"`
		models.ComputerWorkflow
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 40000)).Decode(&input); err != nil {
		return echo.NewHTTPError(400, "Invalid workflow")
	}
	switch input.Action {
	case "save":
		if err := svc.SaveWorkflow(ctx, id, &input.ComputerWorkflow); err != nil {
			return echo.NewHTTPError(400, err.Error())
		}
		return c.JSON(201, input.ComputerWorkflow)
	case "use":
		task, err := svc.UseWorkflow(ctx, id, input.ID, h.taskSvc)
		if err != nil {
			return echo.NewHTTPError(400, err.Error())
		}
		return c.JSON(201, map[string]string{"task_id": task.ID, "url": "/tasks/" + task.ID})
	case "delete":
		if err := svc.DeleteWorkflow(ctx, id, input.ID); err != nil {
			return err
		}
		return c.JSON(200, map[string]bool{"ok": true})
	default:
		return echo.NewHTTPError(400, "Choose save, use or delete")
	}
}
