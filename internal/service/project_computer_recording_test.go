package service

import (
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestRecordingImageBudgetSmallModel(t *testing.T) {
	agent := models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "fixture", ContextWindow: 32768}
	n, err := RecordingImageBudget(agent, "Click reports")
	if err != nil || n < 2 || n >= 30 {
		t.Fatalf("budget=%d error=%v tools=%d", n, err, estimateDefaultProviderToolTokens(agent.Provider))
	}
	if _, err = RecordingImageBudget(agent, strings.Repeat("long action ", 20000)); err == nil {
		t.Fatal("oversized log accepted")
	}
	if _, err = RecordingImageBudget(agent, "Click", requestBudgetForAgent(agent).SafeInputLimit); err == nil {
		t.Fatal("full context accepted")
	}
}
