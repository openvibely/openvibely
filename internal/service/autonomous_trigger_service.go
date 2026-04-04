package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// BuildSummary holds a concise summary of a completed autonomous build.
type BuildSummary struct {
	RootTaskID string `json:"root_task_id"`
	Summary    string `json:"summary"`
	Phase      string `json:"phase"`   // Which phase produced the summary (e.g. "summary", "review")
	Completed  bool   `json:"completed"`
}

// AutonomousTriggerService creates autonomous build pipelines as chains of tasks.
// Each build phase becomes a visible task on the kanban board, connected via task chaining.
type AutonomousTriggerService struct {
	taskSvc        *TaskService
	projectRepo    *repository.ProjectRepo
	taskRepo       *repository.TaskRepo
	execRepo       *repository.ExecutionRepo
	autonomousRepo *repository.AutonomousRepo // for config only
	trendSvc       *TrendIntelligenceService   // for trend intelligence (optional)
}

// SetTrendIntelligenceService sets the trend intelligence service for enriching discovery.
func (s *AutonomousTriggerService) SetTrendIntelligenceService(trendSvc *TrendIntelligenceService) {
	s.trendSvc = trendSvc
}

func NewAutonomousTriggerService(
	taskSvc *TaskService,
	projectRepo *repository.ProjectRepo,
	taskRepo *repository.TaskRepo,
	execRepo *repository.ExecutionRepo,
	autonomousRepo *repository.AutonomousRepo,
) *AutonomousTriggerService {
	return &AutonomousTriggerService{
		taskSvc:        taskSvc,
		projectRepo:    projectRepo,
		taskRepo:       taskRepo,
		execRepo:       execRepo,
		autonomousRepo: autonomousRepo,
	}
}

// TriggerBuild creates the first task in an autonomous build chain.
// Subsequent tasks are created automatically via task chaining as each phase completes.
func (s *AutonomousTriggerService) TriggerBuild(ctx context.Context, projectID string) (*models.Task, error) {
	log.Printf("[autonomous-trigger] triggering build for project=%s", projectID)

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if project == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	// Load config for excluded areas
	excludedAreas := "none"
	if s.autonomousRepo != nil {
		config, _ := s.autonomousRepo.GetConfig(ctx, projectID)
		if config != nil {
			areas, _ := config.ParseExcludedAreas()
			if len(areas) > 0 {
				excludedAreas = strings.Join(areas, ", ")
			}
		}
	}

	// Gather task context for discovery prompt
	taskContext := s.gatherTaskContext(ctx, projectID)

	// Gather trend intelligence context (if available)
	trendContext := ""
	costAnalysisContext := ""
	if s.trendSvc != nil {
		trendContext = s.trendSvc.GetTrendContext(ctx, projectID)
		costAnalysisContext = s.trendSvc.GetCostAnalysisContext()
	}

	// Build the nested chain config (last phase first, working backwards)
	summaryChain := &models.ChainConfiguration{
		Enabled: false, // Terminal phase
	}

	reviewChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Build Summary",
		ChildCategory:     "active",
		ChildPromptPrefix: summaryPrompt(),
		ChildChainConfig:  summaryChain,
	}

	testingChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Code Review",
		ChildCategory:     "active",
		ChildPromptPrefix: reviewPrompt(),
		ChildChainConfig:  reviewChain,
	}

	implementationChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Testing",
		ChildCategory:     "active",
		ChildPromptPrefix: testingPrompt(),
		ChildChainConfig:  testingChain,
	}

	designChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Implementation",
		ChildCategory:     "active",
		ChildPromptPrefix: implementationPrompt(),
		ChildChainConfig:  implementationChain,
	}

	selectionChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Architecture Design",
		ChildCategory:     "active",
		ChildPromptPrefix: designPrompt(),
		ChildChainConfig:  designChain,
	}

	discoveryChain := &models.ChainConfiguration{
		Enabled:           true,
		Trigger:           "on_completion",
		ChildTitle:        "[Auto Build] Feature Selection",
		ChildCategory:     "active",
		ChildPromptPrefix: selectionPrompt() + costAnalysisContext,
		ChildChainConfig:  selectionChain,
	}

	// Create the root discovery task
	discoveryTask := &models.Task{
		ProjectID: projectID,
		Title:     "[Auto Build] Feature Discovery",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    discoveryPrompt(project, taskContext, excludedAreas, trendContext),
		Tag:       models.TagFeature,
	}

	if err := discoveryTask.SetChainConfig(discoveryChain); err != nil {
		return nil, fmt.Errorf("set chain config: %w", err)
	}

	if err := s.taskSvc.Create(ctx, discoveryTask); err != nil {
		return nil, fmt.Errorf("create discovery task: %w", err)
	}

	log.Printf("[autonomous-trigger] created discovery task id=%s for project=%s", discoveryTask.ID, projectID)
	return discoveryTask, nil
}

