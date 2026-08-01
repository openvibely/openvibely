package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	llmollama "github.com/openvibely/openvibely/internal/llm/ollama"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	llmopenai_compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
)

// ProviderAdapter isolates provider-specific call routing from core orchestration.
// Implementations can choose API key, OAuth, or CLI transports per provider.
type ProviderAdapter interface {
	Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error)
}

var resolvePluginRuntimeBundleFn = agentplugins.ResolveRuntimeBundle

func (s *LLMService) initProviderAdapters() {
	anthropicAdapter := llmanthropic.New(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	openaiAdapter := llmopenai.New(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	openaiCompatibleAdapter := llmopenai_compatible.NewWithConfigRepo(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	ollamaAdapter := llmollama.New(s.execRepo, s.executionStreamHub)
	s.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderAnthropic:        &anthropicProviderAdapter{svc: s, adapter: anthropicAdapter},
		models.ProviderOpenAI:           &openAIProviderAdapter{svc: s, adapter: openaiAdapter},
		models.ProviderOpenAICompatible: &openAICompatibleProviderAdapter{svc: s, adapter: openaiCompatibleAdapter},
		models.ProviderOllama:           &ollamaProviderAdapter{svc: s, adapter: ollamaAdapter},
		models.ProviderMixture:          &mixtureProviderAdapter{svc: s},
		models.ProviderTest:             &testProviderAdapter{svc: s},
	}
}

func (s *LLMService) adapterFor(provider models.LLMProvider) (ProviderAdapter, bool) {
	if s.providerAdapters == nil {
		s.initProviderAdapters()
	}
	adapter, ok := s.providerAdapters[provider]
	return adapter, ok
}

func requestUsesChatStreaming(req llmcontracts.AgentRequest) bool {
	if req.Operation != llmcontracts.OperationStreaming {
		return false
	}
	if req.Followup {
		return true
	}
	mode := strings.TrimSpace(string(req.ChatMode))
	return mode == string(models.ChatModeOrchestrate) || mode == string(models.ChatModePlan)
}

func canonicalResult(output, textOnly string, usage llmcontracts.Usage, err error) (llmcontracts.AgentResult, error) {
	if textOnly == "" {
		textOnly = output
	}
	res := llmcontracts.AgentResult{
		Output:         output,
		TextOnlyOutput: textOnly,
		Usage:          usage,
	}
	// Detect max_tokens errors from any provider adapter. Each provider package
	// has its own errMaxTokens sentinel, so match on the error message prefix.
	if err != nil && strings.HasPrefix(err.Error(), "response truncated: max") {
		res.StopReason = "max_tokens"
	}
	return res, err
}

func callProviderOnce(fn func() (llmcontracts.AgentResult, error)) (llmcontracts.AgentResult, error) {
	// Provider transports own retry decisions because they know whether a
	// streamed attempt has emitted output. Replaying here could duplicate a
	// partial turn and would multiply the provider's bounded retry budget.
	return fn()
}

func resolveAgentRuntime(ctx context.Context, ad *models.Agent) (raw *models.Agent, merged *models.Agent, pluginDirs []string) {
	if ad == nil {
		return nil, nil, nil
	}
	raw = ad
	merged = ad
	if len(ad.Plugins) == 0 {
		return raw, merged, nil
	}
	runtime, err := resolvePluginRuntimeBundleFn(ctx, ad.Plugins)
	if err != nil {
		applog.Infof("[agent-svc] resolveAgentRuntime failed for %s: %v", ad.Name, err)
		return raw, merged, nil
	}
	merged = agentplugins.MergeAgentWithRuntime(ad, runtime)
	return raw, merged, runtime.PluginDirs
}

type anthropicProviderAdapter struct {
	svc     *LLMService
	adapter *llmanthropic.Adapter
}

func anthropicAdapterEnabled(agent models.LLMConfig) bool {
	return agent.IsOAuth() || agent.IsAnthropicAPIKey()
}

func (a *anthropicProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	rawAgentDef, runtimeAgentDef, runtimePluginDirs := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
		req.PluginDirs = runtimePluginDirs
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		switch req.Operation {
		case llmcontracts.OperationDirect:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			if req.Agent.IsAnthropicCLI() {
				if req.DisableTools {
					return llmcontracts.AgentResult{}, fmt.Errorf("direct no-tools mode is not supported for Anthropic CLI transport")
				}
				output, tokens, err := a.svc.callClaudeCLISimple(req.Ctx, req.Message, req.Attachments, req.Agent, req.WorkDir, req.DisableTools)
				return canonicalResult(output, output, llmusage.FromTotal(tokens), err)
			}
			output, tokens, err := a.svc.callAnthropic(req.Ctx, req.Message, req.Attachments, req.Agent)
			return canonicalResult(output, output, llmusage.FromTotal(tokens), err)

		case llmcontracts.OperationStreaming:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			if req.Agent.IsAnthropicCLI() {
				if requestUsesChatStreaming(req) {
					output, tokens, err := a.svc.callClaudeCLIChat(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.WorkDir, req.Followup, req.ChatMode, req.PluginDirs)
					return canonicalResult(output, output, llmusage.FromTotal(tokens), err)
				}
				output, textOnly, tokens, err := a.svc.callClaudeCLI(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.PluginDirs, rawAgentDef)
				return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)
			}
			if requestUsesChatStreaming(req) {
				output, tokens, err := a.svc.callAnthropicChat(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode)
				return canonicalResult(output, output, llmusage.FromTotal(tokens), err)
			}
			output, tokens, err := a.svc.callAnthropic(req.Ctx, llmprompt.ApplyTaskCreationToolMode(req.Message, nil), req.Attachments, req.Agent)
			return canonicalResult(output, output, llmusage.FromTotal(tokens), err)

		case llmcontracts.OperationTask:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			if req.Agent.IsAnthropicCLI() {
				output, textOnly, tokens, err := a.svc.callClaudeCLI(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.PluginDirs, rawAgentDef)
				return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)
			}
			output, tokens, err := a.svc.callAnthropic(req.Ctx, llmprompt.ApplyTaskCreationToolMode(req.Message, nil), req.Attachments, req.Agent)
			return canonicalResult(output, output, llmusage.FromTotal(tokens), err)
		default:
			return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
		}
	})
}

