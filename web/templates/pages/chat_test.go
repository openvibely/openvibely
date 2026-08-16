package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
)

func renderChatContentForTest(agents []models.LLMConfig, history []models.Execution, projectID string, attachments map[string][]models.ChatAttachment, pending []models.ThreadInput, latestPlanComplete bool) templ.Component {
	return ChatContent(agents, history, projectID, attachments, pending, latestPlanComplete, false, 30)
}

func TestChatContent_IncludesSharedTaskResultConverters(t *testing.T) {
	var buf bytes.Buffer
	if err := renderChatContentForTest(nil, []models.Execution{{
		ID:         "exec-task-result",
		Status:     models.ExecCompleted,
		PromptSent: "Create task",
		Output:     "- \"Created\" (backlog) [TASK_ID:task-1]\nExamples: `create\n[TASK_ID:coded/create]` and ``edit\n[TASK_EDITED:coded/edit]``\nUnmatched `` prefix; `later\n[TASK_ID:later/create]\n[TASK_EDITED:later/edit]`\nEscaped \\`- \"Escaped\" (backlog) [TASK_ID:escaped/create] escaped \\`\nEscaped \\``- \"Escaped edit\" (updated: title) [TASK_EDITED:escaped/edit]``\nUnicode fence:\n~~~text\n~~~\u202f\n- \"Unicode\" (backlog) [TASK_ID:unicode/create]\n- \"Unicode edit\" (updated: title) [TASK_EDITED:unicode/edit]\n~~~\nBare CR fence:\n```text\r- \"Bare CR\" (backlog) [TASK_ID:bare-cr/create]\r- \"Bare CR edit\" (updated: title) [TASK_EDITED:bare-cr/edit]\r```\nSame-line example: `[Tool grep_search done]coded[/Tool]`.\n[Using tool: grep_search]\n[Tool grep_search done]actual[/Tool]"}}, "project-1", nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	for _, definition := range []string{
		"window.convertTaskLinksInMessage = function(messageElement)",
		"window.convertTaskEditLinksInMessage = function(messageElement)",
	} {
		if count := strings.Count(content, definition); count != 1 {
			t.Fatalf("global Chat must include exactly one shared task-result converter %q, got %d", definition, count)
		}
	}
	if !strings.Contains(content, "if (window.convertTaskLinksInMessage) window.convertTaskLinksInMessage(bubble)") {
		t.Fatal("global Chat hydration must apply shared task-result conversion")
	}
	if count := strings.Count(content, "if (inCode && !inToolOutput) continue"); count != 2 {
		t.Fatalf("global Chat shared converters must preserve ordinary code while allowing code-aware raw tool-output conversion, got %d guards", count)
	}
	if count := strings.Count(content, "(inToolOutput || inMarkdownFallback) && window.isInsideCode"); count != 2 {
		t.Fatalf("global Chat fallback hydration must keep coded task-result examples inert, got %d raw-range guards", count)
	}
	for _, marker := range []string{"[TASK_ID:coded/create]", "[TASK_EDITED:coded/edit]", "[TASK_ID:later/create]", "[TASK_EDITED:later/edit]", "[TASK_ID:escaped/create]", "[TASK_EDITED:escaped/edit]", "[TASK_ID:unicode/create]", "[TASK_EDITED:unicode/edit]", "[TASK_ID:bare-cr/create]", "[TASK_EDITED:bare-cr/edit]", "`[Tool grep_search done]coded[/Tool]`", "[Tool grep_search done]actual[/Tool]"} {
		if !strings.Contains(content, marker) {
			t.Fatalf("global Chat hard-refresh output must retain multiline inline metadata %s until Markdown rendering", marker)
		}
	}
}

func TestChatContent_RestoresSmartScrollAcrossNavigationAndHistory(t *testing.T) {
	var buf bytes.Buffer
	if err := renderChatContentForTest(nil, []models.Execution{{ID: "exec-scroll", Status: models.ExecCompleted, Output: "later markdown"}}, "project-scroll", nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if count := strings.Count(content, `data-scroll-intent-scope="project-scroll"`); count != 2 {
		t.Fatalf("global Chat transcript and composer must share project-scoped send intent, got %d markers", count)
	}

	if !strings.Contains(content, `id="chat-messages" class="flex-1 min-h-0 overflow-y-auto pt-4 pb-0 space-y-6" style="visibility: hidden;" data-transcript-hydrating="true"`) {
		t.Fatal("global Chat must hide its initial transcript until hydration and scroll restoration settle")
	}
	for _, required := range []string{
		"var chatScrollStateKey = 'chat-scroll-' + projectId;",
		"window._chatPageTracker = window.resolveScrollTracker('scrollTracker_chat-messages', chatMessages);",
		"window.saveChatTranscriptScrollState(chatScrollStateKey, chatMessages, window._chatPageTracker);",
		"window.restoreChatTranscriptScroll({",
		"stateKey: chatScrollStateKey",
		"renderPromise: renderPromise",
		"document.body.addEventListener('htmx:historyRestore'",
		"window.saveChatDraftState = function(root)",
		"document.body.addEventListener('htmx:beforeHistorySave'",
		"if (window.saveChatDraftState) window.saveChatDraftState(currentChatRoot);",
		"window.restoreChatLiveEventHandlers = function()",
		"if (document.getElementById('chat-page-root') && window.restoreChatLiveEventHandlers) window.restoreChatLiveEventHandlers();",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("global Chat navigation restoration is missing %q", required)
		}
	}
	if strings.Contains(content, "window._chatPageTracker.suspendIntent") {
		t.Fatal("global Chat must not suspend scroll tracking before HTMX confirms a swap; cancelled revision/no-op swaps must keep accepting user intent")
	}
	if strings.Contains(content, "// Scroll to bottom on page load to show latest messages") {
		t.Fatal("global Chat must not unconditionally jump before transcript hydration finishes")
	}
	chatRestoreStart := strings.Index(content, "return window.restoreChatTranscriptScroll({")
	if chatRestoreStart < 0 {
		t.Fatal("could not isolate global Chat transcript restore options")
	}
	chatRestoreEnd := strings.Index(content[chatRestoreStart:], "});")
	if chatRestoreEnd < 0 {
		t.Fatal("could not find end of global Chat transcript restore options")
	}
	if strings.Contains(content[chatRestoreStart:chatRestoreStart+chatRestoreEnd], "waitForImages") {
		t.Fatal("task-thread delayed-image reveal policy must not change global Chat scroll restoration")
	}
}

func TestChatContent_MobileComposerStaysWithinViewport(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Very Long Agent Name That Should Not Push Send Button", Model: "very-long-model-name", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()
	required := []string{
		`id="chat-page-root" class="h-full flex flex-col min-w-0 max-w-full"`,
		`id="chat-messages" class="flex-1 min-h-0 overflow-y-auto pt-4 pb-0 space-y-6"`,
		`class="chat-input-shadow-gutter w-full min-w-0 max-w-full mt-6"`,
		`class="chat-input-container rounded-xl p-4 relative min-w-0 max-w-full"`,
		`class="flex items-center justify-between gap-2 pt-2 min-w-0 max-w-full overflow-hidden"`,
		`class="flex items-center gap-2 flex-shrink-0"`,
	}
	for _, expected := range required {
		if !strings.Contains(content, expected) {
			t.Fatalf("chat page composer should stay within the mobile viewport; missing %q", expected)
		}
	}
	if strings.Contains(content, `id="chat-page-root" class="h-full flex flex-col min-w-0 max-w-full overflow-x-hidden"`) {
		t.Fatal("chat page root must not clip the composer shadow; horizontal containment belongs on the messages pane and inner controls")
	}
	if strings.Contains(content, `sm:max-w-3xl`) || strings.Contains(content, `sm:mx-auto`) || strings.Contains(content, `px-3 pt-2 pb-4`) || strings.Contains(content, `pt-2 pb-4`) {
		t.Fatal("chat page composer must not add desktop side gaps, mobile right-side empty space, or extra vertical gutters")
	}
	if strings.Contains(content, `-mr-[29px]`) || strings.Contains(content, `-mr-[18px]`) {
		t.Fatal("chat message panes must not use fixed right-margin scrollbar compensation because it still leaves bubbles visually shorter than the input in real browsers")
	}
	if strings.Contains(content, `chat-input-container rounded-xl p-4 relative w-full`) {
		t.Fatal("chat page composer shell should not use w-full with visual margins because that clips the rounded right edge")
	}
	if strings.Contains(content, `class="chat-input-container rounded-xl p-4 relative min-w-0 max-w-full overflow-x-hidden"`) {
		t.Fatal("chat page composer shell should not clip the rounded right edge")
	}
}

func TestChatContent_PlanSwitchAutoSubmitsImplementationHandoff(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
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
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
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
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
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
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
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

func TestChatContent_LiveCompletionSyncTargetsAssistantStreamContainer(t *testing.T) {
	// Channel-origin live Chat appends an outer execution pair and an inner streaming
	// node that both carry data-exec-id. Completion reconciliation must update the
	// inner assistant stream node only; targeting the outer pair renders assistant
	// content outside the bubble until refresh.
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	syncStart := strings.Index(content, "function syncCompletedOutputToBubble(execId, completedOutput, status)")
	if syncStart == -1 {
		t.Fatal("expected syncCompletedOutputToBubble helper")
	}
	syncEnd := strings.Index(content[syncStart:], "function hasActiveChatStream()")
	if syncEnd == -1 {
		t.Fatal("expected syncCompletedOutputToBubble section terminator")
	}
	syncSection := content[syncStart : syncStart+syncEnd]
	if !strings.Contains(syncSection, "document.getElementById('streaming-message-' + execId)") {
		t.Fatal("completed output sync must target the assistant streaming content node")
	}
	if strings.Contains(syncSection, "document.querySelector('[data-exec-id=\"' + execId + '\"]')") {
		t.Fatal("completed output sync must not target the outer execution pair by broad data-exec-id selector")
	}

	bubbleStart := strings.Index(content, "function createStreamingBubble(execId)")
	if bubbleStart == -1 {
		t.Fatal("expected createStreamingBubble function")
	}
	bubbleEnd := strings.Index(content[bubbleStart:], "if (window._chatLiveEventHandlers)")
	if bubbleEnd == -1 {
		t.Fatal("expected createStreamingBubble section terminator")
	}
	bubbleSection := content[bubbleStart : bubbleStart+bubbleEnd]
	if !strings.Contains(bubbleSection, "contentDiv.id = 'streaming-message-' + execId;") {
		t.Fatal("live-created assistant stream node must use the same stable id as server-rendered streaming bubbles")
	}
	for _, required := range []string{
		"contentDiv.setAttribute('data-streaming-resume', 'true');",
		"contentDiv.setAttribute('data-initial-byte-length', '0');",
		"contentDiv.setAttribute('data-messages-container', 'chat-messages');",
		"thinking.id = 'streaming-thinking-resume-' + execId;",
		"dots.id = 'streaming-dots-resume-' + execId;",
	} {
		if !strings.Contains(bubbleSection, required) {
			t.Fatalf("live-created assistant stream must use the shared resumable DOM contract; missing %q", required)
		}
	}
	if !strings.Contains(bubbleSection, "contentDiv._sseConnected = true;") ||
		!strings.Contains(bubbleSection, "function persistLiveStreamState(active)") ||
		!strings.Contains(bubbleSection, "persistLiveStreamState(true);") ||
		!strings.Contains(bubbleSection, "persistLiveStreamState(false);") {
		t.Fatal("live-created assistant streams must have one owner and persist/remove their resumable state across active and terminal phases")
	}
	if !strings.Contains(content, "function persistResumeStreamState(active)") ||
		!strings.Contains(content, "container.setAttribute('data-initial-byte-length', String(streamOffset));") ||
		!strings.Contains(content, "container.setAttribute('data-raw-content', cumulativeContent);") ||
		!strings.Contains(content, "container.removeAttribute('data-streaming-resume');") {
		t.Fatal("shared streams must persist the current raw UTF-8 offset while active and remove resumable state at terminal completion")
	}
	if !strings.Contains(bubbleSection, "function utf8ByteLength(value)") ||
		!strings.Contains(bubbleSection, "var streamOffset = utf8ByteLength(textBuffer);") ||
		!strings.Contains(bubbleSection, "'/events/chat/' + execId + '?offset=' + encodeURIComponent(streamOffset)") {
		t.Fatal("live-created assistant stream must reconnect with the current UTF-8 byte offset")
	}
	if !strings.Contains(bubbleSection, "progressDiv.id = 'mixture-progress-' + execId;") || !strings.Contains(bubbleSection, "innerDiv.appendChild(progressDiv);") {
		t.Fatal("live-created assistant bubble must include the mixture progress status slot before stream content")
	}
	if !strings.Contains(bubbleSection, "window.hideMixtureProgress(execId)") {
		t.Fatal("live-created assistant bubble must hide mixture progress when aggregator output or terminal stream events arrive")
	}
	if !strings.Contains(bubbleSection, "var renderGeneration = 0;") ||
		!strings.Contains(bubbleSection, "var renderPromise = liveRenderer(contentDiv, renderText);") ||
		!strings.Contains(bubbleSection, "renderPromise.then(finishRender)") ||
		!strings.Contains(bubbleSection, "if (generation !== renderGeneration) return;") {
		t.Fatal("live-created assistant bubble must keep render backpressure until the owned async render settles")
	}
}

func TestChatContent_HandlesMixtureProgressLiveEvents(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "if (eventType === 'mixture_progress')") || !strings.Contains(content, "window.applyMixtureProgress(data)") {
		t.Fatal("Chat live event handler must render mixture_progress into the pending assistant status area")
	}
}

func TestChatContent_LiveBubbleErrorClearsStreamingFlag(t *testing.T) {
	// The createStreamingBubble error/onerror handlers in chat.templ must
	// clear _chatStreamInProgress and re-evaluate plan prompt so the flag
	// doesn't stay stuck after streaming failures.
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()
	bubbleStart := strings.Index(content, "function createStreamingBubble(execId)")
	if bubbleStart == -1 {
		t.Fatal("expected createStreamingBubble function")
	}
	sectionEnd := strings.Index(content[bubbleStart:], "if (window._chatLiveEventHandlers)")
	if sectionEnd == -1 {
		t.Fatal("expected createStreamingBubble section terminator")
	}
	bubbleSection := content[bubbleStart : bubbleStart+sectionEnd]

	// Find the createStreamingBubble function's error handler
	errIdx := strings.Index(bubbleSection, "eventSource.addEventListener('error', function(event) {")
	if errIdx == -1 {
		t.Fatal("expected error event listener in createStreamingBubble")
	}
	errEndOffset := strings.Index(bubbleSection[errIdx:], "eventSource.onerror = function() {")
	if errEndOffset == -1 {
		t.Fatal("expected createStreamingBubble error handler boundary")
	}
	errBody := bubbleSection[errIdx : errIdx+errEndOffset]
	if !strings.Contains(errBody, "event.data === 'execution not found'") || !strings.Contains(errBody, "setTimeout(connectChatExecutionStream, 150 * streamRetryCount)") {
		t.Error("error handler in createStreamingBubble must retry early execution lookup races")
	}
	if !strings.Contains(errBody, "_chatStreamInProgress = false") {
		t.Error("error handler in createStreamingBubble must clear _chatStreamInProgress")
	}
	if !strings.Contains(errBody, "evaluatePlanCompletionPrompt") {
		t.Error("error handler in createStreamingBubble must re-evaluate plan prompt")
	}

	// Also check onerror handler
	oeIdx := strings.Index(bubbleSection, "eventSource.onerror = function() {")
	if oeIdx == -1 {
		t.Fatal("expected onerror handler in createStreamingBubble")
	}
	oeEnd := oeIdx + 1200
	if oeEnd > len(bubbleSection) {
		oeEnd = len(bubbleSection)
	}
	oeBody := bubbleSection[oeIdx:oeEnd]
	if !strings.Contains(oeBody, "setTimeout(connectChatExecutionStream, 150 * streamRetryCount)") {
		t.Error("onerror handler in createStreamingBubble must retry empty early stream failures")
	}
	if !strings.Contains(oeBody, "_chatStreamInProgress = false") {
		t.Error("onerror handler in createStreamingBubble must clear _chatStreamInProgress")
	}
	if !strings.Contains(oeBody, "evaluatePlanCompletionPrompt") {
		t.Error("onerror handler in createStreamingBubble must re-evaluate plan prompt")
	}
}

func TestChatContent_KebabTriggerUsesLabelForDesktopWebviewCompatibility(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `<label tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected chat kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(content, `<button tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("unexpected <button> dropdown trigger in chat header")
	}
}

