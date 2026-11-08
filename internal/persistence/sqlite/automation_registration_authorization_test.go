package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRegisteredPublicationIdentityAndRevocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	project := automationGraphTestProject(t, db, "Registration identity")
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), project.ID, "Registered task")
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now(), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: false}
	require.NoError(t, NewScheduleRepo(db).Create(ctx, &schedule))
	in := models.AutomationRegisteredPublication{ProjectID: project.ID, StableKey: "authorization-test", Name: "Registered", AutomationType: "scheduled", AdapterKey: "custom", CreatedVia: "test", Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}}, Resources: []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"}}}
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('registrar','registrar'); INSERT INTO org_memberships VALUES('default','registrar','member')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "registrar", Role: "owner"})
	repo := NewAutomationRepo(db)
	_, _, err = repo.PublishRegistered(actor, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "forged owner cannot elevate inherited editor")
	access := NewProjectAccessRepo(db)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "registrar", "maintainer"))
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, project.ID, entry.ID, "registrar"))
	identity := models.DiscordIngressIdentity{ProjectID: project.ID, ExternalUserID: entry.DiscordUserID, UserID: "registrar"}
	admitted := models.WithDiscordIngressIdentity(actor, identity)
	_, _, err = repo.PublishRegistered(models.WithDiscordIngressIdentity(ctx, identity), in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	foreign := in
	foreign.ProjectID = "default"
	_, _, err = repo.PublishRegistered(admitted, foreign)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = db.Exec(`CREATE TRIGGER fail_registered_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.registered' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.PublishRegistered(admitted, in)
	require.ErrorContains(t, err, "audit unavailable")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automations WHERE project_id=?`, project.ID).Scan(&count))
	require.Zero(t, count)
	saved, err := NewScheduleRepo(db).GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.False(t, saved.Enabled)
	_, err = db.Exec(`DROP TRIGGER fail_registered_audit`)
	require.NoError(t, err)
	def, reused, err := repo.PublishRegistered(admitted, in)
	require.NoError(t, err)
	require.False(t, reused)
	var publisher string
	require.NoError(t, db.QueryRow(`SELECT user_id FROM automation_version_publishers WHERE version_id=?`, def.Version.ID).Scan(&publisher))
	require.Equal(t, "registrar", publisher)
	require.NoError(t, requireDiscordPublisher(ctx, db, def.Version.ID))
	// Reuse normalizes the graph, so audit failure must roll that write back too.
	_, err = db.Exec(`UPDATE automation_versions SET version=2 WHERE id=?`, def.Version.ID)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER fail_registered_reuse BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.registration_reused' BEGIN SELECT RAISE(ABORT,'reuse audit unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.PublishRegistered(owner, in)
	require.ErrorContains(t, err, "reuse audit unavailable")
	var version int
	require.NoError(t, db.QueryRow(`SELECT version FROM automation_versions WHERE id=?`, def.Version.ID).Scan(&version))
	require.Equal(t, 2, version)
	_, err = db.Exec(`DROP TRIGGER fail_registered_reuse`)
	require.NoError(t, err)
	// Reuse by an owner must preserve the original publisher and binding.
	_, reused, err = repo.PublishRegistered(owner, in)
	require.NoError(t, err)
	require.True(t, reused)
	require.NoError(t, db.QueryRow(`SELECT user_id FROM automation_version_publishers WHERE version_id=?`, def.Version.ID).Scan(&publisher))
	require.Equal(t, "registrar", publisher)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "registrar", ""))
	_, _, err = repo.PublishRegistered(owner, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, project.ID, "registrar", "maintainer"))
	require.NoError(t, auth.DeleteForProject(owner, project.ID, entry.ID))
	_, _, err = repo.PublishRegistered(owner, in)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='automation.registration_reused' AND target_id=?`, def.Automation.ID).Scan(&count))
	require.Equal(t, 1, count, "denied reuse leaves no audit")
}
