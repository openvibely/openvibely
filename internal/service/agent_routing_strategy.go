package service

import (
	"context"
	"fmt"
	"log"
	"os"

	llmcapability "github.com/openvibely/openvibely/internal/llm/capability"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
)

// agentRoutingStrategy centralizes provider adapter resolution and
// vision-aware agent overrides.
type agentRoutingStrategy struct {
	svc *LLMService
}

// VisionRoutingDecision captures how vision-aware routing resolved.
// It is intended for logging/telemetry so callers can see why the
// selected agent changed (or did not change).
type VisionRoutingDecision struct {
	Agent   models.LLMConfig
	Changed bool
	Reason  string
	Detail  string
}

func newAgentRoutingStrategy(svc *LLMService) *agentRoutingStrategy {
	return &agentRoutingStrategy{svc: svc}
}

func (r *agentRoutingStrategy) resolveAdapter(provider models.LLMProvider) (ProviderAdapter, error) {
	adapter, ok := r.svc.adapterFor(provider)
	if !ok {
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
	return adapter, nil
}

// resolveVisionRoutingDecision switches away from Anthropic CLI when image
// attachments are present, because CLI mode cannot send multimodal image blocks.
func (r *agentRoutingStrategy) resolveVisionRoutingDecision(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, operation string, taskID string) VisionRoutingDecision {
	decision := VisionRoutingDecision{
		Agent:   agent,
		Changed: false,
		Reason:  "vision_not_required",
	}

	if len(attachments) == 0 {
		decision.Reason = "no_attachments"
		return decision
	}
	if llmcapability.ForAgent(agent).Vision {
		decision.Reason = "agent_already_vision_capable"
		return decision
	}

	hasImages := false
	for _, att := range attachments {
		if llmoutput.IsImageMediaType(att.MediaType) {
			hasImages = true
			break
		}
	}
	if !hasImages {
		decision.Reason = "no_image_attachments"
		return decision
	}

	if taskID != "" {
		log.Printf("[agent-svc] %s task=%s has image attachments but agent=%s is %s (no vision), looking for vision-capable agent",
			operation, taskID, agent.Name, agent.Provider)
	} else {
		log.Printf("[agent-svc] %s has image attachments but agent=%s is %s (no vision), looking for vision-capable agent",
			operation, agent.Name, agent.Provider)
	}

	if r.svc.llmConfigRepo == nil {
		decision.Reason = "config_repo_unavailable"
		decision.Detail = "llmConfigRepo is nil"
		return decision
	}

	configs, listErr := r.svc.llmConfigRepo.List(ctx)
	if listErr != nil || len(configs) == 0 {
		if listErr != nil {
			decision.Reason = "vision_config_lookup_failed"
			decision.Detail = listErr.Error()
		} else {
			decision.Reason = "no_model_configs_available"
		}
		return decision
	}

	complexity := AnalyzeComplexity(prompt)
	visionResult := SelectLLMWithVision(complexity, configs, true)
	if visionResult != nil && visionResult.LLMConfig != nil {
		decision.Agent = *visionResult.LLMConfig
		decision.Changed = true
		decision.Reason = "vision_agent_selected"
		decision.Detail = visionResult.Reason
		if taskID != "" {
			log.Printf("[agent-svc] %s switched to vision-capable agent=%s provider=%s for task=%s",
				operation, decision.Agent.Name, decision.Agent.Provider, taskID)
		} else {
			log.Printf("[agent-svc] %s switched to vision-capable agent=%s provider=%s",
				operation, decision.Agent.Name, decision.Agent.Provider)
		}
		return decision
	}

	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		decision.Agent = models.LLMConfig{
			Name:      "Anthropic API (auto-vision)",
			Provider:  models.ProviderAnthropic,
			Model:     "claude-sonnet-4-5-20250929",
			APIKey:    apiKey,
			MaxTokens: 4096,
		}
		decision.Changed = true
		decision.Reason = "vision_env_fallback"
		decision.Detail = "selected ad-hoc Anthropic API agent via ANTHROPIC_API_KEY"
		if taskID != "" {
			log.Printf("[agent-svc] %s created ad-hoc Anthropic agent using ANTHROPIC_API_KEY env var for task=%s image analysis", operation, taskID)
		} else {
			log.Printf("[agent-svc] %s created ad-hoc Anthropic agent using ANTHROPIC_API_KEY env var for image analysis", operation)
		}
		return decision
	}

	decision.Reason = "no_vision_fallback_available"
	decision.Detail = "no vision-capable agent and ANTHROPIC_API_KEY is empty"
	if taskID != "" {
		log.Printf("[agent-svc] %s no vision-capable agents and no ANTHROPIC_API_KEY env var, proceeding with %s (images will be file paths only)", operation, agent.Name)
	} else {
		log.Printf("[agent-svc] %s no vision-capable agents and no ANTHROPIC_API_KEY env var, proceeding with %s (images will not be analyzed)", operation, agent.Name)
	}
	return decision
}
