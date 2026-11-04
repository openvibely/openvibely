package service

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSlackSenderCannotInheritConnectionIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := &SlackService{workerSvc: worker, slackAuthRepo: env.SlackAuth, userProjects: map[string]string{"T123:U123": "default", "OTHER:U123": "default"}}
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	row := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, env.SlackAuth.Create(owner, row))
	msg := slackIncomingMessage{TeamID: "T123", UserID: "U123", EventKey: "event"}
	_, err := svc.senderPrincipalContext(owner, msg)
	require.Error(t, err)
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "slack-sender", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", row.ID, "T123", user.UserID))
	resolved, err := svc.senderPrincipalContext(owner, msg)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(resolved)
	require.True(t, ok)
	require.Equal(t, user.UserID, principal.UserID)
	msg.TeamID = "OTHER"
	_, err = svc.senderPrincipalContext(owner, msg)
	require.Error(t, err)
	msg.TeamID = "T123"
	// Rebinding cannot replace the principal captured earlier in this event.
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", row.ID, "T123", models.BootstrapUserID))
	_, err = svc.senderPrincipalContext(resolved, msg)
	require.Error(t, err)
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", row.ID, "T123", user.UserID))
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	_, err = svc.senderPrincipalContext(resolved, msg)
	require.Error(t, err)
	// Even an already processed event must recheck its sender before acknowledging reuse.
	svc.processedMessageEvents = map[string]time.Time{"event": time.Now()}
	acked := false
	svc.processIncoming(owner, msg, func() { acked = true })
	require.False(t, acked)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	resolved, err = svc.senderPrincipalContext(ctx, msg)
	require.NoError(t, err)
	principal, _ = models.PrincipalFromContext(resolved)
	require.Equal(t, models.BootstrapUserID, principal.UserID)
}
