package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestInsightsRepo_CreateAndGet(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Insights"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insight := &models.Insight{
		ProjectID:   project.ID,
		Type:        models.InsightBugPattern,
		Severity:    models.InsightSeverityHigh,
		Status:      models.InsightStatusNew,
		Title:       "Recurring null pointer in handler",
		Description: "Multiple tasks have failed due to null pointer dereference in task handler",
		Evidence:    `{"fail_count": 5}`,
		Suggestion:  "Add nil check before accessing task.AgentID",
		Impact:      "Reduces task failure rate by ~20%",
		Confidence:  0.85,
	}

	if err := repo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}
	if insight.ID == "" {
		t.Fatal("expected insight ID to be set")
	}

	got, err := repo.GetInsight(ctx, insight.ID)
	if err != nil {
		t.Fatalf("get insight: %v", err)
	}
	if got == nil {
		t.Fatal("expected insight, got nil")
	}
	if got.Title != insight.Title {
		t.Errorf("title: got %q, want %q", got.Title, insight.Title)
	}
	if got.Type != models.InsightBugPattern {
		t.Errorf("type: got %q, want %q", got.Type, models.InsightBugPattern)
	}
	if got.Confidence != 0.85 {
		t.Errorf("confidence: got %f, want 0.85", got.Confidence)
	}
}

func TestInsightsRepo_ListByStatusAndType(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test List"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create insights of different types and statuses
	for _, tc := range []struct {
		title    string
		itype    models.InsightType
		severity models.InsightSeverity
		status   models.InsightStatus
	}{
		{"Bug 1", models.InsightBugPattern, models.InsightSeverityHigh, models.InsightStatusNew},
		{"Bug 2", models.InsightBugPattern, models.InsightSeverityMedium, models.InsightStatusNew},
		{"Tech Debt 1", models.InsightTechDebt, models.InsightSeverityLow, models.InsightStatusAccepted},
		{"Optimization 1", models.InsightOptimization, models.InsightSeverityMedium, models.InsightStatusNew},
	} {
		i := &models.Insight{
			ProjectID: project.ID,
			Type:      tc.itype,
			Severity:  tc.severity,
			Status:    tc.status,
			Title:     tc.title,
			Evidence:  "{}",
			Confidence: 0.7,
		}
		if err := repo.CreateInsight(ctx, i); err != nil {
			t.Fatalf("create insight %s: %v", tc.title, err)
		}
	}

	// List new insights
	newInsights, err := repo.ListByStatus(ctx, project.ID, models.InsightStatusNew, 10)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(newInsights) != 3 {
		t.Errorf("new insights count: got %d, want 3", len(newInsights))
	}
	// Should be ordered by severity (high first)
	if len(newInsights) > 0 && newInsights[0].Severity != models.InsightSeverityHigh {
		t.Errorf("first insight should be high severity, got %s", newInsights[0].Severity)
	}

	// List by type
	bugInsights, err := repo.ListByType(ctx, project.ID, models.InsightBugPattern, 10)
	if err != nil {
		t.Fatalf("list by type: %v", err)
	}
	if len(bugInsights) != 2 {
		t.Errorf("bug insights count: got %d, want 2", len(bugInsights))
	}
}

