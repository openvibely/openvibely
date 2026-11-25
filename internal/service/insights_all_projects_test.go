package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
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
	}
	chat := &models.Task{ProjectID: models.AllProjectsID, Title: "Conversation", Category: models.CategoryChat, Status: models.StatusPending}
	require.NoError(t, env.Tasks.Create(ctx, chat))
	svc := NewInsightsService(env.Insights, env.Tasks, env.Projects, env.LLMConfigs, env.Executions)
	report, err := svc.RunAnalysis(ctx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, models.AllProjectsID, report.ProjectID)
	reports, err := svc.ListReports(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, report.ID, reports[0].ID)
}
