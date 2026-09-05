package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestUpcomingService_AISummariesBuildPromptsAndTrimOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	llmSvc.SetLLMCaller(mock)

	project := &models.Project{Name: "Summary Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "summary-agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetProjectRepo(projectRepo)
	svc.SetLLMConfigRepo(llmConfigRepo)
	svc.SetLLMService(llmSvc)
	nextRun := time.Date(2026, 8, 16, 15, 4, 0, 0, time.UTC)
	upcoming := &models.Upcoming{
		ProjectID: project.ID,
		RunningTasks: []models.UpcomingTask{{
			Task:      models.Task{Title: "Run migration", Priority: 4},
			AgentName: "Builder",
		}},
		PendingTasks: []models.UpcomingTask{{
			Task: models.Task{Title: "Review rollout", Priority: 3},
		}},
		ScheduledTasks: []models.UpcomingTask{{
			Task:    models.Task{Title: "Nightly smoke"},
			NextRun: &nextRun,
		}},
		TaskSummary: &models.TaskSummary{TotalPending: 5, UrgentCount: 1, HighCount: 2, FailedCount: 1, OverdueCount: 3},
	}

	mock.Response = "  Pulse summary.  \n"
	pulse, err := svc.GeneratePulseSummary(ctx, project.ID, upcoming)
	if err != nil {
		t.Fatalf("GeneratePulseSummary: %v", err)
	}
	if pulse != "Pulse summary." {
		t.Fatalf("pulse summary = %q", pulse)
	}
	pulseCall := mock.LastCall()
	for _, want := range []string{"Running tasks: 1", "Run migration", "Pending tasks: 1", "Nightly smoke", "Task summary: 5 pending total"} {
		if !strings.Contains(pulseCall.Prompt, want) {
			t.Fatalf("pulse prompt missing %q:\n%s", want, pulseCall.Prompt)
		}
	}
	if pulseCall.WorkDir != project.RepoPath {
		t.Fatalf("pulse workdir = %q, want %q", pulseCall.WorkDir, project.RepoPath)
	}

	history := &models.History{
		ProjectID: project.ID,
		TimeRange: models.TimeRangeWeek,
		Since:     nextRun.Add(-7 * 24 * time.Hour),
		Summary:   models.HistorySummary{TotalExecutions: 3, SuccessCount: 1, FailureCount: 1, CancelledCount: 1, AvgDurationMs: 2500},
		Executions: []models.HistoryExecution{{
			TaskTitle: "Failing deploy",
			Execution: models.Execution{Status: models.ExecFailed, ErrorMessage: strings.Repeat("x", 120)},
		}},
		ProjectChanges: &models.ProjectChanges{Available: true, TotalCommits: 2, TotalInsertions: 12, TotalDeletions: 4, FilesChanged: 3, Changes: models.ChangeSummary{Features: []string{"Add pulse"}, BugFixes: []string{"Fix retry"}}},
	}
	mock.Response = "\nReflection summary.\n"
	reflection, err := svc.GenerateReflectionSummary(ctx, project.ID, history)
	if err != nil {
		t.Fatalf("GenerateReflectionSummary: %v", err)
	}
	if reflection != "Reflection summary." {
		t.Fatalf("reflection summary = %q", reflection)
	}
	reflectionCall := mock.LastCall()
	for _, want := range []string{"Time range: week", "Average duration: 2500ms", "Failing deploy", strings.Repeat("x", 100) + "...", "Code changes: 2 commits", "Features: 1", "Bug fixes: 1"} {
		if !strings.Contains(reflectionCall.Prompt, want) {
			t.Fatalf("reflection prompt missing %q:\n%s", want, reflectionCall.Prompt)
		}
	}
	if reflectionCall.WorkDir != project.RepoPath {
		t.Fatalf("reflection workdir = %q, want %q", reflectionCall.WorkDir, project.RepoPath)
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

func TestGenerateHistoryUsesTaskCommitStatsForProjectChanges(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Reflection Stats"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Reflect task", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	baseTime := time.Now().UTC().Add(-time.Hour)
	stats := []*models.TaskCommitStat{
		{ProjectID: project.ID, TaskID: task.ID, CommitSHA: "1111111111111111111111111111111111111111", ShortSHA: "1111111", Subject: "Add API endpoint", Author: "OpenVibely Bot", ProducedAt: baseTime.Add(time.Minute), Insertions: 10, Deletions: 2, FilesChanged: 2, ChangedFilesJSON: `["api.go","README.md"]`},
		{ProjectID: project.ID, TaskID: task.ID, CommitSHA: "2222222222222222222222222222222222222222", ShortSHA: "2222222", Subject: "Fix API bug", Author: "OpenVibely Bot", ProducedAt: baseTime, Insertions: 3, Deletions: 4, FilesChanged: 2, ChangedFilesJSON: `["api.go","web.templ"]`},
	}
	for _, stat := range stats {
		if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
			t.Fatalf("upsert stat: %v", err)
		}
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetTaskCommitStatRepo(statRepo)
	history, err := svc.GenerateHistory(ctx, project.ID, models.TimeRangeDay)
	if err != nil {
		t.Fatalf("GenerateHistory: %v", err)
	}
	pc := history.ProjectChanges
	if pc == nil || !pc.Available {
		t.Fatalf("ProjectChanges unavailable: %#v", pc)
	}
	if pc.TotalCommits != 2 || pc.TotalInsertions != 13 || pc.TotalDeletions != 6 || pc.FilesChanged != 3 {
		t.Fatalf("totals = commits:%d +%d -%d files:%d, want 2 +13 -6 files:3", pc.TotalCommits, pc.TotalInsertions, pc.TotalDeletions, pc.FilesChanged)
	}
	if len(pc.Commits) != 2 || pc.Commits[0].Subject != "Add API endpoint" || pc.Commits[1].Subject != "Fix API bug" {
		t.Fatalf("commits = %#v, want produced_at desc mapping", pc.Commits)
	}
	if len(pc.Changes.Features) != 1 || len(pc.Changes.BugFixes) != 1 {
		t.Fatalf("changes = %#v, want one feature and one bugfix", pc.Changes)
	}
	fileTypes := map[string]int{}
	for _, ft := range pc.FileTypes {
		fileTypes[ft.Extension] = ft.Count
	}
	if fileTypes[".go"] != 1 || fileTypes[".md"] != 1 || fileTypes[".templ"] != 1 {
		t.Fatalf("file types = %#v, want unique changed-file extensions", fileTypes)
	}
}

func TestGenerateHistoryTaskCommitStatsCompactPreviewPreservesTotals(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Reflection Compact Stats"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Reflect compact task", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	baseTime := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	totalInsertions := 0
	totalDeletions := 0
	for i := 0; i < 24; i++ {
		subject := fmt.Sprintf("Add feature %02d", i)
		if i%3 == 1 {
			subject = fmt.Sprintf("Fix bug %02d", i)
		} else if i%3 == 2 {
			subject = fmt.Sprintf("Refactor config %02d", i)
		}
		changedFilesJSON := `{not-json`
		if i == 1 {
			changedFilesJSON = `["partial_valid.go", bad]`
		} else if i == 2 {
			changedFilesJSON = `["trailing_valid.go"] garbage`
		} else if i != 0 {
			payload, err := json.Marshal([]string{"shared.go", fmt.Sprintf("dir/file_%02d.ext%d", i, i)})
			if err != nil {
				t.Fatalf("marshal changed files: %v", err)
			}
			changedFilesJSON = string(payload)
		}
		insertions := i + 1
		deletions := i % 4
		totalInsertions += insertions
		totalDeletions += deletions
		stat := &models.TaskCommitStat{
			ProjectID: project.ID, TaskID: task.ID,
			CommitSHA: fmt.Sprintf("%040d", i+1), ShortSHA: fmt.Sprintf("%07d", i+1),
			Subject: subject, Author: "OpenVibely Bot", ProducedAt: baseTime.Add(-time.Duration(i) * time.Minute),
			Insertions: insertions, Deletions: deletions, FilesChanged: 2, ChangedFilesJSON: changedFilesJSON,
		}
		if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
			t.Fatalf("upsert stat %d: %v", i, err)
		}
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetTaskCommitStatRepo(statRepo)
	for _, tr := range []models.TimeRange{models.TimeRangeHour, models.TimeRangeDay, models.TimeRangeWeek} {
		history, err := svc.GenerateHistory(ctx, project.ID, tr)
		if err != nil {
			t.Fatalf("GenerateHistory(%s): %v", tr, err)
		}
		pc := history.ProjectChanges
		if pc == nil || !pc.Available {
			t.Fatalf("ProjectChanges unavailable for %s: %#v", tr, pc)
		}
		if pc.TotalCommits != 24 || pc.TotalInsertions != totalInsertions || pc.TotalDeletions != totalDeletions {
			t.Fatalf("%s totals = commits:%d +%d -%d, want 24 +%d -%d", tr, pc.TotalCommits, pc.TotalInsertions, pc.TotalDeletions, totalInsertions, totalDeletions)
		}
		if pc.FilesChanged != 22 {
			t.Fatalf("%s FilesChanged = %d, want 22 unique files despite duplicate shared.go and malformed JSON payloads", tr, pc.FilesChanged)
		}
		if len(pc.Commits) != 10 {
			t.Fatalf("%s rendered commit examples = %d, want capped 10", tr, len(pc.Commits))
		}
		for i, commit := range pc.Commits {
			want := fmt.Sprintf("%07d", i+1)
			if commit.ShortHash != want {
				t.Fatalf("%s commit[%d] short hash = %q, want newest-first %q", tr, i, commit.ShortHash, want)
			}
		}
		if got := changeSummaryFeatureCount(pc.Changes); got != 8 {
			t.Fatalf("%s feature count = %d, want 8", tr, got)
		}
		if got := changeSummaryBugFixCount(pc.Changes); got != 8 {
			t.Fatalf("%s bugfix count = %d, want 8", tr, got)
		}
		if got := changeSummaryConfigChangeCount(pc.Changes); got != 8 {
			t.Fatalf("%s config count = %d, want 8", tr, got)
		}
		if len(pc.Changes.Features) != 5 || len(pc.Changes.BugFixes) != 5 || len(pc.Changes.ConfigChanges) != 5 {
			t.Fatalf("%s category examples = features:%d bugs:%d config:%d, want all capped at 5", tr, len(pc.Changes.Features), len(pc.Changes.BugFixes), len(pc.Changes.ConfigChanges))
		}
		if len(pc.FileTypes) <= 12 {
			t.Fatalf("%s file types = %d, want more than 12 for badge overflow coverage", tr, len(pc.FileTypes))
		}
	}
}

func TestGenerateHistoryCombinesFallbackBeforeFirstTaskCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)
	repoDir := t.TempDir()
	runGit(t, repoDir, nil, "init", "-b", "main")
	runGit(t, repoDir, nil, "config", "user.name", "Test User")
	runGit(t, repoDir, nil, "config", "user.email", "test@example.com")

	oldCommitTime := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.WriteFile(filepath.Join(repoDir, "legacy.go"), []byte("package legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	dateEnv := []string{
		"GIT_AUTHOR_DATE=" + oldCommitTime.Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + oldCommitTime.Format(time.RFC3339),
	}
	runGit(t, repoDir, dateEnv, "add", "legacy.go")
	runGit(t, repoDir, dateEnv, "commit", "-m", "Add legacy fallback file")

	project := &models.Project{Name: "Reflection Mixed", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Reflect task", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	stat := &models.TaskCommitStat{
		ProjectID: project.ID, TaskID: task.ID,
		CommitSHA: "1111111111111111111111111111111111111111", ShortSHA: "1111111",
		Subject: "Add DB stat file", Author: "OpenVibely Bot", ProducedAt: time.Now().UTC().Add(-30 * time.Minute),
		Insertions: 4, Deletions: 1, FilesChanged: 1, ChangedFilesJSON: `["db_stat.go"]`,
	}
	if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
		t.Fatalf("upsert stat: %v", err)
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetProjectRepo(projectRepo)
	svc.SetTaskCommitStatRepo(statRepo)
	history, err := svc.GenerateHistory(ctx, project.ID, models.TimeRangeDay)
	if err != nil {
		t.Fatalf("GenerateHistory: %v", err)
	}
	pc := history.ProjectChanges
	if pc == nil || !pc.Available {
		t.Fatalf("ProjectChanges unavailable: %#v", pc)
	}
	if pc.TotalCommits != 2 {
		t.Fatalf("TotalCommits = %d, want DB stat plus pre-stat fallback commit", pc.TotalCommits)
	}
	subjects := map[string]bool{}
	for _, commit := range pc.Commits {
		subjects[commit.Subject] = true
	}
	if !subjects["Add DB stat file"] || !subjects["Add legacy fallback file"] {
		t.Fatalf("subjects = %#v, want DB stat and fallback commit", subjects)
	}
}

func TestGenerateHistoryDoesNotFallbackDuringCoveredQuietGap(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)
	repoDir := t.TempDir()
	runGit(t, repoDir, nil, "init", "-b", "main")
	runGit(t, repoDir, nil, "config", "user.name", "Test User")
	runGit(t, repoDir, nil, "config", "user.email", "test@example.com")

	rawCommitTime := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.WriteFile(filepath.Join(repoDir, "raw.go"), []byte("package raw\n"), 0o644); err != nil {
		t.Fatalf("write raw file: %v", err)
	}
	dateEnv := []string{
		"GIT_AUTHOR_DATE=" + rawCommitTime.Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + rawCommitTime.Format(time.RFC3339),
	}
	runGit(t, repoDir, dateEnv, "add", "raw.go")
	runGit(t, repoDir, dateEnv, "commit", "-m", "Raw covered-gap commit")

	project := &models.Project{Name: "Reflection Covered Gap", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Reflect covered gap", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	firstStat := &models.TaskCommitStat{
		ProjectID: project.ID, TaskID: task.ID,
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ShortSHA: "aaaaaaa",
		Subject: "Stats coverage started", Author: "OpenVibely Bot", ProducedAt: time.Now().UTC().Add(-48 * time.Hour),
		Insertions: 1, FilesChanged: 1, ChangedFilesJSON: `["coverage.go"]`,
	}
	currentStat := &models.TaskCommitStat{
		ProjectID: project.ID, TaskID: task.ID,
		CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ShortSHA: "bbbbbbb",
		Subject: "Current DB stat", Author: "OpenVibely Bot", ProducedAt: time.Now().UTC().Add(-30 * time.Minute),
		Insertions: 2, FilesChanged: 1, ChangedFilesJSON: `["current.go"]`,
	}
	for _, stat := range []*models.TaskCommitStat{firstStat, currentStat} {
		if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
			t.Fatalf("upsert stat: %v", err)
		}
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetProjectRepo(projectRepo)
	svc.SetTaskCommitStatRepo(statRepo)
	history, err := svc.GenerateHistory(ctx, project.ID, models.TimeRangeDay)
	if err != nil {
		t.Fatalf("GenerateHistory: %v", err)
	}
	pc := history.ProjectChanges
	if pc == nil || !pc.Available {
		t.Fatalf("ProjectChanges unavailable: %#v", pc)
	}
	if pc.TotalCommits != 1 {
		t.Fatalf("TotalCommits = %d, want only in-range DB stat and no covered-gap git fallback", pc.TotalCommits)
	}
	if len(pc.Commits) != 1 || pc.Commits[0].Subject != "Current DB stat" {
		t.Fatalf("commits = %#v, want only current DB stat", pc.Commits)
	}
}

