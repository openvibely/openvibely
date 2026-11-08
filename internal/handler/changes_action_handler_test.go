package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type changesGitHubProvider struct {
	*fakeGitHubService
	calls int
}

func (p *changesGitHubProvider) RepositoryOptions(_ context.Context, repo *service.GitHubRepoRef) (*service.GitHubRepositoryOptions, error) {
	p.calls++
	if repo.Owner == "contributor" {
		parent, _ := service.ParseGitHubRepoURL("https://github.com/maintainer/original")
		return &service.GitHubRepositoryOptions{DefaultBranch: "main", Parent: &parent}, nil
	}
	return &service.GitHubRepositoryOptions{DefaultBranch: "trunk"}, nil
}
func (p *changesGitHubProvider) RepositoryBranches(_ context.Context, repo *service.GitHubRepoRef, page int) ([]string, bool, error) {
	p.calls++
	if page == 1 {
		return []string{"trunk", "release"}, true, nil
	}
	return []string{"next"}, false, nil
}

func TestChangesActionSettingsDefaultPersistAndScope(t *testing.T) {
	ctx := context.Background()
	h, e, _, _ := setupTestHandlerWithStores(t)
	project := &models.Project{Name: "Contribution", RepoPath: createHandlerTestGitRepo(t), RepoURL: "https://github.com/contributor/fork"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	provider := &changesGitHubProvider{fakeGitHubService: &fakeGitHubService{resolveRepoFn: func(_ context.Context, raw, path string) (*service.GitHubRepoRef, error) {
		ref, err := service.ParseGitHubRepoURL(raw)
		return &ref, err
	}}}
	h.SetGitHubService(provider)
	tasks := []*models.Task{{ProjectID: project.ID, Title: "One", Category: models.CategoryActive, Status: models.StatusCompleted}, {ProjectID: project.ID, Title: "Two", Category: models.CategoryActive, Status: models.StatusCompleted}}
	for _, task := range tasks {
		if err := h.taskRepo.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	get := func(task *models.Task) changesActionOptions {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree/action-settings", nil)
		e.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
		}
		var result changesActionOptions
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	defaults := get(tasks[0])
	if defaults.Preferences.Mode != "pr" || defaults.Preferences.Repository != "https://github.com/maintainer/original" || defaults.Preferences.Branch != "trunk" {
		t.Fatalf("wrong fork defaults: %+v", defaults)
	}
	selected := defaults.Preferences
	selected.Repository = project.RepoURL
	selected.Branch = "release"
	raw, _ := json.Marshal(selected)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+tasks[0].ID+"/worktree/action-settings", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	next := get(tasks[1])
	if next.Preferences != selected {
		t.Fatalf("preference not project-wide: %+v", next.Preferences)
	}
	task, _ := h.taskSvc.GetByID(ctx, tasks[1].ID)
	if task.MergeTargetBranch != "" {
		t.Fatal("saving preference retargeted another task")
	}
	// Selected-project ownership must be checked before discovery or preference writes.
	other := &models.Project{Name: "Other", RepoPath: createHandlerTestGitRepo(t)}
	if err := h.projectSvc.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, other.ID); err != nil {
		t.Fatal(err)
	}
	calls := provider.calls
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/"+tasks[0].ID+"/worktree/action-settings", nil))
	if rec.Code != 404 || provider.calls != calls {
		t.Fatalf("foreign project read: status=%d calls=%d/%d", rec.Code, provider.calls, calls)
	}
}

func TestChangesActionLocalDefaultAndInvalidTarget(t *testing.T) {
	ctx := context.Background()
	h, e, _, _ := setupTestHandlerWithStores(t)
	project := &models.Project{Name: "Local", RepoPath: createHandlerTestGitRepo(t)}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Local change", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree/action-settings", nil))
	var options changesActionOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if options.Preferences.Mode != "local" || options.Preferences.Method != "merge" || len(options.LocalBranches) == 0 {
		t.Fatalf("wrong local defaults: %+v", options)
	}
	options.Preferences.LocalBranch = "does-not-exist"
	raw, _ := json.Marshal(options.Preferences)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/action-settings", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("invalid branch accepted: %d %s", rec.Code, rec.Body.String())
	}
	value, _ := h.settingsRepo.Get(ctx, changesPreferenceKey(project.ID))
	if value != "" {
		t.Fatal("invalid preference saved")
	}
}
