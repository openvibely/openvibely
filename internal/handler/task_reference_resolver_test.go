package handler

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestHandlerTaskReferenceAdapterDelegatesExplicitLookupAndPreservesCurrentHandling(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	foreignProject := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Handler task reference").
		WithCategory(models.CategoryBacklog).
		Build()
	foreignTask := tc.CreateTask(foreignProject.ID).
		WithTitle("Foreign handler task reference").
		WithCategory(models.CategoryBacklog).
		Build()

	got, err := tc.handler.resolveTaskReference(ctx, project.ID, " "+task.ID+" ", "")
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	got, err = tc.handler.resolveTaskReference(ctx, project.ID, "", "  Handler task reference  ")
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	_, err = tc.handler.resolveTaskReference(ctx, project.ID, foreignTask.ID, "")
	require.EqualError(t, err, "task "+foreignTask.ID+" belongs to a different project")
	_, err = tc.handler.resolveTaskReference(ctx, project.ID, "", "")
	require.EqualError(t, err, "no task_id or title provided")

	followupParams := streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true}
	gotID, err := tc.handler.resolveTaskIDForTool(ctx, followupParams, "current", "")
	require.NoError(t, err)
	require.Equal(t, task.ID, gotID)

	_, err = tc.handler.resolveTaskIDForTool(ctx, streamingResponseParams{ProjectID: project.ID}, "current", "")
	require.ErrorContains(t, err, "only valid in a persisted task thread")
}
