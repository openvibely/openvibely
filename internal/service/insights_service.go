package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

type InsightsService struct {
	insightsRepo  *repository.InsightsRepo
	taskRepo      *repository.TaskRepo
	projectRepo   *repository.ProjectRepo
	llmConfigRepo *repository.LLMConfigRepo
	execRepo      *repository.ExecutionRepo
	llmSvc        *LLMService
}

func NewInsightsService(
	insightsRepo *repository.InsightsRepo,
	taskRepo *repository.TaskRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	execRepo *repository.ExecutionRepo,
) *InsightsService {
	return &InsightsService{
		insightsRepo:  insightsRepo,
		taskRepo:      taskRepo,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
		execRepo:      execRepo,
	}
}

// SetLLMService breaks the circular dependency
func (s *InsightsService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// GetDashboard returns aggregated insight data for the dashboard
func (s *InsightsService) GetDashboard(ctx context.Context, projectID string) (*models.InsightDashboardData, error) {
	var (
		newInsights      []models.Insight
		recentInsights   []models.Insight
		latestReport     *models.InsightReport
		knowledge        []models.KnowledgeEntry
		stats            models.InsightStats
		latestHC         *models.HealthCheck
		healthHistory    []models.HealthCheck
		latestIG         *models.IdeaGrade
		ideaGradeHistory []models.IdeaGrade
	)

	var g errgroup.Group

	g.Go(func() error {
		var err error
		newInsights, err = s.insightsRepo.ListByStatus(ctx, projectID, models.InsightStatusNew, 20)
		if err != nil {
			return fmt.Errorf("list new insights: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		recentInsights, err = s.insightsRepo.ListByProject(ctx, projectID, 50)
		if err != nil {
			return fmt.Errorf("list recent: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		latestReport, err = s.insightsRepo.GetLatestReport(ctx, projectID)
		if err != nil {
			return fmt.Errorf("get latest report: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		knowledge, err = s.insightsRepo.ListKnowledge(ctx, projectID, 20)
		if err != nil {
			return fmt.Errorf("list knowledge: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		stats, err = s.computeStats(ctx, projectID)
		if err != nil {
			return fmt.Errorf("compute stats: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		latestHC, err = s.insightsRepo.GetLatestHealthCheck(ctx, projectID)
		if err != nil {
			log.Printf("[insights-svc] get latest health check error: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		healthHistory, err = s.insightsRepo.ListHealthChecks(ctx, projectID, 10)
		if err != nil {
			log.Printf("[insights-svc] list health checks error: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		latestIG, err = s.insightsRepo.GetLatestIdeaGrade(ctx, projectID)
		if err != nil {
			log.Printf("[insights-svc] get latest idea grade error: %v", err)
		}
		return nil
	})

	g.Go(func() error {
		var err error
		ideaGradeHistory, err = s.insightsRepo.ListIdeaGrades(ctx, projectID, 10)
		if err != nil {
			log.Printf("[insights-svc] list idea grades error: %v", err)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Only include history if the latest entry exists
	if latestHC == nil {
		healthHistory = nil
	}
	if latestIG == nil {
		ideaGradeHistory = nil
	}

	return &models.InsightDashboardData{
		NewInsights:       newInsights,
		RecentInsights:    recentInsights,
		LatestReport:      latestReport,
		KnowledgeEntries:  knowledge,
		Stats:             stats,
		LatestHealthCheck: latestHC,
		HealthHistory:     healthHistory,
		LatestIdeaGrade:   latestIG,
		IdeaGradeHistory:  ideaGradeHistory,
	}, nil
}

func (s *InsightsService) computeStats(ctx context.Context, projectID string) (models.InsightStats, error) {
	var (
		statusCounts map[string]int
		typeCounts   map[string]int
		avgConf      float64
		entries      []models.KnowledgeEntry
	)

	var g errgroup.Group

	g.Go(func() error {
		var err error
		statusCounts, err = s.insightsRepo.CountByStatus(ctx, projectID)
		return err
	})

	g.Go(func() error {
		var err error
		typeCounts, err = s.insightsRepo.CountByType(ctx, projectID)
		return err
	})

	g.Go(func() error {
		var err error
		avgConf, err = s.insightsRepo.AvgConfidence(ctx, projectID)
		return err
	})

	g.Go(func() error {
		var err error
		entries, err = s.insightsRepo.ListKnowledge(ctx, projectID, 1000)
		return err
	})

	if err := g.Wait(); err != nil {
		return models.InsightStats{}, err
	}

	total := 0
	for _, v := range statusCounts {
		total += v
	}

	return models.InsightStats{
		TotalInsights:  total,
		NewCount:       statusCounts["new"],
		AcceptedCount:  statusCounts["accepted"],
		RejectedCount:  statusCounts["rejected"],
		ResolvedCount:  statusCounts["resolved"],
		AvgConfidence:  avgConf,
		KnowledgeCount: len(entries),
		BugPatterns:    typeCounts["bug_pattern"],
		TechDebtItems:  typeCounts["tech_debt"],
		Optimizations:  typeCounts["optimization"],
	}, nil
}

// RunAnalysis performs a full analysis of the project and generates insights
func (s *InsightsService) RunAnalysis(ctx context.Context, projectID string) (*models.InsightReport, error) {
	log.Printf("[insights-svc] starting analysis for project %s", projectID)

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	var insightIDs []string
	var analysisLog []string

	// 1. Bug pattern detection
	bugInsights, err := s.detectBugPatterns(ctx, project)
	if err != nil {
		log.Printf("[insights-svc] bug pattern detection error: %v", err)
		analysisLog = append(analysisLog, fmt.Sprintf("Bug patterns: error - %v", err))
	} else {
		for _, i := range bugInsights {
			insightIDs = append(insightIDs, i.ID)
		}
		analysisLog = append(analysisLog, fmt.Sprintf("Bug patterns: found %d", len(bugInsights)))
	}

	// 2. Incomplete feature detection
	incompleteInsights, err := s.detectIncompleteFeatures(ctx, project)
	if err != nil {
		log.Printf("[insights-svc] incomplete feature detection error: %v", err)
		analysisLog = append(analysisLog, fmt.Sprintf("Incomplete features: error - %v", err))
	} else {
		for _, i := range incompleteInsights {
			insightIDs = append(insightIDs, i.ID)
		}
		analysisLog = append(analysisLog, fmt.Sprintf("Incomplete features: found %d", len(incompleteInsights)))
	}

	// 3. Tech debt detection
	debtInsights, err := s.detectTechDebt(ctx, project)
	if err != nil {
		log.Printf("[insights-svc] tech debt detection error: %v", err)
		analysisLog = append(analysisLog, fmt.Sprintf("Tech debt: error - %v", err))
	} else {
		for _, i := range debtInsights {
			insightIDs = append(insightIDs, i.ID)
		}
		analysisLog = append(analysisLog, fmt.Sprintf("Tech debt: found %d", len(debtInsights)))
	}

	// 4. Optimization opportunities
	optInsights, err := s.detectOptimizations(ctx, project)
	if err != nil {
		log.Printf("[insights-svc] optimization detection error: %v", err)
		analysisLog = append(analysisLog, fmt.Sprintf("Optimizations: error - %v", err))
	} else {
		for _, i := range optInsights {
			insightIDs = append(insightIDs, i.ID)
		}
		analysisLog = append(analysisLog, fmt.Sprintf("Optimizations: found %d", len(optInsights)))
	}

	// 5. Generate proactive suggestions via AI
	if s.llmSvc != nil {
		suggInsights, err := s.generateProactiveSuggestions(ctx, project, len(bugInsights), len(incompleteInsights), len(debtInsights), len(optInsights))
		if err != nil {
			log.Printf("[insights-svc] proactive suggestions error: %v", err)
			analysisLog = append(analysisLog, fmt.Sprintf("Proactive suggestions: error - %v", err))
		} else {
			for _, i := range suggInsights {
				insightIDs = append(insightIDs, i.ID)
			}
			analysisLog = append(analysisLog, fmt.Sprintf("Proactive suggestions: generated %d", len(suggInsights)))
		}
	}

	// Build report
	report := &models.InsightReport{
		ProjectID:   projectID,
		ReportDate:  time.Now().Format("2006-01-02"),
		AnalysisLog: strings.Join(analysisLog, "\n"),
	}

	// Build summary
	summaryParts := []string{
		fmt.Sprintf("# Proactive Insights Report - %s", time.Now().Format("2006-01-02")),
		"",
		fmt.Sprintf("**Project:** %s", project.Name),
		fmt.Sprintf("**Total new insights:** %d", len(insightIDs)),
		"",
		"## Analysis Summary",
	}
	summaryParts = append(summaryParts, analysisLog...)
	report.Summary = strings.Join(summaryParts, "\n")

	if err := report.SetInsightIDs(insightIDs); err != nil {
		return nil, fmt.Errorf("set insight IDs: %w", err)
	}

	stats := map[string]interface{}{
		"total_insights": len(insightIDs),
		"analysis_date":  time.Now().Format("2006-01-02"),
	}
	statsJSON, _ := json.Marshal(stats)
	report.Stats = string(statsJSON)

	if err := s.insightsRepo.CreateReport(ctx, report); err != nil {
		return nil, fmt.Errorf("create report: %w", err)
	}

	log.Printf("[insights-svc] analysis complete for project %s: %d insights", projectID, len(insightIDs))
	return report, nil
}

// detectBugPatterns finds recurring failure patterns
func (s *InsightsService) detectBugPatterns(ctx context.Context, project *models.Project) ([]models.Insight, error) {
	patterns, err := s.insightsRepo.GetFailedTaskPatterns(ctx, project.ID, 2)
	if err != nil {
		return nil, err
	}

	var insights []models.Insight
	for _, p := range patterns {
		// Check for duplicate
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, fmt.Sprintf("Recurring failures: %s", p.Title), models.InsightBugPattern)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}

		severity := models.InsightSeverityMedium
		if p.FailCount >= 5 {
			severity = models.InsightSeverityHigh
		}
		if p.FailCount >= 10 {
			severity = models.InsightSeverityCritical
		}

		evidence := map[string]interface{}{
			"failure_count": p.FailCount,
			"last_error":    p.LastError,
			"task_title":    p.Title,
		}
		evidenceJSON, _ := json.Marshal(evidence)

		insight := &models.Insight{
			ProjectID:   project.ID,
			Type:        models.InsightBugPattern,
			Severity:    severity,
			Status:      models.InsightStatusNew,
			Title:       fmt.Sprintf("Recurring failures: %s", util.Truncate(p.Title, 80)),
			Description: fmt.Sprintf("Task '%s' has failed %d times. Last error: %s", p.Title, p.FailCount, util.Truncate(p.LastError, 200)),
			Evidence:    string(evidenceJSON),
			Suggestion:  "Investigate root cause of repeated failures. Consider splitting into smaller tasks or adding guardrails.",
			Impact:      fmt.Sprintf("Addressing this could prevent %d+ future failures", p.FailCount),
			Confidence:  0.9,
		}
		if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
			return nil, err
		}
		insights = append(insights, *insight)
	}
	return insights, nil
}

// detectIncompleteFeatures finds partially implemented features
func (s *InsightsService) detectIncompleteFeatures(ctx context.Context, project *models.Project) ([]models.Insight, error) {
	// Find completed bug fix tasks to check if the underlying feature was completed
	bugFixes, err := s.insightsRepo.GetCompletedBugFixes(ctx, project.ID, 50)
	if err != nil {
		return nil, err
	}

	var insights []models.Insight

	// Look for tasks that were completed but might indicate incomplete features
	// (e.g., bug fixes without corresponding feature tasks)
	bugFixCount := len(bugFixes)
	if bugFixCount > 5 {
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, "High bug fix ratio detected", models.InsightIncompleteFeature)
		if err != nil {
			return nil, err
		}
		if !exists {
			evidence := map[string]interface{}{
				"bug_fix_count": bugFixCount,
			}
			evidenceJSON, _ := json.Marshal(evidence)

			insight := &models.Insight{
				ProjectID:   project.ID,
				Type:        models.InsightIncompleteFeature,
				Severity:    models.InsightSeverityMedium,
				Status:      models.InsightStatusNew,
				Title:       "High bug fix ratio detected",
				Description: fmt.Sprintf("Found %d completed bug fix tasks. This may indicate features were shipped without sufficient testing or completeness.", bugFixCount),
				Evidence:    string(evidenceJSON),
				Suggestion:  "Review recently completed features for missing test coverage, edge cases, or TODO items.",
				Impact:      "Reducing bug fix ratio improves overall velocity and reduces rework.",
				Confidence:  0.7,
			}
			if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
				return nil, err
			}
			insights = append(insights, *insight)
		}
	}

	// Check for stale pending tasks (features that were started but never completed)
	staleTasks, err := s.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryActive))
	if err != nil {
		return nil, err
	}

	staleCount := 0
	for _, t := range staleTasks {
		if time.Since(t.CreatedAt) > 72*time.Hour {
			staleCount++
		}
	}

	if staleCount > 3 {
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, "Stale pending tasks detected", models.InsightIncompleteFeature)
		if err != nil {
			return nil, err
		}
		if !exists {
			evidence := map[string]interface{}{
				"stale_count": staleCount,
			}
			evidenceJSON, _ := json.Marshal(evidence)

			insight := &models.Insight{
				ProjectID:   project.ID,
				Type:        models.InsightIncompleteFeature,
				Severity:    models.InsightSeverityMedium,
				Status:      models.InsightStatusNew,
				Title:       "Stale pending tasks detected",
				Description: fmt.Sprintf("Found %d tasks pending for more than 72 hours. These may represent abandoned or blocked features.", staleCount),
				Evidence:    string(evidenceJSON),
				Suggestion:  "Review stale tasks: cancel abandoned ones, reprioritize blocked ones, or break into smaller tasks.",
				Impact:      "Clearing stale tasks improves backlog hygiene and reduces planning overhead.",
				Confidence:  0.8,
			}
			if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
				return nil, err
			}
			insights = append(insights, *insight)
		}
	}

	return insights, nil
}

// detectTechDebt identifies areas of technical debt
func (s *InsightsService) detectTechDebt(ctx context.Context, project *models.Project) ([]models.Insight, error) {
	var insights []models.Insight

	// Look for tasks with very long execution times (complexity indicator)
	slowExecs, err := s.insightsRepo.GetSlowExecutions(ctx, project.ID, 300, 10) // > 5 minutes
	if err != nil {
		return nil, err
	}

	if len(slowExecs) > 3 {
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, "Multiple slow task executions", models.InsightTechDebt)
		if err != nil {
			return nil, err
		}
		if !exists {
			var slowNames []string
			for _, se := range slowExecs {
				slowNames = append(slowNames, fmt.Sprintf("%s (%ds)", se.TaskTitle, se.Duration))
			}
			evidence := map[string]interface{}{
				"slow_tasks": slowNames,
				"count":      len(slowExecs),
			}
			evidenceJSON, _ := json.Marshal(evidence)

			insight := &models.Insight{
				ProjectID:   project.ID,
				Type:        models.InsightTechDebt,
				Severity:    models.InsightSeverityMedium,
				Status:      models.InsightStatusNew,
				Title:       "Multiple slow task executions",
				Description: fmt.Sprintf("Found %d task executions taking over 5 minutes. This may indicate complex code areas that need refactoring.", len(slowExecs)),
				Evidence:    string(evidenceJSON),
				Suggestion:  "Break complex tasks into smaller, more focused pieces. Consider refactoring areas where tasks consistently take long.",
				Impact:      "Faster execution times improve developer productivity and reduce costs.",
				Confidence:  0.75,
			}
			if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
				return nil, err
			}
			insights = append(insights, *insight)
		}
	}

	return insights, nil
}

// detectOptimizations finds optimization opportunities from execution data
func (s *InsightsService) detectOptimizations(ctx context.Context, project *models.Project) ([]models.Insight, error) {
	var insights []models.Insight

	// Check for high failure rate
	failedPatterns, err := s.insightsRepo.GetFailedTaskPatterns(ctx, project.ID, 3)
	if err != nil {
		return nil, err
	}

	totalFailures := 0
	for _, p := range failedPatterns {
		totalFailures += p.FailCount
	}

	if totalFailures > 10 {
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, "High overall task failure rate", models.InsightOptimization)
		if err != nil {
			return nil, err
		}
		if !exists {
			evidence := map[string]interface{}{
				"total_failures":   totalFailures,
				"pattern_count":    len(failedPatterns),
			}
			evidenceJSON, _ := json.Marshal(evidence)

			insight := &models.Insight{
				ProjectID:   project.ID,
				Type:        models.InsightOptimization,
				Severity:    models.InsightSeverityHigh,
				Status:      models.InsightStatusNew,
				Title:       "High overall task failure rate",
				Description: fmt.Sprintf("Found %d total failures across %d recurring patterns. Consider improving prompts, adding better error handling, or refining task definitions.", totalFailures, len(failedPatterns)),
				Evidence:    string(evidenceJSON),
				Suggestion:  "Review the most frequently failing tasks and improve their prompts or split them into simpler subtasks.",
				Impact:      "Reducing failure rate directly improves throughput and reduces compute costs.",
				Confidence:  0.85,
			}
			if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
				return nil, err
			}
			insights = append(insights, *insight)
		}
	}

	return insights, nil
}

// generateProactiveSuggestions uses AI to generate actionable suggestions
func (s *InsightsService) generateProactiveSuggestions(ctx context.Context, project *models.Project, bugCount, incompleteCount, debtCount, optCount int) ([]models.Insight, error) {
	if s.llmSvc == nil {
		return nil, nil
	}

	// Get default agent
	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent configured")
	}

	// Build context about the project
	prompt := fmt.Sprintf(`You are analyzing a software project for proactive improvements. Based on the following project context, suggest 2-3 actionable improvements.

Project: %s
Repository: %s

Current analysis found:
- %d recurring bug patterns
- %d incomplete feature indicators
- %d tech debt items
- %d optimization opportunities

Respond with a JSON array of suggestions. Each suggestion should have:
- "title": short descriptive title (max 80 chars)
- "description": detailed explanation
- "suggestion": what to do about it
- "impact": expected impact
- "severity": one of "info", "low", "medium", "high"
- "confidence": 0.0 to 1.0

Respond with ONLY the JSON array, no markdown fences or extra text.`, project.Name, project.RepoPath, bugCount, incompleteCount, debtCount, optCount)

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI suggestion generation failed: %w", err)
	}

	// Parse AI response
	suggestions, err := parseProactiveSuggestions(output)
	if err != nil {
		log.Printf("[insights-svc] failed to parse AI suggestions: %v", err)
		return nil, nil
	}

	var insights []models.Insight
	for _, sg := range suggestions {
		exists, err := s.insightsRepo.ExistingInsight(ctx, project.ID, sg.Title, models.InsightProactiveSuggestion)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}

		insight := &models.Insight{
			ProjectID:   project.ID,
			Type:        models.InsightProactiveSuggestion,
			Severity:    sg.Severity,
			Status:      models.InsightStatusNew,
			Title:       sg.Title,
			Description: sg.Description,
			Evidence:    "{}",
			Suggestion:  sg.Suggestion,
			Impact:      sg.Impact,
			Confidence:  sg.Confidence,
		}
		if insight.Confidence <= 0 || insight.Confidence > 1 {
			insight.Confidence = 0.5
		}
		if err := s.insightsRepo.CreateInsight(ctx, insight); err != nil {
			return nil, err
		}
		insights = append(insights, *insight)
	}

	return insights, nil
}

// ExtractKnowledge analyzes task completions and extracts knowledge entries
func (s *InsightsService) ExtractKnowledge(ctx context.Context, projectID string) ([]models.KnowledgeEntry, error) {
	if s.llmSvc == nil {
		return nil, nil
	}

	// Get recent completed tasks with their execution output
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent")
	}

	// Get recent completed tasks
	completedTasks, err := s.insightsRepo.GetCompletedBugFixes(ctx, projectID, 20)
	if err != nil {
		return nil, err
	}
	if len(completedTasks) == 0 {
		return nil, nil
	}

	// Build context from recent tasks
	var taskSummaries []string
	for _, t := range completedTasks {
		taskSummaries = append(taskSummaries, fmt.Sprintf("- [%s] %s: %s", t.Tag, t.Title, util.Truncate(t.Prompt, 100)))
	}

	prompt := fmt.Sprintf(`Analyze these recently completed tasks for a project called "%s" and extract 2-3 key decisions or learnings that would be valuable for the team's knowledge base.

Recent tasks:
%s

Respond with a JSON array where each entry has:
- "topic": concise topic title
- "content": explanation of the decision/learning and WHY it was made
- "tags": array of 2-3 relevant tags for searchability

Respond with ONLY the JSON array, no markdown fences.`, project.Name, strings.Join(taskSummaries, "\n"))

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("knowledge extraction failed: %w", err)
	}

	entries, err := parseKnowledgeEntries(output)
	if err != nil {
		log.Printf("[insights-svc] failed to parse knowledge entries: %v", err)
		return nil, nil
	}

	var results []models.KnowledgeEntry
	for _, e := range entries {
		k := &models.KnowledgeEntry{
			ProjectID: projectID,
			Topic:     e.Topic,
			Content:   e.Content,
			Source:    "ai_analysis",
			SourceRef: fmt.Sprintf("analysis_%s", time.Now().Format("20060102")),
		}
		if err := k.SetTags(e.Tags); err != nil {
			continue
		}
		if err := s.insightsRepo.CreateKnowledge(ctx, k); err != nil {
			return nil, err
		}
		results = append(results, *k)
	}

	return results, nil
}

