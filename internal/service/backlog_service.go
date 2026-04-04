package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

type BacklogService struct {
	backlogRepo   *repository.BacklogRepo
	taskRepo      *repository.TaskRepo
	projectRepo   *repository.ProjectRepo
	llmConfigRepo *repository.LLMConfigRepo
	execRepo      *repository.ExecutionRepo
	llmSvc        *LLMService
}

func NewBacklogService(
	backlogRepo *repository.BacklogRepo,
	taskRepo *repository.TaskRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	execRepo *repository.ExecutionRepo,
) *BacklogService {
	return &BacklogService{
		backlogRepo:   backlogRepo,
		taskRepo:      taskRepo,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
		execRepo:      execRepo,
	}
}

// SetLLMService breaks the circular dependency
func (s *BacklogService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// GetDashboard returns aggregated data for the backlog management dashboard
func (s *BacklogService) GetDashboard(ctx context.Context, projectID string) (*models.BacklogDashboardData, error) {
	pending, err := s.backlogRepo.ListByStatus(ctx, projectID, models.SuggestionPending, 20)
	if err != nil {
		return nil, fmt.Errorf("list pending suggestions: %w", err)
	}

	recent, err := s.backlogRepo.ListByProject(ctx, projectID, 50)
	if err != nil {
		return nil, fmt.Errorf("list recent suggestions: %w", err)
	}

	health, err := s.backlogRepo.GetLatestHealth(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get latest health: %w", err)
	}

	report, err := s.backlogRepo.GetLatestReport(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get latest report: %w", err)
	}

	stats, err := s.computeStats(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("compute stats: %w", err)
	}

	return &models.BacklogDashboardData{
		PendingSuggestions: pending,
		RecentSuggestions:  recent,
		LatestHealth:       health,
		LatestReport:       report,
		Stats:              stats,
	}, nil
}

func (s *BacklogService) computeStats(ctx context.Context, projectID string) (models.BacklogStats, error) {
	statusCounts, err := s.backlogRepo.CountByStatus(ctx, projectID)
	if err != nil {
		return models.BacklogStats{}, err
	}
	typeCounts, err := s.backlogRepo.CountByType(ctx, projectID)
	if err != nil {
		return models.BacklogStats{}, err
	}
	avgConf, err := s.backlogRepo.AvgConfidence(ctx, projectID)
	if err != nil {
		return models.BacklogStats{}, err
	}
	backlogSize, err := s.backlogRepo.CountBacklogTasks(ctx, projectID)
	if err != nil {
		return models.BacklogStats{}, err
	}
	staleCount, err := s.backlogRepo.CountStaleTasks(ctx, projectID)
	if err != nil {
		return models.BacklogStats{}, err
	}

	total := 0
	for _, v := range statusCounts {
		total += v
	}

	var healthScore float64
	health, _ := s.backlogRepo.GetLatestHealth(ctx, projectID)
	if health != nil {
		healthScore = health.HealthScore
	}

	return models.BacklogStats{
		TotalSuggestions: total,
		PendingCount:     statusCounts["pending"],
		ApprovedCount:    statusCounts["approved"],
		AppliedCount:     statusCounts["applied"],
		RejectedCount:    statusCounts["rejected"],
		AvgConfidence:    avgConf,
		HealthScore:      healthScore,
		BacklogSize:      backlogSize,
		StaleTaskCount:   staleCount,
		QuickWinCount:    typeCounts["quick_win"],
		ObsoleteCount:    typeCounts["obsolete"],
		DecomposeCount:   typeCounts["decompose"],
	}, nil
}

// RunAnalysis performs a full backlog analysis using AI
func (s *BacklogService) RunAnalysis(ctx context.Context, projectID string) (*models.BacklogAnalysisReport, error) {
	if s.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not configured")
	}

	// Gather context
	backlogTasks, err := s.backlogRepo.GetBacklogTasksForAnalysis(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get backlog tasks: %w", err)
	}

	recentCompleted, err := s.backlogRepo.GetRecentCompletedTasks(ctx, projectID, 20)
	if err != nil {
		return nil, fmt.Errorf("get recent completed: %w", err)
	}

	if len(backlogTasks) == 0 {
		return nil, fmt.Errorf("no backlog tasks to analyze")
	}

	// Build AI prompt
	prompt := s.buildAnalysisPrompt(backlogTasks, recentCompleted)

	// Get default agent
	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default model configured")
	}

	// Get project work dir
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("project not found")
	}
	workDir := project.RepoPath

	// Call AI
	response, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, workDir)
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	// Parse and create suggestions
	suggestions, summary := s.parseAnalysisResponse(response, projectID, backlogTasks)

	var suggestionIDs []string
	for i := range suggestions {
		// Dedup check
		if suggestions[i].TaskID != nil {
			exists, _ := s.backlogRepo.ExistingSuggestion(ctx, projectID, *suggestions[i].TaskID, suggestions[i].Type)
			if exists {
				continue
			}
		}
		if err := s.backlogRepo.CreateSuggestion(ctx, &suggestions[i]); err != nil {
			log.Printf("[backlog-svc] failed to create suggestion: %v", err)
			continue
		}
		suggestionIDs = append(suggestionIDs, suggestions[i].ID)
	}

	// Take health snapshot
	if err := s.snapshotHealth(ctx, projectID); err != nil {
		log.Printf("[backlog-svc] failed to snapshot health: %v", err)
	}

	// Create report
	report := &models.BacklogAnalysisReport{
		ProjectID:  projectID,
		ReportDate: time.Now().Format("2006-01-02"),
		Summary:    summary,
	}
	report.SetSuggestionIDs(suggestionIDs)

	statsJSON, _ := json.Marshal(map[string]interface{}{
		"backlog_tasks_analyzed": len(backlogTasks),
		"suggestions_created":   len(suggestionIDs),
		"recent_completed":      len(recentCompleted),
	})
	report.Stats = string(statsJSON)

	if err := s.backlogRepo.CreateReport(ctx, report); err != nil {
		return nil, fmt.Errorf("create report: %w", err)
	}

	return report, nil
}

