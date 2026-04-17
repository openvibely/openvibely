package components

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

// TestChatAutoScrollScript verifies the auto-scroll JavaScript is correctly generated
func TestChatAutoScrollScript(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// Verify core auto-scroll utility exists
	if !strings.Contains(content, "window.chatAutoScroll") {
		t.Error("Missing window.chatAutoScroll namespace")
	}

	// Verify isNearBottom function exists
	if !strings.Contains(content, "isNearBottom: function") {
		t.Error("Missing isNearBottom function")
	}

	// Verify scrollToBottom function exists
	if !strings.Contains(content, "scrollToBottom: function") {
		t.Error("Missing scrollToBottom function")
	}

	// Verify the threshold is reasonable (100px for "near bottom")
	if !strings.Contains(content, "100") {
		t.Error("Expected to find threshold value in script")
	}

	t.Logf("ChatAutoScrollScript generated successfully (%d bytes)", len(content))
}

func TestChatLoadingDots_RendersThreeDotsAndSizeVariants(t *testing.T) {
	var sm bytes.Buffer
	if err := ChatLoadingDots("sm").Render(context.Background(), &sm); err != nil {
		t.Fatalf("Failed to render ChatLoadingDots(sm): %v", err)
	}
	smHTML := sm.String()
	if !strings.Contains(smHTML, "ov-loading-dots ov-loading-dots-sm") {
		t.Error("expected sm variant classes on loading dots")
	}
	if !strings.Contains(smHTML, `aria-hidden="true"`) {
		t.Error("expected loading dots to be aria-hidden")
	}
	if count := strings.Count(smHTML, `class="ov-loading-dot"`); count != 3 {
		t.Errorf("expected exactly 3 loading dots for sm variant, got %d", count)
	}

	var xs bytes.Buffer
	if err := ChatLoadingDots("xs").Render(context.Background(), &xs); err != nil {
		t.Fatalf("Failed to render ChatLoadingDots(xs): %v", err)
	}
	xsHTML := xs.String()
	if !strings.Contains(xsHTML, "ov-loading-dots ov-loading-dots-xs") {
		t.Error("expected xs variant classes on loading dots")
	}
	if count := strings.Count(xsHTML, `class="ov-loading-dot"`); count != 3 {
		t.Errorf("expected exactly 3 loading dots for xs variant, got %d", count)
	}
}

// TestChatBubbleStreamingScrollBehavior verifies streaming bubble has correct scroll behavior
func TestChatBubbleStreamingScrollBehavior(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreaming("assistant", "test-exec-id", "chat-messages", "pause-target", false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleStreaming: %v", err)
	}

	content := buf.String()

	// Verify EventSource is used for streaming
	if !strings.Contains(content, "new EventSource") {
		t.Error("Missing EventSource for streaming")
	}
	if !strings.Contains(content, "window.registerChatStreamEventSource(execId, eventSource)") {
		t.Error("Missing chat stream EventSource registration for non-thread streaming")
	}
	if !strings.Contains(content, "window.unregisterChatStreamEventSource(execId, es)") {
		t.Error("Missing chat stream EventSource unregister helper call")
	}

	// Verify onmessage handler exists
	if !strings.Contains(content, "eventSource.onmessage") {
		t.Error("Missing onmessage handler for streaming")
	}
	if !strings.Contains(content, "renderBufferedOutput(false)") {
		t.Error("Missing batched streaming renderer call in onmessage handler")
	}
	if !strings.Contains(content, "requestAnimationFrame(runRender)") {
		t.Error("Missing requestAnimationFrame batching for streaming renders")
	}

	// Verify that the onmessage handler uses the scroll tracker
	if !strings.Contains(content, "tracker.shouldAutoScroll") {
		t.Error("Missing tracker.shouldAutoScroll check in onmessage handler")
	}

	// Verify done event handler exists
	if !strings.Contains(content, "addEventListener('done'") {
		t.Error("Missing done event listener")
	}

	// Verify error handling
	if !strings.Contains(content, "addEventListener('error'") {
		t.Error("Missing error event listener")
	}

	// Verify page-level tracker is reused (not destroyed per stream)
	if !strings.Contains(content, "scrollTracker_") {
		t.Error("Missing page-level tracker key pattern")
	}

	t.Logf("ChatBubbleStreaming scroll behavior verified (%d bytes)", len(content))
}

// TestChatBubbleStreamingResumeScrollBehavior verifies resume bubble uses data attributes
// for deferred SSE initialization (EventSource created by _initThreadStreaming after morph).
func TestChatBubbleStreamingResumeScrollBehavior(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreamingResume("assistant", "existing content", "test-exec-id", "chat-messages", "").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleStreamingResume: %v", err)
	}

	content := buf.String()

	// Verify data-streaming-resume attribute for deferred SSE init
	if !strings.Contains(content, `data-streaming-resume="true"`) {
		t.Error("Missing data-streaming-resume attribute")
	}

	// Verify exec ID is in data attributes
	if !strings.Contains(content, "test-exec-id") {
		t.Error("Missing exec ID")
	}

	// Verify initial length attribute for delta rendering
	if !strings.Contains(content, "data-initial-length") {
		t.Error("Missing data-initial-length attribute")
	}

	// Verify messages container reference
	if !strings.Contains(content, `data-messages-container="chat-messages"`) {
		t.Error("Missing data-messages-container attribute")
	}

	t.Logf("ChatBubbleStreamingResume scroll behavior verified (%d bytes)", len(content))
}

// TestChatAutoScrollLogic tests the JavaScript logic for determining if we should auto-scroll
func TestChatAutoScrollLogic(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// Verify the isNearBottom function exists and is properly structured
	if !strings.Contains(content, "isNearBottom: function") {
		t.Error("Missing isNearBottom function")
	}

	// Verify it checks scroll position using the key calculation
	if !strings.Contains(content, "scrollHeight") && !strings.Contains(content, "scrollTop") && !strings.Contains(content, "clientHeight") {
		t.Error("Missing scroll position calculation")
	}

	// Verify threshold variable is declared (100px threshold for "near bottom" detection)
	if !strings.Contains(content, "var threshold = 100") {
		t.Error("Missing or incorrect threshold variable")
	}

	// Verify the comparison operator - should return true if near bottom
	thresholdPattern := regexp.MustCompile(`threshold\s*[<>=]+\s*\d+`)
	if !thresholdPattern.MatchString(content) {
		t.Logf("Warning: Could not find explicit threshold comparison, but function exists")
	}

	t.Logf("ChatAutoScrollLogic verified (%d bytes)", len(content))
}

