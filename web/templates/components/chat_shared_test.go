package components

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func renderedBaseMarkdownCodeHelpers(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := layout.Base("Test", nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render base Markdown helpers: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "window.markdownLineRanges = function(text)")
	end := strings.Index(content, "window.escapeRawHTMLForMarkdown = function(text, ranges)")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("base layout must install shared Markdown code helpers")
	}
	return content[start:end]
}

func TestChatRenderingPathsUseBaseSafeMarkdownRenderer(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()
	if strings.Contains(content, "function addInlineRanges(start, end)") || strings.Contains(content, "window.isInsideCode = function") {
		t.Fatal("Chat streaming script must not redefine the base Markdown code-range helpers")
	}
	if strings.Count(content, "innerHTML = window.renderChatMarkdown(") != 4 {
		t.Fatalf("every shared streaming/hydration raw HTML assignment must use the safe Markdown renderer; got %d call sites", strings.Count(content, "innerHTML = window.renderChatMarkdown("))
	}
	generated, err := os.ReadFile("chat_shared_templ.go")
	if err != nil {
		t.Fatalf("read generated Chat components: %v", err)
	}
	if strings.Count(string(generated), "innerHTML = window.renderChatMarkdown(") != 5 {
		t.Fatalf("resume, streaming, and shared hydration paths must all use the safe Markdown renderer; got %d generated call sites", strings.Count(string(generated), "innerHTML = window.renderChatMarkdown("))
	}
	for _, unsafe := range []string{
		"innerHTML = raw",
		"innerHTML = merged",
		"innerHTML = textBuffer",
		"innerHTML = pendingText",
	} {
		if strings.Contains(content, unsafe) {
			t.Fatalf("Chat/task-thread rendering contains unsafe raw HTML assignment %q", unsafe)
		}
	}
	baseHelpers := renderedBaseMarkdownCodeHelpers(t)
	for _, required := range []string{
		"window.codeRanges = function(text)",
		"var backslashCount = 0",
		"if (backslashCount % 2 === 1)",
		"if (closeIndex === -1) { openIndex++; continue; }",
		"marker === fenceChar && runLength >= fenceLength",
		"/^[ \\t]*$/.test(line.substring(runEnd))",
	} {
		if !strings.Contains(baseHelpers, required) {
			t.Fatalf("base Markdown parser missing shared renderer contract %q", required)
		}
	}
}

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

	for _, snippet := range []string{
		"window.applyMixtureProgress = function(data)",
		"document.getElementById('streaming-message-' + data.exec_id)",
		"progress.textContent = message",
		"window.hideMixtureProgress = function(execId)",
	} {
		if !strings.Contains(content, snippet) {
			t.Errorf("missing mixture progress helper snippet: %s", snippet)
		}
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

// TestChatBubbleStreaming_ThreadCompletionStaysSmooth verifies task completion
// does not refresh the task-thread fragment; queued promotion is handled by live events.
func TestChatBubbleStreaming_ThreadCompletionStaysSmooth(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreaming("assistant", "exec-id", "task-thread-messages", "task-thread-view", true).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render thread ChatBubbleStreaming: %v", err)
	}
	content := buf.String()

	if strings.Contains(content, "'#task-detail-content'") || strings.Contains(content, `"#task-detail-content"`) {
		t.Error("thread completion must not target #task-detail-content (hard-refresh UX)")
	}
	if strings.Contains(content, "?tab=chat") {
		t.Error("thread completion must not refresh via ?tab=chat full-detail swap")
	}
	if strings.Contains(content, "refreshThreadViewForPendingInputs()") || strings.Contains(content, "hasVisiblePendingThreadInputs()") {
		t.Error("thread completion must not refresh the thread fragment for pending rows")
	}
	if strings.Contains(content, "htmx.ajax('GET', '/tasks/' + taskIdMatch[1] + '/thread'") {
		t.Error("thread completion must not hard-refresh the task thread fragment")
	}
	if !strings.Contains(content, "showThreadTerminalStatus(terminalStatus)") {
		t.Error("thread completion should show terminal status dynamically with the SSE terminal status")
	}
	if !strings.Contains(content, "/thread/composer-action") {
		t.Error("thread completion should refresh the primary composer action from the server")
	}
	if !strings.Contains(content, "stopThreadPolling()") {
		t.Error("thread completion should stop thread polling dynamically when no pending rows exist")
	}
	if !strings.Contains(content, "restoreThreadPollingFallback()") {
		t.Error("transport errors should restore polling as a fallback")
	}
}

// TestChatBubbleStreaming_NeverTargetsTaskDetailContent ensures NO streaming
// bubble (thread or non-thread) issues a post-stream HTMX swap targeting
// #task-detail-content. That swap was the source of the perceived hard refresh
// after task completion.
func TestChatBubbleStreaming_CancelledDoneWithoutOutputClearsThinkingIndicator(t *testing.T) {
	var buf bytes.Buffer
	err := ChatBubbleStreaming("assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatBubbleStreaming: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "function finalizeEmptyTerminalBubble(status)") {
		t.Fatal("stream done handler must finalize empty terminal bubbles")
	}
	if !strings.Contains(content, "if (textBuffer !== '') return;") {
		t.Fatal("fresh stream terminal helper should only synthesize content for zero-output runs")
	}
	if !strings.Contains(content, "if (thinkingIndicator) thinkingIndicator.classList.add('hidden');") {
		t.Fatal("cancelled zero-output stream must hide the thinking/loading indicator")
	}
	if !strings.Contains(content, "container.classList.remove('hidden');") {
		t.Fatal("cancelled zero-output stream must reveal the terminal assistant bubble")
	}
	if !strings.Contains(content, "container.classList.add('text-error/80', 'font-medium');") {
		t.Fatal("cancelled zero-output stream must use the same error styling as refresh")
	}
	if !strings.Contains(content, "container.classList.remove('text-error/80', 'font-medium');") {
		t.Fatal("normal stream rendering must clear synthetic cancellation error styling")
	}
	if !strings.Contains(content, "container.textContent = 'Error: Cancelled';") {
		t.Fatal("cancelled zero-output stream must show the same terminal text as refresh")
	}
	if !strings.Contains(content, "finalizeEmptyTerminalBubble(terminalStatus);") {
		t.Fatal("done handler must run empty-terminal finalization")
	}
}

func TestInitThreadStreamingScript_CancelledDoneWithoutOutputClearsThinkingIndicator(t *testing.T) {
	var buf bytes.Buffer
	err := ChatStreamingResumeInitScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render _initThreadStreamingScript: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, "function finalizeEmptyTerminalBubble(status)") {
		t.Fatal("resume stream done handler must finalize empty terminal bubbles")
	}
	if !strings.Contains(content, "if (cumulativeContent !== '') return;") {
		t.Fatal("resume terminal helper should only synthesize content for zero-output runs")
	}
	if !strings.Contains(content, "if (thinkingIndicator) thinkingIndicator.classList.add('hidden');") {
		t.Fatal("resume cancelled zero-output stream must hide the thinking/loading indicator")
	}
	if !strings.Contains(content, "container.classList.remove('hidden');") {
		t.Fatal("resume cancelled zero-output stream must reveal the terminal assistant bubble")
	}
	if !strings.Contains(content, "container.classList.add('text-error/80', 'font-medium');") {
		t.Fatal("resume cancelled zero-output stream must use the same error styling as refresh")
	}
	if !strings.Contains(content, "container.classList.remove('text-error/80', 'font-medium');") {
		t.Fatal("resume normal stream rendering must clear synthetic cancellation error styling")
	}
	if !strings.Contains(content, "container.textContent = 'Error: Cancelled';") {
		t.Fatal("resume cancelled zero-output stream must show the same terminal text as refresh")
	}
	if !strings.Contains(content, "finalizeEmptyTerminalBubble(terminalStatus);") {
		t.Fatal("resume done handler must run empty-terminal finalization")
	}
}

func TestChatBubbleStreaming_HidesThinkingWrapperNotMixtureProgress(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "exec-1", "task-thread-messages", "task-thread-view", true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	html := buf.String()
	execIdx := strings.Index(html, "var execId = container.getAttribute('data-exec-id')")
	thinkingIdx := strings.Index(html, "document.getElementById('streaming-thinking-' + execId)")
	if execIdx == -1 || thinkingIdx == -1 || thinkingIdx < execIdx {
		t.Fatal("streaming bubble must initialize execId before selecting and hiding the explicit thinking wrapper")
	}
	if strings.Contains(html, "var thinkingIndicator = container.previousElementSibling") {
		t.Fatal("streaming bubble must not treat mixture-progress as the thinking indicator")
	}
}

func TestChatBubbleStreamingResume_HidesThinkingWrapperNotMixtureProgress(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatStreamingResumeInitScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render init thread streaming script: %v", err)
	}
	html := buf.String()
	execIdx := strings.Index(html, "var execId = container.getAttribute('data-exec-id')")
	thinkingIdx := strings.Index(html, "document.getElementById('streaming-thinking-resume-' + execId)")
	if execIdx == -1 || thinkingIdx == -1 || thinkingIdx < execIdx {
		t.Fatal("resume streaming must initialize execId before selecting and hiding the explicit thinking wrapper")
	}
	if strings.Contains(html, "var thinkingIndicator = !hasContent ? container.previousElementSibling : null") {
		t.Fatal("resume streaming must not treat mixture-progress as the thinking indicator")
	}
}

func TestChatBubbleStreaming_NeverTargetsTaskDetailContent(t *testing.T) {
	cases := []struct {
		name          string
		messagesID    string
		pauseTargetID string
		isThread      bool
	}{
		{"thread", "task-thread-messages", "task-thread-view", true},
		{"chat", "chat-messages", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := ChatBubbleStreaming("assistant", "exec-id", tc.messagesID, tc.pauseTargetID, tc.isThread).Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("Failed to render ChatBubbleStreaming: %v", err)
			}
			content := buf.String()
			if strings.Contains(content, "'#task-detail-content'") || strings.Contains(content, `"#task-detail-content"`) {
				t.Error("streaming bubble must not target #task-detail-content for any post-stream refresh (causes hard-refresh UX)")
			}
			if strings.Contains(content, "?tab=chat") {
				t.Error("streaming bubble must not refresh via ?tab=chat full-detail swap")
			}
		})
	}
}

// TestInitThreadStreamingScript_CompletionStaysSmooth verifies the resume SSE
// handler also avoids fragment refreshes; live events append promoted turns.
func TestInitThreadStreamingScript_CompletionStaysSmooth(t *testing.T) {
	var buf bytes.Buffer
	err := ChatStreamingResumeInitScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render _initThreadStreamingScript: %v", err)
	}
	content := buf.String()
	if strings.Contains(content, "'#task-detail-content'") || strings.Contains(content, `"#task-detail-content"`) {
		t.Error("resume thread stream script must not target #task-detail-content (hard-refresh UX)")
	}
	if strings.Contains(content, "?tab=chat") {
		t.Error("resume thread stream script must not refresh via ?tab=chat full-detail swap")
	}
	if strings.Contains(content, "refreshThreadViewForPendingInputs()") || strings.Contains(content, "hasVisiblePendingThreadInputs()") {
		t.Error("resume completion must not refresh the thread fragment for pending rows")
	}
	if strings.Contains(content, "htmx.ajax('GET', '/tasks/' + taskIdMatch[1] + '/thread'") {
		t.Error("resume completion must not hard-refresh the task thread fragment")
	}
	if !strings.Contains(content, "showThreadTerminalStatus(terminalStatus)") {
		t.Error("resume completion should show terminal status dynamically with the SSE terminal status")
	}
	if !strings.Contains(content, "/thread/composer-action") {
		t.Error("resume completion should refresh the primary composer action from the server")
	}
	if !strings.Contains(content, "restoreThreadPollingFallback()") {
		t.Error("resume transport errors should restore polling as a fallback")
	}
	if !strings.Contains(content, "var largeStreamRenderThreshold = 100 * 1024") ||
		!strings.Contains(content, "var largeStreamRenderInterval = 250") {
		t.Error("resume streaming must throttle full rerenders for large accumulated responses")
	}
	if !strings.Contains(content, "if (renderDelayTimer !== null)") {
		t.Error("resume completion must cancel a pending delayed render before forcing the final render")
	}
	if strings.Count(content, "flushCumulativeContent();") != 2 {
		t.Error("both resume-stream error paths must flush and cancel pending delayed output")
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
	if !strings.Contains(content, "var largeStreamRenderThreshold = 100 * 1024") ||
		!strings.Contains(content, "var largeStreamRenderInterval = 250") {
		t.Error("Missing size-aware throttling for large streaming responses")
	}
	if strings.Contains(content, "container.setAttribute('data-raw-content', window.normalizeTranscriptMarkers ? window.normalizeTranscriptMarkers(textBuffer) : textBuffer)") {
		t.Error("Streaming onmessage must not normalize and copy the entire accumulated response before the scheduled render")
	}
	if strings.Count(content, "flushBufferedOutput();") != 2 {
		t.Error("both fresh-stream error paths must flush and cancel pending delayed output")
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

func TestChatBubbleStreamingFlushesDelayedOutputBeforeError(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the streaming scheduler")
	}
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	content := buf.String()
	start := strings.LastIndex(content, `<script type="text/javascript">`)
	end := strings.LastIndex(content, "</script>")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("streaming script is missing")
	}
	source := content[start+len(`<script type="text/javascript">`) : end]
	harness := `
let now = 1000, renders = 0, cleared = 0;
const frames = [], timers = new Map(); let nextTimer = 1;
global.requestAnimationFrame = fn => { frames.push(fn); return frames.length; };
global.setTimeout = (fn, delay) => { const id = nextTimer++; timers.set(id, { fn, delay }); return id; };
global.clearTimeout = id => { if (timers.delete(id)) cleared++; };
Date.now = () => now;
function classList() { return { add() {}, remove() {}, contains() { return false; } }; }
const appended = [];
const container = {
  attrs: { 'data-exec-id': 'exec-id', 'data-messages-container': 'chat-messages', 'data-pause-polling-target': '', 'data-is-thread': 'false' },
  classList: classList(), getAttribute(name) { return this.attrs[name] || ''; },
  setAttribute(name, value) { this.attrs[name] = value; },
  appendChild(node) { appended.push(node); }, closest() { return null; }
};
const streamingDots = { classList: classList(), previousElementSibling: container };
const thinking = { classList: classList() };
const messages = {};
global.document = {
  currentScript: { previousElementSibling: streamingDots },
  getElementById(id) { if (id === 'streaming-thinking-exec-id') return thinking; if (id === 'chat-messages') return messages; return null; },
  createTextNode(text) { return { textContent: text }; }
};
class FakeEventSource {
  constructor() { this.listeners = {}; global.source = this; }
  addEventListener(name, fn) { this.listeners[name] = fn; }
  close() {}
}
global.EventSource = FakeEventSource;
global.window = {
  normalizeTranscriptMarkers: text => text.replace(/alias/g, 'normalized-alias'),
  renderStreamingContent(container, text) { renders++; container.rendered = text; },
  resolveScrollTracker() { return { resetOnUserSend() {}, shouldAutoScroll() { return false; } }; },
  registerChatStreamEventSource() {}, unregisterChatStreamEventSource() {}, hideMixtureProgress() {}
};
`
	assertions := `
source.onmessage({ data: 'x'.repeat(100 * 1024) });
if (frames.length !== 1) process.exit(1);
frames.shift()();
if (renders !== 1) process.exit(2);
source.onmessage({ data: 'alias' });
if (timers.size !== 1 || renders !== 1) process.exit(3);
source.listeners.error({ data: 'boom' });
if (renders !== 2 || timers.size !== 0 || cleared !== 1) process.exit(4);
if (!appended.length || appended[appended.length - 1].textContent.indexOf('Error: boom') === -1) process.exit(5);
source.onerror({ data: 'boom' });
if (renders !== 2 || appended[appended.length - 1].textContent.indexOf('Error: boom') === -1) process.exit(6);
`
	if output, err := exec.Command(node, "-e", harness+source+assertions).CombinedOutput(); err != nil {
		t.Fatalf("streaming scheduler failed to flush delayed output before error: %v\n%s", err, output)
	}
}

func TestChatBubbleStreamingFifthRetryIsNotFailedByDuplicateErrorCallback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the streaming retry scheduler")
	}
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	content := buf.String()
	start := strings.LastIndex(content, `<script type="text/javascript">`)
	end := strings.LastIndex(content, "</script>")
	if start == -1 || end <= start {
		t.Fatal("streaming script is missing")
	}
	source := content[start+len(`<script type="text/javascript">`) : end]
	harness := `
const timers = new Map(); let nextTimer = 1;
global.requestAnimationFrame = fn => fn();
global.setTimeout = (fn, delay) => { const id = nextTimer++; timers.set(id, { fn, delay }); return id; };
global.clearTimeout = id => timers.delete(id);
function classList() { return { add() {}, remove() {}, contains() { return false; } }; }
const appended = [], sources = [];
const container = {
  attrs: { 'data-exec-id': 'exec-id', 'data-messages-container': 'chat-messages', 'data-pause-polling-target': '', 'data-is-thread': 'false' },
  classList: classList(), getAttribute(name) { return this.attrs[name] || ''; }, setAttribute(name, value) { this.attrs[name] = value; },
  appendChild(node) { appended.push(node); }, closest() { return null; }
};
const streamingDots = { classList: classList(), previousElementSibling: container };
const thinking = { classList: classList() };
global.document = {
  currentScript: { previousElementSibling: streamingDots },
  getElementById(id) { if (id === 'streaming-thinking-exec-id') return thinking; if (id === 'chat-messages') return {}; return null; },
  createTextNode(text) { return { textContent: text }; }
};
class FakeEventSource {
  constructor() { this.listeners = {}; sources.push(this); }
  addEventListener(name, fn) { this.listeners[name] = fn; }
  close() {}
}
global.EventSource = FakeEventSource;
global.window = {
  renderStreamingContent() {},
  resolveScrollTracker() { return { resetOnUserSend() {}, shouldAutoScroll() { return false; } }; },
  registerChatStreamEventSource() {}, unregisterChatStreamEventSource() {}, hideMixtureProgress() {}
};
`
	assertions := `
for (let attempt = 0; attempt < 5; attempt++) {
  const active = sources[sources.length - 1];
  active.listeners.error({ data: 'execution not found' });
  active.onerror({ data: 'execution not found' });
  if (timers.size !== 1 || appended.length !== 0) process.exit(10 + attempt);
  const entry = timers.entries().next().value;
  timers.delete(entry[0]);
  entry[1].fn();
}
if (sources.length !== 6 || appended.length !== 0) process.exit(20);
const exhausted = sources[sources.length - 1];
exhausted.listeners.error({ data: 'execution not found' });
exhausted.onerror({ data: 'execution not found' });
if (timers.size !== 0 || appended.length !== 1 || appended[0].textContent.indexOf('execution not found') === -1) process.exit(21);
`
	if output, err := exec.Command(node, "-e", harness+source+assertions).CombinedOutput(); err != nil {
		t.Fatalf("fifth streaming retry was terminated by the duplicate error callback: %v\n%s", err, output)
	}
}

func TestChatBubbleStreamingTerminalRenderRejectionFallsBackToCompleteText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the terminal render fallback")
	}
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	content := buf.String()
	start := strings.LastIndex(content, `<script type="text/javascript">`)
	end := strings.LastIndex(content, "</script>")
	if start == -1 || end <= start {
		t.Fatal("streaming script is missing")
	}
	source := content[start+len(`<script type="text/javascript">`) : end]
	harness := `
let renders = 0;
global.requestAnimationFrame = fn => fn();
function classList() { return { add() {}, remove() {}, contains() { return false; } }; }
const container = {
  attrs: { 'data-exec-id': 'exec-id', 'data-messages-container': 'chat-messages', 'data-pause-polling-target': '', 'data-is-thread': 'false' },
  classList: classList(), getAttribute(name) { return this.attrs[name] || ''; }, setAttribute(name, value) { this.attrs[name] = value; },
  appendChild() {}, closest() { return null; }, textContent: ''
};
const streamingDots = { classList: classList(), previousElementSibling: container };
const thinking = { classList: classList() };
global.document = {
  currentScript: { previousElementSibling: streamingDots },
  getElementById(id) { if (id === 'streaming-thinking-exec-id') return thinking; if (id === 'chat-messages') return {}; return null; },
  createTextNode(text) { return { textContent: text }; }
};
class FakeEventSource {
  constructor() { this.listeners = {}; global.source = this; this.closed = false; }
  addEventListener(name, fn) { this.listeners[name] = fn; }
  close() { this.closed = true; }
}
global.EventSource = FakeEventSource;
global.window = {
  normalizeTranscriptMarkers() { throw new Error('normalizer must not run in fallback'); },
  renderStreamingContent() { renders++; return Promise.reject(new Error('renderer failed')); },
  resolveScrollTracker() { return { resetOnUserSend() {}, shouldAutoScroll() { return false; } }; },
  registerChatStreamEventSource() {}, unregisterChatStreamEventSource() {}, hideMixtureProgress() {}
};
`
	assertions := `
source.onmessage({ data: 'complete final response' });
source.listeners.done({ data: 'completed' });
setImmediate(function() {
  if (!source.closed || renders < 2) process.exit(30);
  if (container.textContent !== 'complete final response' || container.attrs['data-raw-content'] !== 'complete final response') process.exit(31);
});
`
	if output, err := exec.Command(node, "-e", harness+source+assertions).CombinedOutput(); err != nil {
		t.Fatalf("terminal render rejection did not preserve the complete response: %v\n%s", err, output)
	}
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

	// Verify initial byte length attribute for offset-aware reconnects
	if !strings.Contains(content, "data-initial-byte-length") {
		t.Error("Missing data-initial-byte-length attribute")
	}

	// Verify messages container reference
	if !strings.Contains(content, `data-messages-container="chat-messages"`) {
		t.Error("Missing data-messages-container attribute")
	}
	if !strings.Contains(content, "var liveRenderer = window.renderLiveChatContent || window.renderStreamingContent;") ||
		!strings.Contains(content, "var renderPromise = liveRenderer(el, raw);") {
		t.Fatal("streaming resume bubble must render through the live coordinator")
	}
	if strings.Contains(content, "window.renderStreamingContent(el, raw);") {
		t.Fatal("streaming resume bubble must not bypass live-render ownership")
	}

	t.Logf("ChatBubbleStreamingResume scroll behavior verified (%d bytes)", len(content))
}

func TestTaskThreadView_ResumeHydrationUsesLiveCoordinator(t *testing.T) {
	task := &models.Task{ID: "resume-owner-task", ProjectID: "p1", Status: models.StatusRunning, Category: models.CategoryActive}
	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task thread view: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, `var resumeContainers = document.querySelectorAll('[data-streaming-resume="true"]')`)
	if start == -1 {
		t.Fatal("task thread resume hydration block is missing")
	}
	end := strings.Index(content[start:], "if (window._initThreadStreaming) window._initThreadStreaming();")
	if end == -1 {
		t.Fatal("task thread resume hydration terminator is missing")
	}
	section := content[start : start+end]
	if !strings.Contains(section, "var liveRenderer = window.renderLiveChatContent || window.renderStreamingContent;") ||
		!strings.Contains(section, "var renderPromise = liveRenderer(c, raw);") {
		t.Fatal("task thread resume hydration must use the live coordinator")
	}
	if !strings.Contains(section, "hasRenderedContent && c._renderedRevision === revision") ||
		!strings.Contains(section, "!hasCurrentRenderedContent && !c._activeLiveChatRender") {
		t.Fatal("task thread resume hydration must only reuse content committed for the current raw snapshot")
	}
	if strings.Contains(section, "window.renderStreamingContent(c, raw);") {
		t.Fatal("task thread resume hydration must not call the raw renderer directly")
	}
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
	if !strings.Contains(content, "window.scheduleChatContentRender = function(container, textBuffer, yieldBetweenBatches)") ||
		!strings.Contains(content, "if (window._chatContentRenderActive >= 1) return") ||
		!strings.Contains(content, "while (next && (next.container._scheduledChatRender !== next || !next.container.isConnected))") {
		t.Error("completed transcript hydration must serialize expensive renders")
	}
	if !strings.Contains(content, "window.scheduleChatElementRender(el, raw);") {
		t.Error("completed raw assistant messages must use the bounded render scheduler")
	}
	if !strings.Contains(content, "window.renderStreamingContent(el, raw);") {
		t.Error("cleanAssistantMessages must rebuild tool/thinking cards from raw content")
	}
	if !strings.Contains(content, "window.dedupTaskSummaries = function(text)") ||
		!strings.Contains(content, "text = window.dedupTaskSummaries(text);") {
		t.Error("hard-refresh hydration must use shared Markdown-aware task-summary deduplication")
	}
}

func TestCompletedBubbleUsesSharedHydratorWithoutInlineScript(t *testing.T) {
	raw := strings.Repeat("x", 128)
	var buf bytes.Buffer
	if err := ChatBubble("Assistant", raw).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render completed bubble: %v", err)
	}
	content := buf.String()
	if strings.Contains(content, "<script") || strings.Contains(content, "document.currentScript") || strings.Contains(content, "setInterval") {
		t.Fatalf("completed assistant bubble must not include per-bubble render script; got %d bytes", len(content))
	}
	if !strings.Contains(content, `class="chat-stream-content"`) || !strings.Contains(content, `data-raw-content="`+raw+`"`) || !strings.Contains(content, `data-raw-revision="`) {
		t.Fatalf("completed assistant bubble must expose compact raw-content markup for the shared hydrator:\n%s", content)
	}

	fixed := len(content) - len(raw)
	if fixed >= 300 {
		t.Fatalf("completed assistant bubble fixed markup overhead = %d bytes, want < 300", fixed)
	}
}

func TestCompletedErrorBubbleUsesSharedHydratorWithoutInlineScript(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleError("Assistant", "failed", "partial output").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render error bubble: %v", err)
	}
	content := buf.String()
	if strings.Contains(content, "<script") || strings.Contains(content, "document.currentScript") || strings.Contains(content, "setInterval") {
		t.Fatalf("error assistant bubble must not include per-bubble render script; got %d bytes", len(content))
	}
	if !strings.Contains(content, `Error: failed`) || !strings.Contains(content, `class="chat-stream-content"`) || !strings.Contains(content, `data-raw-content="partial output"`) {
		t.Fatalf("error assistant bubble must keep label and compact raw partial output markup:\n%s", content)
	}
}

