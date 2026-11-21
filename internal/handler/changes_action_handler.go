package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Preferences belong to the project, not the browser or an individual task.
type changesActionPreferences struct {
	Mode        string `json:"mode"`
	Repository  string `json:"repository"`
	Branch      string `json:"branch"`
	LocalBranch string `json:"local_branch"`
	Method      string `json:"method"`
}
type changesRepositoryChoice struct {
	URL           string `json:"url"`
	Name          string `json:"name"`
	Upstream      bool   `json:"upstream"`
	DefaultBranch string `json:"default_branch"`
}
type changesActionOptions struct {
	LinkedPR      *changesActionPreferences `json:"linked_pr,omitempty"`
	Preferences   changesActionPreferences  `json:"preferences"`
	Repositories  []changesRepositoryChoice `json:"repositories"`
	LocalBranches []string                  `json:"local_branches"`
	GitHubError   string                    `json:"github_error,omitempty"`
}

func changesPreferenceKey(projectID string) string { return "project:" + projectID + ":changes_action" }

func (h *Handler) changesActionContext(c echo.Context) (*models.Task, *models.Project, error) {
	task, err := h.taskSvc.GetByID(c.Request().Context(), c.Param("taskId"))
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, echo.NewHTTPError(http.StatusNotFound, "Task not found")
	}
	projectID, hasProject, err := h.taskPullRequestProjectContext(c)
	if err != nil || (hasProject && projectID != task.ProjectID) {
		return nil, nil, echo.NewHTTPError(http.StatusNotFound, "Task not found")
	}
	project, err := h.changesProject(c.Request().Context(), task)
	return task, project, actionHTTPError(err)
}

func (h *Handler) changesProject(ctx context.Context, task *models.Task) (*models.Project, error) {
	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil || project.RepoPath == "" {
		return nil, statusErr(http.StatusBadRequest, "Project has no repository")
	}
	return project, nil
}

func (h *Handler) changesTaskProject(ctx context.Context, taskID string) (*models.Task, *models.Project, error) {
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, statusErr(http.StatusNotFound, "Task not found")
	}
	project, err := h.changesProject(ctx, task)
	return task, project, err
}
func localChangesBranches(ctx context.Context, path string) ([]string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", path, "for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
	if err != nil {
		return nil, fmt.Errorf("listing local branches: %w", err)
	}
	branches := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}
