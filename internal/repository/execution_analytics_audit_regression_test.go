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
	if len(dashboard.CycleDistribution) != 0 {
		t.Fatalf("running task must not enter cycle distribution: %+v", dashboard.CycleDistribution)
	}
	if len(dashboard.FollowUpDistribution) != 4 || dashboard.FollowUpDistribution[0].Count != 1 {
		t.Fatalf("follow-up distribution must use all executed-task KPI cohort: %+v", dashboard.FollowUpDistribution)
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
	if _, err := db.ExecContext(ctx, `UPDATE automation_invocations SET created_at=? WHERE id='inv-1'`, now); err != nil {
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
	if len(dashboard.Workflows) != 1 || dashboard.Workflows[0].DurationSampleSize != 1 {
		t.Fatalf("workflow duration sample size is not disclosed: %+v", dashboard.Workflows)
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
	historical := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "historical"}
	if err := executions.Create(ctx, historical); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, historical.ID, models.ExecFailed, "", "failed", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2025-12-20 10:00:00',completed_at='2025-12-20 10:01:00' WHERE id=?`, historical.ID); err != nil {
		t.Fatal(err)
	}
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
	if err := NewSkillAnalyticsRepo(db).RecordEvent(ctx, &models.SkillAnalyticsEvent{CreatedAt: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC), ProjectID: project.ID, TaskID: task.ID, AgentID: agent.ID, SkillScope: models.SkillScopeProject, SkillHandle: "project:category", EventType: models.SkillEventSelected}); err != nil {
		t.Fatal(err)
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
	if len(dashboard.Agents) != 1 || dashboard.Agents[0].DurationSampleSize != 1 || dashboard.Agents[0].MedianDurationMs < int64(20*24*time.Hour/time.Millisecond) {
		t.Fatalf("Agent duration must use historical first start and disclose one sample: %+v", dashboard.Agents)
	}
	if len(dashboard.AgentSkillOutcomes) != 1 || dashboard.AgentSkillOutcomes[0].AgentID != agent.ID || dashboard.AgentSkillOutcomes[0].SkillHandle != "project:category" || dashboard.AgentSkillOutcomes[0].TasksEvaluated != 1 {
		t.Fatalf("Agent/skill outcome association missing: %+v", dashboard.AgentSkillOutcomes)
	}
}

