package skilldeps

import (
	"context"
	"encoding/json"
)

type approvalContextKey struct{}
type approvalContext struct {
	project string
	check   InstallationPrompt
}

// WithApproval supplies the thread's installation prompt and display context.
func WithApproval(ctx context.Context, project string, check InstallationPrompt) context.Context {
	return context.WithValue(ctx, approvalContextKey{}, approvalContext{project, check})
}
func ApprovalFromContext(ctx context.Context) (string, InstallationPrompt) {
	value, _ := ctx.Value(approvalContextKey{}).(approvalContext)
	return value.project, value.check
}

// Tool arguments identify the interrupted skill_view, including support files.
type skillViewArgumentsKey struct{}

func WithSkillViewArguments(ctx context.Context, args json.RawMessage) context.Context {
	return context.WithValue(ctx, skillViewArgumentsKey{}, append(json.RawMessage(nil), args...))
}
func SkillViewArguments(ctx context.Context) json.RawMessage {
	args, _ := ctx.Value(skillViewArgumentsKey{}).(json.RawMessage)
	return append(json.RawMessage(nil), args...)
}
