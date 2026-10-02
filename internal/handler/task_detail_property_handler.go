package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// UpdateTaskDetailProperty saves one property without replacing the mounted thread.
func (h *Handler) UpdateTaskDetailProperty(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := h.mutationProjectID(c)
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project context required")
	}
	task, err := h.requireTaskInRequestProject(ctx, c.Param("taskId"), projectID)
	if err != nil {
		return err
	}
	field, value := c.FormValue("field"), strings.TrimSpace(c.FormValue("value"))
	switch field {
	case "agent_id":
		if task.SwarmRole == models.SwarmRoleParent {
			return echo.NewHTTPError(http.StatusBadRequest, "Use the swarm editor to change its model")
		}
		if value == "default" {
			model, err := h.selectDefaultAgent(ctx, false)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			value = model.ID
		}
		if value != "" {
			options, err := h.llmConfigRepo.ListBadgeOptions(ctx)
			if err != nil {
				return err
			}
			found := false
			for _, option := range options {
				if option.ID == value {
					found = true
				}
			}
			if !found {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid model selection")
			}
		}
	case "agent_definition_id":
		if _, err := h.resolvePrimaryAgentDefinition(ctx, task.ProjectID, value); err != nil {
			return err
		}
	case "auto_merge", "auto_merge_on_goal_achieved":
		if value != "true" && value != "false" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid auto-merge setting")
		}
	case "priority":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 4 {
			return echo.NewHTTPError(http.StatusBadRequest, "Priority must be between 1 and 4")
		}
	case "tag":
		if value != "" && value != "bug" && value != "feature" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid tag")
		}
	case "prompt":
		if value == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Prompt is required")
		}
	case "category":
		valid := false
		for _, category := range models.SelectableCategories {
			if string(category) == value {
				valid = true
			}
		}
		if !valid {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid category")
		}
		if value == string(models.CategoryScheduled) {
			schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			if err != nil {
				return err
			}
			if len(schedules) == 0 {
				return echo.NewHTTPError(http.StatusBadRequest, "Add a schedule first")
			}
		}
		// Match task edit semantics: do not start work merely by changing a property.
		if task.Category != models.TaskCategory(value) {
			if task.Category == models.CategoryActive && (task.Status == models.StatusRunning || task.Status == models.StatusQueued) {
				err = h.taskSvc.UpdateCategory(ctx, task.ID, models.TaskCategory(value))
			} else {
				err = h.taskRepo.UpdateCategory(ctx, task.ID, models.TaskCategory(value))
			}
		}
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported task property")
	}
	if err != nil {
		return err
	}
	if field != "category" {
		if err = h.taskRepo.UpdateDetailProperty(ctx, task.ID, field, value); err != nil {
			return err
		}
	}
	if field == "auto_merge" || field == "auto_merge_on_goal_achieved" {
		if field == "auto_merge" {
			task.AutoMerge = value == "true"
		} else {
			task.AutoMergeOnGoalAchieved = value == "true"
		}
		return render(c, http.StatusOK, pages.TaskAutoMergePanel(task))
	}
	if field == "prompt" {
		task.Prompt = value
		return render(c, http.StatusOK, pages.TaskPromptPanel(task))
	}
	return h.GetTaskDetailStatus(c)
}
