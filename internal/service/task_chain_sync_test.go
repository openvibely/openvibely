package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestExecuteTaskEditsRefreshesBlockedChildChainConfigAndActivationCreatesGrandchild(t *testing.T) {
	db := teststores.NewDB(t)
	ctx := context.Background()
	projectRepo := teststores.Of(db).Projects
	taskRepo := teststores.Of(db).Tasks
	taskSvc := NewTaskService(taskRepo, teststores.Of(db).Attachments, nil)
	project := &models.Project{Name: "Chain Sync"}
	require.NoError(t, projectRepo.Create(ctx, project))

	initialChain := &models.ChainConfiguration{Enabled: true, Trigger: "on_completion", ChildTitle: "Initial child"}
	parent := &models.Task{ProjectID: project.ID, Title: "Parent", Prompt: "parent prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, parent.SetChainConfig(initialChain))
	require.NoError(t, taskSvc.Create(ctx, parent))
	blockedChild := workflow.BuildBlockedChild(*parent, initialChain)
	require.NoError(t, taskSvc.Create(ctx, blockedChild))
	childBefore, err := taskRepo.FindBlockedChildByParent(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, childBefore)
	require.Equal(t, "{}", childBefore.ChainConfig)

	updatedChain := &models.ChainConfiguration{
		Enabled:    true,
		Trigger:    "on_completion",
		ChildTitle: "Updated child",
		ChildChainConfig: &models.ChainConfiguration{
			Enabled:    true,
			Trigger:    "on_completion",
			ChildTitle: "Grandchild",
		},
	}
	summary := ExecuteTaskEdits(ctx, []TaskEditRequest{{ID: parent.ID, Chain: updatedChain}}, project.ID, taskSvc, nil, "")
	require.Contains(t, summary, "[TASK_EDITED:"+parent.ID+"]")
	refreshedChild, err := taskRepo.FindBlockedChildByParent(ctx, parent.ID)
	require.NoError(t, err)
	require.NotNil(t, refreshedChild)
	require.Equal(t, childBefore.ID, refreshedChild.ID)
	require.Equal(t, "Updated child", refreshedChild.Title)
	childChain, err := refreshedChild.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, childChain.Enabled)
	require.Equal(t, "Grandchild", childChain.ChildTitle)

	llmSvc := &LLMService{taskRepo: taskRepo, taskSvc: taskSvc}
	require.NoError(t, llmSvc.triggerTaskChain(ctx, *parent, "parent output"))
	activatedChild, err := taskRepo.GetByID(ctx, refreshedChild.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, activatedChild.Status)
	require.Equal(t, "Updated child", activatedChild.Title)
	require.Equal(t, "parent output", activatedChild.Prompt)
	activatedChain, err := activatedChild.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, activatedChain.Enabled)
	require.Equal(t, "Grandchild", activatedChain.ChildTitle)

	require.NoError(t, llmSvc.triggerTaskChain(ctx, *activatedChild, "child output"))
	grandchild, err := taskRepo.FindChainChildByParent(ctx, activatedChild.ID)
	require.NoError(t, err)
	require.NotNil(t, grandchild)
	require.Equal(t, "Grandchild", grandchild.Title)
	require.Equal(t, models.StatusPending, grandchild.Status)
}

func TestExecuteTaskEditsDoesNotRewriteOrDuplicateStartedChainChild(t *testing.T) {
	db := teststores.NewDB(t)
	ctx := context.Background()
	projectRepo := teststores.Of(db).Projects
	taskRepo := teststores.Of(db).Tasks
	taskSvc := NewTaskService(taskRepo, teststores.Of(db).Attachments, nil)
	project := &models.Project{Name: "Started Chain Child"}
	require.NoError(t, projectRepo.Create(ctx, project))

	parent := &models.Task{ProjectID: project.ID, Title: "Parent", Prompt: "parent prompt", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, parent.SetChainConfig(&models.ChainConfiguration{Enabled: true, Trigger: "on_completion", ChildTitle: "Original child"}))
	require.NoError(t, taskSvc.Create(ctx, parent))
	startedChild := &models.Task{ProjectID: project.ID, Title: "Started child", Prompt: "already running", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, ParentTaskID: &parent.ID, ChainConfig: "{}"}
	require.NoError(t, taskSvc.Create(ctx, startedChild))

	summary := ExecuteTaskEdits(ctx, []TaskEditRequest{{ID: parent.ID, Chain: &models.ChainConfiguration{Enabled: true, Trigger: "on_completion", ChildTitle: "Edited child"}}}, project.ID, taskSvc, nil, "")
	require.Contains(t, summary, "[TASK_EDITED:"+parent.ID+"]")
	reloadedStarted, err := taskRepo.GetByID(ctx, startedChild.ID)
	require.NoError(t, err)
	require.Equal(t, "Started child", reloadedStarted.Title)
	require.Equal(t, "{}", reloadedStarted.ChainConfig)
	blockedChild, err := taskRepo.FindBlockedChildByParent(ctx, parent.ID)
	require.NoError(t, err)
	require.Nil(t, blockedChild)

	projectTasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	childCount := 0
	for _, task := range projectTasks {
		if task.ParentTaskID != nil && *task.ParentTaskID == parent.ID {
			childCount++
		}
	}
	require.Equal(t, 1, childCount)
	require.False(t, strings.Contains(summary, "Edited child"))
}
