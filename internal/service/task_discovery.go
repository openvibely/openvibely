package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
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

// ViewSwarmRequest is the decoded input for the read-only view_swarm tool.
type ViewSwarmRequest struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
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

type taskDiscoveryFilterSummary struct {
	Query    string `json:"query"`
	Category string `json:"category"`
	Status   string `json:"status"`
}

type taskDiscoveryResult struct {
	OK      bool                       `json:"ok"`
	Tasks   []taskDiscoverySummary     `json:"tasks"`
	Count   int                        `json:"count"`
	Total   int                        `json:"total"`
	Limit   int                        `json:"limit"`
	Offset  int                        `json:"offset"`
	HasMore bool                       `json:"has_more"`
	Filter  taskDiscoveryFilterSummary `json:"filter"`
	Note    string                     `json:"note,omitempty"`
}

type swarmTaskSummary struct {
	TaskID         string `json:"task_id"`
	Title          string `json:"title"`
	Category       string `json:"category"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	UpdatedAt      string `json:"updated_at"`
	SwarmRole      string `json:"swarm_role"`
	SwarmStatus    string `json:"swarm_status,omitempty"`
	SwarmSequence  int    `json:"swarm_sequence,omitempty"`
	ParentTaskID   string `json:"parent_task_id,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
	MergeStatus    string `json:"merge_status,omitempty"`
	HasDiff        bool   `json:"has_diff,omitempty"`
}

type viewSwarmResult struct {
	OK              bool               `json:"ok"`
	IsSwarm         bool               `json:"is_swarm"`
	Message         string             `json:"message,omitempty"`
	RequestedTaskID string             `json:"requested_task_id,omitempty"`
	ResolvedFrom    string             `json:"resolved_from,omitempty"`
	ParentTaskID    string             `json:"parent_task_id,omitempty"`
	Parent          *swarmTaskSummary  `json:"parent,omitempty"`
	Children        []swarmTaskSummary `json:"children"`
	ChildCount      int                `json:"child_count"`
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
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", fmt.Errorf("list_tasks: %w", err)
	}

	query := strings.TrimSpace(req.Query)
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
		Query:    query,
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
		Filter: taskDiscoveryFilterSummary{
			Query:    query,
			Category: category,
			Status:   status,
		},
	}
	if result.Total == 0 && !result.HasMore {
		result.Note = "No tasks matched this exact list_tasks query/filter in the current project; has_more=false means there are no further pages for these parameters."
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ExecuteViewSwarmTool returns a compact parent-centered swarm hierarchy for one
// current-project task selected by id or exact title. Children passed by id resolve
// to their parent hierarchy. Non-swarm tasks return a controlled non-swarm payload.
func ExecuteViewSwarmTool(ctx context.Context, taskRepo *repository.TaskRepo, projectID string, input json.RawMessage) (string, error) {
	if taskRepo == nil {
		return "", fmt.Errorf("view_swarm: task repository unavailable")
	}
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("view_swarm: no current project — cannot inspect swarms without a project context")
	}

	var req ViewSwarmRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", fmt.Errorf("view_swarm: %w", err)
	}
	taskID := strings.TrimSpace(req.TaskID)
	title := strings.TrimSpace(req.Title)
	if taskID == "current" {
		return "", fmt.Errorf("view_swarm: task_id current is only valid in a persisted task thread")
	}
	if taskID == "" && title == "" {
		return "", fmt.Errorf("view_swarm requires task_id or exact title")
	}
	if taskID != "" && title != "" {
		return "", fmt.Errorf("view_swarm accepts task_id or title, not both")
	}

	task, err := taskRepo.GetTaskForSwarmInspection(ctx, projectID, taskID, title)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", fmt.Errorf("view_swarm: task not found in current project")
	}
	requestedTaskID := task.ID
	resolvedFrom := "parent"

	if models.IsSwarmChildRole(task.SwarmRole) {
		if task.ParentTaskID == nil || strings.TrimSpace(*task.ParentTaskID) == "" {
			return marshalViewSwarmResult(viewSwarmResult{
				OK:              true,
				IsSwarm:         false,
				Message:         fmt.Sprintf("Task %s has swarm child role %q but no parent_task_id; cannot resolve a swarm hierarchy.", task.ID, task.SwarmRole),
				RequestedTaskID: requestedTaskID,
				ResolvedFrom:    "child_without_parent",
				Children:        []swarmTaskSummary{},
			})
		}
		parent, err := taskRepo.GetTaskForSwarmInspection(ctx, projectID, strings.TrimSpace(*task.ParentTaskID), "")
		if err != nil {
			return "", err
		}
		if parent == nil {
			return "", fmt.Errorf("view_swarm: parent task not found in current project")
		}
		task = parent
		resolvedFrom = "child"
	}

	if task.SwarmRole != models.SwarmRoleParent {
		return marshalViewSwarmResult(viewSwarmResult{
			OK:              true,
			IsSwarm:         false,
			Message:         fmt.Sprintf("Task %s is not a swarm parent or child.", task.ID),
			RequestedTaskID: requestedTaskID,
			ResolvedFrom:    "non_swarm",
			Children:        []swarmTaskSummary{},
		})
	}

	children, err := taskRepo.ListSwarmChildrenForInspection(ctx, projectID, task.ID)
	if err != nil {
		return "", err
	}
	childSummaries := make([]swarmTaskSummary, 0, len(children))
	for i := range children {
		childSummaries = append(childSummaries, buildSwarmTaskSummary(children[i]))
	}
	parentSummary := buildSwarmTaskSummary(*task)
	return marshalViewSwarmResult(viewSwarmResult{
		OK:              true,
		IsSwarm:         true,
		RequestedTaskID: requestedTaskID,
		ResolvedFrom:    resolvedFrom,
		ParentTaskID:    task.ID,
		Parent:          &parentSummary,
		Children:        childSummaries,
		ChildCount:      len(childSummaries),
	})
}

func marshalViewSwarmResult(result viewSwarmResult) (string, error) {
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

func buildSwarmTaskSummary(t models.Task) swarmTaskSummary {
	summary := swarmTaskSummary{
		TaskID:        t.ID,
		Title:         t.Title,
		Category:      string(t.Category),
		Status:        string(t.Status),
		Priority:      t.Priority,
		UpdatedAt:     t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		SwarmRole:     string(t.SwarmRole),
		SwarmStatus:   strings.TrimSpace(t.SwarmStatus),
		SwarmSequence: t.SwarmSequence,
		MergeStatus:   string(t.MergeStatus),
	}
	if t.ParentTaskID != nil && strings.TrimSpace(*t.ParentTaskID) != "" {
		summary.ParentTaskID = strings.TrimSpace(*t.ParentTaskID)
	}
	if branch := strings.TrimSpace(t.WorktreeBranch); branch != "" {
		summary.WorktreeBranch = branch
	}
	summary.HasDiff = strings.TrimSpace(t.WorktreeBranch) != "" || t.MergeStatus != models.MergeStatusNone
	return summary
}
