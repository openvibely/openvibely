package testutil

import (
	"context"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

// MockLLMCall records the arguments from a single CallModel invocation.
type MockLLMCall struct {
	Prompt      string
	Attachments []models.Attachment
	Agent       models.LLMConfig
	ExecID      string
	WorkDir     string
}

// MockLLMCaller implements service.LLMCaller for tests.
// It returns configurable responses and records every call for assertions.
type MockLLMCaller struct {
	mu       sync.Mutex
	Response string
	TextOnly string
	Tokens   int
	Err      error
	Calls    []MockLLMCall
}

// NewMockLLMCaller creates a mock that returns empty output with no error.
func NewMockLLMCaller() *MockLLMCaller {
	return &MockLLMCaller{}
}

// CallModel satisfies the service.LLMCaller interface.
func (m *MockLLMCaller) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, MockLLMCall{
		Prompt:      prompt,
		Attachments: attachments,
		Agent:       agent,
		ExecID:      execID,
		WorkDir:     workDir,
	})
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
