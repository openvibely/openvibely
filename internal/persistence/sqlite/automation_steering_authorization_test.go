package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSharedAutomationSteeringRechecksAllActors(t *testing.T) {
	for _, revoked := range []string{"sender", "publisher", "initiator"} {
		for _, boundary := range []string{"prepare", "text", "commit"} {
			t.Run(revoked+"/"+boundary, func(t *testing.T) {
				db := testutil.NewTestDB(t)
				ctx := t.Context()
				fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
				tasks := NewTaskRepo(db, nil)
				task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Shared task")
				createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(time.Hour))
				access := NewProjectAccessRepo(db)
				for _, id := range []string{"sender", "publisher", "initiator"} {
					_, err := db.Exec(`INSERT INTO users(id,username) VALUES(?,?); INSERT INTO org_memberships VALUES('default',?,'member')`, id, id, id)
					require.NoError(t, err)
					require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, id, "maintainer"))
				}
				_, err := db.Exec(`INSERT INTO automation_version_publishers(version_id,user_id) VALUES(?,'publisher')`, fixture.VersionID)
				require.NoError(t, err)
				actor := models.WithPrincipal(ctx, models.Principal{UserID: "initiator"})
				repo := NewAutomationRepo(db)
				_, _, err = repo.ClaimManualAutomationRun(actor, fixture.ProjectID, fixture.AutomationID, time.Now().UTC())
				require.NoError(t, err)
				dispatch, err := repo.LeaseNextDispatch(ctx, "steering-test", time.Now().UTC(), time.Minute)
				require.NoError(t, err)
				require.NotNil(t, dispatch)
				execution, err := tasks.ClaimAutomationDispatch(actor, dispatch.ID, "steering-test")
				require.NoError(t, err)
				require.NoError(t, NewExecutionRepo(db).MarkRunning(actor, execution.ID))
				inputs := NewThreadInputRepo(db)
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.ProjectID, TaskID: task.ID, ExpectedTurnID: execution.ID, Content: "change automation"}
				require.NoError(t, inputs.CreateSteeringForActiveExecution(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), input, execution.ID))
				if boundary == "commit" {
					prepared, err := inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
					require.NoError(t, err)
					require.Len(t, prepared, 1)
				}
				require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, revoked, ""))
				if boundary == "commit" {
					err := inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"instruction"}})
					require.ErrorIs(t, err, persistence.ErrProjectDenied)
					stored, err := inputs.GetByID(ctx, input.ID)
					require.NoError(t, err)
					require.Equal(t, models.ThreadInputPending, stored.InputStatus)
					var count int
					require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM conversation_items WHERE execution_id=?`, execution.ID).Scan(&count))
					require.Zero(t, count)
				} else {
					var prepared []models.ThreadInput
					if boundary == "text" {
						prepared, err = inputs.PreparePendingTextSteering(ctx, execution.ID, execution.ID)
					} else {
						prepared, err = inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
					}
					require.NoError(t, err)
					require.Empty(t, prepared)
					stored, err := inputs.GetByID(ctx, input.ID)
					require.NoError(t, err)
					require.Equal(t, models.ThreadInputCancelled, stored.InputStatus)
				}
			})
		}
	}
}
