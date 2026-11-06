package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestGitHubReviewerRemovalDeniesNextStart(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Review changes", Category: models.CategoryActive, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	reviewer := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", Permission: "triage", AddedBy: "test"}
	require.NoError(t, env.GitHubAuth.UpsertAuthorizedActor(owner, reviewer))
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
	require.NoError(t, env.TaskPullRequests.Upsert(owner, pr))
	feedback := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "review", AuthorLogin: "reviewer", Body: "revise", CreatedAt: time.Now()}
	model := models.LLMConfig{Name: "Review model", Provider: "test", Model: "test"}
	require.NoError(t, env.LLMConfigs.Create(ctx, &model))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: model.ID, Content: "revise"}
	recorded, err := env.GitHubPRFeedback.RecordForwardedAndQueue(owner, feedback, input)
	require.NoError(t, err)
	require.True(t, recorded)
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: model.ID, IsFollowup: true}
	require.NoError(t, env.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(ctx, input.ID, execution, nil, &model))
	require.NoError(t, env.Executions.MarkRunning(owner, execution.ID))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	restored, err := worker.executionPrincipalContext(ctx, task, execution.ID)
	require.NoError(t, err)
	running := restored
	require.NoError(t, env.GitHubAuth.DeleteAuthorizedActor(owner, reviewer.ID))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.ErrorIs(t, err, authz.ErrDenied, "owner cannot lend permissions to removed reviewer")
	require.ErrorIs(t, env.Executions.MarkRunning(owner, execution.ID), authz.ErrDenied)
}

func TestGitHubMappedReviewerRebindingDeniesNextStart(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Review changes", Category: models.CategoryActive, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	reviewer := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", Permission: "triage", AddedBy: "test"}
	require.NoError(t, env.GitHubAuth.UpsertAuthorizedActor(owner, reviewer))
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
	require.NoError(t, env.TaskPullRequests.Upsert(owner, pr))
	feedback := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "review", AuthorLogin: "reviewer", AuthorUserID: 123, ProviderHost: "github.com", Body: "revise", CreatedAt: time.Now()}
	model := models.LLMConfig{Name: "Review model", Provider: "test", Model: "test"}
	require.NoError(t, env.LLMConfigs.Create(ctx, &model))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: model.ID, Content: "revise"}
	binding := models.GitHubReviewerIdentity{ProjectID: "default", ActorID: reviewer.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: models.BootstrapUserID}
	require.NoError(t, env.GitHubAuth.BindReviewerIdentity(owner, binding))
	source := &models.Execution{TaskID: task.ID, AgentConfigID: model.ID, Status: models.ExecCompleted}
	require.NoError(t, env.Executions.Create(owner, source))
	admission := models.WithGitHubFeedbackExecution(owner, models.GitHubFeedbackExecution{TaskID: task.ID, ExecutionID: source.ID, RequireIdentity: true})
	recorded, err := env.GitHubPRFeedback.RecordForwardedAndQueue(admission, feedback, input)
	require.NoError(t, err)
	require.True(t, recorded)
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: model.ID, IsFollowup: true}
	require.NoError(t, env.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(ctx, input.ID, execution, nil, &model))
	require.NoError(t, env.Executions.MarkRunning(owner, execution.ID))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	restored, err := worker.executionPrincipalContext(ctx, task, execution.ID)
	require.NoError(t, err)
	running := restored
	binding.ExternalUserID = 456
	require.NoError(t, env.GitHubAuth.BindReviewerIdentity(owner, binding))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.ErrorIs(t, err, authz.ErrDenied, "owner cannot lend permissions to removed reviewer")
	require.ErrorIs(t, env.Executions.MarkRunning(owner, execution.ID), authz.ErrDenied)
}

func TestHistoricalReviewerRecoveryRequiresCapturedIdentityWhenAuthenticated(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Historical reviewer", Category: models.CategoryActive, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	reviewer := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer"}
	require.NoError(t, env.GitHubAuth.UpsertAuthorizedActor(owner, reviewer))
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42"}
	require.NoError(t, env.TaskPullRequests.Upsert(owner, pr))
	f := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "historical", AuthorLogin: "reviewer", Body: "revise", CreatedAt: time.Now()}
	model := models.LLMConfig{Name: "Historical model", Provider: "test", Model: "test"}
	require.NoError(t, env.LLMConfigs.Create(ctx, &model))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: model.ID, Content: "revise"}
	admitted, err := env.GitHubPRFeedback.RecordForwardedAndQueue(owner, f, input)
	require.NoError(t, err)
	require.True(t, admitted)
	// Creating a mapping now does not establish who authored historical input.
	require.NoError(t, env.GitHubAuth.BindReviewerIdentity(owner, models.GitHubReviewerIdentity{ProjectID: "default", ActorID: reviewer.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: models.BootstrapUserID}))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	strict, err := worker.userRunPrincipalContext(owner, task, models.BootstrapUserID)
	require.NoError(t, err)
	require.True(t, models.RequiredReviewerIdentity(strict))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: model.ID, IsFollowup: true}
	require.ErrorIs(t, env.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(strict, input.ID, execution, nil, &model), authz.ErrDenied)
	pending, err := env.ThreadInputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, pending.InputStatus)
	// Auth-off retains historical recovery, including the existing reviewer
	// allowlist checks; it never clears an inherited stricter policy.
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	compatible, err := worker.userRunPrincipalContext(owner, task, models.BootstrapUserID)
	require.NoError(t, err)
	require.False(t, models.RequiredReviewerIdentity(compatible))
	require.NoError(t, env.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(compatible, input.ID, execution, nil, &model))
	require.NoError(t, env.Executions.MarkRunning(compatible, execution.ID))
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.NoError(t, err)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, env.Executions.MarkRunning(strict, execution.ID), authz.ErrDenied)
	require.ErrorIs(t, env.ThreadInputs.CheckSteeringDelivery(strict, execution.ID, nil), authz.ErrDenied, "history-only provider sends must not bypass historical reviewer policy")
	_, err = authorizeGitHubRuntime(owner, githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: execution.ID, TaskRepo: env.Tasks}, authz.Manage)
	require.ErrorIs(t, err, authz.ErrDenied, "a fresh owner context cannot bypass policy at the runtime boundary")
	// An already admitted context is not cancelled by a policy change.
	running := context.WithValue(owner, runExecutionKey{}, execution.ID)
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
}
