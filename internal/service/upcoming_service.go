package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type UpcomingService struct {
	upcomingRepo  *repository.UpcomingRepo
	backlogRepo   *repository.BacklogRepo
	projectRepo   *repository.ProjectRepo
	llmSvc        *LLMService
	llmConfigRepo *repository.LLMConfigRepo
}

func NewUpcomingService(upcomingRepo *repository.UpcomingRepo) *UpcomingService {
	return &UpcomingService{upcomingRepo: upcomingRepo}
}

// SetBacklogRepo sets the backlog repository for health metrics on the Pulse page
func (s *UpcomingService) SetBacklogRepo(backlogRepo *repository.BacklogRepo) {
	s.backlogRepo = backlogRepo
}

// SetProjectRepo sets the project repository for git change summaries
func (s *UpcomingService) SetProjectRepo(projectRepo *repository.ProjectRepo) {
	s.projectRepo = projectRepo
}

// SetLLMService sets the LLM service for AI summary generation
func (s *UpcomingService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// SetLLMConfigRepo sets the LLM config repository for AI summary generation
func (s *UpcomingService) SetLLMConfigRepo(llmConfigRepo *repository.LLMConfigRepo) {
	s.llmConfigRepo = llmConfigRepo
}

// GenerateUpcoming creates a summary of upcoming planned work for a project
func (s *UpcomingService) GenerateUpcoming(ctx context.Context, projectID string) (*models.Upcoming, error) {
	now := time.Now().UTC()

	running, err := s.upcomingRepo.ListRunningTasks(ctx, projectID)
	if err != nil {
		log.Printf("[upcoming-svc] error listing running tasks: %v", err)
		return nil, err
	}

	pending, err := s.upcomingRepo.ListPendingActiveTasks(ctx, projectID)
	if err != nil {
		log.Printf("[upcoming-svc] error listing pending tasks: %v", err)
		return nil, err
	}

	// Look ahead one week for scheduled tasks
	until := now.Add(7 * 24 * time.Hour)
	scheduled, err := s.upcomingRepo.ListUpcomingScheduledTasks(ctx, projectID, until)
	if err != nil {
		log.Printf("[upcoming-svc] error listing scheduled tasks: %v", err)
		return nil, err
	}

	// Fetch backlog health if repo is available
	var backlogHealth *models.BacklogHealthSnapshot
	if s.backlogRepo != nil {
		health, err := s.backlogRepo.GetLatestHealth(ctx, projectID)
		if err != nil {
			log.Printf("[upcoming-svc] error getting backlog health (non-fatal): %v", err)
		} else {
			backlogHealth = health
		}
	}

	// Fetch task summary metrics
	taskSummary, err := s.upcomingRepo.GetTaskSummary(ctx, projectID, now)
	if err != nil {
		log.Printf("[upcoming-svc] error getting task summary (non-fatal): %v", err)
	}

	upcoming := &models.Upcoming{
		ProjectID:      projectID,
		GeneratedAt:    now,
		RunningTasks:   running,
		PendingTasks:   pending,
		ScheduledTasks: scheduled,
		BacklogHealth:  backlogHealth,
		TaskSummary:    taskSummary,
	}

	log.Printf("[upcoming-svc] generated upcoming project=%s running=%d pending=%d scheduled=%d",
		projectID, len(running), len(pending), len(scheduled))

	return upcoming, nil
}

// GenerateHistory creates a summary of recently completed work for a project
func (s *UpcomingService) GenerateHistory(ctx context.Context, projectID string, timeRange models.TimeRange) (*models.History, error) {
	now := time.Now().UTC()
	since := computeSince(now, timeRange)

	summary, err := s.upcomingRepo.GetHistorySummary(ctx, projectID, since)
	if err != nil {
		log.Printf("[upcoming-svc] error getting history summary: %v", err)
		return nil, err
	}

	executions, err := s.upcomingRepo.ListRecentExecutions(ctx, projectID, since)
	if err != nil {
		log.Printf("[upcoming-svc] error listing recent executions: %v", err)
		return nil, err
	}

	// Fetch project changes from git
	var projectChanges *models.ProjectChanges
	if s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			log.Printf("[upcoming-svc] error getting project for git changes (non-fatal): %v", err)
		} else if project.RepoPath != "" {
			changes, err := s.getProjectChanges(project.RepoPath, since)
			if err != nil {
				log.Printf("[upcoming-svc] error getting git changes (non-fatal): %v", err)
			} else {
				projectChanges = changes
			}
		}
	}

	history := &models.History{
		ProjectID:      projectID,
		GeneratedAt:    now,
		TimeRange:      timeRange,
		Since:          since,
		Summary:        summary,
		Executions:     executions,
		ProjectChanges: projectChanges,
	}

	log.Printf("[upcoming-svc] generated history project=%s range=%s executions=%d success=%d failed=%d",
		projectID, timeRange, summary.TotalExecutions, summary.SuccessCount, summary.FailureCount)

	return history, nil
}

