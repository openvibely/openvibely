package models

import "context"

// GitHubFeedbackExecution is internal runtime metadata, never decoded from tool
// input. It pins the execution whose authority admits a feedback instruction.
type GitHubFeedbackExecution struct {
	TaskID, ExecutionID string
	RequireIdentity     bool
}
type githubFeedbackExecutionKey struct{}

func WithGitHubFeedbackExecution(ctx context.Context, identity GitHubFeedbackExecution) context.Context {
	return context.WithValue(ctx, githubFeedbackExecutionKey{}, identity)
}
func GitHubFeedbackExecutionFromContext(ctx context.Context) (GitHubFeedbackExecution, bool) {
	identity, ok := ctx.Value(githubFeedbackExecutionKey{}).(GitHubFeedbackExecution)
	return identity, ok
}