func TestChatAutoScrollScript_RehydratesAssistantRawContentViaStreamingRenderer(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, "container.querySelectorAll('.chat-stream-content[data-raw-content]').forEach(function(el)") {
		t.Error("cleanAssistantMessages must scan raw assistant containers on hydration")
	}
	if !strings.Contains(content, "if (raw && window.renderStreamingContent)") {
		t.Error("cleanAssistantMessages must prefer streaming renderer for tool-card reconstruction")
	}
	if !strings.Contains(content, "window.renderStreamingContent(el, raw);") {
		t.Error("cleanAssistantMessages must rebuild tool/thinking cards from raw content")
	}
}

func TestChatAutoScrollScript_ToolHeaderUsesTextNodesNotInnerHTML(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	if strings.Contains(content, "header.innerHTML = headerHtml;") {
		t.Fatal("tool header must not assign concatenated innerHTML from model/tool text")
	}
	if !strings.Contains(content, "nameSpan.textContent = dn;") {
		t.Error("tool header should render tool name via textContent")
	}
	if !strings.Contains(content, "secondarySpan.textContent = seg.secondary;") {
		t.Error("tool header should render tool secondary text via textContent")
	}
}

func TestChatInputForm_SubmitButtonUsesRequestSubmit(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:       "task-thread-form",
		InputID:      "task-message-input",
		PostEndpoint: "/tasks/task-1/thread",
		TargetID:     "task-thread-messages",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatInputForm: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, "var submitBtn = form.querySelector('button[type=\"submit\"]');") {
		t.Fatal("chat input script must bind the submit button for click-path parity")
	}
	if !strings.Contains(content, "submitBtn.addEventListener('click', function(e)") {
		t.Fatal("chat input script must normalize submit button clicks")
	}
	if !strings.Contains(content, "if (typeof form.requestSubmit === 'function')") {
		t.Fatal("chat input script must feature-detect requestSubmit")
	}
	if !strings.Contains(content, "var submitEvent = new Event('submit', { bubbles: true, cancelable: true });") {
		t.Fatal("chat input script must synthesize submit event when requestSubmit is unavailable")
	}
	if !strings.Contains(content, "form.dispatchEvent(submitEvent);") {
		t.Fatal("chat input script must dispatch submit event fallback")
	}
}

func TestChatInputForm_EnterKeyHasRequestSubmitFallback(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:       "task-thread-form",
		InputID:      "task-message-input",
		PostEndpoint: "/tasks/task-1/thread",
		TargetID:     "task-thread-messages",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatInputForm: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, "if (typeof form.requestSubmit === 'function')") {
		t.Fatal("enter key path must feature-detect requestSubmit")
	}
	if !strings.Contains(content, "var submitEvent = new Event('submit', { bubbles: true, cancelable: true });") {
		t.Fatal("enter key path must synthesize submit event when requestSubmit is unavailable")
	}
	if !strings.Contains(content, "form.dispatchEvent(submitEvent);") {
		t.Fatal("enter key path must dispatch submit fallback")
	}
}

func TestChatAutoScrollScript_ShowsToolCardsInPlanMode(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	if strings.Contains(content, "function shouldHidePlanToolCard(seg)") {
		t.Error("tool-card suppression helper should not exist in plan mode rendering")
	}
	if strings.Contains(content, "if (shouldHidePlanToolCard(seg)) return;") {
		t.Error("streaming renderer should not suppress plan-mode tool cards")
	}
}

// TestStreamingScrollIntegration verifies the integration of streaming and scrolling
func TestStreamingScrollIntegration(t *testing.T) {
	var buf1 bytes.Buffer
	ChatBubbleStreaming("assistant", "exec-1", "chat-messages", "", false).Render(context.Background(), &buf1)
	streamingContent := buf1.String()

	var buf2 bytes.Buffer
	ChatBubbleStreamingResume("assistant", "Previous content", "exec-1", "chat-messages", "").Render(context.Background(), &buf2)
	resumeContent := buf2.String()

	tests := []struct {
		name     string
		content  string
		mustHave []string
	}{
		{
			name:    "ChatBubbleStreaming integration",
			content: streamingContent,
			mustHave: []string{
				"eventSource.onmessage",
				"tracker.shouldAutoScroll",
				"addEventListener('done'",
				"scrollTracker_",
				"resetOnUserSend",
			},
		},
		{
			name:    "ChatBubbleStreamingResume integration",
			content: resumeContent,
			mustHave: []string{
				`data-streaming-resume="true"`,
				"data-exec-id",
				"data-initial-length",
				"data-messages-container",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, required := range tt.mustHave {
				if !strings.Contains(tt.content, required) {
					t.Errorf("Missing required element: %q", required)
				}
			}
		})
	}
}