type openAIProviderAdapter struct {
	svc     *LLMService
	adapter *llmopenai.Adapter
}

func (a *openAIProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef, _ := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	// Apply agent definition: inject system prompt + skill content
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
		if req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
			req.Agent.Model = req.AgentDefinition.Model
		}
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		switch req.Operation {
		case llmcontracts.OperationDirect:
			if openAIDirectClientEnabled(req.Agent) {
				output, usage, err := a.adapter.CallDirect(req.Ctx, req.Message, req.Attachments, req.Agent, req.WorkDir, req.DisableTools)
				return canonicalResult(output, output, usage, err)
			}
			if req.DisableTools {
				return llmcontracts.AgentResult{}, fmt.Errorf("direct no-tools mode is not supported for OpenAI CLI transport")
			}
			output, tokens, err := a.svc.callCodexCLISimple(req.Ctx, req.Message, req.Attachments, req.Agent, req.WorkDir, req.DisableTools)
			return canonicalResult(output, output, llmusage.FromTotal(tokens), err)

		case llmcontracts.OperationStreaming:
			if openAIDirectClientEnabled(req.Agent) {
				if requestUsesChatStreaming(req) {
					output, usage, err := a.adapter.CallChatStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.TransportScope, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode, req.WorkDir, req.AgentDefinition)
					return canonicalResult(output, output, usage, err)
				}
				output, textOnly, usage, err := a.adapter.CallStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.AgentDefinition)
				return canonicalResult(output, textOnly, usage, err)
			}
			if requestUsesChatStreaming(req) {
				output, tokens, err := a.svc.callCodexCLIChat(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.WorkDir, req.Followup, req.ChatMode)
				return canonicalResult(output, output, llmusage.FromTotal(tokens), err)
			}
			output, textOnly, tokens, err := a.svc.callCodexCLI(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir)
			return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)

		case llmcontracts.OperationTask:
			if openAIDirectClientEnabled(req.Agent) {
				output, textOnly, usage, err := a.adapter.CallStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.AgentDefinition)
				return canonicalResult(output, textOnly, usage, err)
			}
			output, textOnly, tokens, err := a.svc.callCodexCLI(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir)
			return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)
		default:
			return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
		}
	})
}

type openAICompatibleProviderAdapter struct {
	svc     *LLMService
	adapter *llmopenai_compatible.Adapter
}

func (a *openAICompatibleProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef, _ := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
		if req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
			req.Agent.Model = req.AgentDefinition.Model
		}
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		return a.adapter.Call(req.Ctx, req, req.WorkDir)
	})
}

type ollamaProviderAdapter struct {
	svc     *LLMService
	adapter *llmollama.Adapter
}

func (a *ollamaProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef, _ := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	// Apply agent definition: inject system prompt + skill content
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
	})
}