// UpdateInsightStatus updates the status of an insight
func (s *InsightsService) UpdateInsightStatus(ctx context.Context, id string, status models.InsightStatus) error {
	return s.insightsRepo.UpdateStatus(ctx, id, status)
}

// AcceptInsight marks an insight as accepted and optionally links a task
func (s *InsightsService) AcceptInsight(ctx context.Context, insightID string, taskID *string) error {
	if taskID != nil && *taskID != "" {
		return s.insightsRepo.LinkTask(ctx, insightID, *taskID)
	}
	return s.insightsRepo.UpdateStatus(ctx, insightID, models.InsightStatusAccepted)
}

// GetInsight returns a single insight
func (s *InsightsService) GetInsight(ctx context.Context, id string) (*models.Insight, error) {
	return s.insightsRepo.GetInsight(ctx, id)
}

// ListByType returns insights filtered by type
func (s *InsightsService) ListByType(ctx context.Context, projectID string, insightType models.InsightType) ([]models.Insight, error) {
	return s.insightsRepo.ListByType(ctx, projectID, insightType, 50)
}

// SearchKnowledge searches the knowledge base
func (s *InsightsService) SearchKnowledge(ctx context.Context, projectID, query string) ([]models.KnowledgeEntry, error) {
	return s.insightsRepo.SearchKnowledge(ctx, projectID, query, 20)
}

