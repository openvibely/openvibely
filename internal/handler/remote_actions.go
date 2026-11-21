package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
)

// serverAction is work that must run on the server owning its task or project
// (git, worktrees, files, agents). Local entities run it directly; remote ones
// run it over that server's live socket. Callers render the result locally.
type serverAction struct {
	args reflect.Type
	// owner returns the project the args act on and the permission required.
	owner func(h *Handler, ctx context.Context, args any) (string, authz.Permission, error)
	run   func(h *Handler, ctx context.Context, args any) (any, error)
}

var serverActions = map[string]serverAction{}

// registerAction declares a server action. owner must resolve the project
// from persisted rows, never from caller-supplied project IDs alone.
func registerAction[A, R any](name string, owner func(*Handler, context.Context, A) (string, authz.Permission, error), run func(*Handler, context.Context, A) (R, error)) {
	if _, dup := serverActions[name]; dup {
		panic("duplicate server action " + name)
	}
	serverActions[name] = serverAction{
		args: reflect.TypeFor[A](),
		owner: func(h *Handler, ctx context.Context, a any) (string, authz.Permission, error) {
			return owner(h, ctx, a.(A))
		},
		run: func(h *Handler, ctx context.Context, a any) (any, error) { return run(h, ctx, a.(A)) },
	}
}

// defineAction registers an action and returns its caller, which runs it on
// the server owning any remote ID in args, or here. Local callers have already
// passed the HTTP access boundary, so owner only validates there.
func defineAction[A, R any](name string, owner func(*Handler, context.Context, A) (string, authz.Permission, error), run func(*Handler, context.Context, A) (R, error)) func(*Handler, context.Context, A) (R, error) {
	registerAction(name, owner, run)
	return func(h *Handler, ctx context.Context, args A) (R, error) {
		var out R
		server, err := remote.ActionServer(args)
		if err != nil {
			return out, statusErr(http.StatusBadRequest, err.Error())
		}
		if server != "" {
			if !h.remoteSignedIn(ctx, server) {
				return out, reconnectError{server}
			}
			err := h.remoteHub().Act(ctx, server, name, args, &out)
			return out, actionHTTPError(err)
		}
		if _, _, err := owner(h, ctx, args); err != nil {
			return out, actionHTTPError(err)
		}
		out, err = run(h, ctx, args)
		return out, actionHTTPError(err)
	}
}

// remoteAct runs a server action on the saved server owning entityID. It
// reports false for local entities, which callers handle directly.
func (h *Handler) remoteAct(ctx context.Context, entityID, name string, args, out any) (bool, error) {
	server, ok := remoteid.ServerOf(entityID)
	if !ok {
		return false, nil
	}
	if _, known := serverActions[name]; !known {
		return true, fmt.Errorf("unknown server action %s", name)
	}
	if !h.remoteSignedIn(ctx, server) {
		return true, reconnectError{server}
	}
	return true, actionHTTPError(h.remoteHub().Act(ctx, server, name, args, out))
}

// runRemoteAction answers an "op":"action" socket command from a hub client.
func (h *Handler) runRemoteAction(ctx context.Context, action remote.Action) remote.Result {
	a, ok := serverActions[action.Name]
	if !ok {
		return remote.EncodeResult(nil, remote.ErrUnsupported, nil)
	}
	args := reflect.New(a.args)
	if err := remote.Decode(action.Args, args.Interface()); err != nil {
		return remote.EncodeResult(nil, &remote.StatusError{Status: http.StatusBadRequest, Message: "invalid action arguments"}, nil)
	}
	projectID, permission, err := a.owner(h, ctx, args.Elem().Interface())
	if err == nil {
		err = h.requireActionProject(ctx, projectID, permission)
	}
	if err != nil {
		return remote.EncodeResult(nil, err, nil)
	}
	res, err := a.run(h, ctx, args.Elem().Interface())
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		err = &remote.StatusError{Status: httpErr.Code, Message: fmt.Sprint(httpErr.Message)}
	}
	return remote.EncodeResult(res, err, func(projectID string) bool {
		return h.requireActionProject(ctx, projectID, authz.Manage) == nil
	})
}

func (h *Handler) requireActionProject(ctx context.Context, projectID string, permission authz.Permission) error {
	if projectID == instanceActionProject {
		return h.requireActionAdmin(ctx)
	}
	if projectID == "" || models.IsAggregateProject(projectID) {
		return authz.ErrDenied
	}
	if h.projectAccessSvc == nil {
		return nil
	}
	return h.projectAccessSvc.Require(ctx, projectID, permission)
}

