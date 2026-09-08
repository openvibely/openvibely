package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestGetDefaultAgentForTask_ProjectDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	projectAgent := &models.LLMConfig{
		Name:     "Project Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	if err := llmConfigRepo.Create(ctx, projectAgent); err != nil {
		t.Fatalf("Create project agent: %v", err)
	}

	project := &models.Project{
		Name:                 "Test Project Default Agent",
		DefaultAgentConfigID: &projectAgent.ID,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected agent, got nil")
	}
	if agent.ID != projectAgent.ID {
		t.Errorf("expected project agent ID=%s, got %s", projectAgent.ID, agent.ID)
	}
	if agent.Name != "Project Agent" {
		t.Errorf("expected agent Name=Project Agent, got %q", agent.Name)
	}
}

func TestGetDefaultAgentForTask_FallsBackToGlobalDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	project := &models.Project{
		Name: "No Default Agent Project",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestGetDefaultAgentForTask_EmptyProjectID(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	agent, err := svc.getDefaultAgentForTask(ctx, "")
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestGetDefaultAgentForTask_DeletedProjectAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	projectAgent := &models.LLMConfig{
		Name:     "Temporary Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	llmConfigRepo.Create(ctx, projectAgent)

	project := &models.Project{
		Name:                 "Project With Deleted Agent",
		DefaultAgentConfigID: &projectAgent.ID,
	}
	projectRepo.Create(ctx, project)

	llmConfigRepo.Delete(ctx, projectAgent.ID)

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent after project agent deleted, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestProjectWorkDirMatches_PathBoundariesAndSeparators(t *testing.T) {
	tests := []struct {
		name     string
		repoPath string
		workDir  string
		want     bool
	}{
		{name: "repository root", repoPath: "/repos/app", workDir: "/repos/app", want: true},
		{name: "filesystem root", repoPath: string(filepath.Separator), workDir: filepath.Join(string(filepath.Separator), "repos", "app"), want: true},
		{name: "relative root", repoPath: ".", workDir: filepath.Join("internal", "service"), want: true},
		{name: "nested source", repoPath: "/repos/app", workDir: "/repos/app/internal/service", want: true},
		{name: "task worktree", repoPath: "/repos/app", workDir: "/repos/app/.worktrees/task_1/internal", want: true},
		{name: "prefixed sibling", repoPath: "/repos/app", workDir: "/repos/application", want: false},
		{name: "empty repository", repoPath: "", workDir: "/repos/app", want: false},
		{name: "windows nested", repoPath: `C:\\repos\\app`, workDir: `C:\\repos\\app\\internal\\service`, want: true},
		{name: "windows worktree", repoPath: `C:\\repos\\app`, workDir: `C:\\repos\\app\\.worktrees\\task_1`, want: true},
		{name: "windows prefixed sibling", repoPath: `C:\\repos\\app`, workDir: `C:\\repos\\application`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectWorkDirMatches(tt.repoPath, tt.workDir); got != tt.want {
				t.Fatalf("projectWorkDirMatches(%q, %q) = %v, want %v", tt.repoPath, tt.workDir, got, tt.want)
			}
		})
	}
}

func TestLLMService_projectIDForWorkDir_EmptyWorkDirSkipsLookup(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	svc := NewLLMService(nil, nil, nil, repository.NewProjectRepo(db), nil, nil)
	counter.SetEnabled(true)
	if got := svc.projectIDForWorkDir(context.Background(), "  "); got != "" {
		t.Fatalf("projectIDForWorkDir(empty) = %q, want empty", got)
	}
	counter.SetEnabled(false)
	if statements := counter.Statements(); len(statements) != 0 {
		t.Fatalf("empty work directory executed statements: %v", statements)
	}
}

var directAttributionBenchmarkSink string

func setupDirectAttributionFixture(tb testing.TB, projectCount int) (*LLMService, *repository.ProjectRepo, *testutil.SQLStatementCounter, string, string) {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	repo := repository.NewProjectRepo(db)
	ctx := context.Background()
	description := strings.Repeat("d", 16<<10)
	repoURL := "https://example.test/" + strings.Repeat("u", 2<<10)
	root := tb.TempDir()

	var firstID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM projects LIMIT 1`).Scan(&firstID); err != nil {
		tb.Fatalf("select seeded project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects SET name = ?, description = ?, repo_path = ?, repo_url = ? WHERE id = ?`, "Project 0000", description, filepath.Join(root, "project-0000"), repoURL, firstID); err != nil {
		tb.Fatalf("shape seeded project: %v", err)
	}
	targetID := firstID
	for i := 1; i < projectCount; i++ {
		project := &models.Project{
			Name:        fmt.Sprintf("Project %04d", i),
			Description: description,
			RepoPath:    filepath.Join(root, fmt.Sprintf("project-%04d", i)),
			RepoURL:     repoURL,
		}
		if err := repo.Create(ctx, project); err != nil {
			tb.Fatalf("create project %d: %v", i, err)
		}
		targetID = project.ID
	}

	svc := NewLLMService(nil, nil, nil, repo, nil, nil)
	workDir := filepath.Join(root, fmt.Sprintf("project-%04d", projectCount-1), ".worktrees", "task", "internal")
	return svc, repo, counter, workDir, targetID
}

func legacyDirectAttribution(ctx context.Context, repo *repository.ProjectRepo, workDir string) error {
	projects, err := repo.List(ctx)
	if err != nil {
		return err
	}
	bestID := ""
	bestLen := -1
	for _, project := range projects {
		if legacyProjectWorkDirMatches(project.RepoPath, workDir) {
			if length := len(filepath.Clean(project.RepoPath)); length > bestLen {
				bestID, bestLen = project.ID, length
			}
		}
	}
	directAttributionBenchmarkSink = bestID
	return nil
}

func legacyProjectWorkDirMatches(repoPath string, workDir string) bool {
	repo := strings.TrimSpace(repoPath)
	if repo == "" || strings.TrimSpace(workDir) == "" {
		return false
	}
	repo = filepath.Clean(repo)
	want := filepath.Clean(workDir)
	if repo == want {
		return true
	}
	if rel, err := filepath.Rel(repo, want); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return true
	}
	worktreesDir := filepath.Join(repo, ".worktrees")
	if rel, err := filepath.Rel(worktreesDir, want); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return true
	}
	return false
}