func TestCompletedBubbleSharedHydrationInChrome(t *testing.T) {
	chrome := testChromePath(t)
	var bubble bytes.Buffer
	if err := ChatBubble("Assistant", "# Rendered\n\n```go\nfmt.Println(\"hi\")\n```").Render(context.Background(), &bubble); err != nil {
		t.Fatalf("render completed bubble: %v", err)
	}
	var chatScript bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &chatScript); err != nil {
		t.Fatalf("render shared chat script: %v", err)
	}
	html := `<!doctype html><html><head><meta charset="utf-8"></head><body><main id="fixture-root"><div id="chat-messages">` + bubble.String() + `</div></main><script>
	window.requestAnimationFrame = function(callback) { return setTimeout(callback, 0); };
	window.cancelAnimationFrame = clearTimeout;
	window.renderChatMarkdown = function(text) {
	  function esc(value) { return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
	  var fence = String.fromCharCode(96, 96, 96);
	  return esc(text).replace(new RegExp(fence + 'go\\n([\\s\\S]*?)\\n' + fence, 'g'), '<pre><code class="language-go">$1</code></pre>').replace(/^# (.*)$/m, '<h1>$1</h1>');
	};
	window.addCodeCopyButtons = function(container) {
	  container.querySelectorAll('pre').forEach(function(pre) {
	    var button = document.createElement('button');
	    button.setAttribute('data-code-copy-button', 'true');
	    button.textContent = 'Copy';
	    pre.appendChild(button);
	  });
	};
	window.renderChatMarkdownLargeFallback = function(text) {
	  var div = document.createElement('div');
	  div.className = 'chat-markdown';
	  div.innerHTML = window.renderChatMarkdown(text);
	  window.addCodeCopyButtons(div);
	  return div;
	};
	</script>` + chatScript.String() + `<script>
	window.addEventListener('DOMContentLoaded', function() {
	  var root = document.getElementById('fixture-root');
	  var messages = document.getElementById('chat-messages');
	  function fail(message) { root.setAttribute('data-test-result', 'fail'); root.setAttribute('data-test-error', message); }
	  if ((messages.textContent || '').indexOf('# Rendered') !== -1) return fail('raw markdown was visible before shared hydration');
	  Promise.resolve(window.cleanAssistantMessages(messages)).then(function() {
	    if (!messages.querySelector('.chat-markdown h1')) return fail('markdown heading did not hydrate');
	    if (!messages.querySelector('pre code.language-go')) return fail('code block did not hydrate');
	    if (!messages.querySelector('[data-code-copy-button="true"]')) return fail('copy button was not attached');
	    if ((messages.textContent || '').indexOf(String.fromCharCode(96, 96, 96)) !== -1) return fail('raw fenced markdown remained visible after hydration');
	    root.setAttribute('data-test-result', 'pass');
	  }).catch(function(error) { fail(String(error && error.stack || error)); });
	});
	</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chrome-completed-bubble.html")
	stderrPath := filepath.Join(t.TempDir(), "chrome-completed-bubble.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout: %v", err)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--run-all-compositor-stages-before-draw", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome-completed-bubble-profile"), "--virtual-time-budget=5000", "--dump-dom", server.URL)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome completed-bubble fixture: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopTestBrowserProcess(cmd)
	if !strings.Contains(result, `data-test-result="pass"`) {
		state := regexp.MustCompile(`<main id="fixture-root"[^>]*>`).FindString(result)
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("completed-bubble browser hydration fixture failed: %s\nDOM: %s\nChrome stderr: %s", state, result, stderr)
	}
}

func TestChatContentRenderSchedulerSerializesAndRecovers(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat scheduler: %v", err)
	}
	content := buf.String()
	for _, forbidden := range []string{"dataset.renderingRaw", "dataset.cleanedRaw", "dataset.liveRenderedRaw"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("full transcript render state must not be copied into a data attribute: %s", forbidden)
		}
	}
	start := strings.Index(content, "if (!window._chatContentRenderQueue)")
	end := strings.Index(content[start:], "window.renderStreamingContent = function(container, textBuffer, yieldBetweenBatches)")
	if start == -1 || end == -1 {
		t.Fatal("chat render scheduler source is missing")
	}
	scheduler := content[start : start+end]
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the chat render scheduler")
	}
	script := "global.window = { _chatContentRenderTimeoutMS: 50 }; global.document = { createTextNode: function(text) { return { text: text }; } };\n" +
		"window.renderChatMarkdownLargeFallback = function(text) { return { safe: text }; };\n" +
		"function container(connected, raw) { return { isConnected: connected, hasRaw: raw !== undefined, raw: raw || '', attributes: {}, dataset: new Proxy({}, { set: function() { throw new Error('render cache must not create data attributes'); } }), replacements: [], hasAttribute: function(name) { return name === 'data-raw-content' && this.hasRaw; }, getAttribute: function(name) { if (name === 'data-raw-content' && this.hasRaw) return this.raw; return this.attributes[name] || null; }, setAttribute: function(name, value) { this.attributes[name] = String(value); }, removeAttribute: function(name) { delete this.attributes[name]; }, replaceChildren: function(value) { this.replacements.push(value); } }; }\n" +
		scheduler + "\n" +
		"const delay = ms => new Promise(resolve => setTimeout(resolve, ms));\n" +
		"(async function() {\n" +
		"  const calls = [], controls = []; window.renderStreamingContent = function(c, text) { calls.push(text); return new Promise(function(resolve, reject) { controls.push({ resolve: resolve, reject: reject }); }); };\n" +
		"  const first = container(true), second = container(true); const p1 = window.scheduleChatContentRender(first, 'first'), p2 = window.scheduleChatContentRender(second, 'second');\n" +
		"  await delay(10); if (calls.join(',') !== 'first') throw new Error('renders were not serialized'); controls[0].resolve(true);\n" +
		"  await delay(10); if (calls.join(',') !== 'first,second') throw new Error('second render did not drain'); controls[1].reject(new Error('failed'));\n" +
		"  const initial = await Promise.all([p1, p2]); if (!initial[0] || initial[1] || !second.replacements[0] || second.replacements[0].safe !== 'second') throw new Error('rejection did not use safe fallback');\n" +
		"  window.renderStreamingContent = function(c, text) { if (text === 'hydrate-fail') return Promise.reject(new Error('hydrate failed')); return Promise.resolve(true); };\n" +
		"  const hydrated = container(true, 'hydrate-ok'); if (!await window.scheduleChatElementRender(hydrated, 'hydrate-ok') || hydrated._renderedRevision !== 'hydrate-ok' || hydrated._renderingRevision) throw new Error('successful hydration signature was not committed');\n" +
		"  const failedHydration = container(true, 'hydrate-fail'); if (await window.scheduleChatElementRender(failedHydration, 'hydrate-fail') || failedHydration._renderedRevision || failedHydration._renderingRevision) throw new Error('failed hydration signature was retained');\n" +
		"  const orderingOwner = container(true, 'snapshot-a'); if (!await window.renderLiveChatContent(orderingOwner, 'snapshot-a') || orderingOwner._renderedRevision !== 'snapshot-a') throw new Error('live snapshot was not authoritative'); orderingOwner.raw = 'snapshot-b'; if (!await window.scheduleChatElementRender(orderingOwner, 'snapshot-b') || orderingOwner._renderedRevision !== 'snapshot-b') throw new Error('scheduled snapshot did not replace live snapshot'); orderingOwner.raw = 'snapshot-a'; if (orderingOwner._renderedRevision === orderingOwner.raw) throw new Error('A-B-A ordering falsely treated stale DOM as current');\n" +
		"  window._chatLiveRenderQuietMS = 5; let liveResolve = null, completedAttempts = 0; window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'completed-hung' && ++completedAttempts === 1) return new Promise(function() {}); if (text === 'live-hung') return new Promise(function(resolve) { liveResolve = resolve; }); return Promise.resolve(true); };\n" +
		"  const completedDuringLive = container(true); const completedResult = window.scheduleChatContentRender(completedDuringLive, 'completed-hung'); await delay(10); const liveResult = window.renderLiveChatContent(container(true), 'live-now'); if (!await liveResult || await completedResult || completedDuringLive.replacements.length !== 0) throw new Error('live render dumped interrupted completed output into the DOM'); await delay(15); if (completedAttempts !== 2) throw new Error('interrupted completed render was not requeued after live work');\n" +
		"  const livePending = window.renderLiveChatContent(container(true), 'live-hung'); await delay(1); const queuedAfterLive = window.scheduleChatContentRender(container(true), 'queued-after-live'); await delay(2); if (calls.indexOf('queued-after-live') !== -1) throw new Error('completed render ran concurrently with live render'); liveResolve(true); await livePending; if (!await queuedAfterLive || calls.indexOf('queued-after-live') === -1) throw new Error('completed queue did not resume after live render');\n" +
		"  let staleRawAttempts = 0; window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'stale-raw') { staleRawAttempts++; return new Promise(function() {}); } return Promise.resolve(true); }; const changedRawOwner = container(true, 'stale-raw'); const changedRawResult = window.scheduleChatElementRender(changedRawOwner, 'stale-raw'); await delay(10); changedRawOwner.raw = 'new-raw'; if (!await window.renderLiveChatContent(container(true), 'live-after-raw-change') || await changedRawResult) throw new Error('raw-change interruption did not settle'); await delay(10); if (staleRawAttempts !== 1) throw new Error('interrupted stale raw content was requeued over newer content');\n" +
		"  window._chatLiveRenderTimeoutMS = 5; window.renderStreamingContent = function() { return new Promise(function() {}); }; if (await window.renderLiveChatContent(container(true), 'live-timeout') || window._liveChatRenderActive !== 0) throw new Error('live timeout did not settle and release renderer');\n" +
		"  window._chatLiveRenderTimeoutMS = 50; window._chatRenderMaxLiveDeferralMS = 5; window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'live-deferral-cap') return new Promise(function() {}); return Promise.resolve(true); }; const cappedLive = window.renderLiveChatContent(container(true), 'live-deferral-cap'); const cappedQueued = window.scheduleChatContentRender(container(true), 'queued-after-cap'); if (!await cappedQueued || await cappedLive || calls.indexOf('queued-after-cap') === -1) throw new Error('active live render exceeded completed-render deferral cap');\n" +
		"  let staleLiveSettled = false; window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'live-with-stale-queue') return new Promise(function() {}); return Promise.resolve(true); }; const staleLive = window.renderLiveChatContent(container(true), 'live-with-stale-queue').then(function(result) { staleLiveSettled = true; return result; }); const staleQueued = window.scheduleChatContentRender(container(false), 'stale-during-live'); if (await staleQueued) throw new Error('disconnected queued render succeeded'); await delay(10); if (staleLiveSettled) throw new Error('stale queue entry cancelled active live render'); window.cancelLiveChatRenders(); if (await staleLive) throw new Error('cancelled stale-test live render succeeded');\n" +
		"  let markdownFallbackRan = false, markdownWorkerTerminated = false; const markdownOwner = container(true); window.renderStreamingContent = function(c) { c._markdownWorkerState = { cancelled: false, finished: false, fallbackTimer: setTimeout(function() { markdownFallbackRan = true; }, 5), worker: { terminate: function() { markdownWorkerTerminated = true; } }, resolve: function() {} }; return new Promise(function() {}); }; const cancelledMarkdown = window.renderLiveChatContent(markdownOwner, 'cancel-markdown'); await delay(1); window.cancelLiveChatRenders(); if (await cancelledMarkdown) throw new Error('cancelled Markdown render succeeded'); await delay(10); if (markdownFallbackRan || !markdownWorkerTerminated || markdownOwner._markdownWorkerState !== null) throw new Error('live cancellation left Markdown fallback work active');\n" +
		"  let replacementResolve = null, replacementSettled = false; const replacementOwner = container(true); window._chatLiveRenderTimeoutMS = 5; window.renderStreamingContent = function(c, text) { if (text === 'replacement-new') return new Promise(function(resolve) { replacementResolve = resolve; }); return new Promise(function() {}); }; const replacedLive = window.renderLiveChatContent(replacementOwner, 'replacement-old'); await delay(1); window._chatLiveRenderTimeoutMS = 50; const replacementLive = window.renderLiveChatContent(replacementOwner, 'replacement-new').then(function(result) { replacementSettled = true; return result; }); if (await replacedLive) throw new Error('superseded same-container render succeeded'); await delay(10); if (replacementSettled || window._liveChatRenderActive !== 1) throw new Error('old live timeout cancelled the replacement render'); replacementResolve(true); if (!await replacementLive || replacementOwner._activeLiveChatRender !== null || replacementOwner._renderedRevision !== 'replacement-new') throw new Error('replacement render did not commit its owned raw signature');\n" +
		"  let timeoutFallbackRan = false, timeoutWorkerTerminated = false; const timeoutMarkdownOwner = container(true); window._chatLiveRenderTimeoutMS = 5; window.renderStreamingContent = function(c) { c._markdownWorkerState = { cancelled: false, finished: false, fallbackTimer: setTimeout(function() { timeoutFallbackRan = true; }, 15), worker: { terminate: function() { timeoutWorkerTerminated = true; } }, resolve: function() {} }; return new Promise(function() {}); }; if (await window.renderLiveChatContent(timeoutMarkdownOwner, 'timeout-markdown')) throw new Error('timed out Markdown render succeeded'); await delay(20); if (timeoutFallbackRan || !timeoutWorkerTerminated || timeoutMarkdownOwner._markdownWorkerState !== null || timeoutMarkdownOwner._activeLiveChatRender !== null) throw new Error('live timeout left owned Markdown work active');\n" +
		"  window._chatRenderMaxLiveDeferralMS = 5; window._chatLiveRenderQuietUntil = Date.now() + 1000; window.renderStreamingContent = function(c, text) { calls.push(text); return Promise.resolve(true); }; if (!await window.scheduleChatContentRender(container(true), 'max-deferral') || calls.indexOf('max-deferral') === -1) throw new Error('live quiet period starved completed render');\n" +
		"  window._chatContentRenderTimeoutMS = 50; window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'old-active') return new Promise(function() {}); return Promise.resolve(true); };\n" +
		"  const oldActive = container(true), navigated = container(true); const oldResult = window.scheduleChatContentRender(oldActive, 'old-active'); await delay(10); oldActive.isConnected = false; const navigatedResult = window.scheduleChatContentRender(navigated, 'navigated'); const navigation = await Promise.all([oldResult, navigatedResult]); if (navigation[0] || !navigation[1] || calls.indexOf('navigated') === -1) throw new Error('navigation did not cancel disconnected active render');\n" +
		"  window._chatContentRenderTimeoutMS = 5;\n" +
		"  window.renderStreamingContent = function(c, text) { calls.push(text); if (text === 'hung') return new Promise(function() {}); return Promise.resolve(true); };\n" +
		"  const hung = container(true), after = container(true); const hungResult = window.scheduleChatContentRender(hung, 'hung'); const afterResult = window.scheduleChatContentRender(after, 'after');\n" +
		"  const recovered = await Promise.all([hungResult, afterResult]); if (recovered[0] || !recovered[1] || !hung.replacements[0] || calls.indexOf('after') === -1) throw new Error('timeout did not release queue');\n" +
		"  const disconnected = container(false); if (await window.scheduleChatContentRender(disconnected, 'gone')) throw new Error('disconnected render succeeded'); if (calls.indexOf('gone') !== -1) throw new Error('disconnected render ran');\n" +
		"})().catch(function(err) { console.error(err && err.stack || err); process.exit(1); });\n"
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("chat render scheduler failed: %v\n%s", err, output)
	}
}

func TestChatContentRevisionIsCompactAndContentSensitive(t *testing.T) {
	large := strings.Repeat("tool output\n", 100000)
	revision := chatContentRevision(large)
	if len(revision) > 40 {
		t.Fatalf("revision must stay compact, got %d bytes", len(revision))
	}
	if revision != chatContentRevision(large) {
		t.Fatal("revision must be stable for identical content")
	}
	if revision == chatContentRevision(large+"changed") {
		t.Fatal("revision must change with transcript content")
	}
}

func TestChatExecutionPairPreservesTerminalTranscriptDuringMorph(t *testing.T) {
	terminal := models.Execution{ID: "terminal-1", Status: models.ExecCompleted, Output: "large output"}
	var terminalHTML bytes.Buffer
	if err := ChatExecutionPair(terminal, nil, []models.Execution{terminal}, 0, false, nil, "messages", "thread", "project-a").Render(context.Background(), &terminalHTML); err != nil {
		t.Fatalf("render terminal execution pair: %v", err)
	}
	if !strings.Contains(terminalHTML.String(), `id="chat-execution-terminal-1"`) || !strings.Contains(terminalHTML.String(), `hx-preserve="true"`) {
		t.Fatal("terminal execution pair must be preserved across polling morphs")
	}

	running := models.Execution{ID: "running-1", Status: models.ExecRunning}
	var runningHTML bytes.Buffer
	if err := ChatExecutionPair(running, nil, []models.Execution{running}, 0, false, nil, "messages", "thread", "project-a").Render(context.Background(), &runningHTML); err != nil {
		t.Fatalf("render running execution pair: %v", err)
	}
	if strings.Contains(runningHTML.String(), `hx-preserve="true"`) {
		t.Fatal("running execution pair must remain morphable for polling fallback")
	}

	var followupHTML bytes.Buffer
	if err := ChatFollowupResponse("follow up", "running-1", "messages", "thread", true, nil, "project-a").Render(context.Background(), &followupHTML); err != nil {
		t.Fatalf("render live follow-up pair: %v", err)
	}
	if !strings.Contains(followupHTML.String(), `id="chat-execution-running-1"`) {
		t.Fatal("live follow-up pair must use the same stable execution ID as polling markup")
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

func TestChatInputForm_SendButtonUsesSharedPrimaryActionColorClass(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{
		FormID:       "chat-form",
		InputID:      "message-input",
		PostEndpoint: "/chat/send",
		TargetID:     "chat-messages",
	}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		`class="btn btn-primary btn-sm chat-send-button rounded-full`,
		`(kind === 'stop' ? '' : ' chat-send-button')`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("initial and dynamically rebuilt send buttons must consume the shared primary action color class %q, got %s", want, body)
		}
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
	if !strings.Contains(content, "function currentSubmitButton()") {
		t.Fatal("chat input script must resolve the current submit button for click-path parity")
	}
	if !strings.Contains(content, "form.addEventListener('click', function(e)") {
		t.Fatal("chat input script must delegate submit button clicks after OOB action swaps")
	}
	if !strings.Contains(content, `e.target.closest('[id="' + formId + '-primary-action"] button')`) {
		t.Fatal("chat input script must detect the current primary action from delegated clicks")
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
	if !strings.Contains(content, "function currentSubmitButton()") {
		t.Fatal("enter key path must resolve the current submit button after OOB action swaps")
	}
	if strings.Contains(content, "var submitBtn = form.querySelector('button[type=\"submit\"]');") {
		t.Fatal("enter key path must not capture the original submit button before OOB action swaps")
	}
	safeSubmitIdx := strings.Index(content, "function safeSubmit()")
	currentBtnIdx := strings.Index(content, "var submitBtn = currentSubmitButton();")
	requestSubmitIdx := strings.Index(content, "form.requestSubmit(submitBtn || undefined);")
	if safeSubmitIdx == -1 || currentBtnIdx == -1 || requestSubmitIdx == -1 || !(safeSubmitIdx < currentBtnIdx && currentBtnIdx < requestSubmitIdx) {
		t.Fatal("safeSubmit must resolve the current submit button immediately before requestSubmit")
	}
}

func TestChatInputForm_MessageHistoryNavigationScript(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:       "chat-form",
		InputID:      "message-input",
		PostEndpoint: "/chat/send",
		TargetID:     "chat-messages",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	required := []string{
		`data-message-history-key="openvibely-chat-message-history"`,
		"var messageHistoryLimit = 50;",
		"function loadMessageHistory()",
		"localStorage.getItem(messageHistoryStorageKey)",
		"JSON.parse(raw)",
		"function saveMessageHistory(entries)",
		"localStorage.setItem(messageHistoryStorageKey, JSON.stringify(entries.slice(-messageHistoryLimit)))",
		"function rememberSubmittedMessage(message)",
		"messageHistoryEntries.push(message);",
		"function handleMessageHistoryKeydown(e)",
		"if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown' && e.key !== 'Escape') return false;",
		"if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && messageInput.value !== '' && !isAtFirstLineStart(messageInput)) return false;",
		"if (e.key === 'ArrowDown' && messageHistoryIndex === -1) return false;",
		"if (messageHistoryIndex === -1) messageHistoryDraft = messageInput.value;",
		"messageHistoryEntries[messageHistoryEntries.length - 1 - messageHistoryIndex]",
		"setMessageInputFromHistory(messageHistoryDraft);",
		"resetMessageHistoryNavigation();",
		"rememberSubmittedMessage(submittedMessage);",
		"if (!event.detail || event.detail.elt !== form) return;",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Fatalf("message history script missing %q", r)
		}
	}
}

func TestChatInputForm_AttachmentUploadsAppendToCurrentSession(t *testing.T) {
	configs := []ChatInputFormConfig{
		{
			FormID:       "chat-form",
			InputID:      "message-input",
			PostEndpoint: "/chat/send",
			TargetID:     "chat-messages",
		},
		{
			FormID:       "task-thread-form",
			InputID:      "task-message-input",
			PostEndpoint: "/tasks/task-123/thread",
			TargetID:     "task-thread-messages",
			TaskID:       "task-123",
		},
	}
	for _, config := range configs {
		t.Run(config.FormID, func(t *testing.T) {
			var buf bytes.Buffer
			if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render ChatInputForm: %v", err)
			}
			content := buf.String()
			required := []string{
				"var existingAttachmentCount = listContainer ? listContainer.querySelectorAll('[data-pending-attachment]').length : 0;",
				"if (existingAttachmentCount + files.length > 3)",
				"if (sessionInput && sessionInput.value) formData.append('attachment_session_id', sessionInput.value);",
				"if (sessionInput) sessionInput.value = result.session_id;",
				"div.setAttribute('data-pending-attachment', 'true');",
				"appendPendingAttachmentRow(listContainer, att);",
			}
			for _, r := range required {
				if !strings.Contains(content, r) {
					t.Fatalf("attachment upload script missing %q", r)
				}
			}
			if count := strings.Count(content, "listContainer.innerHTML = '';"); count != 1 {
				t.Fatalf("attachment upload script must not clear previews after upload, while Clear All should still clear intentionally; got %d clear calls", count)
			}
		})
	}
}

func TestChatInputForm_AttachmentUploadsSerializeInFlightDrops(t *testing.T) {
	configs := []ChatInputFormConfig{
		{
			FormID:       "chat-form",
			InputID:      "message-input",
			PostEndpoint: "/chat/send",
			TargetID:     "chat-messages",
		},
		{
			FormID:       "task-thread-form",
			InputID:      "task-message-input",
			PostEndpoint: "/tasks/task-123/thread",
			TargetID:     "task-thread-messages",
			TaskID:       "task-123",
		},
	}
	for _, config := range configs {
		t.Run(config.FormID, func(t *testing.T) {
			var buf bytes.Buffer
			if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render ChatInputForm: %v", err)
			}
			content := buf.String()
			required := []string{
				"var attachmentUploadQueue = Promise.resolve();",
				"var attachmentUploadGeneration = 0;",
				"attachmentUploadGeneration++;",
				"async function uploadFiles(files, uploadGeneration)",
				"function handleFiles(files)",
				"var uploadGeneration = attachmentUploadGeneration;",
				"attachmentUploadQueue = attachmentUploadQueue.then(function()",
				"if (uploadGeneration !== attachmentUploadGeneration) return;",
				"return uploadFiles(filesToUpload, uploadGeneration);",
			}
			for _, r := range required {
				if !strings.Contains(content, r) {
					t.Fatalf("in-flight attachment upload serialization script missing %q", r)
				}
			}

			handleFilesIndex := strings.Index(content, "function handleFiles(files)")
			uploadFilesIndex := strings.Index(content, "async function uploadFiles(files, uploadGeneration)")
			formDataIndex := strings.Index(content, "if (sessionInput && sessionInput.value) formData.append('attachment_session_id', sessionInput.value);")
			if uploadFilesIndex == -1 || handleFilesIndex == -1 || formDataIndex == -1 {
				t.Fatal("attachment upload functions missing from rendered composer")
			}
			if !(uploadFilesIndex < formDataIndex && formDataIndex < handleFilesIndex) {
				t.Fatal("attachment session ID must be read inside the queued upload execution, not before earlier uploads complete")
			}
		})
	}
}

func TestChatInputForm_MessageHistoryScopedPerTaskThread(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:       "task-thread-form",
		InputID:      "task-message-input",
		PostEndpoint: "/tasks/task-123/thread",
		TargetID:     "task-thread-messages",
		TaskID:       "task-123",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `data-message-history-key="openvibely-task-thread-message-history-task-123"`) {
		t.Fatal("task thread form should use a task-scoped message history key")
	}
	if strings.Contains(content, `data-message-history-key="openvibely-chat-message-history"`) {
		t.Fatal("task thread form must not share the global chat message history key")
	}
}

func TestChatInputForm_MobileControlsStayContained(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:            "chat-form",
		InputID:           "message-input",
		PostEndpoint:      "/chat/send",
		TargetID:          "chat-messages",
		ShowModelSelector: true,
		ShowModeSelector:  true,
		Agents: []models.LLMConfig{
			{ID: "agent-1", Name: "Very Long Agent Name That Should Not Push Send Button", Model: "very-long-model-name"},
		},
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	gutterClass := `class="chat-input-shadow-gutter w-full min-w-0 max-w-full mt-6"`
	if !strings.Contains(content, gutterClass) {
		t.Fatalf("chat composer should render inside a full-width shadow gutter with a persistent top gap and without artificial side padding or desktop caps; missing %q", gutterClass)
	}
	if strings.Contains(content, `sm:max-w-3xl`) || strings.Contains(content, `sm:mx-auto`) || strings.Contains(content, `px-3 pt-2 pb-4`) || strings.Contains(content, `pt-2 pb-4`) {
		t.Fatal("chat composer gutter must not add desktop side gaps or mobile right-side empty space")
	}
	formClass := `class="chat-input-container rounded-xl p-4 relative min-w-0 max-w-full"`
	if !strings.Contains(content, formClass) {
		t.Fatalf("chat composer shell should fill the shadow gutter without clipping; missing %q", formClass)
	}
	if !strings.Contains(content, `var form = wrapper ? wrapper.querySelector('form.chat-input-container') : null;`) {
		t.Fatal("chat composer script must find the nested form inside the shadow gutter wrapper")
	}
	if strings.Contains(content, `chat-input-container rounded-xl p-4 relative w-full`) {
		t.Fatal("chat composer shell must not use w-full with visual margins because it clips the rounded right edge")
	}
	if strings.Contains(content, `class="chat-input-container rounded-xl p-4 relative min-w-0 max-w-full overflow-x-hidden"`) {
		t.Fatal("chat composer shell must not hard-clip its rounded edge with overflow-x-hidden")
	}
	required := []string{
		`class="flex items-center justify-between gap-2 pt-2 min-w-0 max-w-full overflow-hidden"`,
		`class="flex items-center gap-1 min-w-0 flex-1"`,
		`w-auto min-w-0 max-w-[7.5rem] sm:max-w-[140px]`,
		`w-auto min-w-0 max-w-[6rem] sm:max-w-[120px]`,
		`class="flex items-center gap-2 flex-shrink-0"`,
	}
	for _, expected := range required {
		if !strings.Contains(content, expected) {
			t.Fatalf("chat composer should keep mobile controls contained; missing %q", expected)
		}
	}
}

func TestChatInputForm_MessageHistoryCursorGuardsPreventArrowHijack(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:       "chat-form",
		InputID:      "message-input",
		PostEndpoint: "/chat/send",
		TargetID:     "chat-messages",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	required := []string{
		"function isAtFirstLineStart(textarea)",
		"if (start !== end) return false;",
		"return start === 0;",
		"if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && messageInput.value !== '' && !isAtFirstLineStart(messageInput)) return false;",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Fatalf("message history cursor guard missing %q", r)
		}
	}
}

func TestChatInputForm_SharedQueueAndSteerShortcuts(t *testing.T) {
	configs := []ChatInputFormConfig{
		{
			FormID:        "chat-form",
			InputID:       "message-input",
			PostEndpoint:  "/chat/send?project_id=project-1",
			SteerEndpoint: "/chat/steer?project_id=project-1",
			TargetID:      "chat-messages",
			IsRunning:     true,
			ActiveTurnID:  "exec-active-chat",
		},
		{
			FormID:        "task-thread-form",
			InputID:       "task-message-input",
			PostEndpoint:  "/tasks/task-1/thread",
			SteerEndpoint: "/tasks/task-1/thread/steer",
			TargetID:      "task-thread-messages",
			TaskID:        "task-1",
			IsRunning:     true,
			ActiveTurnID:  "exec-active-task",
		},
	}
	for _, config := range configs {
		t.Run(config.FormID, func(t *testing.T) {
			var buf bytes.Buffer
			if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
				t.Fatalf("render ChatInputForm: %v", err)
			}
			content := buf.String()
			required := []string{
				`data-steer-endpoint="` + config.SteerEndpoint + `"`,
				"function chatComposerUsesAppleShortcuts()",
				"navigator.userAgentData && navigator.userAgentData.platform",
				"/Mac|iPhone|iPad|iPod|iOS/i.test(platform)",
				"⏎ sends or queues · ⌘+⏎ steers",
				"Enter sends or queues · Ctrl+Enter steers",
				`data-composer-running="true"`,
				"function composerHasActiveTurn()",
				"new MutationObserver(handleComposerStateMutation).observe(form, { childList: true, subtree: true })",
				"if (!composerHasActiveTurn()) {",
				"if (guard) guard.remove();",
				"if (e.isComposing || e.keyCode === 229) return;",
				"if (e.key === 'Enter' && !e.shiftKey)",
				"if (shortcutModifierPressed(e)) {",
				"if (steerEndpoint && turnID) {",
				"submitComposer(normalPostEndpoint, 'normal');",
				"submitComposer(steerEndpoint, 'steer');",
				"form.addEventListener('htmx:beforeRequest'",
				"if (request._chatSubmittedIntent === 'normal') normalRequestInFlight++;",
				"e.stopImmediatePropagation();",
			}
			for _, expected := range required {
				if !strings.Contains(content, expected) {
					t.Fatalf("shared shortcut script missing %q", expected)
				}
			}
			if strings.Contains(content, "if (intent !== 'steer') normalRequestInFlight++") {
				t.Fatal("normal request must not be counted before native form validation succeeds")
			}
		})
	}
}

func TestChatInputForm_RunningChatExposesModifierSteeringEndpoint(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:        "chat-form",
		InputID:       "message-input",
		PostEndpoint:  "/chat/send",
		TargetID:      "chat-messages",
		IsRunning:     true,
		ActiveTurnID:  "exec-active-chat",
		SteerEndpoint: "/chat/steer",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `name="expected_turn_id" value="exec-active-chat"`) {
		t.Fatal("running chat form should keep the active turn guard for queue safety")
	}
	if !strings.Contains(content, `data-steer-endpoint="/chat/steer"`) {
		t.Fatal("running chat form must expose the guarded steering endpoint for modifier-click")
	}
	if strings.Contains(content, `name="steer_endpoint"`) || strings.Contains(content, ">Steer") {
		t.Fatal("running chat form must not expose a separate visible composer steering control")
	}
	if !strings.Contains(content, "submitComposer(steerEndpoint, 'steer');") || !strings.Contains(content, "htmx.ajax('POST', endpoint") {
		t.Fatal("running chat modifier-click must post through the steering endpoint")
	}
}

func TestChatInputForm_RunningTaskThreadExposesModifierSteeringEndpoint(t *testing.T) {
	var buf bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID:        "task-thread-form",
		InputID:       "task-message-input",
		PostEndpoint:  "/tasks/task-1/thread",
		TargetID:      "task-thread-messages",
		TaskID:        "task-1",
		IsRunning:     true,
		ActiveTurnID:  "exec-active-task",
		SteerEndpoint: "/tasks/task-1/thread/steer",
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `name="expected_turn_id" value="exec-active-task"`) {
		t.Fatal("running task-thread form should keep the active turn guard for queue safety")
	}
	if !strings.Contains(content, `data-steer-endpoint="/tasks/task-1/thread/steer"`) {
		t.Fatal("running task-thread form must expose the guarded steering endpoint for modifier-click")
	}
	if strings.Contains(content, `name="steer_endpoint"`) || strings.Contains(content, ">Steer") {
		t.Fatal("running task-thread form must not expose a separate visible composer steering control")
	}
}

func TestPendingThreadInputRows_LeavesComposerOwnedInputsOutOfTranscript(t *testing.T) {
	inputs := []models.ThreadInput{
		{ID: "queued-1", TaskID: "task-1", InputMode: models.ThreadInputModeQueued, Content: "queue this"},
		{ID: "steer-1", TaskID: "task-1", InputMode: models.ThreadInputModeSteering, Content: "steer this"},
	}
	var buf bytes.Buffer
	err := PendingThreadInputRows(inputs, func(input models.ThreadInput) string {
		return "/tasks/" + input.TaskID + "/thread/queued/" + input.ID + "/steer"
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render PendingThreadInputRows: %v", err)
	}

	content := buf.String()
	if strings.Contains(content, `thread-input-queued-1`) || strings.Contains(content, `thread-input-steer-1`) || strings.Contains(content, `pending-steering-inputs`) {
		t.Fatal("queued and steering pending rows should render inside the composer, not the chat transcript")
	}
}

func TestChatComposerQueuedInputRows_RenderInsideInputBoxStyle(t *testing.T) {
	inputs := []models.ThreadInput{
		{ID: "queued-1", TaskID: "task-1", InputMode: models.ThreadInputModeQueued, Content: "queue this", AttachmentSessionID: "pending-session-1"},
		{ID: "steer-1", TaskID: "task-1", InputMode: models.ThreadInputModeSteering, Content: "steer this", AttachmentSessionID: "steering-session-1"},
	}
	var buf bytes.Buffer
	err := ChatComposerQueuedInputRows(inputs, func(input models.ThreadInput) string {
		return "/tasks/" + input.TaskID + "/thread/queued/" + input.ID + "/steer"
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render ChatComposerQueuedInputRows: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `id="pending-thread-inputs"`) || !strings.Contains(content, `space-y-1.5`) {
		t.Fatal("pending rows should render in a composer queue container")
	}
	if !strings.Contains(content, `data-task-id=""`) {
		t.Fatal("global chat pending container should explicitly render an empty task scope")
	}
	if strings.Contains(content, `id="pending-thread-inputs" class="space-y-1.5 mb-3`) || strings.Contains(content, `id="pending-thread-inputs" class="space-y-1.5 pb-4`) {
		t.Fatal("pending row spacing should be controlled by shared CSS, not per-render classes")
	}
	if !strings.Contains(content, `queued-input-row`) || !strings.Contains(content, `steering-input-row`) || !strings.Contains(content, `bg-base-300/45`) || !strings.Contains(content, `flex-1`) || !strings.Contains(content, `ml-auto`) {
		t.Fatal("pending rows should look like part of the input box with right-aligned actions")
	}
	if !strings.Contains(content, `hx-post="/tasks/task-1/thread/queued/queued-1/steer"`) || !strings.Contains(content, "Steer") {
		t.Fatal("queued pending row must expose Steer action")
	}
	if !strings.Contains(content, "Attachments queued") || !strings.Contains(content, `aria-label="Attachments queued with this follow-up"`) || !strings.Contains(content, `M15.172 7l-6.586 6.586`) {
		t.Fatal("queued pending row with an attachment session should indicate that attachments are queued")
	}
	if !strings.Contains(content, `hx-post="/thread-inputs/queued-1/cancel"`) || !strings.Contains(content, `aria-label="Cancel queued follow-up"`) || !strings.Contains(content, `M19 7l-.867 12.142`) {
		t.Fatal("queued pending row must expose the alerts trash icon cancel action")
	}
	if !strings.Contains(content, `thread-input-steer-1`) || !strings.Contains(content, "Steering pending") || !strings.Contains(content, `aria-label="Cancel pending steering"`) {
		t.Fatal("composer pending rows should include steering rows with a trash-icon cancel action")
	}
	if !strings.Contains(content, "Attachments included") || !strings.Contains(content, `aria-label="Attachments included with this steering instruction"`) {
		t.Fatal("steering pending row with an attachment session should indicate that attachments are included")
	}
	if strings.Contains(content, "Send now") || strings.Contains(content, "btn-warning") || strings.Contains(content, "bg-warning") || strings.Contains(content, ">Cancel</button>") || strings.Contains(content, ">×</button>") {
		t.Fatal("composer pending rows should avoid old warning/text/× cancel treatments")
	}
}

func TestChatQueuedInputRowOOB_WithAttachmentsShowsQueuedAttachmentIndicator(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatQueuedInputRowOOB("queued-1", "queue this", "/chat/queued/queued-1/steer", true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatQueuedInputRowOOB: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `hx-swap-oob="beforeend"`) || !strings.Contains(content, `thread-input-queued-1`) {
		t.Fatal("OOB queued row should append the pending input row")
	}
	if !strings.Contains(content, "Attachments queued") || !strings.Contains(content, `aria-label="Attachments queued with this follow-up"`) {
		t.Fatal("OOB queued row should indicate when attachments are queued with the message")
	}
}

func TestChatSteeringInputRow_WithAttachmentsShowsIncludedAttachmentIndicator(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatSteeringInputRow("steer-1", "steer this", true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatSteeringInputRow: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `thread-input-steer-1`) || !strings.Contains(content, `data-input-mode="steering"`) || !strings.Contains(content, "Steering pending") {
		t.Fatal("steering row should render as a pending steering row")
	}
	if !strings.Contains(content, "Attachments included") || !strings.Contains(content, `aria-label="Attachments included with this steering instruction"`) {
		t.Fatal("steering row should indicate when attachments are included with the message")
	}
}

func TestTaskQueuedInputRowOOB_TargetsOnlyMatchingTaskPendingContainer(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatQueuedInputRowOOBForTask("queued-task-1", "queue this", "/tasks/task-a/thread/queued/queued-task-1/steer", false, "task-a").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task ChatQueuedInputRowOOB: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, `data-task-id="task-a"`) || !strings.Contains(content, `data-thread-input-id="queued-task-1"`) {
		t.Fatalf("task queued OOB row must carry task/input identity, got: %s", content)
	}
	if !strings.Contains(content, `hx-swap-oob="beforeend:#pending-thread-inputs[data-task-id=&#34;task-a&#34;]"`) {
		t.Fatalf("task queued OOB row must append only to the matching task pending container, got: %s", content)
	}
	if strings.Contains(content, `hx-swap-oob="beforeend"`) {
		t.Fatal("task queued OOB row must not use the global pending container target")
	}
}

func TestChatBubbleWithAttachments_MarksImagesForSmartScroll(t *testing.T) {
	attachments := []models.ChatAttachment{
		{ID: "att-image", FileName: "screenshot.png", MediaType: "image/png", FileSize: 1234},
		{ID: "att-file", FileName: "notes.txt", MediaType: "text/plain", FileSize: 42},
	}

	var buf bytes.Buffer
	if err := ChatBubbleWithAttachments("User", "see screenshot", attachments, "project-a").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleWithAttachments: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `data-chat-attachment-image="true"`) {
		t.Fatal("image attachments must be marked so lazy-load layout growth can trigger smart-scroll correction")
	}
	if count := strings.Count(content, `data-chat-attachment-image="true"`); count != 1 {
		t.Fatalf("expected only image attachments to be marked for smart scroll, got %d markers", count)
	}
	if !strings.Contains(content, `/chat/attachments/att-image/download?project_id=project-a`) {
		t.Fatalf("image attachment URL must carry project context, got: %s", content)
	}
	if !strings.Contains(content, `/chat/attachments/att-file/download?project_id=project-a`) {
		t.Fatalf("file attachment URL must carry project context, got: %s", content)
	}
}

func TestChatAttachmentListOnlyCarriesProjectContext(t *testing.T) {
	attachments := []models.ChatAttachment{{ID: "att-list", FileName: "notes.txt", MediaType: "text/plain", FileSize: 42}}

	var buf bytes.Buffer
	if err := ChatAttachmentListOnly(attachments, "project-a").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAttachmentListOnly: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `/chat/attachments/att-list/download?project_id=project-a`) {
		t.Fatalf("download URL must carry project context, got: %s", content)
	}
	if !strings.Contains(content, `hx-delete="/chat/attachments/att-list?project_id=project-a"`) {
		t.Fatalf("delete URL must carry project context, got: %s", content)
	}
}

func TestTaskAttachmentListCarriesProjectContext(t *testing.T) {
	attachments := []models.Attachment{{ID: "att-list", FileName: "notes.txt", MediaType: "text/plain", FileSize: 42}}

	var buf bytes.Buffer
	if err := AttachmentList(attachments, "project-a", "task-a").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render AttachmentList: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `hx-post="/tasks/task-a/attachments?project_id=project-a"`) {
		t.Fatalf("upload URL must carry project context, got: %s", content)
	}
	if !strings.Contains(content, `hx-delete="/attachments/att-list?project_id=project-a"`) {
		t.Fatalf("delete URL must carry project context, got: %s", content)
	}
}