func TestChatContent_ClearChatDoesNotRequireConfirmation(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `hx-delete="/chat/history?project_id=project-1"`) {
		t.Fatal("expected clear chat action to issue hx-delete request")
	}
	if strings.Contains(content, `hx-confirm="Clear all chat history? This cannot be undone."`) {
		t.Fatal("clear chat action should not require confirmation in desktop app")
	}
}

func TestChatContent_RunningChatCanSteerFromPendingRowsOnly(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}
	history := []models.Execution{{ID: "exec-running", Status: models.ExecRunning, PromptSent: "hello"}}
	pending := []models.ThreadInput{{ID: "queued-1", Scope: models.ThreadInputScopeChat, ProjectID: "project-1", InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued"}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, history, "project-1", map[string][]models.ChatAttachment{}, pending, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `name="expected_turn_id" value="exec-running"`) {
		t.Fatal("chat UI should include the active turn id for queue safety")
	}
	if strings.Contains(content, `name="steer_endpoint"`) || strings.Contains(content, `data-steer-submit="true"`) {
		t.Fatal("chat composer must not expose direct steering controls")
	}
	if !strings.Contains(content, `id="pending-thread-inputs"`) || !strings.Contains(content, `hx-post="/chat/queued/queued-1/steer"`) || !strings.Contains(content, "Steer") {
		t.Fatal("chat composer queued row must expose Steer action")
	}
	messagesStart := strings.Index(content, `id="chat-messages"`)
	formStart := strings.Index(content, `id="chat-form"`)
	if messagesStart < 0 || formStart < 0 || strings.Contains(content[messagesStart:formStart], `thread-input-queued-1`) {
		t.Fatal("chat queued rows should render with the input box, not in the message transcript")
	}
	if !strings.Contains(content, `queued-input-row`) || !strings.Contains(content, `ml-auto`) || !strings.Contains(content, `aria-label="Cancel queued follow-up"`) || !strings.Contains(content, `M19 7l-.867 12.142`) {
		t.Fatal("chat queued row should use input-box styling with right-aligned Steer and trash-icon cancel action")
	}
	if strings.Contains(content, "Send now") || strings.Contains(content, "btn-warning") || strings.Contains(content, "bg-warning") || strings.Contains(content, ">Cancel</button>") || strings.Contains(content, ">×</button>") {
		t.Fatal("chat queued/steering rows should not use warning styling, Send now copy, or text/× cancel")
	}
}