func BenchmarkDirectModelUsageAttribution(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		b.Run(fmt.Sprintf("projects=%d", projectCount), func(b *testing.B) {
			svc, repo, counter, workDir, projectID := setupDirectAttributionFixture(b, projectCount)
			ctx := context.Background()
			explicitCtx := WithDirectUsageProject(ctx, projectID)
			cases := []struct {
				name string
				call func() error
			}{
				{name: "legacy-full-list", call: func() error { return legacyDirectAttribution(ctx, repo, workDir) }},
				{name: "fallback-repo-roots", call: func() error {
					directAttributionBenchmarkSink = svc.projectIDForWorkDir(ctx, workDir)
					return nil
				}},
				{name: "explicit-project", call: func() error {
					directAttributionBenchmarkSink = directUsageProjectFromContext(explicitCtx)
					return nil
				}},
			}
			for _, benchmarkCase := range cases {
				b.Run(benchmarkCase.name, func(b *testing.B) {
					counter.Reset()
					counter.SetEnabled(true)
					if err := benchmarkCase.call(); err != nil {
						b.Fatalf("instrumented attribution: %v", err)
					}
					counter.SetEnabled(false)
					statementCount := len(counter.Statements())
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if err := benchmarkCase.call(); err != nil {
							b.Fatalf("attribution: %v", err)
						}
					}
					b.ReportMetric(float64(statementCount), "sql-statements/op")
				})
			}
		})
	}
}

func medianDuration(durations []time.Duration) time.Duration {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func pairedDirectAttributionMedians(t *testing.T, samples, runs int, baseline, candidate func() error) (time.Duration, time.Duration) {
	t.Helper()
	baselineDurations := make([]time.Duration, samples)
	candidateDurations := make([]time.Duration, samples)
	measure := func(call func() error) time.Duration {
		start := time.Now()
		for i := 0; i < runs; i++ {
			if err := call(); err != nil {
				t.Fatalf("attribution sample: %v", err)
			}
		}
		return time.Since(start) / time.Duration(runs)
	}
	for i := 0; i < samples; i++ {
		if i%2 == 0 {
			baselineDurations[i] = measure(baseline)
			candidateDurations[i] = measure(candidate)
		} else {
			candidateDurations[i] = measure(candidate)
			baselineDurations[i] = measure(baseline)
		}
	}
	return medianDuration(baselineDurations), medianDuration(candidateDurations)
}

func allocatedBytesPerDirectAttribution(t *testing.T, runs int, call func() error) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if err := call(); err != nil {
			t.Fatalf("attribution allocation sample: %v", err)
		}
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

func TestDirectModelUsageFallbackPerformanceReductionAt500Projects(t *testing.T) {
	svc, repo, _, workDir, _ := setupDirectAttributionFixture(t, 500)
	ctx := context.Background()
	baseline := func() error { return legacyDirectAttribution(ctx, repo, workDir) }
	candidate := func() error {
		directAttributionBenchmarkSink = svc.projectIDForWorkDir(ctx, workDir)
		return nil
	}
	if err := baseline(); err != nil {
		t.Fatalf("warm baseline: %v", err)
	}
	if err := candidate(); err != nil {
		t.Fatalf("warm candidate: %v", err)
	}

	baselineMedian, candidateMedian := pairedDirectAttributionMedians(t, 11, 5, baseline, candidate)
	if candidateMedian*5 > baselineMedian {
		t.Fatalf("fallback median %s must be at least 80%% below full-list baseline %s", candidateMedian, baselineMedian)
	}
	baselineBytes := allocatedBytesPerDirectAttribution(t, 5, baseline)
	candidateBytes := allocatedBytesPerDirectAttribution(t, 20, candidate)
	if candidateBytes*20 > baselineBytes {
		t.Fatalf("fallback allocated bytes/op %d must be at least 95%% below full-list baseline %d", candidateBytes, baselineBytes)
	}
	t.Logf("500 projects: median baseline=%s fallback=%s reduction=%.1f%%; bytes/op baseline=%d fallback=%d reduction=%.1f%%",
		baselineMedian, candidateMedian, 100*(1-float64(candidateMedian)/float64(baselineMedian)),
		baselineBytes, candidateBytes, 100*(1-float64(candidateBytes)/float64(baselineBytes)))
}

func TestLLMService_projectIDForWorkDir_DeletedAndEmptyRepositoriesDoNotMatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewProjectRepo(db)
	svc := NewLLMService(nil, nil, nil, repo, nil, nil)
	ctx := context.Background()
	root := t.TempDir()
	project := &models.Project{Name: "Deleted Root", RepoPath: root}
	if err := repo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	if err := repo.Create(ctx, &models.Project{Name: "Empty Root", RepoPath: ""}); err != nil {
		t.Fatalf("Create empty-root project: %v", err)
	}
	workDir := filepath.Join(root, "internal")
	if got := svc.projectIDForWorkDir(ctx, workDir); got != project.ID {
		t.Fatalf("projectIDForWorkDir before delete = %q, want %q", got, project.ID)
	}
	if err := repo.Delete(ctx, project.ID); err != nil {
		t.Fatalf("Delete project: %v", err)
	}
	if got := svc.projectIDForWorkDir(ctx, workDir); got != "" {
		t.Fatalf("projectIDForWorkDir after delete = %q, want unscoped", got)
	}
}

