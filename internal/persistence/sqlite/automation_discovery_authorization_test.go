package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationDiscoveryAuthorizationAndRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), fixture.ProjectID, "Discovery")
	createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(time.Hour))
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "discovery-sender"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	actor := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: fixture.ProjectID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	repo := NewAutomationRepo(db)
	invocations, _, err := repo.ClaimManualAutomationRun(actor, fixture.ProjectID, fixture.AutomationID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, invocations, 1)
	input := persistence.AutomationProjectionEvent{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:     models.AutomationBinding{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["trigger"], InvocationID: invocations[0].ID},
		WorkItemKey: "github:example/repo#1", WorkItemKind: "github_issue", WorkItemTitle: "Discovered issue", WorkItemStatus: models.AutomationWorkItemWaiting,
		ActivityKey: "discovery", ActivityType: "discover_assigned_issue", ActivityStatus: models.AutomationActivityCompleted,
	}
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('discovery-editor','discovery-editor'); INSERT INTO org_memberships VALUES('default','discovery-editor','member')`)
	require.NoError(t, err)
	_, _, err = repo.RecordProjectionEvent(models.WithPrincipal(ctx, models.Principal{UserID: "discovery-editor", Role: "owner"}), input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	foreign := input
	foreign.Context.ProjectID = "default"
	_, _, err = repo.RecordProjectionEvent(owner, foreign)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = db.Exec(`CREATE TRIGGER reject_discovery_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='automation.issue_discovered' BEGIN SELECT RAISE(ABORT,'discovery audit unavailable'); END`)
	require.NoError(t, err)
	_, _, err = repo.RecordProjectionEvent(owner, input)
	require.ErrorContains(t, err, "discovery audit unavailable")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_activities WHERE activity_key='discovery'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM automation_work_items WHERE work_item_key=?`, input.WorkItemKey).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER reject_discovery_audit`)
	require.NoError(t, err)
	_, activity, err := repo.RecordProjectionEvent(owner, input)
	require.NoError(t, err)
	require.NotNil(t, activity)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	_, _, err = repo.RecordProjectionEvent(owner, input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "reusing discovery must still check saved initiator binding")
	input.ActivityKey = "later-discovery"
	input.WorkItemKey = "github:example/repo#2"
	_, _, err = repo.RecordProjectionEvent(owner, input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='automation.issue_discovered'`).Scan(&count))
	require.Equal(t, 1, count)
}