var benchmarkHistoryProjectChanges *models.ProjectChanges

func BenchmarkTaskCommitStatHistoryProjection(b *testing.B) {
	svc, _, projectID, since := setupTaskCommitStatHistoryBenchmarkFixture(b, 1000, 50)
	b.ReportAllocs()
	b.ReportMetric(4, "sql/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc, _, err := svc.buildProjectChangesFromTaskCommitStats(context.Background(), projectID, since)
		if err != nil {
			b.Fatalf("buildProjectChangesFromTaskCommitStats: %v", err)
		}
		if pc == nil || pc.TotalCommits != 1000 || len(pc.Commits) != projectChangeCommitPreviewLimit {
			b.Fatalf("compact project changes = %#v", pc)
		}
		benchmarkHistoryProjectChanges = pc
	}
}

func setupTaskCommitStatHistoryBenchmarkFixture(tb testing.TB, rows, pathsPerCommit int) (*UpcomingService, *repository.TaskCommitStatRepo, string, time.Time) {
	tb.Helper()
	ctx := context.Background()
	db := testutil.NewTestDB(tb)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Reflection Benchmark"}
	if err := projectRepo.Create(ctx, project); err != nil {
		tb.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Benchmark task", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		tb.Fatalf("create task: %v", err)
	}

	baseTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < rows; i++ {
		files := make([]string, 0, pathsPerCommit)
		for j := 0; j < pathsPerCommit; j++ {
			files = append(files, fmt.Sprintf("pkg/shared/file_%02d.ext%d", j, j%25))
		}
		payload, err := json.Marshal(files)
		if err != nil {
			tb.Fatalf("marshal changed files: %v", err)
		}
		subject := fmt.Sprintf("Add benchmark feature %04d", i)
		if i%3 == 1 {
			subject = fmt.Sprintf("Fix benchmark bug %04d", i)
		} else if i%3 == 2 {
			subject = fmt.Sprintf("Refactor benchmark config %04d", i)
		}
		stat := &models.TaskCommitStat{
			ProjectID: project.ID, TaskID: task.ID,
			CommitSHA: fmt.Sprintf("%040d", i+1), ShortSHA: fmt.Sprintf("%07d", i+1),
			Subject: subject, Author: "OpenVibely Bot", ProducedAt: baseTime.Add(-time.Duration(i) * time.Second),
			Insertions: i%200 + 1, Deletions: i % 50, FilesChanged: pathsPerCommit, ChangedFilesJSON: string(payload),
		}
		if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
			tb.Fatalf("upsert stat %d: %v", i, err)
		}
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetTaskCommitStatRepo(statRepo)
	return svc, statRepo, project.ID, baseTime.Add(-time.Duration(rows+1) * time.Second)
}

func runGit(t *testing.T, dir string, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func TestFormatGitSinceUsesTimezoneSafeRFC3339(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	since := time.Date(2026, 8, 17, 7, 30, 15, 0, loc)
	got := formatGitSince(since)
	if got != "2026-08-17T12:30:15Z" {
		t.Fatalf("formatGitSince = %q, want UTC RFC3339 timestamp with timezone", got)
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("formatGitSince output is not RFC3339: %v", err)
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