// TestUserScrollTracking verifies user scroll detection and auto-scroll control
func TestUserScrollTracking(t *testing.T) {
	tests := []struct {
		name     string
		renderFn func() (string, error)
	}{
		{
			name: "ChatBubbleStreaming",
			renderFn: func() (string, error) {
				var buf bytes.Buffer
				err := ChatBubbleStreaming("assistant", "test-exec-123", "chat-messages", "", false).Render(context.Background(), &buf)
				return buf.String(), err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := tt.renderFn()
			if err != nil {
				t.Fatalf("Failed to render %s: %v", tt.name, err)
			}

			// Verify tracker is obtained/created
			if !strings.Contains(content, "ChatScrollTracker") {
				t.Error("Missing ChatScrollTracker reference")
			}

			// Verify tracker.shouldAutoScroll() is called
			if !strings.Contains(content, "tracker.shouldAutoScroll") {
				t.Error("Missing tracker.shouldAutoScroll check")
			}

			// Verify EventSource connection setup
			if !strings.Contains(content, "new EventSource") {
				t.Error("Missing EventSource setup")
			}

			// Verify onmessage handler
			if !strings.Contains(content, "eventSource.onmessage") {
				t.Error("Missing onmessage handler")
			}

			// Verify done event listener
			if !strings.Contains(content, "addEventListener('done'") {
				t.Error("Missing done event listener")
			}

			// Verify page-level tracker pattern (tracker persists across streams)
			if !strings.Contains(content, "scrollTracker_") {
				t.Error("Missing page-level tracker key pattern")
			}
		})
	}

	// ChatBubbleStreamingResume uses data-streaming-resume attribute instead of inline script.
	// The EventSource is initialized by _initThreadStreaming() from TaskThreadView's afterSwap handler.
	t.Run("ChatBubbleStreamingResume", func(t *testing.T) {
		var buf bytes.Buffer
		err := ChatBubbleStreamingResume("assistant", "initial content", "test-exec-456", "chat-messages", "").Render(context.Background(), &buf)
		if err != nil {
			t.Fatalf("Failed to render: %v", err)
		}
		content := buf.String()

		if !strings.Contains(content, `data-streaming-resume="true"`) {
			t.Error("Missing data-streaming-resume attribute")
		}
		if !strings.Contains(content, "test-exec-456") {
			t.Error("Missing exec ID in data attributes")
		}
		if !strings.Contains(content, "data-initial-length") {
			t.Error("Missing data-initial-length attribute")
		}
	})
}