// GetConfig returns the autonomous build configuration for a project.
func (s *AutonomousTriggerService) GetConfig(ctx context.Context, projectID string) (*models.AutonomousConfig, error) {
	if s.autonomousRepo == nil {
		return nil, nil
	}
	return s.autonomousRepo.GetConfig(ctx, projectID)
}

// UpdateConfig updates the autonomous build configuration.
func (s *AutonomousTriggerService) UpdateConfig(ctx context.Context, config *models.AutonomousConfig) error {
	if s.autonomousRepo == nil {
		return fmt.Errorf("autonomous repo not available")
	}
	return s.autonomousRepo.UpsertConfig(ctx, config)
}

// ListRecentBuilds returns recent autonomous build root tasks for a project.
func (s *AutonomousTriggerService) ListRecentBuilds(ctx context.Context, projectID string) ([]models.Task, error) {
	allTasks, err := s.taskRepo.ListByProject(ctx, projectID, "")
	if err != nil {
		return nil, err
	}

	var builds []models.Task
	for _, t := range allTasks {
		if strings.HasPrefix(t.Title, "[Auto Build] Feature Discovery") && t.ParentTaskID == nil {
			builds = append(builds, t)
		}
	}
	// Return at most 10
	if len(builds) > 10 {
		builds = builds[:10]
	}
	return builds, nil
}

// GetBuildSummary returns the summary for an autonomous build by traversing the task chain
// from the root discovery task to the final summary task.
func (s *AutonomousTriggerService) GetBuildSummary(ctx context.Context, rootTaskID string) (*BuildSummary, error) {
	// Find root task first to get project ID
	rootTask, err := s.taskRepo.GetByID(ctx, rootTaskID)
	if err != nil {
		return nil, fmt.Errorf("get root task: %w", err)
	}
	if rootTask == nil {
		return nil, fmt.Errorf("task not found: %s", rootTaskID)
	}

	// Walk the chain from root task following parent_task_id references
	allTasks, err := s.taskRepo.ListByProject(ctx, rootTask.ProjectID, "")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	// Build a map from parent_task_id -> child task
	childMap := make(map[string]*models.Task)
	for i := range allTasks {
		if allTasks[i].ParentTaskID != nil {
			childMap[*allTasks[i].ParentTaskID] = &allTasks[i]
		}
	}

	// Walk the chain from root to find the summary task (or last completed task)
	currentID := rootTaskID
	var summaryTask *models.Task
	var lastCompletedTask *models.Task

	if rootTask.Status == models.StatusCompleted {
		lastCompletedTask = rootTask
	}

	for {
		child, ok := childMap[currentID]
		if !ok {
			break
		}
		if child.Status == models.StatusCompleted {
			lastCompletedTask = child
		}
		if strings.HasPrefix(child.Title, "[Auto Build] Build Summary") {
			summaryTask = child
			break
		}
		currentID = child.ID
	}

	// If we found the summary task and it's completed, return its output
	if summaryTask != nil && summaryTask.Status == models.StatusCompleted {
		output, err := s.getTaskOutput(ctx, summaryTask.ID)
		if err != nil {
			return nil, fmt.Errorf("get summary output: %w", err)
		}
		return &BuildSummary{
			RootTaskID: rootTaskID,
			Summary:    output,
			Phase:      "summary",
			Completed:  true,
		}, nil
	}

	// If the build chain hasn't reached the summary phase yet, check if review is done
	if lastCompletedTask != nil && strings.HasPrefix(lastCompletedTask.Title, "[Auto Build] Code Review") {
		output, err := s.getTaskOutput(ctx, lastCompletedTask.ID)
		if err != nil {
			return nil, fmt.Errorf("get review output: %w", err)
		}
		return &BuildSummary{
			RootTaskID: rootTaskID,
			Summary:    output,
			Phase:      "review",
			Completed:  false,
		}, nil
	}

	// Build still in progress
	return &BuildSummary{
		RootTaskID: rootTaskID,
		Summary:    "",
		Phase:      "in_progress",
		Completed:  false,
	}, nil
}

// GetBuildChainStatus returns the status of each phase in the build chain.
func (s *AutonomousTriggerService) GetBuildChainStatus(ctx context.Context, rootTaskID string) ([]models.Task, error) {
	// Get root task's project
	rootTask, err := s.taskRepo.GetByID(ctx, rootTaskID)
	if err != nil || rootTask == nil {
		return nil, fmt.Errorf("get root task: %w", err)
	}

	allTasks, err := s.taskRepo.ListByProject(ctx, rootTask.ProjectID, "")
	if err != nil {
		return nil, err
	}

	// Build child map
	childMap := make(map[string]*models.Task)
	for i := range allTasks {
		if allTasks[i].ParentTaskID != nil {
			childMap[*allTasks[i].ParentTaskID] = &allTasks[i]
		}
	}

	// Walk the chain
	var chain []models.Task
	chain = append(chain, *rootTask)
	currentID := rootTaskID
	for {
		child, ok := childMap[currentID]
		if !ok {
			break
		}
		chain = append(chain, *child)
		currentID = child.ID
	}
	return chain, nil
}

