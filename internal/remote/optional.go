package remote

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

// Optional store methods reached through type assertions. Remote calls name the
// optional method so the owning server uses the same fast path.

type displayStore interface {
	ListDisplayExecutions(context.Context, string, string, string, int, int) ([]models.Execution, bool, error)
	ReadProjectDisplayTail(context.Context, string, string, int64, int) (models.DisplayTail, error)
	ReadProjectDisplayTailRevision(context.Context, string, string, int64, int, string) (models.DisplayTail, error)
	GetDisplayExecutionTail(context.Context, string) (*models.Execution, error)
	GetDisplayExecution(context.Context, string) (*models.Execution, error)
}

type replayStore interface {
	ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	ReplayContinuationRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	AppendEncodedReplayChanges(context.Context, models.ConversationOwner, []models.ConversationChange) ([]models.ConversationChange, error)
}

func init() {
	paramNames["Executions"]["ListDisplayExecutions"] = []string{"projectID", "taskID", "before", "limit", "budget"}
	paramNames["Executions"]["ReadProjectDisplayTail"] = []string{"projectID", "id", "before", "budget"}
	paramNames["Executions"]["ReadProjectDisplayTailRevision"] = []string{"projectID", "id", "before", "budget", "revision"}
	paramNames["Executions"]["GetDisplayExecutionTail"] = []string{"id"}
	paramNames["Executions"]["GetDisplayExecution"] = []string{"id"}
	paramNames["Executions"]["ReplayRowsByExecutionIDs"] = []string{"ids"}
	paramNames["Executions"]["ReplayContinuationRowsByExecutionIDs"] = []string{"ids"}
	paramNames["Executions"]["GetChatCompactionCheckpointIfChanged"] = []string{"scopeType", "scopeID", "previous"}
}

func (s *ExecutionStore) ListDisplayExecutions(ctx context.Context, projectID, taskID, before string, limit, budget int) (r0 []models.Execution, r1 bool, r2 error) {
	d := s.local.(displayStore)
	if s.r.Remote(ctx, "Executions", "ListDisplayExecutions", []any{projectID, taskID, before, limit, budget}, func() []any {
		r0, r1, r2 = d.ListDisplayExecutions(ctx, projectID, taskID, before, limit, budget)
		return []any{r0, r1, r2}
	}, &r0, &r1, &r2) {
		return
	}
	return d.ListDisplayExecutions(ctx, projectID, taskID, before, limit, budget)
}

func (s *ExecutionStore) ReadProjectDisplayTail(ctx context.Context, projectID, id string, before int64, budget int) (r0 models.DisplayTail, r1 error) {
	d := s.local.(displayStore)
	if s.r.Remote(ctx, "Executions", "ReadProjectDisplayTail", []any{projectID, id, before, budget}, nil, &r0, &r1) {
		return
	}
	return d.ReadProjectDisplayTail(ctx, projectID, id, before, budget)
}

func (s *ExecutionStore) ReadProjectDisplayTailRevision(ctx context.Context, projectID, id string, before int64, budget int, revision string) (r0 models.DisplayTail, r1 error) {
	d := s.local.(displayStore)
	if s.r.Remote(ctx, "Executions", "ReadProjectDisplayTailRevision", []any{projectID, id, before, budget, revision}, nil, &r0, &r1) {
		return
	}
	return d.ReadProjectDisplayTailRevision(ctx, projectID, id, before, budget, revision)
}

func (s *ExecutionStore) GetDisplayExecutionTail(ctx context.Context, id string) (r0 *models.Execution, r1 error) {
	if s.r.Remote(ctx, "Executions", "GetDisplayExecutionTail", []any{id}, nil, &r0, &r1) {
		return
	}
	return s.local.(displayStore).GetDisplayExecutionTail(ctx, id)
}

func (s *ExecutionStore) GetDisplayExecution(ctx context.Context, id string) (r0 *models.Execution, r1 error) {
	if s.r.Remote(ctx, "Executions", "GetDisplayExecution", []any{id}, nil, &r0, &r1) {
		return
	}
	return s.local.(displayStore).GetDisplayExecution(ctx, id)
}

