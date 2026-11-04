package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestHasPendingSteeringDoesNotClaimInput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(ctx, execution))
	repo := NewThreadInputRepo(db)
	check := func(want bool) {
		t.Helper()
		pending, err := repo.HasPendingSteering(ctx, execution.ID)
		require.NoError(t, err)
		require.Equal(t, want, pending)
	}
	check(false)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "steer"}
	require.NoError(t, repo.CreateSteeringForActiveExecution(ctx, input, execution.ID))
	check(true)
	check(true)
	stored, err := repo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, execution.ID, stored.ExpectedTurnID)
	pending, err := repo.HasPendingSteering(ctx, "other-execution")
	require.NoError(t, err)
	require.False(t, pending)
	claimed, err := repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	check(false)
	require.NoError(t, repo.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{map[string]any{"type": "message", "role": "user", "content": "steer"}}}))
	check(false)
}

func TestSteeringPreparationRejectsRevokedSenderAndAuditsAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(owner, execution))
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('steerer','steerer'); INSERT INTO org_memberships VALUES('default','steerer','member')`)
	require.NoError(t, err)
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "steerer"})
	repo := NewThreadInputRepo(db)
	denied := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "Revoked instruction"}
	require.NoError(t, repo.CreateSteeringForActiveExecution(sender, denied, execution.ID))
	allowed := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "Owner instruction"}
	require.NoError(t, repo.CreateSteeringForActiveExecution(owner, allowed, execution.ID))
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='steerer'; CREATE TRIGGER fail_steering_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='steering.access_revoked' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
	require.ErrorContains(t, err, "audit unavailable")
	stored, err := repo.GetByID(ctx, denied.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	_, err = db.Exec(`DROP TRIGGER fail_steering_audit`)
	require.NoError(t, err)
	prepared, err := repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	require.Equal(t, allowed.ID, prepared[0].ID)
	stored, err = repo.GetByID(ctx, denied.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputCancelled, stored.InputStatus)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='steering.access_revoked' AND target_id=? AND actor_user_id='steerer'`, denied.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestSteeringCommitRechecksSenderAfterPreparation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(owner, execution))
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('late-revoked','late-revoked'); INSERT INTO org_memberships VALUES('default','late-revoked','member')`)
	require.NoError(t, err)
	repo := NewThreadInputRepo(db)
	var ids []string
	for _, actor := range []context.Context{owner, models.WithPrincipal(ctx, models.Principal{UserID: "late-revoked"})} {
		input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "instruction"}
		require.NoError(t, repo.CreateSteeringForActiveExecution(actor, input, execution.ID))
		ids = append(ids, input.ID)
	}
	prepared, err := repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
	require.NoError(t, err)
	require.Len(t, prepared, 2)
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='late-revoked'`)
	require.NoError(t, err)
	err = repo.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: ids, ResponsesHistory: []any{map[string]any{"role": "user", "content": "instruction"}}})
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	for _, id := range ids {
		stored, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, models.ThreadInputPending, stored.InputStatus, "whole batch must roll back")
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM conversation_items WHERE execution_id=?`, execution.ID).Scan(&count))
	require.Zero(t, count)
}

func TestPrivilegedSteeringRechecksRequiredRole(t *testing.T) {
	for _, kind := range []string{"ordinary", "merge", "goal-merge", "webhook", "automation"} {
		for _, boundary := range []string{"prepare", "text-prepare", "commit"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				db := testutil.NewTestDB(t)
				ctx := t.Context()
				owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
				task := createThreadInputTask(t, owner, db, "default")
				var err error
				switch kind {
				case "merge":
					_, err = db.Exec(`UPDATE tasks SET auto_merge=1 WHERE id=?`, task.ID)
				case "goal-merge":
					_, err = db.Exec(`UPDATE tasks SET auto_merge_on_goal_achieved=1 WHERE id=?`, task.ID)
				case "webhook":
					_, err = db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, models.TaskOriginWebhook, task.ID)
				case "automation":
					_, err = db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, persistence.AutomationCompilerTaskCreatedVia("automation", "node"), task.ID)
				}
				require.NoError(t, err)
				execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
				require.NoError(t, NewExecutionRepo(db).Create(owner, execution))
				_, err = db.Exec(`INSERT INTO users(id,username) VALUES('steerer','steerer'); INSERT INTO org_memberships VALUES('default','steerer','member')`)
				require.NoError(t, err)
				access := NewProjectAccessRepo(db)
				require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "steerer", "maintainer"))
				repo := NewThreadInputRepo(db)
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "instruction"}
				require.NoError(t, repo.CreateSteeringForActiveExecution(models.WithPrincipal(ctx, models.Principal{UserID: "steerer"}), input, execution.ID))
				if boundary == "commit" {
					prepared, err := repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
					require.NoError(t, err)
					require.Len(t, prepared, 1)
				}
				// Membership remains active: only the maintainer grant is revoked.
				require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "steerer", ""))
				if boundary == "commit" {
					err = repo.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{map[string]any{"role": "user", "content": "instruction"}}})
					if kind == "ordinary" {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, persistence.ErrProjectDenied)
						stored, err := repo.GetByID(ctx, input.ID)
						require.NoError(t, err)
						require.Equal(t, models.ThreadInputPending, stored.InputStatus)
						var count int
						require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM conversation_items WHERE execution_id=?`, execution.ID).Scan(&count))
						require.Zero(t, count)
					}
				} else {
					var prepared []models.ThreadInput
					if boundary == "text-prepare" {
						prepared, err = repo.PreparePendingTextSteering(ctx, execution.ID, execution.ID)
					} else {
						prepared, err = repo.PreparePendingSteering(ctx, execution.ID, execution.ID)
					}
					require.NoError(t, err)
					if kind == "ordinary" {
						require.Len(t, prepared, 1)
					} else {
						require.Empty(t, prepared)
						stored, err := repo.GetByID(ctx, input.ID)
						require.NoError(t, err)
						require.Equal(t, models.ThreadInputCancelled, stored.InputStatus)
					}
				}
			})
		}
	}
}

