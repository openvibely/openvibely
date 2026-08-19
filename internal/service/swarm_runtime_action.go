package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// SwarmTaskRuntimeInput is the canonical Go input shape for the advertised
// create_swarm_task runtime tool schema.
type SwarmTaskRuntimeInput struct {
	Title             string `json:"title"`
	Prompt            string `json:"prompt"`
	Goal              string `json:"goal"`
	ProjectID         string `json:"project_id"`
	Category          string `json:"category"`
	Priority          int    `json:"priority"`
	AgentID           string `json:"agent_id"`
	AgentDefinitionID string `json:"agent_definition_id"`
	Agent             string `json:"agent"`
	Tag               string `json:"tag"`
	MaxWorkers        int    `json:"max_workers"`
	WorkerIsolation   string `json:"worker_isolation"`
	ReviewerEnabled   *bool  `json:"reviewer_enabled"`
	MergerEnabled     *bool  `json:"merger_enabled"`
	StartImmediately  *bool  `json:"start_immediately"`
	MergeTargetBranch string `json:"merge_target_branch"`
}

type CreateSwarmTaskRuntimeOptions struct {
	ProjectID string
	Input     SwarmTaskRuntimeInput
	SwarmSvc  *SwarmService
	TaskSvc   *TaskService
}

func ExecuteCreateSwarmTaskRuntime(ctx context.Context, opts CreateSwarmTaskRuntimeOptions) (*models.Task, string, error) {
	if strings.TrimSpace(opts.Input.Title) == "" || strings.TrimSpace(opts.Input.Prompt) == "" {
		return nil, "", fmt.Errorf("create_swarm_task requires title and prompt")
	}
	projectID := strings.TrimSpace(opts.ProjectID)
	if projectID == "" {
		return nil, "", fmt.Errorf("create_swarm_task requires project_id")
	}
	swarmSvc := opts.SwarmSvc
	if swarmSvc == nil && opts.TaskSvc != nil {
		swarmSvc = opts.TaskSvc.swarmSvc
	}
	if swarmSvc == nil {
		return nil, "", fmt.Errorf("create_swarm_task: swarm service unavailable")
	}

	req := opts.Input
	category := models.CategoryActive
	if strings.EqualFold(strings.TrimSpace(req.Category), string(models.CategoryBacklog)) {
		category = models.CategoryBacklog
	}
	priority := req.Priority
	if priority == 0 {
		priority = 2
	}
	if priority < 1 || priority > 4 {
		return nil, "", fmt.Errorf("create_swarm_task: priority must be between 1 and 4")
	}
	tag := models.TaskTag(strings.TrimSpace(req.Tag))
	switch tag {
	case models.TagNone, models.TagFeature, models.TagBug:
	default:
		return nil, "", fmt.Errorf("create_swarm_task: tag must be bug or feature")
	}
	reviewerEnabled := true
	if req.ReviewerEnabled != nil {
		reviewerEnabled = *req.ReviewerEnabled
	}
	mergerEnabled := true
	if req.MergerEnabled != nil {
		mergerEnabled = *req.MergerEnabled
	}
	var agentID *string
	if trimmed := strings.TrimSpace(req.AgentID); trimmed != "" {
		agentID = &trimmed
	}
	var agentDefinitionID *string
	if strings.TrimSpace(req.AgentDefinitionID) != "" || strings.TrimSpace(req.Agent) != "" {
		resolved, err := resolveTaskCreationAgentDefinition(ctx, TaskCreationRequest{AgentDefinitionID: req.AgentDefinitionID, Agent: req.Agent}, projectID, opts.TaskSvc)
		if err != nil {
			return nil, "", fmt.Errorf("create_swarm_task: %w", err)
		}
		if resolved != "" {
			agentDefinitionID = &resolved
		}
	}

	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{
		ProjectID:         projectID,
		Title:             req.Title,
		Prompt:            req.Prompt,
		Goal:              req.Goal,
		Category:          category,
		Priority:          priority,
		AgentID:           agentID,
		AgentDefinitionID: agentDefinitionID,
		Tag:               tag,
		MaxWorkers:        req.MaxWorkers,
		WorkerIsolation:   req.WorkerIsolation,
		ReviewerEnabled:   reviewerEnabled,
		MergerEnabled:     mergerEnabled,
		StartImmediately:  req.StartImmediately,
		MergeTargetBranch: req.MergeTargetBranch,
	})
	if err != nil {
		return nil, "", err
	}
	return parent, CreateSwarmTaskRuntimeSummary(parent), nil
}

func CreateSwarmTaskRuntimeSummary(parent *models.Task) string {
	if parent == nil {
		return ""
	}
	plannerMessage := "Planner starts when the swarm parent is Active."
	return fmt.Sprintf("Created swarm task: %s.\n%s\n- \"%s\" (%s) [TASK_ID:%s]", parent.Title, plannerMessage, parent.Title, parent.Category, parent.ID)
}
