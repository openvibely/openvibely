package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestEmailAuthenticatedIngressRequiresVerifiedSender(t *testing.T) {
	env := teststores.New(t)
	svc, _, _, project, _, _ := newEmailAttachmentTestServiceWithStores(t, env)
	svc.workerSvc.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID, Role: "owner"})
	msg := EmailInboundMessage{FromAddress: "alice@example.com", Body: "run a task", Subject: "test"}
	id, allowed, err := svc.resolveAuthorizedProjectForInbound(ctx, msg.FromAddress)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.False(t, allowed)
	require.Empty(t, id)
	called := false
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool { called = true; return true }
	require.False(t, svc.ProcessIncoming(ctx, msg))
	require.False(t, called)
	svc.processIncomingMessageFn = nil
	runs := 0
	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) { runs++ })
	require.False(t, svc.ProcessIncoming(ctx, msg))
	require.False(t, svc.processIncomingMessageWithNormalizedSelfAddress(ctx, msg, "mailbox", "key", "").handled)
	require.Zero(t, runs)
	connected := false
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) {
		connected = true
		return nil, authz.ErrDenied
	}
	svc.pollOnce(ctx, EmailRuntimeConfig{})
	require.ErrorIs(t, svc.markUnreadSeenForRun(ctx, EmailRuntimeConfig{}, nil), authz.ErrDenied)
	require.False(t, svc.acquirePollSideEffects(ctx, nil))
	require.False(t, connected, "denied polling must not connect or mark mail seen")
	handlers := svc.emailActionHandlers(project.ID, msg.FromAddress)
	require.NotEmpty(t, handlers)
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handler(ctx, json.RawMessage(`{}`))
			require.ErrorIs(t, err, authz.ErrDenied)
		})
	}
	selected, err := env.EmailSenderProjects.GetSenderProject(ctx, msg.FromAddress)
	require.NoError(t, err)
	require.Equal(t, project.ID, selected)
	svc.workerSvc.runAuthOff = true
	id, allowed, err = svc.resolveAuthorizedProjectForInbound(ctx, msg.FromAddress)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, project.ID, id)
	require.True(t, svc.ProcessIncoming(ctx, msg))
	require.Equal(t, 1, runs)
	svc.workerSvc.runAuthOff = false
	require.False(t, svc.ProcessIncoming(ctx, msg))
	require.Equal(t, 1, runs)
	for _, handler := range handlers {
		_, err := handler(ctx, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied)
	}
}
