package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestChannelChatAttachmentContextAndImages_SharedInlineTextLimitAcrossServices(t *testing.T) {
	require.Equal(t, int64(102400), int64(channelChatMaxInlineTextAttachmentSize))

	dir := t.TempDir()
	atLimitPath := filepath.Join(dir, "at-limit.txt")
	overLimitPath := filepath.Join(dir, "over-limit.txt")
	atLimitContent := strings.Repeat("a", 102400)
	overLimitContent := strings.Repeat("b", 102401)
	require.NoError(t, os.WriteFile(atLimitPath, []byte(atLimitContent), 0600))
	require.NoError(t, os.WriteFile(overLimitPath, []byte(overLimitContent), 0600))

	chatAttachments := []models.ChatAttachment{
		{FileName: "at-limit.txt", FilePath: atLimitPath, MediaType: "text/plain", FileSize: 102400},
		{FileName: "over-limit.txt", FilePath: overLimitPath, MediaType: "text/plain", FileSize: 102401},
		{FileName: "diagram.png", FilePath: filepath.Join(dir, "diagram.png"), MediaType: "image/png", FileSize: 17},
	}
	servicePaths := []struct {
		name    string
		convert func([]models.ChatAttachment) (string, []models.Attachment)
	}{
		{name: "Discord", convert: discordAttachmentContextAndImages},
		{name: "Email", convert: emailAttachmentContextAndImages},
		{name: "Slack", convert: slackAttachmentContextAndImages},
		{name: "Telegram", convert: telegramAttachmentContextAndImages},
	}

	for _, servicePath := range servicePaths {
		t.Run(servicePath.name, func(t *testing.T) {
			contextText, images := servicePath.convert(chatAttachments)

			require.Contains(t, contextText, "File: at-limit.txt\n```\n"+atLimitContent+"\n```")
			require.Contains(t, contextText, "File: over-limit.txt (attached, 102401 bytes - too large to include inline)")
			require.NotContains(t, contextText, "diagram.png")
			require.Len(t, images, 1)
			require.Equal(t, "diagram.png", images[0].FileName)
			require.Equal(t, int64(17), images[0].FileSize)
		})
	}
}
