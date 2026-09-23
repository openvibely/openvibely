package repository

import (
	"context"
	"reflect"
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
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET worktree_path='' WHERE id=?`, achieved.ID); err != nil {
		t.Fatal(err)
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
	trendByPeriod := map[string]models.OutcomeTrendPoint{}
	for _, point := range dashboard.OutcomeTrend {
		trendByPeriod[point.Period] = point
	}
	if len(trendByPeriod) != 5 {
		t.Fatalf("outcome trend periods = %+v, want five project-scoped days", dashboard.OutcomeTrend)
	}
	if got := trendByPeriod["2026-01-10"]; got.TechnicalCompletion.Numerator != 1 || got.TechnicalCompletion.Denominator != 1 || got.GoalAchievement.Numerator != 1 || got.GoalAchievement.Denominator != 1 || got.FirstPass.Numerator != 1 || got.FirstPass.Denominator != 1 || got.FollowUp.Numerator != 0 || got.FollowUp.Denominator != 1 {
		t.Errorf("January 10 outcome trend = %+v", got)
	}
	if got := trendByPeriod["2026-01-12"]; got.TechnicalCompletion.Numerator != 1 || got.TechnicalCompletion.Denominator != 1 || got.GoalAchievement.Numerator != 0 || got.GoalAchievement.Denominator != 1 || got.FirstPass.Numerator != 0 || got.FirstPass.Denominator != 1 || got.FollowUp.Numerator != 1 || got.FollowUp.Denominator != 1 {
		t.Errorf("January 12 outcome trend = %+v", got)
	}
	if _, leaked := trendByPeriod["2026-01-14"]; leaked {
		t.Fatalf("foreign project leaked into outcome trend: %+v", dashboard.OutcomeTrend)
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
	if len(dashboard.Funnel) < 5 || dashboard.Funnel[4].Count != 1 || dashboard.Funnel[4].Denominator != 1 {
		t.Errorf("merge funnel lost cleaned-up merged worktree: %+v", dashboard.Funnel)
	}
	foundMergedEvidence := false
	for _, row := range dashboard.RecentOutcomes {
		if row.TaskID == achieved.ID {
			foundMergedEvidence = row.FunnelMergeEligible && row.FunnelMerged
		}
	}
	if !foundMergedEvidence {
		t.Errorf("merged task evidence lost eligibility after worktree cleanup: %+v", dashboard.RecentOutcomes)
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
				outcomeTrend:   true,
				comparison:     true,
				funnel:         true,
				workflows:      true,
				evidenceRows:   true,
				insights:       true,
			}},
		{
			view: "outcomes",
			want: analyticsDashboardSections{
				outcomeMetrics:       true,
				outcomeTrend:         true,
				comparison:           true,
				followUpDistribution: true, funnel: true,
				evidenceRows:  true,
				evidenceTotal: true,
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
			view: "models",
			want: analyticsDashboardSections{
				models:          true,
				modelCategories: true,
				agents:          true,
				workflows:       true,
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
				outcomeMetrics: true,
				outcomeTrend:   true,
				comparison:     true},
		},
		{
			view: "automations", want: analyticsDashboardSections{
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
	if !all.outcomeMetrics || !all.outcomeTrend || !all.followUpDistribution || !all.comparison || !all.funnel || !all.agents || !all.skills || !all.agentSkills || !all.modelCategories || !all.models || !all.workflows || !all.evidenceRows || !all.evidenceTotal || !all.agentDetail || !all.workflowDetail || !all.insights {
		t.Fatalf("legacy empty view must retain all dashboard sections: %+v", all)
	}
}

func TestAnalyticsDashboardModelsCompareConfiguredModelOutcomesAndUsage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name) VALUES ('model-project','Model project');
		INSERT INTO agent_configs(id,name,provider,model,auth_method,reasoning_effort) VALUES
			('model-a','Fable','anthropic','claude-fable','oauth','high'),
			('model-b','Luna XHigh','openai','gpt-luna','oauth','xhigh');
		INSERT INTO tasks(id,project_id,title,status,created_at,worktree_path,merge_status) VALUES
			('task-a','model-project','Recovered task','completed','2026-09-01 09:00:00','',''),
			('task-b','model-project','Direct task','completed','2026-09-01 09:00:00','/tmp/task-b','pending');
		INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,duration_ms,is_followup,history_order) VALUES
			('exec-a1','task-a','model-a','failed','2026-09-01 10:00:00','2026-09-01 10:10:00',600000,0,1),
			('exec-a2','task-a','model-b','completed','2026-09-01 11:00:00','2026-09-01 11:20:00',1200000,1,2),
			('exec-b1','task-b','model-a','completed','2026-09-01 12:00:00','2026-09-01 12:30:00',1800000,0,1);
		INSERT INTO task_goals(task_id,goal_id,objective,status,achieved_at,updated_at) VALUES
			('task-a','goal-a','Recover','achieved','2026-09-01 11:20:00','2026-09-01 11:20:00'),
			('task-b','goal-b','Complete','achieved','2026-09-01 12:30:00','2026-09-01 12:30:00');
		INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,total_tokens,cost_usd,occurred_at) VALUES
			('usage-b','openai','model-project','task-a','exec-a2','model-b','gpt-luna',1000,0.25,'2026-09-01 11:20:00');
	`); err != nil {
		t.Fatalf("seed model analytics: %v", err)
	}

	dashboard, err := NewExecutionRepo(db).GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: "model-project", View: "models"})
	if err != nil {
		t.Fatalf("get model analytics: %v", err)
	}
	if len(dashboard.Models) != 2 {
		t.Fatalf("models = %+v, want two configurations", dashboard.Models)
	}
	byID := map[string]models.ModelPerformance{}
	for _, row := range dashboard.Models {
		byID[row.ModelConfigID] = row
	}
	fable := byID["model-a"]
	if fable.ConfigName != "Fable" || fable.ReasoningEffort != "high" || fable.TasksUsed != 1 || fable.RunCount != 1 ||
		fable.GoalAchievement.Numerator != 1 || fable.GoalAchievement.Denominator != 1 ||
		fable.MergeCompletion.Numerator != 0 || fable.MergeCompletion.Denominator != 1 ||
		fable.MedianDurationMs < 1799000 || fable.MedianDurationMs > 1801000 || fable.TokenCoveredTasks != 0 || fable.KnownCostUSD != nil {
		t.Fatalf("single-model task comparison = %+v", fable)
	}
	if _, ok := byID["model-b"]; ok {
		t.Fatal("finishing model must not receive credit for a mixed-model task")
	}
	mixed := byID["__mixed__"]
	if !mixed.MixedModels || mixed.ConfigName != "Mixed models" || mixed.TasksUsed != 1 || mixed.RunCount != 2 ||
		mixed.GoalAchievement.Numerator != 1 || mixed.GoalAchievement.Denominator != 1 || mixed.MergeCompletion.Denominator != 0 ||
		mixed.AverageFollowUps != 1 || mixed.MedianDurationMs < 4799000 || mixed.MedianDurationMs > 4801000 ||
		mixed.TotalTokens != 1000 || mixed.CostCoveredTasks != 1 || mixed.KnownCostUSD == nil || *mixed.KnownCostUSD != 0.25 {
		t.Fatalf("mixed-model comparison = %+v", mixed)
	}
}

