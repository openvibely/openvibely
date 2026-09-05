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
	XAuthorizedUsers             []models.XAuthorizedUser
	XStatus                      service.XConnectionStatus
	XHasConsumerKey              bool
	XHasConsumerSecret           bool
	XHasAccessToken              bool
	XHasAccessTokenSecret        bool
	XPollIntervalSeconds         string
	XSendResponses               bool
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
	HasXChannel                  bool
	HasEmailChannel              bool
	Webhooks                     []models.WebhookEndpoint
	WebhooksPageOffset           int
	WebhooksSearch               string
	ChannelTypeFilter            string
	ConnectionStateFilter        string
	WebhookEnabledFilter         string
	WebhooksHasMore              bool
	AgentPickerOptions           []repository.AgentPickerOption
	WebhookAgents                map[string][]models.WebhookEndpointAgent
	ChannelTargets               []models.ChannelTarget
	SendMessageExplicitTargets   bool
}
