package handler

import (
	"context"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
	"net/http"
	"strings"
)

var actDeleteProject = defineAction("project.delete", func(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	return projectID, authz.Own, nil
}, func(h *Handler, ctx context.Context, projectID string) (bool, error) {
	p, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] DeleteProject fetch error: %v", err)
		return false, err
	}
	if p == nil {
		applog.Infof("[handler] DeleteProject not found id=%s", projectID)
		return false, statusErr(http.StatusNotFound, "project not found")
	}

	if p.IsDefault {
		applog.Infof("[handler] DeleteProject refused: cannot delete default project id=%s", projectID)
		return false, statusErr(http.StatusBadRequest, "cannot delete the default project")
	}

	cleanupWarning := false
	if err := h.projectSvc.Delete(ctx, projectID); err != nil {
		var cleanupErr *service.ProjectDeletionCleanupError
		if !errors.As(err, &cleanupErr) {
			applog.Infof("[handler] DeleteProject error: %v", err)
			return false, statusErr(http.StatusInternalServerError, "could not delete this project; its data was kept")
		}
		cleanupWarning = true
		applog.Infof("[handler] DeleteProject filesystem cleanup warning: %v", cleanupErr)
	}

	if h.pagePlugins != nil {
		h.pagePlugins.ReleaseProject(projectID)
	}
	applog.Infof("[handler] DeleteProject success id=%s", projectID)

	return cleanupWarning, nil
})

type projectEditArgs struct {
	ProjectID, AgentID string
	Values             map[string]string `remote:"raw"`
	Knowledge          *models.SkillMemoryPermissions
	Computer           *bool
}

func (a projectEditArgs) value(key string) string {
	if key == "default_agent_config_id" {
		return a.AgentID
	}
	return a.Values[key]
}

type projectUpdateResult struct {
	Code       int
	Message    string
	CloneError bool
}

var actUpdateProject = defineAction("project.update", func(h *Handler, ctx context.Context, a projectEditArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Manage, nil
}, func(h *Handler, ctx context.Context, a projectEditArgs) (projectUpdateResult, error) {
	projectID := a.ProjectID
	p, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] UpdateProject fetch error: %v", err)
		return projectUpdateResult{}, err
	}
	if p == nil {
		applog.Infof("[handler] UpdateProject not found id=%s", projectID)
		return projectUpdateResult{}, statusErr(http.StatusNotFound, "project not found")
	}

	localRepoPathEnabled := h.isLocalRepoPathEnabled()
	globalMaxWorkers, err := h.configuredGlobalWorkerLimit(ctx)
	if err != nil {
		return projectUpdateResult{Code: http.StatusInternalServerError, Message: "failed to read global worker limit"}, nil
	}
	currentRepoPath := p.RepoPath
	settings, err := parseProjectFormValues(a.value, projectFormSettingsOptions{
		LocalRepoPathEnabled: localRepoPathEnabled,
		GitHubSvc:            h.githubSvc,
		CurrentProject:       p,
		GlobalMaxWorkers:     globalMaxWorkers,
	})
	if err != nil {
		return projectUpdateResult{Code: http.StatusBadRequest, Message: err.Error()}, nil
	}

	recloneGitHubRepo := settings.RepoSource == "github"
	if recloneGitHubRepo {
		submittedRepo, err := service.ParseGitHubRepoURL(settings.RepoURL)
		if err != nil {
			return projectUpdateResult{Code: http.StatusBadRequest, Message: err.Error(), CloneError: true}, nil
		}
		if p.RepoURL != "" {
			if currentRepo, err := service.ParseGitHubRepoURL(p.RepoURL); err == nil {
				recloneGitHubRepo = !strings.EqualFold(currentRepo.HTMLURL, submittedRepo.HTMLURL)
			}
		}
	}

	p.Name = settings.Name
	p.Description = settings.Description
	p.DefaultAgentConfigID = settings.DefaultAgentConfigID
	p.MaxWorkers = settings.MaxWorkers
	if a.Knowledge != nil {
		p.SkillMemoryPermissions = a.Knowledge
	}

	if recloneGitHubRepo {
		reclonedPath, normalizedURL, err := h.githubSvc.RecloneProjectRepo(ctx, p.ID, currentRepoPath, settings.RepoURL)
		if err != nil {
			return projectUpdateResult{Code: http.StatusBadRequest, Message: err.Error(), CloneError: true}, nil
		}
		p.RepoPath = reclonedPath
		p.RepoURL = normalizedURL
	} else if settings.RepoSource == "github" {
		p.RepoPath = currentRepoPath
	} else if settings.PreserveLegacyLocalProject {
		// Preserve existing local-path configuration for legacy projects when local paths
		// are disabled in this environment.
		p.RepoPath = currentRepoPath
		p.RepoURL = ""
	} else {
		p.RepoPath = settings.RepoPath
		p.RepoURL = ""
	}
	applog.Infof("[handler] UpdateProject id=%s name=%q repo_source=%q repo_path=%q repo_url=%q default_agent=%v max_workers=%v local_repo_path_enabled=%v legacy_local_project=%v", projectID, p.Name, settings.RepoSource, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, localRepoPathEnabled, settings.PreserveLegacyLocalProject)

	if a.Computer != nil {
		if h.computerSvc == nil {
			return projectUpdateResult{}, statusErr(http.StatusServiceUnavailable, "Project computers unavailable")
		}
		if err := h.computerSvc.SetEnabled(ctx, p.ID, *a.Computer); err != nil {
			return projectUpdateResult{}, statusErr(http.StatusBadRequest, err.Error())
		}
	}
	if err := h.projectSvc.Update(ctx, p); err != nil {
		applog.Infof("[handler] UpdateProject error: %v", err)
		return projectUpdateResult{Code: http.StatusBadRequest, Message: err.Error()}, nil
	}
	applog.Infof("[handler] UpdateProject success id=%s", projectID)
	if h.workerSvc != nil {
		h.workerSvc.ReconcilePendingTasks(ctx)
		h.workerSvc.DispatchNext()
	}

	return projectUpdateResult{}, nil
})