func TestInsightsRepo_UpdateStatusAndDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Status"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insight := &models.Insight{
		ProjectID:  project.ID,
		Type:       models.InsightTechDebt,
		Severity:   models.InsightSeverityMedium,
		Status:     models.InsightStatusNew,
		Title:      "Refactor auth module",
		Evidence:   "{}",
		Confidence: 0.75,
	}
	if err := repo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}

	// Update to resolved
	if err := repo.UpdateStatus(ctx, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ := repo.GetInsight(ctx, insight.ID)
	if got.Status != models.InsightStatusResolved {
		t.Errorf("status: got %q, want resolved", got.Status)
	}
	if got.ResolvedAt == nil {
		t.Error("resolved_at should be set when resolved")
	}

	// Delete
	if err := repo.DeleteInsight(ctx, insight.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.GetInsight(ctx, insight.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestInsightsRepo_CountsAndAvgConfidence(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Counts"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	for _, conf := range []float64{0.6, 0.8, 1.0} {
		i := &models.Insight{
			ProjectID:  project.ID,
			Type:       models.InsightBugPattern,
			Severity:   models.InsightSeverityMedium,
			Status:     models.InsightStatusNew,
			Title:      "Test",
			Evidence:   "{}",
			Confidence: conf,
		}
		if err := repo.CreateInsight(ctx, i); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	counts, err := repo.CountByStatus(ctx, project.ID)
	if err != nil {
		t.Fatalf("count by status: %v", err)
	}
	if counts["new"] != 3 {
		t.Errorf("new count: got %d, want 3", counts["new"])
	}

	avg, err := repo.AvgConfidence(ctx, project.ID)
	if err != nil {
		t.Fatalf("avg confidence: %v", err)
	}
	if avg < 0.79 || avg > 0.81 {
		t.Errorf("avg confidence: got %f, want ~0.8", avg)
	}
}

func TestInsightsRepo_ExistingInsight(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Dedup"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insight := &models.Insight{
		ProjectID:  project.ID,
		Type:       models.InsightBugPattern,
		Severity:   models.InsightSeverityHigh,
		Status:     models.InsightStatusNew,
		Title:      "Duplicate Check",
		Evidence:   "{}",
		Confidence: 0.9,
	}
	if err := repo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create: %v", err)
	}

	exists, err := repo.ExistingInsight(ctx, project.ID, "Duplicate Check", models.InsightBugPattern)
	if err != nil {
		t.Fatalf("existing check: %v", err)
	}
	if !exists {
		t.Error("expected existing insight to be found")
	}

	exists, err = repo.ExistingInsight(ctx, project.ID, "Nonexistent", models.InsightBugPattern)
	if err != nil {
		t.Fatalf("existing check: %v", err)
	}
	if exists {
		t.Error("expected no existing insight")
	}
}

func TestInsightsRepo_Reports(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Reports"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	rpt := &models.InsightReport{
		ProjectID:   project.ID,
		ReportDate:  "2026-02-28",
		Summary:     "# Daily Insights\n\nFound 3 new insights.",
		InsightIDs:  `["abc","def"]`,
		Stats:       `{"total": 3}`,
		AnalysisLog: "Analyzed bug fixes, TODOs, execution logs",
	}
	if err := repo.CreateReport(ctx, rpt); err != nil {
		t.Fatalf("create report: %v", err)
	}
	if rpt.ID == "" {
		t.Fatal("expected report ID")
	}

	latest, err := repo.GetLatestReport(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest report: %v", err)
	}
	if latest == nil {
		t.Fatal("expected latest report")
	}
	if latest.Summary != rpt.Summary {
		t.Errorf("summary mismatch")
	}

	reports, err := repo.ListReports(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 1 {
		t.Errorf("report count: got %d, want 1", len(reports))
	}
}

func TestInsightsRepo_HealthChecks(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Health Checks"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	hc := &models.HealthCheck{
		ProjectID:        project.ID,
		Grade:            "B+",
		Strengths:        "Good completion rate, consistent activity",
		Improvements:     "Too many urgent tasks, backlog growing",
		Assessment:       "Solid project with room for improvement",
		HowToImprove:     "Focus on reducing urgent tasks and clearing backlog",
		TasksTotal:       50,
		TasksCompleted:   35,
		TasksFailed:      5,
		TasksPending:     10,
		BacklogSize:      20,
		AvgCompletionPct: 70.0,
	}
	if err := repo.CreateHealthCheck(ctx, hc); err != nil {
		t.Fatalf("create health check: %v", err)
	}
	if hc.ID == "" {
		t.Fatal("expected health check ID to be set")
	}

	// Get latest
	latest, err := repo.GetLatestHealthCheck(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected latest health check")
	}
	if latest.Grade != "B+" {
		t.Errorf("grade: got %q, want %q", latest.Grade, "B+")
	}
	if latest.TasksTotal != 50 {
		t.Errorf("tasks_total: got %d, want 50", latest.TasksTotal)
	}
	if latest.AvgCompletionPct != 70.0 {
		t.Errorf("avg_completion_pct: got %f, want 70.0", latest.AvgCompletionPct)
	}

	// Create a second health check
	hc2 := &models.HealthCheck{
		ProjectID:        project.ID,
		Grade:            "A-",
		Strengths:        "Improved velocity",
		Improvements:     "Still needs backlog work",
		Assessment:       "Great progress",
		HowToImprove:     "Keep shipping",
		TasksTotal:       60,
		TasksCompleted:   50,
		TasksFailed:      2,
		TasksPending:     8,
		BacklogSize:      15,
		AvgCompletionPct: 83.3,
	}
	if err := repo.CreateHealthCheck(ctx, hc2); err != nil {
		t.Fatalf("create health check 2: %v", err)
	}

	// Latest should be the newer one
	latest, err = repo.GetLatestHealthCheck(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest 2: %v", err)
	}
	if latest.Grade != "A-" {
		t.Errorf("latest grade: got %q, want %q", latest.Grade, "A-")
	}

	// List all
	checks, err := repo.ListHealthChecks(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list health checks: %v", err)
	}
	if len(checks) != 2 {
		t.Errorf("health check count: got %d, want 2", len(checks))
	}
	// Should be ordered newest first
	if len(checks) > 0 && checks[0].Grade != "A-" {
		t.Errorf("first check grade: got %q, want A-", checks[0].Grade)
	}

	// Get latest for nonexistent project
	none, err := repo.GetLatestHealthCheck(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get latest nonexistent: %v", err)
	}
	if none != nil {
		t.Error("expected nil for nonexistent project")
	}
}

