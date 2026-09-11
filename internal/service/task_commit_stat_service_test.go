package service

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestParseNumstatLines(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []parsedNumstatLine
	}{
		{
			name:   "normal rows",
			output: "12\t3\tadded.go\n0\t7\tdeleted.go\n5\t2\tmodified.go\n",
			want: []parsedNumstatLine{
				{insertions: 12, deletions: 3, path: "added.go"},
				{insertions: 0, deletions: 7, path: "deleted.go"},
				{insertions: 5, deletions: 2, path: "modified.go"},
			},
		},
		{
			name:   "empty output",
			output: "",
		},
		{
			name:   "blank and malformed rows",
			output: "\nnot a numstat row\n1\t2\nbad\trows\tmalformed.go\n",
			want: []parsedNumstatLine{
				{path: "malformed.go"},
			},
		},
		{
			name:   "binary row",
			output: "-\t-\timage.png\n",
			want: []parsedNumstatLine{
				{path: "image.png"},
			},
		},
		{
			name:   "tab-delimited path",
			output: "8\t4\tdir\tfile\twith\ttabs.go\n",
			want: []parsedNumstatLine{
				{insertions: 8, deletions: 4, path: "dir\tfile\twith\ttabs.go"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNumstatLines(tt.output)
			if err != nil {
				t.Fatalf("parseNumstatLines: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parsed numstat = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseNumstatLinesReturnsScannerError(t *testing.T) {
	_, err := parseNumstatLines(strings.Repeat("x", bufio.MaxScanTokenSize+1))
	if err == nil {
		t.Fatal("parseNumstatLines returned nil error for an overlong row")
	}
}

func TestAddNumstatLinesDeduplicatesFilesWithoutChangingTotals(t *testing.T) {
	stat := &models.TaskCommitStat{}
	seenFiles := map[string]bool{}
	var files []string

	if err := addNumstatLines(stat, "1\t2\tduplicate.go\n3\t4\tduplicate.go\n", seenFiles, &files); err != nil {
		t.Fatalf("addNumstatLines: %v", err)
	}
	if stat.Insertions != 4 || stat.Deletions != 6 {
		t.Fatalf("totals = %d/%d, want 4/6", stat.Insertions, stat.Deletions)
	}
	if !reflect.DeepEqual(files, []string{"duplicate.go"}) {
		t.Fatalf("files = %#v, want duplicate.go once", files)
	}
}

func TestApplyNumstatLinesPreservesDirectCommitFileDuplicates(t *testing.T) {
	numstatLines, err := parseNumstatLines("1\t2\tduplicate.go\n3\t4\tduplicate.go\n")
	if err != nil {
		t.Fatalf("parseNumstatLines: %v", err)
	}
	stat := &models.TaskCommitStat{}
	var files []string
	applyNumstatLines(stat, numstatLines, nil, &files)

	if stat.Insertions != 4 || stat.Deletions != 6 {
		t.Fatalf("totals = %d/%d, want 4/6", stat.Insertions, stat.Deletions)
	}
	if !reflect.DeepEqual(files, []string{"duplicate.go", "duplicate.go"}) {
		t.Fatalf("files = %#v, want duplicate.go twice", files)
	}
}

func TestParseNumstatLinesNULDelimitedPathsAndRenameCopyRecords(t *testing.T) {
	oldPath := "reports/old\tname.go"
	newPath := "reports/new\nname.go"
	quotePath := "reports/quote\"name.go"
	backslashPath := "reports/back\\name.go"
	output := "1\t2\t" + oldPath + "\x00" +
		"0\t0\t\x00" + oldPath + "\x00" + newPath + "\x00" +
		"-\t-\t" + quotePath + "\x00" +
		"3\t4\t" + backslashPath + "\x00"

	got, err := parseNumstatLines(output)
	if err != nil {
		t.Fatalf("parseNumstatLines: %v", err)
	}
	want := []parsedNumstatLine{
		{insertions: 1, deletions: 2, path: oldPath},
		{path: oldPath + " => " + newPath},
		{path: quotePath},
		{insertions: 3, deletions: 4, path: backslashPath},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed NUL numstat = %#v, want %#v", got, want)
	}
}

func TestCollectProducedCommitStatRoundTripsGitSpecialPaths(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	project := &models.Project{Name: "Special Commit Paths", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Record special paths", Category: models.CategoryActive, Status: models.StatusRunning, WorktreePath: repoDir}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execModel := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "change special paths"}
	if err := execRepo.Create(ctx, execModel); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	wantPaths := []string{
		"reports/odd\tname.go",
		"reports/line\nname.go",
		"reports/quote\"name.go",
		"reports/back\\name.go",
	}
	for _, path := range wantPaths {
		fullPath := filepath.Join(repoDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir for %q: %v", path, err)
		}
		if err := os.WriteFile(fullPath, []byte("package special\n\nfunc Added() {}\n"), 0644); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}

	llmSvc := &LLMService{taskCommitStatRepo: statRepo}
	if err := llmSvc.CommitTaskWorktreeChanges(ctx, task, execModel, repoDir, "Add special path files"); err != nil {
		t.Fatalf("CommitTaskWorktreeChanges: %v", err)
	}
	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	var gotPaths []string
	if err := json.Unmarshal([]byte(stats[0].ChangedFilesJSON), &gotPaths); err != nil {
		t.Fatalf("decode changed files: %v", err)
	}
	gotSet := make(map[string]bool, len(gotPaths))
	for _, path := range gotPaths {
		gotSet[path] = true
	}
	wantSet := make(map[string]bool, len(wantPaths))
	for _, path := range wantPaths {
		wantSet[path] = true
	}
	if !reflect.DeepEqual(gotSet, wantSet) || len(gotPaths) != len(wantPaths) {
		t.Fatalf("changed files = %#v, want exact paths %#v", gotPaths, wantPaths)
	}
	if stats[0].FilesChanged != len(wantPaths) {
		t.Fatalf("files changed = %d, want %d", stats[0].FilesChanged, len(wantPaths))
	}
	upcomingSvc := NewUpcomingService(repository.NewUpcomingRepo(db))
	upcomingSvc.SetTaskCommitStatRepo(statRepo)
	projectChanges, _, err := upcomingSvc.buildProjectChangesFromTaskCommitStats(ctx, project.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("build project changes: %v", err)
	}
	if projectChanges == nil || projectChanges.FilesChanged != len(wantPaths) || !reflect.DeepEqual(projectChanges.FileTypes, []models.FileTypeCount{{Extension: ".go", Count: len(wantPaths)}}) {
		t.Fatalf("project changes = %#v, want all special paths classified as .go", projectChanges)
	}
}

func TestCollectProducedCommitStatPreservesSpecialRenameAndCopyRecords(t *testing.T) {
	repoDir := createTestGitRepo(t)
	oldPath := "reports/old\tname.go"
	newPath := "reports/new\nname.go"
	oldFullPath := filepath.Join(repoDir, oldPath)
	if err := os.MkdirAll(filepath.Dir(oldFullPath), 0755); err != nil {
		t.Fatalf("mkdir rename source: %v", err)
	}
	if err := os.WriteFile(oldFullPath, []byte("package renamed\n\nfunc Same() {}\n"), 0644); err != nil {
		t.Fatalf("write rename source: %v", err)
	}
	if err := CommitWorktreeChanges(repoDir, "Add rename source"); err != nil {
		t.Fatalf("commit rename source: %v", err)
	}
	if err := os.Rename(oldFullPath, filepath.Join(repoDir, newPath)); err != nil {
		t.Fatalf("rename special path: %v", err)
	}
	if err := CommitWorktreeChanges(repoDir, "Rename special path"); err != nil {
		t.Fatalf("commit rename: %v", err)
	}
	renameSHA, err := gitCommitSHA(repoDir, "HEAD")
	if err != nil {
		t.Fatalf("rename SHA: %v", err)
	}
	stat, err := collectProducedCommitStat(repoDir, renameSHA)
	if err != nil {
		t.Fatalf("collect rename stat: %v", err)
	}
	if stat.Insertions != 0 || stat.Deletions != 0 || stat.FilesChanged != 1 {
		t.Fatalf("rename totals = %d/%d files=%d, want 0/0/1", stat.Insertions, stat.Deletions, stat.FilesChanged)
	}
	var renamePaths []string
	if err := json.Unmarshal([]byte(stat.ChangedFilesJSON), &renamePaths); err != nil {
		t.Fatalf("decode rename changed files: %v", err)
	}
	if !reflect.DeepEqual(renamePaths, []string{oldPath + " => " + newPath}) {
		t.Fatalf("rename paths = %#v, want one raw rename path", renamePaths)
	}

	copyPath := "reports/copy\"name.go"
	if err := os.WriteFile(filepath.Join(repoDir, copyPath), []byte("package renamed\n\nfunc Same() {}\n"), 0644); err != nil {
		t.Fatalf("write copy path: %v", err)
	}
	if err := CommitWorktreeChanges(repoDir, "Copy special path"); err != nil {
		t.Fatalf("commit copy: %v", err)
	}
	cmd := exec.Command("git", "diff", "--find-copies-harder", "--numstat", "-z", "HEAD^", "HEAD")
	cmd.Dir = repoDir
	copyOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("read copy numstat: %v", err)
	}
	copyLines, err := parseNumstatLines(string(copyOutput))
	if err != nil {
		t.Fatalf("parse copy numstat: %v", err)
	}
	if !reflect.DeepEqual(copyLines, []parsedNumstatLine{{path: newPath + " => " + copyPath}}) {
		t.Fatalf("copy numstat = %#v, want raw rename/copy path", copyLines)
	}
}

func TestCommitTaskWorktreeChangesRecordsProducedCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	project := &models.Project{Name: "Commit Stats", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Record task commit", Category: models.CategoryActive, Status: models.StatusRunning, WorktreePath: repoDir}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execModel := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "change files"}
	if err := execRepo.Create(ctx, execModel); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	completedAt := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	execModel.CompletedAt = &completedAt

	if err := os.WriteFile(filepath.Join(repoDir, "feature.go"), []byte("package feature\n\nfunc Added() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	llmSvc := &LLMService{taskCommitStatRepo: statRepo}
	if err := llmSvc.CommitTaskWorktreeChanges(ctx, task, execModel, repoDir, "Add feature file"); err != nil {
		t.Fatalf("CommitTaskWorktreeChanges: %v", err)
	}

	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, completedAt.Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	stat := stats[0]
	if stat.TaskID != task.ID || stat.ExecutionID == nil || *stat.ExecutionID != execModel.ID {
		t.Fatalf("stat task/execution = %s/%v, want %s/%s", stat.TaskID, stat.ExecutionID, task.ID, execModel.ID)
	}
	if stat.Subject != "Add feature file" || stat.ShortSHA == "" || stat.CommitSHA == "" {
		t.Fatalf("unexpected commit metadata: %#v", stat)
	}
	if !stat.ProducedAt.Equal(completedAt) {
		t.Fatalf("produced_at = %s, want %s", stat.ProducedAt, completedAt)
	}
	if stat.Insertions == 0 || stat.FilesChanged != 1 || stat.ChangedFilesJSON != `["feature.go"]` {
		t.Fatalf("unexpected stat totals: insertions=%d files=%d changed=%s", stat.Insertions, stat.FilesChanged, stat.ChangedFilesJSON)
	}
}

func TestHandlePostExecutionRecordsProducedCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	project := &models.Project{Name: "Post Execution Stats", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{
		ProjectID:      project.ID,
		Title:          "Record post-execution commit",
		Category:       models.CategoryActive,
		Status:         models.StatusRunning,
		WorktreePath:   repoDir,
		WorktreeBranch: "main",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execModel := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "post execution change"}
	if err := execRepo.Create(ctx, execModel); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	completedAt := time.Date(2026, 8, 17, 15, 45, 0, 0, time.UTC)
	execModel.CompletedAt = &completedAt

	if err := os.WriteFile(filepath.Join(repoDir, "post_execution.go"), []byte("package postexecution\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	llmSvc := &LLMService{execRepo: execRepo, taskCommitStatRepo: statRepo}
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, repository.NewSettingsRepo(db))
	worktreeSvc.SetLLMService(llmSvc)
	worktreeSvc.HandlePostExecution(ctx, task, execModel, repoDir)

	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, completedAt.Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	if stats[0].ExecutionID == nil || *stats[0].ExecutionID != execModel.ID {
		t.Fatalf("execution_id = %v, want %s", stats[0].ExecutionID, execModel.ID)
	}
	if stats[0].ChangedFilesJSON != `["post_execution.go"]` {
		t.Fatalf("changed files = %s, want post_execution.go", stats[0].ChangedFilesJSON)
	}
}

func TestMergeBranchRecordsAppCreatedMergeCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	project := &models.Project{Name: "Merge Commit Stats", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if out, err := gitOutput(repoDir, "checkout", "-b", "task/merge-stat"); err != nil {
		t.Fatalf("checkout task branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "merge_stat.go"), []byte("package mergestat\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := CommitWorktreeChanges(repoDir, "Add merge stat file"); err != nil {
		t.Fatalf("commit task branch: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Merge stat task", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: repoDir, WorktreeBranch: "task/merge-stat", MergeTargetBranch: "main"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	llmSvc := &LLMService{taskCommitStatRepo: statRepo}
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, repository.NewSettingsRepo(db))
	worktreeSvc.SetLLMService(llmSvc)

	result, err := worktreeSvc.MergeBranch(ctx, task, repoDir, "merge")
	if err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if result == nil || !result.Success || result.MergeCommit == "" {
		t.Fatalf("merge result = %#v, want success with merge commit", result)
	}
	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	if stats[0].CommitSHA != result.MergeCommit || stats[0].Subject != "Merge task: Merge stat task" {
		t.Fatalf("recorded stat = %#v, want merge commit %s", stats[0], result.MergeCommit)
	}
}

func TestTaskCommitStatUpsertDoesNotDoubleCountDuplicateCommit(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Duplicate Stats"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Duplicate commit", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	producedAt := time.Now().UTC().Add(-time.Hour)
	stat := &models.TaskCommitStat{
		ProjectID: project.ID, TaskID: task.ID, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ShortSHA: "aaaaaaa",
		Subject: "Add duplicate-safe stats", Author: "OpenVibely Bot", ProducedAt: producedAt,
		Insertions: 5, Deletions: 1, FilesChanged: 1, ChangedFilesJSON: `["a.go"]`,
	}
	if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	stat.Insertions = 9
	if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	svc := NewUpcomingService(repository.NewUpcomingRepo(db))
	svc.SetTaskCommitStatRepo(statRepo)
	history, err := svc.GenerateHistory(ctx, project.ID, models.TimeRangeWeek)
	if err != nil {
		t.Fatalf("GenerateHistory: %v", err)
	}
	if history.ProjectChanges == nil || history.ProjectChanges.TotalCommits != 1 || history.ProjectChanges.TotalInsertions != 9 {
		t.Fatalf("ProjectChanges = %#v, want one updated commit", history.ProjectChanges)
	}
}

func TestTaskCommitStatsCascadeOnTaskDelete(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Task Cascade"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Delete me", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	insertTaskCommitStat(t, ctx, statRepo, project.ID, task.ID)
	if err := taskRepo.Delete(ctx, task.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	assertTaskCommitStatCount(t, db, 0)
}

func TestTaskCommitStatsCascadeOnProjectDelete(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	statRepo := repository.NewTaskCommitStatRepo(db)

	project := &models.Project{Name: "Project Cascade"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Project-owned task", Category: models.CategoryActive, Status: models.StatusCompleted}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	insertTaskCommitStat(t, ctx, statRepo, project.ID, task.ID)
	if err := projectRepo.Delete(ctx, project.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	assertTaskCommitStatCount(t, db, 0)
}

func insertTaskCommitStat(t *testing.T, ctx context.Context, statRepo *repository.TaskCommitStatRepo, projectID, taskID string) {
	t.Helper()
	stat := &models.TaskCommitStat{
		ProjectID: projectID, TaskID: taskID, CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ShortSHA: "bbbbbbb",
		Subject: "Add cascade stat", Author: "OpenVibely Bot", ProducedAt: time.Now().UTC(),
		Insertions: 1, FilesChanged: 1, ChangedFilesJSON: `["cascade.go"]`,
	}
	if err := statRepo.UpsertProducedCommitStat(ctx, stat); err != nil {
		t.Fatalf("insert stat: %v", err)
	}
}

func assertTaskCommitStatCount(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM task_commit_stats`).Scan(&count); err != nil {
		t.Fatalf("count stats: %v", err)
	}
	if count != want {
		t.Fatalf("task_commit_stats count = %d, want %d", count, want)
	}
}
