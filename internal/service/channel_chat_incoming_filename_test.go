package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestChannelChatIncomingFileNames(t *testing.T) {
	for _, platform := range []string{"slack", "discord"} {
		t.Run(platform, func(t *testing.T) {
			for _, tc := range []struct {
				name, id, want string
			}{
				{"../../plan.txt", "123", "plan.txt"},
				{" \t../../plan.txt\n", "123", "plan.txt"},
				{"", "123", platform + "-123"},
				{" \t\n", " 123 ", platform + "-123"},
				{"", "", platform + "-attachment"},
				{" \t\n", " \t", platform + "-attachment"},
				{".", "123", platform + "-123"},
				{".", "", platform + "-attachment"},
				{"/", "123", platform + "-123"},
				{"///", "", platform + "-attachment"},
			} {
				t.Run(tc.name+"/"+tc.id, func(t *testing.T) {
					if platform == "slack" {
						file := slackIncomingFile{Name: tc.name, ID: tc.id}
						require.Equal(t, tc.want, slackSafeFileName(file))
						require.Equal(t, tc.want, slackFileDisplayName(file))
					} else {
						file := discordIncomingAttachment{FileName: tc.name, ID: tc.id}
						require.Equal(t, tc.want, discordSafeFileName(file))
						require.Equal(t, tc.want, discordFileDisplayName(file))
					}
				})
			}
		})
	}
	require.Equal(t, "title.md", slackSafeFileName(slackIncomingFile{Name: " \t", Title: " ../../title.md "}))
	require.Equal(t, "plan.txt", slackSafeFileName(slackIncomingFile{Name: " ../../plan.txt ", Title: "title.md"}))
	require.Equal(t, "slack-123", slackSafeFileName(slackIncomingFile{Name: ".", Title: "title.md", ID: "123"}))
}

func TestChannelChatDownloadsUseNormalizedFileNames(t *testing.T) {
	ctx := context.Background()
	for _, platform := range []string{"slack", "discord"} {
		t.Run(platform, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/failure" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("plan"))
			}))
			defer server.Close()

			var download func(name, path string) ([]models.ChatAttachment, error)
			if platform == "slack" {
				db := teststores.New(t)
				_, _, _, _, _, _, _, settings, _, _ := newSlackAttachmentTestRepos(t, db)
				require.NoError(t, settings.Set(ctx, SlackSettingBotTokenOverride, "xoxb-test"))
				require.NoError(t, settings.Set(ctx, SlackSettingBotTokenSource, SlackBotTokenSourceManual))
				svc := NewSlackService(settings, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				useSlackFileServer(t, svc, server)
				download = func(name, path string) ([]models.ChatAttachment, error) {
					return svc.downloadSlackFiles(ctx, []slackIncomingFile{{ID: "123", Name: name, Mimetype: "text/plain", URLPrivateDownload: slackTestURL(path)}})
				}
			} else {
				svc, _, _, _, _, _, _ := newDiscordServiceForTest(t)
				useDiscordFileServers(t, svc, map[string]*httptest.Server{"cdn.discordapp.com": server})
				download = func(name, path string) ([]models.ChatAttachment, error) {
					return svc.downloadDiscordFiles(ctx, []discordIncomingAttachment{{ID: "123", FileName: name, ContentType: "text/plain", URL: "https://cdn.discordapp.com" + path}})
				}
			}
			for _, tc := range []struct{ name, want string }{
				{" ../../plan.txt ", "plan.txt"},
				{" \t", platform + "-123"},
			} {
				attachments, err := download(tc.name, "/success")
				require.NoError(t, err)
				require.Len(t, attachments, 1)
				require.Equal(t, tc.want, attachments[0].FileName)
				cleanupChannelChatAttachmentSourceDirs(attachments)

				_, err = download(tc.name, "/failure")
				require.Error(t, err)
				require.Contains(t, err.Error(), "\""+tc.want+"\"")
				require.NotContains(t, err.Error(), "../")
			}
		})
	}
}
