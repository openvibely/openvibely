package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// listTasksMaxLimit bounds a single list_tasks page so discovery stays cheap and
// the result payload never grows without limit.
const listTasksMaxLimit = 50

// listTasksDefaultLimit is used when the caller does not request an explicit limit.
const listTasksDefaultLimit = 20

// listTasksAllowedCategories bounds the category filter to real, non-chat categories.
var listTasksAllowedCategories = map[string]bool{
	string(models.CategoryActive):    true,
	string(models.CategoryBacklog):   true,
	string(models.CategoryScheduled): true,
	string(models.CategoryCompleted): true,
}

// listTasksAllowedStatuses bounds the status filter to known task statuses.
var listTasksAllowedStatuses = map[string]bool{
	string(models.StatusPending):   true,
	string(models.StatusQueued):    true,
	string(models.StatusRunning):   true,
	string(models.StatusCompleted): true,
	string(models.StatusFailed):    true,
	string(models.StatusCancelled): true,
	string(models.StatusBlocked):   true,
}

// ListTasksRequest is the decoded input for the read-only list_tasks discovery tool.
type ListTasksRequest struct {
	Query    string `json:"query"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

// taskDiscoverySummary is the compact, read-only projection returned for each task.
// It carries just enough identity for existing exact-target task actions (task_id,
// title) plus lightweight triage fields.
type taskDiscoverySummary struct {
	TaskID       string `json:"task_id"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	Status       string `json:"status"`
	Priority     int    `json:"priority"`
	UpdatedAt    string `json:"updated_at"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	SwarmRole    string `json:"swarm_role,omitempty"`
}

type taskDiscoveryResult struct {
	OK      bool                   `json:"ok"`
	Tasks   []taskDiscoverySummary `json:"tasks"`
	Count   int                    `json:"count"`
	Total   int                    `json:"total"`
	Limit   int                    `json:"limit"`
	Offset  int                    `json:"offset"`
	HasMore bool                   `json:"has_more"`
}

// ExecuteListTasksTool runs the bounded, read-only, current-project task discovery
// tool. It never crosses project boundaries and excludes internal chat rows. Returns
// a compact JSON summary payload with deterministic ordering and explicit pagination.
func ExecuteListTasksTool(ctx context.Context, taskRepo *repository.TaskRepo, projectID string, input json.RawMessage) (string, error) {
	if taskRepo == nil {
		return "", fmt.Errorf("list_tasks: task repository unavailable")
	}
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("list_tasks: no current project — cannot list tasks without a project context")
	}

	var req ListTasksRequest
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("list_tasks: invalid input: %w", err)
		}
	}

	category := strings.ToLower(strings.TrimSpace(req.Category))
	if category != "" && !listTasksAllowedCategories[category] {
		return "", fmt.Errorf("list_tasks: unsupported category %q", req.Category)
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if status != "" && !listTasksAllowedStatuses[status] {
		return "", fmt.Errorf("list_tasks: unsupported status %q", req.Status)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = listTasksDefaultLimit
	}
	if limit > listTasksMaxLimit {
		limit = listTasksMaxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	tasks, total, err := taskRepo.ListTasksForDiscovery(ctx, projectID, repository.TaskDiscoveryFilter{
		Query:    strings.TrimSpace(req.Query),
		Category: category,
		Status:   status,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return "", err
	}

	summaries := make([]taskDiscoverySummary, 0, len(tasks))
	for i := range tasks {
		summaries = append(summaries, buildTaskDiscoverySummary(tasks[i]))
	}

	result := taskDiscoveryResult{
		OK:      true,
		Tasks:   summaries,
		Count:   len(summaries),
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(summaries) < total,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildTaskDiscoverySummary(t models.Task) taskDiscoverySummary {
	summary := taskDiscoverySummary{
		TaskID:    t.ID,
		Title:     t.Title,
		Category:  string(t.Category),
		Status:    string(t.Status),
		Priority:  t.Priority,
		UpdatedAt: t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if t.ParentTaskID != nil && strings.TrimSpace(*t.ParentTaskID) != "" {
		summary.ParentTaskID = strings.TrimSpace(*t.ParentTaskID)
	}
	if role := strings.TrimSpace(string(t.SwarmRole)); role != "" {
		summary.SwarmRole = role
	}
	return summary
}
