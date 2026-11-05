package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageplugins"
)

// publishCustomPageTool publishes a bundle authored by the current task. The
// caller cannot choose a different task/project or execute a backend via this tool.
func (h *Handler) publishCustomPageTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.pagePlugins == nil {
		return "", errors.New("custom pages are unavailable")
	}
	if !params.IsTaskFollowup || params.TaskID == "" {
		return "", errors.New("publish_custom_page requires a persisted coding task; create a task to build the page")
	}
	task, err := h.taskRepo.GetByID(ctx, params.TaskID)
	if err != nil {
		return "", err
	}
	if task == nil || task.ProjectID != params.ProjectID || task.ProjectID == models.AllProjectsID || task.Category == models.CategoryChat || task.Category == pagePluginChatCategory {
		return "", errors.New("custom page must belong to the current coding task's project")
	}
	var req struct {
		PackagePath string `json:"package_path"`
		PageID      string `json:"page_id,omitempty"`
	}
	if !json.Valid(input) || len(input) > 65536 {
		return "", errors.New("invalid publication input")
	}
	if err := pageplugins.DecodeStrict(input, &req); err != nil {
		return "", err
	}
	workDir := task.WorktreePath
	if workDir == "" {
		project, err := h.projectSvc.GetByID(ctx, task.ProjectID)
		if err != nil {
			return "", err
		}
		if project != nil {
			workDir = project.RepoPath
		}
	}
	if workDir == "" {
		return "", errors.New("task has no workspace; configure the project's repository before creating a page")
	}
	if filepath.IsAbs(req.PackagePath) {
		return "", errors.New("package_path must be relative to the task workspace")
	}
	source, err := pageplugins.SafeJoin(workDir, req.PackagePath)
	if err != nil {
		return "", fmt.Errorf("package_path: %w", err)
	}
	// The installed copy must survive task worktree cleanup.
	workspace, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", err
	}
	managed, err := filepath.Abs(h.pagePlugins.Root())
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(workspace, managed); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("configure the plugin package store outside the task workspace")
	}
	result, err := h.pagePlugins.Publish(ctx, source, task.ProjectID, req.PageID, "task:"+task.ID)
	if err != nil {
		return "", err
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: task.ProjectID})
	}
	out := struct {
		InstanceID       string `json:"instance_id"`
		ProjectID        string `json:"project_id"`
		URL              string `json:"url"`
		Enabled          bool   `json:"enabled"`
		ApprovalRequired bool   `json:"approval_required"`
		SettingsURL      string `json:"settings_url,omitempty"`
		Message          string `json:"message"`
	}{InstanceID: result.Instance.ID, ProjectID: task.ProjectID, Enabled: result.Instance.Enabled, ApprovalRequired: result.ApprovalRequired,
		URL: "/p/" + url.PathEscape(result.Instance.ID) + "?project_id=" + url.QueryEscape(task.ProjectID), Message: "Page installed and available in the project's left rail. Return the page link to the user."}
	if result.ApprovalRequired {
		out.SettingsURL = "/settings/plugin-pages?project_id=" + url.QueryEscape(task.ProjectID)
		out.Message = "Page installed. Its native backend has NOT been executed. An administrator must approve the package and enable the page in Plugin pages settings. Return the approval link; do not claim the page is live."
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}