func TestLLMService_projectIDForWorkDir_ResolvesTaskWorktreesToProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	svc := NewLLMService(nil, nil, nil, projectRepo, nil, nil)

	repoPath := t.TempDir()
	project := &models.Project{
		Name:     "Worktree Usage Project",
		RepoPath: repoPath,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	for _, workDir := range []string{
		repoPath,
		filepath.Join(repoPath, "internal", "service"),
		filepath.Join(repoPath, ".worktrees", "task_abc123"),
		filepath.Join(repoPath, ".worktrees", "task_abc123", "internal", "service"),
	} {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", workDir, err)
		}
		if got := svc.projectIDForWorkDir(ctx, workDir); got != project.ID {
			t.Fatalf("projectIDForWorkDir(%q) = %q, want %q", workDir, got, project.ID)
		}
	}
}

func TestLLMService_projectIDForWorkDir_PrefersMostSpecificProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	svc := NewLLMService(nil, nil, nil, projectRepo, nil, nil)

	parentRepo := t.TempDir()
	childRepo := filepath.Join(parentRepo, "packages", "child")
	if err := os.MkdirAll(childRepo, 0o755); err != nil {
		t.Fatalf("MkdirAll child repo: %v", err)
	}
	parent := &models.Project{Name: "Parent", RepoPath: parentRepo}
	child := &models.Project{Name: "Child", RepoPath: childRepo}
	if err := projectRepo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent project: %v", err)
	}
	if err := projectRepo.Create(ctx, child); err != nil {
		t.Fatalf("Create child project: %v", err)
	}

	workDir := filepath.Join(childRepo, ".worktrees", "task_child")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree: %v", err)
	}
	if got := svc.projectIDForWorkDir(ctx, workDir); got != child.ID {
		t.Fatalf("projectIDForWorkDir(%q) = %q, want child project %q", workDir, got, child.ID)
	}
}

func TestLLMService_ExecuteTaskWithAgent_UsesProjectRepoPathAsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	project := &models.Project{
		Name:     "Chrome Plugin",
		RepoPath: t.TempDir(),
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Debug Microphone Issue",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "debug the microphone issue",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != project.RepoPath {
		t.Errorf("expected workDir=%q, got %q", project.RepoPath, got)
	}
}

func TestLLMService_CallClaudeCLI_SetsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	projectDir := t.TempDir()
	project := &models.Project{
		Name:     "Test Project",
		RepoPath: projectDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "WorkDir Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != projectDir {
		t.Errorf("expected workDir=%q, got %q", projectDir, got)
	}
}

func TestLLMService_CallClaudeCLI_NoWorkDirWhenProjectHasNoRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	task := &models.Task{
		ProjectID: "default",
		Title:     "No RepoPath Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != "" {
		t.Errorf("expected empty workDir for project without RepoPath, got %q", got)
	}
}

func TestLLMService_CallCodexCLI_SetsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	projectDir := t.TempDir()
	project := &models.Project{
		Name:     "Codex WorkDir Project",
		RepoPath: projectDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Codex WorkDir Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	execRec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execRec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != projectDir {
		t.Errorf("expected workDir=%q, got %q", projectDir, got)
	}
}
