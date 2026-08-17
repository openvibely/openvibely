package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

// CommitTaskWorktreeChanges commits OpenVibely-produced task turn changes and records summary-only commit stats.
func (s *LLMService) CommitTaskWorktreeChanges(ctx context.Context, task *models.Task, execModel *models.Execution, worktreePath, message string) error {
	beforeSHA, _ := gitCommitSHA(worktreePath, "HEAD")
	if err := CommitWorktreeChanges(worktreePath, message); err != nil {
		return err
	}
	afterSHA, err := gitCommitSHA(worktreePath, "HEAD")
	if err != nil || afterSHA == "" || afterSHA == beforeSHA {
		return nil
	}
	if s == nil || s.taskCommitStatRepo == nil || task == nil {
		return nil
	}
	if err := s.recordProducedCommitStat(ctx, task, execModel, worktreePath, afterSHA); err != nil {
		applog.Infof("[task-commit-stats] error recording produced commit stat task=%s sha=%s: %v", task.ID, afterSHA, err)
	}
	return nil
}

func (s *LLMService) recordProducedCommitStat(ctx context.Context, task *models.Task, execModel *models.Execution, worktreePath, sha string) error {
	stat, err := collectProducedCommitStat(worktreePath, sha)
	if err != nil {
		return err
	}
	stat.ProjectID = task.ProjectID
	stat.TaskID = task.ID
	if execModel != nil {
		if execModel.ID != "" {
			stat.ExecutionID = &execModel.ID
		}
		if execModel.CompletedAt != nil && !execModel.CompletedAt.IsZero() {
			stat.ProducedAt = execModel.CompletedAt.UTC()
		} else if s.execRepo != nil && execModel.ID != "" {
			storedExec, err := s.execRepo.GetByID(ctx, execModel.ID)
			if err == nil && storedExec != nil && storedExec.CompletedAt != nil && !storedExec.CompletedAt.IsZero() {
				stat.ProducedAt = storedExec.CompletedAt.UTC()
			}
		}
	}
	if stat.ProducedAt.IsZero() {
		stat.ProducedAt = time.Now().UTC()
	}
	return s.taskCommitStatRepo.UpsertProducedCommitStat(ctx, stat)
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

	var files []string
	scanner := bufio.NewScanner(strings.NewReader(string(numstatOut)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		if n, err := strconv.Atoi(fields[0]); err == nil {
			stat.Insertions += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			stat.Deletions += n
		}
		files = append(files, strings.Join(fields[2:], "\t"))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning commit numstat: %w", err)
	}
	stat.FilesChanged = len(files)
	changedFilesJSON, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("encoding changed files: %w", err)
	}
	stat.ChangedFilesJSON = string(changedFilesJSON)
	return stat, nil
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
