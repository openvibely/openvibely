package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAnalyticsDashboardPeriodDoesNotResurrectHistoricalOutcomes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Period semantics", RepoPath: "/period-semantics"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	config := &models.LLMConfig{Name: "Model", Provider: models.ProviderTest, Model: "model"}
	if err := NewLLMConfigRepo(db).Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Historical outcome", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "work"}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	executions := NewExecutionRepo(db)
	old := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "old"}
	if err := executions.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, old.ID, models.ExecCompleted, "", "", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2025-12-01 10:00:00',completed_at='2025-12-01 10:01:00' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	goal := &models.TaskGoal{TaskID: task.ID, GoalID: "old-goal", Objective: "old", Status: models.TaskGoalStatusAchieved}
	if err := NewTaskGoalRepo(db).CreateOrReplace(ctx, goal); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at='2025-12-01 10:01:00',updated_at='2025-12-01 10:01:00' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	current := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "current"}
	if err := executions.Create(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-10 10:00:00' WHERE id=?`, current.ID); err != nil {
		t.Fatal(err)
	}

	dashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Current.FirstPass.Denominator != 0 || dashboard.Current.GoalAchievement.Denominator != 0 {
		t.Fatalf("historical outcomes leaked into current period: first=%+v goal=%+v", dashboard.Current.FirstPass, dashboard.Current.GoalAchievement)
	}
	if len(dashboard.RecentOutcomes) != 1 {
		t.Fatalf("recent evidence = %+v, want running task context", dashboard.RecentOutcomes)
	}
	evidence := dashboard.RecentOutcomes[0]
	if evidence.FirstPassEligible || evidence.GoalAchievementEligible || evidence.GoalAchievedInPeriod || evidence.PeriodCompletedCount != 0 {
		t.Fatalf("historical task context was incorrectly marked eligible for current KPI evidence: %+v", evidence)
	}
}

func TestAnalyticsDashboardWorkflowFilterReturnsLinkedTaskAndNodeEvidence(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger", "task": "task"})
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	insertAutomationHistoryInvocation(t, db, fixture, "inv-1", fixture.Nodes["trigger"], "failed", now.Add(-time.Hour), now, false)
	insertAutomationHistoryWorkItem(t, db, fixture, "analytics-work", "feature", "Feature", "blocked", now.Add(-time.Hour), time.Time{})
	insertAutomationHistoryActivity(t, db, fixture, "analytics-activity", "inv-1", "analytics-work", fixture.Nodes["task"], "task_execution", "failed", now.Add(-45*time.Minute), now.Add(-30*time.Minute), "failed")
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_edges (id,project_id,automation_id,version_id,source_node_id,target_node_id,edge_key,display_order) VALUES ('analytics-edge-1',?,?,?,?,?,'edge-1',0),('analytics-edge-2',?,?,?,?,?,'edge-2',1)`, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], fixture.Nodes["trigger"], fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], fixture.Nodes["task"]); err != nil {
		t.Fatal(err)
	}
	insertAutomationHistoryTransition(t, db, fixture, "analytics-transition-1", "analytics-work", "inv-1", "analytics-activity", "", fixture.Nodes["trigger"], "analytics-edge-1", "entered", now.Add(-50*time.Minute))
	insertAutomationHistoryTransition(t, db, fixture, "analytics-transition-2", "analytics-work", "inv-1", "analytics-activity", fixture.Nodes["trigger"], fixture.Nodes["task"], "analytics-edge-2", "entered", now.Add(-40*time.Minute))
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_work_item_positions (project_id,automation_id,version_id,work_item_id,node_id,state) VALUES (?,?,?,?,?,'blocked')`, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, "analytics-work", fixture.Nodes["task"]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_invocations SET created_at=? WHERE id='analytics-invocation'`, now); err != nil {
		t.Fatal(err)
	}

	config := &models.LLMConfig{Name: "Workflow model", Provider: models.ProviderTest, Model: "workflow-model"}
	if err := NewLLMConfigRepo(db).Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	makeTask := func(title string) (*models.Task, *models.Execution) {
		task := &models.Task{ProjectID: fixture.ProjectID, Title: title, Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "work"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "", "", 0, 100); err != nil {
			t.Fatal(err)
		}
		started := now.Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at=?,completed_at=? WHERE id=?`, started, started, exec.ID); err != nil {
			t.Fatal(err)
		}
		return task, exec
	}
	linked, _ := makeTask("Workflow linked")
	makeTask("Not linked")
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_dispatch_outbox (id,invocation_id,task_id) VALUES ('analytics-dispatch','inv-1',?)`, linked.ID); err != nil {
		t.Fatal(err)
	}
	filter := AnalyticsDashboardFilter{ProjectID: fixture.ProjectID, WorkflowID: fixture.AutomationID, DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Limit: 20}
	dashboard, err := execRepo.GetAnalyticsDashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Current.TasksEvaluated != 1 || len(dashboard.RecentOutcomes) != 1 || dashboard.RecentOutcomes[0].TaskID != linked.ID {
		t.Fatalf("workflow task filter not applied: current=%+v evidence=%+v", dashboard.Current, dashboard.RecentOutcomes)
	}
	if dashboard.WorkflowDetail == nil || len(dashboard.WorkflowDetail.Funnel) != 2 || len(dashboard.WorkflowDetail.Durations) == 0 || len(dashboard.WorkflowDetail.Failures) == 0 || len(dashboard.WorkflowDetail.Bottlenecks) == 0 {
		t.Fatalf("workflow node detail missing: %+v", dashboard.WorkflowDetail)
	}
	foundBlockedInsight := false
	for _, insight := range dashboard.Insights {
		if insight.MetricKey == "workflow_blocked" && insight.EvidenceID == fixture.AutomationID {
			foundBlockedInsight = true
		}
	}
	if !foundBlockedInsight {
		t.Fatalf("blocked workflow insight does not identify supporting workflow: %+v", dashboard.Insights)
	}
}

func TestAnalyticsAgentCategoryUsesTaskCategoryAndTerminalExecutionDenominator(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Agent categories", RepoPath: "/agent-categories"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{Name: "Category Agent", SystemPrompt: "work", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := NewAgentRepo(db).Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	config := &models.LLMConfig{Name: "Category Model", Provider: models.ProviderTest, Model: "category-model"}
	if err := NewLLMConfigRepo(db).Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Active feature", Category: models.CategoryActive, Tag: models.TagFeature, Status: models.StatusRunning, Prompt: "work", AgentDefinitionID: &agent.ID}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	executions := NewExecutionRepo(db)
	for i, status := range []models.ExecutionStatus{models.ExecFailed, models.ExecCompleted} {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "work", IsFollowup: i > 0}
		if err := executions.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
		if err := executions.Complete(ctx, exec.ID, status, "", "failed", 0, 100); err != nil {
			t.Fatal(err)
		}
		started := time.Date(2026, 1, 10+i, 10, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at=?,completed_at=? WHERE id=?`, started, started, exec.ID); err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, AgentID: agent.ID, DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.AgentDetail == nil || len(dashboard.AgentDetail.Categories) != 1 {
		t.Fatalf("Agent category detail missing: %+v", dashboard.AgentDetail)
	}
	category := dashboard.AgentDetail.Categories[0]
	if category.Category != string(models.CategoryActive) || category.TechnicalCompletion.Numerator != 1 || category.TechnicalCompletion.Denominator != 2 {
		t.Fatalf("category projection groups tags or uses task-level completion: %+v", category)
	}
	if len(dashboard.ModelCategories) != 1 || dashboard.ModelCategories[0].Category != string(models.CategoryActive) || dashboard.ModelCategories[0].TechnicalCompletion.Denominator != 2 {
		t.Fatalf("model category projection is inconsistent: %+v", dashboard.ModelCategories)
	}
}

