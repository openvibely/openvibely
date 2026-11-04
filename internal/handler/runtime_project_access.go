package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Wrap the handlers rather than only the web executor: API, follow-up and
// supplemental lifecycle executors all reuse this map. Service-owned/channel
// runtimes still need their own admission and durable run-as checks.
func (h *Handler) authorizeProjectRuntimeHandlers(projectID string, handlers map[string]chatcontrol.RuntimeActionHandler) {
	if h.projectAccessSvc == nil {
		return
	}
	for name, handler := range handlers {
		permission, protected := projectRuntimePermission(name)
		if !protected {
			continue
		}
		handlers[name] = func(ctx context.Context, input json.RawMessage) (string, error) {
			ctx = h.projectRuntimePrincipalContext(ctx)
			if err := h.requireProjectRuntimeAction(ctx, projectID, name, permission, input); err != nil {
				return "", err
			}
			return handler(ctx, input)
		}
	}
}

func (h *Handler) requireProjectRuntimeAction(ctx context.Context, projectID, name string, permission authz.Permission, input json.RawMessage) error {
	if def := chatcontrol.Get(name); def != nil {
		switch def.Domain {
		case chatcontrol.DomainSettings, chatcontrol.DomainModels, chatcontrol.DomainPersonality, chatcontrol.DomainAgents:
			principal, ok := models.PrincipalFromContext(ctx)
			if !ok || principal.UserID == "" || h.organizationSvc == nil {
				return authz.ErrDenied
			}
			user, err := h.organizationSvc.Store.UserByID(ctx, principal.UserID)
			if err != nil {
				return err
			}
			if user == nil || user.Disabled || !user.IsOrgAdmin() {
				return authz.ErrDenied
			}
		}
	}
	// Re-read membership on every invocation. Neither the model's arguments nor
	// a previously built runtime is authority for the caller's permissions.
	if err := h.projectAccessSvc.Require(ctx, projectID, permission); err != nil {
		return err
	}
	var args struct {
		AlertID                 string  `json:"alert_id"`
		AutomationID            string  `json:"automation_id"`
		ID                      string  `json:"id"`
		TaskID                  string  `json:"task_id"`
		ScheduleID              string  `json:"schedule_id"`
		ProjectID               string  `json:"project_id"`
		AutoMerge               *bool   `json:"auto_merge"`
		AutoMergeOnGoalAchieved *bool   `json:"auto_merge_on_goal_achieved"`
		MergeTargetBranch       *string `json:"merge_target_branch"`
	}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &args); err != nil {
		return err
	}
	if args.ProjectID != "" && args.ProjectID != projectID {
		return authz.ErrDenied
	}
	if name == "merge_task" || args.AutoMerge != nil || args.AutoMergeOnGoalAchieved != nil || args.MergeTargetBranch != nil {
		if err := h.projectAccessSvc.Require(ctx, projectID, authz.Manage); err != nil {
			return err
		}
	}
	if args.AutomationID != "" {
		if err := h.requireAutomationProject(ctx, projectID, args.AutomationID); err != nil {
			return err
		}
	}
	alertID := strings.TrimSpace(args.AlertID)
	if name == "delete_alert" || name == "toggle_alert" {
		alertID = strings.TrimSpace(args.ID)
	}
	if alertID != "" {
		if h.alertSvc == nil {
			return authz.ErrDenied
		}
		alert, err := h.alertSvc.GetByID(ctx, projectID, alertID)
		if errors.Is(err, persistence.ErrAlertNotFound) {
			return authz.ErrDenied
		}
		if err != nil {
			return err
		}
		if alert == nil {
			return authz.ErrDenied
		}
	}
	taskID := strings.TrimSpace(args.TaskID)
	if name == "edit_task" {
		taskID = strings.TrimSpace(args.ID)
	}
	if args.ScheduleID != "" {
		schedule, err := h.scheduleRepo.GetByID(ctx, args.ScheduleID)
		if err != nil {
			return err
		}
		if schedule == nil {
			return authz.ErrDenied
		}
		if err := h.requireRuntimeTaskProject(ctx, schedule.TaskID, projectID); err != nil {
			return err
		}
	}
	// "current" and title resolution are performed by the existing scoped
	// handlers. Explicit IDs must never cross the runtime's project boundary.
	if taskID != "" && taskID != "current" {
		return h.requireRuntimeTaskProject(ctx, taskID, projectID)
	}
	return nil
}

