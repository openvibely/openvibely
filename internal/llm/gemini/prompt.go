package gemini

import (
	"context"

	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	gemini "github.com/openvibely/openvibely/pkg/gemini_client"
)

type basePromptKey struct{}

// PreparePrompt resolves the base once, before budgeting, and shares that exact
// text with sending. Internal requests retain their purpose-specific prompts.
func (a *Adapter) PreparePrompt(req contracts.AgentRequest) context.Context {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(basePromptKey{}).(string); ok {
		return ctx
	}
	if _, ok := contracts.SummarySystemPromptFromContext(ctx); ok || req.SummaryInput != nil || req.RawDirectPrompt || req.LifecycleHookCall {
		return ctx
	}
	return context.WithValue(ctx, basePromptKey{}, prompt.GoogleBasePrompt(req.Agent.Model, !requestUsesChatStreaming(req) || req.Followup, promptTools(ctx, req)))
}
func promptTools(ctx context.Context, req contracts.AgentRequest) []string {
	if req.DisableTools {
		return nil
	}
	chat := requestUsesChatStreaming(req)
	if chat && !req.Followup && req.ChatMode != models.ChatModePlan && contracts.RuntimeToolsFromContext(ctx) == nil {
		return nil
	}
	skip := taskSkipDefaultTools(ctx, req.AgentDefinition)
	if chat {
		skip = chatSkipDefaultTools(ctx, req.Followup, req.ChatMode, req.AgentDefinition)
	}
	var defs []gemini.ToolDefinition
	if !skip {
		defs = gemini.DefaultTools()
	}
	defs = append(defs, runtimeTools(ctx)...)
	filter := toolFilter(ctx, req.Followup || !chat, req.ChatMode, req.AgentDefinition)
	var names []string
	for _, d := range defs {
		if filter == nil || filter(d.Name) {
			names = append(names, d.Name)
		}
	}
	return names
}
func BaseSystemPrompt(req contracts.AgentRequest) string {
	if req.Ctx != nil {
		if text, ok := req.Ctx.Value(basePromptKey{}).(string); ok {
			return text
		}
	}
	return prompt.GoogleBasePrompt(req.Agent.Model, !requestUsesChatStreaming(req) || req.Followup, promptTools(req.Ctx, req))
}
