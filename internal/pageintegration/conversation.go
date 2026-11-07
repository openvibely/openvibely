package pageintegration

import "context"

// ConversationStore is scoped to one authorized page by its adapter. Implementations
// persist the conversation identity and immutable turn snapshots, independently of
// the browser lifetime. The standard persistence store supports every page kind.
type ConversationStore interface {
	TaskID(context.Context) (string, error)
	BindTask(context.Context, string) error
	SaveSnapshot(context.Context, string, string) error
}

// Conversation binds the shared chat runtime to a server-authorized page. Begin
// must recheck access and admit work against the page's enabled lifetime. Lifetime
// is cancelled on disable/removal, never merely on navigation away.
type Conversation struct {
	Page     PageRef
	Store    ConversationStore
	Contexts *ContextStore
	Resolve  Resolver
	Begin    func(context.Context) (context.Context, func(), error)
	Lifetime func() context.Context
}

func (c Conversation) LockKey() string { return "page-chat:" + c.Page.ProjectID + ":" + c.Page.Key() }