func (h *Handler) changesRepositories(ctx context.Context, project *models.Project) ([]changesRepositoryChoice, *service.GitHubRepoRef, error) {
	if h.githubSvc == nil {
		return nil, nil, fmt.Errorf("GitHub integration is not configured")
	}
	repo, err := h.githubSvc.ResolveRepo(ctx, project.RepoURL, project.RepoPath)
	if err != nil {
		return nil, nil, err
	}
	if err = service.ConfigureGitHubRepoEndpoint(repo, h.githubSvc.GlobalAPIEndpoint(ctx)); err != nil {
		return nil, nil, err
	}
	provider, ok := h.githubSvc.(service.GitHubRepositoryOptionsProvider)
	if !ok {
		return nil, repo, fmt.Errorf("GitHub repository discovery is unavailable")
	}
	metadata, err := provider.RepositoryOptions(ctx, repo)
	if err != nil {
		return nil, repo, err
	}
	choices := []changesRepositoryChoice{{URL: repo.HTMLURL, Name: repo.FullName, DefaultBranch: metadata.DefaultBranch}}
	if metadata.Parent != nil {
		parentData, err := provider.RepositoryOptions(ctx, metadata.Parent)
		if err != nil {
			return choices, repo, fmt.Errorf("Upstream repository is unavailable: %w", err)
		}
		choices = append([]changesRepositoryChoice{{URL: metadata.Parent.HTMLURL, Name: metadata.Parent.FullName, Upstream: true, DefaultBranch: parentData.DefaultBranch}}, choices...)
	}
	return choices, repo, nil
}
func (h *Handler) GetChangesActionSettings(c echo.Context) error {
	task, _, err := h.changesActionContext(c)
	if err != nil {
		return err
	}
	result, err := h.changesActionSettings(c.Request().Context(), task.ID)
	if err != nil {
		return actionHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

func (h *Handler) changesActionSettings(ctx context.Context, taskID string) (changesActionOptions, error) {
	var result changesActionOptions
	if remote, err := h.remoteAct(ctx, taskID, "changes.settings", taskID, &result); remote {
		return result, err
	}
	task, project, err := h.changesTaskProject(ctx, taskID)
	if err != nil {
		return result, err
	}
	branches, err := localChangesBranches(ctx, project.RepoPath)
	if err != nil {
		return result, err
	}
	prefs := changesActionPreferences{Mode: "local", Method: "merge", LocalBranch: task.MergeTargetBranch}
	if prefs.LocalBranch == "" {
		prefs.LocalBranch = service.GetDefaultBranch(project.RepoPath)
	}
	choices, source, githubErr := h.changesRepositories(ctx, project)
	if source != nil || project.RepoURL != "" {
		prefs.Mode = "pr"
	}
	if len(choices) > 0 {
		prefs.Mode = "pr"
		prefs.Repository = choices[0].URL
		prefs.Branch = choices[0].DefaultBranch
	}
	if h.settingsRepo != nil {
		raw, err := h.settingsRepo.Get(ctx, changesPreferenceKey(project.ID))
		if err != nil {
			return result, err
		}
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &prefs); err != nil {
				return result, err
			}
		}
	}
	result = changesActionOptions{Preferences: prefs, Repositories: choices, LocalBranches: branches}
	if h.taskPullRequestRepo != nil && source != nil {
		record, err := h.taskPullRequestRepo.GetByTaskID(ctx, task.ID)
		if err != nil {
			return result, err
		}
		if record != nil {
			target, err := service.TaskPullRequestTarget(record.PRURL, record.PRNumber, source)
			if err != nil {
				return result, err
			}
			result.LinkedPR = &changesActionPreferences{Repository: target.HTMLURL, Branch: record.BaseBranch}
		}
	}
	if githubErr != nil {
		result.GitHubError = githubErr.Error()
	}
	return result, nil
}
func (h *Handler) validateChangesPreferences(ctx context.Context, project *models.Project, prefs changesActionPreferences) error {
	if prefs.Mode != "pr" && prefs.Mode != "local" {
		return fmt.Errorf("unsupported action")
	}
	switch prefs.Method {
	case "merge", "ff", "squash", "rebase":
	default:
		return fmt.Errorf("unsupported merge method")
	}
	if prefs.Mode == "local" {
		branches, err := localChangesBranches(ctx, project.RepoPath)
		if err != nil {
			return err
		}
		for _, branch := range branches {
			if branch == prefs.LocalBranch {
				return nil
			}
		}
		return fmt.Errorf("local target branch no longer exists")
	}
	choices, source, err := h.changesRepositories(ctx, project)
	if err != nil && len(choices) == 0 {
		return err
	}
	for _, choice := range choices {
		if choice.URL == prefs.Repository {
			if strings.TrimSpace(prefs.Branch) == "" {
				return fmt.Errorf("target branch is required")
			}
			if err := exec.CommandContext(ctx, "git", "check-ref-format", "refs/heads/"+prefs.Branch).Run(); err != nil {
				return fmt.Errorf("invalid target branch")
			}
			if validator, ok := h.githubSvc.(interface {
				ValidatePullRequestBase(context.Context, *service.GitHubRepoRef, string) error
			}); ok {
				target, err := service.SameHostGitHubRepo(prefs.Repository, source)
				if err != nil {
					return err
				}
				return validator.ValidatePullRequestBase(ctx, target, prefs.Branch)
			}
			return nil
		}
	}
	return fmt.Errorf("PR target must be the project repository or its upstream parent")
}
func (h *Handler) SaveChangesActionSettings(c echo.Context) error {
	task, _, err := h.changesActionContext(c)
	if err != nil {
		return err
	}
	var prefs changesActionPreferences
	if err = json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 8192)).Decode(&prefs); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid preferences")
	}
	if _, err := actChangesSettings(h, c.Request().Context(), changesSettingsArgs{TaskID: task.ID, Prefs: prefs}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

type changesSettingsArgs struct {
	TaskID string
	Prefs  changesActionPreferences
}

var actChangesSettings = defineAction("changes.settings.save", func(h *Handler, ctx context.Context, a changesSettingsArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}, func(h *Handler, ctx context.Context, a changesSettingsArgs) (struct{}, error) {
	_, project, err := h.changesTaskProject(ctx, a.TaskID)
	if err != nil {
		return struct{}{}, err
	}
	if err := h.validateChangesPreferences(ctx, project, a.Prefs); err != nil {
		return struct{}{}, statusErr(http.StatusBadRequest, err.Error())
	}
	if h.settingsRepo == nil {
		return struct{}{}, statusErr(http.StatusServiceUnavailable, "Preference storage unavailable")
	}
	data, _ := json.Marshal(a.Prefs)
	return struct{}{}, h.settingsRepo.Set(ctx, changesPreferenceKey(project.ID), string(data))
})

func (h *Handler) GetChangesActionBranches(c echo.Context) error {
	task, _, err := h.changesActionContext(c)
	if err != nil {
		return err
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid page")
	}
	result, err := h.changesActionBranches(c.Request().Context(), changesBranchesArgs{TaskID: task.ID, Repository: c.QueryParam("repository"), Page: page})
	if err != nil {
		return actionHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

type changesBranchesArgs struct {
	TaskID     string
	Repository string
	Page       int
}

type changesBranchesPage struct {
	Branches []string `json:"branches"`
	HasMore  bool     `json:"has_more"`
}

func (h *Handler) changesActionBranches(ctx context.Context, args changesBranchesArgs) (changesBranchesPage, error) {
	var result changesBranchesPage
	if remote, err := h.remoteAct(ctx, args.TaskID, "changes.branches", args, &result); remote {
		return result, err
	}
	if args.Page < 1 || args.Page > 10000 {
		return result, statusErr(http.StatusBadRequest, "Invalid page")
	}
	_, project, err := h.changesTaskProject(ctx, args.TaskID)
	if err != nil {
		return result, err
	}
	choices, source, err := h.changesRepositories(ctx, project)
	if err != nil && len(choices) == 0 {
		return result, statusErr(http.StatusBadGateway, err.Error())
	}
	valid := false
	for _, choice := range choices {
		if choice.URL == args.Repository {
			valid = true
		}
	}
	if !valid {
		return result, statusErr(http.StatusBadRequest, "Invalid target repository")
	}
	repo, err := service.SameHostGitHubRepo(args.Repository, source)
	if err != nil {
		return result, err
	}
	provider := h.githubSvc.(service.GitHubRepositoryOptionsProvider)
	result.Branches, result.HasMore, err = provider.RepositoryBranches(ctx, repo, args.Page)
	if err != nil {
		return result, statusErr(http.StatusBadGateway, err.Error())
	}
	return result, nil
}

// selectChangesLocalTarget validates a request's destination on an in-memory
// task. It never persists the choice; the mutation callback does that only
// after revalidation, while holding the same lease as the Git operation.
func (h *Handler) selectChangesLocalTarget(ctx context.Context, task *models.Task, project *models.Project, target string) error {
	if target == "" {
		return nil
	}
	if task.Status == models.StatusRunning || task.MergeStatus == models.MergeStatusConflict {
		return fmt.Errorf("task cannot select a merge target while running or resolving conflicts")
	}
	if target == task.WorktreeBranch {
		return fmt.Errorf("target cannot be the task branch")
	}
	prefs := changesActionPreferences{Mode: "local", Method: "merge", LocalBranch: target}
	if err := h.validateChangesPreferences(ctx, project, prefs); err != nil {
		return err
	}
	task.MergeTargetBranch = target
	return nil
}

// GetChangesLocalEligibility evaluates the requested destination without changing the task.
func (h *Handler) GetChangesLocalEligibility(c echo.Context) error {
	task, _, err := h.changesActionContext(c)
	if err != nil {
		return err
	}
	target := strings.TrimSpace(c.QueryParam("target"))
	if target == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Target branch is required")
	}
	result, err := h.changesLocalEligibility(c.Request().Context(), changesEligibilityArgs{TaskID: task.ID, Target: target})
	if err != nil {
		return actionHTTPError(err)
	}
	return c.JSON(http.StatusOK, result)
}

type changesEligibilityArgs struct {
	TaskID string
	Target string
}

type changesEligibility struct {
	Target          string `json:"target"`
	MergeAvailable  bool   `json:"merge_available"`
	RebaseAvailable bool   `json:"rebase_available"`
	Reason          string `json:"reason"`
}

func (h *Handler) changesLocalEligibility(ctx context.Context, args changesEligibilityArgs) (changesEligibility, error) {
	var result changesEligibility
	if remote, err := h.remoteAct(ctx, args.TaskID, "changes.eligibility", args, &result); remote {
		return result, err
	}
	task, project, err := h.changesTaskProject(ctx, args.TaskID)
	if err != nil {
		return result, err
	}
	selected := *task
	if err := h.selectChangesLocalTarget(ctx, &selected, project, args.Target); err != nil {
		return result, statusErr(http.StatusBadRequest, err.Error())
	}
	merged := taskBranchAlreadyMergedForProject(ctx, &selected, project)
	eligibility := h.resolveTaskMergeEligibility(ctx, &selected, project, merged)
	return changesEligibility{
		Target:          args.Target,
		MergeAvailable:  eligibility.MergeAvailable,
		RebaseAvailable: eligibility.MergeAvailable && h.taskRebaseAvailable(ctx, &selected, project, merged),
		Reason:          eligibility.Reason,
	}, nil
}