func TestAnalyticsDashboardModelsWholeTaskEffortAndPeriod(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name) VALUES ('p','Project');
		INSERT INTO agent_configs(id,name,provider,model,auth_method) VALUES ('m','Model','openai','model','oauth');
		INSERT INTO tasks(id,project_id,title,status,merge_status) VALUES
			('delivered','p','Delivered without goal','completed','merged'),
			('failed','p','Failed task','failed','pending'),
			('running','p','Reopened task','running','');
		INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,is_followup,history_order) VALUES
			('d1','delivered','m','failed','2026-08-31 23:00:00','2026-08-31 23:10:00',0,1),
			('d2','delivered','m','completed','2026-09-01 07:00:00','2026-09-01 07:30:00',1,2),
			('f','failed','m','failed','2026-09-01 10:00:00','2026-09-01 10:30:00',0,1),
			('r1','running','m','completed','2026-09-01 11:00:00','2026-09-01 11:30:00',0,1),
			('r2','running','m','running','2026-09-01 12:00:00',NULL,1,2);
		INSERT INTO task_goals(task_id,goal_id,objective,status) VALUES ('failed','g','Goal','failed');
		INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,total_tokens,cost_usd,occurred_at) VALUES
			('u1','openai','p','delivered','d1','m','model',1000,1,'2026-08-31 23:10:00'),
			('u2','openai','p','delivered','d2','m','model',2000,2,'2026-09-01 07:30:00'),
			('u3','openai','p','failed','f','m','model',500,0.5,'2026-09-01 10:30:00');
	`)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dashboard, err := NewExecutionRepo(db).GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: "p", View: "models", DateFrom: from, DateTo: from.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Models) != 1 {
		t.Fatalf("models: %+v", dashboard.Models)
	}
	r := dashboard.Models[0]
	if r.TasksUsed != 2 || r.RunCount != 3 || r.TotalTokens != 3500 || r.KnownCostUSD == nil || *r.KnownCostUSD != 3.5 || r.AverageFollowUps != 0.5 {
		t.Fatalf("must include historical and unsuccessful effort, exclude reopened tasks: %+v", r)
	}
	if r.GoalAchievement.Numerator != 0 || r.GoalAchievement.Denominator != 1 || r.MergeCompletion.Numerator != 1 || r.MergeCompletion.Denominator != 2 {
		t.Fatalf("goal and delivery must remain independent: %+v", r)
	}
	// Median of 8h30m and 30m is 4h30m; the earlier failed run starts the clock.
	if r.DurationSampleSize != 2 || r.MedianDurationMs < 16199000 || r.MedianDurationMs > 16201000 {
		t.Fatalf("whole-task elapsed time: %+v", r)
	}
	if len(r.OutcomeTrend) != 1 || r.OutcomeTrend[0].Period != "2026-09-01" || r.OutcomeTrend[0].GoalAchievement.Denominator != 1 || r.OutcomeTrend[0].MergeCompletion.Percent != 50 {
		t.Fatalf("trend must match scorecard samples: %+v", r.OutcomeTrend)
	}
	if _, err := db.Exec(`UPDATE executions SET started_at='2026-09-02 10:00:00',completed_at='2026-09-02 10:30:00' WHERE id='f'`); err != nil {
		t.Fatal(err)
	}
	filter := AnalyticsDashboardFilter{ProjectID: "p", View: "models", DateFrom: from, DateTo: from.AddDate(0, 0, 3)}
	daily, err := NewExecutionRepo(db).queryModelPerformance(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	points := daily[0].OutcomeTrend
	if len(points) != 2 || points[0].Period != "2026-09-01" || points[1].Period != "2026-09-02" || points[0].GoalAchievement.Denominator != 0 || points[0].MergeCompletion.Percent != 100 || points[1].MergeCompletion.Percent != 0 {
		t.Fatalf("missing goal evidence must not become zero success; periods must be ordered: %+v", points)
	}
	filter.GroupBy = "month"
	monthly, err := NewExecutionRepo(db).queryModelPerformance(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(monthly[0].OutcomeTrend) != 1 || monthly[0].OutcomeTrend[0].Period != "2026-09" || monthly[0].OutcomeTrend[0].MergeCompletion.Percent != 50 {
		t.Fatalf("monthly trend: %+v", monthly)
	}
	// Neither chat tasks nor non-task usage attributed to a real task may change
	// any model metric, attribution, or trend point.
	_, err = db.Exec(`
		INSERT INTO agent_configs(id,name,provider,model,auth_method) VALUES ('chat-model','Chat model','openai','chat','oauth');
		INSERT INTO tasks(id,project_id,title,category,status,merge_status) VALUES ('chat','p','Chat','chat','completed','merged');
		INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at) VALUES ('chat-run','chat','chat-model','completed','2026-09-01 10:00:00','2026-09-01 10:00:10');
		INSERT INTO task_goals(task_id,goal_id,objective,status) VALUES ('chat','chat-goal','Chat goal','achieved');
		INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,operation,total_tokens,cost_usd) VALUES
		('chat-usage','openai','p','chat','chat-run','chat-model','chat','task',999999,99),
		('hook-usage','openai','p','delivered','d2','chat-model','chat','direct',999999,99),
		('stream-usage','openai','p','delivered','d2','chat-model','chat','streaming',999999,99);
	`)
	if err != nil {
		t.Fatal(err)
	}
	taskOnly, err := NewExecutionRepo(db).queryModelPerformance(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(monthly, taskOnly) {
		t.Fatalf("chat or non-task usage contaminated Models: before=%+v after=%+v", monthly, taskOnly)
	}
}

func TestAnalyticsDashboardModelsIncludeAllWorkTypes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO projects(id,name) VALUES ('work-type-project','Work type project');
		INSERT INTO agent_configs(id,name,provider,model,auth_method) VALUES ('work-type-model','Model','openai','gpt-model','oauth');
		INSERT INTO tasks(id,project_id,title,category,status,created_at) VALUES
			('interactive-task','work-type-project','Interactive','backlog','completed','2026-09-01 09:00:00'),
			('recurring-task','work-type-project','Recurring','scheduled','completed','2026-09-01 09:00:00');
		INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,is_followup,history_order) VALUES
			('interactive-run','interactive-task','work-type-model','completed','2026-09-01 10:00:00','2026-09-01 10:10:00',0,1),
			('recurring-run','recurring-task','work-type-model','completed','2026-09-01 11:00:00','2026-09-01 11:10:00',0,1);
	`); err != nil {
		t.Fatalf("seed work type analytics: %v", err)
	}

	repo := NewExecutionRepo(db)
	for _, test := range []struct {
		workType  string
		wantTasks int
		wantRuns  int
	}{
		{workType: "", wantTasks: 2, wantRuns: 2},
		{workType: "interactive", wantTasks: 2, wantRuns: 2},
		{workType: "recurring", wantTasks: 2, wantRuns: 2},
	} {
		dashboard, err := repo.GetAnalyticsDashboard(ctx, AnalyticsDashboardFilter{ProjectID: "work-type-project", View: "models", WorkType: test.workType})
		if err != nil {
			t.Fatalf("get %q model analytics: %v", test.workType, err)
		}
		if len(dashboard.Models) != 1 || dashboard.Models[0].TasksUsed != test.wantTasks || dashboard.Models[0].RunCount != test.wantRuns {
			t.Fatalf("%q models = %+v, want tasks=%d runs=%d", test.workType, dashboard.Models, test.wantTasks, test.wantRuns)
		}
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