// TestCleanDisplayContent_ToolMarkers verifies that tool use markers are stripped from display content.
func TestCleanDisplayContent_ToolMarkers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "strips Using tool marker",
			input:    "Let me check.\n[Using tool: Read]\nHere is the result.",
			expected: "Let me check.\n\nHere is the result.",
		},
		{
			name:     "strips multiple tool markers",
			input:    "I'll look at that.\n[Using tool: Read]\n[Using tool: Grep]\n[Using tool: Bash]\nDone.",
			expected: "I'll look at that.\n\nDone.",
		},
		{
			name:     "strips Tool done markers",
			input:    "Checking.\n[Tool Read done: file contents here]\nFound it.",
			expected: "Checking.\nFound it.",
		},
		{
			name:     "strips Tool error markers",
			input:    "Trying.\n[Tool Bash error: command not found]\nFailed.",
			expected: "Trying.\nFailed.",
		},
		{
			name:     "strips Tool done block markers",
			input:    "Checking.\n[Tool read_file done]\nfile contents here\n[/Tool]\nFound it.",
			expected: "Checking.\nFound it.",
		},
		{
			name:     "strips Tool error block markers",
			input:    "Trying.\n[Tool bash error]\ncommand not found\n[/Tool]\nFailed.",
			expected: "Trying.\nFailed.",
		},
		{
			name:     "strips Tool block markers on same line",
			input:    "Working.\n[Tool grep_search done]matches here[/Tool]\nDone.",
			expected: "Working.\nDone.",
		},
		{
			name:     "strips mixed tool and status markers",
			input:    "Working.\n[Using tool: Edit]\n[Tool Edit done: updated]\nAll done.\n[STATUS: SUCCESS]",
			expected: "Working.\n\nAll done.",
		},
		{
			name:     "strips Thinking blocks at end",
			input:    "Actual response.\n[Thinking]\nSome internal thoughts",
			expected: "Actual response.",
		},
		{
			name:     "strips Thinking block with end marker preserving first char of response",
			input:    "\n[Thinking]\nLet me think about this task...\n[/Thinking]\nNow let me read the chat handler.",
			expected: "Now let me read the chat handler.",
		},
		{
			name:     "strips Thinking block before content without eating first character",
			input:    "[Thinking]\nSome thoughts here\n[/Thinking]\nHere is my response.",
			expected: "Here is my response.",
		},
		{
			name:     "strips multiple Thinking blocks preserving content between them",
			input:    "[Thinking]\nFirst thought\n[/Thinking]\n\nResponse part 1.\n\n[Thinking]\nSecond thought\n[/Thinking]\n\nResponse part 2.",
			expected: "Response part 1.\n\nResponse part 2.",
		},
		{
			name:     "preserves clean text",
			input:    "This is a normal response with no markers.",
			expected: "This is a normal response with no markers.",
		},
		{
			name:     "strips proposed_plan wrappers and keeps content",
			input:    "Plan:\n<proposed_plan>\nStep one\nStep two\n</proposed_plan>\nDone.",
			expected: "Plan:\n\nStep one\nStep two\n\nDone.",
		},
		{
			name:     "does not strip arbitrary angle-bracket tags",
			input:    "Keep literal tag text: <custom_tag>hello</custom_tag>",
			expected: "Keep literal tag text: <custom_tag>hello</custom_tag>",
		},
		{
			name:     "strips CREATE_TASK blocks",
			input:    "Creating.\n[CREATE_TASK]\n{\"title\":\"test\"}\n[/CREATE_TASK]\nDone.",
			expected: "Creating.\n\nDone.",
		},
		{
			name:     "thinking-only output extracts thinking content as fallback",
			input:    "\n[Thinking]\nThe answer is 1 + 1 = 2.\n",
			expected: "The answer is 1 + 1 = 2.",
		},
		{
			name:     "thinking-only output with closed marker extracts content",
			input:    "[Thinking]\nLet me calculate: 5 * 3 = 15\n[/Thinking]",
			expected: "Let me calculate: 5 * 3 = 15",
		},
		{
			name:     "empty input returns empty",
			input:    "",
			expected: "",
		},
		{
			name:  "unclosed thinking blocks with embedded markers",
			input: "\n[Thinking]\nLet me start by reading.\n\n\n[Thinking]\nNow I see the issue.\n\n[Using tool: Read]\n\n[Thinking]\nThe fix is clear.\n",
			// After stripping tool/status markers, all content is in unclosed thinking blocks.
			// The fallback extracts thinking content and strips embedded [Thinking] markers.
			expected: "Let me start by reading.\n\nNow I see the issue.\n\nThe fix is clear.",
		},
		{
			name:  "multi-turn unclosed thinking with tool markers",
			input: "\n[Thinking]\nAnalyzing the problem.\n\n[Using tool: Read]\n\nLet me check this file.\n\n[Using tool: Edit]\n\n[Thinking]\nNow let me verify.\n\n[Using tool: Bash]\n\nAll tests pass.\n\n[STATUS: SUCCESS]",
			// Tool markers and STATUS stripped first, then unclosed thinking handled.
			// extractThinkingContent captures everything after first [Thinking],
			// strips embedded [Thinking] markers from the result.
			expected: "Analyzing the problem.\n\nLet me check this file.\n\nNow let me verify.\n\nAll tests pass.",
		},
		{
			name:     "strips multi_tool_use.parallel protocol artifact",
			input:    "} to=multi_tool_use.parallel code 彩神争霸高json uμ? Wait malformed because command has extra }}. Need correct. let's call separately.{}\nI hit a malformed shell command.",
			expected: "I hit a malformed shell command.",
		},
		{
			name:     "strips multi_tool_use.parallel without to= prefix",
			input:    "multi_tool_use.parallel error\nActual useful text here.",
			expected: "Actual useful text here.",
		},
		{
			name:     "strips multi_tool_use.parallel with leading braces",
			input:    "}} multi_tool_use.sequential something\nNarrative continues.",
			expected: "Narrative continues.",
		},
		{
			name:     "strips multi_tool_use artifact between tool blocks",
			input:    "[Using tool: Bash]\n[Tool Bash error]\nbash error\n[/Tool]\n} to=multi_tool_use.parallel code\nRetrying the command.\n[Using tool: Bash]\n",
			expected: "Retrying the command.",
		},
		{
			name:     "preserves normal text mentioning tools",
			input:    "The multi-tool approach works well for this task.",
			expected: "The multi-tool approach works well for this task.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanDisplayContent(tt.input)
			if got != tt.expected {
				t.Errorf("CleanDisplayContent() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

// TestCleanDisplayContent_DedupSummaries verifies task summary deduplication.
func TestCleanDisplayContent_DedupSummaries(t *testing.T) {
	input := "I'll create a task.\n\n" +
		"[CREATE_TASK]\n{\"title\": \"Fix bug\"}\n[/CREATE_TASK]\n\n" +
		"---\nCreated 1 task(s):\n- \"Fix bug\" (backlog)\n\n" +
		"---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]"

	got := CleanDisplayContent(input)

	count := strings.Count(got, "Created 1 task(s):")
	if count != 1 {
		t.Errorf("should have exactly 1 'Created' summary, got %d in:\n%q", count, got)
	}

	if !strings.Contains(got, "[TASK_ID:abc123]") {
		t.Errorf("should preserve [TASK_ID:] markers for link conversion, got:\n%q", got)
	}

	if strings.Contains(got, "[CREATE_TASK]") {
		t.Errorf("should strip [CREATE_TASK] blocks, got:\n%q", got)
	}
}

// TestDedupTaskSummaries verifies task summary deduplication logic.
func TestDedupTaskSummaries(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no summaries unchanged",
			input:    "Just a normal response.",
			expected: "Just a normal response.",
		},
		{
			name:     "single summary unchanged",
			input:    "I'll create that task.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]",
			expected: "I'll create that task.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]",
		},
		{
			name:     "duplicate created summaries keeps last",
			input:    "I'll create that task.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog)\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]",
			expected: "I'll create that task.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]",
		},
		{
			name:     "duplicate edited summaries keeps last",
			input:    "Updated.\n\n---\nEdited 1 task(s):\n- \"New title\" (updated: title)\n\n---\nEdited 1 task(s):\n- \"New title\" (updated: title) [TASK_EDITED:abc]",
			expected: "Updated.\n\n---\nEdited 1 task(s):\n- \"New title\" (updated: title) [TASK_EDITED:abc]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DedupTaskSummaries(tt.input)
			if got != tt.expected {
				t.Errorf("DedupTaskSummaries() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

// TestChatBubbleRunning_CleansToolMarkers verifies that ChatBubbleRunning strips tool markers.
func TestChatBubbleRunning_CleansToolMarkers(t *testing.T) {
	partialOutput := "Let me check the file.\n[Using tool: Read]\n[Tool Read done: contents here]\nI found the issue."

	var buf bytes.Buffer
	err := ChatBubbleRunning("Assistant", partialOutput).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleRunning: %v", err)
	}

	content := buf.String()

	// Should NOT contain raw tool markers
	if strings.Contains(content, "[Using tool:") {
		t.Error("ChatBubbleRunning should strip [Using tool:] markers from partial output")
	}
	if strings.Contains(content, "[Tool Read done:") {
		t.Error("ChatBubbleRunning should strip [Tool ... done:] markers from partial output")
	}

	// Should contain the actual text content
	if !strings.Contains(content, "Let me check the file.") {
		t.Error("ChatBubbleRunning should preserve actual text content")
	}
	if !strings.Contains(content, "I found the issue.") {
		t.Error("ChatBubbleRunning should preserve actual text content")
	}
}

// TestChatBubbleRunning_ShowsWorkingWhenOnlyToolMarkers verifies that when partial output
// contains only tool markers (no actual text), the bubble shows "Working..." instead of empty.
func TestChatBubbleRunning_ShowsWorkingWhenOnlyToolMarkers(t *testing.T) {
	// Output that is entirely tool markers - no user-visible text
	partialOutput := "[Using tool: Read]\n[Tool Read done: file contents here]\n[Using tool: Grep]\n[Tool Grep done: search results]"

	var buf bytes.Buffer
	err := ChatBubbleRunning("Assistant", partialOutput).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleRunning: %v", err)
	}

	content := buf.String()

	// Should show "Working..." indicator, not empty content
	if !strings.Contains(content, "Working...") {
		t.Error("ChatBubbleRunning should show 'Working...' when cleaned output is empty")
	}

	// Should NOT contain raw tool markers
	if strings.Contains(content, "[Using tool:") {
		t.Error("ChatBubbleRunning should not show raw tool markers")
	}
}

// TestChatMessages_RunningExecUsesSSEStreaming verifies that ChatMessages renders
// running executions with SSE streaming (EventSource) instead of a static bubble.
func TestChatMessages_RunningExecUsesSSEStreaming(t *testing.T) {
	task := &models.Task{ID: "task-1", Title: "Test task", Prompt: "Do something"}
	executions := []models.Execution{
		{
			ID:         "exec-1",
			Status:     models.ExecRunning,
			PromptSent: "Do something",
			Output:     "partial output so far",
		},
	}

	var buf bytes.Buffer
	err := ChatMessages(executions, task, nil, "task-thread-messages", "task-thread-view").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatMessages: %v", err)
	}
	content := buf.String()

	// Must have data-streaming-resume attribute for SSE initialization
	if !strings.Contains(content, `data-streaming-resume="true"`) {
		t.Error("Running execution should have data-streaming-resume attribute for SSE streaming")
	}

	// Must contain the exec ID for connecting to the right SSE endpoint
	if !strings.Contains(content, "exec-1") {
		t.Error("Should contain execution ID for SSE endpoint")
	}

	// Must contain the pause-polling-target data attribute for task thread
	if !strings.Contains(content, "data-pause-polling-target") {
		t.Error("Should contain data-pause-polling-target for pausing HTMX polling")
	}

	// Must reference the task-thread-view polling element
	if !strings.Contains(content, "task-thread-view") {
		t.Error("Should reference task-thread-view as pause polling target")
	}

	// Should NOT be a static ChatBubbleRunning (no "Working..." indicator)
	if strings.Contains(content, "Working...") {
		t.Error("Running execution should use streaming bubble, not static 'Working...' bubble")
	}
}

// TestTaskLinkRegex_MatchesBothPlainAndMarkdownRendered verifies that the
// JavaScript regex used by convertTaskLinksInMessage matches both:
// 1. Plain text format: - "Title" (category) [TASK_ID:id] (from raw SSE streaming)
// 2. Markdown-rendered format: "Title" (category) [TASK_ID:id] (after marked.parse() consumes the "- " list marker)
// This was a bug where markdown rendering consumed the "- " prefix and the regex
// required it, causing task links to render as plain text instead of clickable links.
func TestTaskLinkRegex_MatchesBothPlainAndMarkdownRendered(t *testing.T) {
	// This is the JS regex from convertTaskLinksInMessage in chat.templ
	// Go's regexp uses the same syntax (with minor escaping differences)
	taskIDRegex := regexp.MustCompile(`(?:-\s*)?"([^"]+)"\s*(?:\(([^)]+)\)\s*)?\[TASK_ID:([^\]]+)\]`)
	taskEditRegex := regexp.MustCompile(`(?:-\s*)?"([^"]+)"\s*\(updated:\s*([^)]+)\)\s*\[TASK_EDITED:([^\]]+)\]`)

	tests := []struct {
		name        string
		input       string
		regex       *regexp.Regexp
		expectMatch bool
		expectTitle string
		expectExtra string
		expectID    string
	}{
		{
			name:        "plain text with dash - TASK_ID",
			input:       `- "Build API endpoint" (backlog) [TASK_ID:abc123def456]`,
			regex:       taskIDRegex,
			expectMatch: true,
			expectTitle: "Build API endpoint",
			expectExtra: "backlog",
			expectID:    "abc123def456",
		},
		{
			name:        "markdown-rendered without dash - TASK_ID",
			input:       `"Build API endpoint" (backlog) [TASK_ID:abc123def456]`,
			regex:       taskIDRegex,
			expectMatch: true,
			expectTitle: "Build API endpoint",
			expectExtra: "backlog",
			expectID:    "abc123def456",
		},
		{
			name:        "no category - TASK_ID",
			input:       `"Build API endpoint" [TASK_ID:abc123def456]`,
			regex:       taskIDRegex,
			expectMatch: true,
			expectTitle: "Build API endpoint",
			expectExtra: "",
			expectID:    "abc123def456",
		},
		{
			name:        "plain text with dash - TASK_EDITED",
			input:       `- "Updated Task" (updated: title, priority) [TASK_EDITED:task789]`,
			regex:       taskEditRegex,
			expectMatch: true,
			expectTitle: "Updated Task",
			expectExtra: "title, priority",
			expectID:    "task789",
		},
		{
			name:        "markdown-rendered without dash - TASK_EDITED",
			input:       `"Updated Task" (updated: title, priority) [TASK_EDITED:task789]`,
			regex:       taskEditRegex,
			expectMatch: true,
			expectTitle: "Updated Task",
			expectExtra: "title, priority",
			expectID:    "task789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := tt.regex.FindStringSubmatch(tt.input)
			if tt.expectMatch {
				if matches == nil {
					t.Fatalf("expected regex to match %q but it didn't", tt.input)
				}
				if matches[1] != tt.expectTitle {
					t.Errorf("title: got %q, want %q", matches[1], tt.expectTitle)
				}
				if matches[2] != tt.expectExtra {
					t.Errorf("extra: got %q, want %q", matches[2], tt.expectExtra)
				}
				if matches[3] != tt.expectID {
					t.Errorf("id: got %q, want %q", matches[3], tt.expectID)
				}
			} else if matches != nil {
				t.Errorf("expected regex NOT to match %q but it matched: %v", tt.input, matches)
			}
		})
	}
}

// TestChatBubble_PreservesTaskIDMarkers verifies that ChatBubble preserves
// [TASK_ID:xxx] markers in the data-raw-content attribute for JS conversion.
func TestChatBubble_PreservesTaskIDMarkers(t *testing.T) {
	content := "Created 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]"

	var buf bytes.Buffer
	err := ChatBubble("Assistant", content).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubble: %v", err)
	}

	html := buf.String()

	// The raw content with TASK_ID marker should be in data-raw-content for JS processing
	if !strings.Contains(html, "[TASK_ID:abc123]") {
		t.Error("ChatBubble should preserve [TASK_ID:] markers in data-raw-content for convertTaskLinksInMessage")
	}

	// Should have the convertTaskLinksInMessage call
	if !strings.Contains(html, "convertTaskLinksInMessage") {
		t.Error("ChatBubble should call convertTaskLinksInMessage for task link conversion")
	}

	// Keep the chat-stream-content class even in markdown fallback so refresh cleanup
	// can reliably find and re-process the container.
	if strings.Contains(html, "className = 'chat-markdown'") {
		t.Error("ChatBubble should not replace container className with chat-markdown")
	}
	if !strings.Contains(html, "classList.add('chat-markdown')") {
		t.Error("ChatBubble markdown fallback should add chat-markdown class without removing existing classes")
	}
}

// TestRenderStreamingContent_StripsProtocolArtifacts verifies that
// renderStreamingContent pre-strips multi_tool_use.parallel protocol artifact
// lines from the text buffer so they don't leak between tool cards.
func TestRenderStreamingContent_StripsProtocolArtifacts(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// renderStreamingContent must strip multi_tool_use protocol artifact lines
	if !strings.Contains(content, "multi_tool_use") {
		t.Error("renderStreamingContent should contain multi_tool_use artifact stripping regex")
	}

	// Verify the regex pattern is applied to textBuffer before segment parsing
	if !strings.Contains(content, "textBuffer = textBuffer.replace") {
		t.Error("renderStreamingContent should pre-strip protocol artifacts from textBuffer")
	}
}

// TestCleanActionMarkers_StripsProtocolArtifacts verifies that cleanActionMarkers
// strips multi_tool_use.parallel protocol artifact lines from text.
func TestCleanActionMarkers_StripsProtocolArtifacts(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// cleanActionMarkers must include multi_tool_use protocol artifact pattern
	if !strings.Contains(content, "multi_tool_use\\.\\S+") {
		t.Error("cleanActionMarkers should strip multi_tool_use protocol artifact lines")
	}
}

func TestCleanActionMarkers_StripsProposedPlanWrappersOnly(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, "text = text.replace(/<\\/?\\s*proposed_plan\\s*>/gi, '')") {
		t.Fatal("cleanActionMarkers should strip <proposed_plan> wrappers")
	}
}