func TestChatAutoScrollScript_BindsAttachmentImageSmartScroll(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	required := []string{
		"window.markChatSendScrollIntent = function(formOrMessagesId, explicitScope)",
		"window.consumeChatSendScrollIntent = function(messagesId, explicitScope)",
		"window.initializeChatTranscriptScrollState = function(options)",
		"if (sentByUser)",
		"window.hasChatSendScrollIntent = function(messagesId, explicitScope)",
		"function chatSendScrollIntentScopeKey(scope)",
		"window.getChatSendIntentScopeRevision = function(messagesId, scope)",
		"window.advanceChatSendIntentScopeRevision = function(messagesEl)",
		"var scopedIntents = window._chatSendScrollIntents[messagesId] || {};",
		"scopedIntents[scopeKey] = { scopeRevision: null, scope: intentScope };",
		"var intent = scopedIntents && scopedIntents[chatSendScrollIntentScopeKey(currentScope)];",
		"scopeRevision === intent.scopeRevision",
		"delete scopedIntents[chatSendScrollIntentScopeKey(currentScope)];",
		"window.scrollChatToBottomAfterLayout = function(messagesEl, smooth)",
		"window.bindAttachmentImageSmartScroll = function(messagesEl, trackerKey, trackerFallback)",
		`querySelectorAll('img[data-chat-attachment-image="true"]')`,
		"var hasSendIntent = window.hasChatSendScrollIntent(messagesId);",
		"snapshotPinnedState();",
		"var tracker = trackerKey && window.resolveScrollTracker ? window.resolveScrollTracker(trackerKey, liveMessages) : trackerFallback;",
		"if (hasSendIntent && tracker) tracker.userScrolledUp = false;",
		"shouldScroll = hasSendIntent || !tracker || tracker.shouldAutoScroll();",
		"if (tracker && !tracker.shouldAutoScroll()) return;",
		"img.addEventListener('load', scrollAfterImageLayout, { once: true });",
		"img.addEventListener('error', scrollAfterImageLayout, { once: true });",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Fatalf("attachment image smart-scroll helper missing %q", r)
		}
	}
	mismatchGuard := strings.Index(content, "if (messagesEl && intentScope !== mountedScope)")
	trackerResolution := strings.Index(content, "var tracker = window.resolveScrollTracker ? window.resolveScrollTracker(trackerKey, messagesEl) : window[trackerKey];")
	if mismatchGuard < 0 || trackerResolution < 0 || mismatchGuard > trackerResolution {
		t.Fatal("send intent scope mismatch must be rejected before resolving or resetting the mounted transcript tracker")
	}
}

func TestTaskThreadUsesSharedSendScrollTracker(t *testing.T) {
	var taskBuf bytes.Buffer
	if err := TaskThreadView(&models.Task{ID: "task-scroll"}, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &taskBuf); err != nil {
		t.Fatalf("render task thread: %v", err)
	}

	content := taskBuf.String()
	if !strings.Contains(content, "window.resolveScrollTracker('scrollTracker_task-thread-messages', chatMessages)") {
		t.Fatal("task Thread must register its page tracker with the shared send/layout coordinator")
	}
	if count := strings.Count(content, `data-scroll-intent-scope="task-scroll"`); count != 2 {
		t.Fatalf("task Thread transcript and composer must share the task-scoped send intent, got %d markers", count)
	}
	if !strings.Contains(content, "window.consumeChatSendScrollIntent('task-thread-messages', _taskThreadTaskId())") {
		t.Fatal("task Thread must consume send intent using the current task identity")
	}
}

func TestChatInputForm_CommitsSendScrollIntentAfterSuccessfulAcceptance(t *testing.T) {
	config := ChatInputFormConfig{
		FormID:       "chat-form",
		InputID:      "message-input",
		PostEndpoint: "/chat/send",
		TargetID:     "chat-messages",
	}

	var buf bytes.Buffer
	if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}
	content := buf.String()

	if strings.Contains(content, "form.addEventListener('submit', markSendScrollIntent);") {
		t.Fatal("composer must not replace upward-reading intent before the send is accepted")
	}
	for _, required := range []string{
		"if (event.detail.successful)",
		"if (responseText.trim() !== '')",
		"if (window.markChatSendScrollIntent) window.markChatSendScrollIntent(form);",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("accepted composer send must commit shared bottom-pin intent: missing %q", required)
		}
	}
}

