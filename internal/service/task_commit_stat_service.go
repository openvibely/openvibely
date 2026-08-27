package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// SetTaskCommitStatRepo sets the repository used to record task-produced commit metrics.
func (s *LLMService) SetTaskCommitStatRepo(repo *repository.TaskCommitStatRepo) {
	s.taskCommitStatRepo = repo
}

// CommitTaskWorktreeChanges commits OpenVibely-produced task changes and records summary-only commit stats.
func (s *LLMService) CommitTaskWorktreeChanges(ctx context.Context, task *models.Task, execModel *models.Execution, worktreePath, message string) error {
	beforeSHA, _ := gitCommitSHA(worktreePath, "HEAD")
	if err := CommitWorktreeChanges(worktreePath, message); err != nil {
		return err
	}
	afterSHA, err := gitCommitSHA(worktreePath, "HEAD")
	if err != nil || afterSHA == "" || afterSHA == beforeSHA {
		return nil
	}
	if err := s.RecordTaskCommitStat(ctx, task, execModel, worktreePath, afterSHA); err != nil {
		applog.Infof("[task-commit-stats] error recording produced commit stat task=%s sha=%s: %v", task.ID, afterSHA, err)
	}
	return nil
}

// RecordTaskCommitStat records summary-only stats for a commit the app produced for a task.
func (s *LLMService) RecordTaskCommitStat(ctx context.Context, task *models.Task, execModel *models.Execution, repoPath, sha string) error {
	if s == nil || s.taskCommitStatRepo == nil || task == nil {
		return nil
	}
	return recordProducedCommitStat(ctx, s.taskCommitStatRepo, s.execRepo, task, execModel, repoPath, sha)
}

func recordProducedCommitStat(ctx context.Context, statRepo *repository.TaskCommitStatRepo, execRepo *repository.ExecutionRepo, task *models.Task, execModel *models.Execution, repoPath, sha string) error {
	if statRepo == nil || task == nil {
		return nil
	}
	stat, err := collectProducedCommitStat(repoPath, sha)
	if err != nil {
		return err
	}
	applyTaskCommitStatContext(ctx, stat, execRepo, task, execModel)
	return statRepo.UpsertProducedCommitStat(ctx, stat)
}

func applyTaskCommitStatContext(ctx context.Context, stat *models.TaskCommitStat, execRepo *repository.ExecutionRepo, task *models.Task, execModel *models.Execution) {
	stat.ProjectID = task.ProjectID
	stat.TaskID = task.ID
	if execModel != nil {
		if execModel.ID != "" {
			stat.ExecutionID = &execModel.ID
		}
		if execModel.CompletedAt != nil && !execModel.CompletedAt.IsZero() {
			stat.ProducedAt = execModel.CompletedAt.UTC()
		} else if execRepo != nil && execModel.ID != "" {
			storedExec, err := execRepo.GetByID(ctx, execModel.ID)
			if err == nil && storedExec != nil && storedExec.CompletedAt != nil && !storedExec.CompletedAt.IsZero() {
				stat.ProducedAt = storedExec.CompletedAt.UTC()
			}
		}
	}
	if stat.ProducedAt.IsZero() {
		stat.ProducedAt = time.Now().UTC()
	}
}

func collectPublishedBranchCommitStat(worktreePath, baseRef, sha, subject, author string, commitStats *GitHubPublishedCommitStats) (*models.TaskCommitStat, error) {
	stat, err := newPublishedBranchCommitStat(sha, subject, author)
	if err != nil {
		return nil, err
	}
	if commitStats != nil {
		addGitHubPublishedCommitStats(stat, commitStats)
		return stat, nil
	}
	if err := addNumstatFromGitDiff(worktreePath, baseRef, stat); err != nil {
		return nil, err
	}
	return stat, nil
}

func newPublishedBranchCommitStat(sha, subject, author string) (*models.TaskCommitStat, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return nil, fmt.Errorf("commit sha is required")
	}
	stat := &models.TaskCommitStat{
		CommitSHA: sha,
		ShortSHA:  shortCommitSHA(sha),
		Author:    strings.TrimSpace(author),
		Subject:   strings.TrimSpace(subject),
	}
	if stat.Author == "" {
		stat.Author = "OpenVibely Bot"
	}
	if stat.Subject == "" {
		stat.Subject = "Publish task branch"
	}
	return stat, nil
}

