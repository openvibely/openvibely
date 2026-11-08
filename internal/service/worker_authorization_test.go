package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestWorkerDurableRunIdentityAndRevocation(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "worker-bob", "bob-long-password", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "editor"))
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	task := models.Task{ProjectID: "default", Title: "Saved identity", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(bobCtx, &task))
	userID, err := env.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, bob.UserID, userID)
	// Construct a fresh worker to simulate recovery without the original request.
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	restored, err := worker.runPrincipalContext(ctx, task)
	require.NoError(t, err)
	p, ok := models.PrincipalFromContext(restored)
	require.True(t, ok)
	require.Equal(t, bob.UserID, p.UserID)
	// Project role is checked against persistence, never the saved role claim.
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, policy.ProjectID, bob.UserID, "viewer"))
	_, err = worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "editor"))
	restored, err = worker.runPrincipalContext(ctx, task)
	require.NoError(t, err)
	// Revocation does not stop a run already started; the next start is denied.
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, ""))
	require.NoError(t, restored.Err())
	_, err = worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
	// A failed bind must leave the previous run-as user untouched.
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.RunIdentities.BindTask(ownerCtx, task.ID))
	require.ErrorIs(t, env.RunIdentities.BindTask(bobCtx, task.ID), authz.ErrDenied)
	userID, err = env.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, userID)
}

func TestWorkerMissingIdentityAndAuthOff(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	task := models.Task{ProjectID: "default", Title: "Internal task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(ctx, &task))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	_, err := worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	restored, err := worker.runPrincipalContext(ctx, task)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(restored)
	require.True(t, ok)
	require.Equal(t, models.BootstrapUserID, principal.UserID)
}

func TestWorkerSubmitAsDoesNotReplaceQueuedIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Queued identity", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(ctx, &task))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetAdmissionGate(func() bool { return false })
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	require.ErrorIs(t, worker.SubmitAs(ctx, task), authz.ErrDenied)

	require.NoError(t, worker.SubmitAs(ownerCtx, task))
	// The second user may also edit, but cannot retag a run already queued.
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "duplicate-bob", "bob-long-password", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "editor"))
	require.NoError(t, worker.SubmitAs(models.WithPrincipal(ctx, bob.Principal), task))
	userID, err := env.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, userID)
	require.Len(t, worker.queue, 1)
	events, err := env.ProjectAccess.Audit(ctx, "default")
	require.NoError(t, err)
	require.NotEmpty(t, events)
	var runAsEvents []models.ProjectAuditEvent
	for _, event := range events {
		if event.Action == "task.run_as" {
			runAsEvents = append(runAsEvents, event)
		}
	}
	require.Len(t, runAsEvents, 1, "duplicate submission must not rebind the run")
}

func TestWebhookWorkerRequiresCurrentMaintainer(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "webhook-worker", "long-password-value", "")
	require.NoError(t, err)
	task := models.Task{ProjectID: "default", Title: "Webhook work", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginWebhook}
	require.NoError(t, env.Tasks.Create(models.WithPrincipal(ctx, user.Principal), &task))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	_, err = worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "maintainer"))
	_, err = worker.runPrincipalContext(ctx, task)
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	_, err = worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
}

func TestWorkerSubmissionDenialPreservesIdentityAndQueue(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := org.Accept(ctx, invite, "submit-editor", "long-password-value", "")
	require.NoError(t, err)
	editor := models.WithPrincipal(ctx, member.Principal)
	for _, kind := range []string{"merge", "goal-merge", "webhook", "automation"} {
		task := models.Task{ProjectID: "default", Title: kind, Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
		task.AutoMerge = kind == "merge"
		task.AutoMergeOnGoalAchieved = kind == "goal-merge"
		if kind == "automation" {
			task.CreatedVia = persistence.AutomationCompilerTaskCreatedVia("automation-id", "node")
		}
		if kind == "webhook" {
			task.CreatedVia = models.TaskOriginWebhook
		}
		require.NoError(t, env.Tasks.Create(owner, &task))
		worker := NewWorkerService(nil, 1, env.Projects)
		worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
		require.ErrorIs(t, worker.SubmitAs(editor, task), authz.ErrDenied, kind)
		require.Empty(t, worker.queue)
		require.False(t, worker.pending[task.ID])
		saved, err := env.RunIdentities.TaskUser(ctx, task.ID)
		require.NoError(t, err)
		require.Equal(t, models.BootstrapUserID, saved)
		// A later grant permits the run policy; a revoked grant denies it again.
		require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", member.UserID, "maintainer"))
		require.NoError(t, worker.checkRunPermission(editor, task))
		require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", member.UserID, ""))
		require.ErrorIs(t, worker.SubmitAs(editor, task), authz.ErrDenied)
	}
}

func TestRecoveredAutomationRequiresMaintainerWithoutRuntimeContext(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "recovered-publisher", "long-password-value", "")
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, user.Principal)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "maintainer"))
	task := models.Task{ProjectID: "default", Title: "Recovered automation", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2, CreatedVia: persistence.AutomationCompilerTaskCreatedVia("automation-id", "task")}
	require.NoError(t, env.Tasks.Create(actor, &task))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	_, err = worker.runPrincipalContext(ctx, task)
	require.NoError(t, err)
	// Org-wide editor access remains, but is insufficient for automation execution.
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, ""))
	_, err = worker.runPrincipalContext(ctx, task)
	require.ErrorIs(t, err, authz.ErrDenied)
}

func TestWorkerUnmappedChannelRecoveryDenied(t *testing.T) {
	for _, origin := range []string{models.TaskOriginTelegram, models.TaskOriginDiscord, models.TaskOriginEmail} {
		t.Run(origin, func(t *testing.T) {
			env := teststores.New(t)
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
			task := models.Task{ProjectID: "default", Title: origin + " recovery", CreatedVia: origin, Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			worker := NewWorkerService(nil, 1, env.Projects)
			worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
			_, err := worker.runPrincipalContext(ctx, task)
			require.ErrorIs(t, err, authz.ErrDenied, "saved owner cannot substitute for sender")
			require.ErrorIs(t, worker.SubmitAs(ctx, task), authz.ErrDenied)
			execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "legacy channel"}
			require.NoError(t, env.Executions.Create(ctx, &execution))
			opts := githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: task.ProjectID, TaskID: task.ID, ExecutionID: execution.ID, TaskRepo: env.Tasks}
			_, err = authorizeGitHubRuntime(ctx, opts, authz.View)
			require.ErrorIs(t, err, authz.ErrDenied, "provider actions check access when they run")

			worker.runAuthOff = true
			restored, err := worker.runPrincipalContext(ctx, task)
			require.NoError(t, err)
			require.NoError(t, worker.checkRunPermission(restored, task))
			_, err = authorizeGitHubRuntime(ctx, opts, authz.View)
			require.NoError(t, err)
			worker.runAuthOff = false
			require.ErrorIs(t, worker.checkRunPermission(restored, task), authz.ErrDenied)
		})
	}
}
