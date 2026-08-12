package pages

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

// ChannelsSettingsView contains all channel state rendered by the Channels settings page and fragment.
type ChannelsSettingsView struct {
	TelegramToken                string
	IsBotRunning                 bool
	AuthorizedUsers              []models.TelegramAuthorizedUser
	SlackAuthorizedUsers         []models.SlackAuthorizedUser
	DiscordAuthorizedUsers       []models.DiscordAuthorizedUser
	CurrentProjectID             string
	SendResponses                bool
	RichMessagesV2               bool
	GitHubStatus                 service.GitHubConnectionStatus
	GitHubAuthMode               string
	GitHubAppID                  string
	GitHubAppSlug                string
	GitHubPrivateKeyValue        string
	GitHubPATValue               string
	GitHubAPIEndpoint            string
	GitHubHasPrivateKey          bool
	GitHubHasPAT                 bool
	SlackStatus                  service.SlackConnectionStatus
	SlackClientID                string
	SlackClientSecret            string
	SlackAppToken                string
	SlackBotToken                string
	SlackBotTokenMode            string
	SlackHasClientID             bool
	SlackHasClientSecret         bool
	SlackHasAppToken             bool
	SlackHasBotToken             bool
	SlackSendResponses           bool
	DiscordStatus                service.DiscordConnectionStatus
	DiscordBotToken              string
	DiscordSendResponses         bool
	EmailStatus                  service.EmailConnectionStatus
	EmailAuthorizedSenders       []models.EmailAuthorizedSender
	EmailPasswordValue           string
	EmailSendResponses           bool
	EmailSkipAttachments         bool
	EmailMarkExistingSeenOnStart bool
	EmailPollIntervalSeconds     string
	HasTelegramChannel           bool
	HasGitHubChannel             bool
	HasSlackChannel              bool
	HasDiscordChannel            bool
	HasEmailChannel              bool
	Webhooks                     []models.WebhookEndpoint
	AgentPickerOptions           []repository.AgentPickerOption
	WebhookAgents                map[string][]models.WebhookEndpointAgent
	ChannelTargets               []models.ChannelTarget
	SendMessageExplicitTargets   bool
}
