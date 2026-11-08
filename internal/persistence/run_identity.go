package persistence

import "context"

// RunIdentityStore persists the principal for queued task execution separately
// from message authorship. BindTask records the current caller, not a client ID.
type RunIdentityStore interface {
	TaskUser(context.Context, string) (string, error)
	BindTask(context.Context, string) error
	ScheduleUser(context.Context, string) (string, error)
	// ExecutionUser distinguishes a missing legacy snapshot from an empty, denied one.
	ExecutionUser(context.Context, string) (string, bool, error)
	// CheckExecutionGoal rejects a continuation whose saved goal was replaced or stopped.
	CheckExecutionGoal(context.Context, string, string) error
	// RequireChannelTaskProvenance rejects verified-channel tasks lacking a
	// current sender binding snapshot; other origins pass.
	RequireChannelTaskProvenance(context.Context, string) error
}
