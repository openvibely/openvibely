package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordSteeringConsumptionRetainsRevocationEvidence(t *testing.T) {
	for _, boundary := range []string{"local", "legacy", ProviderSteeringAcceptedPending, ProviderSteeringAcceptedAmbiguous, ProviderSteeringAcceptedConfirmed} {
		t.Run(boundary, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			auth := NewDiscordAuthRepo(db)
			entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
			require.NoError(t, auth.Create(owner, entry))
			require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
			admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
			task := createThreadInputTask(t, ctx, db, "default")
			agent := createThreadInputLLMConfig(t, owner, db)
			executions := NewExecutionRepo(db)
			run := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
			require.NoError(t, executions.Create(owner, run))
			inputs := NewThreadInputRepo(db)
			input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: run.ID, Content: "instruction"}
			require.NoError(t, inputs.CreateSteeringForActiveExecution(admitted, input, run.ID))
			if boundary == "legacy" {
				other := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
				require.NoError(t, executions.Create(owner, other))
				require.ErrorIs(t, inputs.MarkApplied(ctx, input.ID, other.ID, "foreign-turn"), persistence.ErrProjectDenied)
				var foreignCount int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_consumed_provenance WHERE execution_id=?`, other.ID).Scan(&foreignCount))
				require.Zero(t, foreignCount)
			}
			apply := func() error {
				switch boundary {
				case "local":
					return inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: run.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"instruction"}})
				case "legacy":
					return inputs.MarkApplied(ctx, input.ID, run.ID, "turn")
				default:
					return inputs.RecordProviderSteering(ctx, []string{input.ID}, "steering", "previous", "response", boundary)
				}
			}
			_, err := db.Exec(`CREATE TRIGGER fail_discord_consumed_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='discord.input_consumed' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
			require.NoError(t, err)
			require.Error(t, apply())
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_consumed_provenance`).Scan(&count))
			require.Zero(t, count)
			stored, err := inputs.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.Equal(t, models.ThreadInputPending, stored.InputStatus)
			_, err = db.Exec(`DROP TRIGGER fail_discord_consumed_audit`)
			require.NoError(t, err)
			if boundary == "local" {
				require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
				require.Error(t, apply(), "revoked prepared input cannot be committed")
				require.NoError(t, auth.Create(owner, entry))
				require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
			}
			if boundary != "local" {
				require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			}
			require.NoError(t, apply())
			if boundary == "local" {
				require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
			}
			_, err = db.Exec(`DELETE FROM thread_inputs WHERE id=?`, input.ID)
			require.NoError(t, err)
			require.Error(t, NewRunIdentityRepo(db).CheckExecutionGoal(owner, run.ID, task.ID))
			require.Error(t, executions.MarkRunning(owner, run.ID))
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_consumed_provenance WHERE execution_id=?`, run.ID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
