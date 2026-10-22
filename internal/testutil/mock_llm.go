package testutil

import (
	"context"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/managedcontext"
)

// MockLLMCall records the arguments from a single CallModel invocation.
type MockLLMCall struct {
	// Snapshot while request permissions are live; turn contexts may be canceled after completion.
	ManagedKnowledge string
	Prompt           string
	Attachments      []models.Attachment
	Agent            models.LLMConfig
	ExecID           string
	WorkDir          string
}

// MockLLMCaller implements service.LLMCaller for tests.
// It returns configurable responses and records every call for assertions.
type MockLLMCaller struct {
	mu            sync.Mutex
	Response      string
	TextOnly      string
	Tokens        int
	Err           error
	Calls         []MockLLMCall
	AgentRequests []llmcontracts.AgentRequest
	OnCall        func(context.Context, MockLLMCall)
}

// NewMockLLMCaller creates a mock that returns empty output with no error.
func NewMockLLMCaller() *MockLLMCaller {
	return &MockLLMCaller{}
}

// RecordAgentRequest records the normalized provider request when called through
// the test provider adapter.
func (m *MockLLMCaller) RecordAgentRequest(req llmcontracts.AgentRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AgentRequests = append(m.AgentRequests, req)
}

// CallModel satisfies the service.LLMCaller interface.
func (m *MockLLMCaller) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	call := MockLLMCall{
		ManagedKnowledge: managedcontext.Text(ctx),
		Prompt:           prompt,
		Attachments:      attachments,
		Agent:            agent,
		ExecID:           execID,
		WorkDir:          workDir,
	}
	m.mu.Lock()
	m.Calls = append(m.Calls, call)
	onCall := m.OnCall
	m.mu.Unlock()
	if onCall != nil {
		onCall(ctx, call)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Response, m.TextOnly, m.Tokens, m.Err
}

// CallCount returns the number of times CallModel was invoked.
func (m *MockLLMCaller) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}

// LastCall returns the most recent call, or an empty MockLLMCall if none.
func (m *MockLLMCaller) LastCall() MockLLMCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Calls) == 0 {
		return MockLLMCall{}
	}
	return m.Calls[len(m.Calls)-1]
}

// LastAgentRequest returns the most recent normalized provider request, or an
// empty request if none was recorded.
func (m *MockLLMCaller) LastAgentRequest() llmcontracts.AgentRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.AgentRequests) == 0 {
		return llmcontracts.AgentRequest{}
	}
	return m.AgentRequests[len(m.AgentRequests)-1]
}