// getTaskOutput returns the most recent completed execution output for a task.
func (s *AutonomousTriggerService) getTaskOutput(ctx context.Context, taskID string) (string, error) {
	if s.execRepo == nil {
		return "", nil
	}
	execs, err := s.execRepo.ListByTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, e := range execs {
		if e.Status == models.ExecCompleted && e.Output != "" {
			return e.Output, nil
		}
	}
	return "", nil
}

func (s *AutonomousTriggerService) gatherTaskContext(ctx context.Context, projectID string) string {
	activeTasks, _ := s.taskRepo.ListByProject(ctx, projectID, string(models.CategoryActive))
	backlogTasks, _ := s.taskRepo.ListByProject(ctx, projectID, string(models.CategoryBacklog))
	completedTasks, _ := s.taskRepo.ListByProject(ctx, projectID, string(models.CategoryCompleted))
	tasks := append(activeTasks, backlogTasks...)
	tasks = append(tasks, completedTasks...)

	var sb strings.Builder
	sb.WriteString("## Current Task Summary\n\n")
	sb.WriteString(fmt.Sprintf("Total tasks: %d\n", len(tasks)))

	categoryCounts := make(map[string]int)
	for _, t := range tasks {
		categoryCounts[string(t.Category)]++
	}
	sb.WriteString("\nBy category:\n")
	for cat, count := range categoryCounts {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", cat, count))
	}

	// Failed tasks for pain point analysis
	var failedTitles []string
	for _, t := range tasks {
		if t.Status == models.StatusFailed {
			failedTitles = append(failedTitles, t.Title)
		}
	}
	if len(failedTitles) > 0 {
		sb.WriteString("\nRecently failed tasks:\n")
		for _, title := range failedTitles {
			sb.WriteString(fmt.Sprintf("- %s\n", title))
		}
	}

	// Backlog tasks
	var backlogTitles []string
	for _, t := range tasks {
		if t.Category == models.CategoryBacklog {
			backlogTitles = append(backlogTitles, t.Title)
		}
	}
	if len(backlogTitles) > 0 {
		sb.WriteString("\nBacklog tasks (unfulfilled needs):\n")
		limit := 10
		if len(backlogTitles) < limit {
			limit = len(backlogTitles)
		}
		for _, title := range backlogTitles[:limit] {
			sb.WriteString(fmt.Sprintf("- %s\n", title))
		}
	}

	return sb.String()
}

// --- Phase prompt templates ---

func discoveryPrompt(project *models.Project, taskContext, excludedAreas, trendContext string) string {
	trendSection := ""
	if trendContext != "" {
		trendSection = trendContext
	}

	return fmt.Sprintf(`You are an autonomous feature discovery agent for a project called "%s".
Your job is to propose creative, high-impact features that can be realistically built in a single overnight coding session (4-6 hours of automated work).

## Project Info
- Name: %s
- Description: %s
- Repository: %s

## Current State
%s
%s
## Constraints
- Features must be implementable in Go with Echo v4 + HTMX/Templ + SQLite
- Features should follow the existing architecture: models -> repository -> service -> handler -> templates
- Avoid areas: %s
- Each feature must include database migrations, models, repos, services, handlers, templates, and tests
- Features should be self-contained (not break existing functionality)

## What to Consider
1. **Codebase gaps**: What would make this app more complete?
2. **Task patterns**: What tasks do users create most? What keeps failing?
3. **Missing capabilities**: What features do similar tools have that this lacks?
4. **User experience**: What would make daily usage smoother?
5. **Fun factor**: What would be exciting and impressive?
6. **External signals**: What are users and the market demanding? Reference specific trends if trend data is available.
7. **Competitive gaps**: What features do competitors have that we lack?
8. **Cost efficiency**: What features provide the most value for the least operational cost?

## Output
Return a JSON array of 5-8 feature proposals. Each proposal should have:
- title: Short descriptive name
- description: 2-3 sentence description
- problem_statement: What problem does this solve?
- implementation_approach: High-level technical approach
- estimated_complexity: 1-5 scale (1=trivial, 5=very complex)
- expected_impact: 1-5 scale (1=low, 5=transformative)
- risk_assessment: What could go wrong?
- trend_reference: If inspired by external signals, reference the source (e.g., "Seeing increased discussion about X on social media")

Prefer features with complexity 2-4 (buildable in one session) and impact 3-5 (meaningful to users).

Return ONLY the JSON array, no other text.`, project.Name, project.Name, project.Description, project.RepoPath, taskContext, trendSection, excludedAreas)
}