func computeSince(now time.Time, timeRange models.TimeRange) time.Time {
	switch timeRange {
	case models.TimeRangeHour:
		return now.Add(-1 * time.Hour)
	case models.TimeRangeDay:
		return now.Add(-24 * time.Hour)
	case models.TimeRangeWeek:
		return now.Add(-7 * 24 * time.Hour)
	default:
		return now.Add(-24 * time.Hour)
	}
}

// GeneratePulseSummary generates an AI summary of the current project state
func (s *UpcomingService) GeneratePulseSummary(ctx context.Context, projectID string, upcoming *models.Upcoming) (string, error) {
	if s.llmSvc == nil || s.llmConfigRepo == nil {
		return "", fmt.Errorf("LLM service not configured")
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return "", fmt.Errorf("no default model configured")
	}

	// Build a concise data summary for the prompt
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Running tasks: %d\n", len(upcoming.RunningTasks)))
	for _, t := range upcoming.RunningTasks {
		sb.WriteString(fmt.Sprintf("  - %s (agent: %s)\n", t.Task.Title, t.AgentName))
	}
	sb.WriteString(fmt.Sprintf("Pending tasks: %d\n", len(upcoming.PendingTasks)))
	for _, t := range upcoming.PendingTasks {
		sb.WriteString(fmt.Sprintf("  - %s (priority: %d)\n", t.Task.Title, t.Task.Priority))
	}
	sb.WriteString(fmt.Sprintf("Scheduled tasks: %d\n", len(upcoming.ScheduledTasks)))
	for _, t := range upcoming.ScheduledTasks {
		nextRun := "unscheduled"
		if t.NextRun != nil {
			nextRun = t.NextRun.Format("Jan 2, 3:04 PM")
		}
		sb.WriteString(fmt.Sprintf("  - %s (next: %s)\n", t.Task.Title, nextRun))
	}
	if upcoming.TaskSummary != nil {
		ts := upcoming.TaskSummary
		sb.WriteString(fmt.Sprintf("Task summary: %d pending total, %d urgent, %d high, %d failed, %d overdue\n",
			ts.TotalPending, ts.UrgentCount, ts.HighCount, ts.FailedCount, ts.OverdueCount))
	}
	if upcoming.BacklogHealth != nil {
		sb.WriteString(fmt.Sprintf("Backlog health: %.0f%% score, %d total, %d stale\n",
			upcoming.BacklogHealth.HealthScore, upcoming.BacklogHealth.TotalTasks, upcoming.BacklogHealth.StaleCount))
	}

	prompt := fmt.Sprintf(`You are summarizing the current state of a software project for a dashboard.
Given the following data about what is happening right now, write a brief 2-3 sentence summary.
Be direct and factual. Focus on what matters most: anything running, urgent items, failures, or overdue work.
If nothing notable is happening, say so simply.

Current state:
%s

Respond with ONLY the summary text, no formatting or labels.`, sb.String())

	var workDir string
	if s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err == nil && project != nil {
			workDir = project.RepoPath
		}
	}

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, workDir)
	if err != nil {
		return "", fmt.Errorf("AI summary generation failed: %w", err)
	}

	return strings.TrimSpace(output), nil
}