// DeleteInsight removes an insight
func (s *InsightsService) DeleteInsight(ctx context.Context, id string) error {
	return s.insightsRepo.DeleteInsight(ctx, id)
}

// DeleteKnowledge removes a knowledge entry
func (s *InsightsService) DeleteKnowledge(ctx context.Context, id string) error {
	return s.insightsRepo.DeleteKnowledge(ctx, id)
}

// ListReports returns recent insight reports
func (s *InsightsService) ListReports(ctx context.Context, projectID string, limit int) ([]models.InsightReport, error) {
	return s.insightsRepo.ListReports(ctx, projectID, limit)
}

// RunHealthCheck generates a comprehensive project health evaluation using AI
func (s *InsightsService) RunHealthCheck(ctx context.Context, projectID string) (*models.HealthCheck, error) {
	if s.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not available")
	}

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent configured")
	}

	// Gather project metrics
	total, completed, failed, pending, backlog, err := s.insightsRepo.GetProjectTaskStats(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get task stats: %w", err)
	}

	priorityDist, err := s.insightsRepo.GetTaskPriorityDistribution(ctx, projectID)
	if err != nil {
		log.Printf("[insights-svc] priority distribution error: %v", err)
		priorityDist = make(map[int]int)
	}

	completionRate, err := s.insightsRepo.GetRecentCompletionRate(ctx, projectID, 30)
	if err != nil {
		log.Printf("[insights-svc] completion rate error: %v", err)
	}

	activityTrend, err := s.insightsRepo.GetTaskActivityTrend(ctx, projectID, 14)
	if err != nil {
		log.Printf("[insights-svc] activity trend error: %v", err)
	}

	tagDist, err := s.insightsRepo.GetTagDistribution(ctx, projectID)
	if err != nil {
		log.Printf("[insights-svc] tag distribution error: %v", err)
		tagDist = make(map[string]int)
	}

	// Get failure patterns
	failPatterns, err := s.insightsRepo.GetFailedTaskPatterns(ctx, projectID, 1)
	if err != nil {
		log.Printf("[insights-svc] fail patterns error: %v", err)
	}

	// Get recent tasks for prompt quality analysis
	recentTasks, err := s.taskRepo.ListByProject(ctx, projectID, "")
	if err != nil {
		log.Printf("[insights-svc] recent tasks error: %v", err)
	}

	// Build task summary for LLM
	var taskSamples []string
	limit := 20
	if len(recentTasks) < limit {
		limit = len(recentTasks)
	}
	for i := 0; i < limit; i++ {
		t := recentTasks[i]
		promptPreview := util.Truncate(t.Prompt, 120)
		taskSamples = append(taskSamples, fmt.Sprintf("- [%s] %s (priority:%d, status:%s, tag:%s): %s",
			t.Category, t.Title, t.Priority, t.Status, t.Tag, promptPreview))
	}

	// Build activity pattern description
	var activityDesc string
	if len(activityTrend) > 0 {
		var days []string
		for _, a := range activityTrend {
			days = append(days, fmt.Sprintf("%s: %d tasks", a.Date, a.Count))
		}
		activityDesc = strings.Join(days, ", ")
	} else {
		activityDesc = "No recent activity data"
	}

	// Build failure pattern description
	var failDesc string
	if len(failPatterns) > 0 {
		var patterns []string
		for _, p := range failPatterns {
			patterns = append(patterns, fmt.Sprintf("'%s' failed %d times", util.Truncate(p.Title, 50), p.FailCount))
		}
		failDesc = strings.Join(patterns, "; ")
	} else {
		failDesc = "No recurring failures"
	}

	// Build priority description
	priorityDesc := fmt.Sprintf("P1(urgent):%d, P2(high):%d, P3(medium):%d, P4(low):%d, P5(someday):%d",
		priorityDist[1], priorityDist[2], priorityDist[3], priorityDist[4], priorityDist[5])

	// Build tag distribution description
	var tagParts []string
	for tag, count := range tagDist {
		tagParts = append(tagParts, fmt.Sprintf("%s:%d", tag, count))
	}
	tagDesc := strings.Join(tagParts, ", ")
	if tagDesc == "" {
		tagDesc = "No tags used"
	}

	prompt := fmt.Sprintf(`You are an expert product/project coach evaluating a builder's project health. Analyze the following data for the project "%s" and provide a brutally honest but constructive assessment.

## Project Metrics
- Total tasks: %d
- Completed: %d (%.1f%% completion rate over last 30 days)
- Failed: %d
- Pending (active): %d
- Backlog size: %d
- Priority distribution: %s
- Tag/category distribution: %s

## Activity Pattern (last 14 days)
%s

## Failure Patterns
%s

## Sample Tasks (recent %d)
%s

## Your Assessment

Respond with a JSON object with these exact fields:
{
  "grade": "A letter grade from A+ to F based on overall project health",
  "strengths": "2-4 specific things they're doing well, based on actual data. Be specific, reference real numbers and patterns.",
  "improvements": "2-4 specific areas to improve. Be constructive but honest. Reference actual data.",
  "assessment": "A 2-3 paragraph personalized overall assessment. Be thoughtful, specific, and encouraging but honest. Don't be generic.",
  "how_to_improve": "Detailed guidance on how to reach the next grade level. Include 4-6 specific actionable steps covering: shipping velocity, task quality, prioritization, backlog management, and focus areas. Reference their actual metrics."
}

Grading criteria:
- A+/A: High completion rate (>80%%), low failure rate, balanced priorities, consistent activity, clear focused tasks, clean backlog
- A-/B+: Good completion (>65%%), some failures, mostly balanced, regular activity
- B/B-: Moderate completion (>50%%), notable failures, priority imbalance, inconsistent activity
- C+/C: Low completion (<50%%), many failures, urgent-heavy priorities, sporadic activity, vague tasks
- C-/D: Very low completion, high failure rate, chaotic priorities, large unfocused backlog
- F: Minimal activity, overwhelming backlog, no clear direction

Be specific to THIS project's data. Don't give generic advice. Reference actual numbers.
Respond with ONLY the JSON object, no markdown fences or extra text.`,
		project.Name, total, completed, completionRate, failed, pending, backlog,
		priorityDesc, tagDesc, activityDesc, failDesc, len(taskSamples), strings.Join(taskSamples, "\n"))

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI health check failed: %w", err)
	}

	// Parse AI response
	hc, err := parseHealthCheckResponse(output)
	if err != nil {
		return nil, fmt.Errorf("parse health check: %w", err)
	}

	hc.ProjectID = projectID
	hc.TasksTotal = total
	hc.TasksCompleted = completed
	hc.TasksFailed = failed
	hc.TasksPending = pending
	hc.BacklogSize = backlog
	hc.AvgCompletionPct = completionRate

	if err := s.insightsRepo.CreateHealthCheck(ctx, hc); err != nil {
		return nil, fmt.Errorf("save health check: %w", err)
	}

	log.Printf("[insights-svc] health check complete for project %s: grade=%s", projectID, hc.Grade)
	return hc, nil
}

