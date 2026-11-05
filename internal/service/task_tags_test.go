package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestCustomTagsCreationEditingAndScopedExecution(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	svc := NewTaskService(env.Tasks, env.Attachments, NewWorkerService(nil, 0, nil))
	other := &models.Project{Name: "Other project"}
	require.NoError(t, env.Projects.Create(ctx, other))
	create := func(project, title, category string) models.Task {
		tasks, summary := ExecuteTaskCreationsWithReturn(ctx, []TaskCreationRequest{{Title: title, Prompt: "Work", Category: category, Priority: 2, Tag: " Docs/Security "}}, project, svc)
		require.Len(t, tasks, 1, summary)
		require.Equal(t, models.TaskTag("docs/security"), tasks[0].Tag)
		return tasks[0]
	}
	target := create("default", "Target", "backlog")
	foreign := create(other.ID, "Foreign", "backlog")
	completed := create("default", "Completed", "backlog")
	require.NoError(t, svc.UpdateCategory(ctx, completed.ID, models.CategoryCompleted))
	summary := ExecuteTaskExecutions(ctx, []TaskExecutionRequest{{Tags: []string{" Docs/Security "}}}, "default", svc)
	require.Contains(t, summary, "Executed 1 task(s)")
	for _, id := range []string{foreign.ID, completed.ID} {
		require.NotContains(t, summary, "[TASK_ID:"+id+"]")
	}
	// Omitted tags survive edits; explicit empty strings clear them.
	var req TaskEditRequest
	require.NoError(t, json.Unmarshal([]byte(`{"id":"`+target.ID+`","prompt":"Updated"}`), &req))
	require.Contains(t, ExecuteTaskEdits(ctx, []TaskEditRequest{req}, "default", svc, nil, ""), "Edited 1")
	saved, err := svc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskTag("docs/security"), saved.Tag)
	require.NoError(t, json.Unmarshal([]byte(`{"id":"`+target.ID+`","tag":""}`), &req))
	require.True(t, req.TagSet)
	require.Contains(t, ExecuteTaskEdits(ctx, []TaskEditRequest{req}, "default", svc, nil, ""), "Edited 1")
	saved, err = svc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	require.Empty(t, saved.Tag)
	require.NoError(t, json.Unmarshal([]byte(`{"id":"`+target.ID+`","tag":" Maintenance "}`), &req))
	require.Contains(t, ExecuteTaskEdits(ctx, []TaskEditRequest{req}, "default", svc, nil, ""), "Edited 1")
	saved, err = svc.GetByID(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskTag("maintenance"), saved.Tag)
	req.Tag = strings.Repeat("x", 65)
	require.Contains(t, ExecuteTaskEdits(ctx, []TaskEditRequest{req}, "default", svc, nil, ""), models.ErrInvalidTaskTag.Error())
	// A mixed valid/invalid filter must fail instead of executing a valid subset.
	summary = ExecuteTaskExecutions(ctx, []TaskExecutionRequest{{Tags: []string{"docs/security", "bad\ntag"}, IncludeCompleted: true}}, "default", svc)
	require.Contains(t, summary, models.ErrInvalidTaskTag.Error())
	require.NotContains(t, summary, "Executed")
	tasks, summary := ExecuteTaskCreationsWithReturn(ctx, []TaskCreationRequest{{Title: "Invalid", Prompt: "Work", Tag: strings.Repeat("x", 65)}}, "default", svc)
	require.Empty(t, tasks)
	require.Contains(t, summary, models.ErrInvalidTaskTag.Error())
}

func TestLegacyTagSurvivesUnrelatedEdits(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	svc := NewTaskService(env.Tasks, env.Attachments, nil)
	task := &models.Task{ProjectID: "default", Title: "Legacy", Prompt: "Work", Category: models.CategoryBacklog, Priority: 2}
	require.NoError(t, svc.Create(ctx, task))
	task.Tag = models.TaskTag(" Legacy " + strings.Repeat("x", 65))
	require.NoError(t, env.Tasks.Update(ctx, task)) // Simulate a historical value outside today's contract.
	summary := ExecuteTaskEdits(ctx, []TaskEditRequest{{ID: task.ID, Prompt: "Updated"}}, "default", svc, nil, "")
	require.Contains(t, summary, "Edited 1")
	saved, err := svc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.Tag, saved.Tag)
	require.Equal(t, "Updated", saved.Prompt)
}
