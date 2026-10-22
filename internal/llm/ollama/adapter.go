// Package ollama adapts saved Ollama configurations to Chat Completions.
package ollama

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/contextlimits"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// DefaultContextWindow is a request-budget fallback, not a detected server limit.
const DefaultContextWindow = contextlimits.DefaultOllamaContextWindow

type Adapter struct{ shared *compatible.Adapter }

func New(execRepo persistence.ExecutionStore, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{shared: compatible.New(execRepo, streamHub)}
}

func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string, _ *llmstream.Writer) (llmcontracts.AgentResult, error) {
	// Only translate the request snapshot; persisted provider identity and settings
	// remain Ollama. ContextWindow is a local admission budget, never num_ctx.
	base := strings.TrimRight(strings.TrimSpace(req.Agent.OllamaBaseURL), "/")
	if base == "" {
		base = "http://localhost:11434"
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	req.Agent.BaseURL = base
	req.Agent.Transport = "chat_completions"
	req.Agent.AuthMethod = models.AuthMethodAPIKey
	req.Agent.CustomAuthConfigJSON = ""
	req.Agent.CustomAuthStateJSON = ""
	return a.shared.Call(ctx, req, workDir)
}