// GetLatestHealthCheck returns the most recent health check for a project
func (s *InsightsService) GetLatestHealthCheck(ctx context.Context, projectID string) (*models.HealthCheck, error) {
	return s.insightsRepo.GetLatestHealthCheck(ctx, projectID)
}

// ListHealthChecks returns recent health checks for trend display
func (s *InsightsService) ListHealthChecks(ctx context.Context, projectID string, limit int) ([]models.HealthCheck, error) {
	return s.insightsRepo.ListHealthChecks(ctx, projectID, limit)
}

// GradeIdeas evaluates the quality of a user's ideas and tasks using AI
func (s *InsightsService) GradeIdeas(ctx context.Context, projectID string) (*models.IdeaGrade, error) {
	if s.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not available")
	}

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent configured")
	}

	// Gather all tasks for analysis
	allTasks, err := s.taskRepo.ListByProject(ctx, projectID, "")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	if len(allTasks) == 0 {
		return nil, fmt.Errorf("no tasks found to evaluate")
	}

	// Compute basic metrics
	total := len(allTasks)
	var completed, failed int
	tagCounts := make(map[string]int)
	var taskSamples []string

	limit := 30
	if total < limit {
		limit = total
	}
	for i, t := range allTasks {
		if t.Status == models.StatusCompleted {
			completed++
		}
		if t.Status == models.StatusFailed {
			failed++
		}
		tagCounts[string(t.Tag)]++
		if i < limit {
			promptPreview := util.Truncate(t.Prompt, 150)
			taskSamples = append(taskSamples, fmt.Sprintf("- Title: \"%s\" | Tag: %s | Priority: %d | Status: %s | Prompt: %s",
				t.Title, t.Tag, t.Priority, t.Status, promptPreview))
		}
	}

	completionRate := 0.0
	if total > 0 {
		completionRate = float64(completed) / float64(total) * 100
	}

	// Build tag summary
	var tagParts []string
	for tag, count := range tagCounts {
		tagParts = append(tagParts, fmt.Sprintf("%s: %d", tag, count))
	}
	tagSummary := strings.Join(tagParts, ", ")
	if tagSummary == "" {
		tagSummary = "No tags used"
	}

	prompt := fmt.Sprintf(`You are an expert product coach evaluating the quality of a builder's IDEAS and TASKS. Focus specifically on the quality of their thinking, not project execution metrics.

## Project: "%s"
## Task Statistics
- Total tasks: %d
- Completed: %d (%.1f%% completion rate)
- Failed: %d
- Tag distribution: %s

## Task Samples (%d of %d total)
%s

## Evaluation Criteria

Score each dimension 0-100:

1. **Clarity & Specificity** (clarity_score): How clear, specific, and well-defined are the task titles and prompts? Do they provide enough context for execution? Are they actionable?

2. **Ambition & Scope** (ambition_score): Are the ideas ambitious and meaningful? Do they push the project forward significantly, or are they mostly trivial/maintenance tasks?

3. **Follow-Through** (follow_through): What percentage of tasks get completed? Is there evidence of abandoned ideas or consistent delivery?

4. **Diversity of Work** (diversity_score): Is there a healthy mix of features, bug fixes, improvements, and different types of work? Or is the work too narrow/repetitive?

5. **Strategic Thinking** (strategy_score): Do tasks build on each other logically? Is there evidence of a coherent vision? Are priorities well-assigned?

## Response Format

Respond with a JSON object with these exact fields:
{
  "grade": "Letter grade from A+ to F",
  "next_grade": "The next grade up to aim for (e.g., if grade is B+, next_grade is A-)",
  "summary": "1-2 sentence overall summary of their idea quality",
  "strengths": "2-4 specific strengths in their ideation and task creation. Reference actual task titles and patterns.",
  "improvements": "2-4 specific, actionable areas where their ideas could be better. Be constructive and reference actual examples.",
  "how_to_next_grade": "4-6 specific, actionable steps to reach the next grade level. Be very specific — reference their actual tasks, suggest concrete improvements to specific task titles/prompts they wrote, and give examples of what better versions would look like.",
  "clarity_score": 0-100,
  "ambition_score": 0-100,
  "follow_through": 0-100,
  "diversity_score": 0-100,
  "strategy_score": 0-100
}

Grading rubric:
- A+/A: Exceptionally clear tasks, ambitious vision, high completion, diverse work, strong strategic coherence
- A-/B+: Very good clarity, good ambition, solid follow-through, decent variety, mostly strategic
- B/B-: Good clarity with some vague tasks, moderate ambition, adequate follow-through, some variety
- C+/C: Mixed clarity, modest ambition, inconsistent follow-through, narrow focus
- C-/D: Vague tasks, low ambition, poor follow-through, repetitive work, no clear strategy
- F: Minimal or unintelligible tasks, no direction

Be honest but constructive. Reference SPECIFIC task titles in your feedback.
Respond with ONLY the JSON object, no markdown fences or extra text.`,
		project.Name, total, completed, completionRate, failed, tagSummary, len(taskSamples), total, strings.Join(taskSamples, "\n"))

	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI idea grading failed: %w", err)
	}

	ig, err := parseIdeaGradeResponse(output)
	if err != nil {
		return nil, fmt.Errorf("parse idea grade: %w", err)
	}

	ig.ProjectID = projectID
	ig.TasksEvaluated = total

	if err := s.insightsRepo.CreateIdeaGrade(ctx, ig); err != nil {
		return nil, fmt.Errorf("save idea grade: %w", err)
	}

	log.Printf("[insights-svc] idea grade complete for project %s: grade=%s", projectID, ig.Grade)
	return ig, nil
}