// actionHTTPError maps an action failure to the HTTP error its handler returned before.
func actionHTTPError(err error) error {
	var status *remote.StatusError
	if errors.As(err, &status) {
		return echo.NewHTTPError(status.Status, status.Message)
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr
	}
	if errors.Is(err, remote.ErrUnavailable) {
		return echo.NewHTTPError(http.StatusBadGateway, "remote server unavailable")
	}
	return err
}

var errNoModels = statusErr(http.StatusBadRequest, noModelsConfiguredMessage)

// noModelsResponse answers an action that failed because no models exist.
func noModelsResponse(c echo.Context, err error) (bool, error) {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) && httpErr.Message == noModelsConfiguredMessage {
		return true, noModelsConfiguredResponse(c)
	}
	return false, err
}

// statusErr reports a handler-visible failure from inside an action; it
// survives the trip to a remote caller.
func statusErr(status int, message string) error {
	return &remote.StatusError{Status: status, Message: message}
}

// taskActionOwner resolves a task argument's project.
func (h *Handler) taskActionOwner(ctx context.Context, taskID string, permission authz.Permission) (string, authz.Permission, error) {
	task, err := h.taskRepo.GetThreadRenderMetadata(ctx, taskID)
	if err != nil {
		return "", "", err
	}
	if task == nil {
		return "", "", statusErr(http.StatusNotFound, "task not found")
	}
	return task.ProjectID, permission, nil
}

type changesFileArgs struct {
	TaskID string
	Index  int
}

type changesFileMeta struct {
	Meta components.DiffFileRenderMeta
	OK   bool
}

type worktreeInfo struct {
	Task      *models.Task
	FileStats []service.WorktreeFileStat
}

func viewTask(h *Handler, ctx context.Context, taskID string) (string, authz.Permission, error) {
	return h.taskActionOwner(ctx, taskID, authz.View)
}

func editTask(h *Handler, ctx context.Context, taskID string) (string, authz.Permission, error) {
	return h.taskActionOwner(ctx, taskID, authz.Edit)
}

// editProject authorizes an action on a project the caller named; the project
// must exist on this server.
func editProject(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if project == nil {
		return "", "", statusErr(http.StatusNotFound, "project not found")
	}
	return project.ID, authz.Edit, nil
}

func init() {
	registerAction("changes.state", viewTask, func(h *Handler, ctx context.Context, taskID string) (taskChangesWorktreeState, error) {
		task, err := h.taskSvc.GetByID(ctx, taskID)
		if err != nil || task == nil {
			return taskChangesWorktreeState{}, err
		}
		return h.resolveTaskChangesWorktreeState(ctx, task), nil
	})
	registerAction("changes.file", func(h *Handler, ctx context.Context, a changesFileArgs) (string, authz.Permission, error) {
		return viewTask(h, ctx, a.TaskID)
	}, func(h *Handler, ctx context.Context, a changesFileArgs) (changesFileMeta, error) {
		task, err := h.taskSvc.GetByID(ctx, a.TaskID)
		if err != nil || task == nil {
			return changesFileMeta{}, err
		}
		meta, ok := h.resolveTaskChangesFileMeta(ctx, task, a.Index)
		return changesFileMeta{Meta: meta, OK: ok}, nil
	})
	registerAction("worktree.info", viewTask, func(h *Handler, ctx context.Context, taskID string) (worktreeInfo, error) {
		task, err := h.taskSvc.GetByID(ctx, taskID)
		if err != nil || task == nil {
			return worktreeInfo{}, err
		}
		project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
		h.recoverTaskWorktreeState(ctx, task, project)
		info := worktreeInfo{Task: task}
		if task.WorktreeBranch != "" {
			h.reconcileAlreadyMergedBranch(ctx, task)
			if project != nil && project.RepoPath != "" {
				target := task.MergeTargetBranch
				if target == "" {
					target = service.GetDefaultBranch(project.RepoPath)
				}
				info.FileStats = service.GetWorktreeFileStats(project.RepoPath, task.WorktreeBranch, target)
			}
		}
		return info, nil
	})
	registerAction("changes.settings", viewTask, (*Handler).changesActionSettings)
	registerAction("changes.branches", func(h *Handler, ctx context.Context, a changesBranchesArgs) (string, authz.Permission, error) {
		return viewTask(h, ctx, a.TaskID)
	}, (*Handler).changesActionBranches)
	registerAction("changes.eligibility", func(h *Handler, ctx context.Context, a changesEligibilityArgs) (string, authz.Permission, error) {
		return viewTask(h, ctx, a.TaskID)
	}, (*Handler).changesLocalEligibility)
	registerAction("task.merge_menu", viewTask, func(h *Handler, ctx context.Context, taskID string) (components.TaskCardMergeMenuState, error) {
		return h.taskCardMergeMenu(ctx, taskID)
	})
}