func TestAnalyticsDashboardFailedCostDisclosesCoverageOrUnavailable(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Cost coverage", RepoPath: "/cost-coverage"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	config := &models.LLMConfig{Name: "Model", Provider: models.ProviderTest, Model: "model"}
	if err := NewLLMConfigRepo(db).Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	taskRepo := NewTaskRepo(db, nil)
	executions := NewExecutionRepo(db)
	for i := 0; i < 2; i++ {
		task := &models.Task{ProjectID: project.ID, Title: "Failed " + string(rune('A'+i)), Category: models.CategoryCompleted, Status: models.StatusFailed, Prompt: "work"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "work"}
		if err := executions.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
		if err := executions.Complete(ctx, exec.ID, models.ExecFailed, "", "failed", 0, 100); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-10 10:00:00',completed_at='2026-01-10 10:01:00' WHERE id=?`, exec.ID); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			cost := 0.75
			if err := NewUsageRepo(db).RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "test", ProjectID: project.ID, TaskID: task.ID, ExecutionID: exec.ID, AgentConfigID: config.ID, Model: "model", Operation: "task", Status: "failed", CostUSD: &cost, OccurredAt: time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC), RawUsageJSON: "{}"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	filter := AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	dashboard, err := executions.GetAnalyticsDashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Current.KnownFailedExecutionCost == nil || dashboard.Current.KnownFailedExecutionCost.Value != 0.75 || dashboard.Current.KnownFailedExecutionCost.Covered != 1 || dashboard.Current.KnownFailedExecutionCost.Eligible != 2 {
		t.Fatalf("failed cost coverage = %+v", dashboard.Current.KnownFailedExecutionCost)
	}
	filter.DateFrom = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	filter.DateTo = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	dashboard, err = executions.GetAnalyticsDashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Current.KnownFailedExecutionCost != nil {
		t.Fatalf("period without recorded failed cost must be unavailable: %+v", dashboard.Current.KnownFailedExecutionCost)
	}
}
