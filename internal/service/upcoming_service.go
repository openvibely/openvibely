package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type UpcomingService struct {
	upcomingRepo       *repository.UpcomingRepo
	projectRepo        *repository.ProjectRepo
	taskCommitStatRepo *repository.TaskCommitStatRepo
	llmSvc             *LLMService
	llmConfigRepo      *repository.LLMConfigRepo
}

func NewUpcomingService(upcomingRepo *repository.UpcomingRepo) *UpcomingService {
	return &UpcomingService{upcomingRepo: upcomingRepo}
}

// SetProjectRepo sets the project repository for git change summaries
func (s *UpcomingService) SetProjectRepo(projectRepo *repository.ProjectRepo) {
	s.projectRepo = projectRepo
}

// SetTaskCommitStatRepo sets the task commit stat repository for Reflection git-work metrics.
func (s *UpcomingService) SetTaskCommitStatRepo(taskCommitStatRepo *repository.TaskCommitStatRepo) {
	s.taskCommitStatRepo = taskCommitStatRepo
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
		applog.Infof("[upcoming-svc] error listing running tasks: %v", err)
		return nil, err
	}

	pending, err := s.upcomingRepo.ListPendingActiveTasks(ctx, projectID)
	if err != nil {
		applog.Infof("[upcoming-svc] error listing pending tasks: %v", err)
		return nil, err
	}

	// Look ahead one week for scheduled tasks
	until := now.Add(7 * 24 * time.Hour)
	scheduled, err := s.upcomingRepo.ListUpcomingScheduledTasks(ctx, projectID, until)
	if err != nil {
		applog.Infof("[upcoming-svc] error listing scheduled tasks: %v", err)
		return nil, err
	}

	// Fetch task summary metrics
	taskSummary, err := s.upcomingRepo.GetTaskSummary(ctx, projectID, now)
	if err != nil {
		applog.Infof("[upcoming-svc] error getting task summary (non-fatal): %v", err)
	}

	upcoming := &models.Upcoming{
		ProjectID:      projectID,
		GeneratedAt:    now,
		RunningTasks:   running,
		PendingTasks:   pending,
		ScheduledTasks: scheduled,
		TaskSummary:    taskSummary,
	}

	applog.Infof("[upcoming-svc] generated upcoming project=%s running=%d pending=%d scheduled=%d",
		projectID, len(running), len(pending), len(scheduled))

	return upcoming, nil
}

// ExecuteViewPulseTool returns a compact, prompt-safe upcoming-work agenda for
// the current project using the same data path as the Pulse page.
func ExecuteViewPulseTool(ctx context.Context, upcomingSvc *UpcomingService, projectID string, input json.RawMessage) (string, error) {
	if upcomingSvc == nil {
		return "", fmt.Errorf("view_pulse: upcoming service is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("view_pulse requires a current project")
	}
	var req struct{}
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	upcoming, err := upcomingSvc.GenerateUpcoming(ctx, projectID)
	if err != nil {
		return "", err
	}
	response := compactPulseActionResponse(upcoming)
	b, err := json.Marshal(response)
	return string(b), err
}

type pulseActionResponse struct {
	OK             bool                   `json:"ok"`
	ProjectID      string                 `json:"project_id"`
	GeneratedAt    time.Time              `json:"generated_at"`
	LookaheadDays  int                    `json:"lookahead_days"`
	RunningTasks   []pulseActionTaskEntry `json:"running_tasks"`
	PendingTasks   []pulseActionTaskEntry `json:"pending_tasks"`
	ScheduledTasks []pulseActionTaskEntry `json:"scheduled_tasks"`
	TaskSummary    pulseActionTaskSummary `json:"task_summary"`
}

type pulseActionTaskEntry struct {
	TaskID         string              `json:"task_id"`
	Title          string              `json:"title"`
	Status         models.TaskStatus   `json:"status"`
	Category       models.TaskCategory `json:"category"`
	Priority       int                 `json:"priority"`
	Tag            models.TaskTag      `json:"tag,omitempty"`
	AgentName      string              `json:"agent_name,omitempty"`
	PromptPreview  string              `json:"prompt_preview,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	ScheduleID     string              `json:"schedule_id,omitempty"`
	NextRun        *time.Time          `json:"next_run,omitempty"`
	RepeatType     models.RepeatType   `json:"repeat_type,omitempty"`
	RepeatInterval int                 `json:"repeat_interval,omitempty"`
	RepeatLabel    string              `json:"repeat_label,omitempty"`
}

type pulseActionTaskSummary struct {
	TotalPending int `json:"total_pending"`
	Priority     struct {
		Urgent int `json:"urgent"`
		High   int `json:"high"`
		Normal int `json:"normal"`
		Low    int `json:"low"`
	} `json:"priority"`
	Status struct {
		Pending   int `json:"pending"`
		Running   int `json:"running"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	} `json:"status"`
	Category struct {
		Active    int `json:"active"`
		Backlog   int `json:"backlog"`
		Scheduled int `json:"scheduled"`
	} `json:"category"`
	Scheduled struct {
		Overdue     int `json:"overdue"`
		DueToday    int `json:"due_today"`
		DueThisWeek int `json:"due_this_week"`
	} `json:"scheduled"`
}

