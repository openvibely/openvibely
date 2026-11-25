package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/util"
)

var ErrInsightNotFound = persistence.ErrInsightNotFound

type InsightsService struct {
	projectAccess *ProjectAccessService
	authOff       bool
	insightsRepo  persistence.InsightsStore
	taskRepo      persistence.TaskStore
	projectRepo   persistence.ProjectStore
	llmConfigRepo persistence.LLMConfigStore
	execRepo      persistence.ExecutionStore
	llmSvc        *LLMService
}

func NewInsightsService(
	insightsRepo persistence.InsightsStore,
	taskRepo persistence.TaskStore,
	projectRepo persistence.ProjectStore,
	llmConfigRepo persistence.LLMConfigStore,
	execRepo persistence.ExecutionStore,
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

// RunAnalysis performs a full analysis of the project and generates insights
func (s *InsightsService) RunAnalysis(ctx context.Context, projectID string) (*models.InsightReport, error) {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.Edit)
	if authErr != nil {
		return nil, authErr
	}
	applog.Infof("[insights-svc] starting analysis for project %s", projectID)

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	var insightIDs []string
	var analysisLog []string

	// 1. Bug pattern detection
	bugInsights, err := s.detectBugPatterns(ctx, project)
	if err != nil {
		applog.Infof("[insights-svc] bug pattern detection error: %v", err)
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
		applog.Infof("[insights-svc] incomplete feature detection error: %v", err)
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
		applog.Infof("[insights-svc] tech debt detection error: %v", err)
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
		applog.Infof("[insights-svc] optimization detection error: %v", err)
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
			applog.Infof("[insights-svc] proactive suggestions error: %v", err)
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

	applog.Infof("[insights-svc] analysis complete for project %s: %d insights", projectID, len(insightIDs))
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
		if t.Status == models.StatusPending && time.Since(t.CreatedAt) > 72*time.Hour {
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
				"total_failures": totalFailures,
				"pattern_count":  len(failedPatterns),
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

	output, _, err := s.llmSvc.CallAgentDirect(WithDirectUsageProject(ctx, project.ID), insightsScopePrompt(project.ID, prompt), nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI suggestion generation failed: %w", err)
	}

	// Parse AI response
	suggestions, err := parseProactiveSuggestions(output)
	if err != nil {
		applog.Infof("[insights-svc] failed to parse AI suggestions: %v", err)
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

// UpdateInsightStatus updates the status of an insight within a project.
func (s *InsightsService) UpdateInsightStatus(ctx context.Context, projectID, id string, status models.InsightStatus) error {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.Edit)
	if authErr != nil {
		return authErr
	}
	return s.insightsRepo.UpdateStatus(ctx, projectID, id, status)
}

// AcceptInsight marks an insight as accepted within a project and optionally links a task.
func (s *InsightsService) AcceptInsight(ctx context.Context, projectID, insightID string, taskID *string) error {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.Edit)
	if authErr != nil {
		return authErr
	}
	if taskID != nil && *taskID != "" {
		return s.insightsRepo.LinkTask(ctx, projectID, insightID, *taskID)
	}
	return s.insightsRepo.UpdateStatus(ctx, projectID, insightID, models.InsightStatusAccepted)
}

// GetInsight returns a single insight within a project.
func (s *InsightsService) GetInsight(ctx context.Context, projectID, id string) (*models.Insight, error) {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.View)
	if authErr != nil {
		return nil, authErr
	}
	return s.insightsRepo.GetInsight(ctx, projectID, id)
}

// ListByType returns insights filtered by type
func (s *InsightsService) ListByType(ctx context.Context, projectID string, insightType models.InsightType) ([]models.Insight, error) {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.View)
	if authErr != nil {
		return nil, authErr
	}
	return s.insightsRepo.ListByType(ctx, projectID, insightType, 50)
}

// DeleteInsight removes an insight from a project.
func (s *InsightsService) DeleteInsight(ctx context.Context, projectID, id string) error {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.Edit)
	if authErr != nil {
		return authErr
	}
	return s.insightsRepo.DeleteInsight(ctx, projectID, id)
}

// ListReports returns recent insight reports
func (s *InsightsService) ListReports(ctx context.Context, projectID string, limit int) ([]models.InsightReport, error) {
	ctx, authErr := s.authorizeInsights(ctx, projectID, authz.View)
	if authErr != nil {
		return nil, authErr
	}
	return s.insightsRepo.ListReports(ctx, projectID, limit)
}

// --- Parsing helpers ---

type aiSuggestion struct {
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Suggestion  string                 `json:"suggestion"`
	Impact      string                 `json:"impact"`
	Severity    models.InsightSeverity `json:"severity"`
	Confidence  float64                `json:"confidence"`
}

func parseProactiveSuggestions(output string) ([]aiSuggestion, error) {
	cleaned := util.ExtractJSONArray(output)
	var suggestions []aiSuggestion
	if err := json.Unmarshal([]byte(cleaned), &suggestions); err != nil {
		return nil, fmt.Errorf("parse suggestions: %w", err)
	}
	return suggestions, nil
}

func insightsScopePrompt(projectID, prompt string) string {
	if models.IsSharedProjectID(projectID) {
		return "This is one combined analysis of every copy of one repository, kept on several machines. Produce one overall assessment.\n\n" + prompt
	}
	if projectID == models.AllProjectsID {
		return "This is one combined analysis of all projects. The metrics and tasks below span all projects. Produce one overall assessment, not separate reports per project. Do not treat this scope as a single repository.\n\n" + prompt
	}
	return prompt
}

func (s *InsightsService) SetProjectAuthorization(access *ProjectAccessService, authOff bool) {
	s.projectAccess, s.authOff = access, authOff
}

func (s *InsightsService) authorizeInsights(ctx context.Context, projectID string, permission authz.Permission) (context.Context, error) {
	if s.projectAccess == nil {
		return ctx, nil
	}
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok || principal.UserID == "" {
		if !s.authOff {
			return ctx, authz.ErrDenied
		}
		ctx = models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	}
	return ctx, s.projectAccess.Require(ctx, projectID, permission)
}