func (h *Handler) requireRuntimeTaskProject(ctx context.Context, taskID, projectID string) error {
	task, err := h.taskRepo.GetThreadRenderMetadata(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.ProjectID != projectID {
		return authz.ErrDenied
	}
	return nil
}

// The executor may return cached discovery results without invoking a handler.
// Authorize before that cache as well as at direct handler entry points.
func (h *Handler) authorizeProjectRuntimeExecutor(projectID string, next llmcontracts.RuntimeToolExecutor) llmcontracts.RuntimeToolExecutor {
	if h.projectAccessSvc == nil {
		return next
	}
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		ctx = h.projectRuntimePrincipalContext(ctx)
		name = strings.ToLower(strings.TrimSpace(name))
		if permission, protected := projectRuntimePermission(name); protected {
			if err := h.requireProjectRuntimeAction(ctx, projectID, name, permission, input); err != nil {
				return "", true, true, err
			}
		}
		return next(ctx, name, input)
	}
}

// Interactive project selection must be resolved before the pre-cache check.
// Persisted task and automation runtimes remain pinned to their owning project.
func (h *Handler) authorizeSelectedProjectRuntimeExecutor(params streamingResponseParams, surface chatcontrol.Surface, next llmcontracts.RuntimeToolExecutor) llmcontracts.RuntimeToolExecutor {
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		projectID := params.ProjectID
		name = strings.ToLower(strings.TrimSpace(name))
		if (surface == chatcontrol.SurfaceWeb || surface == chatcontrol.SurfaceAPI) && !params.IsTaskFollowup && params.AutomationContext == nil && chatcontrol.SupportsProjectSelection(name) {
			selected, _, err := decodeChatProjectSelection(name, input)
			if err != nil {
				return "", true, true, err
			}
			if selected != "" {
				projectID = selected
			}
		}
		return h.authorizeProjectRuntimeExecutor(projectID, next)(ctx, name, input)
	}
}

// Only explicitly disabled authentication permits anonymous local execution.
// In authenticated modes, background work must supply its persisted run-as user.
func (h *Handler) projectRuntimePrincipalContext(ctx context.Context) context.Context {
	if _, ok := models.PrincipalFromContext(ctx); !ok && h.authMode == auth.AuthModeDisabled {
		return models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	}
	return ctx
}

func projectRuntimePermission(name string) (authz.Permission, bool) {
	def := chatcontrol.Get(name)
	if def == nil {
		return "", false
	}
	switch def.Domain {
	case chatcontrol.DomainTasks, chatcontrol.DomainSchedules, chatcontrol.DomainAlerts:
		if def.Access == chatcontrol.AccessRead {
			return authz.View, true
		}
		return authz.Edit, true
	case chatcontrol.DomainAnalytics, chatcontrol.DomainPulse, chatcontrol.DomainMemory, chatcontrol.DomainSettings, chatcontrol.DomainModels, chatcontrol.DomainPersonality, chatcontrol.DomainAgents:
		return authz.View, true
	case chatcontrol.DomainAutomations:
		// Description preview invokes a model even though the registry labels its
		// draft output read-only. Treat it as builder access, not a passive read.
		if def.Access == chatcontrol.AccessRead && name != "preview_automation_description" {
			return authz.View, true
		}
		return authz.Manage, true
	default:
		return "", false
	}
}
