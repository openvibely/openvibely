package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type atomicEmailRuntime struct {
	EmailServiceProvider
	reload func(context.Context) error
}

func (s *atomicEmailRuntime) ReloadFromSettings(ctx context.Context) error { return s.reload(ctx) }

func TestDiscordAndEmailConfigurationAtomicAdmission(t *testing.T) {
	for _, provider := range []string{"discord", "email"} {
		for _, scenario := range []string{"failure", "revoked", "concurrent", "success"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				h, e, _, stores := setupTestHandlerWithStores(t)
				ctx := t.Context()
				org := service.NewOrganizationService(stores.Organizations, "secret")
				invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
				require.NoError(t, err)
				actor, err := org.Accept(ctx, invite, "channel-editor", "long-password-value", "")
				require.NoError(t, err)
				secret, preference := service.DiscordSettingBotToken, service.DiscordSettingSendResponses
				form := url.Values{"discord_bot_token": {"new-secret"}, "discord_send_responses": {"false"}}
				if provider == "email" {
					secret = service.EmailSettingPassword
					preference = service.EmailSettingSendResponses
					form = url.Values{"email_provider": {"gmail"}, "email_address": {"sender@gmail.com"}, "email_password": {"new-secret"}, "email_send_responses": {"false"}}
				}
				old := map[string]string{secret: "old-secret", preference: "true"}
				if provider == "email" {
					old[service.EmailSettingSMTPHost] = "old-host"
					old[service.EmailSettingAddress] = "old@gmail.com"
				}
				require.NoError(t, stores.Settings.SetMany(ctx, old))
				wrapper := &slackConfigurationStore{SettingsStore: stores.Settings}
				want := 200
				switch scenario {
				case "failure":
					wrapper.failure = errors.New("write failed")
					want = 500
				case "revoked":
					wrapper.before = func() {
						require.NoError(t, stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
					}
					want = 403
				case "concurrent":
					wrapper.before = func() { require.NoError(t, stores.Settings.Set(ctx, secret, "concurrent-secret")) }
					want = 409
				}
				h.settingsRepo = wrapper
				reloads := 0
				reload := func(context.Context) error { reloads++; return nil }
				if provider == "discord" {
					h.SetDiscordService(&fakeDiscordService{reloadFn: reload})
				} else {
					h.emailService = &atomicEmailRuntime{reload: reload}
				}
				req := httptest.NewRequest("POST", "/channels/"+provider+"/configure", strings.NewReader(form.Encode())).WithContext(models.WithPrincipal(ctx, actor.Principal))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, want, rec.Code, rec.Body.String())
				if scenario == "success" {
					require.Equal(t, 1, reloads)
					old[secret] = "new-secret"
					old[preference] = "false"
					if provider == "email" {
						old[service.EmailSettingSMTPHost] = "smtp.gmail.com"
						old[service.EmailSettingAddress] = "sender@gmail.com"
					}
				} else {
					require.Zero(t, reloads)
				}
				if scenario == "concurrent" {
					old[secret] = "concurrent-secret"
				}
				for key, value := range old {
					got, err := stores.Settings.Get(ctx, key)
					require.NoError(t, err)
					require.Equal(t, value, got, key)
				}
			})
		}
	}
}

func TestGitHubConfigurationAtomicAdmission(t *testing.T) {
	for _, mode := range []string{service.GitHubAuthModePAT, service.GitHubAuthModeApp} {
		for _, scenario := range []string{"failure", "revoked", "concurrent", "success"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				h, e, _, stores := setupTestHandlerWithStores(t)
				ctx := t.Context()
				org := service.NewOrganizationService(stores.Organizations, "secret")
				invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
				require.NoError(t, err)
				actor, err := org.Accept(ctx, invite, "github-editor", "long-password-value", "")
				require.NoError(t, err)
				old := map[string]string{service.GitHubSettingAuthMode: "pat", service.GitHubSettingPAT: "old-pat", service.GitHubSettingAppPrivateKey: "old-key", service.GitHubSettingAPIEndpoint: "old-endpoint", "github_app_installation_id": "123"}
				require.NoError(t, stores.Settings.SetMany(ctx, old))
				wrapper := &slackConfigurationStore{SettingsStore: stores.Settings}
				want := 200
				switch scenario {
				case "failure":
					wrapper.failure = errors.New("write failed")
					want = 500
				case "revoked":
					wrapper.before = func() {
						require.NoError(t, stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
					}
					want = 403
				case "concurrent":
					wrapper.before = func() { require.NoError(t, stores.Settings.Set(ctx, service.GitHubSettingPAT, "concurrent")) }
					want = 409
				}
				h.settingsRepo = wrapper
				form := url.Values{"github_auth_mode": {mode}, "github_pat": {"new-pat"}, "github_app_id": {"42"}, "github_app_slug": {"new-app"}, "github_app_private_key": {"new-key"}, "github_api_endpoint": {"new-endpoint"}}
				req := httptest.NewRequest("POST", "/channels/github/configure", strings.NewReader(form.Encode())).WithContext(models.WithPrincipal(ctx, actor.Principal))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, want, rec.Code, rec.Body.String())
				if scenario == "success" {
					old[service.GitHubSettingAuthMode] = mode
					old[service.GitHubSettingAPIEndpoint] = "new-endpoint"
					if mode == service.GitHubAuthModePAT {
						old[service.GitHubSettingPAT] = "new-pat"
					} else {
						old[service.GitHubSettingAppPrivateKey] = "new-key"
						old["github_app_installation_id"] = ""
					}
				}
				if scenario == "concurrent" {
					old[service.GitHubSettingPAT] = "concurrent"
				}
				for key, value := range old {
					got, err := stores.Settings.Get(ctx, key)
					require.NoError(t, err)
					require.Equal(t, value, got, key)
				}
			})
		}
	}
}