var actRecoverProject = defineAction("project.reclone", func(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	return projectID, authz.Manage, nil
}, func(h *Handler, ctx context.Context, projectID string) (projectUpdateResult, error) {
	p, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		return projectUpdateResult{}, err
	}
	if p == nil {
		return projectUpdateResult{Code: http.StatusNotFound, Message: "Project not found."}, nil
	}
	if p.RepoURL == "" || h.projectSvc.RepoPathHealth(p).Status != models.RepoPathHealthMissing {
		return projectUpdateResult{Code: http.StatusConflict, Message: "Re-clone is only available for a missing GitHub repository folder. Reopen project settings to check its status."}, nil
	}
	if h.githubSvc == nil {
		return projectUpdateResult{Code: http.StatusServiceUnavailable, Message: "GitHub integration is unavailable."}, nil
	}
	if err := h.githubSvc.RecoverProjectRepo(ctx, p.ID, p.RepoPath, p.RepoURL); err != nil {
		applog.Infof("[handler] repository recovery failed for project %s: %v", p.ID, err)
		return projectUpdateResult{Code: http.StatusBadRequest, Message: "Re-clone failed. Check GitHub access and the managed repository storage, then retry."}, nil
	}
	return projectUpdateResult{}, nil
})

type projectWorkerLimitArgs struct {
	ProjectID string
	Limit     int
}
type workerLimitResult struct {
	Max, Running, Total, Queue         int
	Projects                           []pages.ProjectWorkerStats
	Models                             []pages.ModelWorkerStats
	ProjectsAvailable, ModelsAvailable bool
	ValidationError                    string
}

var actProjectWorkerLimit = defineAction("project.worker_limit", func(h *Handler, ctx context.Context, a projectWorkerLimitArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Manage, nil
}, func(h *Handler, ctx context.Context, a projectWorkerLimitArgs) (workerLimitResult, error) {
	projectID, maxWorkers := a.ProjectID, a.Limit
	if maxWorkers < 0 {
		return workerLimitResult{}, statusErr(http.StatusBadRequest, "invalid worker limit")
	}
	// Get the project
	project, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit error getting project: %v", err)
		return workerLimitResult{}, err
	}
	if project == nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit project not found id=%s", projectID)
		return workerLimitResult{}, statusErr(http.StatusNotFound, "project not found")
	}

	if maxWorkers > 0 {
		globalMaxWorkers, err := h.configuredGlobalWorkerLimit(ctx)
		if err != nil {
			return workerLimitResult{}, err
		}
		if err := models.ValidateProjectWorkerLimit(&maxWorkers, globalMaxWorkers); err != nil {
			return workerLimitResult{ValidationError: fmt.Sprintf("Max concurrent workers %s", strings.TrimPrefix(err.Error(), "project max_workers "))}, nil
		}
	}

	// Update max_workers (0 or empty means no project limit)
	if maxWorkers == 0 {
		project.MaxWorkers = nil
	} else {
		project.MaxWorkers = &maxWorkers
	}

	if err := h.projectSvc.Update(ctx, project); err != nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit error updating project: %v", err)
		return workerLimitResult{}, err
	}

	applog.Infof("[handler] UpdateProjectWorkerLimit success project=%s max_workers=%v", projectID, project.MaxWorkers)

	// Trigger dispatch check — if the limit was increased and there are queued
	// tasks for this project, they should start immediately. Reconcile durable
	// active tasks as well so a missed submission does not remain pending.
	h.workerSvc.ReconcilePendingTasks(ctx)
	h.workerSvc.DispatchNext()

	// Return the updated worker settings content for HTMX
	maxGlobalWorkers, _ := h.workerRepo.GetMaxWorkers(ctx)
	queueSize := h.workerSvc.QueueSize()
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()

	projectStats, projectStatsAvailable := h.buildProjectWorkerStats(ctx)

	modelStats, modelStatsAvailable := h.buildModelWorkerStatsList(ctx)
	if h.projectAccessSvc != nil {
		visible := projectStats[:0]
		for _, project := range projectStats {
			if h.projectAccessSvc.Require(ctx, project.ID, authz.View) == nil {
				visible = append(visible, project)
			}
		}
		projectStats = visible
	}
	return workerLimitResult{Max: maxGlobalWorkers, Running: runningWorkers, Total: totalRunning, Queue: queueSize, Projects: projectStats, Models: modelStats, ProjectsAvailable: projectStatsAvailable, ModelsAvailable: modelStatsAvailable}, nil
})