func TestChatInputForm_PersistsPendingAttachmentsAcrossNavigation(t *testing.T) {
	if got := chatAttachmentStateKey(ChatInputFormConfig{TaskID: "task-attachments"}); got != "openvibely-task-thread-attachments-task-attachments" {
		t.Fatalf("task-thread attachment state key = %q", got)
	}
	config := ChatInputFormConfig{
		FormID:       "chat-form",
		InputID:      "message-input",
		PostEndpoint: "/chat/send",
		TargetID:     "chat-messages",
		ProjectID:    "project-attachments",
	}

	var buf bytes.Buffer
	if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatInputForm: %v", err)
	}
	content := buf.String()
	for _, required := range []string{
		`data-attachment-state-key="openvibely-chat-attachments-project-attachments"`,
		"window._chatPendingAttachmentStates = window._chatPendingAttachmentStates || {};",
		"window.saveChatPendingAttachmentState = function(formOrID)",
		"window.restoreChatPendingAttachmentState = function(formOrID)",
		"targetSession.setAttribute('value', state.sessionID);",
		"sessionStorage.setItem(key, JSON.stringify(state));",
		"window.restoreChatPendingAttachmentState(form);",
		"document.body.addEventListener('htmx:beforeHistorySave'",
		"document.querySelectorAll('form[data-attachment-state-key]').forEach(function(restoredForm)",
		"window.saveChatPendingAttachmentState(form);",
		"delete window._chatPendingAttachmentStates[attachmentStateKey];",
		"sessionStorage.removeItem(attachmentStateKey);",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("pending attachment navigation persistence is missing %q", required)
		}
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
			},
		},
		{
			name:    "ChatBubbleStreamingResume integration",
			content: resumeContent,
			mustHave: []string{
				`data-streaming-resume="true"`,
				"data-exec-id",
				"data-initial-byte-length", "data-messages-container",
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
	if strings.Contains(streamingContent, "tracker.resetOnUserSend()") {
		t.Fatal("stream discovery without a deliberate local send must preserve upward-reading intent")
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
		if !strings.Contains(content, "data-initial-byte-length") {
			t.Error("Missing data-initial-byte-length attribute")
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
			name:     "preserves inline status example",
			input:    "Use `[STATUS: FAILED | reason]` only as the final standalone line.\n[STATUS: SUCCESS]",
			expected: "Use `[STATUS: FAILED | reason]` only as the final standalone line.",
		},
		{
			name:     "preserves inline tool example",
			input:    "The transcript may contain `[Using tool: bash]`.\n[Using tool: bash]",
			expected: "The transcript may contain `[Using tool: bash]`.",
		},
		{
			name:     "preserves inline legacy tool result examples",
			input:    "Examples: `[Tool bash done: output]` and `[Tool bash error: command failed]`.\n[Tool bash done: actual output]",
			expected: "Examples: `[Tool bash done: output]` and `[Tool bash error: command failed]`.",
		},
		{
			name:     "preserves inline same-line tool result block example",
			input:    "Example: `[Tool grep_search done]matches[/Tool]`.\n[Tool grep_search done]actual[/Tool]\nDone.",
			expected: "Example: `[Tool grep_search done]matches[/Tool]`.\nDone.",
		},
		{
			name:     "preserves fenced tool controls and strips later real controls",
			input:    "Examples:\n```text\n[Using tool: bash]\n[Tool bash done: output]\n[Tool read_file error]\nnot found\n[/Tool]\n```\n[Using tool: bash]\n[Tool bash done: actual]",
			expected: "Examples:\n```text\n[Using tool: bash]\n[Tool bash done: output]\n[Tool read_file error]\nnot found\n[/Tool]\n```",
		},
		{
			name:     "preserves tilde fenced tool controls",
			input:    "~~~log\n[Using tool: grep_search]\n[Tool grep_search done]\nmatch\n[/Tool]\n~~~",
			expected: "~~~log\n[Using tool: grep_search]\n[Tool grep_search done]\nmatch\n[/Tool]\n~~~",
		},
		{
			name:     "preserves inline thinking controls",
			input:    "Literal `[Thinking]example[/Thinking]`.\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "Literal `[Thinking]example[/Thinking]`.\nVisible answer.",
		},
		{
			name:     "unclosed inline thinking example cannot mask later real control",
			input:    "Literal `[Thinking]example`.\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "Literal `[Thinking]example`.\nVisible answer.",
		},
		{
			name:     "preserves fenced thinking controls and strips later real control",
			input:    "Examples:\n```text\n[Thinking]\nfenced example\n[/Thinking]\n```\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "Examples:\n```text\n[Thinking]\nfenced example\n[/Thinking]\n```\nVisible answer.",
		},
		{
			name:     "preserves tilde fenced thinking controls",
			input:    "~~~log\n[Thinking]\nfenced example\n[/Thinking]\n~~~",
			expected: "~~~log\n[Thinking]\nfenced example\n[/Thinking]\n~~~",
		},
		{
			name:     "preserves malformed status controls",
			input:    "Malformed controls: [STATUS: FAILED], [STATUS: NEEDS_FOLLOWUP | ], [STATUS: SUCCESS | unexpected].",
			expected: "Malformed controls: [STATUS: FAILED], [STATUS: NEEDS_FOLLOWUP | ], [STATUS: SUCCESS | unexpected].",
		},
		{
			name:     "preserves failed status with extra pipe delimiter",
			input:    "Failure text.\n[STATUS: FAILED | reason | extra]",
			expected: "Failure text.\n[STATUS: FAILED | reason | extra]",
		},
		{
			name:     "preserves followup status with extra pipe delimiter",
			input:    "Follow-up text.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]",
			expected: "Follow-up text.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]",
		},
		{
			name:     "preserves noncanonical status whitespace",
			input:    "Spacing variant:\n[STATUS:  SUCCESS]",
			expected: "Spacing variant:\n[STATUS:  SUCCESS]",
		},
		{
			name:     "preserves status followed by thinking",
			input:    "[STATUS: SUCCESS]\n[Thinking]\nLater internal text\n[/Thinking]",
			expected: "[STATUS: SUCCESS]",
		},
		{
			name:     "preserves status in explanatory prose",
			input:    "The canonical completion control is [STATUS: SUCCESS] when used correctly.",
			expected: "The canonical completion control is [STATUS: SUCCESS] when used correctly.",
		},
		{
			name:     "preserves status bullet",
			input:    "Example:\n- [STATUS: FAILED | reason]",
			expected: "Example:\n- [STATUS: FAILED | reason]",
		},
		{
			name:     "preserves status quote",
			input:    "Example:\n> [STATUS: NEEDS_FOLLOWUP | reason]",
			expected: "Example:\n> [STATUS: NEEDS_FOLLOWUP | reason]",
		},
		{
			name:     "preserves status fenced example",
			input:    "Example:\n```text\n[STATUS: SUCCESS]\n```",
			expected: "Example:\n```text\n[STATUS: SUCCESS]\n```",
		},
		{
			name:     "mismatched fence character does not expose status",
			input:    "Example:\n```text\n~~~\n[STATUS: FAILED | still fenced]",
			expected: "Example:\n```text\n~~~\n[STATUS: FAILED | still fenced]",
		},
		{
			name:     "shorter delimiter does not expose status",
			input:    "Example:\n`````text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]",
			expected: "Example:\n`````text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]",
		},
		{
			name:     "matching long delimiter exposes later real status",
			input:    "Example:\n`````text\n```\n`````\n[STATUS: FAILED | real failure]",
			expected: "Example:\n`````text\n```\n`````",
		},
		{
			name:     "preserves status with trailing prose",
			input:    "[STATUS: FAILED | reason] but this is explanatory text",
			expected: "[STATUS: FAILED | reason] but this is explanatory text",
		},
		{
			name:     "preserves non-final standalone status line",
			input:    "[STATUS: SUCCESS]\nMore explanation follows.",
			expected: "[STATUS: SUCCESS]\nMore explanation follows.",
		},
		{
			name:     "strips only final canonical status control",
			input:    "[STATUS: SUCCESS]\nMore explanation follows.\n[STATUS: FAILED | actual failure]",
			expected: "[STATUS: SUCCESS]\nMore explanation follows.",
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
			name:     "preserves inert CREATE_TASK text",
			input:    "Creating.\n[CREATE_TASK]\n{\"title\":\"test\"}\n[/CREATE_TASK]\nDone.",
			expected: "Creating.\n[CREATE_TASK]\n{\"title\":\"test\"}\n[/CREATE_TASK]\nDone.",
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
			name:     "strips bare CR multi_tool_use artifact without consuming answer",
			input:    "} to=multi_tool_use.parallel code something\rNarrative continues.",
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

func TestCleanDisplayContent_PreservesCodedAliasesAndCleansRealAliases(t *testing.T) {
	codedTool := `[Using tool: bash"> <parameter name="command">echo coded</parameter> </invoke>`
	multilineThinking := "`<thinking>multiline\nthought</thinking>`"
	multilineTool := "``[Using tool: bash\">\n<parameter name=\"command\">echo multiline</parameter>\n</invoke>``"
	input := "Inline `<thinking>coded</thinking>` and `" + codedTool + "`.\n" + multilineThinking + "\n" + multilineTool + "\n" +
		"```text\n<thinking>fenced</thinking>\n" + codedTool + "\n```\n" +
		"~~~text\n<thinking>tilde</thinking>\n" + codedTool + "\n~~~\n" +
		"<thinking>real internal</thinking>\nVisible answer.\n" +
		`[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`

	got := CleanDisplayContent(input)
	for _, literal := range []string{
		"`<thinking>coded</thinking>`",
		"`" + codedTool + "`",
		multilineThinking,
		multilineTool,
		"```text\n<thinking>fenced</thinking>\n" + codedTool + "\n```",
		"~~~text\n<thinking>tilde</thinking>\n" + codedTool + "\n~~~",
	} {
		if !strings.Contains(got, literal) {
			t.Fatalf("server display changed coded alias %q:\n%q", literal, got)
		}
	}
	if strings.Contains(got, "real internal") || strings.Contains(got, "echo real") || !strings.Contains(got, "Visible answer.") {
		t.Fatalf("server display did not clean real aliases normally:\n%q", got)
	}
}

func TestCleanDisplayContent_BareCRControlsAndSummaries(t *testing.T) {
	coded := "~~~~~text\r[Thinking]coded[/Thinking]\r[Using tool: bash]\r[Tool bash done]\rcoded output\r[/Tool]\r[TASK_ID:coded/create]\r[TASK_EDITED:coded/edit]\r~~~\r~~~~~~\t "
	input := coded + "\r[Thinking]\rreal thought\r[/Thinking]\r[Using tool: bash]\r[Tool bash done]\rreal output\r[/Tool]\rVisible answer.\r[STATUS: SUCCESS]"
	got := CleanDisplayContent(input)
	if !strings.Contains(got, coded) || strings.Contains(got, "real thought") || strings.Contains(got, "real output") || strings.Contains(got, "[STATUS: SUCCESS]") || !strings.Contains(got, "Visible answer.") {
		t.Fatalf("server display did not apply bare-CR control boundaries:\n%q", got)
	}

	summaries := "Intro.\r\r---\rEdited 1 task(s):\r- \"Old\" (updated: title)\r\rExample:\r```text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r```\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]"
	want := "Intro.\r\rExample:\r```text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r```\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]"
	if got := CleanDisplayContent(summaries); got != want {
		t.Fatalf("server display bare-CR summary deduplication =\n%q\nwant:\n%q", got, want)
	}
}

func TestCleanDisplayContent_EscapedBackticksDoNotProtectRealControls(t *testing.T) {
	validCoded := "``[Thinking]coded[/Thinking]\n[Using tool: bash]``"
	input := `Escaped \` + "`" + `<thinking>real alias</thinking> escaped \` + "`" + "\n" +
		validCoded + "\n[Using tool: bash]\nVisible answer.\n[STATUS: SUCCESS]"
	got := CleanDisplayContent(input)
	if !strings.Contains(got, validCoded) || strings.Contains(got, "real alias") || strings.Count(got, "[Using tool: bash]") != 1 || strings.Contains(got, "[STATUS: SUCCESS]") || !strings.Contains(got, "Visible answer.") {
		t.Fatalf("server display did not honor escaped delimiter parity:\n%q", got)
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

	if !strings.Contains(got, "[CREATE_TASK]") {
		t.Errorf("should preserve inert [CREATE_TASK] text, got:\n%q", got)
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
		{
			name:     "inline coded summary text remains unchanged",
			input:    "Example: `Created 1 task(s): [TASK_ID:coded]`.\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
			expected: "Example: `Created 1 task(s): [TASK_ID:coded]`.\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
		},
		{
			name:     "backtick fenced created summary before real summary remains unchanged",
			input:    "Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
			expected: "Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
		},
		{
			name:     "tilde fenced edited summary before real summary remains unchanged",
			input:    "Example:\n~~~text\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\n~~~\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]",
			expected: "Example:\n~~~text\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\n~~~\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]",
		},
		{
			name:     "multiline inline created summary before real summary remains unchanged",
			input:    "Example `literal\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\nend`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
			expected: "Example `literal\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\nend`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
		},
		{
			name:     "multiline inline edited summary does not mask duplicate real summaries",
			input:    "Intro.\n\n---\nEdited 1 task(s):\n- \"Old\" (updated: title)\n\nExample ``literal\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\nend``\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]",
			expected: "Intro.\n\nExample ``literal\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\nend``\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]",
		},
		{
			name:     "unmatched delimiter before later coded summary is preserved",
			input:    "Unmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]`\n\n---\nCreated 1 task(s):\n- \"Old\" (backlog) [TASK_ID:old]\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
			expected: "Unmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
		},
		{
			name:     "coded summary between duplicate real summaries is preserved",
			input:    "Intro.\n\n---\nCreated 1 task(s):\n- \"Old\" (backlog)\n\nLiteral example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
			expected: "Intro.\n\nLiteral example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]",
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

func TestCleanDisplayContent_PreservesCodedTaskSummariesWhileDeduplicatingRealSummaries(t *testing.T) {
	input := "Unmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Inline coded\" (backlog) [TASK_ID:inline-coded]`\n\n" +
		"Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n" +
		"---\nCreated 1 task(s):\n- \"Old\" (backlog)\n\n" +
		"---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	want := "Unmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Inline coded\" (backlog) [TASK_ID:inline-coded]`\n\n" +
		"Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n" +
		"---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"

	if got := CleanDisplayContent(input); got != want {
		t.Fatalf("CleanDisplayContent() =\n%q\nwant:\n%q", got, want)
	}
}

func TestDedupTaskSummaries_EscapedBackticksDoNotProtectRealSummaries(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kind    string
		details string
		marker  string
	}{
		{name: "created", kind: "Created", details: "backlog", marker: "TASK_ID"},
		{name: "edited", kind: "Edited", details: "updated: title", marker: "TASK_EDITED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := `Example \` + "`" + "\n---\n" + tt.kind + " 1 task(s):\n- \"Old\" (" + tt.details + ") [" + tt.marker + ":old]\n" + `\` + "`" +
				"\n\n---\n" + tt.kind + " 1 task(s):\n- \"Real\" (" + tt.details + ") [" + tt.marker + ":real]"
			got := DedupTaskSummaries(input)
			if strings.Contains(got, `"Old"`) || strings.Count(got, tt.kind+" 1 task(s):") != 1 || !strings.Contains(got, `"Real"`) {
				t.Fatalf("escaped backticks falsely protected a real summary:\n%q", got)
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
	err := ChatMessages(executions, task, nil, "task-thread-messages", "task-thread-view", false, task.ProjectID).Render(context.Background(), &buf)
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

func TestSharedTaskResultLinkConversionAvailableOnDirectTaskThreadLoad(t *testing.T) {
	task := &models.Task{ID: "task-thread-direct", Title: "Direct thread", Prompt: "Run"}
	executions := []models.Execution{{
		ID:         "exec-result",
		Status:     models.ExecCompleted,
		PromptSent: "Create and edit tasks",
		Output:     "Created:\n- \"New task\" (backlog) [TASK_ID:task/id?unsafe]\nEdited:\n- \"Existing task\" (updated: title) [TASK_EDITED:edited/id]\nMultiline examples: `create\n[TASK_ID:coded/create]` and ``edit\n[TASK_EDITED:coded/edit]``\nUnmatched `` prefix; `later\n[TASK_ID:later/create]\n[TASK_EDITED:later/edit]`\nEscaped \\`- \"Escaped task\" (backlog) [TASK_ID:escaped/create] escaped \\`\nEscaped \\``- \"Escaped edit\" (updated: title) [TASK_EDITED:escaped/edit]``\nUnicode fence:\n```text\n```\u00a0\n- \"Unicode coded\" (backlog) [TASK_ID:unicode/create]\n- \"Unicode edited\" (updated: title) [TASK_EDITED:unicode/edit]\n```\nBare CR fence:\n~~~text\r- \"Bare CR coded\" (backlog) [TASK_ID:bare-cr/create]\r- \"Bare CR edited\" (updated: title) [TASK_EDITED:bare-cr/edit]\r~~~\nSame-line example: `[Tool grep_search done]coded[/Tool]`.\n[Using tool: grep_search]\n[Tool grep_search done]actual[/Tool]",
	}}

	var buf bytes.Buffer
	if err := TaskThreadView(task, executions, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadView: %v", err)
	}
	content := renderedBaseMarkdownCodeHelpers(t) + buf.String()

	normalizeToolTextDefinition := strings.Index(content, "window.normalizeTaskResultToolOutputTextNodes = function(messageElement)")
	createDefinition := strings.Index(content, "window.convertTaskLinksInMessage = function(messageElement)")
	editDefinition := strings.Index(content, "window.convertTaskEditLinksInMessage = function(messageElement)")
	codeDefinition := strings.Index(content, "window.codeRanges = function(text)")
	normalizeDefinition := strings.Index(content, "window.normalizeTranscriptMarkers = function(text, ranges)")
	hydration := strings.Index(content, "// Apply cleaning and scroll on load")
	if normalizeToolTextDefinition == -1 || createDefinition == -1 || editDefinition == -1 || codeDefinition == -1 || normalizeDefinition == -1 {
		t.Fatal("direct task-thread output must define task-result converters plus Markdown-aware transcript normalization without requiring a prior /chat load")
	}
	if hydration == -1 || normalizeToolTextDefinition > hydration || createDefinition > hydration || editDefinition > hydration || codeDefinition > hydration || normalizeDefinition > hydration {
		t.Fatal("task-result converters and Markdown-aware transcript normalization must be defined before hard-refresh task-thread hydration")
	}
	for _, marker := range []string{"[TASK_ID:task/id?unsafe]", "[TASK_EDITED:edited/id]", "[TASK_ID:coded/create]", "[TASK_EDITED:coded/edit]", "[TASK_ID:later/create]", "[TASK_EDITED:later/edit]", "[TASK_ID:escaped/create]", "[TASK_EDITED:escaped/edit]", "[TASK_ID:unicode/create]", "[TASK_EDITED:unicode/edit]", "[TASK_ID:bare-cr/create]", "[TASK_EDITED:bare-cr/edit]", "`[Tool grep_search done]coded[/Tool]`", "[Tool grep_search done]actual[/Tool]"} {
		if !strings.Contains(content, marker) {
			t.Fatalf("hard-refresh task-thread output must retain %s until shared link conversion", marker)
		}
	}
	for _, snippet := range []string{
		"var encodedTaskId = encodeURIComponent(taskId.trim())",
		"link.href = '/tasks/' + encodedTaskId",
		"if (inCode && !inToolOutput) continue", "(inToolOutput || inMarkdownFallback) && window.isInsideCode",
		"window.convertTaskLinksInMessage(bubble)",
		"window.convertTaskEditLinksInMessage(bubble)",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("direct-load/streaming task-result handling missing %q", snippet)
		}
	}
}

func TestSharedTaskResultLinkConversionExecutesForCreateAndEditMetadata(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the shared task-result converters")
	}

	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "window.normalizeTaskResultToolOutputTextNodes = function(messageElement)")
	end := strings.Index(content, "window.hideMixtureProgress = function(execId)")
	codeHelpers := renderedBaseMarkdownCodeHelpers(t)
	if start == -1 || end == -1 || end <= start {
		t.Fatal("shared task-result converter definitions are missing")
	}

	harness := `
const replacements = [];
global.NodeFilter = { SHOW_TEXT: 4 };
function element(tag) {
  return {
    tagName: tag.toUpperCase(), nodeName: tag.toUpperCase(), children: [], style: {},
    appendChild(child) { this.children.push(child); child.parentNode = this; return child; },
    closest() { return null; }
  };
}
global.document = {
  createTreeWalker(root) { let i = 0; return { nextNode() { return root.nodes[i++] || null; } }; },
  createDocumentFragment() { return element('fragment'); },
  createElement: element,
  createTextNode(text) { return { textContent: text }; }
};
function message(text, context) {
  const inCode = context === 'code' || context === 'tool-code';
  const inPre = context === 'pre' || context === 'tool-pre' || context === 'fallback-tool';
  const inTool = context === 'tool' || context === 'tool-code' || context === 'tool-pre' || context === 'fallback-tool';
  const inFallback = context === 'fallback' || context === 'fallback-tool';
  const parent = element(inCode ? 'code' : inPre ? 'pre' : inFallback ? 'span' : 'p');
  parent.closest = function(selector) {
    if (selector === 'code, pre' && (inCode || inPre)) return parent;
    if (selector === '.stream-tool-body-content' && inTool) return parent;
    if (selector === '[data-chat-markdown-fallback="true"]' && inFallback) return parent;
    return null;
  };
  parent.replaceChild = function(next, old) { replacements.push({ context, next }); };
  const textNode = { textContent: text, parentElement: parent, parentNode: parent };
  return { nodes: [textNode] };
}
function chunkedToolMessage(text) {
  const root = element('div');
  const pre = element('pre');
  const chunks = [text.substring(0, 64 * 1024), text.substring(64 * 1024)];
  root.nodes = chunks.map(function(chunk) { return { textContent: chunk, parentElement: pre, parentNode: pre }; });
  root.matches = function(selector) { return selector === '.stream-tool-body-content'; };
  root.querySelectorAll = function(selector) { return selector === 'pre' ? [pre] : []; };
  pre.childNodes = root.nodes;
  pre.closest = function(selector) {
    if (selector === 'code, pre' || selector === '.stream-tool-body-content') return pre;
    return null;
  };
  pre.normalize = function() {
    const merged = { textContent: pre.childNodes.map(function(child) { return child.textContent; }).join(''), parentElement: pre, parentNode: pre };
    pre.childNodes = [merged];
    root.nodes = [merged];
  };
  pre.replaceChild = function(next, old) { replacements.push({ context: 'chunked-tool', next }); };
  return { root, pre };
}
function descendants(root, tag, out = []) {
  if (!root) return out;
  if (root.tagName === tag) out.push(root);
  (root.children || []).forEach(child => descendants(child, tag, out));
  return out;
}
function anchors(root, out = []) { return descendants(root, 'A', out); }
function buttons(root, out = []) { return descendants(root, 'BUTTON', out); }
`
	script := "global.window = {};\n" + content[start:end] + codeHelpers + harness + `
window.convertTaskLinksInMessage(message('- "Created" (backlog) [TASK_ID:task/id?unsafe]', 'plain'));
window.convertTaskEditLinksInMessage(message('- "Edited" (updated: title) [TASK_EDITED:edit/id]', 'plain'));
const toolCreate = message('- "Tool-created" (active) [TASK_ID:tool/create]', 'tool-pre');
const toolEdit = message('- "Tool-edited" (updated: prompt) [TASK_EDITED:tool/edit]', 'tool-pre');
window.convertTaskLinksInMessage(toolCreate);
window.convertTaskEditLinksInMessage(toolEdit);
const inlineCreate = message('- "Inline example" (backlog) [TASK_ID:inline/create]', 'code');
const fencedEdit = message('- "Fence example" (updated: title) [TASK_EDITED:fence/edit]', 'pre');
const codedToolCreate = message('Example: \x60- "Code example" (backlog) [TASK_ID:example/create]\x60', 'tool-pre');
const codedToolEdit = message('\x60\x60\x60text\n- "Tool fence example" (updated: title) [TASK_EDITED:example/edit]\n\x60\x60\x60', 'tool-pre');
const multilineToolCreate = message('\x60- "Multiline create" (backlog)\n[TASK_ID:multiline/create]\x60', 'tool-pre');
const multilineToolEdit = message('\x60\x60- "Multiline edit" (updated: title)\n[TASK_EDITED:multiline/edit]\x60\x60', 'tool-pre');
const unmatchedThenCreate = message('Unmatched \x60\x60 prefix; \x60- "Later create" (backlog)\n[TASK_ID:later/create]\x60', 'tool-pre');
const unmatchedThenEdit = message('Unmatched \x60 prefix; \x60\x60- "Later edit" (updated: title)\n[TASK_EDITED:later/edit]\x60\x60', 'tool-pre');
const escapedCreate = message('Escaped \\\x60- "Escaped create" (backlog) [TASK_ID:escaped/create] escaped \\\x60', 'tool-pre');
const escapedEdit = message('Escaped \\\x60\x60- "Escaped edit" (updated: title) [TASK_EDITED:escaped/edit]\x60\x60', 'tool-pre');
const unicodeFenceCreate = message('\x60\x60\x60text\n\x60\x60\x60\u00a0\n- "Unicode create" (backlog) [TASK_ID:unicode/create]\n\x60\x60\x60', 'tool-pre');
const unicodeFenceEdit = message('~~~text\n~~~\u2003\n- "Unicode edit" (updated: title) [TASK_EDITED:unicode/edit]\n~~~', 'tool-pre');
const bareCRFenceCreate = message('\x60\x60\x60text\r- "Bare CR create" (backlog) [TASK_ID:bare-cr/create]\r\x60\x60\x60', 'tool-pre');
const bareCRFenceEdit = message('~~~~~text\r~~~\r- "Bare CR edit" (updated: title) [TASK_EDITED:bare-cr/edit]\r~~~~~~\t ', 'tool-pre');
const fallbackCreate = message('Inline \x60- "Fallback inline" (backlog) [TASK_ID:fallback/inline]\x60\nMultiline \x60- "Fallback multiline" (backlog)\n[TASK_ID:fallback/multiline]\x60\n\x60\x60\x60text\n- "Fallback fence" (backlog) [TASK_ID:fallback/fence]\n\x60\x60\x60\n~~~text\r- "Fallback bare CR" (backlog) [TASK_ID:fallback/bare-cr]\r~~~\r- "Fallback real" (backlog) [TASK_ID:fallback/real]', 'fallback');
const fallbackEdit = message('Inline \x60- "Fallback inline edit" (updated: title) [TASK_EDITED:fallback/inline-edit]\x60\nMultiline \x60\x60- "Fallback multiline edit" (updated: prompt)\n[TASK_EDITED:fallback/multiline-edit]\x60\x60\n~~~text\n- "Fallback fence edit" (updated: priority) [TASK_EDITED:fallback/fence-edit]\n~~~\n\x60\x60\x60\x60\x60text\r\x60\x60\x60\r- "Fallback bare CR edit" (updated: title) [TASK_EDITED:fallback/bare-cr-edit]\r\x60\x60\x60\x60\x60\r- "Fallback real edit" (updated: title) [TASK_EDITED:fallback/real-edit]', 'fallback');
const fallbackToolCreate = message('Example \x60- "Fallback tool coded" (backlog) [TASK_ID:fallback/tool-coded]\x60\n~~~text\r- "Fallback tool bare CR" (backlog) [TASK_ID:fallback/tool-bare-cr]\r~~~\r- "Fallback tool real" (active) [TASK_ID:fallback/tool-real]', 'fallback-tool');
const fallbackToolEdit = message('~~~text\n- "Fallback tool coded edit" (updated: title) [TASK_EDITED:fallback/tool-coded-edit]\n~~~\n\x60\x60\x60text\r- "Fallback tool bare CR edit" (updated: title) [TASK_EDITED:fallback/tool-bare-cr-edit]\r\x60\x60\x60\r- "Fallback tool real edit" (updated: prompt) [TASK_EDITED:fallback/tool-real-edit]', 'fallback-tool');
const sourceCreate = message('package chatcontrol\n\ncase "create_task":\n\treturn "source value"\n[TASK_ID:source/create]', 'tool-pre');
const sourceEdit = message('package chatcontrol\r\n\r\ncase "edit_task":\r\n\treturn "source value"\r\n[TASK_EDITED:source/edit]', 'tool-pre');
const blankCreate = message('- "Blank create" (backlog) [TASK_ID: \t]', 'tool-pre');
const blankEdit = message('- "Blank edit" (updated: title) [TASK_EDITED: \t]', 'tool-pre');
window.convertTaskLinksInMessage(inlineCreate);
window.convertTaskEditLinksInMessage(fencedEdit);
window.convertTaskLinksInMessage(codedToolCreate);
window.convertTaskEditLinksInMessage(codedToolEdit);
window.convertTaskLinksInMessage(multilineToolCreate);
window.convertTaskEditLinksInMessage(multilineToolEdit);
window.convertTaskLinksInMessage(unmatchedThenCreate);
window.convertTaskEditLinksInMessage(unmatchedThenEdit);
window.convertTaskLinksInMessage(escapedCreate);
window.convertTaskEditLinksInMessage(escapedEdit);
window.convertTaskLinksInMessage(unicodeFenceCreate);
window.convertTaskEditLinksInMessage(unicodeFenceEdit);
window.convertTaskLinksInMessage(bareCRFenceCreate);
window.convertTaskEditLinksInMessage(bareCRFenceEdit);
window.convertTaskLinksInMessage(fallbackCreate);
window.convertTaskEditLinksInMessage(fallbackEdit);
window.convertTaskLinksInMessage(fallbackToolCreate);
window.convertTaskEditLinksInMessage(fallbackToolEdit);
window.convertTaskLinksInMessage(sourceCreate);
window.convertTaskEditLinksInMessage(sourceEdit);
window.convertTaskLinksInMessage(blankCreate);
window.convertTaskEditLinksInMessage(blankEdit);
if (replacements.length !== 10) { console.error('replacements', replacements.length, replacements.map(item => anchors(item.next).map(link => link.href).join(','))); process.exit(1); }
if (inlineCreate.nodes[0].textContent !== '- "Inline example" (backlog) [TASK_ID:inline/create]' || fencedEdit.nodes[0].textContent !== '- "Fence example" (updated: title) [TASK_EDITED:fence/edit]' || codedToolCreate.nodes[0].textContent.indexOf('[TASK_ID:example/create]') === -1 || codedToolEdit.nodes[0].textContent.indexOf('[TASK_EDITED:example/edit]') === -1 || multilineToolCreate.nodes[0].textContent.indexOf('[TASK_ID:multiline/create]') === -1 || multilineToolEdit.nodes[0].textContent.indexOf('[TASK_EDITED:multiline/edit]') === -1 || unmatchedThenCreate.nodes[0].textContent.indexOf('[TASK_ID:later/create]') === -1 || unmatchedThenEdit.nodes[0].textContent.indexOf('[TASK_EDITED:later/edit]') === -1 || unicodeFenceCreate.nodes[0].textContent.indexOf('[TASK_ID:unicode/create]') === -1 || unicodeFenceEdit.nodes[0].textContent.indexOf('[TASK_EDITED:unicode/edit]') === -1 || bareCRFenceCreate.nodes[0].textContent.indexOf('[TASK_ID:bare-cr/create]') === -1 || bareCRFenceEdit.nodes[0].textContent.indexOf('[TASK_EDITED:bare-cr/edit]') === -1) { console.error('coded metadata changed'); process.exit(2); }
const fallbackCreateOutput = replacements[6];
const fallbackEditOutput = replacements[7];
if (anchors(fallbackCreateOutput.next).length !== 1 || anchors(fallbackCreateOutput.next)[0].href !== '/tasks/fallback%2Freal' || buttons(fallbackCreateOutput.next).length !== 1) { console.error('fallback create hydration', fallbackCreateOutput); process.exit(9); }
if (anchors(fallbackEditOutput.next).length !== 1 || anchors(fallbackEditOutput.next)[0].href !== '/tasks/fallback%2Freal-edit') { console.error('fallback edit hydration', fallbackEditOutput); process.exit(10); }
if (fallbackCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/inline]') === -1 || fallbackCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/multiline]') === -1 || fallbackCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/fence]') === -1 || fallbackCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/bare-cr]') === -1 || fallbackEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/inline-edit]') === -1 || fallbackEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/multiline-edit]') === -1 || fallbackEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/fence-edit]') === -1 || fallbackEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/bare-cr-edit]') === -1 || fallbackToolCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/tool-bare-cr]') === -1 || fallbackToolEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/tool-bare-cr-edit]') === -1) { console.error('fallback coded metadata changed'); process.exit(11); }
if (toolCreate.nodes[0].textContent.indexOf('[TASK_ID:tool/create]') === -1 || toolEdit.nodes[0].textContent.indexOf('[TASK_EDITED:tool/edit]') === -1 || fallbackToolCreate.nodes[0].textContent.indexOf('[TASK_ID:fallback/tool-real]') === -1 || fallbackToolEdit.nodes[0].textContent.indexOf('[TASK_EDITED:fallback/tool-real-edit]') === -1) { console.error('preformatted tool metadata changed'); process.exit(12); }
const links = replacements.flatMap(item => anchors(item.next));
if (links.length !== 10) { console.error('links', links.length); process.exit(3); }
if (links[0].href !== '/tasks/task%2Fid%3Funsafe' || links[0].textContent !== 'Created') { console.error(links[0]); process.exit(4); }
if (links[1].href !== '/tasks/edit%2Fid' || links[1].textContent !== '"Edited"') { console.error(links[1]); process.exit(5); }
if (links[2].href !== '/tasks/tool%2Fcreate' || links[2].textContent !== 'Tool-created' || links[2].textContent.includes('\n')) { console.error('tool create', links[2]); process.exit(13); }
if (links[3].href !== '/tasks/tool%2Fedit' || links[3].textContent !== '"Tool-edited"' || links[3].textContent.includes('\n')) { console.error('tool edit', links[3]); process.exit(14); }
if (links[4].href !== '/tasks/escaped%2Fcreate' || links[5].href !== '/tasks/escaped%2Fedit') { console.error('escaped real links', links[4], links[5]); process.exit(15); }
if (links[8].href !== '/tasks/fallback%2Ftool-real' || links[9].href !== '/tasks/fallback%2Ftool-real-edit') { console.error('fallback tool links', links[8], links[9]); process.exit(17); }
if (anchors(sourceCreate).length || anchors(sourceEdit).length || anchors(blankCreate).length || anchors(blankEdit).length) { console.error('invalid source or blank marker linked'); process.exit(16); }
const chunkSize = 64 * 1024;
const crossingCreate = chunkedToolMessage('x'.repeat(chunkSize - 18) + '\n- "Boundary create" (backlog) [TASK_ID:chunk/create]');
const crossingEdit = chunkedToolMessage('y'.repeat(chunkSize - 18) + '\n- "Boundary edit" (updated: title) [TASK_EDITED:chunk/edit]');
const codedInline = chunkedToolMessage('z'.repeat(chunkSize - 8) + '\x60coded\n- "Coded create" (backlog) [TASK_ID:chunk/coded-create]\n\x60');
const codedFence = chunkedToolMessage('q'.repeat(chunkSize - 16) + '\n\x60\x60\x60text\nfence lead\n- "Coded edit" (updated: title) [TASK_EDITED:chunk/coded-edit]\n\x60\x60\x60');
const chunkedReplacementStart = replacements.length;
window.convertTaskLinksInMessage(crossingCreate.root);
window.convertTaskEditLinksInMessage(crossingEdit.root);
window.convertTaskLinksInMessage(codedInline.root);
window.convertTaskEditLinksInMessage(codedFence.root);
const chunkedReplacements = replacements.slice(chunkedReplacementStart);
if (chunkedReplacements.length !== 2 || anchors(chunkedReplacements[0].next)[0].href !== '/tasks/chunk%2Fcreate' || anchors(chunkedReplacements[1].next)[0].href !== '/tasks/chunk%2Fedit') { console.error('chunk-boundary links', chunkedReplacements); process.exit(18); }
if (codedInline.pre.childNodes.length !== 1 || codedFence.pre.childNodes.length !== 1 || anchors(codedInline.root).length || anchors(codedFence.root).length) { console.error('cross-chunk coded metadata converted'); process.exit(19); }
const createButtons = buttons(replacements[0].next);
if (createButtons.length !== 1 || createButtons[0].className.indexOf('ov-task-result-start-btn') === -1) { console.error('buttons', createButtons); process.exit(8); }
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("shared task-result converters failed in browser harness: %v\n%s", err, output)
	}
}

func TestSharedTaskResultLinkConversionGeneratedParity(t *testing.T) {
	generated, err := os.ReadFile("chat_shared_templ.go")
	if err != nil {
		t.Fatalf("read generated shared template: %v", err)
	}
	content := string(generated)
	for _, snippet := range []string{
		"window.normalizeTaskResultToolOutputTextNodes = function(messageElement)",
		"var selector = messageElement.matches && messageElement.matches('.stream-tool-body-content')",
		"messageElement.querySelectorAll(selector).forEach(function(pre)",
		"if (pre.childNodes && pre.childNodes.length > 1 && pre.normalize) pre.normalize()",
		"window.convertTaskLinksInMessage = function(messageElement)",
		"window.convertTaskEditLinksInMessage = function(messageElement)",
		"var encodedTaskId = encodeURIComponent(taskId.trim())",
		"if (inCode && !inToolOutput) continue",
		`textNode.parentElement.closest('[data-chat-markdown-fallback=\"true\"]')`,
		"(inToolOutput || inMarkdownFallback) && window.isInsideCode",
		"if (!m[3] || !m[3].trim()) continue;",
		`container.closest('[data-execution-pair=\"true\"][data-exec-id]')`,
		`text.match(/\\r\\n|\\r|\\n/g)`,
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("generated shared template missing task-result conversion snippet %q", snippet)
		}
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
	taskIDRegex := regexp.MustCompile(`(?:-[ \t]*)?"([^"\r\n]+)"[ \t]*(?:\(([^)\r\n]+)\)[ \t]*)?\[TASK_ID:([^\]\r\n]+)\]`)
	taskEditRegex := regexp.MustCompile(`(?:-[ \t]*)?"([^"\r\n]+)"[ \t]*\(updated:[ \t]*([^)\r\n]+)\)[ \t]*\[TASK_EDITED:([^\]\r\n]+)\]`)
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
		{
			name:        "create marker on later source line",
			input:       "case \"create_task\":\n\treturn \"source value\"\n[TASK_ID:source-task]",
			regex:       taskIDRegex,
			expectMatch: false,
		},
		{
			name:        "edit marker on later source line",
			input:       "case \"edit_task\":\r\n\treturn \"source value\"\r\n[TASK_EDITED:source-task]",
			regex:       taskEditRegex,
			expectMatch: false,
		},
		{
			name:        "create title crosses line",
			input:       "- \"Crossed\nline\" (backlog) [TASK_ID:crossed]",
			regex:       taskIDRegex,
			expectMatch: false,
		},
		{
			name:        "edit fields cross line",
			input:       "- \"Edited\" (updated: title\npriority) [TASK_EDITED:crossed]",
			regex:       taskEditRegex,
			expectMatch: false,
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
	content := "Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n\n" +
		"---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:abc123]"

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
	if !strings.Contains(html, "Coded") || !strings.Contains(html, "[TASK_ID:coded]") {
		t.Error("ChatBubble should preserve coded task summaries for direct-load hydration")
	}

	if strings.Contains(html, "<script") || strings.Contains(html, "convertTaskLinksInMessage") || strings.Contains(html, "classList.add('chat-markdown')") {
		t.Error("ChatBubble should leave task-link conversion and Markdown fallback to the shared hydrator")
	}

	var script bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &script); err != nil {
		t.Fatalf("render shared chat script: %v", err)
	}
	scriptHTML := script.String()
	if !strings.Contains(scriptHTML, "window.convertTaskLinksInMessage(bubble)") {
		t.Error("shared assistant hydrator should convert task link markers after rendering raw bubbles")
	}
	// Keep the chat-stream-content class even in raw-bubble markdown fallback so refresh cleanup
	// can reliably find and re-process the container.
	if !strings.Contains(scriptHTML, "classList.add('chat-markdown')") {
		t.Error("shared Markdown fallback should add chat-markdown class without removing existing classes")
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

// TestCleanTranscriptControls_StripsProtocolArtifacts verifies that cleanTranscriptControls
// strips multi_tool_use.parallel protocol artifact lines from text.
func TestCleanTranscriptControls_StripsProtocolArtifacts(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()

	// cleanTranscriptControls must include the multi_tool_use protocol-artifact pattern
	if !strings.Contains(content, "multi_tool_use\\.\\S+") {
		t.Error("cleanTranscriptControls should strip multi_tool_use protocol artifact lines")
	}
}

func TestCleanTranscriptControls_PreservesCodeExamples(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	start := strings.Index(content, "window.replaceOutsideCode = function")
	end := strings.Index(content, "// Apply transcript control-artifact cleaning")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("rendered script must define transcript normalization and cleaning helpers")
	}
	codeHelpers := renderedBaseMarkdownCodeHelpers(t)
	if strings.Count(content, "window.codeRanges(textBuffer)") != 2 {
		t.Fatal("streaming renderer must synchronously calculate code ranges only for small inputs")
	}
	if strings.Contains(content, "result !== null ? result : (window.codeRanges") {
		t.Fatal("streaming renderer must not repeat failed large worker scans on the UI thread")
	}
	if strings.Count(content, "window.isInsideCodeRanges(codeRanges, match.index, match.index + match[0].length)") != 4 {
		t.Fatal("streaming renderer must not convert inline or fenced-code thinking/tool use/result examples into control cards")
	}
	if strings.Count(content, "textNode.parentElement.closest('code, pre')") != 2 {
		t.Fatal("both DOM transcript-cleaning paths must preserve text inside rendered code elements")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the rendered browser cleaner")
	}
	input := "Actual [STATUS: SUCCESS]; examples: `[STATUS: FAILED | reason]`, `[Thinking]inline thought[/Thinking]`, `[Thinking]unclosed inline`, `[Using tool: bash]`, `[Tool bash done: output]`, and `[Tool bash error: failed]`; fenced:\n```text\n[Thinking]\nfenced thought\n[/Thinking]\n[Using tool: read_file]\n[Tool read_file done: contents]\n[Tool bash error]\nfailed\n[/Tool]\n```\ntilde fenced:\n~~~log\n[Thinking]\ntilde thought\n[/Thinking]\n[Using tool: grep_search]\n[Tool grep_search done: match]\n~~~\n[Thinking]\nreal internal text\n[/Thinking]\nactual [Using tool: bash] and [Tool bash done: actual output].\n[STATUS: FAILED | actual failure]"
	want := "Actual [STATUS: SUCCESS]; examples: `[STATUS: FAILED | reason]`, `[Thinking]inline thought[/Thinking]`, `[Thinking]unclosed inline`, `[Using tool: bash]`, `[Tool bash done: output]`, and `[Tool bash error: failed]`; fenced:\n```text\n[Thinking]\nfenced thought\n[/Thinking]\n[Using tool: read_file]\n[Tool read_file done: contents]\n[Tool bash error]\nfailed\n[/Tool]\n```\ntilde fenced:\n~~~log\n[Thinking]\ntilde thought\n[/Thinking]\n[Using tool: grep_search]\n[Tool grep_search done: match]\n~~~\nactual  and ."
	codedAlias := `[Using tool: bash"> <parameter name="command">echo coded</parameter> </invoke>`
	aliasInput := "Inline `\u003cthinking\u003ecoded\u003c/thinking\u003e` and `" + codedAlias + "`.\n" +
		"```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n" + codedAlias + "\n```\n" +
		"~~~text\n\u003cthinking\u003etilde\u003c/thinking\u003e\n" + codedAlias + "\n~~~\n" +
		"\u003cthinking\u003ereal internal\u003c/thinking\u003e\nVisible answer.\n" +
		`[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`
	overlapProtectedAlias := "`[Using tool: bash\"> <parameter name=\"command\">coded`"
	overlapAliasInput := "Literal " + overlapProtectedAlias + " before real alias.\n" +
		`[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`
	multilineAlias := "`<thinking>coded\nthought</thinking>`"
	multilineToolAlias := "``[Using tool: bash\">\n<parameter name=\"command\">echo coded</parameter>\n</invoke>``"
	multilineControls := "`[Thinking]coded\n[/Thinking]\n[Using tool: bash]\n[Tool bash done: output]\n[TASK_ID:coded/create]\n[TASK_EDITED:coded/edit]`"
	multilineCleanedWant := multilineControls + "\n\n[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer."
	multilineOverlapAlias := "`[Using tool: bash\">\n<parameter name=\"command\">partial`"
	multilineOverlapInput := multilineOverlapAlias + "\n" + `[Using tool: bash">` + "\n" + `<parameter name="command">echo real overlap</parameter>` + "\n" + `</invoke>`
	multilineCreatedSummary := "Intro.\n\n---\nCreated 1 task(s):\n- \"Old\" (backlog)\n\nExample `literal\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\nend`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	multilineCreatedWant := "Intro.\n\nExample `literal\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\nend`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	multilineEditedSummary := "Intro.\n\n---\nEdited 1 task(s):\n- \"Old\" (updated: title)\n\nExample ``literal\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\nend``\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]"
	multilineEditedWant := "Intro.\n\nExample ``literal\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:coded]\nend``\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]"
	unmatchedCodedControls := "Unmatched `` prefix; `<thinking>coded\nthought</thinking>\n[Thinking]coded[/Thinking]\n[Using tool: bash]\n[Tool bash done: coded]\n[STATUS: FAILED | coded]\n[TASK_ID:coded/create]\n[TASK_EDITED:coded/edit]`"
	unmatchedAliasInput := unmatchedCodedControls + "\n<thinking>real</thinking>"
	unmatchedSummary := "Intro.\n\n---\nCreated 1 task(s):\n- \"Old\" (backlog)\n\nUnmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	unmatchedSummaryWant := "Intro.\n\nUnmatched `` prefix; `Example\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]`\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	escapedAliasInput := `Escaped \` + "`" + `<thinking>escaped real</thinking> escaped \` + "`" + "\n" +
		"``<thinking>coded</thinking>``\n<thinking>later real</thinking>"
	escapedControls := `Escaped \` + "`" + `[Thinking]escaped real[/Thinking] escaped \` + "`" + "\n" +
		"``[Thinking]coded[/Thinking]\n[Using tool: bash]\n[TASK_ID:coded]``\n[Using tool: bash]\nVisible answer.\n[STATUS: SUCCESS]"
	escapedSummary := `Example \` + "`" + "\n---\nCreated 1 task(s):\n- \"Old\" (backlog) [TASK_ID:old]\n" + `\` + "`" +
		"\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	escapedDOMInput := `Escaped \` + "`" + `[Thinking]real[/Thinking] escaped \` + "`" + ` Visible DOM answer.`
	escapedDOMWant := `Escaped \` + "`" + ` escaped \` + "`" + ` Visible DOM answer.`
	unicodeFenceControls := "`````text\n<thinking>coded alias</thinking>\n`````\u00a0\n[Thinking]coded thought[/Thinking]\n[Using tool: bash]\n[Tool bash done: coded]\n[STATUS: FAILED | coded]\n[TASK_ID:unicode/create]\n[TASK_EDITED:unicode/edit]\n`````"
	unicodeFenceCleanWant := unicodeFenceControls + "\n\n[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer."
	unicodeFenceSummary := "Intro.\n\n---\nEdited 1 task(s):\n- \"Old\" (updated: title)\n\n~~~text\n~~~\u202f\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:unicode/edit]\n~~~\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]"
	unicodeFenceSummaryWant := "Intro.\n\n~~~text\n~~~\u202f\n---\nEdited 1 task(s):\n- \"Coded\" (updated: title) [TASK_EDITED:unicode/edit]\n~~~\n\n---\nEdited 1 task(s):\n- \"Real\" (updated: title) [TASK_EDITED:real]"
	validFenceCloser := "```text\n[Thinking]coded[/Thinking]\n``` \t\n[Thinking]real[/Thinking]\nVisible answer.\n[STATUS: SUCCESS]"
	validFenceCloserWant := "```text\n[Thinking]coded[/Thinking]\n``` \t\nVisible answer."
	bareCRFenceControls := "`````text\r<thinking>coded alias</thinking>\r```\r[Thinking]coded thought[/Thinking]\r[Using tool: bash]\r[Tool bash done: coded]\r[STATUS: FAILED | coded]\r[TASK_ID:bare-cr/create]\r[TASK_EDITED:bare-cr/edit]\r``````\t "
	bareCRFenceSummary := "Intro.\n\n---\nCreated 1 task(s):\n- \"Old\" (backlog)\n\n~~~text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:bare-cr/edit]\r~~~\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	bareCRFenceSummaryWant := "Intro.\n\n~~~text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:bare-cr/edit]\r~~~\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	bareCRDuplicateSummary := "Intro.\r\r---\rCreated 1 task(s):\r- \"Old\" (backlog)\r\rExample:\r~~~text\r---\rCreated 1 task(s):\r- \"Coded\" (backlog) [TASK_ID:coded]\r~~~\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]"
	bareCRDuplicateSummaryWant := "Intro.\r\rExample:\r~~~text\r---\rCreated 1 task(s):\r- \"Coded\" (backlog) [TASK_ID:coded]\r~~~\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]"
	bareCREditedSummary := "Intro.\r\r---\rEdited 1 task(s):\r- \"Old\" (updated: title)\r\rExample:\r`````text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r```\r``````\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]"
	bareCREditedSummaryWant := "Intro.\r\rExample:\r`````text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r```\r``````\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]"
	literalToolOutput := "const first = `inline code`;\nconst second = `more inline code`;\nconst fixture = '[Using tool: fake]\\n[Tool fake done]\\ninside\\n[/Tool]';\nconst thinking = '<thinking>literal source</thinking>';\n- \"Valid task\" (backlog) [TASK_ID:fixture]"
	literalToolTranscript := "[Using tool: read_file | fixture.go]\n[Tool read_file done]\n" + literalToolOutput + "\n[/Tool]"
	bubbleStart := strings.Index(content, "window.cleanBubbleContent = function")
	bubbleEnd := strings.Index(content, "// Clean all assistant messages")
	if bubbleStart == -1 || bubbleEnd == -1 || bubbleEnd <= bubbleStart {
		t.Fatal("rendered script must define DOM transcript cleaning")
	}
	bubbleCleaner := content[bubbleStart:bubbleEnd]
	rendererStart := strings.Index(content, "// Persistent stores survive streaming rerenders")
	rendererEnd := strings.Index(content, "// window.codeRanges and window.isInsideCode are installed by the base layout")
	if rendererStart == -1 || rendererEnd == -1 || rendererEnd <= rendererStart {
		t.Fatal("rendered script must define shared streaming rendering")
	}
	renderer := content[rendererStart:rendererEnd]
	script := "global.window = { addEventListener: function() {}, innerWidth: 1280, innerHeight: 900, devicePixelRatio: 1, getComputedStyle: function() { return { lineHeight: '14px', fontFamily: 'monospace', fontSize: '12px', fontWeight: '400', letterSpacing: 'normal' }; } };\n" +
		"global.NodeFilter = { SHOW_TEXT: 4 };\n" +
		"global.Blob = function() {}; window.URL = { createObjectURL: function() { return 'blob:test'; } }; global.Worker = function() {}; Worker.prototype.terminate = function() {}; Worker.prototype.postMessage = function(value) { var self = this; setTimeout(function() { self.onmessage({ data: { ranges: window.codeRanges(value) } }); }, 0); };\n" +
		"function element(tag) { return { tagName: tag, className: '', textContent: '', children: [], replaceCount: 0, style: {}, clientHeight: 416, classList: { add: function() {}, remove: function() {} }, addEventListener: function() {}, appendChild: function(child) { this.children.push(child); child.parentNode = this; return child; }, removeChild: function(child) { this.children = this.children.filter(function(candidate) { return candidate !== child; }); }, remove: function() {}, getBoundingClientRect: function() { return { height: 28 }; }, replaceChildren: function(fragment) { this.replaceCount++; this.children = (fragment && fragment.children ? fragment.children : []).slice(); }, querySelectorAll: function() { return []; }, getAttribute: function() { return null; }, setAttribute: function() {}, closest: function() { return null; } }; }\n" +
		"global.document = { documentElement: { getAttribute: function() { return ''; }, className: '' }, body: { className: '', getAttribute: function() { return ''; }, appendChild: function() {} }, createTreeWalker: function(element) { let i = 0; return { nextNode: function() { return element.nodes[i++] || null; } }; }, createElement: element, createDocumentFragment: function() { return element('fragment'); }, createTextNode: function(text) { return { textContent: text }; } };\n" +
		codeHelpers + "\n" + content[start:end] + "\n" + renderer + "\n" + bubbleCleaner + "\n" +
		"const got = window.cleanTranscriptControls(" + strconv.Quote(input) + ", true);\n" +
		"if (got !== " + strconv.Quote(want) + ") { console.error(JSON.stringify(got)); process.exit(1); }\n" +
		"const followed = window.cleanTranscriptControls('[STATUS: SUCCESS]\\n[Thinking]\\nLater internal text\\n[/Thinking]', true);\n" +
		"if (followed !== '[STATUS: SUCCESS]') { console.error(JSON.stringify(followed)); process.exit(2); }\n" +
		"function textNode(text, inCode) { return { textContent: text, parentElement: { closest: function() { return inCode ? {} : null; } } }; }\n" +
		"const fencedNode = textNode('[Thinking]example[/Thinking]\\n[Using tool: bash]\\n[Tool bash done: output]', true);\n" +
		"const realNode = textNode('[Thinking]real[/Thinking]\\nVisible answer.\\n[Using tool: bash]', false);\n" +
		"const div = { nodes: [fencedNode, realNode] };\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [div]; } });\n" +
		"if (fencedNode.textContent !== '[Thinking]example[/Thinking]\\n[Using tool: bash]\\n[Tool bash done: output]' || realNode.textContent !== 'Visible answer.') { console.error(JSON.stringify({ fenced: fencedNode.textContent, real: realNode.textContent })); process.exit(3); }\n" +
		"const stream = element('div'); stream.id = 'streaming-test';\n" +
		"window.renderStreamingContent(stream, '```text\\n[Thinking]\\nfenced thought\\n[/Thinking]\\n[Using tool: bash]\\n[Tool bash done: output]\\n```');\n" +
		"if (stream.replaceCount !== 1 || stream.children.length !== 1 || stream.children[0].className.indexOf('chat-markdown') === -1 || stream.children[0].textContent !== '```text\\n[Thinking]\\nfenced thought\\n[/Thinking]\\n[Using tool: bash]\\n[Tool bash done: output]\\n```') process.exit(4);\n" +
		"const realThinking = element('div'); realThinking.id = 'streaming-real-thinking';\n" +
		"window.renderStreamingContent(realThinking, '[Thinking]real internal text[/Thinking]\\nVisible answer.');\n" +
		"if (realThinking.children.length !== 2 || realThinking.children[0].className.indexOf('stream-thinking') === -1) process.exit(5);\n" +
		"const mixedThinking = element('div'); mixedThinking.id = 'streaming-mixed-thinking';\n" +
		"window.renderStreamingContent(mixedThinking, 'Literal `[Thinking]example`.\\n[Thinking]real internal text[/Thinking]\\nVisible answer.');\n" +
		"if (mixedThinking.children.length !== 3 || mixedThinking.children[0].textContent.indexOf('`[Thinking]example`') === -1 || mixedThinking.children[1].className.indexOf('stream-thinking') === -1) process.exit(6);\n" +
		"const fencedStatus = element('div'); fencedStatus.id = 'streaming-fenced-status';\n" +
		"window.renderStreamingContent(fencedStatus, '`````text\\n```\\n[STATUS: FAILED | still fenced]');\n" +
		"if (fencedStatus.children.length !== 1 || fencedStatus.children[0].textContent.indexOf('[STATUS: FAILED | still fenced]') === -1) process.exit(7);\n" +
		"const realStatus = element('div'); realStatus.id = 'streaming-real-status';\n" +
		"window.renderStreamingContent(realStatus, '`````text\\n```\\n`````\\n[STATUS: FAILED | real failure]');\n" +
		"if (realStatus.children.length !== 1 || realStatus.children[0].textContent.indexOf('[STATUS: FAILED | real failure]') !== -1) process.exit(8);\n" +
		"const codedSummary = 'Example:\\n```text\\n---\\nCreated 1 task(s):\\n- \\\"Coded\\\" (backlog) [TASK_ID:coded]\\n```\\n\\n---\\nCreated 1 task(s):\\n- \\\"Real\\\" (backlog) [TASK_ID:real]';\n" +
		"if (window.cleanTranscriptControls(codedSummary, false) !== codedSummary) process.exit(9);\n" +
		"const codedEditSummary = 'Example: `Edited 1 task(s): [TASK_EDITED:inline]`.\\n~~~text\\n---\\nEdited 1 task(s):\\n- \\\"Coded\\\" (updated: title) [TASK_EDITED:coded]\\n~~~\\n\\n---\\nEdited 1 task(s):\\n- \\\"Real\\\" (updated: title) [TASK_EDITED:real]';\n" +
		"if (window.cleanTranscriptControls(codedEditSummary, false) !== codedEditSummary) process.exit(10);\n" +
		"const duplicateSummary = 'Intro.\\n\\n---\\nEdited 1 task(s):\\n- \\\"Old\\\" (updated: title)\\n\\n---\\nEdited 1 task(s):\\n- \\\"Real\\\" (updated: title) [TASK_EDITED:real]';\n" +
		"const dedupedSummary = 'Intro.\\n\\n---\\nEdited 1 task(s):\\n- \\\"Real\\\" (updated: title) [TASK_EDITED:real]';\n" +
		"if (window.cleanTranscriptControls(duplicateSummary, false) !== dedupedSummary) process.exit(11);\n" +
		"const mixedSummary = 'Intro.\\n\\n---\\nCreated 1 task(s):\\n- \\\"Old\\\" (backlog)\\n\\nLiteral example:\\n```text\\n---\\nCreated 1 task(s):\\n- \\\"Coded\\\" (backlog) [TASK_ID:coded]\\n```\\n\\n---\\nCreated 1 task(s):\\n- \\\"Real\\\" (backlog) [TASK_ID:real]';\n" +
		"const mixedWant = 'Intro.\\n\\nLiteral example:\\n```text\\n---\\nCreated 1 task(s):\\n- \\\"Coded\\\" (backlog) [TASK_ID:coded]\\n```\\n\\n---\\nCreated 1 task(s):\\n- \\\"Real\\\" (backlog) [TASK_ID:real]';\n" +
		"if (window.cleanTranscriptControls(mixedSummary, false) !== mixedWant) process.exit(12);\n" +
		"const summaryStream = element('div'); summaryStream.id = 'streaming-coded-summary';\n" +
		"window.renderStreamingContent(summaryStream, codedSummary);\n" +
		"if (summaryStream.children.length !== 1 || summaryStream.children[0].textContent.indexOf('Coded') === -1 || summaryStream.children[0].textContent.indexOf('Real') === -1) process.exit(13);\n" +
		"const codedSummaryNode = textNode('---\\nCreated 1 task(s):\\n- \\\"Coded\\\" (backlog) [TASK_ID:coded]', true);\n" +
		"const duplicateSummaryNode = textNode(duplicateSummary, false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [codedSummaryNode, duplicateSummaryNode] }]; } });\n" +
		"if (codedSummaryNode.textContent.indexOf('Coded') === -1 || duplicateSummaryNode.textContent !== dedupedSummary) process.exit(14);\n" +
		"const aliasInput = " + strconv.Quote(aliasInput) + ";\n" +
		"const normalizedAliases = window.normalizeTranscriptMarkers(aliasInput);\n" +
		"if (normalizedAliases.indexOf('`\u003cthinking\u003ecoded\u003c/thinking\u003e`') === -1 || normalizedAliases.indexOf(" + strconv.Quote("`"+codedAlias+"`") + ") === -1 || normalizedAliases.indexOf('[Thinking]\\nreal internal\\n[/Thinking]') === -1 || normalizedAliases.indexOf('[Using tool: bash | echo real]') === -1) { console.error(JSON.stringify(normalizedAliases)); process.exit(15); }\n" +
		"const cleanedAliases = window.cleanTranscriptControls(aliasInput, true);\n" +
		"if (cleanedAliases.indexOf('`\u003cthinking\u003ecoded\u003c/thinking\u003e`') === -1 || cleanedAliases.indexOf(" + strconv.Quote("```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n"+codedAlias+"\n```") + ") === -1 || cleanedAliases.indexOf('real internal') !== -1 || cleanedAliases.indexOf('echo real') !== -1 || cleanedAliases.indexOf('Visible answer.') === -1) { console.error(JSON.stringify(cleanedAliases)); process.exit(16); }\n" +
		"const aliasStream = element('div'); aliasStream.id = 'streaming-coded-alias';\n" +
		"const fencedAlias = " + strconv.Quote("```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n"+codedAlias+"\n```") + ";\n" +
		"window.renderStreamingContent(aliasStream, fencedAlias);\n" +
		"if (aliasStream.children.length !== 1 || aliasStream.children[0].className.indexOf('chat-markdown') === -1 || aliasStream.children[0].textContent !== fencedAlias) process.exit(17);\n" +
		"const realAliasStream = element('div'); realAliasStream.id = 'streaming-real-alias';\n" +
		"window.renderStreamingContent(realAliasStream, '\u003cthinking\u003ereal internal\u003c/thinking\u003e\\nVisible answer.');\n" +
		"if (realAliasStream.children.length !== 2 || realAliasStream.children[0].className.indexOf('stream-thinking') === -1) process.exit(18);\n" +
		"const realToolAliasStream = element('div'); realToolAliasStream.id = 'streaming-real-tool-alias';\n" +
		"window.renderStreamingContent(realToolAliasStream, " + strconv.Quote(`[Using tool: bash">`+"\n"+`<parameter name="command">echo real</parameter>`+"\n"+`</invoke>`) + ");\n" +
		"if (realToolAliasStream.children.length !== 1 || realToolAliasStream.children[0].className.indexOf('stream-tool') === -1) process.exit(19);\n" +
		"const codedAliasNode = textNode('\u003cthinking\u003ecoded\u003c/thinking\u003e\\n' + " + strconv.Quote(codedAlias) + ", true);\n" +
		"const realAliasNode = textNode('\u003cthinking\u003ereal\u003c/thinking\u003e\\nVisible DOM answer.', false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [codedAliasNode, realAliasNode] }]; } });\n" +
		"if (codedAliasNode.textContent !== '\u003cthinking\u003ecoded\u003c/thinking\u003e\\n' + " + strconv.Quote(codedAlias) + " || realAliasNode.textContent !== '\\n\\nVisible DOM answer.') { console.error(JSON.stringify({ coded: codedAliasNode.textContent, real: realAliasNode.textContent })); process.exit(20); }\n" +
		"const overlapAliases = window.normalizeTranscriptMarkers(" + strconv.Quote(overlapAliasInput) + ");\n" +
		"if (overlapAliases.indexOf(" + strconv.Quote(overlapProtectedAlias) + ") === -1 || overlapAliases.indexOf('[Using tool: bash | echo real]') === -1) { console.error(JSON.stringify(overlapAliases)); process.exit(21); }\n" +
		"const multilineAliases = window.normalizeTranscriptMarkers(" + strconv.Quote(multilineAlias+"\n"+multilineToolAlias+"\n<thinking>real</thinking>") + ");\n" +
		"if (multilineAliases.indexOf(" + strconv.Quote(multilineAlias) + ") === -1 || multilineAliases.indexOf(" + strconv.Quote(multilineToolAlias) + ") === -1 || multilineAliases.indexOf('[Thinking]\\nreal\\n[/Thinking]') === -1) { console.error(JSON.stringify(multilineAliases)); process.exit(22); }\n" +
		"const multilineCleaned = window.cleanTranscriptControls(" + strconv.Quote(multilineControls+"\n[Thinking]real[/Thinking]\n[Using tool: bash]\n[Tool bash done: actual]\n[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer.\n[STATUS: SUCCESS]") + ", true);\n" +
		"if (multilineCleaned !== " + strconv.Quote(multilineCleanedWant) + ") { console.error(JSON.stringify(multilineCleaned)); process.exit(23); }\n" +
		"const multilineStream = element('div'); multilineStream.id = 'streaming-multiline-inline';\n" +
		"window.renderStreamingContent(multilineStream, " + strconv.Quote(multilineControls) + ");\n" +
		"if (multilineStream.children.length !== 1 || multilineStream.children[0].className.indexOf('chat-markdown') === -1 || multilineStream.children[0].textContent !== " + strconv.Quote(multilineControls) + ") process.exit(24);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(multilineCreatedSummary) + ", false) !== " + strconv.Quote(multilineCreatedWant) + ") process.exit(25);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(multilineEditedSummary) + ", false) !== " + strconv.Quote(multilineEditedWant) + ") process.exit(26);\n" +
		"const multilineOverlap = window.normalizeTranscriptMarkers(" + strconv.Quote(multilineOverlapInput) + ");\n" +
		"if (multilineOverlap.indexOf(" + strconv.Quote(multilineOverlapAlias) + ") === -1 || multilineOverlap.indexOf('[Using tool: bash | echo real overlap]') === -1) { console.error(JSON.stringify(multilineOverlap)); process.exit(27); }\n" +
		"const multilineCodeNode = textNode('[Thinking]coded\\n[/Thinking]\\n[TASK_ID:coded]', true);\n" +
		"const realMultilineNode = textNode('[Thinking]real[/Thinking]\\nVisible DOM answer.', false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [multilineCodeNode, realMultilineNode] }]; } });\n" +
		"if (multilineCodeNode.textContent !== '[Thinking]coded\\n[/Thinking]\\n[TASK_ID:coded]' || realMultilineNode.textContent.indexOf('real') !== -1 || realMultilineNode.textContent.indexOf('Visible DOM answer.') === -1) process.exit(28);\n" +
		"const unmatchedNormalized = window.normalizeTranscriptMarkers(" + strconv.Quote(unmatchedAliasInput) + ");\n" +
		"if (unmatchedNormalized.indexOf(" + strconv.Quote(unmatchedCodedControls) + ") === -1 || unmatchedNormalized.indexOf('[Thinking]\\nreal\\n[/Thinking]') === -1) { console.error(JSON.stringify(unmatchedNormalized)); process.exit(29); }\n" +
		"const unmatchedCleaned = window.cleanTranscriptControls(" + strconv.Quote(unmatchedCodedControls+"\n[Thinking]real[/Thinking]\n[Using tool: bash]\n[Tool bash done: actual]\nVisible answer.\n[STATUS: SUCCESS]") + ", true);\n" +
		"if (unmatchedCleaned !== " + strconv.Quote(unmatchedCodedControls+"\n\nVisible answer.") + ") { console.error(JSON.stringify(unmatchedCleaned)); process.exit(30); }\n" + "const unmatchedStream = element('div'); unmatchedStream.id = 'streaming-unmatched-prefix';\n" +
		"window.renderStreamingContent(unmatchedStream, " + strconv.Quote(unmatchedCodedControls) + ");\n" +
		"if (unmatchedStream.children.length !== 1 || unmatchedStream.children[0].className.indexOf('chat-markdown') === -1 || unmatchedStream.children[0].textContent !== " + strconv.Quote(unmatchedCodedControls) + ") process.exit(31);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(unmatchedSummary) + ", false) !== " + strconv.Quote(unmatchedSummaryWant) + ") process.exit(32);\n" +
		"const escapedAliases = window.normalizeTranscriptMarkers(" + strconv.Quote(escapedAliasInput) + ");\n" +
		"if (escapedAliases.indexOf('<thinking>escaped real</thinking>') !== -1 || escapedAliases.indexOf('[Thinking]\\nescaped real\\n[/Thinking]') === -1 || escapedAliases.indexOf('[Thinking]\\nlater real\\n[/Thinking]') === -1 || escapedAliases.indexOf('``<thinking>coded</thinking>``') === -1) { console.error(JSON.stringify(escapedAliases)); process.exit(33); }\n" +
		"const escapedCleaned = window.cleanTranscriptControls(" + strconv.Quote(escapedControls) + ", true);\n" +
		"if (escapedCleaned.indexOf('escaped real') !== -1 || escapedCleaned.indexOf('``[Thinking]coded[/Thinking]\\n[Using tool: bash]\\n[TASK_ID:coded]``') === -1 || escapedCleaned.indexOf('[STATUS: SUCCESS]') !== -1 || escapedCleaned.indexOf('Visible answer.') === -1) { console.error(JSON.stringify(escapedCleaned)); process.exit(34); }\n" +
		"const escapedStream = element('div'); escapedStream.id = 'streaming-escaped-prefix';\n" +
		"window.renderStreamingContent(escapedStream, " + strconv.Quote(escapedControls) + ");\n" +
		"if (escapedStream.children.some(function(child) { return child.className.indexOf('stream-thinking') !== -1; }) || !escapedStream.children.some(function(child) { return child.textContent.indexOf('Escaped \\\\`[Thinking]escaped real[/Thinking]') !== -1; })) process.exit(35);\n" +
		"const escapedDOMNode = textNode(" + strconv.Quote(escapedDOMInput) + ", false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [escapedDOMNode] }]; } });\n" +
		"if (escapedDOMNode.textContent !== " + strconv.Quote(escapedDOMWant) + ") { console.error(JSON.stringify(escapedDOMNode.textContent)); process.exit(36); }\n" +
		"const escapedDedup = window.cleanTranscriptControls(" + strconv.Quote(escapedSummary) + ", false);\n" +
		"if (escapedDedup.indexOf('\\\"Old\\\"') !== -1 || escapedDedup.indexOf('\\\"Real\\\"') === -1 || (escapedDedup.match(/Created 1 task\\(s\\):/g) || []).length !== 1) { console.error(JSON.stringify(escapedDedup)); process.exit(37); }\n" +
		"const unicodeFenceNormalized = window.normalizeTranscriptMarkers(" + strconv.Quote(unicodeFenceControls) + ");\n" +
		"if (unicodeFenceNormalized !== " + strconv.Quote(unicodeFenceControls) + ") { console.error(JSON.stringify(unicodeFenceNormalized)); process.exit(38); }\n" +
		"const unicodeFenceCleaned = window.cleanTranscriptControls(" + strconv.Quote(unicodeFenceControls+"\n[Thinking]real[/Thinking]\n[Using tool: bash]\n[Tool bash done: actual]\n[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer.\n[STATUS: SUCCESS]") + ", true);\n" +
		"if (unicodeFenceCleaned !== " + strconv.Quote(unicodeFenceCleanWant) + ") { console.error(JSON.stringify(unicodeFenceCleaned)); process.exit(39); }\n" +
		"const unicodeFenceStream = element('div'); unicodeFenceStream.id = 'streaming-unicode-fence';\n" +
		"window.renderStreamingContent(unicodeFenceStream, " + strconv.Quote(unicodeFenceControls) + ");\n" +
		"if (unicodeFenceStream.children.length !== 1 || unicodeFenceStream.children[0].className.indexOf('chat-markdown') === -1 || unicodeFenceStream.children[0].textContent !== " + strconv.Quote(unicodeFenceControls) + ") process.exit(40);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(unicodeFenceSummary) + ", false) !== " + strconv.Quote(unicodeFenceSummaryWant) + ") process.exit(41);\n" +
		"const validFenceCleaned = window.cleanTranscriptControls(" + strconv.Quote(validFenceCloser) + ", true);\n" +
		"if (validFenceCleaned !== " + strconv.Quote(validFenceCloserWant) + ") { console.error(JSON.stringify(validFenceCleaned)); process.exit(42); }\n" +
		"const unicodeFenceCodeNode = textNode('[Thinking]coded[/Thinking]\\n[TASK_ID:unicode/create]\\n[TASK_EDITED:unicode/edit]', true);\n" +
		"const unicodeFenceRealNode = textNode('[Thinking]real[/Thinking]\\nVisible DOM answer.', false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [unicodeFenceCodeNode, unicodeFenceRealNode] }]; } });\n" +
		"if (unicodeFenceCodeNode.textContent.indexOf('[TASK_ID:unicode/create]') === -1 || unicodeFenceRealNode.textContent.indexOf('[Thinking]') !== -1 || unicodeFenceRealNode.textContent.indexOf('Visible DOM answer.') === -1) process.exit(43);\n" +
		"const bareCRNormalized = window.normalizeTranscriptMarkers(" + strconv.Quote(bareCRFenceControls+"\r<thinking>real alias</thinking>") + ");\n" +
		"if (bareCRNormalized.indexOf(" + strconv.Quote(bareCRFenceControls) + ") === -1 || bareCRNormalized.indexOf('[Thinking]\\nreal alias\\n[/Thinking]') === -1) { console.error(JSON.stringify(bareCRNormalized)); process.exit(44); }\n" +
		"const bareCRCleaned = window.cleanTranscriptControls(" + strconv.Quote(bareCRFenceControls+"\r[Thinking]real[/Thinking]\r[Using tool: bash]\r[Tool bash done]\ractual\r[/Tool]\r[TASK_ID:real/create]\r[TASK_EDITED:real/edit]\rVisible answer.\r[STATUS: SUCCESS]") + ", true);\n" +
		"if (bareCRCleaned.indexOf(" + strconv.Quote(bareCRFenceControls) + ") === -1 || bareCRCleaned.indexOf('actual') !== -1 || bareCRCleaned.indexOf('[TASK_ID:real/create]') === -1 || bareCRCleaned.indexOf('[TASK_EDITED:real/edit]') === -1 || bareCRCleaned.indexOf('[STATUS: SUCCESS]') !== -1 || bareCRCleaned.indexOf('Visible answer.') === -1) { console.error(JSON.stringify(bareCRCleaned)); process.exit(45); }\n" +
		"const bareCRStream = element('div'); bareCRStream.id = 'streaming-bare-cr-fence';\n" +
		"window.renderStreamingContent(bareCRStream, " + strconv.Quote(bareCRFenceControls) + ");\n" +
		"if (bareCRStream.children.length !== 1 || bareCRStream.children[0].className.indexOf('chat-markdown') === -1 || bareCRStream.children[0].textContent !== " + strconv.Quote(strings.TrimRight(bareCRFenceControls, " \t")) + ") { console.error(JSON.stringify(bareCRStream)); process.exit(46); }\n" +
		"const bareCRRealToolStream = element('div'); bareCRRealToolStream.id = 'streaming-bare-cr-real-tool';\n" +
		"window.renderStreamingContent(bareCRRealToolStream, " + strconv.Quote(bareCRFenceControls+"\r[Using tool: bash]\r[Tool bash done]\ractual\r[/Tool]") + ");\n" +
		"if (!bareCRRealToolStream.children.some(function(child) { return child.className.indexOf('stream-tool') !== -1; })) { console.error(JSON.stringify(bareCRRealToolStream)); process.exit(48); }\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(bareCRFenceSummary) + ", false) !== " + strconv.Quote(bareCRFenceSummaryWant) + ") process.exit(47);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(bareCRDuplicateSummary) + ", false) !== " + strconv.Quote(bareCRDuplicateSummaryWant) + ") process.exit(49);\n" +
		"if (window.cleanTranscriptControls(" + strconv.Quote(bareCREditedSummary) + ", false) !== " + strconv.Quote(bareCREditedSummaryWant) + ") process.exit(50);\n" +
		"if (window.cleanTranscriptControls('} to=multi_tool_use.parallel code something\\rNarrative continues.', false) !== 'Narrative continues.') process.exit(51);\n" +
		"const sameLineControls = 'Example: `[Tool grep_search done]coded[/Tool]`.\\n[Tool grep_search done]actual match[/Tool]\\n[Tool bash error]command failed[/Tool]\\nVisible answer.';\n" +
		"const sameLineCleaned = window.cleanTranscriptControls(sameLineControls, false);\n" +
		"if (sameLineCleaned !== 'Example: `[Tool grep_search done]coded[/Tool]`.\\nVisible answer.') { console.error(JSON.stringify(sameLineCleaned)); process.exit(52); }\n" +
		"const partialSameLine = window.cleanTranscriptControls('Partial `[Tool grep_search done]coded`.\\n[Tool grep_search done]actual[/Tool]\\nVisible answer.', false);\n" +
		"if (partialSameLine !== 'Partial `[Tool grep_search done]coded`.\\nVisible answer.') { console.error(JSON.stringify(partialSameLine)); process.exit(56); }\n" +
		"const sameLineStream = element('div'); sameLineStream.id = 'streaming-same-line-tool-result';\n" +
		"window.renderStreamingContent(sameLineStream, '[Using tool: grep_search]\\n[Tool grep_search done]actual match[/Tool]');\n" +
		"if (sameLineStream.children.length !== 1 || sameLineStream.children[0].className.indexOf('stream-tool') === -1) process.exit(53);\n" +
		"const codedSameLineStream = element('div'); codedSameLineStream.id = 'streaming-coded-same-line-tool-result';\n" +
		"window.renderStreamingContent(codedSameLineStream, '`[Tool grep_search done]coded[/Tool]`\\n[Using tool: grep_search]\\n[Tool grep_search done]actual[/Tool]');\n" +
		"if (!codedSameLineStream.children.some(function(child) { return child.textContent.indexOf('`[Tool grep_search done]coded[/Tool]`') !== -1; }) || !codedSameLineStream.children.some(function(child) { return child.className.indexOf('stream-tool') !== -1; })) process.exit(54);\n" +
		"const sameLineDOMNode = textNode('[Tool grep_search done]actual[/Tool]\\nVisible DOM answer.', false);\n" +
		"window.cleanBubbleContent({ querySelectorAll: function() { return [{ nodes: [sameLineDOMNode] }]; } });\n" +
		"if (sameLineDOMNode.textContent !== 'Visible DOM answer.') { console.error(JSON.stringify(sameLineDOMNode.textContent)); process.exit(55); }\n" +
		"const literalToolOutput = " + strconv.Quote(literalToolOutput) + ";\n" +
		"const literalToolStream = element('div'); literalToolStream.id = 'literal-marker-tool-output';\n" +
		"window.renderStreamingContent(literalToolStream, " + strconv.Quote(literalToolTranscript) + ");\n" +
		"function findTag(node, tag) { if (node.tagName === tag) return node; for (const child of (node.children || [])) { const found = findTag(child, tag); if (found) return found; } return null; }\n" +
		"const literalPre = findTag(literalToolStream, 'pre');\n" +
		"if (literalToolStream.children.length !== 1 || literalToolStream.children[0].className.indexOf('stream-tool') === -1 || !literalPre || literalPre.textContent !== literalToolOutput) { console.error(JSON.stringify(literalToolStream)); process.exit(59); }\n" +
		"const crossedToolOutput = 'output closes ` span';\n" +
		"const crossedToolStream = element('div'); crossedToolStream.id = 'cross-boundary-code-range';\n" +
		"window.renderStreamingContent(crossedToolStream, 'Literal `unclosed\\n[Using tool: bash]\\n[Tool bash done]\\n' + crossedToolOutput + '\\n[/Tool]\\n[Thinking]real thought[/Thinking]\\nVisible answer.');\n" +
		"const crossedPre = findTag(crossedToolStream, 'pre');\n" +
		"if (!crossedToolStream.children.some(function(child) { return child.className.indexOf('stream-tool') !== -1; }) || !crossedToolStream.children.some(function(child) { return child.className.indexOf('stream-thinking') !== -1; }) || !crossedPre || crossedPre.textContent !== crossedToolOutput) { console.error(JSON.stringify(crossedToolStream)); process.exit(60); }\n" +
		"const fencedCanonicalTool = '```text\\n[Tool bash done]\\nliteral example\\n[/Tool]\\n```';\n" +
		"const fencedCanonicalStream = element('div'); fencedCanonicalStream.id = 'fenced-canonical-tool';\n" +
		"window.renderStreamingContent(fencedCanonicalStream, fencedCanonicalTool);\n" +
		"if (fencedCanonicalStream.children.length !== 1 || fencedCanonicalStream.children[0].className.indexOf('chat-markdown') === -1 || fencedCanonicalStream.children[0].textContent !== fencedCanonicalTool) { console.error(JSON.stringify(fencedCanonicalStream)); process.exit(61); }\n" +
		"const enclosedToolStream = element('div'); enclosedToolStream.id = 'inline-range-enclosing-tool';\n" +
		"window.renderStreamingContent(enclosedToolStream, 'Literal `open\\n[Using tool: bash]\\n[Tool bash done]\\nenclosed output\\n[/Tool]\\nclose`\\n[Thinking]later thought[/Thinking]');\n" +
		"if (!enclosedToolStream.children.some(function(child) { return child.className.indexOf('stream-tool') !== -1; }) || !enclosedToolStream.children.some(function(child) { return child.className.indexOf('stream-thinking') !== -1; })) { console.error(JSON.stringify(enclosedToolStream)); process.exit(62); }\n" +
		"const largeMarkdown = Array(2200).fill('A paragraph with enough words to exercise cooperative Markdown chunking.\\n\\n').join('');\n" +
		"const largeMarkdownStream = element('div'); largeMarkdownStream.id = 'large-markdown';\n" +
		"window.renderStreamingContent(largeMarkdownStream, largeMarkdown).then(function(committed) {\n" +
		"  if (!committed || largeMarkdownStream.replaceCount !== 1 || largeMarkdownStream.children.length !== 1) throw new Error('large Markdown was not rendered as one document');\n" +
		"  const largeToolStream = element('div'); largeToolStream.id = 'large-tool';\n" +
		"  const largeTool = '[Using tool: bash]\\n[Tool bash done]\\n' + 'x'.repeat(150 * 1024) + '\\n[/Tool]';\n" +
		"  return window.renderStreamingContent(largeToolStream, largeTool, true).then(function(toolCommitted) {\n" +
		"    function findPre(node) { if (node.tagName === 'pre') return node; for (const child of (node.children || [])) { const found = findPre(child); if (found) return found; } return null; }\n" +
		"    const pre = findPre(largeToolStream);\n" +
		"    if (!toolCommitted || !pre || pre.children.length < 3) throw new Error('large tool output was not appended in chunks');\n" +
		"  const unavailableRangeStream = element('div'); unavailableRangeStream.id = 'large-tool-without-range-worker';\n" +
		"  window.codeRangesAsync = function() { return Promise.resolve(null); };\n" +
		"  let unavailableRejected = false;\n" +
		"  return window.renderStreamingContent(unavailableRangeStream, largeTool, true).then(function() { throw new Error('unavailable code-range worker retried synchronously'); }, function() { unavailableRejected = true; }).then(function() {\n" +
		"    if (!unavailableRejected || unavailableRangeStream.children.length !== 0) throw new Error('unavailable code-range worker must reject before rendering raw tool transcript');\n" +
		"    window.codeRangesAsync = function(text) { return Promise.resolve(window.codeRanges(text)); };\n" +
		"    const failing = element('div'); failing.id = 'failing-large-render'; failing.replaceChildren = function() { throw new Error('commit failed'); };\n" +
		"    const manyTools = ('[Using tool: bash]\\n[Tool bash done]\\nx\\n[/Tool]\\n').repeat(80) + 'tail '.repeat(22000);\n" +
		"    return window.renderStreamingContent(failing, manyTools, true).then(function() { process.exit(56); }, function(err) { if (!err || err.message !== 'commit failed') process.exit(57); });\n" +
		"  });\n" +
		"  });\n" +
		"}).catch(function(err) { console.error(err && err.stack || err); process.exit(58); });\n"
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("rendered browser cleaner did not preserve code examples: %v\n%s", err, output)
	}
}