// GenerateReflectionSummary generates an AI summary of recent project history
func (s *UpcomingService) GenerateReflectionSummary(ctx context.Context, projectID string, history *models.History) (string, error) {
	if s.llmSvc == nil || s.llmConfigRepo == nil {
		return "", fmt.Errorf("LLM service not configured")
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return "", fmt.Errorf("no default model configured")
	}

	// Build a concise data summary
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Time range: %s (since %s)\n", history.TimeRange, history.Since.Format("Jan 2, 3:04 PM")))
	sb.WriteString(fmt.Sprintf("Executions: %d total, %d succeeded, %d failed, %d cancelled\n",
		history.Summary.TotalExecutions, history.Summary.SuccessCount, history.Summary.FailureCount, history.Summary.CancelledCount))
	if history.Summary.AvgDurationMs > 0 {
		sb.WriteString(fmt.Sprintf("Average duration: %dms\n", history.Summary.AvgDurationMs))
	}

	// Recent executions
	limit := 10
	if len(history.Executions) < limit {
		limit = len(history.Executions)
	}
	if limit > 0 {
		sb.WriteString("Recent executions:\n")
		for _, e := range history.Executions[:limit] {
			sb.WriteString(fmt.Sprintf("  - %s: %s", e.TaskTitle, e.Execution.Status))
			if e.Execution.Status == models.ExecFailed && e.Execution.ErrorMessage != "" {
				errMsg := e.Execution.ErrorMessage
				if len(errMsg) > 100 {
					errMsg = errMsg[:100] + "..."
				}
				sb.WriteString(fmt.Sprintf(" (%s)", errMsg))
			}
			sb.WriteString("\n")
		}
	}

	// Git changes
	if history.ProjectChanges != nil && history.ProjectChanges.Available {
		pc := history.ProjectChanges
		sb.WriteString(fmt.Sprintf("Code changes: %d commits, +%d/-%d lines, %d files\n",
			pc.TotalCommits, pc.TotalInsertions, pc.TotalDeletions, pc.FilesChanged))
		if len(pc.Changes.Features) > 0 {
			sb.WriteString(fmt.Sprintf("  Features: %d\n", len(pc.Changes.Features)))
		}
		if len(pc.Changes.BugFixes) > 0 {
			sb.WriteString(fmt.Sprintf("  Bug fixes: %d\n", len(pc.Changes.BugFixes)))
		}
	}

	prompt := fmt.Sprintf(`You are summarizing what has happened recently in a software project for a dashboard.
Given the following data about recent activity, write a brief 2-3 sentence summary.
Be direct and factual. Highlight successes, failures, and notable code changes.
If nothing happened in this period, say so simply.

Recent activity:
%s

Respond with ONLY the summary text, no formatting or labels.`, sb.String())

	var workDir string
	if s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err == nil && project != nil {
			workDir = project.RepoPath
		}
	}

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, workDir)
	if err != nil {
		return "", fmt.Errorf("AI summary generation failed: %w", err)
	}

	return strings.TrimSpace(output), nil
}

// getProjectChanges runs git commands against a project's repo to gather change data
func (s *UpcomingService) getProjectChanges(repoPath string, since time.Time) (*models.ProjectChanges, error) {
	// Verify this is a git repo
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolving repo path: %w", err)
	}

	sinceStr := since.Format("2006-01-02T15:04:05")

	// Get commit log with stats
	cmd := exec.Command("git", "log",
		"--since="+sinceStr,
		"--pretty=format:%H|%h|%an|%aI|%s",
		"--shortstat",
	)
	cmd.Dir = absPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running git log: %w", err)
	}

	commits, err := parseGitLog(string(out))
	if err != nil {
		return nil, fmt.Errorf("parsing git log: %w", err)
	}

	// Get files changed with their types
	cmd = exec.Command("git", "diff", "--stat", "--name-only",
		fmt.Sprintf("--since=%s", sinceStr),
		"HEAD",
	)
	cmd.Dir = absPath

	// Use git log to get list of changed files instead (more reliable)
	cmd = exec.Command("git", "log",
		"--since="+sinceStr,
		"--pretty=format:",
		"--name-only",
	)
	cmd.Dir = absPath
	fileOut, err := cmd.Output()
	if err != nil {
		log.Printf("[upcoming-svc] error getting changed files (non-fatal): %v", err)
	}

	fileTypes := parseFileTypes(string(fileOut))

	// Compute totals
	pc := &models.ProjectChanges{
		Available:    true,
		TotalCommits: len(commits),
		Commits:      commits,
		FileTypes:    fileTypes,
	}

	// Count unique files
	uniqueFiles := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(fileOut))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			uniqueFiles[line] = true
		}
	}
	pc.FilesChanged = len(uniqueFiles)

	for _, c := range commits {
		pc.TotalInsertions += c.Insertions
		pc.TotalDeletions += c.Deletions
	}

	// Categorize commits
	pc.Changes = categorizeCommits(commits)

	return pc, nil
}