// GetLatestIdeaGrade returns the most recent idea grade for a project
func (s *InsightsService) GetLatestIdeaGrade(ctx context.Context, projectID string) (*models.IdeaGrade, error) {
	return s.insightsRepo.GetLatestIdeaGrade(ctx, projectID)
}

// ListIdeaGrades returns recent idea grades for trend display
func (s *InsightsService) ListIdeaGrades(ctx context.Context, projectID string, limit int) ([]models.IdeaGrade, error) {
	return s.insightsRepo.ListIdeaGrades(ctx, projectID, limit)
}

// --- Parsing helpers ---

type aiSuggestion struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Suggestion  string          `json:"suggestion"`
	Impact      string          `json:"impact"`
	Severity    models.InsightSeverity `json:"severity"`
	Confidence  float64         `json:"confidence"`
}

func parseProactiveSuggestions(output string) ([]aiSuggestion, error) {
	cleaned := util.ExtractJSONArray(output)
	var suggestions []aiSuggestion
	if err := json.Unmarshal([]byte(cleaned), &suggestions); err != nil {
		return nil, fmt.Errorf("parse suggestions: %w", err)
	}
	return suggestions, nil
}

type aiKnowledgeEntry struct {
	Topic   string   `json:"topic"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

func parseKnowledgeEntries(output string) ([]aiKnowledgeEntry, error) {
	cleaned := util.ExtractJSONArray(output)
	var entries []aiKnowledgeEntry
	if err := json.Unmarshal([]byte(cleaned), &entries); err != nil {
		return nil, fmt.Errorf("parse knowledge: %w", err)
	}
	return entries, nil
}

type aiHealthCheck struct {
	Grade        string `json:"grade"`
	Strengths    string `json:"strengths"`
	Improvements string `json:"improvements"`
	Assessment   string `json:"assessment"`
	HowToImprove string `json:"how_to_improve"`
}

func parseHealthCheckResponse(output string) (*models.HealthCheck, error) {
	cleaned := util.ExtractJSONObject(output)
	var hc aiHealthCheck
	if err := json.Unmarshal([]byte(cleaned), &hc); err != nil {
		return nil, fmt.Errorf("parse health check JSON: %w", err)
	}

	return &models.HealthCheck{
		Grade:        hc.Grade,
		Strengths:    hc.Strengths,
		Improvements: hc.Improvements,
		Assessment:   hc.Assessment,
		HowToImprove: hc.HowToImprove,
	}, nil
}

type aiIdeaGrade struct {
	Grade          string  `json:"grade"`
	NextGrade      string  `json:"next_grade"`
	Summary        string  `json:"summary"`
	Strengths      string  `json:"strengths"`
	Improvements   string  `json:"improvements"`
	HowToNextGrade string  `json:"how_to_next_grade"`
	ClarityScore   float64 `json:"clarity_score"`
	AmbitionScore  float64 `json:"ambition_score"`
	FollowThrough  float64 `json:"follow_through"`
	DiversityScore float64 `json:"diversity_score"`
	StrategyScore  float64 `json:"strategy_score"`
}

func parseIdeaGradeResponse(output string) (*models.IdeaGrade, error) {
	cleaned := util.ExtractJSONObject(output)
	var ig aiIdeaGrade
	if err := json.Unmarshal([]byte(cleaned), &ig); err != nil {
		return nil, fmt.Errorf("parse idea grade JSON: %w", err)
	}

	return &models.IdeaGrade{
		Grade:          ig.Grade,
		NextGrade:      ig.NextGrade,
		Summary:        ig.Summary,
		Strengths:      ig.Strengths,
		Improvements:   ig.Improvements,
		HowToNextGrade: ig.HowToNextGrade,
		ClarityScore:   ig.ClarityScore,
		AmbitionScore:  ig.AmbitionScore,
		FollowThrough:  ig.FollowThrough,
		DiversityScore: ig.DiversityScore,
		StrategyScore:  ig.StrategyScore,
	}, nil
}