func addGitHubPublishedCommitStats(stat *models.TaskCommitStat, commitStats *GitHubPublishedCommitStats) {
	stat.Insertions = commitStats.Insertions
	stat.Deletions = commitStats.Deletions
	seenFiles := map[string]bool{}
	changedFiles := make([]string, 0, len(commitStats.ChangedFiles))
	for _, file := range commitStats.ChangedFiles {
		path := strings.TrimSpace(file)
		if path == "" || seenFiles[path] {
			continue
		}
		seenFiles[path] = true
		changedFiles = append(changedFiles, path)
	}
	stat.FilesChanged = len(changedFiles)
	if stat.FilesChanged == 0 && commitStats.FilesChanged > 0 {
		stat.FilesChanged = commitStats.FilesChanged
	}
	changedFilesJSON, err := json.Marshal(changedFiles)
	if err != nil {
		stat.ChangedFilesJSON = "[]"
		return
	}
	stat.ChangedFilesJSON = string(changedFilesJSON)
}

func collectProducedCommitStat(worktreePath, sha string) (*models.TaskCommitStat, error) {
	metaCmd := exec.Command("git", "show", "-s", "--format=%H%n%h%n%an%n%s", sha)
	metaCmd.Dir = worktreePath
	metaOut, err := metaCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading commit metadata: %w", err)
	}
	parts := strings.SplitN(strings.TrimRight(string(metaOut), "\n"), "\n", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("unexpected commit metadata format")
	}

	stat := &models.TaskCommitStat{
		CommitSHA: parts[0],
		ShortSHA:  parts[1],
		Author:    parts[2],
		Subject:   parts[3],
	}

	numstatCmd := exec.Command("git", "show", "--numstat", "--format=", sha)
	numstatCmd.Dir = worktreePath
	numstatOut, err := numstatCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading commit numstat: %w", err)
	}

	numstatLines, err := parseNumstatLines(string(numstatOut))
	if err != nil {
		return nil, fmt.Errorf("scanning commit numstat: %w", err)
	}
	var files []string
	applyNumstatLines(stat, numstatLines, nil, &files)
	stat.FilesChanged = len(files)
	changedFilesJSON, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("encoding changed files: %w", err)
	}
	stat.ChangedFilesJSON = string(changedFilesJSON)
	return stat, nil
}

func addNumstatFromGitDiff(worktreePath, baseRef string, stat *models.TaskCommitStat) error {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return fmt.Errorf("base ref is required")
	}
	seenFiles := map[string]bool{}
	var files []string

	diffCmd := exec.Command("git", "diff", "--numstat", baseRef)
	diffCmd.Dir = worktreePath
	diffOut, err := diffCmd.Output()
	if err != nil {
		return fmt.Errorf("reading publish diff numstat: %w", err)
	}
	if err := addNumstatLines(stat, string(diffOut), seenFiles, &files); err != nil {
		return err
	}

	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	untrackedCmd.Dir = worktreePath
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return fmt.Errorf("reading untracked publish files: %w", err)
	}
	for _, field := range strings.Split(string(untrackedOut), "\x00") {
		path := strings.TrimSpace(filepath.ToSlash(field))
		if path == "" || seenFiles[path] {
			continue
		}
		absPath := filepath.Join(worktreePath, filepath.FromSlash(path))
		info, err := os.Lstat(absPath)
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		content, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("reading untracked publish file %s: %w", path, err)
		}
		stat.Insertions += countTextLines(content)
		seenFiles[path] = true
		files = append(files, path)
	}

	stat.FilesChanged = len(files)
	changedFilesJSON, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("encoding changed files: %w", err)
	}
	stat.ChangedFilesJSON = string(changedFilesJSON)
	return nil
}

func addNumstatLines(stat *models.TaskCommitStat, output string, seenFiles map[string]bool, files *[]string) error {
	numstatLines, err := parseNumstatLines(output)
	if err != nil {
		return fmt.Errorf("scanning commit numstat: %w", err)
	}
	applyNumstatLines(stat, numstatLines, seenFiles, files)
	return nil
}

type parsedNumstatLine struct {
	insertions int
	deletions  int
	path       string
}

func parseNumstatLines(output string) ([]parsedNumstatLine, error) {
	var numstatLines []parsedNumstatLine
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		parsed := parsedNumstatLine{path: strings.Join(fields[2:], "\t")}
		if n, err := strconv.Atoi(fields[0]); err == nil {
			parsed.insertions = n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			parsed.deletions = n
		}
		numstatLines = append(numstatLines, parsed)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return numstatLines, nil
}

func applyNumstatLines(stat *models.TaskCommitStat, numstatLines []parsedNumstatLine, seenFiles map[string]bool, files *[]string) {
	for _, numstatLine := range numstatLines {
		stat.Insertions += numstatLine.insertions
		stat.Deletions += numstatLine.deletions
		if seenFiles == nil {
			*files = append(*files, numstatLine.path)
			continue
		}
		if numstatLine.path != "" && !seenFiles[numstatLine.path] {
			seenFiles[numstatLine.path] = true
			*files = append(*files, numstatLine.path)
		}
	}
}

func countTextLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := 0
	for _, b := range content {
		if b == '\n' {
			lines++
		}
	}
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func shortCommitSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func gitCommitSHA(worktreePath, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