func TestTranscriptCodeProtectionGeneratedParity(t *testing.T) {
	generated, err := os.ReadFile("chat_shared_templ.go")
	if err != nil {
		t.Fatalf("read generated shared template: %v", err)
	}
	baseGenerated, err := os.ReadFile("../layout/base_templ.go")
	if err != nil {
		t.Fatalf("read generated base template: %v", err)
	}
	content := string(baseGenerated) + string(generated)
	for _, snippet := range []string{
		"window.markdownLineRanges = function(text)",
		"window.codeRanges = function(text)",
		"function addInlineRanges(start, end)",
		"var backslashCount = 0",
		"if (backslashCount % 2 === 1)",
		"runs.push({ start: runStart, end: i, rawLength: i - runStart, openerStart: openerStart, openerLength: openerLength, next: -1 })",
		"lastRawRunByLength[String(runs[runIndex].rawLength)] = runIndex",
		"ranges.push({ start: runs[openIndex].openerStart, end: runs[closeIndex].end })",
		"if (closeIndex === -1) { openIndex++; continue; }",
		"openIndex = closeIndex + 1",
		"addInlineRanges(plainStart, lineStart)",
		"addInlineRanges(plainStart, text.length)",
		"/^[ \\\\t]*$/.test(line.substring(runEnd))",
		"window.isInsideCode = function(text, start, end)",
		"window.isInsideCodeRanges = function(ranges, start, end)",
		"window.stripOutsideCode = function(text, pattern, ranges)",
		"window.replaceOutsideCode = function(text, pattern, replacement, ranges)",
		"window.dedupTaskSummaries = function(text)",
		"var lines = window.markdownLineRanges(text)",
		"window.isInsideCodeRanges(protectedRanges, delimiter.start, delimiter.end)",
		"blocks.push({ start: start, end: end })",
		"text = window.dedupTaskSummaries(text);",
		"window.isInsideCodeRanges(protectedRanges, markerStart, sourceLine.end)",
		`var toolResultBlockPattern = /^[\\t ]*\\[Tool\\s+(\\S+)\\s+(done|error)\\]`,
		`window.stripOutsideCode(text, /\\[Tool\\s+\\S+\\s+(?:done|error)\\](?:\\r\\n|\\r|\\n)?[\\s\\S]*?(?:\\r\\n|\\r|\\n)?\\[\\/Tool\\](?:\\r\\n|\\r|\\n)?/g)`,
		"[^\\\\s\\\\]|][^|\\\\]]*",
	} {
		if !strings.Contains(content, snippet) {
			t.Fatalf("generated shared template missing code-protection snippet %q", snippet)
		}
	}
	if strings.Contains(string(generated), "function addInlineRanges(start, end)") || strings.Contains(string(generated), "window.isInsideCode = function") {
		t.Fatal("generated Chat component must use the base layout's shared Markdown helpers instead of defining duplicates")
	}
	if strings.Count(content, "window.codeRanges(textBuffer)") != 2 {
		t.Fatal("generated streaming renderer must synchronously scan only small inputs")
	}
	if strings.Contains(content, "result !== null ? result : (window.codeRanges") {
		t.Fatal("large worker failure must not repeat the code-range scan on the UI thread")
	}
	if strings.Count(content, "window.isInsideCodeRanges(codeRanges, match.index, match.index + match[0].length)") != 4 {
		t.Fatal("generated streaming renderer must protect thinking, tool-use, and both tool-result controls inside Markdown code")
	}
	if !strings.Contains(content, "function(raw, tool, command, closing)") || strings.Count(content, "return window.replaceOutsideCode(text,") != 1 {
		t.Fatal("generated transcript normalization must process tool and thinking aliases in one protected pass")
	}
}

func TestStatusCleaning_ScopesStreamingAndDOMToFinalCanonicalControl(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	start := strings.Index(content, "window.replaceOutsideCode = function")
	end := strings.Index(content, "// Apply transcript control-artifact cleaning")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("rendered script must define status and transcript-cleaning helpers")
	}
	codeHelpers := renderedBaseMarkdownCodeHelpers(t)
	for _, want := range []string{
		"textBuffer = window.stripFinalStatusControl(textBuffer, rawCodeRanges)",
		"window.stripFinalStatusControlFromElement(div)",
		"var t = cleanPreparedText(pendingText)",
		"closest('code, pre, blockquote, li, strong, em, del, a, h1, h2, h3, h4, h5, h6, table')",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered streaming/DOM cleaner missing %q", want)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the rendered DOM status cleaner")
	}
	script := "global.window = { addEventListener: function() {}, innerWidth: 1280, innerHeight: 900, devicePixelRatio: 1, getComputedStyle: function() { return { lineHeight: '14px', fontFamily: 'monospace', fontSize: '12px', fontWeight: '400', letterSpacing: 'normal' }; } };\n" +
		"global.NodeFilter = { SHOW_TEXT: 4 };\n" +
		"global.document = { createTreeWalker: function(element) { let i = 0; return { nextNode: function() { return element.nodes[i++] || null; } }; } };\n" +
		codeHelpers + "\n" + content[start:end] + "\n" +
		"function n(text, excluded) { return { textContent: text, parentElement: { closest: function() { return excluded ? {} : null; } } }; }\n" +
		"const earlier = n('[STATUS: SUCCESS]', false), later = n('More explanation follows.', false);\n" +
		"window.stripFinalStatusControlFromElement({ nodes: [earlier, later] });\n" +
		"if (earlier.textContent !== '[STATUS: SUCCESS]') process.exit(1);\n" +
		"const prose = n('Completed.\\n', false), terminal = n('[STATUS: FAILED | actual failure]', false);\n" +
		"window.stripFinalStatusControlFromElement({ nodes: [prose, terminal] });\n" +
		"if (terminal.textContent !== '') process.exit(2);\n" +
		"const quoted = n('[STATUS: NEEDS_FOLLOWUP | example]', true);\n" +
		"window.stripFinalStatusControlFromElement({ nodes: [quoted] });\n" +
		"if (quoted.textContent !== '[STATUS: NEEDS_FOLLOWUP | example]') process.exit(3);\n" +
		"const mixedFence = '```text\\n~~~\\n[STATUS: FAILED | still fenced]';\n" +
		"if (window.stripFinalStatusControl(mixedFence) !== mixedFence) process.exit(4);\n" +
		"const shortCloser = '`````text\\n```\\n[STATUS: NEEDS_FOLLOWUP | still fenced]';\n" +
		"if (window.stripFinalStatusControl(shortCloser) !== shortCloser) process.exit(5);\n" +
		"const matchedCloser = '`````text\\n```\\n`````\\n[STATUS: FAILED | real failure]';\n" +
		"if (window.stripFinalStatusControl(matchedCloser) !== '`````text\\n```\\n`````') process.exit(6);\n" +
		"const malformedFailed = 'Failure text.\\n[STATUS: FAILED | reason | extra]';\n" +
		"if (window.stripFinalStatusControl(malformedFailed) !== malformedFailed) process.exit(7);\n" +
		"const malformedFollowup = 'Follow-up text.\\n[STATUS: NEEDS_FOLLOWUP | reason | extra]';\n" +
		"if (window.cleanTranscriptControls(malformedFollowup, false) !== malformedFollowup) process.exit(8);\n" +
		"const malformedNode = n('[STATUS: FAILED | reason | extra]', false);\n" +
		"window.stripFinalStatusControlFromElement({ nodes: [malformedNode] });\n" +
		"if (malformedNode.textContent !== '[STATUS: FAILED | reason | extra]') process.exit(9);\n" +
		"const multilineInline = 'Example `[STATUS: FAILED | coded\\nreason]`';\n" +
		"if (window.stripFinalStatusControl(multilineInline) !== multilineInline) process.exit(10);\n" +
		"const multilineThenReal = multilineInline + '\\n[STATUS: FAILED | real failure]';\n" +
		"if (window.stripFinalStatusControl(multilineThenReal) !== multilineInline) process.exit(11);\n" +
		"const bareCRFenced = '~~~text\\r[STATUS: FAILED | coded]\\r~~~';\n" +
		"if (window.stripFinalStatusControl(bareCRFenced) !== bareCRFenced) process.exit(12);\n" +
		"const bareCRThenReal = '`````text\\r```\\r``````\\t \\r[STATUS: FAILED | real failure]';\n" +
		"if (window.stripFinalStatusControl(bareCRThenReal) !== '`````text\\r```\\r``````\\t ') process.exit(13);\n"
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("rendered DOM status cleaner violated final-control scoping: %v\n%s", err, output)
	}
}

func TestCleanTranscriptControls_StripsProposedPlanWrappersOnly(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	if !strings.Contains(content, "text = text.replace(/<\\/?\\s*proposed_plan\\s*>/gi, '')") {
		t.Fatal("cleanTranscriptControls should strip <proposed_plan> wrappers")
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
	rendererStart := strings.Index(content, "window.renderStreamingContent = function(container, textBuffer, yieldBetweenBatches)")
	if rendererStart == -1 {
		t.Fatal("renderStreamingContent definition is missing")
	}
	rendererEnd := strings.Index(content[rendererStart:], "// window.codeRanges and window.isInsideCode are installed by the base layout")
	if rendererEnd == -1 {
		t.Fatal("renderStreamingContent definition end is missing")
	}
	renderer := content[rendererStart : rendererStart+rendererEnd]

	// renderStreamingContent should remove whitespace-pre-wrap from the container
	if !strings.Contains(renderer, "container.classList.remove('whitespace-pre-wrap')") {
		t.Error("renderStreamingContent should remove 'whitespace-pre-wrap' class from container to prevent CSS inheritance issues")
	}
	if !strings.Contains(renderer, "var renderFragment = document.createDocumentFragment()") ||
		!strings.Contains(renderer, "container.replaceChildren(renderFragment)") {
		t.Error("renderStreamingContent must build detached DOM and commit it once")
	}
	if strings.Contains(renderer, "container.innerHTML = ''") {
		t.Error("renderStreamingContent must not clear the live container before detached rendering completes")
	}
	for _, snippet := range []string{
		"var shouldYieldPreparation = yieldBetweenBatches !== false && textBuffer.length >= 64 * 1024",
		"window.codeRangesAsync(textBuffer, container)",
		"targetProperty = 'thinkingRenderedContent'",
		"pendingThinkingRenderedContent = seg.thinkingRenderedContent",
		"var preparationPhases = [",
		"if (phaseResult && typeof phaseResult.then === 'function')",
		"setTimeout(runPreparationPhase, 0)",
		"batchSegments >= 12 || batchBytes + segmentBytes > 64 * 1024",
		"- batchStartedAt) >= 8",
		"setTimeout(renderBatch, 0)",
		"pendingLargeToolOutputs.push",
		"output.offset + 64 * 1024",
		"setTimeout(fillNextChunk, 0)",
		"if (container._streamRenderVersion !== renderVersion)",
	} {
		if !strings.Contains(renderer, snippet) {
			t.Errorf("large renderer must yield and cancel stale batches; missing %q", snippet)
		}
	}
}

func TestRenderStreamingContent_PairsRepeatedToolResultsFIFOAndUsesStableToolIDs(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	for _, want := range []string{
		"var toolUseQueues = {}",
		"toolUseQueues[usingName].push(segments[si])",
		"for (var sk = 0; sk < queue.length; sk++)",
		"segments[si].toolRenderID = 'tool-' + segments[si].index + '-' + toolRenderOrdinal++",
		"wrap.setAttribute('data-tool-render-id', seg.toolRenderID || '')",
		"outScroll.setAttribute('data-tool-render-id', seg.toolRenderID || '')",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("renderStreamingContent missing repeated-tool boundary logic %q", want)
		}
	}

	if strings.Contains(content, "for (var sk = si - 1; sk >= 0; sk--)") {
		t.Fatal("renderStreamingContent must not pair tool results with the newest previous same-name tool")
	}
}

func TestRenderStreamingContent_PreservesToolScrollByStableToolID(t *testing.T) {
	var buf bytes.Buffer
	err := ChatAutoScrollScript().Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}

	content := buf.String()
	for _, want := range []string{
		"var prevToolBodyScrollStates = {}",
		"var toolID = el.getAttribute('data-tool-render-id') || ''",
		"var rowKind = el.getAttribute('data-tool-row') || ''",
		"prevToolBodyScrollStates[toolID + ':' + rowKind]",
		"inScroll.setAttribute('data-tool-row', 'in')",
		"outScroll.setAttribute('data-tool-row', 'out')",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("renderStreamingContent missing stable tool scroll-state logic %q", want)
		}
	}

	if strings.Contains(content, "prevToolBodyScrollStates.push") || strings.Contains(content, "prevToolBodyScrollStates[idx]") {
		t.Fatal("renderStreamingContent must not preserve tool output scroll state by positional index")
	}
}

func TestRenderStreamingContent_ToolStateKeyIsExecutionScopedAcrossMorphReplacement(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the tool state-key regression")
	}

	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "function _thinkingStateKey(container)")
	end := strings.Index(content[start:], "// Shared streaming content renderer")
	if start == -1 || end == -1 {
		t.Fatal("shared state-key function is missing")
	}
	stateKeyFunction := content[start : start+end]

	script := `
const sharedPrefix = 'same-prefix-'.padEnd(200, 'x');
function completedContainer(execID) {
  const pair = { getAttribute(name) { return name === 'data-exec-id' ? execID : ''; } };
  return {
    id: '',
    parentElement: null,
    getAttribute(name) { return name === 'data-raw-content' ? sharedPrefix + '-tail-' + execID : ''; },
    closest(selector) { return selector === '[data-execution-pair="true"][data-exec-id]' ? pair : null; }
  };
}
const first = completedContainer('exec-first');
const second = completedContainer('exec-second');
const firstReplacement = completedContainer('exec-first');
const firstKey = _thinkingStateKey(first);
const secondKey = _thinkingStateKey(second);
const replacementKey = _thinkingStateKey(firstReplacement);
if (firstKey === secondKey) throw new Error('different executions shared state key: ' + firstKey);
if (firstKey !== replacementKey) throw new Error('morph replacement changed execution key');
const rowStates = {};
rowStates[firstKey + ':tool-1-0:out'] = { expanded: true, scrollTop: 42, pinned: false };
if (rowStates[secondKey + ':tool-1-0:out']) throw new Error('second execution inherited first execution state');
if (!rowStates[replacementKey + ':tool-1-0:out'] || rowStates[replacementKey + ':tool-1-0:out'].scrollTop !== 42) throw new Error('replacement did not restore first execution state');
`
	if output, err := exec.Command(node, "-e", stateKeyFunction+script).CombinedOutput(); err != nil {
		t.Fatalf("execution-scoped tool state regression failed: %v\n%s", err, output)
	}
}

