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

// CollisionService provides semantic impact analysis and predictive conflict detection
type CollisionService struct {
	collisionRepo *repository.CollisionRepo
	taskRepo      *repository.TaskRepo
	projectRepo   *repository.ProjectRepo
	llmConfigRepo *repository.LLMConfigRepo
	llmSvc        *LLMService
}

func NewCollisionService(
	collisionRepo *repository.CollisionRepo,
	taskRepo *repository.TaskRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
) *CollisionService {
	return &CollisionService{
		collisionRepo: collisionRepo,
		taskRepo:      taskRepo,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
	}
}

// SetLLMService sets the LLM service for AI analysis calls.
// Called after construction to avoid circular dependencies.
func (s *CollisionService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// AnalyzeTaskImpact performs AI-powered semantic analysis of what a task will touch
func (s *CollisionService) AnalyzeTaskImpact(ctx context.Context, taskID string) (*models.ImpactAnalysis, error) {
	log.Printf("[collision-svc] AnalyzeTaskImpact task=%s", taskID)

	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("task not found: %w", err)
	}

	project, err := s.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	// Get the default agent for AI analysis
	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default agent configured for analysis: %w", err)
	}

	// Build the analysis prompt
	prompt := buildImpactAnalysisPrompt(task.Title, task.Prompt, project.Name, project.RepoPath)

	// Call AI for analysis
	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	// Parse the AI response
	response, err := parseImpactAnalysisResponse(output)
	if err != nil {
		log.Printf("[collision-svc] AnalyzeTaskImpact failed to parse AI response, using raw: %v", err)
		response = &models.ImpactAnalysisResponse{
			Summary:    output,
			Confidence: 0.3,
		}
	}

	// Create impact analysis record
	ia := &models.ImpactAnalysis{
		TaskID:        taskID,
		ProjectID:     task.ProjectID,
		ImpactSummary: response.Summary,
		Confidence:    response.Confidence,
		AnalysisModel: agent.Model,
	}
	if err := ia.SetFiles(response.Files); err != nil {
		return nil, err
	}
	if err := ia.SetAPIs(response.APIs); err != nil {
		return nil, err
	}
	if err := ia.SetSchemas(response.Schemas); err != nil {
		return nil, err
	}
	if err := ia.SetComponents(response.Components); err != nil {
		return nil, err
	}

	if err := s.collisionRepo.CreateImpactAnalysis(ctx, ia); err != nil {
		return nil, fmt.Errorf("saving impact analysis: %w", err)
	}

	log.Printf("[collision-svc] AnalyzeTaskImpact completed task=%s files=%d apis=%d schemas=%d components=%d confidence=%.2f",
		taskID, len(response.Files), len(response.APIs), len(response.Schemas), len(response.Components), response.Confidence)

	return ia, nil
}

// DetectConflicts compares impact analyses across pending/running tasks to find conflicts
func (s *CollisionService) DetectConflicts(ctx context.Context, projectID string) ([]models.ConflictPrediction, error) {
	log.Printf("[collision-svc] DetectConflicts project=%s", projectID)

	analyses, err := s.collisionRepo.ListImpactAnalysesByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing impact analyses: %w", err)
	}

	if len(analyses) < 2 {
		log.Printf("[collision-svc] DetectConflicts only %d analyses, skipping", len(analyses))
		return nil, nil
	}

	var newConflicts []models.ConflictPrediction

	// Compare every pair of analyses
	for i := 0; i < len(analyses); i++ {
		for j := i + 1; j < len(analyses); j++ {
			a := analyses[i]
			b := analyses[j]

			results := compareImpactAnalyses(&a, &b)
			for _, result := range results {
				if !result.HasConflict {
					continue
				}

				// Check if this conflict already exists
				exists, err := s.collisionRepo.ExistingConflict(ctx, a.TaskID, b.TaskID, result.ConflictType)
				if err != nil {
					log.Printf("[collision-svc] DetectConflicts error checking existing conflict: %v", err)
					continue
				}
				if exists {
					continue
				}

				cp := models.ConflictPrediction{
					ProjectID:          projectID,
					TaskAID:            a.TaskID,
					TaskBID:            b.TaskID,
					ConflictType:       result.ConflictType,
					Severity:           result.Severity,
					Description:        result.Description,
					ResolutionStrategy: result.ResolutionStrategy,
					Status:             models.ConflictDetected,
				}
				if err := cp.SetOverlappingResources(result.OverlappingResources); err != nil {
					log.Printf("[collision-svc] DetectConflicts error setting resources: %v", err)
					continue
				}

				if err := s.collisionRepo.CreateConflictPrediction(ctx, &cp); err != nil {
					log.Printf("[collision-svc] DetectConflicts error creating prediction: %v", err)
					continue
				}

				newConflicts = append(newConflicts, cp)
				log.Printf("[collision-svc] DetectConflicts found %s conflict between task=%s and task=%s severity=%s",
					result.ConflictType, a.TaskID, b.TaskID, result.Severity)
			}
		}
	}

	return newConflicts, nil
}

