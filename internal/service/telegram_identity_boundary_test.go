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

func TestTelegramAuthenticatedIngressRequiresVerifiedSender(t *testing.T) {
	env := teststores.New(t)
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	lookedUp := false
	s := &TelegramService{workerSvc: worker, projectRepo: env.Projects, telegramUserProjectRepo: env.TelegramUserProjects, userProjects: map[int64]string{71: "default"}, telegramAuthRepo: telegramAuthorizationStoreStub{
		isAuthorizedAnywhere: func(context.Context, int64, string) (bool, error) { lookedUp = true; return true, nil },
	}}
	require.False(t, s.checkAuthorization(71, "trusted-owner", "default"))
	require.False(t, s.checkAuthorization(71, "trusted-owner", "foreign-project"))
	require.False(t, lookedUp, "transport allowlist and username must not establish application identity")
	require.Equal(t, telegramIdentityUnavailable, s.handleStart(71))
	require.Equal(t, telegramIdentityUnavailable, s.handleProject(71, ""))
	require.Equal(t, telegramIdentityUnavailable, s.handleProject(71, "foreign-project"))
	result, handled := s.handleNaturalLanguageProjectCommand(71, "list projects")
	require.True(t, handled)
	require.Equal(t, telegramIdentityUnavailable, result)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID, Role: "owner"})
	require.ErrorIs(t, s.setTelegramActiveProject(owner, 71, "foreign-project"), authz.ErrDenied)
	require.Equal(t, "default", s.userProjects[71])
	handedOff := false
	require.True(t, s.handleChatMessageWithDurableHandoff(owner, nil, func() { handedOff = true }), "rejected input is consumed without invoking persistence or model work")
	require.False(t, handedOff)
	handlers := s.telegramActionHandlers("default", 1, 71, nil)
	require.NotEmpty(t, handlers)
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handler(owner, json.RawMessage(`{}`))
			require.ErrorIs(t, err, authz.ErrDenied, "borrowed owner must not authorize Telegram tools")
		})
	}
	// The guard is checked when each action executes, not just at runtime creation.
	worker.runAuthOff = true
	require.True(t, s.checkAuthorization(71, "trusted-owner", "default"))
	require.True(t, lookedUp, "auth-off retains the existing external allowlist")
	worker.runAuthOff = false
	_, err := handlers["create_task"](owner, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	s.telegramAuthRepo = nil
	require.False(t, s.checkAuthorization(71, "", "default"), "missing allowlist cannot bypass authenticated identity checks")
}