type mixtureProviderAdapter struct {
	svc *LLMService
}

func (a *mixtureProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	cfg, err := llmmixture.ParseConfig(req.Agent.MixtureConfigJSON)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	aggregator, err := a.resolveMixtureSlot(req.Ctx, cfg.Aggregator)
	if err != nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("mixture aggregator: %w", err)
	}
	if aggregator.Provider == models.ProviderMixture {
		return llmcontracts.AgentResult{}, fmt.Errorf("mixture aggregator cannot use provider %s", aggregator.Provider)
	}
	if !cfg.Enabled {
		aggReq := req
		aggReq.Agent = aggregator
		aggReq.Agent.Temperature = cfg.AggregatorTemperature
		return a.callAggregator(aggReq)
	}

	a.publishMixtureProgress(req, "running_references", 0, len(cfg.ReferenceModels), fmt.Sprintf("Running %d reference models...", len(cfg.ReferenceModels)))
	results := a.runMixtureReferences(req, cfg)
	completed := 0
	for _, result := range results {
		if result.Err == "" {
			completed++
		}
	}
	a.publishMixtureProgress(req, "references_complete", len(results), len(cfg.ReferenceModels), "Reference models complete")
	if err := contextErr(req.Ctx); err != nil {
		return llmcontracts.AgentResult{}, err
	}
	contextBlock := llmmixture.PrivateContext(results)
	aggReq := llmmixture.AppendPrivateContext(req, contextBlock)
	aggReq.Agent = aggregator
	aggReq.Agent.Temperature = cfg.AggregatorTemperature
	a.publishMixtureProgress(req, "aggregator_starting", len(results), len(cfg.ReferenceModels), "Aggregator starting...")
	// applog.Debugf("[mixture] aggregator input provider=%s model=%s message_len=%d message=%q chat_context_len=%d chat_context=%q project_instructions_len=%d project_instructions=%q",
	//	aggReq.Agent.Provider,
	//	aggReq.Agent.Model,
	//	len(aggReq.Message),
	//	aggReq.Message,
	//	len(aggReq.ChatSystemContext),
	//	aggReq.ChatSystemContext,
	//	len(aggReq.ProjectInstructions),
	//	aggReq.ProjectInstructions,
	// )
	res, err := a.callAggregator(aggReq)
	res.Usage = mergeMixtureUsage(results, res.Usage)
	return res, err
}

func (a *mixtureProviderAdapter) resolveMixtureSlot(ctx context.Context, slot llmmixture.ModelSlot) (models.LLMConfig, error) {
	id := strings.TrimSpace(slot.AgentConfigID)
	if id == "" {
		return models.LLMConfig{}, fmt.Errorf("model config id is required")
	}
	if a.svc == nil || a.svc.llmConfigRepo == nil {
		return models.LLMConfig{}, fmt.Errorf("model config repository is unavailable")
	}
	cfg, err := a.svc.llmConfigRepo.GetByID(ctx, id)
	if err != nil {
		return models.LLMConfig{}, err
	}
	if cfg == nil {
		return models.LLMConfig{}, fmt.Errorf("model config not found")
	}
	return *cfg, nil
}

