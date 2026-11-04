package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// authorizeTaskResource resolves the persisted object's project. A supplied or
// selected project is never used as evidence that an arbitrary object belongs
// to it. Existing boundary middleware additionally checks nested IDs/context.
func (h *Handler) authorizeTaskResource(c echo.Context) error {
	ctx := c.Request().Context()
	path := c.Path()
	taskID := ""
	permission := authz.Edit
	if c.Request().Method == http.MethodGet || c.Request().Method == http.MethodHead || path == "/tasks/:taskId/changes/live" || path == "/attachments/:id/download" {
		permission = authz.View
	}
	switch {
	case strings.HasPrefix(path, "/tasks/:taskId"):
		taskID = c.Param("taskId")
	case strings.HasPrefix(path, "/api/tasks/:id/"):
		taskID = c.Param("id")
	case strings.HasPrefix(path, "/schedules/"), strings.HasPrefix(path, "/api/schedules/"):
		id := c.Param("id")
		if id == "" {
			id = c.Param("scheduleId")
		}
		if id == "" {
			return nil
		}
		schedule, err := h.scheduleRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if schedule == nil {
			return authz.ErrDenied
		}
		taskID = schedule.TaskID
	case path == "/executions/:id":
		var err error
		taskID, err = h.execRepo.GetExecutionTaskID(ctx, c.Param("id"))
		if err != nil {
			return err
		}
		if taskID == "" {
			return authz.ErrDenied
		}
	case strings.HasPrefix(path, "/attachments/:id"):
		attachment, err := h.attachmentRepo.GetByID(ctx, c.Param("id"))
		if err != nil {
			return err
		}
		if attachment == nil {
			return authz.ErrDenied
		}
		taskID = attachment.TaskID
	case strings.HasPrefix(path, "/thread-inputs/"), strings.HasPrefix(path, "/chat/queued/"):
		input, err := h.threadInputRepo.GetByID(ctx, c.Param("inputId"))
		if err != nil {
			return err
		}
		if input == nil {
			return authz.ErrDenied
		}
		return h.projectAccessSvc.Require(ctx, input.ProjectID, permission)
	case path == "/tasks" || strings.HasPrefix(path, "/tasks/") || strings.HasPrefix(path, "/api/tasks/"):
		projectID := c.QueryParam("project_id")
		if path == "/tasks/batch-category" {
			projectID = c.FormValue("project_id")
			if projectID == "" {
				return authz.ErrDenied
			}
		} else if projectID == "" {
			var err error
			projectID, err = h.getCurrentProjectID(c)
			if err != nil {
				return err
			}
			// The handler must use exactly the project that was authorized, including
			// legacy requests that omitted project context.
			query := c.QueryParams()
			query.Set("project_id", projectID)
			c.Request().URL.RawQuery = query.Encode()
		}
		if path == "/tasks/backlog/sort" || path == "/tasks/completed/sort" {
			permission = authz.View
		}
		if err := h.projectAccessSvc.Require(ctx, projectID, permission); err != nil {
			return err
		}
		if path == "/tasks" && c.Request().Method == http.MethodPost && (formBoolEnabled(c, "auto_merge", false) || formBoolEnabled(c, "auto_merge_on_goal_achieved", false)) {
			return h.projectAccessSvc.Require(ctx, projectID, authz.Manage)
		}
		return nil
	default:
		return nil
	}
	if taskID == "" {
		return authz.ErrDenied
	}
	task, err := h.taskRepo.GetThreadRenderMetadata(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return authz.ErrDenied
	}
	// Check the ordinary permission before inspecting any submitted settings.
	if err := h.projectAccessSvc.Require(ctx, task.ProjectID, permission); err != nil {
		return err
	}
	if permission == authz.View {
		return nil
	}
	switch {
	case strings.Contains(path, "/worktree/"):
		permission = authz.Manage
	case strings.HasSuffix(path, "/swarm/rerun-merger"), strings.HasSuffix(path, "/swarm/rerun-integrator"):
		permission = authz.Manage
	case path == "/tasks/:taskId/details/property":
		switch c.FormValue("field") {
		case "auto_merge", "auto_merge_on_goal_achieved":
			permission = authz.Manage
		}
	case path == "/tasks/:taskId" && c.Request().Method == http.MethodPut:
		// The regular task edit form includes merge settings even when unchanged.
		// Editors may save ordinary fields, but cannot alter the merge policy.
		full, err := h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if full == nil {
			return authz.ErrDenied
		}
		if (c.FormValue("auto_merge_present") != "" && formBoolEnabled(c, "auto_merge", false) != full.AutoMerge) ||
			(c.FormValue("auto_merge_on_goal_achieved_present") != "" && formBoolEnabled(c, "auto_merge_on_goal_achieved", false) != full.AutoMergeOnGoalAchieved) ||
			(c.FormValue("merge_target_branch") != "" && c.FormValue("merge_target_branch") != full.MergeTargetBranch) {
			permission = authz.Manage
		}
	}
	return h.projectAccessSvc.Require(ctx, task.ProjectID, permission)
}