func (s *ExecutionStore) AppendEncodedReplayChanges(ctx context.Context, owner models.ConversationOwner, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	if s.r.Refuse(ctx, "Executions", []any{owner}) {
		return nil, ErrUnsupported
	}
	return s.local.(replayStore).AppendEncodedReplayChanges(ctx, owner, changes)
}

func (s *TelegramAuthStore) ResolveApplicationUser(ctx context.Context, id int64) (string, error) {
	return s.local.(interface {
		ResolveApplicationUser(context.Context, int64) (string, error)
	}).ResolveApplicationUser(ctx, id)
}

func (s *ExecutionStore) GetChatCompactionCheckpointIfChanged(ctx context.Context, scopeType, scopeID, previous string) (r0 *models.ChatCompactionCheckpoint, r1 string, r2 error) {
	if s.r.Remote(ctx, "Executions", "GetChatCompactionCheckpointIfChanged", []any{scopeType, scopeID, previous}, nil, &r0, &r1, &r2) {
		return
	}
	return s.local.(interface {
		GetChatCompactionCheckpointIfChanged(context.Context, string, string, string) (*models.ChatCompactionCheckpoint, string, error)
	}).GetChatCompactionCheckpointIfChanged(ctx, scopeType, scopeID, previous)
}

func (s *LifecycleStore) AppendExecutionEvent(ctx context.Context, event *models.LifecycleExecutionEvent) error {
	if s.r.Refuse(ctx, "Lifecycle", []any{event}) {
		return ErrUnsupported
	}
	return s.local.(interface {
		AppendExecutionEvent(context.Context, *models.LifecycleExecutionEvent) error
	}).AppendExecutionEvent(ctx, event)
}

func (s *EmailAuthStore) IsAuthorizedAnywhere(ctx context.Context, email string) (bool, error) {
	return s.local.(interface {
		IsAuthorizedAnywhere(context.Context, string) (bool, error)
	}).IsAuthorizedAnywhere(ctx, email)
}

type senderBinder interface {
	BindSender(ctx context.Context, id, user, team string) error
}

func (s *TelegramAuthStore) BindSender(ctx context.Context, id, user, team string) error {
	return s.local.(senderBinder).BindSender(ctx, id, user, team)
}

func (s *SlackAuthStore) BindSender(ctx context.Context, id, user, team string) error {
	return s.local.(senderBinder).BindSender(ctx, id, user, team)
}

func (s *DiscordAuthStore) BindSender(ctx context.Context, id, user, team string) error {
	return s.local.(senderBinder).BindSender(ctx, id, user, team)
}

func (s *EmailAuthStore) BindSender(ctx context.Context, id, user, team string) error {
	return s.local.(senderBinder).BindSender(ctx, id, user, team)
}

type structuredOutputStore interface {
	StartStructuredOutput(context.Context, string) (int64, error)
	SealStructuredOutput(context.Context, string, []byte, int) error
	AppendStructuredOutput(context.Context, string, []models.ConversationChange) ([]models.ConversationChange, error)
}

func (s *ExecutionStore) StartStructuredOutput(ctx context.Context, id string) (int64, error) {
	if s.r.Refuse(ctx, "Executions", []any{id}) {
		return 0, ErrUnsupported
	}
	return s.local.(structuredOutputStore).StartStructuredOutput(ctx, id)
}

func (s *ExecutionStore) SealStructuredOutput(ctx context.Context, id string, digest []byte, total int) error {
	if s.r.Refuse(ctx, "Executions", []any{id}) {
		return ErrUnsupported
	}
	return s.local.(structuredOutputStore).SealStructuredOutput(ctx, id, digest, total)
}

func (s *ExecutionStore) AppendStructuredOutput(ctx context.Context, id string, changes []models.ConversationChange) ([]models.ConversationChange, error) {
	if s.r.Refuse(ctx, "Executions", []any{id}) {
		return nil, ErrUnsupported
	}
	return s.local.(structuredOutputStore).AppendStructuredOutput(ctx, id, changes)
}

// Repository subscriptions observe this server's store; remote changes arrive over the hub.
func (s *ProjectStore) SubscribeRepositoryChanges() (<-chan struct{}, func()) {
	if source, ok := s.local.(interface {
		SubscribeRepositoryChanges() (<-chan struct{}, func())
	}); ok {
		return source.SubscribeRepositoryChanges()
	}
	return nil, func() {}
}