func TestSteeringAdmissionChecksRoleAndProjectBeforeWriting(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	_, err := db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, models.TaskOriginWebhook, task.ID)
	require.NoError(t, err)
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(owner, execution))
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "sender", Role: "owner"})
	repo := NewThreadInputRepo(db)
	wakeup, unsubscribe := repo.SubscribeSteeringWakeups(execution.ID)
	defer unsubscribe()
	newInput := func(project string) *models.ThreadInput {
		return &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project, TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "instruction"}
	}
	denied := newInput("default")
	require.ErrorIs(t, repo.CreateSteeringForActiveExecution(sender, denied, execution.ID), persistence.ErrProjectDenied)
	require.Empty(t, denied.ID)
	require.Zero(t, denied.QueuePosition)
	other := createThreadInputProject(t, ctx, db)
	require.ErrorIs(t, repo.CreateSteeringForActiveExecution(owner, newInput(other.ID), execution.ID), persistence.ErrProjectDenied)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM thread_inputs WHERE run_execution_id=?`, execution.ID).Scan(&count))
	require.Zero(t, count)
	select {
	case <-wakeup:
		t.Fatal("denied admission woke the provider")
	default:
	}
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "sender", "maintainer"))
	allowed := newInput("default")
	require.NoError(t, repo.CreateSteeringForActiveExecution(sender, allowed, execution.ID))
	require.Equal(t, "sender", allowed.AuthorUserID)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "sender", ""))
	require.ErrorIs(t, repo.CreateSteeringForActiveExecution(sender, newInput("default"), execution.ID), persistence.ErrProjectDenied)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM thread_inputs WHERE run_execution_id=?`, execution.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestSteeringConversionChecksCallerAndOriginalSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	_, err := db.Exec(`UPDATE tasks SET created_via=? WHERE id=?`, models.TaskOriginWebhook, task.ID)
	require.NoError(t, err)
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(owner, execution))
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('converter','converter'),('original','original'); INSERT INTO org_memberships VALUES('default','converter','member'),('default','original','member')`)
	require.NoError(t, err)
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "original", "maintainer"))
	original := models.WithPrincipal(ctx, models.Principal{UserID: "original"})
	converter := models.WithPrincipal(ctx, models.Principal{UserID: "converter", Role: "owner"})
	repo := NewThreadInputRepo(db)
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, RunExecutionID: execution.ID, Content: "instruction"}
	require.NoError(t, repo.CreateQueued(original, queued))
	wakeup, unsubscribe := repo.SubscribeSteeringWakeups(execution.ID)
	defer unsubscribe()
	checkDenied := func(actor context.Context) {
		t.Helper()
		converted, err := repo.ConvertQueuedToSteering(actor, queued.ID, execution.ID, execution.ID)
		require.ErrorIs(t, err, persistence.ErrProjectDenied)
		require.Nil(t, converted)
		stored, err := repo.GetByID(ctx, queued.ID)
		require.NoError(t, err)
		require.Equal(t, models.ThreadInputModeQueued, stored.InputMode)
		require.Equal(t, "original", stored.AuthorUserID)
		select {
		case <-wakeup:
			t.Fatal("denied conversion woke provider")
		default:
		}
	}
	checkDenied(converter)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "converter", "maintainer"))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "original", ""))
	checkDenied(converter)
	checkDenied(owner)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, "default", "original", "maintainer"))
	_, err = db.Exec(`CREATE TRIGGER reject_conversion_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='steering.converted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = repo.ConvertQueuedToSteering(converter, queued.ID, execution.ID, execution.ID)
	require.ErrorContains(t, err, "audit unavailable")
	stored, err := repo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputModeQueued, stored.InputMode)
	select {
	case <-wakeup:
		t.Fatal("rolled-back conversion woke provider")
	default:
	}
	_, err = db.Exec(`DROP TRIGGER reject_conversion_audit`)
	require.NoError(t, err)
	converted, err := repo.ConvertQueuedToSteering(converter, queued.ID, execution.ID, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputModeSteering, converted.InputMode)
	require.Equal(t, "original", converted.AuthorUserID)
}
