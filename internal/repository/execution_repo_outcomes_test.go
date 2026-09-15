package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestExecutionRepo_GetAnalyticsDashboardUsesTaskOutcomesAndProjectPeriod(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := NewProjectRepo(db)
	tasks := NewTaskRepo(db, nil)
	executions := NewExecutionRepo(db)
	goals := NewTaskGoalRepo(db)
	configs := NewLLMConfigRepo(db)
	agents := NewAgentRepo(db)
	usage := NewUsageRepo(db)
	skills := NewSkillAnalyticsRepo(db)

	project := &models.Project{Name: "Analytics project", RepoPath: "/analytics-project"}
	other := &models.Project{Name: "Other project", RepoPath: "/analytics-other"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := projects.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	configA := &models.LLMConfig{Name: "Model A", Provider: models.ProviderTest, Model: "model-a"}
	configB := &models.LLMConfig{Name: "Model B", Provider: models.ProviderTest, Model: "model-b"}
	if err := configs.Create(ctx, configA); err != nil {
		t.Fatal(err)
	}
	if err := configs.Create(ctx, configB); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{Name: "Reusable Agent", Description: "outcome evaluator", SystemPrompt: "work", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := agents.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	makeTask := func(projectID, title string, definitionID *string, worktree bool) *models.Task {
		t.Helper()
		task := &models.Task{ProjectID: projectID, Title: title, Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work", AgentDefinitionID: definitionID}
		if worktree {
			task.WorktreePath = "/tmp/" + title
			task.MergeStatus = models.MergeStatusMerged
		}
		if err := tasks.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	makeExecution := func(task *models.Task, configID string, status models.ExecutionStatus, followup bool, started string, duration int64) *models.Execution {
		t.Helper()
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: configID, Status: models.ExecRunning, PromptSent: "prompt", IsFollowup: followup}
		if err := executions.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
		if err := executions.Complete(ctx, exec.ID, status, "output", "failure", 10, duration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at=?, completed_at=? WHERE id=?`, started, started, exec.ID); err != nil {
			t.Fatal(err)
		}
		exec.StartedAt, _ = time.Parse("2006-01-02 15:04:05", started)
		return exec
	}
	makeGoal := func(task *models.Task, status models.TaskGoalStatus, eventAt string) {
		t.Helper()
		goal := &models.TaskGoal{TaskID: task.ID, GoalID: "goal-" + task.ID, Objective: "finish", Status: status}
		if err := goals.CreateOrReplace(ctx, goal); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at=CASE WHEN status='achieved' THEN ? ELSE NULL END, updated_at=? WHERE task_id=?`, eventAt, eventAt, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	recordCost := func(task *models.Task, exec *models.Execution, cost *float64, tokens int) {
		t.Helper()
		event := &models.LLMUsageEvent{Provider: "test", ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: exec.ID, AgentConfigID: exec.AgentConfigID, Model: "model", Operation: "task", Status: string(exec.Status), TotalTokens: tokens, CostUSD: cost, OccurredAt: exec.StartedAt, RawUsageJSON: "{}"}
		if err := usage.RecordUsageEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	assignedID := agent.ID
	achieved := makeTask(project.ID, "Achieved", &assignedID, true)
	achievedExec := makeExecution(achieved, configA.ID, models.ExecCompleted, false, "2026-01-10 10:00:00", 1000)
	makeGoal(achieved, models.TaskGoalStatusAchieved, "2026-01-10 10:00:00")
	known := 2.0
	recordCost(achieved, achievedExec, &known, 100)
	outsideCost := 100.0
	if err := usage.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "test", ProjectID: project.ID, TaskID: achieved.ID, ExecutionID: achievedExec.ID, AgentConfigID: achievedExec.AgentConfigID, Model: "model", Operation: "outside-period", Status: string(achievedExec.Status), TotalTokens: 1000, CostUSD: &outsideCost, OccurredAt: time.Date(2025, 12, 20, 0, 0, 0, 0, time.UTC), RawUsageJSON: "{}"}); err != nil {
		t.Fatal(err)
	}

	reworked := makeTask(project.ID, "Reworked", &assignedID, false)
	failedExec := makeExecution(reworked, configB.ID, models.ExecFailed, false, "2026-01-11 10:00:00", 2000)
	makeExecution(reworked, configB.ID, models.ExecCompleted, true, "2026-01-12 10:00:00", 3000)
	makeGoal(reworked, models.TaskGoalStatusFailed, "2026-01-12 10:00:00")
	failedCost := 0.5
	recordCost(reworked, failedExec, &failedCost, 40)
	for _, task := range []*models.Task{achieved, reworked} {
		if err := skills.RecordEvent(ctx, &models.SkillAnalyticsEvent{CreatedAt: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC), ProjectID: project.ID, TaskID: task.ID, SkillScope: models.SkillScopeProject, SkillHandle: "project:evaluator", EventType: models.SkillEventSelected}); err != nil {
			t.Fatal(err)
		}
	}
	if err := skills.RecordEvent(ctx, &models.SkillAnalyticsEvent{CreatedAt: time.Date(2026, 1, 10, 12, 1, 0, 0, time.UTC), ProjectID: project.ID, TaskID: achieved.ID, SkillScope: models.SkillScopeGlobal, SkillHandle: "project:evaluator", EventType: models.SkillEventSelected}); err != nil {
		t.Fatal(err)
	}
	if err := skills.RecordEvent(ctx, &models.SkillAnalyticsEvent{CreatedAt: time.Date(2026, 1, 10, 12, 2, 0, 0, time.UTC), ProjectID: other.ID, TaskID: achieved.ID, SkillScope: models.SkillScopeProject, SkillHandle: "foreign:telemetry", EventType: models.SkillEventSelected}); err != nil {
		t.Fatal(err)
	}

	cancelled := makeTask(project.ID, "Cancelled", nil, false)
	cancelled.Status = models.StatusCancelled
	if err := tasks.Update(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	makeExecution(cancelled, configA.ID, models.ExecCancelled, false, "2026-01-13 10:00:00", 500)
	if err := skills.RecordEvent(ctx, &models.SkillAnalyticsEvent{CreatedAt: time.Date(2026, 1, 13, 12, 0, 0, 0, time.UTC), ProjectID: project.ID, TaskID: cancelled.ID, SkillScope: models.SkillScopeProject, SkillHandle: "project:evaluator", EventType: models.SkillEventViewed}); err != nil {
		t.Fatal(err)
	}

	foreign := makeTask(other.ID, "Foreign", &assignedID, false)
	makeExecution(foreign, configA.ID, models.ExecCompleted, false, "2026-01-14 10:00:00", 900)
	previousPeriod := makeTask(project.ID, "Previous failure, current retry", &assignedID, false)
	makeExecution(previousPeriod, configA.ID, models.ExecFailed, false, "2025-12-15 10:00:00", 800)
	makeExecution(previousPeriod, configA.ID, models.ExecCompleted, true, "2026-01-15 10:00:00", 900)
	endBoundary := makeTask(project.ID, "Exclusive end boundary", &assignedID, false)
	makeExecution(endBoundary, configA.ID, models.ExecCompleted, false, "2026-02-01 00:00:00", 700)
	for _, task := range []*models.Task{achieved, reworked, cancelled, previousPeriod} {
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at='2026-01-05 00:00:00' WHERE id=?`, task.ID); err != nil {
			t.Fatal(err)
		}
	}

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	dashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: from, DateTo: to, Compare: true, Limit: 20})
	if err != nil {
		t.Fatalf("GetAnalyticsDashboard: %v", err)
	}

	if got := dashboard.Current.TechnicalCompletion; got.Numerator != 3 || got.Denominator != 5 || got.SampleSize != 5 {
		t.Errorf("technical completion = %+v, want 3/5 including cancellation denominator", got)
	}
	if got := dashboard.Current.FirstPass; got.Numerator != 1 || got.Denominator != 4 {
		t.Errorf("first pass = %+v, want 1/4 using each task's historical first terminal outcome", got)
	}
	if got := dashboard.Current.GoalAchievement; got.Numerator != 1 || got.Denominator != 2 {
		t.Errorf("goal achievement = %+v, want achieved goals over evaluable goal-bearing tasks", got)
	}
	if got := dashboard.Current.FollowUp; got.Numerator != 2 || got.Denominator != 4 {
		t.Errorf("follow-up = %+v, want tasks with follow-ups over executed tasks", got)
	}
	if dashboard.Current.KnownCostPerAchievedGoal == nil || dashboard.Current.KnownCostPerAchievedGoal.Covered != 1 || dashboard.Current.KnownCostPerAchievedGoal.Eligible != 1 || dashboard.Current.KnownCostPerAchievedGoal.Value != 2 {
		t.Errorf("known cost per achieved goal = %+v", dashboard.Current.KnownCostPerAchievedGoal)
	}
	if dashboard.Current.KnownCostPerCompletedTask == nil || dashboard.Current.KnownCostPerCompletedTask.Covered != 2 || dashboard.Current.KnownCostPerCompletedTask.Eligible != 3 || dashboard.Current.KnownCostPerCompletedTask.Value != 1.25 {
		t.Errorf("known cost per technically completed task = %+v", dashboard.Current.KnownCostPerCompletedTask)
	}
	if dashboard.Current.KnownFailedExecutionCost == nil || dashboard.Current.KnownFailedExecutionCost.Value != 0.5 || dashboard.Current.KnownFailedExecutionCost.Covered != 1 || dashboard.Current.KnownFailedExecutionCost.Eligible != 1 {
		t.Errorf("failed known cost coverage = %+v", dashboard.Current.KnownFailedExecutionCost)
	}
	if len(dashboard.Agents) != 2 {
		t.Fatalf("agents = %+v, want reusable Agent and Unassigned", dashboard.Agents)
	}
	if dashboard.Agents[0].AgentName != "Reusable Agent" || dashboard.Agents[0].TasksEvaluated != 3 {
		t.Errorf("actual Agent-definition attribution missing: %+v", dashboard.Agents)
	}
	if dashboard.Agents[0].FirstPass.Numerator != 1 || dashboard.Agents[0].FirstPass.Denominator != 3 {
		t.Errorf("agent first pass ignored historical attempts: %+v", dashboard.Agents[0].FirstPass)
	}
	if dashboard.Agents[0].TechnicalCompletion.Numerator != 3 || dashboard.Agents[0].TechnicalCompletion.Denominator != 4 {
		t.Errorf("agent execution aggregation duplicated terminal rows: %+v", dashboard.Agents[0].TechnicalCompletion)
	}
	if dashboard.Agents[0].KnownCostPerAchievedGoal == nil || dashboard.Agents[0].KnownCostPerAchievedGoal.Covered != 1 || dashboard.Agents[0].KnownCostPerAchievedGoal.Value != 2 {
		t.Errorf("agent achieved-goal cost coverage = %+v", dashboard.Agents[0].KnownCostPerAchievedGoal)
	}
	if len(dashboard.Funnel) < 4 || dashboard.Funnel[3].Denominator != 2 {
		t.Errorf("goal funnel denominator = %+v, want two evaluable goal-bearing tasks", dashboard.Funnel)
	}
	if len(dashboard.SkillOutcomes) != 2 {
		t.Fatalf("observed skill outcomes merged identical handles across scopes: %+v", dashboard.SkillOutcomes)
	}
	skillOutcomes := map[string]models.SkillOutcomePerformance{}
	for _, row := range dashboard.SkillOutcomes {
		skillOutcomes[row.SkillScope] = row
	}
	if row := skillOutcomes[models.SkillScopeProject]; row.TasksEvaluated != 2 || row.GoalAchievement.Numerator != 1 || row.GoalAchievement.Denominator != 2 {
		t.Errorf("project skill outcomes = %+v, want two project-scoped task outcomes", row)
	}
	if row := skillOutcomes[models.SkillScopeGlobal]; row.TasksEvaluated != 1 || row.GoalAchievement.Numerator != 1 || row.GoalAchievement.Denominator != 1 {
		t.Errorf("global skill outcomes = %+v, want one distinct global-scope task outcome", row)
	}
	if len(dashboard.AgentSkillOutcomes) != 2 {
		t.Fatalf("Agent/skill outcomes merged identical handles across scopes: %+v", dashboard.AgentSkillOutcomes)
	}
	for _, row := range dashboard.AgentSkillOutcomes {
		if row.AgentID != agent.ID || row.SkillHandle != "project:evaluator" || row.SkillScope == "" {
			t.Fatalf("Agent/skill outcome lost Agent, handle, or scope identity: %+v", row)
		}
	}
	projectSkillEvidence, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{
		ProjectID: project.ID, DateFrom: from, DateTo: to, Limit: 20,
		EvidenceSkillHandle: "project:evaluator", EvidenceSkillScope: models.SkillScopeProject, EvidenceSkillAgentID: agent.ID,
	})
	if err != nil {
		t.Fatalf("project skill outcome evidence: %v", err)
	}
	if projectSkillEvidence.EvidenceTotal != 2 || len(projectSkillEvidence.RecentOutcomes) != 2 {
		t.Fatalf("project skill outcome evidence = total %d rows %+v, want exact two task outcome rows", projectSkillEvidence.EvidenceTotal, projectSkillEvidence.RecentOutcomes)
	}
	for _, row := range projectSkillEvidence.RecentOutcomes {
		if row.TaskID != achieved.ID && row.TaskID != reworked.ID {
			t.Fatalf("skill outcome evidence included view-only, taskless, wrong-scope, or foreign task: %+v", row)
		}
	}
	globalSkillEvidence, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{
		ProjectID: project.ID, DateFrom: from, DateTo: to, Limit: 20,
		EvidenceSkillHandle: "project:evaluator", EvidenceSkillScope: models.SkillScopeGlobal,
	})
	if err != nil || globalSkillEvidence.EvidenceTotal != 1 || len(globalSkillEvidence.RecentOutcomes) != 1 || globalSkillEvidence.RecentOutcomes[0].TaskID != achieved.ID {
		t.Fatalf("global skill outcome evidence = total %d rows %+v err=%v, want achieved task only", globalSkillEvidence.EvidenceTotal, globalSkillEvidence.RecentOutcomes, err)
	}
	if len(dashboard.RecentOutcomes) != 4 {
		t.Errorf("recent project outcomes = %d, want 4", len(dashboard.RecentOutcomes))
	}
	if dashboard.Previous == nil {
		t.Fatal("comparison requested but previous period missing")
	}
	if len(dashboard.Definitions) < 12 {
		t.Errorf("metric definitions = %d, want centralized definitions for outcome and detailed metrics", len(dashboard.Definitions))
	}
	filtered, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: from, DateTo: to, AgentID: agent.ID, GroupBy: "day", Limit: 20})
	if err != nil {
		t.Fatalf("filtered Agent dashboard: %v", err)
	}
	if filtered.Current.TasksEvaluated != 3 || filtered.AgentDetail == nil || len(filtered.AgentDetail.OutcomeTrend) == 0 || len(filtered.AgentDetail.Categories) == 0 || len(filtered.AgentDetail.ModelMix) == 0 || len(filtered.AgentDetail.Failures) == 0 || len(filtered.AgentDetail.Skills) != 2 || len(filtered.AgentDetail.RecentTasks) != 3 {
		t.Errorf("Agent filter/detail not applied consistently: current=%+v detail=%+v", filtered.Current, filtered.AgentDetail)
	}
	unassigned, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: from, DateTo: to, AgentID: "__unassigned__", Limit: 20})
	if err != nil || unassigned.Current.TasksEvaluated != 1 || len(unassigned.RecentOutcomes) != 1 || unassigned.RecentOutcomes[0].TaskID != cancelled.ID {
		t.Errorf("unassigned filter = current %+v evidence %+v err=%v", unassigned.Current, unassigned.RecentOutcomes, err)
	}

	fromSQL, toSQL := "2026-01-01 00:00:00", "2026-02-01 00:00:00"
	if rows, err := executions.GetAvgExecutionTimeByTask(ctx, project.ID, 20, fromSQL, toSQL); err != nil || len(rows) != 3 {
		t.Errorf("period task durations = %+v, err=%v, want three current tasks", rows, err)
	}
	if rows, err := executions.GetAvgExecutionTimeByAgent(ctx, project.ID, fromSQL, toSQL); err != nil || len(rows) != 2 {
		t.Errorf("period model durations = %+v, err=%v, want two current models", rows, err)
	}
	if rows, err := executions.GetAgentUsageByProject(ctx, project.ID, fromSQL, toSQL); err != nil || len(rows) != 2 || rows[0].ExecutionCount+rows[1].ExecutionCount != 5 {
		t.Errorf("period model execution share = %+v, err=%v, want five current executions", rows, err)
	}
	if rows, err := executions.GetMostFrequentTasks(ctx, project.ID, 20, fromSQL, toSQL); err != nil || len(rows) != 4 {
		t.Errorf("period frequent tasks = %+v, err=%v, want four current tasks", rows, err)
	}
	if rows, err := executions.GetMostFrequentTasks(ctx, project.ID, 20, fromSQL, toSQL, agent.ID, ""); err != nil || len(rows) != 3 {
		t.Errorf("Agent-filtered frequent tasks = %+v, err=%v, want three assigned tasks", rows, err)
	}
	if rows, err := executions.GetSuccessFailureRates(ctx, project.ID, "day", fromSQL, toSQL, agent.ID, ""); err != nil || len(rows) == 0 {
		t.Errorf("Agent-filtered technical trend = %+v, err=%v", rows, err)
	} else {
		total := 0
		for _, row := range rows {
			total += row.TotalCount
		}
		if total != 4 {
			t.Errorf("Agent-filtered technical trend total=%d, want 4", total)
		}
	}
	if rows, err := executions.GetFailedTaskPatternsInRange(ctx, project.ID, 20, fromSQL, toSQL); err != nil || len(rows) != 1 || rows[0].TaskID != reworked.ID {
		t.Errorf("period failed patterns = %+v, err=%v, want only reworked task", rows, err)
	}
}

func TestExecutionRepo_SkillOutcomeEvidencePreservesAggregateEligibilityForRetriesAndGoals(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Skill evidence fidelity", RepoPath: "/skill-evidence-fidelity"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Completed then failed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "work"}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	executions := NewExecutionRepo(db)
	for i, status := range []models.ExecutionStatus{models.ExecCompleted, models.ExecFailed} {
		execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "prompt"}
		if err := executions.Create(ctx, execution); err != nil {
			t.Fatal(err)
		}
		if err := executions.Complete(ctx, execution.ID, status, "output", "failure", 0, 1000); err != nil {
			t.Fatal(err)
		}
		started := time.Date(2026, 1, 10+i, 10, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at=?, completed_at=? WHERE id=?`, started, started, execution.ID); err != nil {
			t.Fatal(err)
		}
	}
	goal := &models.TaskGoal{TaskID: task.ID, GoalID: "goal-" + task.ID, Objective: "finish", Status: models.TaskGoalStatusAchieved}
	if err := NewTaskGoalRepo(db).CreateOrReplace(ctx, goal); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE task_goals SET achieved_at='2026-02-02 00:00:00', updated_at='2026-02-02 00:00:00' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := NewSkillAnalyticsRepo(db).RecordEvent(ctx, &models.SkillAnalyticsEvent{
		CreatedAt: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC), ProjectID: project.ID, TaskID: task.ID,
		SkillScope: models.SkillScopeProject, SkillHandle: "project:retry-review", EventType: models.SkillEventSelected,
	}); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	aggregate, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: project.ID, DateFrom: from, DateTo: to, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.SkillOutcomes) != 1 {
		t.Fatalf("skill outcomes = %+v, want one scoped row", aggregate.SkillOutcomes)
	}
	outcome := aggregate.SkillOutcomes[0]
	if outcome.TechnicalCompletion.Numerator != 1 || outcome.TechnicalCompletion.Denominator != 1 {
		t.Fatalf("retry technical aggregate = %+v, want completed task in 1/1 numerator and denominator", outcome.TechnicalCompletion)
	}
	if outcome.GoalAchievement.Numerator != 0 || outcome.GoalAchievement.Denominator != 0 {
		t.Fatalf("out-of-period goal aggregate = %+v, want excluded from goal numerator and denominator", outcome.GoalAchievement)
	}

	dashboard, err := executions.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{
		ProjectID: project.ID, DateFrom: from, DateTo: to, Limit: 20,
		EvidenceSkillHandle: "project:retry-review", EvidenceSkillScope: models.SkillScopeProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.EvidenceTotal != 1 || len(dashboard.RecentOutcomes) != 1 {
		t.Fatalf("skill evidence = total %d rows %+v, want one retry task", dashboard.EvidenceTotal, dashboard.RecentOutcomes)
	}
	row := dashboard.RecentOutcomes[0]
	if row.TechnicalResult != string(models.ExecFailed) || row.PeriodCompletedCount != 1 || row.PeriodFailedCount != 1 || row.PeriodCancelledCount != 0 {
		t.Fatalf("retry evidence lost aggregate technical inputs: %+v", row)
	}
	if row.GoalResult != string(models.TaskGoalStatusAchieved) || row.GoalAchievementEligible || row.GoalAchievedInPeriod {
		t.Fatalf("out-of-period current goal was treated as period evidence: %+v", row)
	}
}

func TestAnalyticsDashboardSectionsForView(t *testing.T) {
	tests := []struct {
		view string
		want analyticsDashboardSections
	}{
		{
			view: "overview",
			want: analyticsDashboardSections{
				outcomeMetrics: true,
				comparison:     true,
				workflows:      true,
				evidenceRows:   true,
				insights:       true,
			},
		},
		{
			view: "outcomes",
			want: analyticsDashboardSections{
				outcomeMetrics:       true,
				followUpDistribution: true,
				funnel:               true,
				evidenceRows:         true,
				evidenceTotal:        true,
			},
		},
		{
			view: "agents",
			want: analyticsDashboardSections{
				agents:       true,
				skills:       true,
				evidenceRows: true,
				agentDetail:  true,
			},
		},
		{
			view: "learning",
			want: analyticsDashboardSections{
				skills:      true,
				agentSkills: true,
			},
		},
		{
			view: "usage",
			want: analyticsDashboardSections{
				outcomeMetrics:  true,
				modelCategories: true,
			},
		},
		{
			view: "workflows",
			want: analyticsDashboardSections{
				workflows:      true,
				workflowDetail: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.view, func(t *testing.T) {
			if got := analyticsDashboardSectionsForView(tt.view); got != tt.want {
				t.Fatalf("sections for %q = %+v, want %+v", tt.view, got, tt.want)
			}
		})
	}
	all := analyticsDashboardSectionsForView("")
	if !all.outcomeMetrics || !all.followUpDistribution || !all.comparison || !all.funnel || !all.agents || !all.skills || !all.agentSkills || !all.modelCategories || !all.workflows || !all.evidenceRows || !all.evidenceTotal || !all.agentDetail || !all.workflowDetail || !all.insights {
		t.Fatalf("legacy empty view must retain all dashboard sections: %+v", all)
	}
}

func TestAnalyticsDashboardOverviewOmitsHiddenEvidenceCountAndFollowUpDistribution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name) VALUES ('overview-project','Overview project');
		INSERT INTO tasks(id,project_id,title,category,status,created_at)
			VALUES ('overview-task','overview-project','Overview task','backlog','completed',CURRENT_TIMESTAMP);
		INSERT INTO executions(id,task_id,status,started_at,completed_at,is_followup,history_order)
			VALUES ('overview-exec','overview-task','completed',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0,1);
	`); err != nil {
		t.Fatal(err)
	}

	repo := NewExecutionRepo(db)
	overview, err := repo.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: "overview-project", View: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if overview.EvidenceTotal != 0 {
		t.Fatalf("Overview computed hidden evidence total %d", overview.EvidenceTotal)
	}
	if len(overview.RecentOutcomes) != 1 {
		t.Fatalf("Overview recent outcomes = %+v, want visible row", overview.RecentOutcomes)
	}
	if len(overview.FollowUpDistribution) != 0 {
		t.Fatalf("Overview computed hidden follow-up distribution: %+v", overview.FollowUpDistribution)
	}

	outcomes, err := repo.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: "overview-project", View: "outcomes"})
	if err != nil {
		t.Fatal(err)
	}
	if outcomes.EvidenceTotal != 1 || len(outcomes.RecentOutcomes) != 1 {
		t.Fatalf("Outcomes evidence = total %d rows %+v, want one", outcomes.EvidenceTotal, outcomes.RecentOutcomes)
	}
	if len(outcomes.FollowUpDistribution) == 0 {
		t.Fatal("Outcomes omitted visible follow-up distribution")
	}
}

func TestExecutionRepo_GetAnalyticsDashboardAllTimeOmitsComparison(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	project := &models.Project{Name: "All time", RepoPath: "/all-time"}
	if err := NewProjectRepo(db).Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	dashboard, err := repo.GetAnalyticsDashboard(context.Background(), AnalyticsDashboardFilter{ProjectID: project.ID, Compare: true})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Previous != nil {
		t.Fatalf("all-time comparison must be omitted: %+v", dashboard.Previous)
	}
	if dashboard.CycleDistribution == nil || len(dashboard.CycleDistribution) != 0 || dashboard.FollowUpDistribution == nil || len(dashboard.FollowUpDistribution) != 0 {
		t.Fatalf("empty distributions must be non-nil empty arrays: cycle=%+v followups=%+v", dashboard.CycleDistribution, dashboard.FollowUpDistribution)
	}
}