func selectionPrompt() string {
	return `You are evaluating feature proposals for an autonomous build pipeline. The previous phase discovered potential features.

Evaluate each proposal on a scale of 0.0 to 1.0 for each criterion:
- buildability: How realistic is it to build this in 4-6 hours? (0=impossible, 1=easy)
- value: Would users actually use and benefit from this? (0=useless, 1=essential)
- risk: How likely is this to break existing functionality? (0=safe, 1=very risky)
- coherence: Does this fit the product vision? (0=off-brand, 1=perfect fit)
- fun_factor: Is this exciting and impressive? (0=boring, 1=amazing)

Select the single best feature based on weighted scoring: buildability*0.3 + value*0.3 + (1-risk)*0.15 + coherence*0.15 + fun_factor*0.1

Output your selection in this format:
1. Brief evaluation of each proposal (2-3 sentences each)
2. Final selection with reasoning
3. The selected feature's full details (title, description, problem, approach, complexity, impact)

Here are the proposals from the discovery phase:`
}

func designPrompt() string {
	return `You are an expert software architect designing a new feature for a Go web application using Echo v4 + HTMX/Templ + SQLite.

Based on the selected feature from the previous phase, create a detailed implementation plan including:
1. Database schema changes (SQLite migration)
2. New Go model structs
3. Repository methods needed (with SQL queries)
4. Service layer business logic
5. HTTP handler endpoints
6. UI template structure
7. Test plan
8. Integration points with existing features

The application follows a layered architecture:
- Models (internal/models/) - Plain Go structs
- Repository (internal/repository/) - Raw SQL with database/sql
- Service (internal/service/) - Business logic
- Handler (internal/handler/) - HTTP layer with Echo
- Templates (web/templates/pages/) - Templ templates with HTMX

Be specific with code structure, function signatures, and SQL schemas. This plan will be used to automatically generate the implementation.

Here is the selected feature:`
}

func implementationPrompt() string {
	return `Based on the following design plan, generate the complete implementation.

Generate the complete implementation including:
1. Migration SQL file
2. Model Go file
3. Repository Go file
4. Service Go file
5. Handler Go file
6. Template file structure description

For each file, provide:
- File path
- Complete file content
- Any integration notes

Follow existing patterns in the codebase. Use raw SQL (no ORM), context.Context for all DB calls, and QueryRowContext with RETURNING for inserts.

Provide the implementation as structured code blocks with file paths.

Here is the design plan:`
}

func testingPrompt() string {
	return `Generate comprehensive tests for the implementation from the previous phase.

Requirements:
- Repository tests using testutil.NewTestDB(t) for in-memory SQLite
- Service tests with mocked dependencies where appropriate
- Handler integration tests using httptest
- Each test must be independent (fresh DB per test)
- Use valid CHECK constraint values in test fixtures
- Follow existing test patterns in the codebase

Generate test files with complete test functions.

Here is the implementation to test:`
}

func reviewPrompt() string {
	return `Review the following implementation and tests for quality and correctness.

Review Criteria:
1. **Code Quality**: Is the code clean, readable, and follows Go conventions?
2. **Security**: Any SQL injection, XSS, or other OWASP vulnerabilities?
3. **Performance**: Any N+1 queries, missing indexes, or inefficient patterns?
4. **Architecture**: Does it follow the layered architecture pattern?
5. **Error Handling**: Are errors properly handled and logged?
6. **Testing**: Are the tests comprehensive and independent?

Provide a review summary with:
- Issues found (critical, major, minor)
- Recommendations
- Overall quality assessment

Here is the code to review:`
}

func summaryPrompt() string {
	return `You are generating a concise user-facing summary of a feature that was just built by an autonomous build pipeline.

Based on the code review output from the previous phase (which contains the full context of what was built), generate a brief, user-friendly summary that explains:

1. **Feature Name**: The name of the feature that was built
2. **What It Does**: A 1-2 sentence description of the feature's purpose
3. **How to Access It**: The URL path, page name, or navigation location where users can find this feature
4. **Key Endpoints/Pages**: List the main routes or pages added (e.g., "/feature-name", "/api/feature-name")
5. **Usage Example**: A brief example of how to use the feature

Format your response as a clean, readable summary using this exact structure:

## [Feature Name]

**What it does:** [1-2 sentences]

**How to access:** Navigate to [page/URL] from the sidebar, or visit [URL] directly.

**Key pages & endpoints:**
- [URL 1] - [description]
- [URL 2] - [description]

**Quick start:** [1-2 sentences on how to get started using the feature]

Keep it concise (under 200 words). Write for end users, not developers. Do not include implementation details, code snippets, or technical architecture information.

Here is the review output from the previous phase:`
}