// RecommendExecutionOrder suggests optimal task execution order to minimize conflicts
func (s *CollisionService) RecommendExecutionOrder(ctx context.Context, projectID string) (*models.ExecutionOrderRecommendation, error) {
	log.Printf("[collision-svc] RecommendExecutionOrder project=%s", projectID)

	// Get pending tasks in active category
	tasks, err := s.taskRepo.ListByProject(ctx, projectID, string(models.CategoryActive))
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	// Filter to pending only
	var pendingTasks []models.Task
	for _, t := range tasks {
		if t.Status == models.StatusPending {
			pendingTasks = append(pendingTasks, t)
		}
	}

	if len(pendingTasks) < 2 {
		return nil, nil
	}

	// Get active conflicts for the project
	conflicts, err := s.collisionRepo.ListActiveConflicts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing conflicts: %w", err)
	}

	// Build conflict graph to determine optimal ordering
	ordering, batchGroups, reasoning := computeExecutionOrder(pendingTasks, conflicts)

	rec := &models.ExecutionOrderRecommendation{
		ProjectID:     projectID,
		Reasoning:     reasoning,
		ConflictCount: len(conflicts),
		Status:        models.RecommendationPending,
		ExpiresAt:     time.Now().UTC().Add(1 * time.Hour),
	}
	if err := rec.SetTaskIDs(ordering); err != nil {
		return nil, err
	}
	if err := rec.SetBatchGroups(batchGroups); err != nil {
		return nil, err
	}

	if err := s.collisionRepo.CreateRecommendation(ctx, rec); err != nil {
		return nil, fmt.Errorf("saving recommendation: %w", err)
	}

	log.Printf("[collision-svc] RecommendExecutionOrder created rec=%s tasks=%d batches=%d conflicts=%d",
		rec.ID, len(ordering), len(batchGroups), len(conflicts))

	return rec, nil
}

// GetCollisionReport generates a full collision report for a project
func (s *CollisionService) GetCollisionReport(ctx context.Context, projectID string) (*models.CollisionReport, error) {
	analyses, err := s.collisionRepo.ListImpactAnalysesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}

	conflicts, err := s.collisionRepo.ListActiveConflicts(ctx, projectID)
	if err != nil {
		return nil, err
	}

	rec, err := s.collisionRepo.GetLatestRecommendation(ctx, projectID)
	if err != nil {
		return nil, err
	}

	return &models.CollisionReport{
		ProjectID:      projectID,
		Analyses:       analyses,
		Conflicts:      conflicts,
		Recommendation: rec,
		GeneratedAt:    time.Now().UTC(),
	}, nil
}

// GetImpactAnalysis returns the latest impact analysis for a task
func (s *CollisionService) GetImpactAnalysis(ctx context.Context, taskID string) (*models.ImpactAnalysis, error) {
	return s.collisionRepo.GetImpactAnalysisByTaskID(ctx, taskID)
}