func TestChatContent_RendersCancelledChatExecutions(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}
	history := []models.Execution{{ID: "exec-cancelled", Status: models.ExecCancelled, PromptSent: "hello", Output: "partial"}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, history, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "Cancelled") || !strings.Contains(content, "partial") {
		t.Fatal("cancelled chat executions must render a terminal assistant state")
	}
}

func TestChatContent_LivePromotedQueuedRowsAreRemoved(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "data.pending_input_id") || !strings.Contains(content, "pendingRow.remove()") {
		t.Fatal("live promoted chat events must remove the durable queued row")
	}
}

func TestChatContent_LiveSteeringRowsAreCancelable(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	steeringStart := strings.Index(content, "if (eventType === 'chat_turn_steered')")
	if steeringStart == -1 {
		t.Fatal("expected live steering event branch")
	}
	branchEnd := strings.Index(content[steeringStart:], "if (eventType === 'chat_new_message')")
	if branchEnd == -1 {
		t.Fatal("expected live steering branch terminator")
	}
	branch := content[steeringStart : steeringStart+branchEnd]
	if strings.Contains(branch, "_chatKnownExecIds[data.exec_id]") {
		t.Fatal("live steering pending-input ids must not pollute the chat execution duplicate guard")
	}
	if !strings.Contains(branch, "existingSteeringRow.remove()") || !strings.Contains(branch, "data-input-mode') === 'steering'") {
		t.Fatal("live steering events must replace stale queued rows without duplicating existing steering rows")
	}
	if !strings.Contains(branch, "window._chatQueuedSteerInFlight[data.exec_id]") {
		t.Fatal("same-tab queued-to-steer live events must not remove the local HTMX swap target")
	}
	if !strings.Contains(content, "window._chatDirectSteerInFlight = true") || !strings.Contains(branch, "window._chatDirectSteerInFlight") {
		t.Fatal("same-tab direct steering live events must not duplicate the local HTMX steering row")
	}
	if !strings.Contains(branch, "'/thread-inputs/' + data.exec_id + '/cancel'") {
		t.Fatal("live steering row must expose cancel action")
	}
	if !strings.Contains(branch, "htmx.process(steeringRow)") {
		t.Fatal("live steering row must process dynamic HTMX controls")
	}
	if !strings.Contains(branch, "data.has_attachments") || !strings.Contains(branch, "Attachments included") {
		t.Fatal("live steering row must render the attachment indicator when the event has attachments")
	}
}

