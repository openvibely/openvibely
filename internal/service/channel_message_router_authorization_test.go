package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestChannelMessageRouterRechecksProjectRole(t *testing.T) {
	ctx, env, _, _, _, _, _, project, router, slack, _, _, _ := setupChannelMessageRouterTest(t)
	require.NoError(t, env.Fixtures.SeedChannelTarget(ctx, models.ChannelTarget{ID: "slack-home", ProjectID: project.ID, Platform: "slack", TargetID: "C123", Home: true}))
	router.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)

	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "router-bob", "bob-long-password", "")
	require.NoError(t, err)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, "viewer"))

	// Background callers without a run-as principal are denied in auth mode.
	require.False(t, router.Send(ctx, project.ID, SendMessageRequest{Target: "slack", Message: "anon"}).OK)
	_, err = router.ListTargets(ctx, project.ID, 10, 0)
	require.Error(t, err)

	// Viewers may list targets but not send or test.
	page, err := router.ListTargets(bobCtx, project.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, page.Targets, 1)
	require.False(t, router.Send(bobCtx, project.ID, SendMessageRequest{Target: "slack", Message: "viewer"}).OK)
	require.False(t, router.SendDirectTarget(bobCtx, project.ID, ChannelTarget{Platform: "slack", TargetID: "C9"}, SendMessageRequest{Message: "viewer"}).OK)
	require.Empty(t, slack.text)

	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, "editor"))
	require.True(t, router.Send(bobCtx, project.ID, SendMessageRequest{Target: "slack", Message: "editor"}).OK)
	require.Equal(t, "editor", slack.text)

	// Revocation applies to the next call; no grant is cached.
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, bob.UserID, ""))
	require.False(t, router.Send(bobCtx, project.ID, SendMessageRequest{Target: "slack", Message: "revoked"}).OK)
	require.Equal(t, "editor", slack.text)

	// Auth-off keeps local single-user behavior via the bootstrap owner.
	router.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), true)
	require.True(t, router.Send(ctx, project.ID, SendMessageRequest{Target: "slack", Message: "local"}).OK)
}