func TestToolOutputLineCountUsesCommonMarkLineEndings(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute tool-output line counting")
	}

	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "function toolOutputLineCount(text)")
	end := strings.Index(content[start:], "function resolveToolOutputLineCapacity()")
	if start == -1 || end == -1 {
		t.Fatal("tool-output line-count function is missing")
	}
	lineCountFunction := content[start : start+end]
	script := `
const cases = [
  ['one', 1],
  ['one\ntwo', 2],
  ['one\r\ntwo', 2],
  ['one\rtwo', 2],
  ['one\r\ntwo\rthree\nfour', 4],
  [Array(40).fill('row').join('\r'), 40]
];
for (const [text, expected] of cases) {
  const actual = toolOutputLineCount(text);
  if (actual !== expected) throw new Error(JSON.stringify(text) + ': got ' + actual + ', want ' + expected);
}
`
	if output, err := exec.Command(node, "-e", lineCountFunction+script).CombinedOutput(); err != nil {
		t.Fatalf("CommonMark tool-output line counting failed: %v\n%s", err, output)
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
	if !strings.Contains(content, "if (!window._thinkingOpenStates) window._thinkingOpenStates = {};") {
		t.Error("should initialize window._thinkingOpenStates with executable JavaScript, not inside a comment")
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

// TestChatBubbleStreamingResume_InitialByteLengthUsesUTF8Bytes verifies that
// resume streams reconnect from the persisted raw UTF-8 byte offset, not JS string length.
func TestChatBubbleStreamingResume_InitialByteLengthUsesUTF8Bytes(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedBytes string
	}{
		{name: "ASCII only", content: "Hello World", expectedBytes: "11"},
		{name: "Unicode characters", content: "Hello… World—test", expectedBytes: "21"},
		{name: "emoji content", content: "Done! 🎉", expectedBytes: "10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := ChatBubbleStreamingResume("Assistant", tt.content, "exec-1", "chat-messages", "").Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("Failed to render: %v", err)
			}

			html := buf.String()
			expected := `data-initial-byte-length="` + tt.expectedBytes + `"`
			if !strings.Contains(html, expected) {
				t.Errorf("Expected %s but not found in HTML.\nGot HTML snippet around initial-byte-length: %s",
					expected, extractAttr(html, "data-initial-byte-length"))
			}
			if strings.Contains(html, "data-initial-length=") {
				t.Error("resume stream should not use data-initial-length after switching to byte offsets")
			}
		})
	}
}

func TestChatBubbleStreaming_UsesUTF8ByteOffsetInEventSourceURL(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "exec-1", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "function utf8ByteLength") || !strings.Contains(html, "streamTextEncoder.encode(value || '').length") {
		t.Fatal("fresh stream should compute offsets as UTF-8 byte lengths")
	}
	if !strings.Contains(html, "var streamOffset = utf8ByteLength(textBuffer);") || !strings.Contains(html, "'/events/chat/' + execId + '?offset=' + encodeURIComponent(streamOffset)") {
		t.Fatal("fresh stream EventSource URL should include current textBuffer byte offset")
	}
}

func TestChatBubbleStreamingResume_SeedsRenderedContentAndOffset(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreamingResume("Assistant", "partial 世界", "exec-1", "chat-messages", "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreamingResume: %v", err)
	}
	bubbleHTML := buf.String()
	if !strings.Contains(bubbleHTML, `data-initial-byte-length="14"`) {
		t.Fatalf("resume bubble should render raw UTF-8 byte offset, got attr %s", extractAttr(bubbleHTML, "data-initial-byte-length"))
	}

	buf.Reset()
	if err := ChatStreamingResumeInitScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render init thread streaming script: %v", err)
	}
	script := buf.String()
	for _, snippet := range []string{
		"var cumulativeContent = container.getAttribute('data-raw-content') || '';",
		"var streamOffset = parseInt(container.getAttribute('data-initial-byte-length') || '0', 10) || 0;",
		"new EventSource('/events/chat/' + execId + '?offset=' + encodeURIComponent(streamOffset))",
		"function persistResumeStreamState(active)",
		"streamOffset += utf8ByteLength(event.data);",
		"container.setAttribute('data-initial-byte-length', String(streamOffset));",
		"persistResumeStreamState(true);",
		"persistResumeStreamState(false);",
	} {
		if !strings.Contains(script, snippet) {
			t.Fatalf("resume script missing offset snippet: %s", snippet)
		}
	}
	if strings.Contains(script, "var initialLength = parseInt") || strings.Contains(script, "cumulativeContent.length < initialLength") {
		t.Fatal("resume script should not wait for replay from byte zero")
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
func TestTaskThreadLiveEventsScript_HandlesMixtureProgressWithoutTaskID(t *testing.T) {
	var buf bytes.Buffer
	if err := TaskThreadLiveEventsScript("task-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadLiveEventsScript: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "if (data.type !== 'mixture_progress' && data.task_id !== taskId) return;") {
		t.Fatal("task-thread live handler must not drop mixture_progress events that are keyed only by execution ID")
	}
	if !strings.Contains(html, "if (data.type === 'mixture_progress')") || !strings.Contains(html, "window.applyMixtureProgress(data)") {
		t.Fatal("task-thread live handler must render mixture_progress into the pending assistant status area")
	}
}

func TestTaskThreadLiveEventsScript_SteeredRowsReplaceStaleQueuedRows(t *testing.T) {
	var buf bytes.Buffer
	if err := TaskThreadLiveEventsScript("task-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadLiveEventsScript: %v", err)
	}
	html := buf.String()
	steeredStart := strings.Index(html, "function renderSteeringRow(data)")
	if steeredStart == -1 {
		t.Fatal("expected task-thread steering row renderer")
	}
	branchEnd := strings.Index(html[steeredStart:], "var pendingFragmentExecs")
	if branchEnd == -1 {
		t.Fatal("expected task-thread steering row renderer terminator")
	}
	branch := html[steeredStart : steeredStart+branchEnd]
	for _, snippet := range []string{
		"existing.getAttribute('data-input-mode') === 'steering'",
		"queuedSteerInFlight[data.pending_input_id]",
		"directSteerInFlight && !existing",
		"if (existing) existing.remove()",
		"Steering pending",
		"Attachments included",
		"htmx.process(steeringRow)",
		"if (data.type === 'task_thread_input_steered')",
	} {
		if !strings.Contains(html, snippet) && !strings.Contains(branch, snippet) {
			t.Fatalf("task-thread live steering must include %q", snippet)
		}
	}
	for _, snippet := range []string{
		"window._taskThreadQueuedSteerRequestHandlers = window._taskThreadQueuedSteerRequestHandlers || {}",
		"directSteerInFlight = true",
		"directSteerInFlight = false",
		"if (directSteerClearTimer) clearTimeout(directSteerClearTimer)",
		"document.body.addEventListener('htmx:beforeRequest', queuedSteerBeforeRequestHandler)",
		"document.body.addEventListener('htmx:afterRequest', queuedSteerAfterRequestHandler)",
		"window._taskThreadQueuedSteerRequestHandlers[taskId] = {",
		"document.body.removeEventListener('htmx:beforeRequest', queuedSteerBeforeRequestHandler)",
		"document.body.removeEventListener('htmx:afterRequest', queuedSteerAfterRequestHandler)",
		"delete window._taskThreadQueuedSteerRequestHandlers[taskId]",
		"clearQueuedSteerRequestGuards()",
	} {
		if !strings.Contains(html, snippet) {
			t.Fatalf("task-thread live script must manage local queued-to-steer request guard: missing %q", snippet)
		}
	}
}

func TestTaskThreadLiveEventsScript_QueuedRowsShowAttachmentIndicator(t *testing.T) {
	var buf bytes.Buffer
	if err := TaskThreadLiveEventsScript("task-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadLiveEventsScript: %v", err)
	}
	html := buf.String()
	queuedStart := strings.Index(html, "function renderQueuedRow(data)")
	if queuedStart == -1 {
		t.Fatal("expected task-thread queued row renderer")
	}
	branchEnd := strings.Index(html[queuedStart:], "var pendingFragmentExecs")
	if branchEnd == -1 {
		t.Fatal("expected task-thread queued row renderer terminator")
	}
	branch := html[queuedStart : queuedStart+branchEnd]
	if !strings.Contains(branch, "data.has_attachments") || !strings.Contains(branch, "Attachments queued") {
		t.Fatal("task-thread live queued row must render the attachment indicator when the event has attachments")
	}
}

func TestTaskThreadView_RuntimePreservationIsTaskScoped(t *testing.T) {
	taskOne := &models.Task{ID: "task-one", Status: models.StatusRunning}
	taskTwo := &models.Task{ID: "task-two", Status: models.StatusRunning}
	var first bytes.Buffer
	if err := TaskThreadView(taskOne, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &first); err != nil {
		t.Fatalf("render first task thread: %v", err)
	}
	var second bytes.Buffer
	if err := TaskThreadView(taskTwo, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &second); err != nil {
		t.Fatalf("render second task thread: %v", err)
	}
	if !strings.Contains(first.String(), `id="task-thread-runtime-task-one"`) || strings.Contains(first.String(), `id="task-thread-runtime-task-two"`) {
		t.Fatal("first task thread runtime must use its task-scoped preservation ID")
	}
	if strings.Contains(first.String(), `id="task-thread-runtime-task-one" class="contents" hx-preserve="true"`) {
		t.Fatal("full task-thread runtime must remain replaceable on same-task detail refreshes")
	}
	if !strings.Contains(second.String(), `id="task-thread-runtime-task-two"`) || strings.Contains(second.String(), `id="task-thread-runtime-task-one"`) {
		t.Fatal("second task thread runtime must not reuse the first task's preservation ID")
	}
}

func TestTaskThreadLiveEventsScript_ReconcilesChatResponseDoneCompletedOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := TaskThreadLiveEventsScript("task-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadLiveEventsScript: %v", err)
	}
	html := buf.String()
	for _, snippet := range []string{
		"window.addEventListener('sse-chat-live-event', chatHandler)",
		"if (data.type !== 'chat_response_done' || data.task_id !== taskId || !data.exec_id) return;",
		"syncCompletedThreadOutput(data.exec_id, data.completed_output, data.status || '')",
		"showCompletedThreadTerminalStatus(terminalStatus)",
		"refreshThreadComposerAction()",
		"htmx.ajax('GET', '/tasks/' + encodeURIComponent(taskId) + '/thread/composer-action'",
		"stopThreadPollingAfterCompletion()",
		"view.setAttribute('data-task-active', 'false')",
		"document.getElementById('streaming-message-' + execId)",
		"streamContainer.setAttribute('data-raw-content', completedOutput)",
		"liveRenderer(streamContainer, completedOutput)",
		"streamContainer.classList.remove('hidden')",
		"loading.classList.add('hidden')",
		"thinking.classList.add('hidden')",
		"window._taskThreadStreamingActive = false",
		"Task ' + (status === 'failed' ? 'failed' : (status === 'cancelled' ? 'cancelled' : 'completed'))",
		"window.removeEventListener('sse-chat-live-event', chatHandler)",
	} {
		if !strings.Contains(html, snippet) {
			t.Fatalf("task-thread live script must reconcile completed task follow-up output from chat_response_done; missing %q", snippet)
		}
	}
	if strings.Contains(html, "|| target.id === 'task-thread-view'") {
		t.Fatal("ordinary task-thread polling must not tear down preserved live handlers")
	}
	if strings.Contains(html, "}, { once: true });") || !strings.Contains(html, "document.body.removeEventListener('htmx:beforeSwap', cleanupTaskThreadLiveHandlers)") {
		t.Fatal("live-handler cleanup must survive polls and unregister itself only after navigation cleanup")
	}
}

func TestChatBubbleStreaming_RendersMixtureProgressStatusSlot(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "exec-mix", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreaming: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `id="mixture-progress-exec-mix"`) || !strings.Contains(html, `role="status"`) || !strings.Contains(html, `aria-live="polite"`) {
		t.Fatalf("streaming bubble must include an accessible mixture progress status slot, got: %s", html)
	}
	for _, snippet := range []string{
		"window.hideMixtureProgress(execId)",
	} {
		if !strings.Contains(html, snippet) {
			t.Fatalf("streaming bubble/shared script missing mixture progress snippet: %s", snippet)
		}
	}
}

func TestChatBubbleStreamingResume_RendersMixtureProgressStatusSlot(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreamingResume("Assistant", "", "exec-1", "chat-messages", "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatBubbleStreamingResume: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `id="mixture-progress-exec-1"`) || !strings.Contains(html, `role="status"`) || !strings.Contains(html, `aria-live="polite"`) {
		t.Fatal("streaming resume bubble must include an accessible mixture progress status slot")
	}
}

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
	if count := strings.Count(content, "el._renderedRevision === revision"); count < 2 {
		t.Errorf("cleanAssistantMessages must recognize the authoritative rendered signature in both raw-content paths, found %d", count)
	}
	if !strings.Contains(content, "Promise.resolve(renderPromise).then(function(rendered)") ||
		!strings.Contains(content, "if (el._renderingRevision === revision) delete el._renderingRevision") {
		t.Error("direct async rendering fallback must settle before committing or clearing its revision")
	}
	if !strings.Contains(content, "div.dataset.cleanedText === text") {
		t.Error("cleanAssistantMessages must skip unchanged assistant text blocks using cleanedText signature")
	}

	// If renderStreamingContent is unavailable, fallback markdown render must NOT lock
	// renderedRaw state. This allows a later pass (after renderStreamingContent loads)
	// to re-render tool cards from raw markers instead of staying markdown-only.
	if !strings.Contains(content, "delete el._renderedRevision") {
		t.Error("cleanAssistantMessages fallback markdown path must clear renderedRaw so tool-card re-render can occur later")
	}
}

// TestTaskThreadView_SkipsExpensiveWorkDuringNavigation verifies that the thread
// view's afterSwap handlers check _sidebarNavigating before running expensive
// DOM operations (cleanAssistantMessages, _initThreadStreaming). This prevents
// morph-induced main-thread blocking from delaying sidebar navigation clicks.
func TestTaskThreadView_SelectsTaskAssignedModelInDropdown(t *testing.T) {
	agentID := "opus-config"
	task := &models.Task{
		ID:        "t1",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryActive,
		AgentID:   &agentID,
	}
	agents := []models.LLMConfig{
		{ID: "sonnet-config", Name: "Claude Sonnet 4.6", Model: "claude-sonnet-4-6"},
		{ID: agentID, Name: "Claude Opus 4.7", Model: "claude-opus-4-7"},
	}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, agents, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `id="task-thread-form-agent-id" name="agent_id" value="opus-config"`) {
		t.Fatal("task thread hidden agent input must default to the task-assigned model")
	}
	// Custom select button stores the selected value in data-current-value (no <option selected> in the new implementation)
	if !strings.Contains(content, `data-current-value="opus-config"`) {
		t.Fatal("task thread model selector button must carry data-current-value matching the task-assigned model")
	}
	// The option list must contain the opus-config entry
	if !strings.Contains(content, `<li data-value="opus-config"`) {
		t.Fatal("task thread model option list must include the task-assigned model option")
	}
}

func TestTaskThreadView_ContainsHorizontalOverflowOnMobile(t *testing.T) {
	task := &models.Task{
		ID:        "task-thread-mobile-overflow",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	longUnbroken := strings.Repeat("very-long-unbroken-token", 24)
	execs := []models.Execution{{
		ID:         "exec-wide-content",
		TaskID:     task.ID,
		Status:     models.ExecCompleted,
		PromptSent: longUnbroken,
		Output:     "```\n" + longUnbroken + "\n```",
	}}

	var buf bytes.Buffer
	if err := TaskThreadView(task, execs, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `id="task-thread-view" class="flex flex-col flex-1 min-h-0 min-w-0 max-w-full"`) {
		t.Fatal("task thread root should stay width-bounded without clipping the composer shadow")
	}
	if strings.Contains(content, `id="task-thread-view" class="flex flex-col flex-1 min-h-0 min-w-0 max-w-full overflow-x-hidden"`) {
		t.Fatal("task thread root must not clip the composer shadow; horizontal containment belongs on the messages pane and inner controls")
	}
	if !strings.Contains(content, `id="task-thread-messages" class="flex-1 overflow-y-auto pt-4 pb-4 -mb-3 space-y-6 min-h-0"`) {
		t.Fatal("task thread messages pane should avoid stacking an outer bottom margin above the composer")
	}
	if strings.Contains(content, `id="task-thread-messages" class="flex-1 overflow-y-auto pt-4 pb-0 mb-4`) {
		t.Fatal("task thread messages pane must not add a second vertical gap before the composer")
	}
	if strings.Contains(content, `id="task-thread-messages" class="flex-1 overflow-y-auto overflow-x-hidden`) {
		t.Fatal("task thread messages pane must not hard-clip chat bubble shadows")
	}
	if strings.Contains(content, `-mr-[29px]`) || strings.Contains(content, `-mr-[18px]`) {
		t.Fatal("task thread messages must not use fixed right-margin scrollbar compensation because it leaves bubbles visually shorter than the input in real browsers")
	}
	if !strings.Contains(content, `class="chat-input-shadow-gutter w-full min-w-0 max-w-full mt-6"`) {
		t.Fatal("task thread composer must use a full-width shadow gutter with only the shared persistent top gap")
	}
	if strings.Contains(content, `chat-input-shadow-gutter w-full min-w-0 max-w-full sm:max-w-3xl`) || strings.Contains(content, `chat-input-shadow-gutter w-full min-w-0 max-w-full pt-2 pb-4 px-3`) || strings.Contains(content, `chat-input-shadow-gutter w-full min-w-0 max-w-full pt-2 pb-4`) {
		t.Fatal("task thread composer gutter must not add artificial side gaps or extra vertical spacing")
	}
	if !strings.Contains(content, `class="chat-input-container rounded-xl p-4 relative min-w-0 max-w-full"`) {
		t.Fatal("task thread composer shell must fill its shadow gutter without clipping")
	}
	if strings.Contains(content, `chat-input-container rounded-xl p-4 relative w-full`) {
		t.Fatal("task thread composer shell must not use w-full with visual margins")
	}
	if !strings.Contains(content, `class="flex items-center justify-between gap-2 pt-2 min-w-0 max-w-full overflow-hidden"`) {
		t.Fatal("task thread composer controls must contain horizontal overflow without clipping the shell bevel")
	}
	if strings.Contains(content, `overflow-x-auto py-4 mb-4`) {
		t.Fatal("task thread should not make the whole messages pane horizontally scrollable")
	}
}

func TestTaskThreadView_RunningThreadCanSteerFromPendingRowsOnly(t *testing.T) {
	task := &models.Task{
		ID:        "task-steer-ui",
		ProjectID: "p1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	execs := []models.Execution{{ID: "exec-running-task", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "running"}}
	pending := []models.ThreadInput{{ID: "queued-task", TaskID: task.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued"}}

	var buf bytes.Buffer
	if err := TaskThreadView(task, execs, nil, nil, nil, pending, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadView: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `name="expected_turn_id" value="exec-running-task"`) {
		t.Fatal("task-thread UI should keep the active turn id for queue safety")
	}
	if strings.Contains(content, `name="steer_endpoint"`) || strings.Contains(content, `data-steer-submit="true"`) {
		t.Fatal("task-thread composer must not expose direct steering controls")
	}
	if !strings.Contains(content, `id="pending-thread-inputs"`) || !strings.Contains(content, `hx-post="/tasks/task-steer-ui/thread/queued/queued-task/steer"`) || !strings.Contains(content, "Steer") {
		t.Fatal("task-thread composer queued row must expose Steer action")
	}
	if !strings.Contains(content, `id="pending-thread-inputs" class="space-y-1.5" data-task-id="task-steer-ui"`) || !strings.Contains(content, `data-thread-input-id="queued-task" data-task-id="task-steer-ui"`) {
		t.Fatal("task-thread pending container and rows must carry task id so queued rows cannot leak across task tabs")
	}
	messagesStart := strings.Index(content, `id="task-thread-messages"`)
	formStart := strings.Index(content, `id="task-thread-form"`)
	if messagesStart < 0 || formStart < 0 || strings.Contains(content[messagesStart:formStart], `thread-input-queued-task`) {
		t.Fatal("task-thread queued rows should render with the input box, not in the message transcript")
	}
	if !strings.Contains(content, `queued-input-row`) || !strings.Contains(content, `ml-auto`) || !strings.Contains(content, `aria-label="Cancel queued follow-up"`) || !strings.Contains(content, `M19 7l-.867 12.142`) {
		t.Fatal("task-thread queued row should use input-box styling with right-aligned Steer and trash-icon cancel action")
	}
	queuedStart := strings.Index(content, `class="queued-input-row`)
	if queuedStart < 0 {
		t.Fatal("task-thread queued row should render")
	}
	queuedEnd := strings.Index(content[queuedStart:], `</div></div>`)
	if queuedEnd < 0 {
		t.Fatal("task-thread queued row markup should be bounded")
	}
	queuedMarkup := content[queuedStart : queuedStart+queuedEnd]
	if strings.Contains(content, "Send now") || strings.Contains(content, "btn-warning") || strings.Contains(content, "bg-warning") || strings.Contains(queuedMarkup, ">Cancel</button>") || strings.Contains(queuedMarkup, ">×</button>") {
		t.Fatal("task-thread queued rows should not use warning styling, Send now copy, or text/× cancel")
	}
	if !strings.Contains(content, "task_thread_execution_started") || !strings.Contains(content, "/thread/executions/") || !strings.Contains(content, "'/fragment'") || !strings.Contains(content, "target: '#task-thread-messages'") || !strings.Contains(content, "swap: 'beforeend'") {
		t.Fatal("task-thread UI must append promoted queued executions smoothly via authoritative live event fragments")
	}
	if strings.Contains(content, "function createStreamingBubble(execId)") || strings.Contains(content, "document.createElement('div');\n\t\t\t\t\t\t\t\t\tbubble.className = 'chat-bubble-assistant-msg") {
		t.Fatal("task-thread live execution starts must not hand-build assistant bubbles outside shared fragments")
	}
	if !strings.Contains(content, "if (data.type === 'task_thread_input_applied')") || !strings.Contains(content, "ensureStreamingFragment(data)") {
		t.Fatal("task-thread UI must treat applied queued input events as a backup promotion signal")
	}
	if !strings.Contains(content, "function refreshPendingInputsIfVisible()") || !strings.Contains(content, "pendingContainer.querySelector('[data-thread-input-id]')") || !strings.Contains(content, "data.type === 'task_status_changed' || data.type === 'task_category_changed'") {
		t.Fatal("task-thread UI must reconcile visible pending rows on live task state changes so applied queued rows cannot remain stale")
	}
	if !strings.Contains(content, "function currentTaskThreadView()") || !strings.Contains(content, `return view && view.getAttribute('data-task-id') === taskId ? view : null;`) || !strings.Contains(content, "function scopedPendingContainerSelector()") || !strings.Contains(content, `#pending-thread-inputs[data-task-id=`) || !strings.Contains(content, "taskId.replace") {
		t.Fatal("task-thread live pending reconciliation must be scoped to the current task view")
	}
	if !strings.Contains(content, "pendingContainer.setAttribute('data-task-id', taskId)") || !strings.Contains(content, "queuedRow.setAttribute('data-task-id', taskId)") {
		t.Fatal("live-created pending containers and rows must carry task id")
	}
	if !strings.Contains(content, "if (data.type === 'task_thread_input_cancelled')") || !strings.Contains(content, "removePendingRow(data.pending_input_id)") {
		t.Fatal("task-thread UI must remove cancelled pending rows from live events")
	}
	if !strings.Contains(content, "pendingFragmentExecs") || !strings.Contains(content, "setTimeout(function() { ensureStreamingFragment(data, attempt + 1); }") {
		t.Fatal("task-thread UI must retry promoted execution fragment attachment to cover commit/event timing races")
	}
	if !strings.Contains(content, "window._pendingTaskThreadLiveEvents = window._pendingTaskThreadLiveEvents || {};") || !strings.Contains(content, "rememberPendingExecutionEvent(data)") || !strings.Contains(content, "consumePendingExecutionEvent();") {
		t.Fatal("task-thread UI must remember execution-start events that arrive before lazy thread DOM exists")
	}
	if !strings.Contains(content, "if (!currentTaskThreadView()) return;") {
		t.Fatal("promoted execution fragments must not append after the visible task thread has changed")
	}
	if !strings.Contains(content, `data-task-id="task-steer-ui"`) || !strings.Contains(content, "getAttribute('data-task-id')") {
		t.Fatal("task-thread live script must bind to the rendered task id, not a literal templ placeholder")
	}
}

func TestTaskThreadView_SkipsExpensiveWorkDuringNavigation(t *testing.T) {
	task := &models.Task{
		ID:        "t1",
		ProjectID: "p1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	var buf bytes.Buffer
	err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf)
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

func TestChatTranscriptRevision_ChangesForMissedExecutionState(t *testing.T) {
	base := []models.Execution{{ID: "exec-1", Status: models.ExecRunning, PromptSent: "hello", Output: "partial"}}
	if got, want := ChatTranscriptRevision(base, nil, "running"), ChatTranscriptRevision(base, nil, "running"); got != want {
		t.Fatalf("identical transcript snapshots must have stable revisions: %q != %q", got, want)
	}

	updated := append([]models.Execution(nil), base...)
	updated[0].Output = "partial plus missed output"
	if ChatTranscriptRevision(base, nil, "running") == ChatTranscriptRevision(updated, nil, "running") {
		t.Fatal("missed active execution output must invalidate the transcript snapshot")
	}
	updated[0].Status = models.ExecCompleted
	if ChatTranscriptRevision(base, nil, "running") == ChatTranscriptRevision(updated, nil, "completed") {
		t.Fatal("terminal execution/task transitions must invalidate the transcript snapshot")
	}
	updated = append(updated, models.Execution{ID: "exec-2", Status: models.ExecRunning, PromptSent: "queued promotion"})
	if ChatTranscriptRevision(base, nil, "running") == ChatTranscriptRevision(updated, nil, "running") {
		t.Fatal("a task start missed while hidden must invalidate the transcript snapshot")
	}
}

func TestTaskThreadView_ReconnectSkipsUnchangedDOMAndKeepsActiveStreams(t *testing.T) {
	task := &models.Task{
		ID:        "thread-reconnect-stable",
		ProjectID: "p1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	executions := []models.Execution{{ID: "done-1", Status: models.ExecCompleted, Output: "stable"}}
	var buf bytes.Buffer
	if err := TaskThreadView(task, executions, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task thread: %v", err)
	}
	content := buf.String()

	for _, required := range []string{
		`data-thread-revision=`,
		"if (currentRevision && nextRevision && currentRevision === nextRevision)",
		"event.detail.shouldSwap = false",
		"if (window._taskThreadStreamingActive)",
		"function waitForActiveCatchup()",
		"window.reconcileTaskThreadState(reconnectTaskId);",
		"window.reconcileTaskThreadState = function(reconnectTaskId)",
		"preserved_exec_ids",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("expected reconnect DOM-stability contract %q", required)
		}
	}

	noOp := strings.Index(content, "if (currentRevision && nextRevision && currentRevision === nextRevision)")
	closeStreams := strings.Index(content, "_closeTaskThreadEventSources();")
	if noOp < 0 || closeStreams < 0 || noOp > closeStreams {
		t.Fatal("unchanged poll/reconnect responses must be rejected before tracked execution streams are closed")
	}
}

func TestTaskThreadView_ClosesStreamsBeforeThreadRefresh(t *testing.T) {
	task := &models.Task{
		ID:        "t-stream-cleanup",
		ProjectID: "p1",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, "function _closeTaskThreadEventSources()") {
		t.Fatal("expected shared thread EventSource cleanup helper")
	}
	if !strings.Contains(content, "target.id === 'thread-content' || target.id === 'task-thread-view' || target.id === 'task-detail-content' || target.id === 'main-content'") {
		t.Fatal("expected thread refresh and navigation targets to close per-execution EventSources before swap")
	}
	if !strings.Contains(content, "_closeTaskThreadEventSources();") {
		t.Fatal("expected beforeSwap to close active thread EventSources")
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
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
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
	if strings.Contains(content, "if (window.markChatSendScrollIntent) window.markChatSendScrollIntent('task-thread-messages');") {
		t.Fatal("thread request start must not override upward-reading intent before the send succeeds")
	}
	if !strings.Contains(content, "window._taskThreadSavedInput = ''") {
		t.Fatal("beforeSwap should clear saved thread input on successful thread form swap")
	}
	if !strings.Contains(content, "if (sentKey) delete window._taskThreadDrafts[sentKey];") {
		t.Fatal("beforeSwap should clear persisted draft key on successful thread form swap")
	}
}

func TestTaskThreadView_BindsAttachmentImageSmartScrollAfterRenderAndSwap(t *testing.T) {
	task := &models.Task{
		ID:        "thread-attachment-scroll-1",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	if count := strings.Count(content, "window.bindAttachmentImageSmartScroll"); count < 4 {
		t.Fatalf("expected task thread to bind attachment image smart-scroll on initial render and HTMX swap paths, got %d", count)
	}
	trackerInit := strings.Index(content, "window._taskThreadPageTracker = window.resolveScrollTracker('scrollTracker_task-thread-messages', chatMessages)")
	initialBind := strings.Index(content, "window.bindAttachmentImageSmartScroll(chatMessages, 'scrollTracker_task-thread-messages', window._taskThreadPageTracker)")
	if initialBind < 0 {
		t.Fatal("task thread must bind attachment image smart-scroll with the task-thread tracker")
	}
	if trackerInit < 0 || trackerInit > initialBind {
		t.Fatal("task thread must create the tracker before initial attachment image binding so restored upward scroll intent is respected")
	}
	if !strings.Contains(content, "window.bindAttachmentImageSmartScroll(target, 'scrollTracker_task-thread-messages', window._taskThreadPageTracker)") {
		t.Fatal("task thread message-target swaps must bind attachment image smart-scroll on the swapped element")
	}
	for _, required := range []string{
		"function _consumeTaskThreadSendScrollIntent()",
		"window._taskThreadPageTracker.resetOnUserSend();",
		"window._taskThreadUserScrolledUp = false;",
		"delete window._taskThreadScrollStates[key];",
		"_consumeTaskThreadSendScrollIntent();",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("task thread should reset stale reading state after deliberate sends; missing %q", required)
		}
	}
	if !strings.Contains(content, "_finishTaskThreadRenderScroll(target, renderPromise);") {
		t.Fatal("task thread message swaps should reconcile variable-sized screenshots through the shared render barrier")
	}
	fullRefreshIdx := strings.Index(content, "target && target.id === 'task-detail-content'")
	if fullRefreshIdx < 0 {
		t.Fatal("task thread should handle full task detail content refreshes")
	}
	fullRefreshBody := content[fullRefreshIdx:]
	fullRefreshTrackerInit := strings.Index(fullRefreshBody, "window._taskThreadPageTracker = window.resolveScrollTracker('scrollTracker_task-thread-messages', chatMessages)")
	fullRefreshBind := strings.Index(fullRefreshBody, "window.bindAttachmentImageSmartScroll(chatMessages, 'scrollTracker_task-thread-messages', window._taskThreadPageTracker)")
	if fullRefreshBind < 0 {
		t.Fatal("task detail content refreshes must bind attachment image smart-scroll")
	}
	if fullRefreshTrackerInit < 0 || fullRefreshTrackerInit > fullRefreshBind {
		t.Fatal("task detail content refreshes must rebind the tracker before attachment image binding")
	}
	if !strings.Contains(fullRefreshBody, "_finishTaskThreadRenderScroll(chatMessages, renderPromise);") {
		t.Fatal("task detail content refreshes should consume send intent and reconcile after rendering")
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
	errEndOffset := strings.Index(content[errIdx:], "eventSource.onerror")
	if errEndOffset <= 0 {
		t.Fatal("ChatBubbleStreaming error handler boundary is missing")
	}
	errBody := content[errIdx : errIdx+errEndOffset]
	if !strings.Contains(errBody, "event.data === 'execution not found'") || !strings.Contains(errBody, "scheduleExecutionStreamRetry()") {
		t.Error("error handler must retry early execution lookup races before failing the stream")
	}
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
	oeEnd := oeIdx + 2000
	if oeEnd > len(content) {
		oeEnd = len(content)
	}
	oeBody := content[oeIdx:oeEnd]
	if !strings.Contains(oeBody, "scheduleExecutionStreamRetry()") {
		t.Error("onerror handler must retry empty early stream failures before clearing chat streaming state")
	}
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
	errBody := content[errIdx:min(len(content), errIdx+2000)]

	// Thread error should do the HTMX refresh but NOT clear _chatStreamInProgress
	// for plan prompt evaluation (plan mode is chat-only)
	if !strings.Contains(errBody, "isThread") {
		t.Error("error handler must check isThread context")
	}
}

func TestInitThreadStreaming_FindsStreamingDotsByID(t *testing.T) {
	var buf bytes.Buffer
	err := ChatStreamingResumeInitScript().Render(context.Background(), &buf)
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
	if !strings.Contains(content, "function connectResumeExecutionStream()") || !strings.Contains(content, "scheduleResumeExecutionStreamRetry()") {
		t.Error("_initThreadStreaming must retry early execution stream races for promoted/resumed rows")
	}
}

// TestChatScrollTracker_UsesInteractionSignalsForScrollIntent verifies the
// regression where smart scrolling stopped working during streaming in large
// conversations. The fix:
//   - Use real user-interaction signals (wheel/touchmove/keydown/pointerdown) to
//     mark scroll intent. The clamp-driven scroll events fired when
//     renderStreamingContent does container.innerHTML = ” must NOT be
//     interpreted as the user reaching the bottom (that previously cleared
//     userScrolledUp every chunk and pulled the user back down).
//   - shouldAutoScroll must return purely !userScrolledUp, never ANDed with
//     isNearBottom — otherwise the same clamp window triggers an auto-scroll
//     pulse that undoes the user's scroll-up.
func TestChatScrollTracker_UsesInteractionSignalsForScrollIntent(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	// REGRESSION GUARDS: the broken implementation unconditionally derived
	// userScrolledUp from isNearBottom on every scroll event, and ANDed the
	// flag with isNearBottom in shouldAutoScroll. Both forms must be absent.
	if strings.Contains(content, "self.userScrolledUp = !window.chatAutoScroll.isNearBottom(self.element);") {
		t.Error("scroll handler must NOT unconditionally derive userScrolledUp from isNearBottom — clamp events fired when innerHTML is cleared would defeat user scroll-up during streaming")
	}
	if strings.Contains(content, "return !this.userScrolledUp && window.chatAutoScroll.isNearBottom(this.element)") {
		t.Error("shouldAutoScroll must NOT AND with isNearBottom — clamp scroll events make isNearBottom briefly true even when the user has scrolled up, causing auto-scroll to yank the user back down")
	}

	required := []string{
		// Interaction-signal model
		"this._userInteracting = false",
		"this._pointerInteracting = false",
		"if (self._userInteracting)",
		// Listeners for real user scroll intent
		"addEventListener('wheel'",
		"addEventListener('touchmove'",
		"addEventListener('pointerdown'",
		"addEventListener('pointerup'",
		"addEventListener('pointercancel'",
		"addEventListener('keydown'",
		// shouldAutoScroll relies solely on the persisted flag
		"return !this.userScrolledUp;",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Errorf("ChatScrollTracker must contain %q to track scroll intent via user-interaction signals", r)
		}
	}

	if !strings.Contains(content, "if (!self._pointerInteracting) self._userInteracting = false;") {
		t.Error("timer-based interaction expiry must not clear _userInteracting during an active pointer/scrollbar drag")
	}
}

// TestChatScrollTracker_ResolveAndRebindRecoversFromStaleElement guards the
// regression where smart scrolling "froze" after a morph swap replaced the
// messages container — the cached window.scrollTracker_<id> held a detached
// element whose scrollHeight is 0, so isNearBottom returned true and auto-scroll
// ran forever until a full page refresh.
func TestChatScrollTracker_ResolveAndRebindRecoversFromStaleElement(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, "window.resolveScrollTracker = function") {
		t.Error("ChatAutoScrollScript must expose window.resolveScrollTracker to recover from stale tracker elements after morph swaps")
	}
	if !strings.Contains(content, "rebind: function(newElement)") {
		t.Error("ChatScrollTracker must expose rebind() so it can recover from stale/detached elements without dropping userScrolledUp")
	}
	// Stale element detection conditions.
	if !strings.Contains(content, "existing.element !== messagesEl") || !strings.Contains(content, "!existing.element.isConnected") {
		t.Error("resolveScrollTracker must detect when the cached element no longer matches the live DOM or has been detached")
	}
}

// TestStreamingRecoversFromStaleScrollTracker ensures the streaming renderers
// re-resolve the scroll tracker against the live messages element before each
// render. Without this, a morph swap mid-stream would leave the tracker bound to
// a detached element and smart scrolling would keep yanking the user to the
// bottom until a page refresh.
func TestStreamingRecoversFromStaleScrollTracker(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render new-message streaming script: %v", err)
	}
	if err := ChatStreamingResumeInitScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render thread streaming script: %v", err)
	}
	content := buf.String()

	if count := strings.Count(content, "window.resolveScrollTracker(trackerKey, "); count < 4 {
		t.Errorf("expected both streaming renderers to resolve the tracker on init AND inside the render loop (got %d resolveScrollTracker calls, want >= 4)", count)
	}
}

// TestStreamingRenderSnapshotsPinnedStateBeforeDomGrowth verifies streaming
// renderers decide whether to scroll before rendering new content. Checking
// after DOM growth breaks large conversations because adding content can move a
// previously pinned viewport outside the near-bottom threshold.
func TestStreamingRenderSnapshotsPinnedStateBeforeDomGrowth(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatBubbleStreaming("Assistant", "exec-id", "chat-messages", "", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render new-message streaming script: %v", err)
	}
	if err := ChatStreamingResumeInitScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render thread streaming script: %v", err)
	}
	content := buf.String()
	unlockPattern := regexp.MustCompile(`if\s*\(committed === false\)\s*\{\s*renderScheduled = false;`)
	if count := len(unlockPattern.FindAllString(content, -1)); count < 2 {
		t.Fatalf("both streaming callers must unlock scheduling after cancellation or timeout, found %d", count)
	}

	if count := strings.Count(content, "var shouldScroll = !tracker || tracker.shouldAutoScroll();"); count < 2 {
		t.Fatalf("expected streaming renderers to snapshot shouldScroll before DOM render, found %d", count)
	}
	textScrollIdx := strings.Index(content, "var shouldScroll = !tracker || tracker.shouldAutoScroll();")
	textRenderIdx := strings.Index(content, "var renderPromise = liveRenderer(container, renderText, shouldYield)")
	if textScrollIdx == -1 || textRenderIdx == -1 || textScrollIdx > textRenderIdx {
		t.Error("new-message streaming renderer must compute shouldScroll before renderStreamingContent")
	}

	resumeRenderIdx := strings.LastIndex(content, "var renderPromise = liveRenderer(container, renderText, shouldYield)")
	if resumeRenderIdx == -1 {
		t.Fatal("resume streaming renderer must call renderStreamingContent")
	}
	resumeScrollIdx := strings.LastIndex(content[:resumeRenderIdx], "var shouldScroll = !tracker || tracker.shouldAutoScroll();")
	if resumeScrollIdx == -1 || resumeScrollIdx > resumeRenderIdx {
		t.Error("resume streaming renderer must compute shouldScroll before renderStreamingContent")
	}
}

func TestTranscriptScrollCoordinatorStabilizesInitialHydrationAndAsyncLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	for _, required := range []string{
		"window.saveChatTranscriptScrollState = function(stateKey, messages, tracker)",
		"window.restoreChatTranscriptScroll = function(options)",
		"window.observeChatTranscriptLayout = function(options)",
		"this.intentRevision = 0;",
		"self.intentRevision = window._chatScrollIntentRevision;",
		"if (!self.element || !self.element.isConnected) return;",
		"var initialIntentRevision = tracker ? tracker.intentRevision : 0;",
		"var intentChanged = tracker && tracker.intentRevision !== initialIntentRevision;",
		"previous.userScrolledUp && !userScrolledUp && intentRevision <= (previous.intentRevision || 0)",
		"if (typeof self.onIntentChange === 'function') self.onIntentChange();",
		"messages.style.visibility = 'hidden';",
		"Promise.resolve(options.renderPromise)",
		"if (!options.waitForImages) return Promise.resolve();",
		"img[data-chat-attachment-image=\"true\"]",
		"if (img.loading === 'lazy') img.loading = 'eager';",
		"timeout = setTimeout(timeoutImage, 3000);",
		"img.removeEventListener('load', loaded);",
		"img.style.display = 'none';",
		"img.removeAttribute('src');",
		"img.setAttribute('data-chat-attachment-image-timeout', 'true');",
		"function loaded() { decodeImage().then(finish, finish); }",
		".then(waitForImages).then(waitForFonts).then(waitForLayout)",
		"messages.style.visibility = '';",
		"if (!tracker || !tracker.shouldAutoScroll()) return;",
		"new ResizeObserver",
		"document.fonts.ready",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("shared transcript stabilization is missing %q", required)
		}
	}
}

