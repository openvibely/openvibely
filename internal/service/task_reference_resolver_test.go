package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func newTaskReferenceTestData(t *testing.T) (*repository.TaskRepo, *models.Project, *models.Task, *models.Task) {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Task reference project"}
	foreignProject := &models.Project{Name: "Foreign task reference project"}
	require.NoError(t, projectRepo.Create(context.Background(), project))
	require.NoError(t, projectRepo.Create(context.Background(), foreignProject))

	exactTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Deploy service",
		Prompt:    "Deploy the service",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	prefixTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Deploy service safely",
		Prompt:    "Deploy the service safely",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	foreignTask := &models.Task{
		ProjectID: foreignProject.ID,
		Title:     "Foreign deploy service",
		Prompt:    "Deploy the foreign service",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
	}
	require.NoError(t, taskRepo.Create(context.Background(), exactTask))
	require.NoError(t, taskRepo.Create(context.Background(), prefixTask))
	require.NoError(t, taskRepo.Create(context.Background(), foreignTask))
	return taskRepo, project, exactTask, foreignTask
}

func TestResolveTaskReferencePreservesLookupOrderScopeAndErrors(t *testing.T) {
	taskRepo, project, task, foreignTask := newTaskReferenceTestData(t)
	ctx := context.Background()

	got, err := ResolveTaskReference(ctx, taskRepo, project.ID, " \n"+task.ID+"\t", "ignored", TaskReferenceResolutionOptions{})
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	got, err = ResolveTaskReference(ctx, taskRepo, project.ID, "", "  Deploy service  ", TaskReferenceResolutionOptions{})
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID, "the first title search result must be selected")

	_, err = ResolveTaskReference(ctx, taskRepo, project.ID, "missing-task", "", TaskReferenceResolutionOptions{})
	require.EqualError(t, err, "task missing-task not found")

	_, err = ResolveTaskReference(ctx, taskRepo, project.ID, "", "missing title", TaskReferenceResolutionOptions{})
	require.EqualError(t, err, `no task found matching "missing title"`)

	_, err = ResolveTaskReference(ctx, taskRepo, project.ID, foreignTask.ID, "", TaskReferenceResolutionOptions{})
	require.EqualError(t, err, "task "+foreignTask.ID+" belongs to a different project")

	_, err = ResolveTaskReference(ctx, taskRepo, project.ID, "", "", TaskReferenceResolutionOptions{})
	require.EqualError(t, err, "no task_id or title provided")

	closedDB := testutil.NewTestDB(t)
	closedTaskRepo := repository.NewTaskRepo(closedDB, nil)
	require.NoError(t, closedDB.Close())
	_, err = ResolveTaskReference(ctx, closedTaskRepo, project.ID, task.ID, "", TaskReferenceResolutionOptions{})
	require.ErrorContains(t, err, "error looking up task "+task.ID+":")
	_, err = ResolveTaskReference(ctx, closedTaskRepo, project.ID, "", "missing title", TaskReferenceResolutionOptions{})
	require.ErrorContains(t, err, `error searching for task "missing title":`)

	_, err = ResolveTaskReference(ctx, nil, project.ID, task.ID, "", TaskReferenceResolutionOptions{})
	require.EqualError(t, err, "task repository not configured")
}

func TestTaskReferenceAdaptersPreserveChannelScopeAndScheduleUnscopedID(t *testing.T) {
	taskRepo, project, task, foreignTask := newTaskReferenceTestData(t)
	ctx := context.Background()

	got, err := resolveChannelTaskReference(ctx, taskRepo, project.ID, " "+task.ID+" ", "")
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	_, err = resolveChannelTaskReference(ctx, taskRepo, project.ID, foreignTask.ID, "")
	require.EqualError(t, err, "task "+foreignTask.ID+" belongs to a different project")

	scheduleSvc := NewScheduleActionService(taskRepo, nil)
	got, err = scheduleSvc.resolveTask(ctx, "", foreignTask.ID, "")
	require.NoError(t, err)
	require.Equal(t, foreignTask.ID, got.ID)

	got, err = scheduleSvc.resolveTask(ctx, project.ID, "", " Deploy service ")
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	_, err = scheduleSvc.resolveTask(ctx, project.ID, foreignTask.ID, "")
	require.EqualError(t, err, "task "+foreignTask.ID+" belongs to a different project")
}

func TestResolveTaskReferenceLiteralTitle(t *testing.T) {
	for _, tt := range []struct {
		query, literal, nonliteral string
	}{
		{"100% rollout", "Release 100% rollout today", "100 day rollout"},
		{"QA_plan", "Release QA_plan today", "QA plan"},
		{"docs\\guide", "Release docs\\guide today", "docsguide"},
		{"deploy", "Deploy service", "Redeploy web"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			taskRepo := repository.NewTaskRepo(db, nil)
			tasks := []models.Task{
				{ProjectID: "default", Title: tt.literal, Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending},
				{ProjectID: "default", Title: tt.nonliteral, Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending},
			}
			for i := range tasks {
				require.NoError(t, taskRepo.Create(ctx, &tasks[i]))
			}

			got, err := ResolveTaskReference(ctx, taskRepo, "default", "", " "+tt.query+" ", TaskReferenceResolutionOptions{})
			require.NoError(t, err)
			require.Equal(t, tasks[0].ID, got.ID)
		})
	}
}