func (s *BacklogService) buildAnalysisPrompt(backlogTasks, recentCompleted []models.Task) string {
	var sb strings.Builder
	sb.WriteString(`You are an intelligent backlog management assistant. Analyze the following backlog and recent activity to provide management suggestions.

For each backlog task, determine if any of these actions apply:

1. **reprioritize** - Task priority should change based on recent activity or dependencies
2. **obsolete** - Task has been made obsolete by recent completed work
3. **decompose** - Task is too large, stale (>30 days old), or has a very long prompt and should be broken into subtasks
4. **quick_win** - Task is a quick win that could be done when workers are idle (well-defined, small scope)
5. **schedule** - Task should be scheduled at a specific time based on complexity/dependencies
6. **stale** - Task is very old and needs attention/review

Respond with a JSON object:
{
  "suggestions": [
    {
      "type": "reprioritize|obsolete|decompose|quick_win|schedule|stale",
      "task_id": "the task ID",
      "title": "Short descriptive title",
      "description": "Why this suggestion is being made",
      "suggested_priority": 1-4 (only for reprioritize),
      "suggested_subtasks": ["subtask 1", "subtask 2"] (only for decompose),
      "reasoning": "Detailed reasoning",
      "confidence": 0.0-1.0
    }
  ],
  "summary": "A brief markdown summary of the backlog health and recommendations"
}

## Current Backlog Tasks
`)

	for _, t := range backlogTasks {
		age := time.Since(t.CreatedAt).Hours() / 24
		promptPreview := t.Prompt
		if len(promptPreview) > 200 {
			promptPreview = promptPreview[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("- **%s** (ID: %s, Priority: %d, Age: %.0f days, Tag: %s)\n  Prompt: %s\n\n",
			t.Title, t.ID, t.Priority, age, t.Tag, promptPreview))
	}

	if len(recentCompleted) > 0 {
		sb.WriteString("\n## Recently Completed Tasks (for obsolescence detection)\n")
		for _, t := range recentCompleted {
			sb.WriteString(fmt.Sprintf("- **%s** (completed %s)\n  Prompt: %s\n\n",
				t.Title, t.UpdatedAt.Format("2006-01-02"), util.Truncate(t.Prompt, 150)))
		}
	}

	return sb.String()
}

func (s *BacklogService) parseAnalysisResponse(response, projectID string, backlogTasks []models.Task) ([]models.BacklogSuggestion, string) {
	// Build task ID lookup for validation
	taskIDs := make(map[string]bool)
	for _, t := range backlogTasks {
		taskIDs[t.ID] = true
	}

	// Parse JSON from AI response
	jsonStr := util.ExtractJSONObject(response)
	if jsonStr == "" {
		log.Printf("[backlog-svc] no JSON found in AI response")
		return nil, "Analysis completed but no structured suggestions were generated."
	}

	var parsed struct {
		Suggestions []struct {
			Type              string   `json:"type"`
			TaskID            string   `json:"task_id"`
			Title             string   `json:"title"`
			Description       string   `json:"description"`
			SuggestedPriority *int     `json:"suggested_priority"`
			SuggestedSubtasks []string `json:"suggested_subtasks"`
			Reasoning         string   `json:"reasoning"`
			Confidence        float64  `json:"confidence"`
		} `json:"suggestions"`
		Summary string `json:"summary"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		log.Printf("[backlog-svc] failed to parse AI response JSON: %v", err)
		return nil, "Analysis completed but response could not be parsed."
	}

	var suggestions []models.BacklogSuggestion
	for _, s := range parsed.Suggestions {
		// Validate task ID if provided
		var taskID *string
		if s.TaskID != "" && taskIDs[s.TaskID] {
			taskID = &s.TaskID
		}

		// Clamp confidence
		conf := s.Confidence
		if conf < 0 {
			conf = 0
		}
		if conf > 1 {
			conf = 1
		}

		suggestion := models.BacklogSuggestion{
			ProjectID:         projectID,
			Type:              models.BacklogSuggestionType(s.Type),
			Status:            models.SuggestionPending,
			Title:             s.Title,
			Description:       s.Description,
			TaskID:            taskID,
			SuggestedPriority: s.SuggestedPriority,
			Reasoning:         s.Reasoning,
			Confidence:        conf,
		}

		if len(s.SuggestedSubtasks) > 0 {
			suggestion.SetSubtasks(s.SuggestedSubtasks)
		} else {
			suggestion.SuggestedSubtasks = "[]"
		}

		suggestions = append(suggestions, suggestion)
	}

	summary := parsed.Summary
	if summary == "" {
		summary = fmt.Sprintf("Analysis complete. Generated %d suggestions.", len(suggestions))
	}

	return suggestions, summary
}

// SnapshotHealth captures current backlog health metrics
func (s *BacklogService) snapshotHealth(ctx context.Context, projectID string) error {
	totalTasks, err := s.backlogRepo.CountBacklogTasks(ctx, projectID)
	if err != nil {
		return err
	}
	avgAge, err := s.backlogRepo.AvgBacklogAgeDays(ctx, projectID)
	if err != nil {
		return err
	}
	staleCount, err := s.backlogRepo.CountStaleTasks(ctx, projectID)
	if err != nil {
		return err
	}
	highPriority, err := s.backlogRepo.CountHighPriorityBacklog(ctx, projectID)
	if err != nil {
		return err
	}
	velocity, err := s.backlogRepo.CompletionVelocity(ctx, projectID)
	if err != nil {
		return err
	}

	// Compute health score (0-100)
	healthScore := computeHealthScore(totalTasks, avgAge, staleCount, highPriority, velocity)

	snapshot := &models.BacklogHealthSnapshot{
		ProjectID:          projectID,
		TotalTasks:         totalTasks,
		AvgAgeDays:         avgAge,
		StaleCount:         staleCount,
		HighPriorityCount:  highPriority,
		CompletionVelocity: velocity,
		BottleneckTags:     "[]",
		HealthScore:        healthScore,
		Details:            "{}",
	}

	return s.backlogRepo.CreateHealthSnapshot(ctx, snapshot)
}

func computeHealthScore(totalTasks int, avgAge float64, staleCount, highPriority int, velocity float64) float64 {
	score := 100.0

	// Penalize for backlog size (more than 50 tasks is concerning)
	if totalTasks > 50 {
		score -= float64(totalTasks-50) * 0.5
	}

	// Penalize for high average age
	if avgAge > 14 {
		score -= (avgAge - 14) * 1.0
	}

	// Penalize for stale tasks
	if staleCount > 0 {
		staleRatio := float64(staleCount) / float64(max(totalTasks, 1))
		score -= staleRatio * 30
	}

	// Penalize for unaddressed high priority tasks
	if highPriority > 5 {
		score -= float64(highPriority-5) * 3
	}

	// Reward for good velocity
	if velocity > 0 {
		score += velocity * 2
	}

	// Clamp to 0-100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	return score
}

// ApplySuggestion applies an approved suggestion
func (s *BacklogService) ApplySuggestion(ctx context.Context, id string) error {
	suggestion, err := s.backlogRepo.GetSuggestion(ctx, id)
	if err != nil || suggestion == nil {
		return fmt.Errorf("suggestion not found")
	}

	if suggestion.Status != models.SuggestionApproved && suggestion.Status != models.SuggestionPending {
		return fmt.Errorf("suggestion must be pending or approved before applying")
	}

	// Auto-approve if still pending
	if suggestion.Status == models.SuggestionPending {
		if err := s.backlogRepo.UpdateSuggestionStatus(ctx, id, models.SuggestionApproved); err != nil {
			return fmt.Errorf("auto-approve: %w", err)
		}
	}

	switch suggestion.Type {
	case models.SuggestionReprioritize:
		if suggestion.TaskID != nil && suggestion.SuggestedPriority != nil {
			task, err := s.taskRepo.GetByID(ctx, *suggestion.TaskID)
			if err != nil || task == nil {
				return fmt.Errorf("task not found for priority update")
			}
			task.Priority = *suggestion.SuggestedPriority
			if err := s.taskRepo.Update(ctx, task); err != nil {
				return fmt.Errorf("update priority: %w", err)
			}
		}
	case models.SuggestionObsolete:
		if suggestion.TaskID != nil {
			if err := s.taskRepo.UpdateCategory(ctx, *suggestion.TaskID, models.CategoryCompleted); err != nil {
				return fmt.Errorf("archive task: %w", err)
			}
		}
	case models.SuggestionDecompose:
		if suggestion.TaskID != nil {
			subtasks, _ := suggestion.ParseSubtasks()
			for _, subtaskTitle := range subtasks {
				newTask := &models.Task{
					ProjectID:    suggestion.ProjectID,
					Title:        subtaskTitle,
					Category:     models.CategoryBacklog,
					Priority:     2, // Normal priority
					Status:       models.StatusPending,
					Prompt:       subtaskTitle,
					ParentTaskID: suggestion.TaskID,
					ChainConfig:  "{}",
				}
				if err := s.taskRepo.Create(ctx, newTask); err != nil {
					log.Printf("[backlog-svc] failed to create subtask: %v", err)
				}
			}
		}
	case models.SuggestionQuickWin:
		if suggestion.TaskID != nil {
			// Move to active so it gets picked up by workers
			if err := s.taskRepo.UpdateCategory(ctx, *suggestion.TaskID, models.CategoryActive); err != nil {
				return fmt.Errorf("activate task: %w", err)
			}
		}
	case models.SuggestionSchedule:
		// Create a new task from the suggestion
		newTask := &models.Task{
			ProjectID:   suggestion.ProjectID,
			Title:       suggestion.Title,
			Category:    models.CategoryBacklog,
			Priority:    2, // Normal priority
			Status:      models.StatusPending,
			Prompt:      suggestion.Description,
			ChainConfig: "{}",
		}
		if err := s.taskRepo.Create(ctx, newTask); err != nil {
			return fmt.Errorf("create task from schedule suggestion: %w", err)
		}
	case models.SuggestionStale:
		if suggestion.TaskID != nil {
			// Mark stale task as completed
			if err := s.taskRepo.UpdateCategory(ctx, *suggestion.TaskID, models.CategoryCompleted); err != nil {
				return fmt.Errorf("archive stale task: %w", err)
			}
		}
	}

	return s.backlogRepo.UpdateSuggestionStatus(ctx, id, models.SuggestionApplied)
}

// UpdateSuggestionStatus updates the status of a suggestion
func (s *BacklogService) UpdateSuggestionStatus(ctx context.Context, id string, status models.BacklogSuggestionStatus) error {
	return s.backlogRepo.UpdateSuggestionStatus(ctx, id, status)
}

// DeleteSuggestion removes a suggestion
func (s *BacklogService) DeleteSuggestion(ctx context.Context, id string) error {
	return s.backlogRepo.DeleteSuggestion(ctx, id)
}

// ListSuggestionsByType returns suggestions filtered by type
func (s *BacklogService) ListSuggestionsByType(ctx context.Context, projectID string, suggestionType models.BacklogSuggestionType) ([]models.BacklogSuggestion, error) {
	return s.backlogRepo.ListByType(ctx, projectID, suggestionType, 50)
}

// ListReports returns analysis reports
func (s *BacklogService) ListReports(ctx context.Context, projectID string) ([]models.BacklogAnalysisReport, error) {
	return s.backlogRepo.ListReports(ctx, projectID, 20)
}

// SnapshotHealth captures a health snapshot (public wrapper)
func (s *BacklogService) SnapshotHealth(ctx context.Context, projectID string) error {
	return s.snapshotHealth(ctx, projectID)
}

// ListHealthHistory returns health snapshot history
func (s *BacklogService) ListHealthHistory(ctx context.Context, projectID string) ([]models.BacklogHealthSnapshot, error) {
	return s.backlogRepo.ListHealthSnapshots(ctx, projectID, 30)
}