func TestChatContent_LiveQueuedAttachmentEventsReachQueuedRowBranch(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	chatNewStart := strings.Index(content, "if (eventType === 'chat_new_message')")
	if chatNewStart == -1 {
		t.Fatal("expected live chat_new_message branch")
	}
	branchEnd := strings.Index(content[chatNewStart:], "// Scroll to bottom")
	if branchEnd == -1 {
		t.Fatal("expected live chat_new_message branch terminator")
	}
	branch := content[chatNewStart : chatNewStart+branchEnd]
	attachmentRefresh := strings.Index(branch, "if (data.has_attachments && !data.queued)")
	queuedBranch := strings.Index(branch, "if (data.queued)")
	queuedBadge := strings.Index(branch, "queuedRow.appendChild(createPendingAttachmentBadge('Attachments queued'")
	if attachmentRefresh == -1 {
		t.Fatal("attachment transcript refresh must be gated to non-queued events so queued attachment rows render")
	}
	if queuedBranch == -1 || queuedBadge == -1 || !(queuedBranch < queuedBadge) {
		t.Fatal("live queued row branch must render the queued attachment indicator")
	}
	if strings.Contains(branch, "if (data.has_attachments) {") {
		t.Fatal("queued attachment events must not be consumed by the non-queued attachment refresh branch")
	}
}