// parseGitLog parses the output of git log with --pretty and --shortstat
func parseGitLog(output string) ([]models.GitCommit, error) {
	var commits []models.GitCommit
	lines := strings.Split(output, "\n")

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Try to parse as a commit line (hash|shorthash|author|date|subject)
		parts := strings.SplitN(line, "|", 5)
		if len(parts) == 5 && len(parts[0]) == 40 {
			date, err := time.Parse(time.RFC3339, parts[3])
			if err != nil {
				date = time.Now()
			}
			commit := models.GitCommit{
				Hash:      parts[0],
				ShortHash: parts[1],
				Author:    parts[2],
				Date:      date,
				Subject:   parts[4],
			}

			// Check if next non-empty line is a stat line
			for j := i + 1; j < len(lines); j++ {
				statLine := strings.TrimSpace(lines[j])
				if statLine == "" {
					continue
				}
				// Parse shortstat line like " 3 files changed, 15 insertions(+), 2 deletions(-)"
				if strings.Contains(statLine, "changed") {
					parseShortStat(statLine, &commit)
					i = j
				}
				break
			}

			commits = append(commits, commit)
		}
	}

	return commits, nil
}

// parseShortStat parses a git shortstat line
func parseShortStat(line string, commit *models.GitCommit) {
	parts := strings.Split(line, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) >= 2 {
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			if strings.Contains(fields[1], "file") {
				commit.FilesChanged = n
			} else if strings.Contains(fields[1], "insertion") {
				commit.Insertions = n
			} else if strings.Contains(fields[1], "deletion") {
				commit.Deletions = n
			}
		}
	}
}

// parseFileTypes counts changed files by extension
func parseFileTypes(output string) []models.FileTypeCount {
	counts := map[string]int{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ext := filepath.Ext(line)
		if ext == "" {
			ext = filepath.Base(line)
		}
		counts[ext]++
	}

	var result []models.FileTypeCount
	for ext, count := range counts {
		result = append(result, models.FileTypeCount{Extension: ext, Count: count})
	}

	// Sort by count descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	return result
}

// categorizeCommits analyzes commit messages to classify changes
func categorizeCommits(commits []models.GitCommit) models.ChangeSummary {
	var cs models.ChangeSummary
	for _, c := range commits {
		subject := strings.ToLower(c.Subject)
		switch {
		case strings.HasPrefix(subject, "fix") ||
			strings.Contains(subject, "bug") ||
			strings.Contains(subject, "patch") ||
			strings.Contains(subject, "hotfix"):
			cs.BugFixes = append(cs.BugFixes, c.Subject)
		case strings.HasPrefix(subject, "feat") ||
			strings.Contains(subject, "add ") ||
			strings.Contains(subject, "new ") ||
			strings.Contains(subject, "implement") ||
			strings.Contains(subject, "enhance"):
			cs.Features = append(cs.Features, c.Subject)
		case strings.Contains(subject, "config") ||
			strings.Contains(subject, "refactor") ||
			strings.Contains(subject, "migrate") ||
			strings.Contains(subject, "rename") ||
			strings.Contains(subject, "restructure") ||
			strings.Contains(subject, "architect"):
			cs.ConfigChanges = append(cs.ConfigChanges, c.Subject)
		default:
			// Default to features for general commits
			cs.Features = append(cs.Features, c.Subject)
		}
	}
	return cs
}
