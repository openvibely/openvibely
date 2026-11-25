package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
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

	got, err := repo.GetInsight(ctx, project.ID, insight.ID)
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

func TestInsightsRepo_ListByType(t *testing.T) {
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
			ProjectID:  project.ID,
			Type:       tc.itype,
			Severity:   tc.severity,
			Status:     tc.status,
			Title:      tc.title,
			Evidence:   "{}",
			Confidence: 0.7,
		}
		if err := repo.CreateInsight(ctx, i); err != nil {
			t.Fatalf("create insight %s: %v", tc.title, err)
		}
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
	if err := repo.UpdateStatus(ctx, project.ID, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ := repo.GetInsight(ctx, project.ID, insight.ID)
	if got.Status != models.InsightStatusResolved {
		t.Errorf("status: got %q, want resolved", got.Status)
	}
	if got.ResolvedAt == nil {
		t.Error("resolved_at should be set when resolved")
	}

	// Delete
	if err := repo.DeleteInsight(ctx, project.ID, insight.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.GetInsight(ctx, project.ID, insight.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestInsightsRepo_ProjectScopedMutations(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := NewProjectRepo(db)
	repo := NewInsightsRepo(db)
	projectA := &models.Project{Name: "Project A"}
	projectB := &models.Project{Name: "Project B"}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}

	insight := &models.Insight{
		ProjectID:  projectB.ID,
		Type:       models.InsightBugPattern,
		Severity:   models.InsightSeverityHigh,
		Status:     models.InsightStatusNew,
		Title:      "Project B insight",
		Evidence:   "{}",
		Confidence: 0.8,
	}
	if err := repo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}

	if got, err := repo.GetInsight(ctx, projectA.ID, insight.ID); err != nil || got != nil {
		t.Fatalf("foreign insight lookup = %#v, err=%v; want not found", got, err)
	}
	before, err := repo.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || before == nil {
		t.Fatalf("get project B insight = %#v, err=%v", before, err)
	}
	if err := repo.UpdateStatus(ctx, projectA.ID, insight.ID, models.InsightStatusAccepted); !errors.Is(err, persistence.ErrInsightNotFound) {
		t.Fatalf("foreign status update error = %v, want %v", err, persistence.ErrInsightNotFound)
	}
	after, err := repo.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("foreign insight changed after rejected update: before=%#v after=%#v err=%v", before, after, err)
	}

	if err := repo.UpdateStatus(ctx, projectB.ID, insight.ID, models.InsightStatusAccepted); err != nil {
		t.Fatalf("same-project accept: %v", err)
	}
	if err := repo.UpdateStatus(ctx, projectB.ID, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("same-project resolve: %v", err)
	}
	resolved, err := repo.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || resolved == nil || resolved.Status != models.InsightStatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolved insight = %#v, err=%v", resolved, err)
	}
	resolvedAt := *resolved.ResolvedAt
	if err := repo.UpdateStatus(ctx, projectB.ID, insight.ID, models.InsightStatusRejected); err != nil {
		t.Fatalf("same-project reject after resolve: %v", err)
	}
	preserved, err := repo.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || preserved == nil || preserved.Status != models.InsightStatusRejected || preserved.ResolvedAt == nil || !preserved.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolved_at was not preserved: insight=%#v err=%v", preserved, err)
	}

	if err := repo.DeleteInsight(ctx, projectA.ID, insight.ID); !errors.Is(err, persistence.ErrInsightNotFound) {
		t.Fatalf("foreign insight delete error = %v, want %v", err, persistence.ErrInsightNotFound)
	}
	if stillThere, err := repo.GetInsight(ctx, projectB.ID, insight.ID); err != nil || stillThere == nil {
		t.Fatalf("foreign insight disappeared after rejected delete: %#v err=%v", stillThere, err)
	}
	if err := repo.DeleteInsight(ctx, projectB.ID, insight.ID); err != nil {
		t.Fatalf("same-project insight delete: %v", err)
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

	reports, err := repo.ListReports(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("report count: got %d, want 1", len(reports))
	}
	if reports[0].Summary != rpt.Summary {
		t.Error("summary mismatch")
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
		AuthMethod: models.AuthMethodAPIKey,
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
		AuthMethod: models.AuthMethodAPIKey,
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
