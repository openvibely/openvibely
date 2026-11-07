package pageplugins

import (
	"context"

	"github.com/openvibely/openvibely/internal/pageintegration"
)

// Conversation adapts plugin storage and enabled lifetime to the shared chat
// runtime. Its identity must come from a server-resolved instance, not a request.
func (m *Manager) Conversation(ref pageintegration.PageRef) pageintegration.Conversation {
	return pageintegration.Conversation{
		Page: ref, Store: conversationStore{m, ref.InstanceID}, Contexts: m.Contexts,
		Resolve: m.Resolver(ref.InstanceID, ref.ProjectID),
		Begin: func(ctx context.Context) (context.Context, func(), error) {
			return m.BeginInstance(ctx, ref.InstanceID)
		},
		Lifetime: func() context.Context { return m.LifetimeContext(ref.InstanceID) },
	}
}

type conversationStore struct {
	manager    *Manager
	instanceID string
}

func (s conversationStore) TaskID(ctx context.Context) (string, error) {
	return s.manager.store.GetConversationTask(ctx, s.instanceID)
}
func (s conversationStore) BindTask(ctx context.Context, taskID string) error {
	return s.manager.store.SetConversationTask(ctx, s.instanceID, taskID)
}
func (s conversationStore) SaveSnapshot(ctx context.Context, execID, raw string) error {
	return s.manager.store.SaveTurnContext(ctx, execID, s.instanceID, raw)
}
