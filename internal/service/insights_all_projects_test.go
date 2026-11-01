package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestInsightsAllProjectsCombinedReports(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	second := &models.Project{Name: "Second analysis project"}
	require.NoError(t, env.Projects.Create(ctx, second))
	for _, owner := range []string{"default", second.ID} {
		task := &models.Task{ProjectID: owner, Title: "Fix " + owner, Prompt: "Repair dependency resolution", Category: models.CategoryCompleted, Status: models.StatusCompleted, Tag: models.TagBug, Priority: 2}
		require.NoError(t, env.Tasks.Create(ctx, task))
		require.NoError(t, env.Insights.CreateHealthCheck(ctx, &models.HealthCheck{ProjectID: owner, Grade: "F", TasksTotal: 999}))
		require.NoError(t, env.Insights.CreateIdeaGrade(ctx, &models.IdeaGrade{ProjectID: owner, Grade: "F", TasksEvaluated: 999}))
	}
	chat := &models.Task{ProjectID: models.AllProjectsID, Title: "Conversation", Category: models.CategoryChat, Status: models.StatusPending}
	require.NoError(t, env.Tasks.Create(ctx, chat))
	llm := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
	mock := testutil.NewMockLLMCaller()
	llm.SetLLMCaller(mock)
	svc := NewInsightsService(env.Insights, env.Tasks, env.Projects, env.LLMConfigs, env.Executions)
	svc.SetLLMService(llm)
	dashboard, err := svc.GetDashboard(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Nil(t, dashboard.LatestHealthCheck, "individual reports must not masquerade as combined reports")
	require.Nil(t, dashboard.LatestIdeaGrade)
	mock.Response = `{"grade":"B","assessment":"Combined assessment"}`
	health, err := svc.RunHealthCheck(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, 2, health.TasksTotal)
	require.Equal(t, 2, health.TasksCompleted)
	require.Equal(t, models.AllProjectsID, health.ProjectID)
	require.Len(t, mock.Calls, 1, "one analysis, not one per project")
	require.Contains(t, mock.Calls[0].Prompt, "Fix default")
	require.Contains(t, mock.Calls[0].Prompt, "Fix "+second.ID)
	require.Contains(t, mock.Calls[0].Prompt, "one combined analysis")
	mock.Response = `{"grade":"A","summary":"Combined ideas"}`
	grade, err := svc.GradeIdeas(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, 2, grade.TasksEvaluated)
	require.Len(t, mock.Calls, 2)
	mock.Response = `[{"topic":"Combined learning","content":"A shared lesson","tags":["dependencies"]}]`
	knowledge, err := svc.ExtractKnowledge(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Len(t, knowledge, 1)
	require.Equal(t, models.AllProjectsID, knowledge[0].ProjectID)
	require.Len(t, mock.Calls, 3)
	require.Contains(t, mock.Calls[2].Prompt, "Fix default")
	require.Contains(t, mock.Calls[2].Prompt, "Fix "+second.ID)
	// A newer individual report must not replace the saved combined report.
	require.NoError(t, env.Insights.CreateHealthCheck(ctx, &models.HealthCheck{ProjectID: second.ID, Grade: "F", TasksTotal: 1000}))
	dashboard, err = svc.GetDashboard(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, health.ID, dashboard.LatestHealthCheck.ID)
	require.Equal(t, grade.ID, dashboard.LatestIdeaGrade.ID)
	require.Len(t, dashboard.HealthHistory, 1)
	require.Len(t, dashboard.IdeaGradeHistory, 1)
	individual, err := svc.GetDashboard(ctx, "default")
	require.NoError(t, err)
	require.Equal(t, 999, individual.LatestHealthCheck.TasksTotal)
	// The non-LLM analysis also saves one report under the combined scope.
	svc.SetLLMService(nil)
	report, err := svc.RunAnalysis(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, models.AllProjectsID, report.ProjectID)
	reports, err := svc.ListReports(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, report.ID, reports[0].ID)
}
