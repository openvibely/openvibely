package sqlite

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
)

var _ persistence.Backend = (*Backend)(nil)

// Backend is the SQLite persistence backend: a dedicated writer connection,
// a query-only reader connection, goose migrations, and incremental vacuum.
type Backend struct {
	conns            *database.Connections
	unregisterWriter func()
	stores           persistence.Stores
}

// Open opens (and migrates) the SQLite database at path and builds every store.
func Open(path string, broadcaster *events.Broadcaster) (*Backend, error) {
	conns, err := database.NewReadWrite(path)
	if err != nil {
		return nil, err
	}
	return &Backend{
		conns:            conns,
		unregisterWriter: RegisterDedicatedWriter(conns.Reader, conns.Writer),
		stores:           NewStores(conns.Reader, broadcaster),
	}, nil
}

// NewStores builds every store on an already-migrated SQLite handle.
func NewStores(db *sql.DB, broadcaster *events.Broadcaster) persistence.Stores {
	automations := NewAutomationRepo(db)
	automations.SetBroadcaster(broadcaster)
	executions := NewExecutionRepo(db)
	executions.SetAutomationRepo(automations)
	tasks := NewTaskRepo(db, broadcaster)
	tasks.SetExecutionRepo(executions)
	alerts := NewAlertRepo(db)
	alerts.SetAutomationRepo(automations)
	return persistence.Stores{
		Organizations:        NewOrganizationRepo(db),
		Agents:               NewAgentRepo(db),
		AgentMutations:       NewAgentMutationRepo(db),
		Alerts:               alerts,
		Attachments:          NewAttachmentRepo(db),
		Automations:          automations,
		ChannelTargets:       NewChannelTargetRepo(db),
		ChatAttachments:      NewChatAttachmentRepo(db),
		ChatInputRequests:    NewChatInputRequestRepo(db),
		CustomPersonalities:  NewCustomPersonalityRepo(db),
		DiscordAuth:          NewDiscordAuthRepo(db),
		DiscordTaskContexts:  NewDiscordTaskContextRepo(db),
		DiscordUserProjects:  NewDiscordUserProjectRepo(db),
		EmailAuth:            NewEmailAuthRepo(db),
		EmailInboundReceipts: NewEmailInboundReceiptRepo(db),
		EmailSenderProjects:  NewEmailSenderProjectRepo(db),
		EmailTaskContexts:    NewEmailTaskContextRepo(db),
		Executions:           executions,
		GitHubAuth:           NewGitHubAuthRepo(db),
		GitHubPRFeedback:     NewGitHubPRFeedbackRepo(db),
		Insights:             NewInsightsRepo(db),
		Lifecycle:            NewLifecycleRepo(db),
		LLMConfigs:           NewLLMConfigRepo(db),
		OpenAIAsyncToolCalls: NewOpenAIAsyncToolCallRepo(db),
		Projects:             NewProjectRepo(db),
		ReviewComments:       NewReviewCommentRepo(db),
		Schedules:            NewScheduleRepo(db),
		Settings:             &persistence.UserSettings{SettingsStore: NewSettingsRepo(db), Organizations: NewOrganizationRepo(db)},
		SkillAnalytics:       NewSkillAnalyticsRepo(db),
		SlackAuth:            NewSlackAuthRepo(db),
		SlackInboundReceipts: NewSlackInboundReceiptRepo(db),
		SlackTaskContexts:    NewSlackTaskContextRepo(db),
		SlackUserProjects:    NewSlackUserProjectRepo(db),
		TaskCommitStats:      NewTaskCommitStatRepo(db),
		TaskGoals:            NewTaskGoalRepo(db),
		TaskPullRequests:     NewTaskPullRequestRepo(db),
		Tasks:                tasks,
		TelegramAuth:         NewTelegramAuthRepo(db),
		TelegramUserProjects: NewTelegramUserProjectRepo(db),
		ThreadInputs:         NewThreadInputRepo(db),
		Upcoming:             NewUpcomingRepo(db),
		Usage:                NewUsageRepo(db),
		Webhooks:             NewWebhookRepo(db),
		Workers:              NewWorkerRepo(db),
		XAuth:                NewXAuthRepo(db),
		XInboundReceipts:     NewXInboundReceiptRepo(db),
		XTaskContexts:        NewXTaskContextRepo(db),
		XUserProjects:        NewXUserProjectRepo(db),
	}
}

func (b *Backend) Stores() persistence.Stores { return b.stores }

func (b *Backend) SchemaVersion(ctx context.Context) (int, error) {
	return database.SchemaVersion(ctx, b.conns.Reader)
}

// StartMaintenance starts incremental vacuum on the writer until ctx is cancelled.
func (b *Backend) StartMaintenance(ctx context.Context) {
	database.StartIncrementalVacuum(ctx, b.conns.Writer)
}

func (b *Backend) Close() error {
	b.unregisterWriter()
	return b.conns.Close()
}
