package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type removalEmailRuntime struct {
	EmailServiceProvider
	stop func()
}

func (s *removalEmailRuntime) Stop() { s.stop() }

func TestChannelRemovalFailurePreservesCredentialsAndRuntime(t *testing.T) {
	for _, provider := range []string{"github", "slack", "discord", "email"} {
		for _, failure := range []string{"storage", "revocation"} {
			t.Run(provider+"/"+failure, func(t *testing.T) {
				h, e, _, stores := setupTestHandlerWithStores(t)
				ctx := t.Context()
				org := service.NewOrganizationService(stores.Organizations, "secret")
				invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
				require.NoError(t, err)
				actor, err := org.Accept(ctx, invite, "remover", "long-password-value", "")
				require.NoError(t, err)
				reset, _ := channelBulkProviderResets(provider)
				old := map[string]string{}
				for key := range reset {
					old[key] = "preserved"
				}
				require.NoError(t, stores.Settings.SetMany(ctx, old))
				calls := 0
				disconnect := func(context.Context) error { calls++; return nil }
				h.SetGitHubService(&fakeGitHubService{disconnectFn: disconnect})
				h.SetSlackService(&fakeSlackService{disconnectFn: disconnect})
				h.SetDiscordService(&fakeDiscordService{disconnectFn: disconnect})
				h.emailService = &removalEmailRuntime{stop: func() { calls++ }}
				wrapper := &removalConfigurationStore{SettingsStore: stores.Settings}
				if failure == "storage" {
					wrapper.failure = errors.New("storage unavailable")
				} else {
					wrapper.before = func() {
						require.NoError(t, stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
					}
				}
				h.settingsRepo = wrapper
				req := httptest.NewRequest("POST", "/channels/"+provider+"/remove?project_id=default", nil).WithContext(models.WithPrincipal(ctx, actor.Principal))
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, 500, rec.Code)
				require.Zero(t, calls)
				for key, want := range old {
					got, err := stores.Settings.Get(ctx, key)
					require.NoError(t, err)
					require.Equal(t, want, got, key)
				}
			})
		}
	}
}

// Exercise failures at the actual transaction entry point used by each handler.
type removalConfigurationStore struct {
	persistence.SettingsStore
	before  func()
	failure error
}

func (s *removalConfigurationStore) SetMany(ctx context.Context, values map[string]string) error {
	if s.before != nil {
		s.before()
	}
	if s.failure != nil {
		return s.failure
	}
	return s.SettingsStore.SetMany(ctx, values)
}
func (s *removalConfigurationStore) RemoveDiscordChannel(ctx context.Context, projectID string, values map[string]string) error {
	if s.before != nil {
		s.before()
	}
	if s.failure != nil {
		return s.failure
	}
	return s.SettingsStore.RemoveDiscordChannel(ctx, projectID, values)
}

func TestDiscordRemovalReportsDisconnectFailure(t *testing.T) {
	h, e, _, stores := setupTestHandlerWithStores(t)
	require.NoError(t, stores.Settings.Set(t.Context(), service.DiscordSettingBotToken, "old-token"))
	h.SetDiscordService(&fakeDiscordService{disconnectFn: func(context.Context) error { return errors.New("disconnect failed") }})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("POST", "/channels/discord/remove?project_id=default", nil))
	require.Equal(t, 500, rec.Code)
	require.Empty(t, rec.Header().Get("Location"))
	token, err := stores.Settings.Get(t.Context(), service.DiscordSettingBotToken)
	require.NoError(t, err)
	require.Empty(t, token)
}