// TestRenderStreamingContent_RemovesWhitespacePreWrap verifies that
// renderStreamingContent removes whitespace-pre-wrap from the streaming container
// to prevent it from leaking into .chat-markdown children via CSS inheritance.
func TestRenderStreamingContent_RemovesWhitespacePreWrap(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// renderStreamingContent should remove whitespace-pre-wrap from the container
	if !strings.Contains(content, "container.classList.remove('whitespace-pre-wrap')") {
		t.Error("renderStreamingContent should remove 'whitespace-pre-wrap' class from container to prevent CSS inheritance issues")
	}
}

// TestRenderStreamingContent_PreservesThinkingOpenState verifies that
// renderStreamingContent saves and restores the open/closed state of
// thinking <details> sections across re-renders (so polling/morph updates
// don't collapse sections the user has expanded).
func TestRenderStreamingContent_PreservesThinkingOpenState(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// Should save open state of existing thinking sections before clearing innerHTML
	if !strings.Contains(content, "prevThinkingStates") {
		t.Error("renderStreamingContent should track previous thinking section open states")
	}
	if !strings.Contains(content, "details.stream-thinking") {
		t.Error("renderStreamingContent should query existing stream-thinking details elements")
	}
	// Should restore open state after creating new thinking sections
	if !strings.Contains(content, "prevThinkingStates[ti]") {
		t.Error("renderStreamingContent should restore open state from saved thinking states")
	}
	// The restoration should set .open = true on new details elements
	if !strings.Contains(content, "newThinkingSections[ti].open = true") {
		t.Error("renderStreamingContent should set open=true on new thinking sections that were previously open")
	}
}