func TestChatContent_ClearsWebSendSuppressionOnRequestCompletion(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	beforeRequest := strings.Index(content, "document.body.addEventListener('htmx:beforeRequest'")
	afterRequest := strings.Index(content, "document.body.addEventListener('htmx:afterRequest'")
	webSendClear := strings.Index(content, "window._chatWebSendInProgress = false")
	if beforeRequest == -1 || afterRequest == -1 || webSendClear == -1 {
		t.Fatal("expected chat form HTMX request lifecycle handlers")
	}
	if !(beforeRequest < afterRequest && afterRequest < webSendClear) {
		t.Fatal("afterRequest clear should be registered after send setup")
	}
	branchEnd := strings.Index(content[afterRequest:], "window._chatKnownExecIds")
	if branchEnd == -1 {
		t.Fatal("expected chat live-event setup after request lifecycle handling")
	}
	branch := content[afterRequest : afterRequest+branchEnd]
	if !strings.Contains(branch, "window._chatWebSendInProgress = false") {
		t.Fatal("web-send suppression must clear on request completion for OOB-only queued responses")
	}
	if strings.Contains(branch, "closest('#chat-form')") {
		t.Fatal("chat request lifecycle handlers must ignore nested HTMX controls such as Stop")
	}
	if !strings.Contains(branch, "triggerEl && triggerEl.id === 'chat-form'") {
		t.Fatal("chat request lifecycle handlers must only treat form-initiated HTMX requests as sends")
	}
}

