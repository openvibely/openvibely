package handler

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type telegramConfigurationStore struct {
	persistence.SettingsStore
	before  func()
	failure error
}

func (s *telegramConfigurationStore) SetMany(ctx context.Context, values map[string]string) error {
	if s.before != nil {
		s.before()
	}
	if s.failure != nil {
		return s.failure
	}
	return s.SettingsStore.SetMany(ctx, values)
}
func TestTelegramConfigurationAtomicAdmission(t *testing.T) {
	original := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = original })
	for _, scenario := range []string{"failure", "revoked", "success"} {
		t.Run(scenario, func(t *testing.T) {
			h, e, _, stores := setupTestHandlerWithStores(t)
			ctx := t.Context()
			org := service.NewOrganizationService(stores.Organizations, "secret")
			invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
			require.NoError(t, err)
			actor, err := org.Accept(ctx, invite, "telegram-config", "long-password-value", "")
			require.NoError(t, err)
			require.NoError(t, stores.Settings.SetMany(ctx, map[string]string{service.TelegramSettingBotToken: "old", service.TelegramSettingRichMessagesV2: "false"}))
			wrapper := &telegramConfigurationStore{SettingsStore: stores.Settings}
			want := 200
			if scenario == "failure" {
				wrapper.failure = errors.New("write failed")
				want = 500
			}
			if scenario == "revoked" {
				wrapper.before = func() {
					require.NoError(t, stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
				}
				want = 403
			}
			h.settingsRepo = wrapper
			h.telegramService = &service.TelegramService{}
			updates := 0
			updateTelegramServiceToken = func(_ *service.TelegramService, token string) error {
				updates++
				require.Equal(t, "new", token)
				return nil
			}
			form := url.Values{"token": {"new"}, "telegram_rich_messages_v2": {"true"}}
			req := httptest.NewRequest("POST", "/channels/telegram", strings.NewReader(form.Encode())).WithContext(models.WithPrincipal(ctx, actor.Principal))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, want, rec.Code, rec.Body.String())
			token, err := stores.Settings.Get(ctx, service.TelegramSettingBotToken)
			require.NoError(t, err)
			rich, err := stores.Settings.Get(ctx, service.TelegramSettingRichMessagesV2)
			require.NoError(t, err)
			if scenario == "success" {
				require.Equal(t, 1, updates)
				require.Equal(t, "new", token)
				require.Equal(t, "true", rich)
			} else {
				require.Zero(t, updates)
				require.Equal(t, "old", token)
				require.Equal(t, "false", rich)
			}
		})
	}
}