func (a *mixtureProviderAdapter) runMixtureReferences(req llmcontracts.AgentRequest, cfg llmmixture.Config) []llmmixture.ReferenceResult {
	results := make([]llmmixture.ReferenceResult, len(cfg.ReferenceModels))
	if len(cfg.ReferenceModels) == 0 {
		return results
	}
	limit := cfg.MaxReferenceWorkers
	if limit <= 0 || limit > len(cfg.ReferenceModels) {
		limit = len(cfg.ReferenceModels)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var finishedReferences atomic.Int64
	for i, slot := range cfg.ReferenceModels {
		i, slot := i, slot
		results[i] = llmmixture.ReferenceResult{
			Index:    i,
			Label:    llmmixture.SlotLabel(slot, ""),
			Provider: strings.TrimSpace(slot.Provider),
			Model:    strings.TrimSpace(slot.Model),
		}
		if err := contextErr(req.Ctx); err != nil {
			results[i].Err = err.Error()
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = a.callMixtureReference(req, cfg, slot, i)
			completed := int(finishedReferences.Add(1))
			a.publishMixtureProgress(req, "reference_complete", completed, len(cfg.ReferenceModels), fmt.Sprintf("Reference model %d complete", i+1))
		}()
	}
	wg.Wait()
	return results
}

func (a *mixtureProviderAdapter) callMixtureReference(req llmcontracts.AgentRequest, cfg llmmixture.Config, slot llmmixture.ModelSlot, index int) llmmixture.ReferenceResult {
	result := llmmixture.ReferenceResult{Index: index, Label: llmmixture.SlotLabel(slot, ""), Provider: strings.TrimSpace(slot.Provider), Model: strings.TrimSpace(slot.Model)}
	resolved, err := a.resolveMixtureSlot(req.Ctx, slot)
	if err != nil {
		result.Err = "model config not found"
		return result
	}
	result.Label = llmmixture.SlotLabel(slot, resolved.Name)
	result.Provider = string(resolved.Provider)
	result.Model = resolved.Model
	if resolved.Provider == models.ProviderMixture {
		result.Err = "recursive mixture model is not allowed"
		return result
	}
	adapter, ok := a.svc.adapterFor(resolved.Provider)
	if !ok {
		result.Err = fmt.Sprintf("no adapter for provider %s", resolved.Provider)
		return result
	}
	refCtx := llmcontracts.WithoutRuntimeTools(req.Ctx)
	refCtx, cancel := context.WithTimeout(refCtx, time.Duration(cfg.ReferenceTimeoutSeconds)*time.Second)
	defer cancel()
	refReq := req
	refReq.Ctx = refCtx
	refReq.Agent = resolved
	refReq.Agent.Temperature = cfg.ReferenceTemperature
	refReq.Operation = llmcontracts.OperationDirect
	refReq.DisableTools = true
	refReq.RawDirectPrompt = true
	refReq.ExecID = ""
	refReq.AgentDefinition = nil
	refReq.PluginDirs = nil
	refReq.ProjectInstructions = ""
	refReq.ChatSystemContext = ""
	refReq.Attachments = nil
	refReq.Message = llmmixture.ReferencePrompt(req.Message, req.ChatHistory)
	res, err := adapter.Call(refReq)
	if err != nil {
		result.Err = err.Error()
		if refCtx.Err() != nil {
			result.Err = refCtx.Err().Error()
		}
		return result
	}
	result.Output = res.TextOnlyOutput
	if result.Output == "" {
		result.Output = res.Output
	}
	// applog.Debugf("[mixture] reference %d output provider=%s model=%s len=%d output=%q",
	//	index+1, result.Provider, result.Model, len(result.Output), result.Output)
	result.Usage = res.Usage
	return result
}

func (a *mixtureProviderAdapter) callAggregator(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	adapter, ok := a.svc.adapterFor(req.Agent.Provider)
	if !ok {
		return llmcontracts.AgentResult{}, fmt.Errorf("no adapter for mixture aggregator provider %s", req.Agent.Provider)
	}
	return adapter.Call(req)
}

func (a *mixtureProviderAdapter) publishMixtureProgress(req llmcontracts.AgentRequest, phase string, completed, total int, message string) {
	if a == nil || a.svc == nil || a.svc.broadcaster == nil || strings.TrimSpace(req.ExecID) == "" {
		return
	}
	a.svc.broadcaster.Publish(events.TaskEvent{
		Type:                events.MixtureProgress,
		ExecID:              req.ExecID,
		Phase:               phase,
		TotalReferences:     total,
		CompletedReferences: completed,
		Message:             message,
	})
}

func mergeMixtureUsage(results []llmmixture.ReferenceResult, aggregator llmcontracts.Usage) llmcontracts.Usage {
	merged := aggregator
	if merged.ProviderRaw == nil {
		merged.ProviderRaw = map[string]int{}
	}
	merged.ProviderRaw["aggregator_total_tokens"] = aggregator.TotalTokens
	for i, result := range results {
		merged.InputTokens += result.Usage.InputTokens
		merged.OutputTokens += result.Usage.OutputTokens
		merged.CachedInputTokens += result.Usage.CachedInputTokens
		merged.ReasoningTokens += result.Usage.ReasoningTokens
		merged.TotalTokens += result.Usage.TotalTokens
		merged.ProviderRaw[fmt.Sprintf("reference_%d_total_tokens", i+1)] = result.Usage.TotalTokens
	}
	return merged
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type testProviderAdapter struct {
	svc *LLMService
}

type agentRequestRecorder interface {
	RecordAgentRequest(req llmcontracts.AgentRequest)
}

func (a *testProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if a.svc.llmCaller == nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("test provider requires LLMCaller to be set via SetLLMCaller()")
	}
	if recorder, ok := a.svc.llmCaller.(agentRequestRecorder); ok {
		recorder.RecordAgentRequest(req)
	}
	output, textOnly, tokens, err := a.svc.llmCaller.CallModel(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir)
	return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)
}