// GetConflictsForTask returns all active conflicts involving a task
func (s *CollisionService) GetConflictsForTask(ctx context.Context, taskID string) ([]models.ConflictPrediction, error) {
	return s.collisionRepo.ListConflictsForTask(ctx, taskID)
}

// UpdateConflictStatus updates the status of a conflict prediction
func (s *CollisionService) UpdateConflictStatus(ctx context.Context, id string, status models.ConflictStatus) error {
	return s.collisionRepo.UpdateConflictStatus(ctx, id, status)
}

// RecordConflict records an actual conflict that occurred for learning
func (s *CollisionService) RecordConflict(ctx context.Context, history *models.ConflictHistory) error {
	return s.collisionRepo.CreateConflictHistory(ctx, history)
}

// GetConflictHistory returns conflict history for a project
func (s *CollisionService) GetConflictHistory(ctx context.Context, projectID string, limit int) ([]models.ConflictHistory, error) {
	return s.collisionRepo.ListConflictHistory(ctx, projectID, limit)
}

// GetPredictionAccuracy returns the prediction accuracy stats
func (s *CollisionService) GetPredictionAccuracy(ctx context.Context, projectID string) (predicted int, total int, err error) {
	return s.collisionRepo.PredictionAccuracy(ctx, projectID)
}

// AcceptRecommendation marks an execution order recommendation as accepted
func (s *CollisionService) AcceptRecommendation(ctx context.Context, id string) error {
	return s.collisionRepo.UpdateRecommendationStatus(ctx, id, models.RecommendationAccepted)
}

// RejectRecommendation marks an execution order recommendation as rejected
func (s *CollisionService) RejectRecommendation(ctx context.Context, id string) error {
	return s.collisionRepo.UpdateRecommendationStatus(ctx, id, models.RecommendationRejected)
}

// GetLatestRecommendation returns the latest pending recommendation for a project
func (s *CollisionService) GetLatestRecommendation(ctx context.Context, projectID string) (*models.ExecutionOrderRecommendation, error) {
	return s.collisionRepo.GetLatestRecommendation(ctx, projectID)
}

// --- Internal helpers ---

func buildImpactAnalysisPrompt(title, prompt, projectName, repoPath string) string {
	return fmt.Sprintf(`Analyze the following task and predict what files, APIs, database schemas, and system components it will likely modify. Respond ONLY with a JSON object in the exact format below, no other text.

Task Title: %s
Task Prompt: %s
Project: %s
Repository Path: %s

Respond with this exact JSON format:
{
  "files": ["path/to/file1.go", "path/to/file2.go"],
  "apis": ["/api/endpoint1", "/api/endpoint2"],
  "schemas": ["table_name.column", "other_table"],
  "components": ["service_name", "handler_name"],
  "summary": "Brief description of the task's impact scope",
  "confidence": 0.7
}

Rules:
- "files" should list specific file paths relative to the repo root that will likely be modified
- "apis" should list REST API endpoints that will be affected
- "schemas" should list database tables or columns that will be modified
- "components" should list high-level system components (services, handlers, templates, etc.)
- "confidence" should be 0.0-1.0 reflecting how confident you are in this analysis
- Be specific rather than general — list actual expected file paths
- Consider both direct modifications and ripple effects (e.g., modifying a model may require updating its repository, service, handler, and templates)
- Return ONLY the JSON object, no markdown fences or explanation`, title, prompt, projectName, repoPath)
}

func parseImpactAnalysisResponse(output string) (*models.ImpactAnalysisResponse, error) {
	jsonStr := util.ExtractJSONObject(output)
	if jsonStr == "" {
		return nil, fmt.Errorf("parsing response JSON: no JSON object found")
	}

	var response models.ImpactAnalysisResponse
	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		return nil, fmt.Errorf("parsing response JSON: %w", err)
	}

	// Clamp confidence to 0-1
	if response.Confidence < 0 {
		response.Confidence = 0
	}
	if response.Confidence > 1 {
		response.Confidence = 1
	}

	return &response, nil
}