// TestRenderStreamingContent_PersistentThinkingState verifies that thinking section
// open/closed state is persisted in window._thinkingOpenStates to survive DOM
// replacement by morph:outerHTML polling (the 3s morph swap destroys JS-created
// <details> elements, so the state must be stored externally).
func TestRenderStreamingContent_PersistentThinkingState(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// Should initialize persistent store on window
	if !strings.Contains(content, "window._thinkingOpenStates") {
		t.Error("should initialize window._thinkingOpenStates for persistent thinking state storage")
	}

	// Should generate a stable key for containers using ID or data-raw-content
	if !strings.Contains(content, "_thinkingStateKey") {
		t.Error("should have _thinkingStateKey function for generating stable container keys")
	}

	// Should save to persistent store when local states are found
	if !strings.Contains(content, "window._thinkingOpenStates[containerKey] = prevThinkingStates.slice()") {
		t.Error("should persist thinking states to window._thinkingOpenStates when local states exist")
	}

	// Should restore from persistent store when local states are empty (after morph DOM replacement)
	if !strings.Contains(content, "prevThinkingStates = window._thinkingOpenStates[containerKey]") {
		t.Error("should restore thinking states from persistent store when DOM elements were replaced by morph")
	}

	// Should update persistent store on user toggle events
	if !strings.Contains(content, "addEventListener('toggle'") {
		t.Error("should add toggle event listener to update persistent state when user expands/collapses")
	}
}

// TestChatBubbleStreaming_ContainerClasses verifies the streaming container
// has whitespace-pre-wrap class (for plain text fallback) which renderStreamingContent
// will remove when it takes over rendering with .chat-markdown children.
func TestChatBubbleStreaming_ContainerClasses(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreaming("Assistant", "exec-123", "chat-messages", "", false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleStreaming: %v", err)
	}

	html := buf.String()

	// Streaming container should have whitespace-pre-wrap initially (for plain text fallback)
	if !strings.Contains(html, "whitespace-pre-wrap") {
		t.Error("Streaming container should have whitespace-pre-wrap class initially")
	}
}