func TestChatContent_FormDraftClearingIgnoresNestedHTMXControls(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	listener := "chatForm.addEventListener('htmx:afterRequest'"
	idx := strings.Index(content, listener)
	if idx == -1 {
		t.Fatal("expected chat form draft-clearing HTMX listener")
	}
	branchEnd := strings.Index(content[idx:], "})();")
	if branchEnd == -1 {
		t.Fatal("expected end of chat draft persistence closure")
	}
	branch := content[idx : idx+branchEnd]
	if !strings.Contains(branch, "event.detail.elt !== chatForm") {
		t.Fatal("chat form draft clearing must ignore bubbled HTMX events from nested stop/queued controls")
	}
	if !strings.Contains(branch, "responseText.trim() !== ''") || !strings.Contains(branch, "window.clearChatDraftState(chatRoot)") {
		t.Fatal("chat form draft clearing should cancel pending persistence and clear only after an accepted non-empty real send")
	}
	if !strings.Contains(content, "clearTimeout(window._chatDraftSaveTimers[key])") {
		t.Fatal("successful send clearing must cancel a pending debounced draft write")
	}
}

func TestChatContent_LiveQueuedRowsDedupeByPendingInputDOMID(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	messageStart := strings.Index(content, "if (eventType === 'chat_new_message')")
	if messageStart == -1 {
		t.Fatal("expected live chat message branch")
	}
	messageEnd := strings.Index(content[messageStart:], "if (eventType === 'chat_response_done')")
	if messageEnd == -1 {
		t.Fatal("expected live chat message branch terminator")
	}
	branch := content[messageStart : messageStart+messageEnd]

	queuedStart := strings.Index(branch, "if (data.queued) {")
	if queuedStart == -1 {
		t.Fatal("expected queued branch before execution dedupe")
	}
	execDedupeStart := strings.Index(branch, "if (window._chatKnownExecIds[data.exec_id]) return;")
	if execDedupeStart == -1 {
		t.Fatal("expected non-queued execution dedupe branch")
	}
	if queuedStart > execDedupeStart {
		t.Fatal("queued events must be handled before execution duplicate guard")
	}
	queuedBranchEnd := strings.Index(branch[queuedStart:], "} else {")
	if queuedBranchEnd == -1 {
		t.Fatal("expected queued branch to terminate before execution dedupe")
	}
	queuedBranch := branch[queuedStart : queuedStart+queuedBranchEnd]
	if !strings.Contains(queuedBranch, "[data-thread-input-id=\"") {
		t.Fatal("queued events must dedupe by durable pending input DOM id")
	}
	if strings.Contains(queuedBranch, "_chatKnownExecIds") {
		t.Fatal("queued pending-input ids must not pollute the chat execution duplicate guard")
	}
}

