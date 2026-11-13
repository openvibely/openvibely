package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type changesGitHubProvider struct {
	*fakeGitHubService
	calls       int
	upstreamErr error
	sourceErr   error
}

func (p *changesGitHubProvider) RepositoryOptions(_ context.Context, repo *service.GitHubRepoRef) (*service.GitHubRepositoryOptions, error) {
	p.calls++
	if repo.Owner == "contributor" {
		if p.sourceErr != nil {
			return nil, p.sourceErr
		}
		parent, _ := service.ParseGitHubRepoURL("https://github.com/maintainer/original")
		return &service.GitHubRepositoryOptions{DefaultBranch: "main", Parent: &parent}, nil
	}
	if p.upstreamErr != nil {
		return nil, p.upstreamErr
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

func TestChangesActionForkRemainsAvailableWhenUpstreamFails(t *testing.T) {
	ctx := context.Background()
	h, e, _, _ := setupTestHandlerWithStores(t)
	project := &models.Project{Name: "Partial discovery", RepoPath: createHandlerTestGitRepo(t), RepoURL: "https://github.com/contributor/fork"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Fork PR", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	provider := &changesGitHubProvider{upstreamErr: errors.New("upstream access denied"), fakeGitHubService: &fakeGitHubService{resolveRepoFn: func(_ context.Context, raw, path string) (*service.GitHubRepoRef, error) {
		ref, err := service.ParseGitHubRepoURL(raw)
		return &ref, err
	}}}
	h.SetGitHubService(provider)
	endpoint := "/tasks/" + task.ID + "/worktree/"
	get := func() changesActionOptions {
		t.Helper()
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"action-settings", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
		}
		var data changesActionOptions
		if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := get()
	if len(data.Repositories) != 1 || data.Repositories[0].URL != project.RepoURL || data.Preferences.Repository != project.RepoURL || data.GitHubError == "" {
		t.Fatalf("lost available fork or warning: %+v", data)
	}
	save := func(prefs changesActionPreferences) int {
		t.Helper()
		raw, _ := json.Marshal(prefs)
		req := httptest.NewRequest(http.MethodPost, endpoint+"action-settings", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := save(data.Preferences); code != http.StatusNoContent {
		t.Fatalf("fork preference blocked: %d", code)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"action-branches?repository="+project.RepoURL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fork branches blocked: %d %s", rec.Code, rec.Body.String())
	}
	// Existing upstream choices must not silently change to fork publication.
	upstream := data.Preferences
	upstream.Repository = "https://github.com/maintainer/original"
	upstream.Branch = "trunk"
	raw, _ := json.Marshal(upstream)
	if err := h.settingsRepo.Set(ctx, changesPreferenceKey(project.ID), string(raw)); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.Preferences != upstream || len(got.Repositories) != 1 {
		t.Fatalf("saved upstream choice was redirected: %+v", got)
	}
	if code := save(upstream); code != http.StatusBadRequest {
		t.Fatalf("unavailable upstream accepted: %d", code)
	}
	provider.sourceErr = errors.New("fork access denied")
	if got := get(); len(got.Repositories) != 0 || got.GitHubError == "" {
		t.Fatalf("source failure was ignored: %+v", got)
	}
	if code := save(data.Preferences); code != http.StatusBadRequest {
		t.Fatalf("unavailable fork accepted: %d", code)
	}
}

func TestChangesActionLocalRebaseEligibilityUsesSelectedTarget(t *testing.T) {
	ctx := context.Background()
	h, e, _, _ := setupTestHandlerWithStores(t)
	path := createHandlerTestGitRepo(t)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("branch", "-M", "main")
	git("checkout", "-b", "develop")
	git("commit", "--allow-empty", "-m", "Develop change")
	git("checkout", "main")
	worktree := filepath.Join(t.TempDir(), "task")
	git("worktree", "add", "-b", "task/rebase", worktree, "main")
	out, err := exec.Command("git", "-C", worktree, "commit", "--allow-empty", "-m", "Task change").CombinedOutput()
	if err != nil {
		t.Fatalf("task commit: %s: %v", out, err)
	}
	project := &models.Project{Name: "Eligibility", RepoPath: path}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: worktree, WorktreeBranch: "task/rebase", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		branch    string
		available bool
	}{{"main", false}, {"develop", true}, {"main", false}} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree/action-local?target="+tt.branch, nil))
		if rec.Code != 200 {
			t.Fatalf("eligibility: %d %s", rec.Code, rec.Body.String())
		}
		var result struct {
			Available bool `json:"rebase_available"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Available != tt.available {
			t.Fatalf("%s availability = %v", tt.branch, result.Available)
		}
	}
	saved, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MergeTargetBranch != "main" {
		t.Fatal("eligibility check changed the task target")
	}
	// Simulate deleting the task's original target after project defaults changed.
	git("branch", "old-target", "main")
	if err := h.taskRepo.UpdateMergeTargetBranch(ctx, task.ID, "old-target"); err != nil {
		t.Fatal(err)
	}
	task.MergeTargetBranch = "old-target"
	git("branch", "-D", "old-target")
	state := h.resolveTaskChangesWorktreeState(ctx, task)
	if state.LocalMergeUnavailable {
		t.Fatal("missing old target must allow choosing a replacement")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree/action-local?target=main", nil))
	var eligibility struct {
		Merge bool `json:"merge_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &eligibility); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || !eligibility.Merge {
		t.Fatalf("replacement target unavailable: %d %s", rec.Code, rec.Body.String())
	}
	saved, err = h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MergeTargetBranch != "old-target" {
		t.Fatal("preview persisted replacement target")
	}
	task.Status = models.StatusRunning
	if !h.resolveTaskChangesWorktreeState(ctx, task).LocalMergeUnavailable {
		t.Fatal("running task must stay unavailable")
	}
}