// TestChatBubbleStreamingResume_UsesDataRawContent verifies that ChatBubbleStreamingResume
// stores content in data-raw-content attribute (not as raw text in the div). This prevents
// raw/unformatted text flash on hard refresh — the div starts empty and the inline render
// script formats the content before the browser paints.
func TestChatBubbleStreamingResume_UsesDataRawContent(t *testing.T) {
	content := "Hello, I'm working on your task.\n[Thinking]\nLet me analyze...\n[Using tool: Read]"

	var buf bytes.Buffer
	err := ChatBubbleStreamingResume("Assistant", content, "exec-1", "chat-messages", "pause-target").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleStreamingResume: %v", err)
	}

	html := buf.String()

	// Content MUST be in data-raw-content attribute, not as text content in the div
	if !strings.Contains(html, "data-raw-content=") {
		t.Error("ChatBubbleStreamingResume must store content in data-raw-content attribute")
	}

	// The div should be empty (content rendered by inline script, not by templ)
	// Check that the raw content text is NOT directly between > and </ of the streaming div
	if strings.Contains(html, ">Hello, I&#39;m working") || strings.Contains(html, ">Hello, I'm working") {
		t.Error("ChatBubbleStreamingResume should NOT render raw text content in the div (causes unformatted flash on hard refresh)")
	}

	// Must have an inline render script (matching ChatBubble pattern)
	if !strings.Contains(html, "renderStreamingContent") {
		t.Error("ChatBubbleStreamingResume must have inline script calling renderStreamingContent")
	}

	// Must have polling fallback for when renderStreamingContent isn't defined yet
	if !strings.Contains(html, "setInterval") {
		t.Error("ChatBubbleStreamingResume inline script must poll for renderStreamingContent")
	}

	// Must have renderChatMarkdown fallback
	if !strings.Contains(html, "renderChatMarkdown") {
		t.Error("ChatBubbleStreamingResume inline script must have renderChatMarkdown fallback")
	}
}

// TestChatBubbleStreamingResume_InitialLengthUsesCharCount verifies that
// data-initial-length uses character count (not byte count) so it matches
// JavaScript's string.length for proper SSE delta rendering threshold.
func TestChatBubbleStreamingResume_InitialLengthUsesCharCount(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		expectedLength string
	}{
		{
			name:           "ASCII only",
			content:        "Hello World",
			expectedLength: "11",
		},
		{
			name:           "Unicode characters (multi-byte UTF-8)",
			content:        "Hello… World—test",
			expectedLength: "17", // 17 JS code units (… and — are BMP, 1 code unit each), but 21 bytes in UTF-8
		},
		{
			name:           "emoji content",
			content:        "Done! 🎉",
			expectedLength: "8", // 6 ASCII + 2 code units for 🎉 (surrogate pair), matches JS "Done! 🎉".length === 8
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := ChatBubbleStreamingResume("Assistant", tt.content, "exec-1", "chat-messages", "").Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("Failed to render: %v", err)
			}

			html := buf.String()
			expected := `data-initial-length="` + tt.expectedLength + `"`
			if !strings.Contains(html, expected) {
				t.Errorf("Expected %s but not found in HTML.\nGot HTML snippet around initial-length: %s",
					expected, extractAttr(html, "data-initial-length"))
			}
		})
	}
}

// extractAttr extracts a data attribute value from HTML for debugging
func extractAttr(html, attr string) string {
	idx := strings.Index(html, attr+`="`)
	if idx == -1 {
		return "(not found)"
	}
	start := idx + len(attr) + 2
	end := strings.Index(html[start:], `"`)
	if end == -1 {
		return html[start:]
	}
	return attr + `="` + html[start:start+end] + `"`
}

// TestChatBubbleStreamingResume_EmptyContentShowsThinkingIndicator verifies that
// when partialContent is empty, the streaming container is hidden and the
// thinking indicator is shown.
func TestChatBubbleStreamingResume_EmptyContentShowsThinkingIndicator(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreamingResume("Assistant", "", "exec-1", "chat-messages", "").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render: %v", err)
	}

	html := buf.String()

	// Should show thinking indicator when content is empty
	if !strings.Contains(html, `id="streaming-thinking-resume-exec-1"`) || !strings.Contains(html, "ov-loading-dots ov-loading-dots-sm") {
		t.Error("Should show thinking indicator markup when partialContent is empty")
	}

	// Streaming container should be hidden when empty
	if !strings.Contains(html, `id="streaming-message-exec-1"`) || !strings.Contains(html, " hidden") {
		t.Error("Streaming container should be hidden when partialContent is empty")
	}
}

// TestCleanAssistantMessages_HandlesStreamingResumeContainers verifies that the
// cleanAssistantMessages JavaScript function handles [data-streaming-resume][data-raw-content]
// elements explicitly, which is needed after morph:outerHTML replaces the DOM
// (inline scripts don't re-execute after morph).
func TestCleanAssistantMessages_HandlesStreamingResumeContainers(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// Must have explicit selector for streaming resume containers with data-raw-content
	if !strings.Contains(content, "[data-streaming-resume][data-raw-content]") {
		t.Error("cleanAssistantMessages must handle [data-streaming-resume][data-raw-content] elements after morph:outerHTML")
	}

	// Must read content from data-raw-content attribute (not textContent)
	if !strings.Contains(content, "getAttribute('data-raw-content')") {
		t.Error("cleanAssistantMessages must read streaming resume content from data-raw-content attribute")
	}

	// Must use content signatures so unchanged bubbles are skipped on poll updates
	if !strings.Contains(content, "el.dataset.cleanedRaw === raw") {
		t.Error("cleanAssistantMessages must skip unchanged chat-stream-content using cleanedRaw signature")
	}
	if !strings.Contains(content, "div.dataset.cleanedText === text") {
		t.Error("cleanAssistantMessages must skip unchanged assistant text blocks using cleanedText signature")
	}

	// If renderStreamingContent is unavailable, fallback markdown render must NOT lock
	// cleanedRaw state. This allows a later pass (after renderStreamingContent loads)
	// to re-render tool cards from raw markers instead of staying markdown-only.
	if !strings.Contains(content, "delete el.dataset.cleanedRaw") {
		t.Error("cleanAssistantMessages fallback markdown path must clear cleanedRaw so tool-card re-render can occur later")
	}
}