func compactPulseActionResponse(upcoming *models.Upcoming) pulseActionResponse {
	if upcoming == nil {
		return pulseActionResponse{OK: true, LookaheadDays: 7}
	}
	response := pulseActionResponse{
		OK:             true,
		ProjectID:      upcoming.ProjectID,
		GeneratedAt:    upcoming.GeneratedAt,
		LookaheadDays:  7,
		RunningTasks:   compactPulseTaskEntries(upcoming.RunningTasks),
		PendingTasks:   compactPulseTaskEntries(upcoming.PendingTasks),
		ScheduledTasks: compactPulseTaskEntries(upcoming.ScheduledTasks),
	}
	if upcoming.TaskSummary != nil {
		s := upcoming.TaskSummary
		response.TaskSummary.TotalPending = s.TotalPending
		response.TaskSummary.Priority.Urgent = s.UrgentCount
		response.TaskSummary.Priority.High = s.HighCount
		response.TaskSummary.Priority.Normal = s.NormalCount
		response.TaskSummary.Priority.Low = s.LowCount
		response.TaskSummary.Status.Pending = s.PendingCount
		response.TaskSummary.Status.Running = s.RunningCount
		response.TaskSummary.Status.Completed = s.CompletedCount
		response.TaskSummary.Status.Failed = s.FailedCount
		response.TaskSummary.Category.Active = s.ActiveCount
		response.TaskSummary.Category.Backlog = s.BacklogCount
		response.TaskSummary.Category.Scheduled = s.ScheduledCount
		response.TaskSummary.Scheduled.Overdue = s.OverdueCount
		response.TaskSummary.Scheduled.DueToday = s.ScheduledToday
		response.TaskSummary.Scheduled.DueThisWeek = s.ScheduledThisWeek
	}
	return response
}

func compactPulseTaskEntries(tasks []models.UpcomingTask) []pulseActionTaskEntry {
	entries := make([]pulseActionTaskEntry, 0, len(tasks))
	for _, upcomingTask := range tasks {
		task := upcomingTask.Task
		entry := pulseActionTaskEntry{
			TaskID:        task.ID,
			Title:         task.Title,
			Status:        task.Status,
			Category:      task.Category,
			Priority:      task.Priority,
			Tag:           task.Tag,
			AgentName:     upcomingTask.AgentName,
			PromptPreview: task.Prompt,
			CreatedAt:     task.CreatedAt,
			UpdatedAt:     task.UpdatedAt,
			NextRun:       upcomingTask.NextRun,
		}
		if upcomingTask.Schedule != nil {
			entry.ScheduleID = upcomingTask.Schedule.ID
			entry.RepeatType = upcomingTask.Schedule.RepeatType
			entry.RepeatInterval = upcomingTask.Schedule.RepeatInterval
			entry.RepeatLabel = pulseRepeatLabel(upcomingTask.Schedule.RepeatType, upcomingTask.Schedule.RepeatInterval)
		}
		entries = append(entries, entry)
	}
	return entries
}

