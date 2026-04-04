package prompt

import (
	"fmt"

	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBuildTaskPromptHeader(t *testing.T) {
	header := BuildTaskPromptHeader()
	if !strings.Contains(header, "Do not use plan mode") {
		t.Error("header should contain plan mode directive")
	}
}

func TestBuildAttachmentInstructions_Empty(t *testing.T) {
	result := BuildAttachmentInstructions(nil)
	if result != "" {
		t.Errorf("expected empty string for nil attachments, got %q", result)
	}
	result = BuildAttachmentInstructions([]models.Attachment{})
	if result != "" {
		t.Errorf("expected empty string for empty attachments, got %q", result)
	}
}

func TestBuildAttachmentInstructions_WithFiles(t *testing.T) {
	attachments := []models.Attachment{
		{FileName: "image.png", FilePath: "/tmp/image.png"},
		{FileName: "doc.txt", FilePath: "/tmp/doc.txt"},
	}
	result := BuildAttachmentInstructions(attachments)
	if !strings.Contains(result, "image.png") {
		t.Error("should contain filename image.png")
	}
	if !strings.Contains(result, "doc.txt") {
		t.Error("should contain filename doc.txt")
	}
	if !strings.Contains(result, "/tmp/image.png") {
		t.Error("should contain absolute path for image.png")
	}
	if !strings.Contains(result, "absolute path") {
		t.Error("should mention absolute paths")
	}
}

func TestBuildChatHistoryText_Empty(t *testing.T) {
	result := BuildChatHistoryText(nil)
	if result != "" {
		t.Errorf("expected empty string for nil history, got %q", result)
	}
	result = BuildChatHistoryText([]models.Execution{})
	if result != "" {
		t.Errorf("expected empty string for empty history, got %q", result)
	}
}

func TestBuildChatHistoryText_WithHistory(t *testing.T) {
	history := []models.Execution{
		{PromptSent: "hello", Output: "hi there", Status: models.ExecCompleted},
		{PromptSent: "how are you", Output: "fine", Status: models.ExecCompleted},
	}
	result := BuildChatHistoryText(history)
	if !strings.Contains(result, "Previous conversation:") {
		t.Error("should contain 'Previous conversation:' header")
	}
	if !strings.Contains(result, "User: hello") {
		t.Error("should contain first user message")
	}
	if !strings.Contains(result, "Assistant: hi there") {
		t.Error("should contain first assistant response")
	}
	if !strings.Contains(result, "User: how are you") {
		t.Error("should contain second user message")
	}
	if !strings.Contains(result, "---") {
		t.Error("should contain separator")
	}
}

func TestBuildChatHistoryText_SkipsRunning(t *testing.T) {
	history := []models.Execution{
		{PromptSent: "hello", Output: "partial", Status: models.ExecRunning},
	}
	result := BuildChatHistoryText(history)
	if !strings.Contains(result, "User: hello") {
		t.Error("should include user prompt from running execution")
	}
	if strings.Contains(result, "Assistant: partial") {
		t.Error("should NOT include output from running execution")
	}
}

func TestBuildChatHistoryText_IncludesFailed(t *testing.T) {
	history := []models.Execution{
		{PromptSent: "do this", Output: "error occurred", Status: models.ExecFailed},
	}
	result := BuildChatHistoryText(history)
	if !strings.Contains(result, "Assistant: error occurred") {
		t.Error("should include output from failed execution")
	}
}

func TestBuildChatHistoryText_LimitsHistory(t *testing.T) {
	// Create 25 history entries (exceeding maxChatHistoryTurns=20)
	var history []models.Execution
	for i := 0; i < 25; i++ {
		history = append(history, models.Execution{
			PromptSent: fmt.Sprintf("msg-%d", i),
			Output:     fmt.Sprintf("reply-%d", i),
			Status:     models.ExecCompleted,
		})
	}
	result := BuildChatHistoryText(history)

	if strings.Contains(result, "msg-0") {
		t.Error("should NOT contain oldest message (truncated)")
	}
	if strings.Contains(result, "msg-4") {
		t.Error("should NOT contain 5th oldest message (truncated)")
	}

	if !strings.Contains(result, "msg-5") {
		t.Error("should contain message at index 5")
	}
	if !strings.Contains(result, "msg-24") {
		t.Error("should contain most recent message")
	}
}