func TestInsightsRepo_ProjectTaskStats(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := &models.Project{Name: "Test Stats"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks in various states
	for _, tc := range []struct {
		title    string
		category models.TaskCategory
		status   models.TaskStatus
	}{
		{"Task 1", models.CategoryActive, models.StatusCompleted},
		{"Task 2", models.CategoryActive, models.StatusCompleted},
		{"Task 3", models.CategoryActive, models.StatusFailed},
		{"Task 4", models.CategoryActive, models.StatusPending},
		{"Task 5", models.CategoryBacklog, models.StatusPending},
		{"Task 6", models.CategoryBacklog, models.StatusPending},
	} {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     tc.title,
			Prompt:    "test prompt",
			Category:  tc.category,
			Status:    tc.status,
			Priority:  3,
			Tag:       models.TagNone,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", tc.title, err)
		}
	}

	total, completed, failed, pending, backlog, err := repo.GetProjectTaskStats(ctx, project.ID)
	if err != nil {
		t.Fatalf("get task stats: %v", err)
	}
	if total != 6 {
		t.Errorf("total: got %d, want 6", total)
	}
	if completed != 2 {
		t.Errorf("completed: got %d, want 2", completed)
	}
	if failed != 1 {
		t.Errorf("failed: got %d, want 1", failed)
	}
	if pending != 1 {
		t.Errorf("pending: got %d, want 1", pending)
	}
	if backlog != 2 {
		t.Errorf("backlog: got %d, want 2", backlog)
	}
}

func TestInsightsRepo_IdeaGrades(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Idea Grades"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ig := &models.IdeaGrade{
		ProjectID:      project.ID,
		Grade:          "B+",
		Summary:        "Good ideas with room for improvement",
		Strengths:      "Clear task titles, good variety",
		Improvements:   "Some prompts are too vague",
		HowToNextGrade: "Add more detail to task prompts",
		NextGrade:      "A-",
		TasksEvaluated: 25,
		ClarityScore:   75.0,
		AmbitionScore:  80.0,
		FollowThrough:  70.0,
		DiversityScore: 85.0,
		StrategyScore:  65.0,
	}
	if err := repo.CreateIdeaGrade(ctx, ig); err != nil {
		t.Fatalf("create idea grade: %v", err)
	}
	if ig.ID == "" {
		t.Fatal("expected idea grade ID to be set")
	}

	// Get latest
	latest, err := repo.GetLatestIdeaGrade(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected latest idea grade")
	}
	if latest.Grade != "B+" {
		t.Errorf("grade: got %q, want B+", latest.Grade)
	}
	if latest.TasksEvaluated != 25 {
		t.Errorf("tasks_evaluated: got %d, want 25", latest.TasksEvaluated)
	}
	if latest.ClarityScore != 75.0 {
		t.Errorf("clarity_score: got %f, want 75.0", latest.ClarityScore)
	}
	if latest.NextGrade != "A-" {
		t.Errorf("next_grade: got %q, want A-", latest.NextGrade)
	}

	// Create a second grade
	ig2 := &models.IdeaGrade{
		ProjectID:      project.ID,
		Grade:          "A-",
		Summary:        "Improved significantly",
		Strengths:      "Much better prompts",
		Improvements:   "Could be more ambitious",
		HowToNextGrade: "Take on bigger challenges",
		NextGrade:      "A",
		TasksEvaluated: 30,
		ClarityScore:   88.0,
		AmbitionScore:  72.0,
		FollowThrough:  85.0,
		DiversityScore: 80.0,
		StrategyScore:  78.0,
	}
	if err := repo.CreateIdeaGrade(ctx, ig2); err != nil {
		t.Fatalf("create idea grade 2: %v", err)
	}

	// Latest should be the newer one
	latest, err = repo.GetLatestIdeaGrade(ctx, project.ID)
	if err != nil {
		t.Fatalf("get latest 2: %v", err)
	}
	if latest.Grade != "A-" {
		t.Errorf("latest grade: got %q, want A-", latest.Grade)
	}

	// List all
	grades, err := repo.ListIdeaGrades(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list idea grades: %v", err)
	}
	if len(grades) != 2 {
		t.Errorf("idea grade count: got %d, want 2", len(grades))
	}
	// Should be ordered newest first
	if len(grades) > 0 && grades[0].Grade != "A-" {
		t.Errorf("first grade: got %q, want A-", grades[0].Grade)
	}

	// Nonexistent project
	none, err := repo.GetLatestIdeaGrade(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get latest nonexistent: %v", err)
	}
	if none != nil {
		t.Error("expected nil for nonexistent project")
	}
}

