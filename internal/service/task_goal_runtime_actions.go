package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
)

const taskGoalRuntimeActor = "assistant"

// TaskGoalRuntimeToolInput is the shared input shape for task-goal runtime tools.
type TaskGoalRuntimeToolInput struct {
	TaskID     string `json:"task_id"`
	Title      string `json:"title"`
	Goal       string `json:"goal"`
	GoalID     string `json:"goal_id"`
	Reason     string `json:"reason"`
	BlockerKey string `json:"blocker_key"`
}

// TaskGoalRuntimeTaskResolver resolves a decoded task-goal tool request to a task ID.
type TaskGoalRuntimeTaskResolver func(context.Context, TaskGoalRuntimeToolInput) (string, error)

// TaskGoalRuntimeAuthorizer performs surface-specific authorization for a tool.
type TaskGoalRuntimeAuthorizer func(context.Context, string) error

type TaskGoalRuntimeActionOptions struct {
	TaskGoalSvc         *TaskGoalService
	ResolveTaskID       TaskGoalRuntimeTaskResolver
	AuthorizeStatusTool TaskGoalRuntimeAuthorizer
}

// BuildTaskGoalRuntimeActionHandlers constructs the canonical handlers for all
// task-goal runtime tools. Surface-specific callers provide only task resolution
// and optional status-tool authorization.
func BuildTaskGoalRuntimeActionHandlers(opts TaskGoalRuntimeActionOptions) map[string]chatcontrol.RuntimeActionHandler {
	core := taskGoalRuntimeActionCore{opts: opts}
	return map[string]chatcontrol.RuntimeActionHandler{
		"set_task_goal": func(ctx context.Context, input json.RawMessage) (string, error) {
			req, taskID, err := core.decodeAndResolve(ctx, input)
			if err != nil {
				return "", err
			}
			goal, err := opts.TaskGoalSvc.SetGoal(ctx, taskID, req.Goal, GoalOptions{Actor: taskGoalRuntimeActor})
			if err != nil {
				return "", err
			}
			return taskGoalRuntimeJSON(goal)
		},
		"clear_task_goal": func(ctx context.Context, input json.RawMessage) (string, error) {
			_, taskID, err := core.decodeAndResolve(ctx, input)
			if err != nil {
				return "", err
			}
			if err := opts.TaskGoalSvc.ClearGoal(ctx, taskID, taskGoalRuntimeActor); err != nil {
				return "", err
			}
			goal, _ := opts.TaskGoalSvc.GetGoal(ctx, taskID)
			return taskGoalRuntimeJSON(goal)
		},
		"get_task_goal": func(ctx context.Context, input json.RawMessage) (string, error) {
			_, taskID, err := core.decodeAndResolve(ctx, input)
			if err != nil {
				return "", err
			}
			goal, err := opts.TaskGoalSvc.GetGoal(ctx, taskID)
			if err != nil {
				return "", err
			}
			return taskGoalRuntimeJSON(goal)
		},
		"pause_task_goal": func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.executeStatusMutation(ctx, input, func(taskID string) error {
				return opts.TaskGoalSvc.PauseGoal(ctx, taskID, taskGoalRuntimeActor)
			})
		},
		"resume_task_goal": func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.executeStatusMutation(ctx, input, func(taskID string) error {
				return opts.TaskGoalSvc.ResumeGoal(ctx, taskID, taskGoalRuntimeActor)
			})
		},
		"mark_task_goal_achieved": func(ctx context.Context, input json.RawMessage) (string, error) {
			if err := core.authorize(ctx, "mark_task_goal_achieved"); err != nil {
				return "", err
			}
			req, taskID, err := core.decodeAndResolve(ctx, input)
			if err != nil {
				return "", err
			}
			goal, err := opts.TaskGoalSvc.MarkAchieved(ctx, taskID, req.GoalID, req.Reason)
			if err != nil {
				return "", err
			}
			return taskGoalRuntimeJSON(goal)
		},
		"report_task_goal_blocked": func(ctx context.Context, input json.RawMessage) (string, error) {
			if err := core.authorize(ctx, "report_task_goal_blocked"); err != nil {
				return "", err
			}
			req, taskID, err := core.decodeAndResolve(ctx, input)
			if err != nil {
				return "", err
			}
			goal, err := opts.TaskGoalSvc.RecordBlockedReport(ctx, taskID, req.GoalID, req.BlockerKey, req.Reason)
			if err != nil {
				return "", err
			}
			return taskGoalRuntimeJSON(goal)
		},
	}
}

type taskGoalRuntimeActionCore struct {
	opts TaskGoalRuntimeActionOptions
}

func (c taskGoalRuntimeActionCore) decodeAndResolve(ctx context.Context, input json.RawMessage) (TaskGoalRuntimeToolInput, string, error) {
	if c.opts.TaskGoalSvc == nil {
		return TaskGoalRuntimeToolInput{}, "", fmt.Errorf("task goal service unavailable")
	}
	if c.opts.ResolveTaskID == nil {
		return TaskGoalRuntimeToolInput{}, "", fmt.Errorf("task goal resolver unavailable")
	}
	var req TaskGoalRuntimeToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return req, "", err
	}
	taskID, err := c.opts.ResolveTaskID(ctx, req)
	if err != nil {
		return req, "", err
	}
	return req, taskID, nil
}

func (c taskGoalRuntimeActionCore) executeStatusMutation(ctx context.Context, input json.RawMessage, fn func(string) error) (string, error) {
	_, taskID, err := c.decodeAndResolve(ctx, input)
	if err != nil {
		return "", err
	}
	if err := fn(taskID); err != nil {
		return "", err
	}
	goal, _ := c.opts.TaskGoalSvc.GetGoal(ctx, taskID)
	return taskGoalRuntimeJSON(goal)
}

func (c taskGoalRuntimeActionCore) authorize(ctx context.Context, toolName string) error {
	if c.opts.AuthorizeStatusTool == nil {
		return nil
	}
	return c.opts.AuthorizeStatusTool(ctx, toolName)
}

func taskGoalRuntimeJSON(goal *models.TaskGoal) (string, error) {
	payload := map[string]any{"ok": true, "goal": goal}
	if goal != nil {
		payload["task_id"] = goal.TaskID
	}
	b, err := json.Marshal(payload)
	return string(b), err
}