func TestTaskThreadView_HidesInitialTranscriptUntilRenderBarrierSettles(t *testing.T) {
	task := &models.Task{ID: "thread-stable-hydration", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	var buf bytes.Buffer
	if err := TaskThreadView(task, []models.Execution{{ID: "exec-1", Status: models.ExecCompleted, Output: "# rendered later"}}, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, `id="task-thread-messages" class="flex-1 overflow-y-auto pt-4 pb-4 -mb-3 space-y-6 min-h-0" style="visibility: hidden;" data-transcript-hydrating="true"`) {
		t.Fatal("task thread must keep the server-rendered transcript hidden during coordinated initial hydration")
	}
	if !strings.Contains(content, "window.restoreChatTranscriptScroll({") || !strings.Contains(content, "_finishTaskThreadRenderScroll(chatMessages, Promise.all(initialRenderPromises))") {
		t.Fatal("task thread must reveal and position the transcript through the shared render barrier")
	}
	if !strings.Contains(content, "waitForImages: true,") {
		t.Fatal("task thread initial reveal must wait for historical attachment image load and decode")
	}
}

// TestChatScrollTracker_DestroyRemovesAllListeners ensures the scroll listener
// is torn down by destroy() to prevent leaks across morph swaps that recreate
// the tracker.
func TestChatScrollTracker_ExposesNavigationSnapshot(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	required := []string{
		"snapshot: function()",
		"scrollTop: this.element.scrollTop || 0",
		"userScrolledUp: this.userScrolledUp",
		"pinned: !this.userScrolledUp",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Fatalf("expected ChatScrollTracker snapshot to include %q", r)
		}
	}
	if strings.Contains(content, "userScrolledUp: this.userScrolledUp || !nearBottom") {
		t.Fatal("scroll snapshots must not mistake automatic content growth for explicit user scroll-up")
	}
}

func TestTaskThreadView_PreservesPerTaskScrollState(t *testing.T) {
	task := &models.Task{
		ID:        "thread-scroll-1",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	required := []string{
		"window._taskThreadScrollStates = window._taskThreadScrollStates || {};",
		"return taskId ? 'task-thread-scroll-' + taskId : '';",
		"var preservedScrollState = freshOpen ? null : _getTaskThreadScrollState(taskId);",
		"window.initializeChatTranscriptScrollState({",
		"state: preservedScrollState,",
		"scope: taskId",
		"if (initialScrollState.sentByUser)",
		"messages.scrollTop = state.userScrolledUp ? (state.scrollTop || 0) : messages.scrollHeight;",
		"userScrolledUp: userScrolledUp, pinned: !userScrolledUp",
		"_saveTaskThreadScrollState();",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Fatalf("expected task thread scroll preservation code to include %q", r)
		}
	}
	initialization := strings.Index(content, "var initialScrollState = window.initializeChatTranscriptScrollState")
	attachmentBinding := strings.Index(content, "window.bindAttachmentImageSmartScroll(chatMessages, 'scrollTracker_task-thread-messages', window._taskThreadPageTracker)")
	if initialization < 0 || attachmentBinding < 0 || initialization > attachmentBinding {
		t.Fatal("task thread must consume initial scoped send intent before attachment/layout handlers can apply stale reading state")
	}

	if strings.Contains(content, "var preservedUserScrolledUp = window._taskThreadUserScrolledUp;") {
		t.Fatal("task thread entry must not restore scroll intent from a single global userScrolledUp flag")
	}
}

func TestTaskThreadView_DiscardsScrollStateWhenLeavingTaskDetail(t *testing.T) {
	task := &models.Task{
		ID:        "thread-scroll-leave",
		ProjectID: "p1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	if !strings.Contains(content, "target.id === 'task-thread-messages' || target.id === 'task-detail-content' || target.id === 'task-thread-view' || target.id === 'thread-content' || target.id === 'main-content'") {
		t.Fatal("main-content navigation must synchronously identify the outgoing task thread")
	}
	mainContentStart := strings.Index(content, "if (target && target.id === 'main-content')")
	if mainContentStart < 0 {
		t.Fatal("task-detail navigation must release detached task-thread state")
	}
	mainContentEnd := strings.Index(content[mainContentStart:], "delete window['scrollTracker_task-thread-messages'];")
	if mainContentEnd < 0 {
		t.Fatal("could not isolate task-detail main-content navigation cleanup")
	}
	mainContentBlock := content[mainContentStart : mainContentStart+mainContentEnd]
	for _, required := range []string{
		"delete window._taskThreadScrollStates[outgoingStateKey];",
		"delete window._chatTranscriptScrollStates[outgoingStateKey];",
		"window._taskThreadUserScrolledUp = false;",
		"window._taskThreadPageTracker.destroy();",
		"window._taskThreadFreshOpenTasks[outgoingTaskId] = true;",
	} {
		if !strings.Contains(mainContentBlock, required) {
			t.Fatalf("task-detail navigation must discard detached task-thread reading state; missing %q", required)
		}
	}
	for _, required := range []string{
		"else if (target && target.id === 'main-content')",
		"document.body.addEventListener('htmx:historyRestore'",
		"_initializeFreshTaskThreadOpen();",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("fresh task-thread navigation must reconcile at the bottom; missing %q", required)
		}
	}
}

func TestTaskThreadView_PinsAfterQueuedTranscriptRendering(t *testing.T) {
	task := &models.Task{ID: "thread-render-scroll-1", Status: models.StatusCompleted, Category: models.CategoryCompleted}

	var buf bytes.Buffer
	if err := TaskThreadView(task, nil, nil, nil, nil, nil, false, 30).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render TaskThreadView: %v", err)
	}
	content := buf.String()

	for _, required := range []string{
		"cleanPromise = window.cleanAssistantMessages ? window.cleanAssistantMessages(chatMessages) : null;",
		"var renderPromise = c._activeLiveChatRender && c._activeLiveChatRender.promise;",
		"resumeRenderPromises.push(Promise.resolve(renderPromise).catch(function()",
		"initialRenderPromises = initialRenderPromises.concat(resumeRenderPromises);",
		"_finishTaskThreadRenderScroll(chatMessages, Promise.all(initialRenderPromises));",
		"return window.restoreChatTranscriptScroll({",
		"var userScrolledUp = tracker ? !!tracker.userScrolledUp : !!window._taskThreadUserScrolledUp;",
		"defaultPinned: !userScrolledUp,",
		"renderPromise: renderPromise",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("task thread must bottom-align after queued transcript rendering unless the user scrolled up; missing %q", required)
		}
	}
	resumeRenderIdx := strings.Index(content, "resumeRenderPromises.push(Promise.resolve(renderPromise).catch(function()")
	finishScrollIdx := strings.Index(content, "_finishTaskThreadRenderScroll(chatMessages, Promise.all(initialRenderPromises));")
	if resumeRenderIdx == -1 || finishScrollIdx == -1 || finishScrollIdx < resumeRenderIdx {
		t.Fatal("task thread must collect resumed/live render completion before scheduling the final bottom alignment")
	}
}

func TestCleanAssistantMessagesReturnsQueuedRenderCompletion(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	for _, required := range []string{
		"var renderPromises = [];",
		"var scheduledRender = window.scheduleChatElementRender(el, raw);",
		"renderPromises.push(scheduledRender);",
		"if (el._chatElementRenderPromise) renderPromises.push(el._chatElementRenderPromise);",
		"var renderedRevision = el._renderedRevision || el.getAttribute('data-rendered-revision') || '';",
		"if (renderedRevision === revision && hasRenderedContent) return;",
		"container.setAttribute('data-rendered-revision', renderRevision);",
		"el.setAttribute('data-rendered-revision', revision);",
		"return Promise.all(renderPromises.map(function(promise)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("cleanAssistantMessages must expose queued transcript completion; missing %q", required)
		}
	}
}

func TestLiveChatRenderExposesCompletionPromise(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	if !strings.Contains(buf.String(), "liveRequest.promise = outerPromise;") {
		t.Fatal("resumed task-thread rendering must expose its completion promise to the initial scroll barrier")
	}
}

func TestChatScrollTracker_DestroyRemovesAllListeners(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render ChatAutoScrollScript: %v", err)
	}
	content := buf.String()

	// destroy() must remove every listener attached by _init, otherwise
	// rebinding the tracker after a morph swap leaks handlers that fire on
	// detached elements.
	required := []string{
		"removeEventListener('scroll'",
		"removeEventListener('wheel'",
		"removeEventListener('touchmove'",
		"removeEventListener('pointerdown'",
		"removeEventListener('pointerup'",
		"removeEventListener('pointercancel'",
		"removeEventListener('blur'",
		"removeEventListener('keydown'",
	}
	for _, r := range required {
		if !strings.Contains(content, r) {
			t.Errorf("destroy() must call %s to clean up listeners", r)
		}
	}
}

func TestTaskThreadView_WindowedTranscriptUsesContainerLoaderAndPruning(t *testing.T) {
	task := &models.Task{ID: "task-1", Title: "Task", Prompt: "original", Status: models.StatusRunning}
	execs := []models.Execution{
		{ID: "exec-1", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "first", Output: "done"},
		{ID: "exec-2", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "second"},
	}
	var buf bytes.Buffer
	if err := TaskThreadView(task, execs, nil, nil, nil, nil, true, 2).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task thread: %v", err)
	}
	content := buf.String()
	if !strings.Contains(content, `data-window-limit="2"`) {
		t.Fatal("expected task thread message container to carry visible window limit")
	}
	if !strings.Contains(content, `data-earlier-url-base="/tasks/task-1/thread?limit=2"`) {
		t.Fatal("expected task thread container to expose a base earlier URL for live-prune-created loaders")
	}
	if !strings.Contains(content, `data-earlier-loader="true"`) || !strings.Contains(content, `hx-trigger="ov:load-earlier"`) || !strings.Contains(content, `/tasks/task-1/thread?before=exec-1&amp;limit=2`) {
		t.Fatal("expected container-scroll earlier loader for task thread")
	}
	if !strings.Contains(content, `hx-swap="outerHTML show:none"`) {
		t.Fatal("expected earlier loader to disable HTMX target show scrolling")
	}
	if strings.Count(content, `data-execution-pair="true"`) < 2 {
		t.Fatal("expected task thread executions to render as whole-turn pairs")
	}
	if strings.Count(content, `chat-execution-pair space-y-6`) < 2 {
		t.Fatal("expected task thread execution pairs to preserve equal vertical spacing between user and assistant bubbles")
	}
	if strings.Contains(content, `chat-execution-pair space-y-3`) {
		t.Fatal("execution-pair spacing must match the surrounding message-list spacing, not use a smaller internal gap")
	}
	if !strings.Contains(content, "window.initChatEarlierLoader") || !strings.Contains(content, "window.pruneChatExecutionWindow") {
		t.Fatal("expected task thread to initialize load-earlier and live pruning helpers")
	}
}

func TestChatAutoScrollScript_EarlierLoaderUsesTopIntentWithoutDuplicateRebinds(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat auto-scroll script: %v", err)
	}
	content := buf.String()

	required := []string{
		"function setEarlierLoaderBusy(loader, busy)",
		"function recoverIdleEarlierRequest()",
		`container.querySelector('[data-earlier-loader="true"][data-loading="true"]')`,
		"data-earlier-loader-idle",
		"data-earlier-loader-busy",
		"Scroll up to load earlier messages",
		"function getFirstVisibleExecutionPair(container)",
		"window.bindChatEarlierHTMXLifecycle",
		"document.body.addEventListener('htmx:afterSettle'",
		"window.restoreChatEarlierScroll(container)",
		"container.dataset.earlierPrevScrollHeight",
		"container.dataset.earlierPrevScrollTop",
		"container.dataset.earlierPrevBottomDistance",
		"container.dataset.earlierAnchorExecId",
		"container.dataset.earlierAnchorOffsetTop",
		"container.dataset.earlierIntentRevision",
		"var preparedIntentRevision = parseInt(container.dataset.earlierIntentRevision",
		"if (currentIntentRevision !== preparedIntentRevision)",
		"if (tracker && tracker.shouldAutoScroll()) window.chatAutoScroll.scrollToBottom(container, false)",
		"clearChatEarlierRestoreState(container)",
		"requestAnimationFrame(function()",
		"container.scrollTop = Math.max(0, (container.scrollHeight || 0) - prevBottomDistance)",
		"currentOffset - anchorOffsetTop",
		"earlierAnchorRestoring",
		"function maybeLoadEarlier()", "addEventListener('scroll', function()",
		"addEventListener('wheel'",
		"event.deltaY < 0",
		"earlierLastWheelAt",
		"now - lastWheelAt > 500",
		"addEventListener('touchstart'",
		"addEventListener('touchmove'",
		"window.bindChatEarlierKeyboardLoader",
		"document.addEventListener('keydown'",
		"['ArrowUp', 'PageUp', 'Home'].includes(event.key)",
		"if (event.repeat) return",
		`container.dataset.earlierRequestLoading === 'true' && container.querySelector('[data-earlier-loader="true"][data-loading="true"]')`,
		"earlierGestureLocked",
		"earlierRequestLoading",
		"window.finishChatEarlierRequest",
		"else if (window.cleanAssistantMessages) hydrationPromise = window.cleanAssistantMessages(container)",
		"container._chatEarlierHydrationPromise = Promise.resolve(hydrationPromise)",
		"var hydrationPromise = container._chatEarlierHydrationPromise || Promise.resolve()",
		"Promise.resolve(hydrationPromise).then(function()",
		"!event.detail.successful",
		"loader.setAttribute('hx-swap', 'outerHTML show:none')",
	}
	for _, marker := range required {
		if !strings.Contains(content, marker) {
			t.Fatalf("expected earlier loader script to contain %q", marker)
		}
	}
	if strings.Contains(content, "setTimeout(maybeLoadEarlier") {
		t.Fatal("earlier loader must not eagerly fetch older history on initialization")
	}
	if strings.Contains(content, "if (window.cleanAssistantMessages) window.cleanAssistantMessages(container);\n\t\t\t\tif (window.applyChatBubbleTransforms) window.applyChatBubbleTransforms(container)") {
		t.Fatal("earlier-message swaps must not start duplicate assistant hydration renders")
	}
	if strings.Contains(content, "scheduleGestureUnlock") || strings.Contains(content, "document.addEventListener('keyup'") {
		t.Fatal("earlier loader must not unlock a consumed gesture before the HTMX request/swap completes")
	}
	if strings.Contains(content, "delete container.dataset.earlierLoaderBound") {
		t.Fatal("earlier loader must not delete the bound marker and stack duplicate listeners")
	}
	if strings.Contains(content, `>Loading earlier messages...</div>`) {
		t.Fatal("earlier loader must not show loading copy while idle")
	}
	if strings.Contains(content, "loader.dataset.prevScrollHeight") || strings.Contains(content, "loader.dataset.anchorExecId") {
		t.Fatal("prepend anchor state must live on the stable messages container, not the loader that HTMX replaces")
	}
}

