package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsFullAnalytics(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	projects, tasks, executions := NewProjectRepo(db), NewTaskRepo(db, nil), NewExecutionRepo(db)
	usage, skills := NewUsageRepo(db), NewSkillAnalyticsRepo(db)
	config := &models.LLMConfig{Name: "Analytics model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, NewLLMConfigRepo(db).Create(ctx, config))
	var projectIDs, workflowIDs []string
	for _, name := range []string{"First", "Second"} {
		project := &models.Project{Name: name, RepoPath: "/tmp/" + name}
		require.NoError(t, projects.Create(ctx, project))
		projectIDs = append(projectIDs, project.ID)
		task := &models.Task{ProjectID: project.ID, Title: name, Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2}
		require.NoError(t, tasks.Create(ctx, task))
		execution := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning}
		require.NoError(t, executions.Create(ctx, execution))
		require.NoError(t, executions.Complete(ctx, execution.ID, models.ExecCompleted, "done", "", 10, 1000))
		require.NoError(t, usage.RecordUsageEvent(ctx, &models.LLMUsageEvent{ProjectID: project.ID, TaskID: task.ID, ExecutionID: execution.ID, AgentConfigID: config.ID, Provider: "test", Model: "test", Operation: "task", TotalTokens: 100, OccurredAt: time.Now().UTC()}))
		require.NoError(t, skills.RecordEvent(ctx, &models.SkillAnalyticsEvent{ProjectID: project.ID, TaskID: task.ID, SkillHandle: "project:test", SkillScope: models.SkillScopeProject, EventType: models.SkillEventSelected, CreatedAt: time.Now().UTC()}))
		schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1}
		require.NoError(t, NewScheduleRepo(db).Create(ctx, schedule))
		automation, _, err := NewAutomationRepo(db).PublishRegistered(ctx, models.AutomationRegisteredPublication{ProjectID: project.ID, Resources: []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"}}, StableKey: "analytics-test", Name: name, AutomationType: "scheduled", AdapterKey: "custom", CreatedVia: "test", Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}}})
		require.NoError(t, err)
		workflowIDs = append(workflowIDs, automation.Automation.ID)
	}
	for _, scope := range append(projectIDs, models.AllProjectsID) {
		expected := 1
		if scope == models.AllProjectsID {
			expected = 2
		}
		for _, view := range []string{"", "overview", "outcomes", "agents", "models", "automations", "learning", "usage"} {
			dashboard, err := executions.GetAnalyticsDashboard(ctx, persistence.AnalyticsDashboardFilter{ProjectID: scope, View: view, Compare: true, DateFrom: time.Now().Add(-time.Hour), DateTo: time.Now().Add(time.Hour)})
			require.NoError(t, err, "%s/%s", scope, view)
			if view == "" {
				require.Len(t, dashboard.RecentOutcomes, expected)
				require.Len(t, dashboard.Workflows, expected)
			}
		}
		totals, err := usage.GetUsageTotals(ctx, persistence.UsageFilter{ProjectID: scope})
		require.NoError(t, err)
		require.Equal(t, expected*100, totals.TotalTokens)
	}
	for _, workflowID := range workflowIDs {
		dashboard, err := executions.GetAnalyticsDashboard(ctx, persistence.AnalyticsDashboardFilter{ProjectID: models.AllProjectsID, View: "automations", WorkflowID: workflowID})
		require.NoError(t, err)
		require.NotNil(t, dashboard.WorkflowDetail)
	}
	skillRows, total, err := skills.GetEvidence(ctx, persistence.SkillAnalyticsFilter{ProjectID: models.AllProjectsID, EvidenceLimit: 20, EvidenceEvent: "selected"})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, skillRows, 2)
	usageRows, total, err := usage.GetEvidence(ctx, persistence.UsageFilter{ProjectID: models.AllProjectsID, EvidenceProvider: "test", EvidenceLimit: 20})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, usageRows, 2)
	for _, projectID := range projectIDs {
		require.Contains(t, []string{usageRows[0].ProjectID, usageRows[1].ProjectID}, projectID)
	}
	rates, err := executions.GetSuccessFailureRates(ctx, models.AllProjectsID, "day", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, rates)
	times, err := executions.GetAvgExecutionTimeByTask(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, times, 2)
	frequent, err := executions.GetMostFrequentTasks(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, frequent, 2)

}