func pulseRepeatLabel(repeatType models.RepeatType, interval int) string {
	switch repeatType {
	case models.RepeatOnce:
		return "once"
	case models.RepeatDaily:
		if interval == 1 {
			return "daily"
		}
	case models.RepeatWeekly:
		if interval == 1 {
			return "weekly"
		}
	case models.RepeatMonthly:
		if interval == 1 {
			return "monthly"
		}
	case models.RepeatHours:
		if interval == 1 {
			return "hourly"
		}
	case models.RepeatMinutes:
		if interval == 1 {
			return "every minute"
		}
	case models.RepeatSeconds:
		if interval == 1 {
			return "every second"
		}
	}
	if interval <= 0 {
		return string(repeatType)
	}
	return fmt.Sprintf("every %d %s", interval, repeatType)
}

// GenerateHistory creates a summary of recently completed work for a project
func (s *UpcomingService) GenerateHistory(ctx context.Context, projectID string, timeRange models.TimeRange) (*models.History, error) {
	now := time.Now().UTC()
	since := computeSince(now, timeRange)

	summary, err := s.upcomingRepo.GetHistorySummary(ctx, projectID, since)
	if err != nil {
		applog.Infof("[upcoming-svc] error getting history summary: %v", err)
		return nil, err
	}

	executions, err := s.upcomingRepo.ListRecentExecutions(ctx, projectID, since)
	if err != nil {
		applog.Infof("[upcoming-svc] error listing recent executions: %v", err)
		return nil, err
	}

	var projectChanges *models.ProjectChanges
	var projectChangeFiles []string
	var firstProjectStatProducedAt time.Time
	if s.taskCommitStatRepo != nil {
		firstProducedAt, err := s.taskCommitStatRepo.FirstProducedCommitStatTime(ctx, projectID)
		if err != nil {
			applog.Infof("[upcoming-svc] error getting first task commit stat time (non-fatal): %v", err)
		} else {
			firstProjectStatProducedAt = firstProducedAt
		}

		changes, changedFiles, err := s.buildProjectChangesFromTaskCommitStats(ctx, projectID, since)
		if err != nil {
			applog.Infof("[upcoming-svc] error summarizing task commit stats (non-fatal): %v", err)
		} else if changes != nil {
			projectChanges, projectChangeFiles = changes, changedFiles
		}
	}

	// Fall back to repository git history only for old data that predates the
	// project's first recorded task_commit_stats row. Once a project has stats
	// coverage before the selected range, quiet gaps must not count raw git history.
	fallbackBefore := time.Time{}
	shouldUseGitFallback := projectChanges == nil && firstProjectStatProducedAt.IsZero()
	if !firstProjectStatProducedAt.IsZero() && firstProjectStatProducedAt.After(since) {
		shouldUseGitFallback = true
		fallbackBefore = firstProjectStatProducedAt
	}
	if s.projectRepo != nil && shouldUseGitFallback {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			applog.Infof("[upcoming-svc] error getting project for git changes (non-fatal): %v", err)
		} else if project.RepoPath != "" {
			changes, changedFiles, err := s.getProjectChangesInRangeWithFiles(project.RepoPath, since, fallbackBefore)
			if err != nil {
				applog.Infof("[upcoming-svc] error getting git changes (non-fatal): %v", err)
			} else if projectChanges == nil {
				projectChanges = changes
				projectChangeFiles = changedFiles
			} else {
				projectChanges = mergeProjectChangesWithFiles(changes, changedFiles, projectChanges, projectChangeFiles)
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

	applog.Infof("[upcoming-svc] generated history project=%s range=%s executions=%d success=%d failed=%d",
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

const (
	projectChangeCommitPreviewLimit   = 10
	projectChangeCategoryPreviewLimit = 5
)

func (s *UpcomingService) buildProjectChangesFromTaskCommitStats(ctx context.Context, projectID string, since time.Time) (*models.ProjectChanges, []string, error) {
	aggregate, err := s.taskCommitStatRepo.ProducedCommitStatAggregate(ctx, projectID, since)
	if err != nil {
		return nil, nil, err
	}
	if aggregate.TotalCommits == 0 {
		return nil, nil, nil
	}

	commits, err := s.taskCommitStatRepo.ListProducedCommitStatCommits(ctx, projectID, since, projectChangeCommitPreviewLimit)
	if err != nil {
		return nil, nil, err
	}
	changes, err := s.summarizeProducedCommitStatCategories(ctx, projectID, since)
	if err != nil {
		return nil, nil, err
	}
	files, err := s.uniqueProducedCommitStatFiles(ctx, projectID, since)
	if err != nil {
		return nil, nil, err
	}

	pc := &models.ProjectChanges{
		Available:       true,
		TotalCommits:    aggregate.TotalCommits,
		TotalInsertions: aggregate.TotalInsertions,
		TotalDeletions:  aggregate.TotalDeletions,
		FilesChanged:    len(files),
		Commits:         commits,
		FileTypes:       fileTypeCountsFromFiles(files),
		Changes:         changes,
	}
	return pc, files, nil
}

func (s *UpcomingService) summarizeProducedCommitStatCategories(ctx context.Context, projectID string, since time.Time) (models.ChangeSummary, error) {
	var summary models.ChangeSummary
	err := s.taskCommitStatRepo.ForEachProducedCommitStatSubject(ctx, projectID, since, func(subject string) error {
		addSubjectToChangeSummary(&summary, subject, projectChangeCategoryPreviewLimit)
		return nil
	})
	return summary, err
}

func (s *UpcomingService) uniqueProducedCommitStatFiles(ctx context.Context, projectID string, since time.Time) ([]string, error) {
	uniqueFiles := map[string]bool{}
	err := s.taskCommitStatRepo.ForEachProducedCommitStatChangedFilesJSON(ctx, projectID, since, func(changedFilesJSON string) error {
		forEachChangedFileInJSON(changedFilesJSON, func(file string) {
			if file == "" || uniqueFiles[file] {
				return
			}
			uniqueFiles[strings.Clone(file)] = true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func buildProjectChangesFromTaskCommitStats(stats []models.TaskCommitStat) *models.ProjectChanges {
	pc, _ := buildProjectChangesFromTaskCommitStatsWithFiles(stats)
	return pc
}

func buildProjectChangesFromTaskCommitStatsWithFiles(stats []models.TaskCommitStat) (*models.ProjectChanges, []string) {
	commits := make([]models.GitCommit, 0, len(stats))
	uniqueFiles := map[string]bool{}
	for _, stat := range stats {
		commits = append(commits, models.GitCommit{
			Hash:         stat.CommitSHA,
			ShortHash:    stat.ShortSHA,
			Author:       stat.Author,
			Date:         stat.ProducedAt,
			Subject:      stat.Subject,
			FilesChanged: stat.FilesChanged,
			Insertions:   stat.Insertions,
			Deletions:    stat.Deletions,
		})
		for _, file := range changedFilesFromStat(stat) {
			if file != "" {
				uniqueFiles[file] = true
			}
		}
	}

	pc := &models.ProjectChanges{
		Available:    true,
		TotalCommits: len(commits),
		Commits:      commits,
		Changes:      categorizeCommits(commits),
	}
	for _, stat := range stats {
		pc.TotalInsertions += stat.Insertions
		pc.TotalDeletions += stat.Deletions
	}
	pc.FilesChanged = len(uniqueFiles)

	files := make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		files = append(files, file)
	}
	sort.Strings(files)
	pc.FileTypes = fileTypeCountsFromFiles(files)

	return pc, files
}

func mergeProjectChangesWithFiles(first *models.ProjectChanges, firstFiles []string, second *models.ProjectChanges, secondFiles []string) *models.ProjectChanges {
	merged := &models.ProjectChanges{Available: true}
	for _, part := range []*models.ProjectChanges{first, second} {
		if part == nil || !part.Available {
			continue
		}
		merged.TotalCommits += part.TotalCommits
		merged.TotalInsertions += part.TotalInsertions
		merged.TotalDeletions += part.TotalDeletions
		merged.Commits = append(merged.Commits, part.Commits...)
	}
	sort.Slice(merged.Commits, func(i, j int) bool {
		return merged.Commits[i].Date.After(merged.Commits[j].Date)
	})
	if len(merged.Commits) > projectChangeCommitPreviewLimit {
		merged.Commits = merged.Commits[:projectChangeCommitPreviewLimit]
	}
	uniqueFiles := map[string]bool{}
	for _, file := range append(firstFiles, secondFiles...) {
		if file != "" {
			uniqueFiles[file] = true
		}
	}
	files := make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		files = append(files, file)
	}
	sort.Strings(files)
	merged.FilesChanged = len(files)
	merged.FileTypes = fileTypeCountsFromFiles(files)
	var summaries []models.ChangeSummary
	if first != nil && second != nil && latestCommitDate(second.Commits).After(latestCommitDate(first.Commits)) {
		summaries = append(summaries, second.Changes, first.Changes)
	} else {
		if first != nil {
			summaries = append(summaries, first.Changes)
		}
		if second != nil {
			summaries = append(summaries, second.Changes)
		}
	}
	merged.Changes = mergeChangeSummaries(summaries...)
	return merged
}

func latestCommitDate(commits []models.GitCommit) time.Time {
	var latest time.Time
	for _, commit := range commits {
		if commit.Date.After(latest) {
			latest = commit.Date
		}
	}
	return latest
}

func changedFilesFromStat(stat models.TaskCommitStat) []string {
	return changedFilesFromJSON(stat.ChangedFilesJSON)
}

func changedFilesFromJSON(changedFilesJSON string) []string {
	var files []string
	if err := json.Unmarshal([]byte(changedFilesJSON), &files); err != nil {
		return nil
	}
	return files
}

func forEachChangedFileInJSON(changedFilesJSON string, fn func(file string)) {
	s := strings.TrimSpace(changedFilesJSON)
	if !scanChangedFileJSONArray(s, nil) {
		return
	}
	scanChangedFileJSONArray(s, fn)
}

func scanChangedFileJSONArray(s string, fn func(file string)) bool {
	if len(s) < 2 || s[0] != '[' {
		return false
	}
	i := skipJSONWhitespace(s, 1)
	if i < len(s) && s[i] == ']' {
		i = skipJSONWhitespace(s, i+1)
		return i == len(s)
	}
	for {
		if i >= len(s) || s[i] != '"' {
			return false
		}
		value, next, ok := parseJSONStringToken(s, i)
		if !ok {
			return false
		}
		if fn != nil {
			fn(value)
		}
		i = skipJSONWhitespace(s, next)
		if i >= len(s) {
			return false
		}
		switch s[i] {
		case ',':
			i = skipJSONWhitespace(s, i+1)
			if i < len(s) && s[i] == ']' {
				return false
			}
		case ']':
			i = skipJSONWhitespace(s, i+1)
			return i == len(s)
		default:
			return false
		}
	}
}

func parseJSONStringToken(s string, start int) (string, int, bool) {
	valueStart := start + 1
	escaped := false
	for i := valueStart; i < len(s); i++ {
		switch s[i] {
		case '"':
			if escaped {
				var value string
				if err := json.Unmarshal([]byte(s[start:i+1]), &value); err != nil {
					return "", 0, false
				}
				return value, i + 1, true
			}
			return s[valueStart:i], i + 1, true
		case '\\':
			escaped = true
			i++
			if i >= len(s) {
				return "", 0, false
			}
		default:
			if s[i] < 0x20 {
				return "", 0, false
			}
		}
	}
	return "", 0, false
}

func skipJSONWhitespace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\n' || s[i] == '\r' || s[i] == '\t') {
		i++
	}
	return i
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
		if featureCount := changeSummaryFeatureCount(pc.Changes); featureCount > 0 {
			sb.WriteString(fmt.Sprintf("  Features: %d\n", featureCount))
		}
		if bugFixCount := changeSummaryBugFixCount(pc.Changes); bugFixCount > 0 {
			sb.WriteString(fmt.Sprintf("  Bug fixes: %d\n", bugFixCount))
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

func formatGitSince(since time.Time) string {
	return since.UTC().Format(time.RFC3339)
}

// getProjectChanges runs git commands against a project's repo to gather change data
func (s *UpcomingService) getProjectChanges(repoPath string, since time.Time) (*models.ProjectChanges, error) {
	pc, _, err := s.getProjectChangesInRangeWithFiles(repoPath, since, time.Time{})
	return pc, err
}

func (s *UpcomingService) getProjectChangesInRangeWithFiles(repoPath string, since, before time.Time) (*models.ProjectChanges, []string, error) {
	// Verify this is a git repo
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving repo path: %w", err)
	}

	sinceStr := formatGitSince(since)
	logArgs := []string{"log", "--since=" + sinceStr}
	if !before.IsZero() {
		logArgs = append(logArgs, "--before="+formatGitSince(before))
	}

	// Get commit log with stats
	cmd := exec.Command("git", append(logArgs,
		"--pretty=format:%H|%h|%an|%aI|%s",
		"--shortstat",
	)...)
	cmd.Dir = absPath
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("running git log: %w", err)
	}

	commits, err := parseGitLog(string(out))
	if err != nil {
		return nil, nil, fmt.Errorf("parsing git log: %w", err)
	}

	// Use git log to get list of changed files.
	cmd = exec.Command("git", append(logArgs,
		"--pretty=format:",
		"--name-only",
	)...)
	cmd.Dir = absPath
	fileOut, err := cmd.Output()
	if err != nil {
		applog.Infof("[upcoming-svc] error getting changed files (non-fatal): %v", err)
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
	files := make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		files = append(files, file)
	}
	sort.Strings(files)

	// Compute totals
	pc := &models.ProjectChanges{
		Available:    true,
		TotalCommits: len(commits),
		Commits:      commits,
		FileTypes:    parseFileTypes(strings.Join(files, "\n")),
		FilesChanged: len(files),
	}

	for _, c := range commits {
		pc.TotalInsertions += c.Insertions
		pc.TotalDeletions += c.Deletions
	}

	// Categorize commits
	pc.Changes = categorizeCommits(commits)

	return pc, files, nil
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

// parseFileTypes counts changed files by extension.
func parseFileTypes(output string) []models.FileTypeCount {
	var files []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			files = append(files, line)
		}
	}
	return fileTypeCountsFromFiles(files)
}

func fileTypeCountsFromFiles(files []string) []models.FileTypeCount {
	counts := map[string]int{}
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		ext := filepath.Ext(file)
		if ext == "" {
			ext = filepath.Base(file)
		}
		counts[ext]++
	}

	result := make([]models.FileTypeCount, 0, len(counts))
	for ext, count := range counts {
		result = append(result, models.FileTypeCount{Extension: ext, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Extension < result[j].Extension
		}
		return result[i].Count > result[j].Count
	})
	return result
}

// categorizeCommits analyzes commit messages to classify changes.
func categorizeCommits(commits []models.GitCommit) models.ChangeSummary {
	var cs models.ChangeSummary
	for _, c := range commits {
		addSubjectToChangeSummary(&cs, c.Subject, 0)
	}
	return cs
}

func addSubjectToChangeSummary(cs *models.ChangeSummary, subject string, exampleLimit int) {
	switch changeCategory(subject) {
	case "bugfix":
		cs.BugFixCount++
		if exampleLimit <= 0 || len(cs.BugFixes) < exampleLimit {
			cs.BugFixes = append(cs.BugFixes, subject)
		}
	case "config":
		cs.ConfigChangeCount++
		if exampleLimit <= 0 || len(cs.ConfigChanges) < exampleLimit {
			cs.ConfigChanges = append(cs.ConfigChanges, subject)
		}
	default:
		cs.FeatureCount++
		if exampleLimit <= 0 || len(cs.Features) < exampleLimit {
			cs.Features = append(cs.Features, subject)
		}
	}
}

func changeCategory(subject string) string {
	normalized := strings.ToLower(subject)
	switch {
	case strings.HasPrefix(normalized, "fix") ||
		strings.Contains(normalized, "bug") ||
		strings.Contains(normalized, "patch") ||
		strings.Contains(normalized, "hotfix"):
		return "bugfix"
	case strings.HasPrefix(normalized, "feat") ||
		strings.Contains(normalized, "add ") ||
		strings.Contains(normalized, "new ") ||
		strings.Contains(normalized, "implement") ||
		strings.Contains(normalized, "enhance"):
		return "feature"
	case strings.Contains(normalized, "config") ||
		strings.Contains(normalized, "refactor") ||
		strings.Contains(normalized, "migrate") ||
		strings.Contains(normalized, "rename") ||
		strings.Contains(normalized, "restructure") ||
		strings.Contains(normalized, "architect"):
		return "config"
	default:
		return "feature"
	}
}

func mergeChangeSummaries(summaries ...models.ChangeSummary) models.ChangeSummary {
	var merged models.ChangeSummary
	for _, summary := range summaries {
		merged.FeatureCount += changeSummaryFeatureCount(summary)
		merged.BugFixCount += changeSummaryBugFixCount(summary)
		merged.ConfigChangeCount += changeSummaryConfigChangeCount(summary)
		merged.Features = appendLimitedStrings(merged.Features, summary.Features, projectChangeCategoryPreviewLimit)
		merged.BugFixes = appendLimitedStrings(merged.BugFixes, summary.BugFixes, projectChangeCategoryPreviewLimit)
		merged.ConfigChanges = appendLimitedStrings(merged.ConfigChanges, summary.ConfigChanges, projectChangeCategoryPreviewLimit)
	}
	return merged
}

func appendLimitedStrings(dst, src []string, limit int) []string {
	for _, item := range src {
		if len(dst) >= limit {
			break
		}
		dst = append(dst, item)
	}
	return dst
}

func changeSummaryFeatureCount(summary models.ChangeSummary) int {
	if summary.FeatureCount > 0 || len(summary.Features) == 0 {
		return summary.FeatureCount
	}
	return len(summary.Features)
}

func changeSummaryBugFixCount(summary models.ChangeSummary) int {
	if summary.BugFixCount > 0 || len(summary.BugFixes) == 0 {
		return summary.BugFixCount
	}
	return len(summary.BugFixes)
}

func changeSummaryConfigChangeCount(summary models.ChangeSummary) int {
	if summary.ConfigChangeCount > 0 || len(summary.ConfigChanges) == 0 {
		return summary.ConfigChangeCount
	}
	return len(summary.ConfigChanges)
}