func TestInsightsRepo_Knowledge(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)

	project := &models.Project{Name: "Test Knowledge"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	entry := &models.KnowledgeEntry{
		ProjectID: project.ID,
		Topic:     "Why we chose SQLite",
		Content:   "SQLite was chosen for simplicity and single-binary deployment",
		Source:    "commit",
		SourceRef: "abc123",
		Tags:      `["architecture","database"]`,
	}
	if err := repo.CreateKnowledge(ctx, entry); err != nil {
		t.Fatalf("create knowledge: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected knowledge ID")
	}

	got, err := repo.GetKnowledge(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get knowledge: %v", err)
	}
	if got.Topic != entry.Topic {
		t.Errorf("topic: got %q, want %q", got.Topic, entry.Topic)
	}

	// Search
	results, err := repo.SearchKnowledge(ctx, project.ID, "SQLite", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("search results: got %d, want 1", len(results))
	}

	results, err = repo.SearchKnowledge(ctx, project.ID, "nonexistent", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("search results: got %d, want 0", len(results))
	}

	// Delete
	if err := repo.DeleteKnowledge(ctx, entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.GetKnowledge(ctx, entry.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestInsightsRepo_GetFailedTaskPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	llmRepo := NewLLMConfigRepo(db)

	project := &models.Project{Name: "Test Failed Patterns"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:       "test-agent",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-3",
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Task A: 3 failures with different errors
	taskA := &models.Task{
		ProjectID: project.ID, Title: "Flaky task A", Prompt: "do A",
		Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Tag: models.TagNone,
	}
	if err := taskRepo.Create(ctx, taskA); err != nil {
		t.Fatalf("create task A: %v", err)
	}
	for i, errMsg := range []string{"old error", "middle error", "latest error for A"} {
		exec := &models.Execution{TaskID: taskA.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "p"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec A-%d: %v", i, err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); err != nil {
			t.Fatalf("complete exec A-%d: %v", i, err)
		}
	}

	// Task B: 2 failures
	taskB := &models.Task{
		ProjectID: project.ID, Title: "Flaky task B", Prompt: "do B",
		Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Tag: models.TagNone,
	}
	if err := taskRepo.Create(ctx, taskB); err != nil {
		t.Fatalf("create task B: %v", err)
	}
	for i := 0; i < 2; i++ {
		exec := &models.Execution{TaskID: taskB.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "p"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec B-%d: %v", i, err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", fmt.Sprintf("error B-%d", i), 0, 0); err != nil {
			t.Fatalf("complete exec B-%d: %v", i, err)
		}
	}

	// Task C: 1 failure (below threshold of 2)
	taskC := &models.Task{
		ProjectID: project.ID, Title: "Rare fail C", Prompt: "do C",
		Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Tag: models.TagNone,
	}
	if err := taskRepo.Create(ctx, taskC); err != nil {
		t.Fatalf("create task C: %v", err)
	}
	execC := &models.Execution{TaskID: taskC.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "p"}
	if err := execRepo.Create(ctx, execC); err != nil {
		t.Fatalf("create exec C: %v", err)
	}
	if err := execRepo.Complete(ctx, execC.ID, models.ExecFailed, "", "only once", 0, 0); err != nil {
		t.Fatalf("complete exec C: %v", err)
	}

	// minFailures=2: should return A (3 failures) and B (2 failures), not C
	results, err := repo.GetFailedTaskPatterns(ctx, project.ID, 2)
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Results should be ordered by fail_count DESC, so A first
	if results[0].Title != "Flaky task A" {
		t.Errorf("first result title: got %q, want %q", results[0].Title, "Flaky task A")
	}
	if results[0].FailCount != 3 {
		t.Errorf("first result fail_count: got %d, want 3", results[0].FailCount)
	}
	if results[0].LastError != "latest error for A" {
		t.Errorf("first result last_error: got %q, want %q", results[0].LastError, "latest error for A")
	}

	if results[1].Title != "Flaky task B" {
		t.Errorf("second result title: got %q, want %q", results[1].Title, "Flaky task B")
	}
	if results[1].FailCount != 2 {
		t.Errorf("second result fail_count: got %d, want 2", results[1].FailCount)
	}

	// minFailures=4: no tasks should match
	results, err = repo.GetFailedTaskPatterns(ctx, project.ID, 4)
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns high threshold: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for high threshold, got %d", len(results))
	}
}

func TestInsightsRepo_GetSlowExecutions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	llmRepo := NewLLMConfigRepo(db)

	project := &models.Project{Name: "Test Slow Executions"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:       "slow-agent",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-3",
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task1 := &models.Task{
		ProjectID: project.ID, Title: "Slow task", Prompt: "do slow",
		Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Tag: models.TagNone,
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatalf("create task1: %v", err)
	}

	task2 := &models.Task{
		ProjectID: project.ID, Title: "Fast task", Prompt: "do fast",
		Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, Tag: models.TagNone,
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatalf("create task2: %v", err)
	}

	// Create a slow execution (120 seconds) by manipulating started_at
	execSlow := &models.Execution{TaskID: task1.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "p"}
	if err := execRepo.Create(ctx, execSlow); err != nil {
		t.Fatalf("create slow exec: %v", err)
	}
	// Set started_at to 120 seconds ago, then complete now
	_, err := db.ExecContext(ctx, `UPDATE executions SET started_at = datetime('now', '-120 seconds') WHERE id = ?`, execSlow.ID)
	if err != nil {
		t.Fatalf("update slow exec started_at: %v", err)
	}
	if err := execRepo.Complete(ctx, execSlow.ID, models.ExecCompleted, "done", "", 100, 120000); err != nil {
		t.Fatalf("complete slow exec: %v", err)
	}

	// Create a fast execution (5 seconds)
	execFast := &models.Execution{TaskID: task2.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "p"}
	if err := execRepo.Create(ctx, execFast); err != nil {
		t.Fatalf("create fast exec: %v", err)
	}
	_, err = db.ExecContext(ctx, `UPDATE executions SET started_at = datetime('now', '-5 seconds') WHERE id = ?`, execFast.ID)
	if err != nil {
		t.Fatalf("update fast exec started_at: %v", err)
	}
	if err := execRepo.Complete(ctx, execFast.ID, models.ExecCompleted, "done", "", 50, 5000); err != nil {
		t.Fatalf("complete fast exec: %v", err)
	}

	// minDurationSecs=60: should only return the slow execution
	results, err := repo.GetSlowExecutions(ctx, project.ID, 60, 10)
	if err != nil {
		t.Fatalf("GetSlowExecutions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 slow execution, got %d", len(results))
	}
	if results[0].TaskTitle != "Slow task" {
		t.Errorf("task title: got %q, want %q", results[0].TaskTitle, "Slow task")
	}
	if results[0].Duration < 100 {
		t.Errorf("duration should be >= 100 seconds, got %d", results[0].Duration)
	}
	if results[0].AgentName != "slow-agent" {
		t.Errorf("agent name: got %q, want %q", results[0].AgentName, "slow-agent")
	}

	// minDurationSecs=0: both executions should be returned
	results, err = repo.GetSlowExecutions(ctx, project.ID, 0, 10)
	if err != nil {
		t.Fatalf("GetSlowExecutions all: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for low threshold, got %d", len(results))
	}
	// Should be ordered by duration DESC
	if results[0].Duration < results[1].Duration {
		t.Errorf("results should be ordered by duration DESC: %d < %d", results[0].Duration, results[1].Duration)
	}

	// Test limit
	results, err = repo.GetSlowExecutions(ctx, project.ID, 0, 1)
	if err != nil {
		t.Fatalf("GetSlowExecutions limit: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with limit=1, got %d", len(results))
	}
}
