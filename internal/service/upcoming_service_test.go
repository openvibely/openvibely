package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupUpcomingTest(t *testing.T) (*UpcomingService, *repository.TaskRepo, *repository.LLMConfigRepo, *repository.ExecutionRepo, string) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	upcomingSvc := NewUpcomingService(upcomingRepo)

	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("creating project: %v", err)
	}

	return upcomingSvc, taskRepo, llmConfigRepo, execRepo, project.ID
}

func TestUpcomingService_GenerateUpcoming_Empty(t *testing.T) {
	upcomingSvc, _, _, _, projectID := setupUpcomingTest(t)

	brief, err := upcomingSvc.GenerateUpcoming(context.Background(), projectID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if brief == nil {
		t.Fatal("expected brief to be non-nil")
	}
	if len(brief.RunningTasks) != 0 {
		t.Fatalf("expected 0 running tasks, got %d", len(brief.RunningTasks))
	}
	if len(brief.PendingTasks) != 0 {
		t.Fatalf("expected 0 pending tasks, got %d", len(brief.PendingTasks))
	}
	if len(brief.ScheduledTasks) != 0 {
		t.Fatalf("expected 0 scheduled tasks, got %d", len(brief.ScheduledTasks))
	}
	if brief.ProjectID != projectID {
		t.Fatalf("expected project ID %q, got %q", projectID, brief.ProjectID)
	}
}

func TestUpcomingService_GenerateUpcoming_WithTasks(t *testing.T) {
	upcomingSvc, taskRepo, _, _, projectID := setupUpcomingTest(t)

	// Create a running task
	running := &models.Task{
		ProjectID: projectID,
		Title:     "Running Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "Working on it",
	}
	if err := taskRepo.Create(context.Background(), running); err != nil {
		t.Fatalf("creating running task: %v", err)
	}

	// Create pending active tasks
	for i := 0; i < 3; i++ {
		pending := &models.Task{
			ProjectID: projectID,
			Title:     "Pending " + string(rune('A'+i)),
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "Do it",
		}
		if err := taskRepo.Create(context.Background(), pending); err != nil {
			t.Fatalf("creating pending task: %v", err)
		}
	}

	brief, err := upcomingSvc.GenerateUpcoming(context.Background(), projectID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(brief.RunningTasks) != 1 {
		t.Fatalf("expected 1 running task, got %d", len(brief.RunningTasks))
	}
	if len(brief.PendingTasks) != 3 {
		t.Fatalf("expected 3 pending tasks, got %d", len(brief.PendingTasks))
	}
}

func TestUpcomingService_GenerateHistory_Empty(t *testing.T) {
	upcomingSvc, _, _, _, projectID := setupUpcomingTest(t)

	history, err := upcomingSvc.GenerateHistory(context.Background(), projectID, models.TimeRangeDay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if history == nil {
		t.Fatal("expected history to be non-nil")
	}
	if history.Summary.TotalExecutions != 0 {
		t.Fatalf("expected 0 executions, got %d", history.Summary.TotalExecutions)
	}
	if history.TimeRange != models.TimeRangeDay {
		t.Fatalf("expected time range 'day', got %q", history.TimeRange)
	}
}

func TestUpcomingService_GenerateHistory_WithExecutions(t *testing.T) {
	upcomingSvc, taskRepo, llmConfigRepo, execRepo, projectID := setupUpcomingTest(t)

	agent := &models.LLMConfig{
		Name:     "Test Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-20250514",
	}
	if err := llmConfigRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("creating agent: %v", err)
	}

	task := &models.Task{
		ProjectID: projectID,
		Title:     "Executed Task",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "Do it",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Do it",
	}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatalf("creating execution: %v", err)
	}
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "Task completed", "", 100, 5000); err != nil {
		t.Fatalf("completing execution: %v", err)
	}

	history, err := upcomingSvc.GenerateHistory(context.Background(), projectID, models.TimeRangeDay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if history.Summary.TotalExecutions != 1 {
		t.Fatalf("expected 1 execution, got %d", history.Summary.TotalExecutions)
	}
	if history.Summary.SuccessCount != 1 {
		t.Fatalf("expected 1 success, got %d", history.Summary.SuccessCount)
	}
	if len(history.Executions) != 1 {
		t.Fatalf("expected 1 execution record, got %d", len(history.Executions))
	}
	if history.Executions[0].TaskTitle != "Executed Task" {
		t.Fatalf("expected task title 'Executed Task', got %q", history.Executions[0].TaskTitle)
	}
}

func TestUpcomingService_GenerateHistory_TimeRanges(t *testing.T) {
	upcomingSvc, _, _, _, projectID := setupUpcomingTest(t)

	// Test all time ranges produce valid history
	for _, tr := range []models.TimeRange{models.TimeRangeHour, models.TimeRangeDay, models.TimeRangeWeek} {
		history, err := upcomingSvc.GenerateHistory(context.Background(), projectID, tr)
		if err != nil {
			t.Fatalf("unexpected error for range %q: %v", tr, err)
		}
		if history.TimeRange != tr {
			t.Fatalf("expected time range %q, got %q", tr, history.TimeRange)
		}
	}
}

func TestParseGitLog(t *testing.T) {
	// Hashes must be exactly 40 hex characters
	hash1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 40 chars
	hash2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // 40 chars
	input := hash1 + "|abc1234|Alice|2026-03-01T10:00:00-05:00|Add new feature\n\n 3 files changed, 15 insertions(+), 2 deletions(-)\n" +
		hash2 + "|def5678|Bob|2026-02-28T14:30:00-05:00|Fix critical bug\n\n 1 file changed, 5 insertions(+), 10 deletions(-)"

	commits, err := parseGitLog(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}

	// First commit
	if commits[0].ShortHash != "abc1234" {
		t.Errorf("expected short hash 'abc1234', got %q", commits[0].ShortHash)
	}
	if commits[0].Author != "Alice" {
		t.Errorf("expected author 'Alice', got %q", commits[0].Author)
	}
	if commits[0].Subject != "Add new feature" {
		t.Errorf("expected subject 'Add new feature', got %q", commits[0].Subject)
	}
	if commits[0].FilesChanged != 3 {
		t.Errorf("expected 3 files changed, got %d", commits[0].FilesChanged)
	}
	if commits[0].Insertions != 15 {
		t.Errorf("expected 15 insertions, got %d", commits[0].Insertions)
	}
	if commits[0].Deletions != 2 {
		t.Errorf("expected 2 deletions, got %d", commits[0].Deletions)
	}

	// Second commit
	if commits[1].ShortHash != "def5678" {
		t.Errorf("expected short hash 'def5678', got %q", commits[1].ShortHash)
	}
	if commits[1].Insertions != 5 {
		t.Errorf("expected 5 insertions, got %d", commits[1].Insertions)
	}
	if commits[1].Deletions != 10 {
		t.Errorf("expected 10 deletions, got %d", commits[1].Deletions)
	}
}

func TestParseGitLog_Empty(t *testing.T) {
	commits, err := parseGitLog("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("expected 0 commits, got %d", len(commits))
	}
}

func TestParseFileTypes(t *testing.T) {
	input := `main.go
handler.go
handler_test.go
models.templ
config.yaml
README.md
`
	fts := parseFileTypes(input)
	if len(fts) == 0 {
		t.Fatal("expected file types, got none")
	}

	// Build a map for easy lookup
	ftMap := map[string]int{}
	for _, ft := range fts {
		ftMap[ft.Extension] = ft.Count
	}

	if ftMap[".go"] != 3 {
		t.Errorf("expected 3 .go files, got %d", ftMap[".go"])
	}
	if ftMap[".templ"] != 1 {
		t.Errorf("expected 1 .templ file, got %d", ftMap[".templ"])
	}
	if ftMap[".yaml"] != 1 {
		t.Errorf("expected 1 .yaml file, got %d", ftMap[".yaml"])
	}
	if ftMap[".md"] != 1 {
		t.Errorf("expected 1 .md file, got %d", ftMap[".md"])
	}
}

func TestCategorizeCommits(t *testing.T) {
	commits := []models.GitCommit{
		{Subject: "Add new dashboard feature"},
		{Subject: "Fix login bug"},
		{Subject: "feat: implement user auth"},
		{Subject: "Refactor database layer"},
		{Subject: "Update README"},
		{Subject: "hotfix: patch security issue"},
		{Subject: "config: update CI pipeline"},
	}

	cs := categorizeCommits(commits)

	if len(cs.Features) != 3 {
		t.Errorf("expected 3 features, got %d: %v", len(cs.Features), cs.Features)
	}
	if len(cs.BugFixes) != 2 {
		t.Errorf("expected 2 bug fixes, got %d: %v", len(cs.BugFixes), cs.BugFixes)
	}
	if len(cs.ConfigChanges) != 2 {
		t.Errorf("expected 2 config changes, got %d: %v", len(cs.ConfigChanges), cs.ConfigChanges)
	}
}

func TestGetProjectChanges_InvalidPath(t *testing.T) {
	svc := &UpcomingService{}
	_, err := svc.getProjectChanges("/nonexistent/path", time.Now().Add(-24*time.Hour))
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestGetProjectChanges_RealRepo(t *testing.T) {
	// Test against the actual repo we're in
	svc := &UpcomingService{}
	since := time.Now().Add(-7 * 24 * time.Hour)
	pc, err := svc.getProjectChanges(".", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pc.Available {
		t.Fatal("expected available to be true")
	}
	// We know the repo has commits, so there should be at least some
	if pc.TotalCommits == 0 {
		t.Log("warning: no commits found in last week (this is OK in CI)")
	}
}

func TestParseShortStat(t *testing.T) {
	commit := &models.GitCommit{}
	parseShortStat(" 3 files changed, 15 insertions(+), 2 deletions(-)", commit)

	if commit.FilesChanged != 3 {
		t.Errorf("expected 3 files changed, got %d", commit.FilesChanged)
	}
	if commit.Insertions != 15 {
		t.Errorf("expected 15 insertions, got %d", commit.Insertions)
	}
	if commit.Deletions != 2 {
		t.Errorf("expected 2 deletions, got %d", commit.Deletions)
	}
}

func TestParseShortStat_InsertOnly(t *testing.T) {
	commit := &models.GitCommit{}
	parseShortStat(" 1 file changed, 42 insertions(+)", commit)

	if commit.FilesChanged != 1 {
		t.Errorf("expected 1 file changed, got %d", commit.FilesChanged)
	}
	if commit.Insertions != 42 {
		t.Errorf("expected 42 insertions, got %d", commit.Insertions)
	}
	if commit.Deletions != 0 {
		t.Errorf("expected 0 deletions, got %d", commit.Deletions)
	}
}
