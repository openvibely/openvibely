package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestOAuthCompletionRechecksSavedActor(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "oauth-admin", "long-password-value", "")
	require.NoError(t, err)
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Revoke after admission but before credentials are persisted.
		err := tc.stores.Organizations.ChangeMember(context.Background(), models.BootstrapUserID, user.UserID, "member", false)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-secret","expires_in":3600}`))
	}))
	defer provider.Close()
	h.oauthIdentityResolver = nil
	flow := &oauthPendingFlow{ActorUserID: user.UserID, Provider: models.ProviderAnthropic, TokenURL: provider.URL, CreatedAt: time.Now(), State: "actor-revoked-test"}
	_, err = h.exchangeOAuthCodeAndSaveTokensContext(ctx, flow, "code", flow.State)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, 1, calls, "one exchange but no credential save after demotion")
	// Even an owner principal on the callback cannot lend permission to its actor.
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err = h.exchangeOAuthCodeAndSaveTokensContext(owner, flow, "code", flow.State)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, 1, calls)
	oauthFlowsMu.Lock()
	oauthFlows[flow.State] = flow
	oauthFlowsMu.Unlock()
	result := h.completeOAuthFlow(flow.State, "code")
	require.ErrorIs(t, result.Err, authz.ErrDenied)
	require.Equal(t, oauthCompletionInvalidState, h.completeOAuthFlow(flow.State, "code").Outcome)
	require.Equal(t, 1, calls)
	flow.ActorUserID = ""
	_, err = h.exchangeOAuthCodeAndSaveTokensContext(owner, flow, "code", flow.State)
	require.ErrorIs(t, err, authz.ErrDenied)
	flow.ActorUserID = models.BootstrapUserID
	require.NoError(t, h.requireOAuthActor(ctx, flow.ActorUserID))
	flow.ActorUserID = user.UserID
	_, err = h.exchangeCustomOAuthCodeAndSaveTokens(flow, "code")
	require.ErrorIs(t, err, authz.ErrDenied, "custom exchange denies before parsing config or making requests")
}
