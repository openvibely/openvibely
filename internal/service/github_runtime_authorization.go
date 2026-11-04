package service

import (
	"context"
	"encoding/json"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Production uses the worker's authorization stores. Unconfigured embedded
// runtimes retain compatibility, as with the shared worker entry points.
func guardGitHubRuntimeActions(handlers map[string]chatcontrol.RuntimeActionHandler, opts githubIssueRuntimeOptions) {
	worker := opts.WorkerSvc
	if worker == nil || worker.runOrganizations == nil {
		return
	}
	for name, handler := range handlers {
		permission := authz.Manage
		switch name {
		case "github_get_issue", "github_get_project_inbox", "github_is_actor_authorized", "github_list_existing_automation_issues":
			permission = authz.View
		}
		handlers[name] = func(ctx context.Context, input json.RawMessage) (string, error) {
			ctx, err := authorizeGitHubRuntime(ctx, opts, permission)
			if err != nil {
				return "", err
			}
			return handler(ctx, input)
		}
	}
}

// Reused at provider mutation boundaries after any intervening network reads.
func authorizeGitHubRuntime(ctx context.Context, opts githubIssueRuntimeOptions, permission authz.Permission) (context.Context, error) {
	worker, projectID := opts.WorkerSvc, opts.ProjectID
	if worker == nil || worker.runOrganizations == nil {
		return ctx, nil
	}
	ctx = worker.reviewerRecoveryContext(ctx)
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok || principal.UserID == "" {
		if !worker.runAuthOff {
			return ctx, authz.ErrDenied
		}
		principal.UserID = models.BootstrapUserID
	}
	user, err := worker.runOrganizations.UserByID(ctx, principal.UserID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || user.Role == "" {
		return ctx, authz.ErrDenied
	}
	ctx = models.WithPrincipal(ctx, user.Principal)
	if err := (&authz.Authorizer{Store: worker.runAccess}).Require(ctx, projectID, permission); err != nil {
		return ctx, err
	}
	taskID, executionID := opts.TaskID, opts.ExecutionID
	if contextTask, contextExecution, ok := AutomationExecutionFromContext(ctx); ok {
		if taskID != "" && (taskID != contextTask || executionID != contextExecution) {
			return ctx, authz.ErrDenied
		}
		taskID, executionID = contextTask, contextExecution
	}
	if taskID != "" || executionID != "" {
		if taskID == "" || executionID == "" || worker.runIdentities == nil || opts.TaskRepo == nil {
			return ctx, authz.ErrDenied
		}
		task, err := opts.TaskRepo.GetByID(ctx, taskID)
		if err != nil {
			return ctx, err
		}
		if task == nil || task.ProjectID != projectID {
			return ctx, authz.ErrDenied
		}
		if !worker.runAuthOff && authz.UnmappedChannelOrigin(task.CreatedVia) {
			return ctx, authz.ErrDenied
		}
		savedUser, found, err := worker.runIdentities.ExecutionUser(ctx, executionID)
		if err != nil {
			return ctx, err
		}
		if found && savedUser != user.UserID {
			return ctx, authz.ErrDenied
		}
		if !found && !worker.runAuthOff {
			return ctx, authz.ErrDenied
		}
		if err := worker.runIdentities.CheckExecutionGoal(ctx, executionID, taskID); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}
