package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type slackConfigurationStore struct {
	persistence.SettingsStore
	before  func()
	failure error
}

func (s *slackConfigurationStore) CompareAndSetMany(ctx context.Context, expected, values, guards map[string]string) (bool, error) {
	if s.before != nil {
		s.before()
	}
	if s.failure != nil {
		return false, s.failure
	}
	return s.SettingsStore.CompareAndSetMany(ctx, expected, values, guards)
}

func TestSlackConfigurationAtomicAdmission(t *testing.T) {
	for _, scenario := range []string{"failure", "concurrent-edit", "revoked", "success"} {
		t.Run(scenario, func(t *testing.T) {
			h, e, _, stores := setupTestHandlerWithStores(t)
			ctx := t.Context()
			org := service.NewOrganizationService(stores.Organizations, "secret")
			invitation, err := org.Invite(ctx, models.BootstrapUserID, "admin")
			require.NoError(t, err)
			actor, err := org.Accept(ctx, invitation, "slack-config", "long-password-value", "")
			require.NoError(t, err)
			old := map[string]string{service.SlackSettingClientID: "old-id", service.SlackSettingClientSecret: "old-secret", service.SlackSettingAppToken: "old-app", service.SlackSettingBotTokenSource: service.SlackBotTokenSourceOAuth, service.SlackSettingBotTokenOverride: "old-override", service.SlackSettingSendResponses: "true"}
			require.NoError(t, stores.Settings.SetMany(ctx, old))
			wrapper := &slackConfigurationStore{SettingsStore: stores.Settings}
			want := http.StatusOK
			switch scenario {
			case "failure":
				wrapper.failure = errors.New("write unavailable")
				want = 500
			case "concurrent-edit":
				wrapper.before = func() { require.NoError(t, stores.Settings.Set(ctx, service.SlackSettingClientID, "concurrent-id")) }
				want = 409
			case "revoked":
				wrapper.before = func() {
					require.NoError(t, stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
				}
				want = 403
			}
			h.settingsRepo = wrapper
			reloads := 0
			h.SetSlackService(&fakeSlackService{reloadFn: func(context.Context) error { reloads++; return nil }})
			form := url.Values{"slack_client_id": {"new-id"}, "slack_client_secret": {"new-secret"}, "slack_app_token": {"new-app"}, "slack_bot_token_mode": {"manual"}, "slack_bot_token": {"new-override"}, "slack_send_responses": {"false"}}
			req := httptest.NewRequest("POST", "/channels/slack/configure", strings.NewReader(form.Encode())).WithContext(models.WithPrincipal(ctx, actor.Principal))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, want, rec.Code, rec.Body.String())
			if scenario == "success" {
				require.Equal(t, 1, reloads)
				old[service.SlackSettingClientID] = "new-id"
				old[service.SlackSettingClientSecret] = "new-secret"
				old[service.SlackSettingAppToken] = "new-app"
				old[service.SlackSettingBotTokenSource] = "manual"
				old[service.SlackSettingBotTokenOverride] = "new-override"
				old[service.SlackSettingSendResponses] = "false"
			} else {
				require.Zero(t, reloads)
			}
			if scenario == "concurrent-edit" {
				old[service.SlackSettingClientID] = "concurrent-id"
			}
			for key, value := range old {
				got, err := stores.Settings.Get(ctx, key)
				require.NoError(t, err)
				require.Equal(t, value, got, key)
			}
		})
	}
}