func TestChatContent_ResponseDoneRefreshesComposerAction(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "if (eventType === 'chat_response_done')") {
		t.Fatal("expected chat response done live branch")
	}
	if !strings.Contains(content, "/chat/composer-action?project_id=") {
		t.Fatal("chat response done must refresh the primary composer action from the server")
	}
	if !strings.Contains(content, "target: '#chat-form-primary-action'") {
		t.Fatal("chat response done composer refresh must target the primary action only")
	}
}

func TestChatContent_LiveCancelledPendingRowsAreRemoved(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "eventType === 'chat_thread_input_applied' || eventType === 'chat_thread_input_cancelled'") {
		t.Fatal("live cancellation events must remove durable pending rows")
	}
}

func TestChatContent_BindsAttachmentImageSmartScrollAfterRenderAndSwap(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	if count := strings.Count(content, "window.bindAttachmentImageSmartScroll(chatMessages, 'scrollTracker_chat-messages', window._chatPageTracker)"); count != 1 {
		t.Fatalf("expected one shared Chat initialization path to bind image smart-scroll for initial render and HTMX swaps, got %d", count)
	}
	if !strings.Contains(content, "var sentByUser = window.consumeChatSendScrollIntent ? window.consumeChatSendScrollIntent('chat-messages') : false;") {
		t.Fatal("chat page should consume submit scroll intent through the shared initialization path")
	}
	if !strings.Contains(content, "return window.restoreChatTranscriptScroll({") {
		t.Fatal("chat page should reconcile variable-sized attachments and later layout through the shared scroll coordinator")
	}
	if !strings.Contains(content, "if (!window._chatStreamInProgress && window.maybeShowPlanCompletionPromptFromHistory) {") {
		t.Fatal("chat page should re-evaluate plan prompt after non-streaming swaps")
	}
}

func TestChatContent_ReconnectReconcilesChangedTranscriptWithoutReplacingComposer(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}
	history := []models.Execution{{ID: "done-1", Status: models.ExecCompleted, Output: "stable"}}

	var buf bytes.Buffer
	if err := renderChatContentForTest(agents, history, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()

	for _, required := range []string{
		`data-chat-revision=`,
		"if (!nextRevision || nextRevision === currentRevision) return;",
		"if (window._chatStreamInProgress && hasActiveChatStream())",
		"function waitForChatCatchup()",
		"handleSharedLiveConnected({detail: {reconnected: true}})",
		"target: '#chat-messages'",
		"select: '#chat-messages'",
		"swap: 'morph:outerHTML'",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("expected state-aware Chat reconnect contract %q", required)
		}
	}

	if !strings.Contains(content, "var attachmentAction = document.getElementById('chat-form-primary-action');") || !strings.Contains(content, "var missedAction = document.getElementById('chat-form-primary-action');") {
		t.Fatal("attachment-bearing and missed-completion recovery must reconcile action state without remounting the composer")
	}
	if strings.Contains(content, "target: chatContainer, swap: 'outerHTML'") {
		t.Fatal("live reconnect and missed-message recovery must not replace the Chat composer root")
	}

	handlerStart := strings.Index(content, "var handleSharedLiveConnected = function(event) {")
	handlerEnd := strings.Index(content[handlerStart:], "var handleSharedChatLiveEvent = function(event) {")
	if handlerStart < 0 || handlerEnd < 0 {
		t.Fatal("expected Chat reconnect handler boundaries")
	}
	handler := content[handlerStart : handlerStart+handlerEnd]
	if strings.Contains(handler, "target: chatContainer, swap: 'outerHTML'") {
		t.Fatal("focus reconnect must not replace chat-page-root and destroy composer draft or attachments")
	}
}