// compareImpactAnalyses compares two impact analyses to find overlapping resources
func compareImpactAnalyses(a, b *models.ImpactAnalysis) []models.ConflictCheckResult {
	var results []models.ConflictCheckResult

	// Compare files
	filesA, _ := a.ParseFiles()
	filesB, _ := b.ParseFiles()
	if overlap := findOverlap(filesA, filesB); len(overlap) > 0 {
		severity := classifySeverity(len(overlap), len(filesA)+len(filesB))
		results = append(results, models.ConflictCheckResult{
			HasConflict:          true,
			ConflictType:         models.ConflictTypeFile,
			Severity:             severity,
			Description:          fmt.Sprintf("Both tasks modify %d shared files: %s", len(overlap), strings.Join(overlap, ", ")),
			OverlappingResources: overlap,
			ResolutionStrategy:   resolveFileConflict(severity),
		})
	}

	// Compare APIs
	apisA, _ := a.ParseAPIs()
	apisB, _ := b.ParseAPIs()
	if overlap := findOverlap(apisA, apisB); len(overlap) > 0 {
		severity := classifySeverity(len(overlap), len(apisA)+len(apisB))
		results = append(results, models.ConflictCheckResult{
			HasConflict:          true,
			ConflictType:         models.ConflictTypeAPI,
			Severity:             severity,
			Description:          fmt.Sprintf("Both tasks affect %d shared API endpoints: %s", len(overlap), strings.Join(overlap, ", ")),
			OverlappingResources: overlap,
			ResolutionStrategy:   "Execute sequentially to prevent API contract conflicts",
		})
	}

	// Compare schemas
	schemasA, _ := a.ParseSchemas()
	schemasB, _ := b.ParseSchemas()
	if overlap := findOverlap(schemasA, schemasB); len(overlap) > 0 {
		results = append(results, models.ConflictCheckResult{
			HasConflict:          true,
			ConflictType:         models.ConflictTypeSchema,
			Severity:             models.SeverityCritical,
			Description:          fmt.Sprintf("Both tasks modify %d shared database schemas: %s", len(overlap), strings.Join(overlap, ", ")),
			OverlappingResources: overlap,
			ResolutionStrategy:   "MUST execute sequentially — concurrent schema changes can corrupt data or cause migration conflicts",
		})
	}

	// Compare components
	componentsA, _ := a.ParseComponents()
	componentsB, _ := b.ParseComponents()
	if overlap := findOverlap(componentsA, componentsB); len(overlap) > 0 {
		severity := classifySeverity(len(overlap), len(componentsA)+len(componentsB))
		results = append(results, models.ConflictCheckResult{
			HasConflict:          true,
			ConflictType:         models.ConflictTypeComponent,
			Severity:             severity,
			Description:          fmt.Sprintf("Both tasks affect %d shared components: %s", len(overlap), strings.Join(overlap, ", ")),
			OverlappingResources: overlap,
			ResolutionStrategy:   resolveComponentConflict(severity),
		})
	}

	return results
}

func findOverlap(a, b []string) []string {
	set := make(map[string]bool)
	for _, s := range a {
		set[strings.ToLower(s)] = true
	}
	var overlap []string
	seen := make(map[string]bool)
	for _, s := range b {
		lower := strings.ToLower(s)
		if set[lower] && !seen[lower] {
			overlap = append(overlap, s)
			seen[lower] = true
		}
	}
	return overlap
}

func classifySeverity(overlapCount, totalCount int) models.ConflictSeverity {
	if totalCount == 0 {
		return models.SeverityLow
	}
	ratio := float64(overlapCount) / float64(totalCount)
	switch {
	case overlapCount >= 5 || ratio > 0.5:
		return models.SeverityCritical
	case overlapCount >= 3 || ratio > 0.3:
		return models.SeverityHigh
	case overlapCount >= 2 || ratio > 0.15:
		return models.SeverityMedium
	default:
		return models.SeverityLow
	}
}

