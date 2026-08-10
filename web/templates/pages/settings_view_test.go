package pages

import "github.com/openvibely/openvibely/internal/service"

func defaultChannelsSettingsView(projectID string) ChannelsSettingsView {
	return ChannelsSettingsView{
		CurrentProjectID:           projectID,
		SendResponses:              true,
		RichMessagesV2:             true,
		GitHubAuthMode:             service.GitHubAuthModePAT,
		SlackBotTokenMode:          service.SlackBotTokenSourceOAuth,
		SlackSendResponses:         true,
		DiscordSendResponses:       true,
		EmailSendResponses:         true,
		EmailSkipAttachments:       true,
		EmailPollIntervalSeconds:   "60",
		SendMessageExplicitTargets: false,
	}
}