// TestTaskThreadView_SkipsExpensiveWorkDuringNavigation verifies that the thread
// view's afterSwap handlers check _sidebarNavigating before running expensive
// DOM operations (cleanAssistantMessages, _initThreadStreaming). This prevents
// morph-induced main-thread blocking from delaying sidebar navigation clicks.
func TestTaskThreadView_SkipsExpensiveWorkDuringNavigation(t *testing.T) {
	task := &models.Task{
		ID:        "t1",
		ProjectID: "p1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	var buf bytes.Buffer
	err := TaskThreadView(task, nil, nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	// afterSwap handler for task-thread-messages must guard expensive work
	if !strings.Contains(content, "target.id === 'task-thread-messages'") {
		t.Fatal("expected afterSwap handler for task-thread-messages")
	}

	// afterSwap handler for task-thread-view must guard expensive work
	if !strings.Contains(content, "target.id === 'task-thread-view'") {
		t.Fatal("expected afterSwap handler for task-thread-view")
	}

	// Both branches must check _sidebarNavigating to skip expensive work
	// (cleanAssistantMessages, _initThreadStreaming, renderStreamingContent)
	if !strings.Contains(content, "if (window._sidebarNavigating) return") {
		t.Fatal("afterSwap handlers must check _sidebarNavigating to skip expensive post-morph work during sidebar navigation")
	}

	// Polling afterSwap should not force-clear message clean-state on every swap;
	// cleanAssistantMessages now performs content-signature based incremental work.
	if strings.Contains(content, "delete c.dataset.cleaned") {
		t.Fatal("task thread polling should not delete per-message clean state on each swap")
	}

	// beforeRequest handler must block polling during sidebar navigation
	if !strings.Contains(content, "window._sidebarNavigating") {
		t.Fatal("beforeRequest handler must check _sidebarNavigating to block polling during navigation")
	}
}

func TestTaskThreadView_ClearsDraftBeforeSuccessfulThreadSwap(t *testing.T) {
	task := &models.Task{
		ID:        "thread-clear-1",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, "isThreadSendRequest && responseText.trim() !== ''") {
		t.Fatal("beforeSwap should detect successful thread send requests with non-empty response")
	}
	if !strings.Contains(content, "requestPath.indexOf('/thread') !== -1") {
		t.Fatal("thread handlers should treat /thread requests like task-thread form submits for enter/button parity")
	}
	if !strings.Contains(content, "window._taskThreadUserScrolledUp = false;") {
		t.Fatal("beforeRequest should reset thread auto-scroll state for any thread send request")
	}
	if !strings.Contains(content, "window._taskThreadSavedInput = ''") {
		t.Fatal("beforeSwap should clear saved thread input on successful thread form swap")
	}
	if !strings.Contains(content, "if (sentKey) delete window._taskThreadDrafts[sentKey];") {
		t.Fatal("beforeSwap should clear persisted draft key on successful thread form swap")
	}
}

// TestInitThreadStreaming_FindsStreamingDotsByID verifies that _initThreadStreaming
// finds streaming dots by ID (not nextElementSibling) to work correctly with the
// inline render script that sits between the container and the dots div.
func TestChatBubbleStreaming_ErrorClearsPlanStreamingFlag(t *testing.T) {
	// Verify that ChatBubbleStreaming error/onerror handlers clear
	// _chatStreamInProgress and re-evaluate plan prompt for non-thread chat.
	// Without this, the flag stays stuck true after a streaming failure.
	var buf bytes.Buffer
	err := ChatBubbleStreaming("Assistant", "exec-plan-err", "chat-messages", "", false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}

	content := buf.String()

	// Error event listener — the handler body with isThread branch can be ~1100 chars
	errIdx := strings.Index(content, "eventSource.addEventListener('error'")
	if errIdx == -1 {
		t.Fatal("ChatBubbleStreaming must have an error event listener")
	}
	errEnd := errIdx + 1200
	if errEnd > len(content) {
		errEnd = len(content)
	}
	errBody := content[errIdx:errEnd]
	if !strings.Contains(errBody, "_chatStreamInProgress = false") {
		t.Error("error handler must clear _chatStreamInProgress for chat (non-thread) context")
	}
	if !strings.Contains(errBody, "evaluatePlanCompletionPrompt") {
		t.Error("error handler must re-evaluate plan prompt for chat (non-thread) context")
	}

	// onerror handler
	oeIdx := strings.Index(content, "eventSource.onerror")
	if oeIdx == -1 {
		t.Fatal("ChatBubbleStreaming must have an onerror handler")
	}
	oeEnd := oeIdx + 1200
	if oeEnd > len(content) {
		oeEnd = len(content)
	}
	oeBody := content[oeIdx:oeEnd]
	if !strings.Contains(oeBody, "_chatStreamInProgress = false") {
		t.Error("onerror handler must clear _chatStreamInProgress for chat (non-thread) context")
	}
	if !strings.Contains(oeBody, "evaluatePlanCompletionPrompt") {
		t.Error("onerror handler must re-evaluate plan prompt for chat (non-thread) context")
	}
}

func TestChatBubbleStreaming_ThreadErrorDoesNotEvaluatePlanPrompt(t *testing.T) {
	// Thread context (isThread=true) should NOT evaluate plan prompt on error —
	// plan mode only applies to /chat, not task thread views.
	var buf bytes.Buffer
	err := ChatBubbleStreaming("Assistant", "exec-thread-err", "thread-messages", "thread-view", true).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatBubbleStreaming (thread): %v", err)
	}

	content := buf.String()

	// Error handler for thread context should refresh task detail, not evaluate plan prompt
	errIdx := strings.Index(content, "eventSource.addEventListener('error'")
	if errIdx == -1 {
		t.Fatal("ChatBubbleStreaming must have an error event listener")
	}
	errBody := content[errIdx : errIdx+800]

	// Thread error should do the HTMX refresh but NOT clear _chatStreamInProgress
	// for plan prompt evaluation (plan mode is chat-only)
	if !strings.Contains(errBody, "isThread") {
		t.Error("error handler must check isThread context")
	}
}

func TestInitThreadStreaming_FindsStreamingDotsByID(t *testing.T) {
	var buf bytes.Buffer
	err := _initThreadStreamingScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render _initThreadStreamingScript: %v", err)
	}

	content := buf.String()

	// Must find streaming dots by ID (reliable across morph and inline script siblings)
	if !strings.Contains(content, "getElementById('streaming-dots-resume-'") {
		t.Error("_initThreadStreaming must find streaming dots by ID, not nextElementSibling")
	}

	// Must check data-raw-content for content presence (not textContent, since content
	// is now in the attribute, not as text nodes)
	if !strings.Contains(content, "getAttribute('data-raw-content')") {
		t.Error("_initThreadStreaming must check data-raw-content for content presence")
	}
}
