package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
)

func TestChannelsConfigureSendResponses(t *testing.T) {
	for _, channel := range []struct {
		name, key, otherKey string
		credentials         url.Values
	}{
		{"slack", service.SlackSettingSendResponses, service.DiscordSettingSendResponses, url.Values{
			"slack_client_id": {"client-id"}, "slack_client_secret": {"client-secret"}, "slack_app_token": {"app-token"},
		}},
		{"discord", service.DiscordSettingSendResponses, service.SlackSettingSendResponses, url.Values{
			"discord_bot_token": {"bot-token"},
		}},
	} {
		t.Run(channel.name, func(t *testing.T) {
			for _, tc := range []struct{ name, existing, input, want string }{
				{"explicit true", "false", "true", "true"},
				{"explicit false", "true", "false", "false"},
				{"blank preserves false", "false", "", "false"},
				{"blank preserves true", "true", "", "true"},
				{"blank initializes unset", "", "", "true"},
				{"normalized explicit false", "true", " FALSE ", "false"},
			} {
				for _, htmx := range []bool{false, true} {
					mode := "redirect"
					if htmx {
						mode = "htmx"
					}
					t.Run(tc.name+"/"+mode, func(t *testing.T) {
						h, e, _ := setupTestHandler(t)
						ctx := context.Background()
						if tc.existing != "" {
							if err := h.settingsRepo.Set(ctx, channel.key, tc.existing); err != nil {
								t.Fatal(err)
							}
						}
						if err := h.settingsRepo.Set(ctx, channel.otherKey, "other-channel-sentinel"); err != nil {
							t.Fatal(err)
						}
						reloads := 0
						reload := func(ctx context.Context) error {
							reloads++
							got, err := h.settingsRepo.Get(ctx, channel.key)
							if err != nil || got != tc.want {
								t.Errorf("preference at reload = %q, %v; want %q", got, err, tc.want)
							}
							return nil
						}
						if channel.name == "slack" {
							h.SetSlackService(&fakeSlackService{reloadFn: reload})
						} else {
							h.SetDiscordService(&fakeDiscordService{reloadFn: reload})
						}
						form := url.Values{}
						for key, values := range channel.credentials {
							form[key] = values
						}
						form.Set(channel.name+"_send_responses", tc.input)
						req := httptest.NewRequest(http.MethodPost, "/channels/"+channel.name+"/configure", strings.NewReader(form.Encode()))
						req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						if htmx {
							req.Header.Set("HX-Request", "true")
						}
						rec := httptest.NewRecorder()
						e.ServeHTTP(rec, req)
						if htmx {
							if rec.Code != http.StatusOK {
								t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
							}
							assertChannelsRefreshTrigger(t, rec)
						} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/channels" {
							t.Fatalf("expected redirect to /channels, got %d, %q", rec.Code, rec.Header().Get("Location"))
						}
						if reloads != 1 {
							t.Errorf("reload count = %d; want 1", reloads)
						}
						for key, want := range map[string]string{channel.key: tc.want, channel.otherKey: "other-channel-sentinel"} {
							got, err := h.settingsRepo.Get(ctx, key)
							if err != nil || got != want {
								t.Errorf("setting %s = %q, %v; want %q", key, got, err, want)
							}
						}
					})
				}
			}
		})
	}
}
