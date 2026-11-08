package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestDetachedGitHubCleanupPreservesAuthorization(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	invocation, _, err := f.repo.ClaimScheduledOccurrence(ctx, f.schedule, time.Now(), f.schedule.ComputeNextRun(time.Now()))
	require.NoError(t, err)
	node := automationNodeByKey(t, f.definition, "bug_finder")
	binding := models.AutomationBinding{AutomationID: f.definition.Automation.ID, VersionID: f.definition.Version.ID, InvocationID: invocation.ID, NodeID: node.ID}
	opts := githubIssueRuntimeOptions{ProjectID: f.project.ID, AutomationRepo: f.repo}
	reserve := func(key string) {
		_, err := f.repo.ReserveExternalActivity(owner, f.project.ID, binding, key, "create_github_issue", "github_issue")
		require.NoError(t, err)
	}
	release := func(caller context.Context, key string) error {
		cancelled, cancel := context.WithCancel(caller)
		cancel()
		return releaseGitHubIssueActivityReservationsDetached(cancelled, opts, []models.AutomationBinding{binding}, key)
	}
	reserve("cancelled-owner")
	require.NoError(t, release(owner, "cancelled-owner"), "cancellation alone must not prevent authorized cleanup")
	reserve("cancelled-owner") // release really removed the pending reservation
	audits, err := f.stores.ProjectAccess.Audit(owner, f.project.ID)
	require.NoError(t, err)
	releases := 0
	for _, audit := range audits {
		if audit.Action == "automation.external_released" {
			releases++
			require.Equal(t, models.BootstrapUserID, audit.ActorUserID)
		}
	}
	require.Equal(t, 1, releases, "detached cleanup must retain actor attribution")

	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "cleanup-editor", "long-password-value", "")
	require.NoError(t, err)
	reserve("editor")
	editor := models.WithPrincipal(ctx, models.Principal{UserID: user.UserID, Role: "owner"})
	require.ErrorIs(t, release(editor, "editor"), persistence.ErrProjectDenied)
	_, err = f.repo.ReserveExternalActivity(owner, f.project.ID, binding, "editor", "create_github_issue", "github_issue")
	require.Error(t, err, "denied cleanup must preserve the pending reservation")

	entry := &models.SlackAuthorizedUser{ProjectID: f.project.ID, SlackUserID: "cleanup-sender"}
	require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
	require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "cleanup-workspace", models.BootstrapUserID))
	actor := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: f.project.ID, TeamID: "cleanup-workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	reserve("revoked")
	require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	require.ErrorIs(t, release(actor, "revoked"), persistence.ErrProjectDenied, "detaching cancellation must preserve the revoked sender context")
	_, err = f.repo.ReserveExternalActivity(owner, f.project.ID, binding, "revoked", "create_github_issue", "github_issue")
	require.Error(t, err, "revoked cleanup must preserve the pending reservation")
}
