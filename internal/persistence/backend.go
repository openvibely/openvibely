package persistence

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

// Backend is an opened persistence implementation. Each backend owns its
// connections, schema migrations, and background maintenance.
type Backend interface {
	Stores() Stores
	SchemaVersion(ctx context.Context) (int, error)
	StartMaintenance(ctx context.Context)
	Close() error
}

// AgentMutationStore records the agent configuration mutation audit log.
type AgentMutationStore interface {
	Create(ctx context.Context, m *models.AgentConfigMutation) error
}

// Stores is the full set of stores a backend provides to the application.
type Stores struct {
	Organizations        OrganizationStore
	Agents               AgentStore
	AgentMutations       AgentMutationStore
	Alerts               AlertStore
	Attachments          AttachmentStore
	Automations          AutomationStore
	ChannelTargets       ChannelTargetStore
	ChatAttachments      ChatAttachmentStore
	ChatInputRequests    ChatInputRequestStore
	CustomPersonalities  CustomPersonalityStore
	DiscordAuth          DiscordAuthStore
	DiscordTaskContexts  DiscordTaskContextStore
	DiscordUserProjects  DiscordUserProjectStore
	EmailAuth            EmailAuthStore
	EmailInboundReceipts EmailInboundReceiptStore
	EmailSenderProjects  EmailSenderProjectStore
	EmailTaskContexts    EmailTaskContextStore
	Executions           ExecutionStore
	GitHubAuth           GitHubAuthStore
	GitHubPRFeedback     GitHubPRFeedbackStore
	Insights             InsightsStore
	Lifecycle            LifecycleStore
	LLMConfigs           LLMConfigStore
	OpenAIAsyncToolCalls OpenAIAsyncToolCallStore
	Projects             ProjectStore
	ReviewComments       ReviewCommentStore
	Schedules            ScheduleStore
	Settings             SettingsStore
	SkillAnalytics       SkillAnalyticsStore
	SlackAuth            SlackAuthStore
	SlackInboundReceipts SlackInboundReceiptStore
	SlackTaskContexts    SlackTaskContextStore
	SlackUserProjects    SlackUserProjectStore
	TaskCommitStats      TaskCommitStatStore
	TaskGoals            TaskGoalStore
	TaskPullRequests     TaskPullRequestStore
	Tasks                TaskStore
	TelegramAuth         TelegramAuthStore
	TelegramUserProjects TelegramUserProjectStore
	ThreadInputs         ThreadInputStore
	Upcoming             UpcomingStore
	Usage                UsageStore
	Webhooks             WebhookStore
	Workers              WorkerStore
	XAuth                XAuthStore
	XInboundReceipts     XInboundReceiptStore
	XTaskContexts        XTaskContextStore
	XUserProjects        XUserProjectStore
}