func TestAnalyticsEvidenceMatchesFunnelCycleGoalCostAndModelCohorts(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Evidence cohorts", RepoPath: "/evidence-cohorts"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	configA := &models.LLMConfig{Name: "Model A", Provider: models.ProviderTest, Model: "model-a"}
	configB := &models.LLMConfig{Name: "Model B", Provider: models.ProviderTest, Model: "model-b"}
	configs := NewLLMConfigRepo(db)
	if err := configs.Create(ctx, configA); err != nil {
		t.Fatal(err)
	}
	if err := configs.Create(ctx, configB); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{Name: "Evidence Agent", SystemPrompt: "work", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := NewAgentRepo(db).Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskRepo(db, nil)
	executions := NewExecutionRepo(db)
	goals := NewTaskGoalRepo(db)
	usage := NewUsageRepo(db)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	createdOnly := &models.Task{ProjectID: project.ID, Title: "Created but unstarted", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "work"}
	if err := tasks.Create(ctx, createdOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2026-01-05 09:00:00' WHERE id=?`, createdOnly.ID); err != nil {
		t.Fatal(err)
	}

	retried := &models.Task{ProjectID: project.ID, Title: "Cross-period multi-model retry", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work", AgentDefinitionID: &agent.ID}
	if err := tasks.Create(ctx, retried); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2025-12-01 09:00:00' WHERE id=?`, retried.ID); err != nil {
		t.Fatal(err)
	}
	oldExec := &models.Execution{TaskID: retried.ID, AgentConfigID: configA.ID, Status: models.ExecRunning, PromptSent: "first"}
	if err := executions.Create(ctx, oldExec); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, oldExec.ID, models.ExecFailed, "", "failed", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2025-12-20 10:00:00',completed_at='2025-12-20 10:01:00' WHERE id=?`, oldExec.ID); err != nil {
		t.Fatal(err)
	}
	periodFailed := &models.Execution{TaskID: retried.ID, AgentConfigID: configA.ID, Status: models.ExecRunning, PromptSent: "period attempt", IsFollowup: true}
	if err := executions.Create(ctx, periodFailed); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, periodFailed.ID, models.ExecFailed, "", "failed", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-09 10:00:00',completed_at='2026-01-09 10:01:00' WHERE id=?`, periodFailed.ID); err != nil {
		t.Fatal(err)
	}
	currentExec := &models.Execution{TaskID: retried.ID, AgentConfigID: configB.ID, Status: models.ExecRunning, PromptSent: "retry", IsFollowup: true}
	if err := executions.Create(ctx, currentExec); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, currentExec.ID, models.ExecCompleted, "", "", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-10 10:00:00',completed_at='2026-01-10 10:00:00' WHERE id=?`, currentExec.ID); err != nil {
		t.Fatal(err)
	}

	oldGoalTask := &models.Task{ProjectID: project.ID, Title: "Old achieved goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work"}
	if err := tasks.Create(ctx, oldGoalTask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2025-12-01 09:00:00' WHERE id=?`, oldGoalTask.ID); err != nil {
		t.Fatal(err)
	}
	oldGoalExec := &models.Execution{TaskID: oldGoalTask.ID, AgentConfigID: configA.ID, Status: models.ExecRunning, PromptSent: "work"}
	if err := executions.Create(ctx, oldGoalExec); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, oldGoalExec.ID, models.ExecCompleted, "", "", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-11 10:00:00',completed_at='2026-01-11 10:01:00' WHERE id=?`, oldGoalExec.ID); err != nil {
		t.Fatal(err)
	}
	if err := goals.CreateOrReplace(ctx, &models.TaskGoal{TaskID: oldGoalTask.ID, GoalID: "old", Objective: "old", Status: models.TaskGoalStatusAchieved}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at='2025-12-15 10:00:00',updated_at='2025-12-15 10:00:00' WHERE task_id=?`, oldGoalTask.ID); err != nil {
		t.Fatal(err)
	}
	oldCost := 99.0
	if err := usage.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "test", ProjectID: project.ID, TaskID: oldGoalTask.ID, ExecutionID: oldGoalExec.ID, AgentConfigID: configA.ID, Model: "model-a", Operation: "task", Status: "completed", CostUSD: &oldCost, OccurredAt: time.Date(2026, 1, 11, 10, 0, 0, 0, time.UTC), RawUsageJSON: "{}"}); err != nil {
		t.Fatal(err)
	}

	currentGoalTask := &models.Task{ProjectID: project.ID, Title: "Current achieved goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work"}
	if err := tasks.Create(ctx, currentGoalTask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2026-01-06 09:00:00' WHERE id=?`, currentGoalTask.ID); err != nil {
		t.Fatal(err)
	}
	currentGoalExec := &models.Execution{TaskID: currentGoalTask.ID, AgentConfigID: configA.ID, Status: models.ExecRunning, PromptSent: "work"}
	if err := executions.Create(ctx, currentGoalExec); err != nil {
		t.Fatal(err)
	}
	if err := executions.Complete(ctx, currentGoalExec.ID, models.ExecCompleted, "", "", 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at='2026-01-12 10:00:00',completed_at='2026-01-12 10:01:00' WHERE id=?`, currentGoalExec.ID); err != nil {
		t.Fatal(err)
	}
	if err := goals.CreateOrReplace(ctx, &models.TaskGoal{TaskID: currentGoalTask.ID, GoalID: "current", Objective: "current", Status: models.TaskGoalStatusAchieved}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at='2026-01-12 10:01:00',updated_at='2026-01-12 10:01:00' WHERE task_id=?`, currentGoalTask.ID); err != nil {
		t.Fatal(err)
	}
	currentCost := 2.0
	if err := usage.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "test", ProjectID: project.ID, TaskID: currentGoalTask.ID, ExecutionID: currentGoalExec.ID, AgentConfigID: configA.ID, Model: "model-a", Operation: "task", Status: "completed", CostUSD: &currentCost, OccurredAt: time.Date(2026, 1, 12, 10, 0, 0, 0, time.UTC), RawUsageJSON: "{}"}); err != nil {
		t.Fatal(err)
	}

	goalOnlyTask := &models.Task{ProjectID: project.ID, Title: "Current goal without current execution", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work"}
	if err := tasks.Create(ctx, goalOnlyTask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2025-12-02 09:00:00' WHERE id=?`, goalOnlyTask.ID); err != nil {
		t.Fatal(err)
	}
	if err := goals.CreateOrReplace(ctx, &models.TaskGoal{TaskID: goalOnlyTask.ID, GoalID: "goal-only", Objective: "current outcome", Status: models.TaskGoalStatusAchieved}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at='2026-01-13 10:01:00',updated_at='2026-01-13 10:01:00' WHERE task_id=?`, goalOnlyTask.ID); err != nil {
		t.Fatal(err)
	}

	dashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: from, DateTo: to, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Current.KnownCostPerAchievedGoal == nil || dashboard.Current.KnownCostPerAchievedGoal.Value != 2 || dashboard.Current.KnownCostPerAchievedGoal.Covered != 1 || dashboard.Current.KnownCostPerAchievedGoal.Eligible != 1 {
		t.Fatalf("achieved-goal cost includes out-of-period or uncovered goal: %+v", dashboard.Current.KnownCostPerAchievedGoal)
	}
	if dashboard.Current.GoalAchievement.Numerator != 2 || dashboard.Current.GoalAchievement.Denominator != 2 {
		t.Fatalf("current goal-event cohort = %+v, want both in-period achievements", dashboard.Current.GoalAchievement)
	}
	stages := make(map[string]models.OutcomeFunnelStage, len(dashboard.Funnel))
	for _, stage := range dashboard.Funnel {
		stages[stage.Key] = stage
	}
	if stages["created"].Count != 2 || stages["started"].Count != 1 || stages["technical_completed"].Count != 1 || stages["goal_achieved"].Count != 1 || stages["goal_achieved"].Denominator != 1 {
		t.Fatalf("funnel cohorts do not match created-period evidence: %+v", dashboard.Funnel)
	}
	rows := make(map[string]models.EvidenceTaskRow, len(dashboard.RecentOutcomes))
	for _, row := range dashboard.RecentOutcomes {
		rows[row.TaskID] = row
	}
	createdEvidence, ok := rows[createdOnly.ID]
	if !ok || !createdEvidence.CreatedInPeriod || createdEvidence.StartedInPeriod || createdEvidence.FunnelTechnicalCompleted {
		t.Fatalf("created-but-unstarted funnel evidence missing or ineligible: %+v", createdEvidence)
	}
	retryEvidence := rows[retried.ID]
	wantCycle := (time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC).Sub(time.Date(2025, 12, 20, 10, 0, 0, 0, time.UTC))).Milliseconds()
	if !retryEvidence.CycleEligible || retryEvidence.CycleTimeMs != wantCycle {
		t.Fatalf("cross-period cycle = eligible %v duration %d, want %d", retryEvidence.CycleEligible, retryEvidence.CycleTimeMs, wantCycle)
	}
	foundModelA, foundModelB := false, false
	for _, modelCategory := range dashboard.ModelCategories {
		if modelCategory.ModelConfigID == configA.ID {
			foundModelA = true
		}
		if modelCategory.ModelConfigID == configB.ID {
			foundModelB = true
		}
	}
	if !foundModelA || !foundModelB {
		t.Fatalf("model/category aggregates do not expose stable execution attribution: %+v", dashboard.ModelCategories)
	}
	if !analyticsContainsString(retryEvidence.ModelConfigIDs, configA.ID) || !analyticsContainsString(retryEvidence.ModelConfigIDs, configB.ID) {
		t.Fatalf("multi-model retry evidence lost execution attribution: %+v", retryEvidence.ModelConfigIDs)
	}
	if !analyticsContainsString(retryEvidence.TerminalPeriodStatuses, "2026-01-09|failed") || !analyticsContainsString(retryEvidence.TerminalPeriodStatuses, "2026-01-10|completed") {
		t.Fatalf("multi-period terminal evidence lost grouped status attribution: %+v", retryEvidence.TerminalPeriodStatuses)
	}
	agentDashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, AgentID: agent.ID, DateFrom: from, DateTo: to, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if agentDashboard.AgentDetail == nil || len(agentDashboard.AgentDetail.ModelMix) != 2 {
		t.Fatalf("selected Agent model mix missing retry models: %+v", agentDashboard.AgentDetail)
	}
	foundAgentModelA, foundAgentModelB := false, false
	for _, modelMix := range agentDashboard.AgentDetail.ModelMix {
		foundAgentModelA = foundAgentModelA || modelMix.ModelConfigID == configA.ID
		foundAgentModelB = foundAgentModelB || modelMix.ModelConfigID == configB.ID
	}
	if !foundAgentModelA || !foundAgentModelB {
		t.Fatalf("selected Agent model mix lacks stable configuration IDs: %+v", agentDashboard.AgentDetail.ModelMix)
	}
	if rows[oldGoalTask.ID].GoalAchievedInPeriod || rows[oldGoalTask.ID].KnownCostEligible {
		t.Fatalf("old goal incorrectly supports current achieved-cost KPI: %+v", rows[oldGoalTask.ID])
	}
	if !rows[currentGoalTask.ID].GoalAchievedInPeriod || !rows[currentGoalTask.ID].KnownCostEligible || !rows[currentGoalTask.ID].StartedInPeriod || !rows[currentGoalTask.ID].FunnelTechnicalCompleted || !rows[currentGoalTask.ID].FunnelGoalEligible || !rows[currentGoalTask.ID].FunnelGoalAchieved {
		t.Fatalf("current achieved goal missing cost or funnel evidence eligibility: %+v", rows[currentGoalTask.ID])
	}
	if !rows[goalOnlyTask.ID].GoalAchievementEligible || !rows[goalOnlyTask.ID].GoalAchievedInPeriod || rows[goalOnlyTask.ID].KnownCostEligible || rows[goalOnlyTask.ID].StartedInPeriod {
		t.Fatalf("goal-event-only evidence does not match goal versus cost cohorts: %+v", rows[goalOnlyTask.ID])
	}
}

func analyticsContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func TestAnalyticsDashboardEvidenceIsBoundedPaginatedAndDisclosed(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Evidence pagination", RepoPath: "/evidence-pagination"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskRepo(db, nil)
	other := &models.Project{Name: "Other evidence project", RepoPath: "/other-evidence-pagination"}
	if err := NewProjectRepo(db).Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	foreign := &models.Task{ProjectID: other.ID, Title: "Foreign evidence", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "work"}
	if err := tasks.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		task := &models.Task{ProjectID: project.ID, Title: "Evidence " + string(rune('A'+i)), Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "work"}
		if err := tasks.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at=? WHERE id=?`, time.Date(2026, 1, 5+i, 10, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	filter := AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Limit: 2}
	first, err := NewExecutionRepo(db).GetAnalyticsDashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	filter.EvidenceOffset = 2
	second, err := NewExecutionRepo(db).GetAnalyticsDashboard(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvidenceTotal != 5 || first.EvidenceLimit != 2 || first.EvidenceOffset != 0 || len(first.RecentOutcomes) != 2 {
		t.Fatalf("first evidence page metadata = total %d limit %d offset %d rows %d", first.EvidenceTotal, first.EvidenceLimit, first.EvidenceOffset, len(first.RecentOutcomes))
	}
	if second.EvidenceTotal != 5 || second.EvidenceOffset != 2 || len(second.RecentOutcomes) != 2 || first.RecentOutcomes[0].TaskID == second.RecentOutcomes[0].TaskID {
		t.Fatalf("second evidence page is not distinct and disclosed: %+v", second)
	}
}