func TestTranscriptScrollCoordinatorInChrome(t *testing.T) {
	chrome := testChromePath(t)
	var chatScript bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &chatScript); err != nil {
		t.Fatalf("render shared chat script: %v", err)
	}

	html := `<!doctype html><html><head><meta charset="utf-8"><style>
		#fixture-root { width: 480px; }
		.transcript { height: 200px; overflow-y: auto; visibility: hidden; }
		.pair { height: 120px; }
	</style><script>window.requestAnimationFrame = function(callback) { return setTimeout(callback, 0); }; window.cancelAnimationFrame = clearTimeout;</script></head><body><main id="fixture-root"><div id="messages" class="transcript" data-transcript-hydrating="true"><div class="pair" data-execution-pair="true"></div><div class="pair" data-execution-pair="true"></div></div></main>` + chatScript.String() + `<script>
	window.addEventListener('DOMContentLoaded', function() {
		var root = document.getElementById('fixture-root');
		function fail(message) { throw new Error(message); }
		function bottomDistance(messages) { return messages.scrollHeight - messages.scrollTop - messages.clientHeight; }
		function pair(messages) { var child = document.createElement('div'); child.className = 'pair'; child.setAttribute('data-execution-pair', 'true'); messages.appendChild(child); }
		function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
		async function verifySendSupersedesEarlierRestore(surfaceId) {
			var surface = document.createElement('div');
			surface.id = surfaceId; surface.className = 'transcript'; surface.style.visibility = '';
			for (var i = 0; i < 7; i++) pair(surface);
			var loader = document.createElement('div');
			loader.setAttribute('data-earlier-loader', 'true');
			loader.setAttribute('data-container-id', surfaceId);
			surface.insertBefore(loader, surface.firstChild);
			root.appendChild(surface);
			var tracker = window.resolveScrollTracker('scrollTracker_' + surfaceId, surface);
			window.observeChatTranscriptLayout({messages: surface, tracker: tracker, stateKey: 'pagination-send-' + surfaceId});
			surface.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			surface.scrollTop = 45;
			surface.dispatchEvent(new Event('scroll'));
			window.prepareChatEarlierSwap(loader);
			var resolveEarlierHydration;
			surface._chatEarlierHydrationPromise = new Promise(function(resolve) { resolveEarlierHydration = resolve; });
			window.restoreChatEarlierScroll(surface);
			window.markChatSendScrollIntent(surfaceId);
			var older = document.createElement('div');
			older.className = 'pair'; older.setAttribute('data-execution-pair', 'true');
			surface.insertBefore(older, loader.nextSibling);
			await wait(100);
			if (bottomDistance(surface) > 1) fail(surfaceId + ' did not remain pinned while earlier-page hydration was pending');
			resolveEarlierHydration();
			await wait(100);
			if (bottomDistance(surface) > 1 || tracker.userScrolledUp) fail(surfaceId + ' delayed earlier-page restore overrode successful-send pinning');
			surface.remove();
		}
		async function verifyReadingSupersedesEarlierRestore(surfaceId) {
			var surface = document.createElement('div');
			surface.id = surfaceId; surface.className = 'transcript'; surface.style.visibility = '';
			for (var i = 0; i < 7; i++) pair(surface);
			var loader = document.createElement('div');
			loader.setAttribute('data-earlier-loader', 'true');
			loader.setAttribute('data-container-id', surfaceId);
			surface.insertBefore(loader, surface.firstChild);
			root.appendChild(surface);
			var tracker = window.resolveScrollTracker('scrollTracker_' + surfaceId, surface);
			window.prepareChatEarlierSwap(loader);
			var resolveEarlierHydration;
			surface._chatEarlierHydrationPromise = new Promise(function(resolve) { resolveEarlierHydration = resolve; });
			window.restoreChatEarlierScroll(surface);
			var older = document.createElement('div');
			older.className = 'pair'; older.setAttribute('data-execution-pair', 'true');
			surface.insertBefore(older, loader.nextSibling);
			surface.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			surface.scrollTop = 90;
			surface.dispatchEvent(new Event('scroll'));
			resolveEarlierHydration();
			await wait(100);
			if (!tracker.userScrolledUp || Math.abs(surface.scrollTop - 90) > 1) fail(surfaceId + ' delayed earlier-page restore overrode newer upward-reading intent');
			surface.remove();
		}
		async function verifyDeliberateSend(surfaceId) {
			var surface = document.createElement('div');
			surface.id = surfaceId; surface.className = 'transcript'; surface.style.visibility = '';
			for (var i = 0; i < 6; i++) pair(surface);
			root.appendChild(surface);
			var trackerKey = 'scrollTracker_' + surfaceId;
			var surfaceTracker = window.resolveScrollTracker(trackerKey, surface);
			window.observeChatTranscriptLayout({messages: surface, tracker: surfaceTracker, stateKey: 'send-' + surfaceId});
			surface.scrollTop = surface.scrollHeight;
			surface.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			surface.scrollTop = 45;
			surface.dispatchEvent(new Event('scroll'));
			if (!surfaceTracker.userScrolledUp) fail(surfaceId + ' did not establish upward-reading intent');
			window.markChatSendScrollIntent(surfaceId);
			if (surfaceTracker.userScrolledUp || bottomDistance(surface) > 1) fail(surfaceId + ' successful send did not pin immediately');
			pair(surface);
			await wait(100);
			if (bottomDistance(surface) > 1) fail(surfaceId + ' did not follow asynchronous post-send growth');
			if (!window.consumeChatSendScrollIntent(surfaceId)) fail(surfaceId + ' lost valid send intent before authoritative swap');
			surface.remove();
		}
			async function verifyPostSendReadingOverridesPendingIntent(surfaceId) {
				var surface = document.createElement('div');
				surface.id = surfaceId; surface.className = 'transcript'; surface.style.visibility = '';
				for (var i = 0; i < 6; i++) pair(surface);
				root.appendChild(surface);
				var surfaceTracker = window.resolveScrollTracker('scrollTracker_' + surfaceId, surface);
				window.observeChatTranscriptLayout({messages: surface, tracker: surfaceTracker, stateKey: 'post-send-' + surfaceId});
				window.markChatSendScrollIntent(surfaceId);
				surface.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
				surface.scrollTop = 45;
				surface.dispatchEvent(new Event('scroll'));
			if (!surfaceTracker.userScrolledUp) fail(surfaceId + ' did not record post-send upward-reading intent');
			var image = document.createElement('img');
			image.setAttribute('data-chat-attachment-image', 'true');
			surface.appendChild(image);
			window.bindAttachmentImageSmartScroll(surface, 'scrollTracker_' + surfaceId, surfaceTracker);
			if (!surfaceTracker.userScrolledUp) fail(surfaceId + ' stale send intent let delayed image binding override newer upward-reading intent');
			if (window.consumeChatSendScrollIntent(surfaceId)) fail(surfaceId + ' stale send intent overrode newer upward-reading intent');				pair(surface);
				await wait(100);
				if (!surfaceTracker.userScrolledUp || Math.abs(surface.scrollTop - 45) > 1) fail(surfaceId + ' post-send reading intent did not survive delayed growth');
				surface.remove();
			}
			async function verifyCrossTaskSendIntentIsolation() {
				var taskA = document.createElement('div');
				taskA.id = 'task-thread-cross-task-messages'; taskA.className = 'transcript'; taskA.style.visibility = '';
				taskA.setAttribute('data-scroll-intent-scope', 'task-a');
				for (var i = 0; i < 6; i++) pair(taskA);
				root.appendChild(taskA);
				window.resolveScrollTracker('scrollTracker_task-thread-cross-task-messages', taskA);
				window.markChatSendScrollIntent('task-thread-cross-task-messages');
				taskA.remove();

				var taskB = document.createElement('div');
				taskB.id = 'task-thread-cross-task-messages'; taskB.className = 'transcript'; taskB.style.visibility = '';
				taskB.setAttribute('data-scroll-intent-scope', 'task-b');
				for (var j = 0; j < 6; j++) pair(taskB);
				root.appendChild(taskB);
				var taskBTracker = window.resolveScrollTracker('scrollTracker_task-thread-cross-task-messages', taskB);
				taskBTracker.userScrolledUp = true;
				taskB.scrollTop = 45;
				if (!taskBTracker.userScrolledUp) fail('task B did not restore upward-reading intent');
				var image = document.createElement('img');
				image.setAttribute('data-chat-attachment-image', 'true');
				taskB.appendChild(image);
				window.bindAttachmentImageSmartScroll(taskB, 'scrollTracker_task-thread-cross-task-messages', taskBTracker);
				if (window.hasChatSendScrollIntent('task-thread-cross-task-messages')) fail('task A send intent leaked into task B');
				if (!taskBTracker.userScrolledUp || Math.abs(taskB.scrollTop - 45) > 1) fail('task A send intent overrode task B reading position');
				taskB.remove();
			}
			async function verifyDelayedCrossTaskAcceptanceIsolation() {
				var taskB = document.createElement('div');
				taskB.id = 'task-thread-delayed-response-messages'; taskB.className = 'transcript'; taskB.style.visibility = '';
				taskB.setAttribute('data-scroll-intent-scope', 'task-b');
				for (var i = 0; i < 6; i++) pair(taskB);
				root.appendChild(taskB);
				var taskBTracker = window.resolveScrollTracker('scrollTracker_task-thread-delayed-response-messages', taskB);
				taskBTracker.userScrolledUp = true;
				taskB.scrollTop = 45;
				var taskBRevision = taskBTracker.intentRevision;

				var detachedTaskAForm = document.createElement('form');
				detachedTaskAForm.setAttribute('hx-target', '#task-thread-delayed-response-messages');
				detachedTaskAForm.setAttribute('data-scroll-intent-scope', 'task-a');
				window.markChatSendScrollIntent(detachedTaskAForm);

				if (!taskBTracker.userScrolledUp) fail('delayed task A acceptance reset task B upward-reading intent');
				if (taskBTracker.intentRevision !== taskBRevision) fail('delayed task A acceptance changed task B intent revision');
				if (Math.abs(taskB.scrollTop - 45) > 1) fail('delayed task A acceptance pinned task B transcript');
				if (window.hasChatSendScrollIntent('task-thread-delayed-response-messages')) fail('delayed task A acceptance created current intent for task B');
				taskB.remove();
			}
			async function verifyDelayedCrossTaskAcceptancePreservesMountedSend() {
				var messagesId = 'task-thread-overwrite-messages';
				var trackerKey = 'scrollTracker_' + messagesId;
				var taskB = document.createElement('div');
				taskB.id = messagesId; taskB.className = 'transcript'; taskB.style.visibility = '';
				taskB.setAttribute('data-scroll-intent-scope', 'task-b');
				for (var i = 0; i < 6; i++) pair(taskB);
				root.appendChild(taskB);
				var taskBTracker = window.resolveScrollTracker(trackerKey, taskB);
				taskBTracker.userScrolledUp = true;
				taskB.scrollTop = 45;

				var detachedTaskBForm = document.createElement('form');
				detachedTaskBForm.setAttribute('hx-target', '#' + messagesId);
				detachedTaskBForm.setAttribute('data-scroll-intent-scope', 'task-b');
				window.markChatSendScrollIntent(detachedTaskBForm);
				if (!window.hasChatSendScrollIntent(messagesId, 'task-b')) fail('task B acceptance did not establish scoped send intent');

				var detachedTaskAForm = document.createElement('form');
				detachedTaskAForm.setAttribute('hx-target', '#' + messagesId);
				detachedTaskAForm.setAttribute('data-scroll-intent-scope', 'task-a');
				window.markChatSendScrollIntent(detachedTaskAForm);
				if (!window.hasChatSendScrollIntent(messagesId, 'task-b')) fail('delayed task A acceptance overwrote task B send intent');

				taskB.remove();
				var replacement = document.createElement('div');
				replacement.id = messagesId; replacement.className = 'transcript'; replacement.style.visibility = '';
				replacement.setAttribute('data-scroll-intent-scope', 'task-b');
				for (var j = 0; j < 7; j++) pair(replacement);
				root.appendChild(replacement);
				taskBTracker = window.resolveScrollTracker(trackerKey, replacement);
				var taskBInitialization = window.initializeChatTranscriptScrollState({
					messages: replacement,
					tracker: taskBTracker,
					state: {scrollTop: 45, userScrolledUp: true, pinned: false},
					scope: 'task-b'
				});
				if (!taskBInitialization.sentByUser) fail('task B send intent was unavailable after tracker rebind');
				if (taskBTracker.userScrolledUp || bottomDistance(replacement) > 1) fail('task B replacement did not restore deliberate-send pinning');
				replacement.remove();

				var taskAReturn = document.createElement('div');
				taskAReturn.id = messagesId; taskAReturn.className = 'transcript'; taskAReturn.style.visibility = '';
				taskAReturn.setAttribute('data-scroll-intent-scope', 'task-a');
				for (var k = 0; k < 7; k++) pair(taskAReturn);
				root.appendChild(taskAReturn);
				var taskAReturnTracker = window.resolveScrollTracker(trackerKey, taskAReturn);
				var taskAInitialization = window.initializeChatTranscriptScrollState({
					messages: taskAReturn,
					tracker: taskAReturnTracker,
					state: {scrollTop: 45, userScrolledUp: true, pinned: false},
					scope: 'task-a'
				});
				if (!taskAInitialization.sentByUser) fail('delayed task A acceptance was not consumed on initial navigation render');
				if (taskAReturnTracker.userScrolledUp || bottomDistance(taskAReturn) > 1) fail('returning to task A did not restore its deliberate-send pinning');
				taskAReturn.remove();
			}
			async function verifyCrossScopeReadingDoesNotInvalidateSend() {
				var messagesId = 'task-thread-scope-revision-messages';
				var trackerKey = 'scrollTracker_' + messagesId;
				var taskA = document.createElement('div');
				taskA.id = messagesId; taskA.className = 'transcript'; taskA.style.visibility = '';
				taskA.setAttribute('data-scroll-intent-scope', 'task-a');
				for (var i = 0; i < 6; i++) pair(taskA);
				root.appendChild(taskA);
				var tracker = window.resolveScrollTracker(trackerKey, taskA);
				window.markChatSendScrollIntent(messagesId, 'task-a');
				if (!window.hasChatSendScrollIntent(messagesId, 'task-a')) fail('task A mounted send did not establish scoped intent');
				taskA.remove();

				var taskB = document.createElement('div');
				taskB.id = messagesId; taskB.className = 'transcript'; taskB.style.visibility = '';
				taskB.setAttribute('data-scroll-intent-scope', 'task-b');
				for (var j = 0; j < 6; j++) pair(taskB);
				root.appendChild(taskB);
				tracker = window.resolveScrollTracker(trackerKey, taskB);
				taskB.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
				taskB.scrollTop = 45;
				taskB.dispatchEvent(new Event('scroll'));
				if (!tracker.userScrolledUp) fail('task B did not record upward-reading intent');
				taskB.remove();

				var taskAReturn = document.createElement('div');
				taskAReturn.id = messagesId; taskAReturn.className = 'transcript'; taskAReturn.style.visibility = '';
				taskAReturn.setAttribute('data-scroll-intent-scope', 'task-a');
				for (var k = 0; k < 7; k++) pair(taskAReturn);
				root.appendChild(taskAReturn);
				tracker = window.resolveScrollTracker(trackerKey, taskAReturn);
				var initialization = window.initializeChatTranscriptScrollState({
					messages: taskAReturn,
					tracker: tracker,
					state: {scrollTop: 45, userScrolledUp: true, pinned: false},
					scope: 'task-a'
				});
				if (!initialization.sentByUser) fail('task B reading invalidated task A scoped send intent');
				if (tracker.userScrolledUp || bottomDistance(taskAReturn) > 1) fail('returning task A did not stay pinned after task B reading');
				taskAReturn.remove();
			}
			async function verifyUnacceptedSendPreservesReading(surfaceId) {
				var surface = document.createElement('div');			surface.id = surfaceId; surface.className = 'transcript'; surface.style.visibility = '';
			for (var i = 0; i < 6; i++) pair(surface);
			root.appendChild(surface);
			var surfaceTracker = window.resolveScrollTracker('scrollTracker_' + surfaceId, surface);
			window.observeChatTranscriptLayout({messages: surface, tracker: surfaceTracker, stateKey: 'failed-' + surfaceId});
			surface.scrollTop = surface.scrollHeight;
			surface.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			surface.scrollTop = 45;
			surface.dispatchEvent(new Event('scroll'));
			pair(surface);
			await wait(100);
			if (!surfaceTracker.userScrolledUp || Math.abs(surface.scrollTop - 45) > 1) fail(surfaceId + ' unaccepted send overrode upward-reading intent');
			surface.remove();
		}
		var messages = document.getElementById('messages');
		var tracker = new window.ChatScrollTracker(messages);
		var resolveHydration;
		var hydration = new Promise(function(resolve) { resolveHydration = resolve; });
		var restored = window.restoreChatTranscriptScroll({messages: messages, tracker: tracker, stateKey: 'browser-chat', defaultPinned: true, renderPromise: hydration});
		if (messages.style.visibility !== 'hidden') fail('initial transcript was revealed before hydration settled');
		pair(messages); pair(messages); pair(messages);
		resolveHydration();
		Promise.resolve(restored).then(async function() {
			if (messages.style.visibility === 'hidden' || messages.getAttribute('data-transcript-hydrating')) fail('initial transcript was not revealed after hydration');
			if (bottomDistance(messages) > 1) fail('initial hydration did not settle at true bottom: ' + bottomDistance(messages));

			window.saveChatTranscriptScrollState('browser-chat', messages, tracker);
			messages.remove();
			var replacement = document.createElement('div');
			replacement.id = 'messages'; replacement.className = 'transcript'; replacement.style.visibility = 'hidden'; replacement.setAttribute('data-transcript-hydrating', 'true');
			for (var i = 0; i < 6; i++) pair(replacement);
			root.appendChild(replacement);
			var replacementTracker = new window.ChatScrollTracker(replacement);
			await window.restoreChatTranscriptScroll({messages: replacement, tracker: replacementTracker, stateKey: 'browser-chat', renderPromise: Promise.resolve()});
			if (bottomDistance(replacement) > 1) fail('pinned navigation restoration missed true bottom: ' + bottomDistance(replacement));

			pair(replacement);
			await wait(100);
			if (bottomDistance(replacement) > 1) fail('pinned async layout growth was not reconciled: ' + bottomDistance(replacement));

			replacementTracker.userScrolledUp = true;
			replacement.scrollTop = 75;
			window.saveChatTranscriptScrollState('browser-chat', replacement, replacementTracker);
			pair(replacement);
			await wait(100);
			if (Math.abs(replacement.scrollTop - 75) > 1) fail('async layout growth overrode intentional upward scroll: ' + replacement.scrollTop);

			var readingState = window.getChatTranscriptScrollState('browser-chat');
			replacement.remove();
			var restoredReading = document.createElement('div');
			restoredReading.id = 'messages'; restoredReading.className = 'transcript'; restoredReading.style.visibility = 'hidden'; restoredReading.setAttribute('data-transcript-hydrating', 'true');
			for (var j = 0; j < 8; j++) pair(restoredReading);
			root.appendChild(restoredReading);
			var readingTracker = new window.ChatScrollTracker(restoredReading);
			await window.restoreChatTranscriptScroll({messages: restoredReading, tracker: readingTracker, stateKey: 'browser-chat', state: readingState, renderPromise: Promise.resolve()});
			if (Math.abs(restoredReading.scrollTop - 75) > 1 || !readingTracker.userScrolledUp) fail('navigation restoration lost intentional reading position');

			restoredReading.remove();
			var interactiveHydration = document.createElement('div');
			interactiveHydration.id = 'messages'; interactiveHydration.className = 'transcript'; interactiveHydration.style.visibility = 'hidden'; interactiveHydration.setAttribute('data-transcript-hydrating', 'true');
			for (var k = 0; k < 10; k++) pair(interactiveHydration);
			root.appendChild(interactiveHydration);
			var interactiveTracker = new window.ChatScrollTracker(interactiveHydration);
			interactiveHydration.scrollTop = interactiveHydration.scrollHeight;
			var resolveInteractiveHydration;
			var interactiveRender = new Promise(function(resolve) { resolveInteractiveHydration = resolve; });
			var interactiveRestore = window.restoreChatTranscriptScroll({messages: interactiveHydration, tracker: interactiveTracker, stateKey: 'interactive-chat', defaultPinned: true, renderPromise: interactiveRender});
			interactiveHydration.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			interactiveHydration.scrollTop = 110;
			interactiveHydration.dispatchEvent(new Event('scroll'));
			resolveInteractiveHydration();
			await interactiveRestore;
			if (Math.abs(interactiveHydration.scrollTop - 110) > 1 || !interactiveTracker.userScrolledUp) fail('render barrier overrode upward intent made during hydration');

			var resolveReadingHydration;
			var readingHydrationRender = new Promise(function(resolve) { resolveReadingHydration = resolve; });
			var continuedReadingState = window.saveChatTranscriptScrollState('interactive-chat', interactiveHydration, interactiveTracker);
			var continuedReadingRestore = window.restoreChatTranscriptScroll({messages: interactiveHydration, tracker: interactiveTracker, stateKey: 'interactive-chat', state: continuedReadingState, renderPromise: readingHydrationRender});
			interactiveHydration.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
			interactiveHydration.scrollTop = 60;
			interactiveHydration.dispatchEvent(new Event('scroll'));
			resolveReadingHydration();
			await continuedReadingRestore;
			if (Math.abs(interactiveHydration.scrollTop - 60) > 1 || !interactiveTracker.userScrolledUp) fail('render barrier restored a stale position after an already-scrolled-up user moved again');
			await verifySendSupersedesEarlierRestore('chat-messages-pagination-send');
			await verifySendSupersedesEarlierRestore('task-thread-messages-pagination-send');
			await verifyReadingSupersedesEarlierRestore('chat-messages-pagination-reading');
			await verifyReadingSupersedesEarlierRestore('task-thread-messages-pagination-reading');
			await verifyDeliberateSend('chat-messages');
			await verifyDeliberateSend('task-thread-messages');
			await verifyPostSendReadingOverridesPendingIntent('chat-post-send-messages');
			await verifyPostSendReadingOverridesPendingIntent('task-thread-post-send-messages');
			await verifyCrossTaskSendIntentIsolation();
			await verifyDelayedCrossTaskAcceptanceIsolation();
			await verifyDelayedCrossTaskAcceptancePreservesMountedSend();
			await verifyCrossScopeReadingDoesNotInvalidateSend();
			await verifyUnacceptedSendPreservesReading('chat-failed-messages');
			await verifyUnacceptedSendPreservesReading('task-thread-failed-messages');
			root.setAttribute('data-test-result', 'pass');
		}).catch(function(error) {
			root.setAttribute('data-test-result', 'fail');
			root.setAttribute('data-test-error', String(error && error.stack || error));
		});
	});
	</script></body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chrome-scroll.html")
	stderrPath := filepath.Join(t.TempDir(), "chrome-scroll.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout: %v", err)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--run-all-compositor-stages-before-draw", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome-scroll-profile"), "--virtual-time-budget=5000", "--dump-dom", server.URL)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome scroll fixture: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopTestBrowserProcess(cmd)
	if !strings.Contains(result, `data-test-result="pass"`) {
		state := regexp.MustCompile(`<main id="fixture-root"[^>]*>`).FindString(result)
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("transcript scroll browser fixture failed: %s\nDOM: %s\nChrome stderr: %s", state, result, stderr)
	}
}

func TestLargeToolOutputHydrationIsLazyAndStatefulInChrome(t *testing.T) {
	if os.Getenv("OPENVIBELY_SKIP_BROWSER_PERF") == "1" {
		t.Skip("browser performance fixture runs in an isolated CI step")
	}
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err != nil {
		for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if resolved, lookupErr := exec.LookPath(candidate); lookupErr == nil {
				chrome = resolved
				break
			}
		}
		if _, err := os.Stat(chrome); err != nil {
			t.Skip("Chrome or Chromium is required for integrated tool-output layout validation")
		}
	}

	var baseHTML bytes.Buffer
	if err := layout.Base("Large tool output fixture", nil, "").Render(context.Background(), &baseHTML); err != nil {
		t.Fatalf("render base layout: %v", err)
	}
	var chatScript bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &chatScript); err != nil {
		t.Fatalf("render shared chat script: %v", err)
	}

	styles := strings.Join(regexp.MustCompile(`(?s)<style>.*?</style>`).FindAllString(baseHTML.String(), -1), "")
	html := `<!DOCTYPE html><html lang="en" data-theme="dark" style="--bc: 0.746477 0.0216 264.436"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0">` + styles + `<style>html[data-theme="dark"] body { color: oklch(var(--bc)); }</style></head><body>`
	fixture := `<main id="fixture-root"><div id="streaming-message-large-fixture" class="chat-stream-content chat-bubble-assistant-msg"></div></main><script>` + renderedBaseMarkdownCodeHelpers(t) + `</script>` + chatScript.String() + `<script>
window.addEventListener('DOMContentLoaded', function() {
  var result = document.getElementById('fixture-root');
  var container = document.getElementById('streaming-message-large-fixture');
  var longTasks = [];
  window.renderChatMarkdown = function(text) {
    return String(text).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  };
  window.renderChatMarkdownAsync = null;
  window.addCodeCopyButtons = function() {};
  window.codeRangesAsync = function(text) {
    return new Promise(function(resolve) {
      setTimeout(function() { resolve(window.codeRanges(text)); }, 0);
    });
  };

  function repeatedLines(count, width, prefix) {
    var rows = [];
    for (var i = 0; i < count; i++) rows.push((prefix + '-' + i + ' ').padEnd(width, 'x'));
    return rows.join('\n');
  }
  var chunkSize = 64 * 1024;
  var largeOutput = repeatedLines(889, 56, 'chat_action_tools.go source line');
  var boundaryCreate = '- "Boundary created" (backlog) [TASK_ID:boundary/create]';
  largeOutput = largeOutput.padEnd(chunkSize - 20, 'x') + '\n' + boundaryCreate + '\n';
  var boundaryEdit = '- "Boundary edited" (updated: title) [TASK_EDITED:boundary/edit]';
  largeOutput = largeOutput.padEnd((2 * chunkSize) - 20, 'y') + '\n' + boundaryEdit + '\n';
  largeOutput = largeOutput.padEnd((3 * chunkSize) - 8, 'z') + '\u0060coded inline\n' +
    '- "Coded boundary create" (backlog) [TASK_ID:coded/boundary-create]\n\u0060\n';
  largeOutput = largeOutput.padEnd((4 * chunkSize) - 16, 'q') + '\n\u0060\u0060\u0060text\nfence lead\n' +
    '- "Coded boundary edit" (updated: title) [TASK_EDITED:coded/boundary-edit]\n\u0060\u0060\u0060';
  var largeLineCount = largeOutput.split('\n').length;

  var transcript = '[Thinking]\nPreparing synthetic large transcript.\n[/Thinking]\n' +
    '- "Assistant created" (backlog) [TASK_ID:assistant/create]\n' +
    '- "Assistant edited" (updated: title) [TASK_EDITED:assistant/edit]\n';
  for (var tool = 0; tool < 114; tool++) {
    var output = tool === 0 ? largeOutput : repeatedLines(40, 100, 'tool-' + tool);
    transcript += '[Using tool: read_file | fixture-' + tool + '.go]\n[Tool read_file done]\n' + output + '\n[/Tool]\n';
  }
  var wideOutput = 'wide '.padEnd(12000, 'w');
  var bareCROutput = Array(40).fill('bare-cr row').join('\r');
  transcript += '[Using tool: read_file | wide.txt]\n[Tool read_file done]\n' + wideOutput + '\n[/Tool]\n';
  transcript += '[Using tool: Bash | printf short-output]\n[Tool Bash done]\nshort output\n[/Tool]\n';
  transcript += '[Using tool: read_file | bare-cr.txt]\n[Tool read_file done]\n' + bareCROutput + '\n[/Tool]\n';

  function fail(message) { throw new Error(message); }
  function waitFor(check, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        if (check()) { resolve(); return; }
        if (performance.now() - started > timeout) { reject(new Error('timed out waiting for output materialization')); return; }
        setTimeout(poll, 5);
      }
      poll();
    });
  }

  if (window.PerformanceObserver) {
    try {
      var observer = new PerformanceObserver(function(list) {
        list.getEntries().forEach(function(entry) { longTasks.push(entry.duration); });
      });
      observer.observe({ entryTypes: ['longtask'] });
    } catch (_) {}
  }
  var started = performance.now();
  Promise.resolve(window.renderStreamingContent(container, transcript, true)).then(async function(committed) {
    if (!committed) fail('large hydration did not commit');
    var hydrationMS = performance.now() - started;
    await new Promise(function(resolve) { setTimeout(resolve, 0); });
    var initialMaxLongTask = longTasks.length ? Math.max.apply(Math, longTasks) : 0;
    var tools = container.querySelectorAll('.stream-tool');
    if (tools.length !== 117) fail('expected 117 tool cards, got ' + tools.length + ': ' + container.innerHTML.substring(0, 500));
    var toggles = Array.from(container.querySelectorAll('.stream-tool-output-toggle'));
    if (toggles.length !== 115) fail('expected 115 vertically overflowing OUT toggles, got ' + toggles.length);
    var collapsed = toggles.filter(function(button) { return button.getAttribute('aria-expanded') === 'false'; });
    if (collapsed.length !== 115) fail('all overflowing OUT rows must start collapsed');
    collapsed.forEach(function(button) {
      var content = button.closest('.stream-tool-body-content');
      if (content.querySelector('.stream-tool-body-scroll')) fail('collapsed output materialized the full scroll surface');
      var preview = content.querySelector('pre.stream-tool-output-preview');
      if (!preview || !preview.textContent.trim()) fail('collapsed output did not use the standard preformatted output presentation');
      if (preview.textContent.length > 4100) fail('collapsed output preview was not bounded');
      if (preview.nextElementSibling !== button) fail('collapsed output toggle must follow the visible output');
    });
    if (container.querySelectorAll('.stream-tool-body-scroll[data-tool-row="out"] > pre').length !== 2) fail('only short and horizontal one-line OUT values should initially materialize');
    if (container.querySelector('[data-tool-output]')) fail('tool output must not be duplicated in data attributes');
    var shortPre = Array.from(container.querySelectorAll('.stream-tool-body-scroll > pre')).find(function(pre) { return pre.textContent === 'short output'; });
    var widePre = Array.from(container.querySelectorAll('.stream-tool-body-scroll > pre')).find(function(pre) { return pre.textContent.length === wideOutput.length; });
    if (!shortPre || !widePre) fail('short or horizontally wide one-line output was collapsed');
    var bareCRTool = tools[116];
    var bareCRToggle = bareCRTool && bareCRTool.querySelector('.stream-tool-output-toggle');
    var bareCRPreview = bareCRTool && bareCRTool.querySelector('pre.stream-tool-output-preview');
    if (!bareCRToggle || bareCRToggle.textContent !== 'Show output (40 lines)' || bareCRToggle.closest('.stream-tool-body-content').querySelector('.stream-tool-body-scroll')) fail('vertically overflowing bare-CR output did not start collapsed');
    if (!bareCRPreview || bareCRPreview.textContent.indexOf('bare-cr row\rbare-cr row') !== 0) fail('bare-CR output preview did not preserve actual output');
    if (!container.querySelector('a[href="/tasks/assistant%2Fcreate"]') || !container.querySelector('a[href="/tasks/assistant%2Fedit"]')) fail('assistant task links were not hydrated');

    var largeToggle = toggles.find(function(button) { return button.textContent === 'Show output (' + largeLineCount + ' lines)'; });
    if (!largeToggle || largeToggle.tagName !== 'BUTTON' || largeToggle.type !== 'button') fail('large output toggle is not an accessible native button');
    var largeOutputContainer = largeToggle.closest('.stream-tool-body-content');
    var largeToolName = largeToggle.closest('.stream-tool').querySelector('.tool-name-text');
    if (!largeOutputContainer || largeOutputContainer.className !== 'stream-tool-body-content') fail('large output toggle is not contained by the original OUT cell');
    var darkToggleColor = window.getComputedStyle(largeToggle).color;
    if (darkToggleColor === 'rgb(255, 255, 255)' || darkToggleColor === 'rgba(255, 255, 255, 1)') fail('dark output toggle foreground must be muted rather than white');
    var largePreview = largeToggle.closest('.stream-tool-body-content').querySelector('pre.stream-tool-output-preview');
    var largeToolBody = largeToggle.closest('.stream-tool-body');
    var darkToolBodyStyle = window.getComputedStyle(largeToolBody);
    if (darkToolBodyStyle.backgroundColor === 'rgb(37, 42, 50)' || darkToolBodyStyle.borderTopColor === 'rgb(65, 72, 84)') fail('dark tool container retained the task-added outer card styling');
    var previewStyle = window.getComputedStyle(largePreview);
    if (previewStyle.borderTopStyle === 'none' || parseFloat(previewStyle.borderTopLeftRadius) <= 0) fail('dark collapsed OUT preview lost the original bordered rounded surface');
    var previewFontSignature = [previewStyle.fontFamily, previewStyle.fontSize, previewStyle.fontWeight, previewStyle.fontStyle, previewStyle.lineHeight, previewStyle.letterSpacing].join('|');
    var renderVersion = container._streamRenderVersion;
    var expansionStarted = performance.now();
    largeToggle.focus();
    largeToggle.click();
    await waitFor(function() {
      var pre = largeToggle.closest('.stream-tool-body-content').querySelector('.stream-tool-body-scroll > pre');
      return pre && pre.textContent.indexOf('Coded boundary edit') !== -1;
    }, 1000);
    var expandedContent = largeToggle.closest('.stream-tool-body-content');
    var expansionMS = performance.now() - expansionStarted;
    if (largeToggle.getAttribute('aria-expanded') !== 'true' || !expandedContent.querySelector('.stream-tool-body-scroll > pre')) fail('expansion did not materialize the styled OUT subtree');
    var expandedSurface = expandedContent.querySelector('.stream-tool-body-scroll');
    if (!expandedSurface || expandedSurface.nextElementSibling !== largeToggle) fail('expanded output toggle moved above the visible output');
    var expandedPre = expandedSurface.querySelector('pre');
    var expandedStyle = window.getComputedStyle(expandedPre);
    var expandedFontSignature = [expandedStyle.fontFamily, expandedStyle.fontSize, expandedStyle.fontWeight, expandedStyle.fontStyle, expandedStyle.lineHeight, expandedStyle.letterSpacing].join('|');
    if (expandedFontSignature !== previewFontSignature) fail('collapsed and expanded output font styling differs: ' + previewFontSignature + ' vs ' + expandedFontSignature);
    var inputPre = container.querySelector('.stream-tool-body-scroll[data-tool-row="in"] > pre');
    if (!inputPre) fail('Bash tool input did not render an IN text surface');
    var inputStyle = window.getComputedStyle(inputPre);
    var inputFontSignature = [inputStyle.fontFamily, inputStyle.fontSize, inputStyle.fontWeight, inputStyle.fontStyle, inputStyle.lineHeight, inputStyle.letterSpacing].join('|');
    if (inputFontSignature !== expandedFontSignature) fail('tool IN and OUT font styling differs: ' + inputFontSignature + ' vs ' + expandedFontSignature);
    var expandedPreview = expandedContent.querySelector('.stream-tool-output-preview');
    if (!expandedPreview || !expandedPreview.hidden) fail('expansion did not hide the collapsed output preview');
    if (container._streamRenderVersion !== renderVersion) fail('expanding one OUT rerendered the response');
    if (!expandedContent.querySelector('a[href="/tasks/boundary%2Fcreate"]') || !expandedContent.querySelector('a[href="/tasks/boundary%2Fedit"]')) {
      fail('chunk-boundary tool task links were not hydrated; links=' + Array.from(expandedContent.querySelectorAll('a')).map(function(anchor) { return anchor.getAttribute('href'); }).join(',') + '; preNodes=' + expandedContent.querySelector('.stream-tool-body-scroll > pre').childNodes.length);
    }
    if (expandedContent.querySelector('a[href="/tasks/coded%2Fboundary-create"]') || expandedContent.querySelector('a[href="/tasks/coded%2Fboundary-edit"]')) fail('cross-chunk Markdown-code task marker became a link');
    Array.from(container.querySelectorAll('a')).forEach(function(anchor) { if (/\r|\n/.test(anchor.textContent)) fail('anchor text contains a newline'); });

    await Promise.resolve(window.renderStreamingContent(container, transcript, true));
    largeToggle = Array.from(container.querySelectorAll('.stream-tool-output-toggle')).find(function(button) { return button.textContent === 'Hide output (' + largeLineCount + ' lines)'; });
    if (!largeToggle || !largeToggle.closest('.stream-tool-body-content').querySelector('pre')) fail('rerender did not preserve expanded state');
    var collapseStarted = performance.now();
    largeToggle.click();
    var collapseMS = performance.now() - collapseStarted;
    if (largeToggle.getAttribute('aria-expanded') !== 'false' || largeToggle.closest('.stream-tool-body-content').querySelector('.stream-tool-body-scroll')) fail('collapse did not release the full large DOM surface');
    var collapsedPreview = largeToggle.closest('.stream-tool-body-content').querySelector('pre.stream-tool-output-preview');
    if (!collapsedPreview || collapsedPreview.hidden || !collapsedPreview.textContent.trim()) fail('collapse did not restore the output preview');
    document.documentElement.setAttribute('data-theme', 'light');
    window.dispatchEvent(new Event('resize'));
    await Promise.resolve(window.renderStreamingContent(container, transcript, true));
    largeToggle = Array.from(container.querySelectorAll('.stream-tool-output-toggle')).find(function(button) { return button.textContent === 'Show output (' + largeLineCount + ' lines)'; });
    if (!largeToggle || largeToggle.closest('.stream-tool-body-content').querySelector('.stream-tool-body-scroll')) fail('rerender did not preserve collapsed state');
    largeToolName = largeToggle.closest('.stream-tool').querySelector('.tool-name-text');
    if (!largeToolName || window.getComputedStyle(largeToggle).color !== window.getComputedStyle(largeToolName).color) fail('light output toggle foreground does not match its tool call');
    largePreview = largeToggle.closest('.stream-tool-body-content').querySelector('pre.stream-tool-output-preview');
    previewStyle = window.getComputedStyle(largePreview);
    if (previewStyle.borderTopStyle !== 'none' || parseFloat(previewStyle.borderTopWidth) !== 0) fail('light collapsed OUT preview retained border emphasis');
    var lightOutputContent = largeToggle.closest('.stream-tool-body-content');
    var lightOutputContentStyle = window.getComputedStyle(lightOutputContent);
    var lightInputPre = container.querySelector('.stream-tool-body-scroll[data-tool-row="in"] > pre');
    var lightInputStyle = window.getComputedStyle(lightInputPre);
    var lightToggleStyle = window.getComputedStyle(largeToggle);
    if (lightOutputContentStyle.backgroundColor !== 'rgba(0, 0, 0, 0)') fail('light OUT content cell still paints a shared background behind preview and toggle: ' + lightOutputContentStyle.backgroundColor);
    if (lightInputStyle.borderTopStyle !== 'none' || parseFloat(lightInputStyle.borderTopWidth) !== 0) fail('light IN surface retained border emphasis');
    if (parseFloat(lightInputStyle.borderTopLeftRadius) <= 0 || parseFloat(previewStyle.borderTopLeftRadius) <= 0) fail('light IN/OUT surfaces lost their separate rounded geometry');
    if (lightInputStyle.backgroundColor === 'rgba(0, 0, 0, 0)' || previewStyle.backgroundColor === 'rgba(0, 0, 0, 0)') fail('light IN/OUT surfaces must remain visually separate from the transparent content cell');
    if (lightToggleStyle.backgroundColor !== 'rgba(0, 0, 0, 0)') fail('light resting output toggle must remain visually outside the IN/OUT surfaces');
    var lightThinking = container.querySelector('details.stream-thinking');
    var lightThinkingSummary = lightThinking && lightThinking.querySelector('summary');
    var lightThinkingBody = lightThinking && lightThinking.querySelector('.stream-thinking-body');
    if (!lightThinkingSummary || !lightThinkingBody) fail('light Thinking control did not render');
    if (window.getComputedStyle(lightThinkingSummary).color !== 'rgb(97, 97, 97)') fail('collapsed light Thinking label is not readable muted text: ' + window.getComputedStyle(lightThinkingSummary).color);
    lightThinking.open = true;
    if (window.getComputedStyle(lightThinkingSummary).color !== 'rgb(31, 31, 31)') fail('expanded light Thinking label is not readable strong text: ' + window.getComputedStyle(lightThinkingSummary).color);
    if (window.getComputedStyle(lightThinkingBody).color !== 'rgb(59, 59, 59)') fail('expanded light Thinking content is not readable: ' + window.getComputedStyle(lightThinkingBody).color);

    result.setAttribute('data-test-result', 'pass');
    result.setAttribute('data-hydration-ms', hydrationMS.toFixed(1));
    result.setAttribute('data-max-long-task-ms', initialMaxLongTask.toFixed(1));
    result.setAttribute('data-expansion-ms', expansionMS.toFixed(1));
    result.setAttribute('data-collapse-ms', collapseMS.toFixed(1));
  }).catch(function(error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  });
});
</script>`
	html += fixture + "</body></html>"
	fixtureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer fixtureServer.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chrome-stdout.html")
	stderrPath := filepath.Join(t.TempDir(), "chrome-stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout file: %v", err)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr file: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"),
		"--window-size=1280,900",
		"--virtual-time-budget=10000",
		"--dump-dom",
		fixtureServer.URL,
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome fixture: %v", err)
	}
	deadline := time.Now().Add(25 * time.Second)
	var result string
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopTestBrowserProcess(cmd)
	if !strings.Contains(result, `data-test-result="pass"`) {
		rootState := regexp.MustCompile(`<main id="fixture-root"[^>]*>`).FindString(result)
		stderr, _ := os.ReadFile(stderrPath)
		if len(result) > 4000 {
			result = result[len(result)-4000:]
		}
		if len(stderr) > 4000 {
			stderr = stderr[len(stderr)-4000:]
		}
		t.Fatalf("large tool-output browser fixture failed:\nFixture state: %s\nDOM tail:\n%s\nChrome stderr tail:\n%s", rootState, result, stderr)
	}
	metrics := regexp.MustCompile(`data-hydration-ms="([^"]+)" data-max-long-task-ms="([^"]+)" data-expansion-ms="([^"]+)" data-collapse-ms="([^"]+)"`).FindStringSubmatch(result)
	if len(metrics) != 5 {
		t.Fatalf("synthetic browser fixture did not report performance metrics")
	}
	hydrationMS, hydrationErr := strconv.ParseFloat(metrics[1], 64)
	maxLongTaskMS, longTaskErr := strconv.ParseFloat(metrics[2], 64)
	expansionMS, expansionErr := strconv.ParseFloat(metrics[3], 64)
	collapseMS, collapseErr := strconv.ParseFloat(metrics[4], 64)
	if hydrationErr != nil || longTaskErr != nil || expansionErr != nil || collapseErr != nil {
		t.Fatalf("parse synthetic browser metrics: hydration=%v long-task=%v expansion=%v collapse=%v", hydrationErr, longTaskErr, expansionErr, collapseErr)
	}
	if hydrationMS > 2000 {
		t.Fatalf("synthetic large hydration exceeded 2s acceptance ceiling: %.1f ms", hydrationMS)
	}
	const maxHydrationSliceMS = 75.0
	if maxLongTaskMS > maxHydrationSliceMS {
		t.Fatalf("synthetic hydration exceeded %.0fms slice ceiling: %.1f ms", maxHydrationSliceMS, maxLongTaskMS)
	}
	if expansionMS > 200 || collapseMS > 200 {
		t.Fatalf("tool output interaction exceeded 200ms target: expansion %.1f ms, collapse %.1f ms", expansionMS, collapseMS)
	}
	t.Logf("synthetic large hydration: %.1f ms, max observed long task: %.1f ms, expansion: %.1f ms, collapse: %.1f ms", hydrationMS, maxLongTaskMS, expansionMS, collapseMS)
}

func TestChatAutoScrollScript_ToolOutputRendersAllTypesAndPreservesScroll(t *testing.T) {
	var buf bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &buf); err != nil {
		t.Fatalf("render chat auto-scroll script: %v", err)
	}
	content := buf.String()

	required := []string{
		"var prevToolBodyScrollStates = {}",
		"container.querySelectorAll('.stream-tool-body-scroll').forEach(function(el)",
		"var toolID = el.getAttribute('data-tool-render-id') || ''",
		"var rowKind = el.getAttribute('data-tool-row') || ''",
		"var pinned = el.getAttribute('data-scroll-pinned') !== 'false'",
		"function trackScrollPin(el, stateKey)",
		"el.addEventListener('scroll'",
		"el.setAttribute('data-scroll-pinned', pinned ? 'true' : 'false')",
		"inScroll.className = 'stream-tool-body-scroll'",
		"inScroll.setAttribute('data-tool-render-id', seg.toolRenderID || '')",
		"inPre.className = 'stream-tool-output-text'",
		"el.scrollTop = Number.MAX_SAFE_INTEGER",
		"el.scrollTop = state.scrollTop",
		"var outputText = seg.resultOutput ? seg.resultOutput.trim() : ''",
		"var hasOut = outputText !== ''",
		"outVal.className = 'stream-tool-body-content'",
		"function toolOutputLineCount(text)",
		"function toolOutputPreview(text)",
		"var maxLines = 6",
		"var maxChars = 4096",
		"preview = document.createElement('pre')",
		"preview.className = 'stream-tool-output-preview stream-tool-output-text'",
		"outPre.className = 'stream-tool-output-text'",
		"function resolveToolOutputLineCapacity()",
		"probe.className = 'stream-tool'",
		"probeBody.className = 'stream-tool-body'",
		"probeGrid.className = 'stream-tool-body-grid'",
		"probeRow.className = 'stream-tool-body-row'",
		"probeContent.className = 'stream-tool-body-content'",
		"probeScroll.className = 'stream-tool-body-scroll'",
		"var shouldCollapse = lineCount > 1 && lineCount > resolveToolOutputLineCapacity()",
		"toggle.type = 'button'",
		"toggle.setAttribute('aria-expanded', expanded ? 'true' : 'false')",
		"toggle.textContent = (expanded ? 'Hide output (' : 'Show output (') + lineCount + ' lines)'",
		"function materializeOutput(useInitialChunkQueue)",
		"outVal.removeChild(outScroll)",
		"window.convertTaskLinksInMessage(outVal)",
		"window.convertTaskEditLinksInMessage(outVal)",
		"window._toolOutputRowStates[stateKey]",
	}
	for _, fragment := range required {
		if !strings.Contains(content, fragment) {
			t.Fatalf("expected tool output renderer to contain %q", fragment)
		}
	}

	forbidden := []string{
		"suppressOut",
		"dn === 'Read' || dn === 'List Files' || dn === 'Write'",
		"Don't show output body for Read/List Files/Write",
		"setAttribute('data-tool-output'",
		"setAttribute(\"data-tool-output\"",
	}
	for _, fragment := range forbidden {
		if strings.Contains(content, fragment) {
			t.Fatalf("tool output renderer must not suppress non-empty outputs for any tool type; found %q", fragment)
		}
	}
	for _, fragment := range []string{"el.scrollHeight > el.clientHeight", "el.scrollTop = el.scrollHeight"} {
		if strings.Contains(content, fragment) {
			t.Fatalf("render pass must not synchronously measure every tool scroller; found %q", fragment)
		}
	}
}