func resolveFileConflict(severity models.ConflictSeverity) string {
	switch severity {
	case models.SeverityCritical, models.SeverityHigh:
		return "Execute sequentially — high file overlap risks merge conflicts"
	case models.SeverityMedium:
		return "Consider executing sequentially or batch these tasks together"
	default:
		return "Low overlap — can likely execute in parallel with minor conflict risk"
	}
}

func resolveComponentConflict(severity models.ConflictSeverity) string {
	switch severity {
	case models.SeverityCritical, models.SeverityHigh:
		return "Execute sequentially — extensive component overlap requires coordination"
	default:
		return "Consider batching these related tasks together for efficiency"
	}
}

// computeExecutionOrder determines optimal task ordering based on conflicts.
// Tasks with conflicts are separated into sequential groups; non-conflicting tasks are batched.
func computeExecutionOrder(tasks []models.Task, conflicts []models.ConflictPrediction) (ordering []string, batchGroups [][]string, reasoning string) {
	if len(tasks) == 0 {
		return nil, nil, ""
	}

	// Build a conflict adjacency set
	conflictPairs := make(map[string]map[string]bool)
	conflictSeverity := make(map[string]models.ConflictSeverity)
	for _, c := range conflicts {
		if conflictPairs[c.TaskAID] == nil {
			conflictPairs[c.TaskAID] = make(map[string]bool)
		}
		if conflictPairs[c.TaskBID] == nil {
			conflictPairs[c.TaskBID] = make(map[string]bool)
		}
		conflictPairs[c.TaskAID][c.TaskBID] = true
		conflictPairs[c.TaskBID][c.TaskAID] = true

		// Track highest severity
		key := c.TaskAID + ":" + c.TaskBID
		if existing, ok := conflictSeverity[key]; !ok || severityOrder(c.Severity) > severityOrder(existing) {
			conflictSeverity[key] = c.Severity
		}
	}

	// Build task ID set and priority map
	taskMap := make(map[string]models.Task)
	for _, t := range tasks {
		taskMap[t.ID] = t
	}

	// Greedy graph coloring to find batches of non-conflicting tasks
	assigned := make(map[string]bool)
	var batches [][]string
	var reasons []string

	for len(assigned) < len(tasks) {
		var batch []string
		batchConflicts := make(map[string]bool)

		// Sort by priority (highest first), then by conflict count (fewer conflicts first)
		for _, t := range sortByPriority(tasks) {
			if assigned[t.ID] {
				continue
			}
			if batchConflicts[t.ID] {
				continue
			}
			batch = append(batch, t.ID)
			assigned[t.ID] = true
			// Mark all tasks that conflict with this one
			for conflictID := range conflictPairs[t.ID] {
				batchConflicts[conflictID] = true
			}
		}

		if len(batch) > 0 {
			batches = append(batches, batch)
		}
	}

	// Build flat ordering from batches
	for _, batch := range batches {
		ordering = append(ordering, batch...)
	}

	// Build reasoning
	if len(conflicts) == 0 {
		reasons = append(reasons, "No conflicts detected — all tasks can run in parallel")
	} else {
		reasons = append(reasons, fmt.Sprintf("Found %d conflicts across %d tasks", len(conflicts), len(tasks)))
		reasons = append(reasons, fmt.Sprintf("Organized into %d sequential batches to prevent conflicts", len(batches)))
		for i, batch := range batches {
			var names []string
			for _, id := range batch {
				if t, ok := taskMap[id]; ok {
					names = append(names, t.Title)
				}
			}
			reasons = append(reasons, fmt.Sprintf("Batch %d: %s", i+1, strings.Join(names, ", ")))
		}
	}

	reasoning = strings.Join(reasons, "\n")
	return ordering, batches, reasoning
}

func severityOrder(s models.ConflictSeverity) int {
	switch s {
	case models.SeverityCritical:
		return 3
	case models.SeverityHigh:
		return 2
	case models.SeverityMedium:
		return 1
	default:
		return 0
	}
}

func sortByPriority(tasks []models.Task) []models.Task {
	sorted := make([]models.Task, len(tasks))
	copy(sorted, tasks)
	// Simple insertion sort (small lists)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Priority > sorted[j-1].Priority; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}
