package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// UpdateTaskDetailProperty saves one property without replacing the mounted thread.
func (h *Handler) UpdateTaskDetailProperty(c echo.Context) error {
	projectID := h.mutationProjectID(c)
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project context required")
	}
	field := c.FormValue("field")
	task, err := actTaskProperty(h, c.Request().Context(), taskPropertyArgs{
		TaskRef: TaskRef{TaskID: c.Param("taskId"), ProjectID: projectID},
		Field:   field, Value: strings.TrimSpace(c.FormValue("value")),
	})
	if err != nil {
		return err
	}
	switch field {
	case "auto_merge", "auto_merge_on_goal_achieved":
		return render(c, http.StatusOK, pages.TaskAutoMergePanel(task))
	case "prompt":
		return render(c, http.StatusOK, pages.TaskPromptPanel(task))
	}
	return h.GetTaskDetailStatus(c)
}

type taskPropertyArgs struct {
	TaskRef
	Field string
	Value string
}

func (a taskPropertyArgs) ref() TaskRef { return a.TaskRef }

var actTaskProperty = defineAction("task.property", ownTaskRef[taskPropertyArgs], func(h *Handler, ctx context.Context, a taskPropertyArgs) (*models.Task, error) {
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return nil, err
	}
	value := a.Value
	switch a.Field {
	case "agent_id":
		if value == "default" {
			model, err := h.selectTaskAgent(ctx, task.ProjectID, "default", "", false)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, err.Error())
			}
			value = model.ID
		}
		if value != "" {
			options, err := h.llmConfigRepo.ListBadgeOptions(ctx)
			if err != nil {
				return nil, err
			}
			found := false
			for _, option := range options {
				if option.ID == value {
					found = true
				}
			}
			if !found {
				return nil, statusErr(http.StatusBadRequest, "invalid model selection")
			}
		}
	case "agent_definition_id":
		if _, err := h.resolvePrimaryAgentDefinition(ctx, task.ProjectID, value); err != nil {
			return nil, err
		}
	case "auto_merge", "auto_merge_on_goal_achieved":
		if value != "true" && value != "false" {
			return nil, statusErr(http.StatusBadRequest, "invalid auto-merge setting")
		}
	case "priority":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 4 {
			return nil, statusErr(http.StatusBadRequest, "Priority must be between 1 and 4")
		}
	case "tag":
		tag, tagErr := models.NormalizeTaskTag(value)
		if tagErr != nil {
			return nil, statusErr(http.StatusBadRequest, tagErr.Error())
		}
		value = string(tag)
	case "prompt":
		if value == "" {
			return nil, statusErr(http.StatusBadRequest, "Prompt is required")
		}
	case "category":
		valid := false
		for _, category := range models.SelectableCategories {
			if string(category) == value {
				valid = true
			}
		}
		if !valid {
			return nil, statusErr(http.StatusBadRequest, "invalid category")
		}
		if value == string(models.CategoryScheduled) {
			schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
			if err != nil {
				return nil, err
			}
			if len(schedules) == 0 {
				return nil, statusErr(http.StatusBadRequest, "Add a schedule first")
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
		return task, err
	default:
		return nil, statusErr(http.StatusBadRequest, "unsupported task property")
	}
	if err := h.taskRepo.UpdateDetailProperty(ctx, task.ID, a.Field, value); err != nil {
		return nil, err
	}
	switch a.Field {
	case "auto_merge":
		task.AutoMerge = value == "true"
	case "auto_merge_on_goal_achieved":
		task.AutoMergeOnGoalAchieved = value == "true"
	case "prompt":
		task.Prompt = value
	}
	return task, nil
})
