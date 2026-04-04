package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestChatContent_PlanSwitchAutoSubmitsImplementationHandoff(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := ChatContent(agents, nil, "project-1", map[string][]models.ChatAttachment{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()

	if !strings.Contains(content, "window.switchChatMode('orchestrate')") {
		t.Error("expected plan switch handler to set orchestrate mode")
	}
	if !strings.Contains(content, "var planHandoffMessage = 'Create one active task for the whole proposed plan above. Do not execute or start any other existing tasks. Report progress.'") {
		t.Error("expected plan switch handler to seed implementation handoff message")
	}
	if !strings.Contains(content, "chatForm.requestSubmit();") {
		t.Error("expected plan switch handler to auto-submit chat form")
	}
}

func TestChatContent_PlanCompletionPromptRestoresFromHistoryOnRefresh(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := ChatContent(agents, nil, "project-1", map[string][]models.ChatAttachment{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()

	if !strings.Contains(content, "window.maybeShowPlanCompletionPromptFromHistory = function()") {
		t.Error("expected chat content to define history-based plan completion prompt recovery")
	}
	if !strings.Contains(content, "window.maybeShowPlanCompletionPromptFromHistory();") {
		t.Error("expected chat content to invoke history-based plan completion prompt recovery")
	}
}

func TestChatContent_PlanPromptRecoveryRunsForContainerOuterHTMLSwaps(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := ChatContent(agents, nil, "project-1", map[string][]models.ChatAttachment{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()

	if !strings.Contains(content, "var isChatRootSwap = !!(swapTarget && swapTarget.id === 'chat-page-root');") {
		t.Error("expected afterSwap handler to detect full #chat-page-root outerHTML swaps")
	}
	if !strings.Contains(content, "if (window.maybeShowPlanCompletionPromptFromHistory) window.maybeShowPlanCompletionPromptFromHistory();") {
		t.Error("expected plan completion prompt recovery after container/message swaps")
	}
}

func TestChatContent_PlanPromptButtonsUseDelegatedHandlers(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := ChatContent(agents, nil, "project-1", map[string][]models.ChatAttachment{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()

	if !strings.Contains(content, "window._chatPlanPromptClickHandlerAttached") {
		t.Error("expected delegated plan prompt click handler guard")
	}
	if !strings.Contains(content, "document.body.addEventListener('click'") {
		t.Error("expected delegated click listener for plan prompt buttons")
	}
}