func TestChatContent_ClosesChatStreamEventSourcesOnSwapAndNavigation(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}

	var buf bytes.Buffer
	err := renderChatContentForTest(agents, nil, "project-1", map[string][]models.ChatAttachment{}, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}

	content := buf.String()

	if !strings.Contains(content, "window.registerChatStreamEventSource = function(execId, eventSource)") {
		t.Error("expected chat stream EventSource registration helper")
	}
	if !strings.Contains(content, "window.unregisterChatStreamEventSource = function(execId, eventSource)") {
		t.Error("expected chat stream EventSource unregister helper")
	}
	if !strings.Contains(content, "window.closeAllChatStreamEventSources = function()") {
		t.Error("expected chat stream EventSource bulk-close helper")
	}
	if !strings.Contains(content, "document.body.addEventListener('htmx:beforeSwap', handleChatBeforeSwap);") {
		t.Error("expected beforeSwap listener for chat stream cleanup")
	}
	if !strings.Contains(content, "if (swapTarget.id === 'chat-page-root')") {
		t.Error("expected chat-page-root swap guard for stream cleanup")
	}
	if !strings.Contains(content, "window.closeAllChatStreamEventSources()") {
		t.Error("expected swap/navigation cleanup to close active chat streams")
	}
}

func TestChatContent_WindowedTranscriptUsesTopLoaderAndPrunesLivePairs(t *testing.T) {
	agents := []models.LLMConfig{{ID: "agent-1", Name: "Agent One", Provider: models.ProviderAnthropic}}
	history := []models.Execution{
		{ID: "exec-1", Status: models.ExecCompleted, PromptSent: "one", Output: "done"},
		{ID: "exec-2", Status: models.ExecRunning, PromptSent: "two"},
	}

	var buf bytes.Buffer
	err := ChatContent(agents, history, "project-1", map[string][]models.ChatAttachment{}, nil, false, true, 2).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render chat content: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `data-window-limit="2"`) {
		t.Fatal("expected chat messages container to carry visible window limit")
	}
	if !strings.Contains(content, `data-earlier-url-base="/chat?project_id=project-1&amp;limit=2"`) {
		t.Fatal("expected chat messages container to expose a base earlier URL for live-prune-created loaders")
	}
	if !strings.Contains(content, `data-earlier-loader="true"`) || !strings.Contains(content, `hx-trigger="ov:load-earlier"`) || !strings.Contains(content, `/chat?project_id=project-1&amp;before=exec-1&amp;limit=2`) {
		t.Fatal("expected scroll-triggered server-side earlier loader with oldest visible execution cursor")
	}
	if strings.Count(content, `data-execution-pair="true"`) < 2 {
		t.Fatal("expected rendered executions to be wrapped as whole-turn pairs")
	}
	if strings.Count(content, `chat-execution-pair space-y-6`) < 2 {
		t.Fatal("expected execution pairs to preserve equal vertical spacing between user and assistant bubbles")
	}
	if strings.Contains(content, `chat-execution-pair space-y-3`) {
		t.Fatal("execution-pair spacing must match the surrounding message-list spacing, not use a smaller internal gap")
	}
	if !strings.Contains(content, "window.initChatEarlierLoader") || !strings.Contains(content, "window.pruneChatExecutionWindow") {
		t.Fatal("expected shared load-earlier and live pruning helpers")
	}
	if !strings.Contains(content, "window.initChatEarlierLoader(chatMessages)") {
		t.Fatal("expected chat initial render to bind the load-earlier helper")
	}
	if !strings.Contains(content, "var pruned = false") || !strings.Contains(content, "data-earlier-url-base") || !strings.Contains(content, "container.insertBefore(loader, container.firstChild)") {
		t.Fatal("expected pruning helper to create an earlier loader when live pruning exposes older history")
	}
	if !strings.Contains(content, "pair.setAttribute('data-execution-pair', 'true')") || !strings.Contains(content, "window.pruneChatExecutionWindow(chatMessages)") {
		t.Fatal("expected live chat appends to create/prune whole execution pairs")
	}
}
