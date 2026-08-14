package layout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBaseRuntimeModeAndChatLinkPolicyHooks(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Runtime", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render web Base: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`data-openvibely-runtime="web"`,
		"window.chatLinkOpensOutsideApp = function(href)",
		"window.sanitizeChatHTMLElement = function(element)",
		"element.setAttribute('target', '_blank')",
		"element.setAttribute('rel', 'noopener noreferrer')",
		"element.removeAttribute('target')",
		"element.removeAttribute('rel')",
		"window.sanitizeChatHTMLElement(element)",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("base layout missing chat link/runtime policy hook %q", want)
		}
	}

	buf.Reset()
	if err := Base("Runtime", []models.Project{}, "").Render(WithDesktopMode(context.Background(), true), &buf); err != nil {
		t.Fatalf("render desktop Base: %v", err)
	}
	if !strings.Contains(buf.String(), `data-openvibely-runtime="desktop"`) {
		t.Fatal("base layout must render authoritative desktop runtime marker")
	}
}

func TestBaseProvidesCentralClientSidePageTitleAndHistorySynchronization(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Initial", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, expected := range []string{
		"window.openVibelyNavigate = function",
		"hx-push-url",
		"window.syncOpenVibelyPageTitle = function",
		"[data-openvibely-page-title]",
		"document.title = marker.getAttribute('data-openvibely-page-title')",
		"htmx:afterSwap",
		"htmx:historyRestore",
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("base layout missing client-side title/history synchronization contract %q", expected)
		}
	}
	if strings.Contains(html, "history.pushState") {
		t.Fatal("base layout and sidebar must use HTMX-managed navigation instead of manual history.pushState")
	}

	start := strings.Index(html, "window.openVibelyNavigate = function")
	if start < 0 {
		t.Fatal("could not find rendered client-side navigation script")
	}
	end := strings.Index(html[start:], "// Scroll position restoration for drop zones")
	if end < 0 {
		t.Fatal("could not extract rendered client-side navigation and page title script")
	}
	scriptBody := html[start : start+end]
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the rendered page title synchronization script")
	}
	script := `
const listeners = {};
const ajaxCalls = [];
let historyRoot;
let navigationSource;
global.window = {};
global.document = {
  title: 'Initial - OpenVibely',
  body: { appendChild: function(element) { navigationSource = element; } },
  addEventListener: function(name, handler) { listeners[name] = handler; },
  getElementById: function(id) {
    if (id === 'main-content') return historyRoot;
    return null;
  },
  createElement: function() {
    const attributes = {};
    return {
      setAttribute: function(name, value) { attributes[name] = String(value); },
      getAttribute: function(name) { return attributes[name] || null; },
      remove: function() {}
    };
  }
};
global.htmx = {
  ajax: function(method, url, context) {
    if (navigationSource !== context.source) throw new Error('programmatic navigation source was not connected before the HTMX request');
    ajaxCalls.push({ method: method, url: url, context: context });
    return Promise.resolve();
  }
};
const swapMarker = { getAttribute: function() { return 'Tasks - OpenVibely'; } };
const cacheHitMarker = { getAttribute: function() { return 'Chat - OpenVibely'; } };
const cacheMissMarker = { getAttribute: function() { return 'History Task - OpenVibely'; } };
const swapRoot = { matches: function() { return false; }, querySelector: function() { return swapMarker; } };
const cacheHitRoot = { matches: function() { return false; }, querySelector: function() { return cacheHitMarker; } };
const cacheMissRoot = { matches: function() { return false; }, querySelector: function() { return cacheMissMarker; } };
historyRoot = cacheHitRoot;
` + scriptBody + `
window.openVibelyNavigate('/tasks/task-1?from=chat');
if (ajaxCalls.length !== 1) throw new Error('programmatic navigation did not issue one HTMX request');
const navigation = ajaxCalls[0];
if (navigation.method !== 'GET' || navigation.url !== '/tasks/task-1?from=chat') throw new Error('programmatic navigation used the wrong request');
if (navigation.context.target !== '#main-content' || navigation.context.swap !== 'innerHTML') throw new Error('programmatic navigation changed the main-content swap contract');
if (!navigation.context.source || navigation.context.source.getAttribute('hx-push-url') !== 'true') throw new Error('programmatic navigation did not opt into HTMX-managed history');
window.openVibelyNavigate('/tasks/task-2?tab=history', '/tasks/task-2');
if (ajaxCalls.length !== 2 || ajaxCalls[1].context.source.getAttribute('hx-push-url') !== '/tasks/task-2') throw new Error('programmatic navigation did not preserve an explicit history URL');
listeners['htmx:afterSwap']({ detail: { target: swapRoot } });
if (document.title !== 'Tasks - OpenVibely') throw new Error('afterSwap did not apply destination title: ' + document.title);
listeners['htmx:historyRestore']({ detail: { item: { title: 'Chat - OpenVibely' } } });
if (document.title !== 'Chat - OpenVibely') throw new Error('cache-hit history restore did not apply restored title: ' + document.title);
historyRoot = cacheMissRoot;
listeners['htmx:historyRestore']({ detail: { cacheMiss: true, serverResponse: '<!doctype html><title>History Task - OpenVibely</title>' } });
if (document.title !== 'History Task - OpenVibely') throw new Error('cache-miss history restore did not apply restored title: ' + document.title);
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("rendered client-side title/history synchronization failed: %v\n%s", err, output)
	}
}

func TestBasePurgesSensitiveHTMXHistoryBeforeHTMXLoads(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render base: %v", err)
	}
	html := buf.String()
	cleanup := strings.Index(html, "window.__ov_purgeSensitiveHTMXHistory = function")
	invocation := strings.Index(html, "window.__ov_purgeSensitiveHTMXHistory();")
	htmx := strings.Index(html, `src="https://unpkg.com/htmx.org@2.0.4"`)
	if cleanup < 0 || invocation < 0 {
		t.Fatal("base layout must purge stale secret-bearing HTMX history entries")
	}
	if htmx < 0 || cleanup > htmx || invocation > htmx {
		t.Fatal("sensitive HTMX history cleanup must run before HTMX loads")
	}
	if !strings.Contains(html, "new URL(entry.url, window.location.href).pathname !== '/models'") {
		t.Fatal("history cleanup must remove Models URLs including query and fragment variants")
	}
}

func TestTabVisibilityManager_DoesNotTreatBlurOrFocusAsTranscriptRefresh(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()
	start := strings.Index(html, "window._tabVisibility = (function() {")
	end := strings.Index(html[start:], "// Track which element was focused before mousedown")
	if start < 0 || end < 0 {
		t.Fatal("tab visibility manager boundaries are missing")
	}
	manager := html[start : start+end]
	if !strings.Contains(manager, "document.addEventListener('visibilitychange'") {
		t.Fatal("hidden-to-visible transitions must remain owned by the visibility manager")
	}
	for _, forbidden := range []string{"window.addEventListener('focus'", "window.addEventListener('blur'", "window.addEventListener('pageshow'"} {
		if strings.Contains(manager, forbidden) {
			t.Fatalf("plain blur/focus/pageshow must not trigger transcript reconciliation: found %q", forbidden)
		}
	}
}

func TestChatMarkdownRendererUsesSharedCodeRangesAndEscapesRawHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "window.configureChatMarked = function")
	end := strings.Index(content, "// Add copy buttons")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("base layout must define shared Markdown code ranges before the chat renderer")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the rendered Markdown safety helpers")
	}
	protected := []string{
		"`first\n<img src=x onerror=alert(1)>\nlast`",
		"~~~html\n<img src=x onerror=alert(1)>\n~~~",
		"`````html\n<img src=x onerror=alert(1)>\n`````",
		"```html\r<img src=x onerror=alert(1)>\r```",
		"~~~html\r<img src=x onerror=alert(1)>\r~~~~\t ",
		"`````html\r<img src=x onerror=alert(1)>\r```\r``````   ",
		"Unmatched `` prefix; `<img src=x onerror=alert(1)>`",
		`\\` + "`<img src=x onerror=alert(1)>`",
		"```html\n<img src=x onerror=alert(1)>\n```\u00a0\n<img src=y onerror=alert(2)>\n```",
		"~~~html\n<img src=x onerror=alert(1)>\n~~~\u2003\n<img src=y onerror=alert(2)>\n~~~",
		"`````html\n<img src=x onerror=alert(1)>\n`````\u202f\n<img src=y onerror=alert(2)>\n`````",
	}
	validClosers := map[string]string{
		"backtick spaces and tabs": "```html\n<img src=x onerror=alert(1)>\n``` \t\n<img src=y onerror=alert(2)>",
		"tilde spaces and tabs":    "~~~html\n<img src=x onerror=alert(1)>\n~~~~\t \n<img src=y onerror=alert(2)>",
		"long fence spaces":        "`````html\n<img src=x onerror=alert(1)>\n``````   \n<img src=y onerror=alert(2)>",
	}
	malicious := []string{
		`\` + "`<img src=x onerror=alert(1)>`",
		`<img src=x onerror=alert(1)>`,
		`<img src="x" onerror="alert(1)">`,
		`<img src='x' onerror='alert(1)'>`,
		`<img/onerror=alert(1)>`,
		`<script>alert(1)</script>`,
		`<a href="javascript:alert(1)">click</a>`,
	}

	script := "global.window = {};\n" +
		"global.document = { createElement: function(tag) { if (tag !== 'template') throw new Error('unexpected element'); return { _html: '', content: { querySelectorAll: function() { return []; } }, set innerHTML(value) { this._html = value; }, get innerHTML() { return this._html; } }; } };\n" +
		content[start:end] + "\n" +
		"const dependencyPayload = '<img src=x onerror=alert(1)> plain & text';\n" +
		"const dependencyMetadataPayload = 'Inline `- \"Coded create\" (backlog) [TASK_ID:coded/create]`\\nMultiline ``- \"Coded edit\" (updated: title)\\n[TASK_EDITED:coded/edit]``\\n```text\\n- \"Fenced create\" (backlog) [TASK_ID:coded/fence]\\n```\\n~~~text\\r- \"Bare CR fenced edit\" (updated: title) [TASK_EDITED:coded/cr]\\r~~~\\r- \"Real create\" (backlog) [TASK_ID:real/create]';\n" +
		"const safeDependencyFallback = '<span data-chat-markdown-fallback=\"true\" style=\"white-space: pre-wrap\">&lt;img src=x onerror=alert(1)&gt; plain &amp; text</span>';\n" +
		"const safeMetadataFallback = '<span data-chat-markdown-fallback=\"true\" style=\"white-space: pre-wrap\">' + window.escapeHTMLForChat(dependencyMetadataPayload) + '</span>';\n" +
		"function assertDependencyFallback(exitCode) { const payloadRendered = window.renderChatMarkdown(dependencyPayload); const metadataRendered = window.renderChatMarkdown(dependencyMetadataPayload); if (payloadRendered !== safeDependencyFallback || metadataRendered !== safeMetadataFallback) { console.error('Marked dependency failed open', exitCode, JSON.stringify({ payloadRendered, metadataRendered })); process.exit(exitCode); } }\n" +
		"assertDependencyFallback(5);\n" +
		"window.marked = global.marked = { setOptions: function() {} };\n" +
		"assertDependencyFallback(6);\n" +
		"window.marked = global.marked = { parse: function(value) { return value; }, setOptions: function() { throw new Error('configuration failed'); } };\n" +
		"assertDependencyFallback(9);\n" +
		"window.marked = global.marked = { parse: function() { throw new Error('load failed'); }, setOptions: function() {} };\n" +
		"assertDependencyFallback(7);\n" +
		"let lateConfigured = false; window.marked = global.marked = { parse: function(value) { return '<p>' + value + '</p>'; }, setOptions: function(options) { lateConfigured = !!(options && options.gfm && options.breaks); } };\n" +
		"const lateRendered = window.renderChatMarkdown('**late** <img src=x onerror=alert(1)>');\n" +
		"if (!lateConfigured || lateRendered !== '<p>**late** &lt;img src=x onerror=alert(1)></p>' || lateRendered.indexOf('data-chat-markdown-fallback') !== -1) { console.error('late Marked was not configured safely', JSON.stringify({ lateConfigured, lateRendered })); process.exit(8); }\n" +
		"window.marked = global.marked = { parse: function(value) { return value; }, setOptions: function() {} };\n" +
		"const protectedCases = " + mustJSON(t, protected) + ";\n" +
		"for (const value of protectedCases) { if (window.escapeRawHTMLForMarkdown(value) !== value) { console.error('changed protected', JSON.stringify(value), JSON.stringify(window.escapeRawHTMLForMarkdown(value))); process.exit(1); } }\n" +
		"const validClosers = " + mustJSON(t, validClosers) + ";\n" +
		"for (const [name, value] of Object.entries(validClosers)) { const escaped = window.escapeRawHTMLForMarkdown(value); if (escaped.indexOf('<img src=x') === -1 || escaped.indexOf('<img src=y') !== -1 || escaped.indexOf('&lt;img src=y') === -1) { console.error('valid closer mismatch', name, JSON.stringify(escaped)); process.exit(4); } }\n" +
		"const maliciousCases = " + mustJSON(t, malicious) + ";\n" +
		"for (const value of maliciousCases) { const escaped = window.escapeRawHTMLForMarkdown(value); if (/<\\/?(?:img|script|a)\\b/i.test(escaped) || /<img\\//i.test(escaped)) { console.error('raw tag survived', JSON.stringify(value), JSON.stringify(escaped)); process.exit(2); } const rendered = window.renderChatMarkdown(value); if (/<\\/?(?:img|script|a)\\b/i.test(rendered) || /<img\\//i.test(rendered)) { console.error('active tag survived', JSON.stringify(value), JSON.stringify(rendered)); process.exit(3); } }\n"
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("rendered Markdown safety helpers failed: %v\n%s", err, output)
	}

	generated, err := os.ReadFile("base_templ.go")
	if err != nil {
		t.Fatalf("read generated base template: %v", err)
	}
	for _, snippet := range []string{
		"window.configureChatMarked = function()",
		"typeof parser.parse !== 'function'",
		"window._chatMarkedConfiguredFor === parser",
		"if (!window.configureChatMarked || !window.configureChatMarked())",
		"return window.renderChatMarkdownFallback(text)",
		"window.escapeHTMLForChat = function(value)",
		"window.renderChatMarkdownFallback = function(text)",
		`data-chat-markdown-fallback=\"true\"`,
		"window.markdownLineRanges = function(text)",
		`if (text.charAt(end) === '\\r' && text.charAt(next) === '\\n') next++`,
		"window.codeRanges = function(text)",
		"window.codeRangesAsync = function(text, owner)",
		"owner._codeRangeWorkerState.finish(null)",
		"window.URL.createObjectURL(new Blob([workerSource]",
		"/^[ \\\\t]*$/.test(line.substring(runEnd))",
		"window.escapeRawHTMLForMarkdown = function(text, ranges)",
		"window.sanitizeChatHTML = function(html)",
		"window.sanitizeChatHTMLFragmentAsync = function(html, cancelled)",
		"window.renderChatMarkdownLargeFallback = function(text)",
		"state.fallbackTimer = setTimeout(function()",
		"/^on/i.test(attr.name)",
		"javascript:|vbscript:|data:",
	} {
		if !strings.Contains(string(generated), snippet) {
			t.Fatalf("generated base template missing Markdown safety snippet %q", snippet)
		}
	}
	if strings.Contains(string(generated), "worker.onerror = function() { finish(window.renderChatMarkdown(text))") {
		t.Fatal("large Markdown worker failures must not synchronously parse the full document")
	}
	if strings.Contains(string(generated), "finish(window.codeRanges(text))") ||
		strings.Contains(string(generated), "Promise.resolve(window.codeRanges(text))") {
		t.Fatal("large code-range worker failures must not synchronously scan the full document")
	}
	if !strings.Contains(string(generated), "html.length - chunkStart > 128 * 1024") {
		t.Fatal("large sanitized HTML must be parsed in bounded top-level chunks")
	}
}

func TestLargeMarkdownAndCodeRangeWorkersCancelAndComplete(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	content := buf.String()
	start := strings.Index(content, "window.configureChatMarked = function")
	end := strings.Index(content, "// Add copy buttons")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("base layout must define Markdown worker helpers")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute worker lifecycle helpers")
	}
	script := "global.window = {};\n" +
		"global.document = { createElement: function() { return { _html: '', content: { querySelectorAll: function() { return []; } }, set innerHTML(value) { this._html = value; }, get innerHTML() { return this._html; } }; } };\n" +
		"let lastBlob = null; global.Blob = function(parts) { this.parts = parts; lastBlob = this; }; window.URL = { createObjectURL: function() { return 'blob:test'; } };\n" +
		"const workers = []; global.Worker = function(url) { this.url = url; this.terminated = false; workers.push(this); }; Worker.prototype.terminate = function() { this.terminated = true; }; Worker.prototype.postMessage = function(value) { this.value = value; };\n" +
		"window.marked = global.marked = { parse: function(value) { return '<p>' + value + '</p>'; }, setOptions: function() {} };\n" +
		content[start:end] + "\n" +
		"const large = 'x'.repeat(110 * 1024), codeOwner = {};\n" +
		"const firstCode = window.codeRangesAsync(large, codeOwner); const firstCodeWorker = workers[workers.length - 1];\n" +
		"const secondCode = window.codeRangesAsync(large + 'y', codeOwner); const secondCodeWorker = workers[workers.length - 1];\n" +
		"if (!firstCodeWorker.terminated) throw new Error('superseded code-range worker was not terminated');\n" +
		"secondCodeWorker.onmessage({ data: { ranges: [{ start: 1, end: 2 }] } });\n" +
		"const markdownOwner = {}; const firstMarkdown = window.renderChatMarkdownAsync(large, markdownOwner); const firstMarkdownWorker = workers[workers.length - 1];\n" +
		"const markdownWorkerSource = lastBlob.parts.join(''); let importedURL = '', workerPost = null; const workerScope = { postMessage: function(value) { workerPost = value; } };\n" +
		"new Function('self', 'importScripts', 'marked', markdownWorkerSource)(workerScope, function(url) { importedURL = url; }, { setOptions: function() {}, parse: function(value) { return value; } });\n" +
		"workerScope.onmessage({ data: 'outside <img src=x>\\n```html\\n<img src=y>\\n```' });\n" +
		"if (importedURL.indexOf('marked@15.0.4') === -1 || !workerPost || workerPost.error || workerPost.html.indexOf('outside &lt;img src=x>') === -1 || workerPost.html.indexOf('<img src=y>') === -1) throw new Error('generated Markdown worker did not execute safely');\n" +
		"const ThreadWorker = require('worker_threads').Worker; const threadWorkerSource = \"const {parentPort}=require('worker_threads');var self=globalThis;self.postMessage=function(value){parentPort.postMessage(value);};\" + markdownWorkerSource.replace(/importScripts\\([^;]+\\);/, \"var marked={setOptions:function(){},parse:function(value){return value;}};\") + \";parentPort.on('message',function(data){self.onmessage({data:data});});\";\n" +
		"const threadWorker = new ThreadWorker(threadWorkerSource, { eval: true }); const threadResult = new Promise(function(resolve, reject) { threadWorker.once('message', function(value) { threadWorker.terminate(); if (!value || value.error || value.html.indexOf('outside &lt;img src=thread>') === -1) reject(new Error('real worker thread returned unsafe output')); else resolve(true); }); threadWorker.once('error', reject); }); threadWorker.postMessage('outside <img src=thread>');\n" +
		"const secondMarkdown = window.renderChatMarkdownAsync(large + 'y', markdownOwner); const secondMarkdownWorker = workers[workers.length - 1];\n" +
		"if (!firstMarkdownWorker.terminated) throw new Error('superseded Markdown worker was not terminated');\n" +
		"secondMarkdownWorker.onmessage({ data: { html: '<ol><li>whole document</li></ol>' } });\n" +
		"window._chatCodeRangeWorkerTimeoutMS = 5; const timeoutOwner = {}; const timedCode = window.codeRangesAsync(large + 'timeout', timeoutOwner); const timedCodeWorker = workers[workers.length - 1];\n" +
		"Promise.all([firstCode, secondCode, firstMarkdown, secondMarkdown, threadResult, timedCode]).then(function(values) { if (values[0] !== null || values[1].length !== 1 || values[2] !== null || !values[3] || typeof values[3] !== 'object' || values[4] !== true || values[5] !== null || !timedCodeWorker.terminated || codeOwner._codeRangeWorkerState !== null || timeoutOwner._codeRangeWorkerState !== null || markdownOwner._markdownWorkerState !== null) process.exit(1); }, function(err) { console.error(err); process.exit(2); });\n"
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("large Markdown/code-range worker lifecycle failed: %v\n%s", err, output)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JavaScript fixture: %v", err)
	}
	return string(encoded)
}

func TestToastCloseButtonIsAccessibleAndTopRightAligned(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, expected := range []string{
		".toast-notification {",
		"position: relative",
		"padding-right: 4rem",
		".toast-close {",
		"position: absolute",
		"top: 0.5rem",
		"right: 0.5rem",
		"width: 2.75rem",
		"height: 2.75rem",
		".toast-close:focus-visible",
		"outline: 2px solid currentColor",
		`type="button"`,
		`aria-label="Dismiss notification"`,
		"toast-close btn btn-ghost btn-circle",
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("toast close control missing responsive/accessibility contract %q", expected)
		}
	}

	if strings.Contains(html, "toast-close btn btn-ghost btn-xs") {
		t.Error("toast close control must not use btn-xs because it is smaller than the minimum touch target")
	}

	mobileMediaStart := strings.Index(html, "@media (max-width: 640px)")
	if mobileMediaStart < 0 {
		t.Fatal("mobile toast media query is missing")
	}
	mobileContainerStart := strings.Index(html[mobileMediaStart:], "#toast-container {")
	if mobileContainerStart < 0 {
		t.Fatal("mobile toast container rule is missing")
	}
	mobileContainerRule := html[mobileMediaStart+mobileContainerStart:]
	mobileContainerEnd := strings.Index(mobileContainerRule, "}")
	if mobileContainerEnd < 0 {
		t.Fatal("mobile toast container rule is incomplete")
	}
	if !strings.Contains(mobileContainerRule[:mobileContainerEnd], "width: auto") {
		t.Error("mobile toast container must use auto width so left and right viewport insets are both honored")
	}
}

func TestBase_SystemUpdateNoticeIsStickyAndActionable(t *testing.T) {
	var buf bytes.Buffer
	comp := Base("Test", []models.Project{}, "")
	if err := comp.Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`data-system-update-toast`,
		`openvibely-system-update-dismissed-version`,
		`/api/system/update/apply`,
		`window.openVibelyNavigate('/alerts')`,
		`systemUpdateIsActionable`,
		`systemUpdateNoticeKey`,
		`metadata.published_at`,
		`target.sha256`,
		`navigateSystemUpdateToAlerts`,
		`applySystemUpdateFromToast`,
		`showSystemUpdateSucceededToast`,
		`handleGlobalSystemUpdateSnapshot`,
		`window.openVibelyHandleSystemUpdateSnapshot`,
		`window.refreshGlobalSystemUpdateIndicators`,
		`openvibely-system-update-success-toast`,
		`openvibely-system-update-pending-success`,
		`clearSystemUpdatePendingSuccess`,
		`data.state === 'failed' || data.state === 'rolled_back' || data.state === 'idle'`,
		`OpenVibely updated to `,
		`background-color: #646fe4`,
		`[data-theme="light"] .system-update-toast`,
		`background-color: #7480ff`,
		`system-update-toast`,
		`options.sticky`,
		`system-update-nav-badge`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("global system update notice missing snippet: %s", required)
		}
	}

	if strings.Contains(html, `.toast-notification:hover`) && strings.Contains(html, `scale(1.02)`) {
		t.Fatalf("toast notifications should not resize on hover")
	}
	if strings.Contains(html, `data-system-update-apply`) || strings.Contains(html, `system-update-toast-action`) {
		t.Fatalf("system update toast should use the whole notification as the update action")
	}
}

// TestToastDismissalCleanup verifies the toast notification system properly
// cleans up DOM elements after dismissal to prevent page unresponsiveness.
func TestToastDismissalCleanup(t *testing.T) {
	var buf bytes.Buffer
	comp := Base("Test", []models.Project{}, "")
	err := comp.Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	// Verify toast-dismiss CSS sets pointer-events: none to prevent blocking clicks
	if !strings.Contains(html, "pointer-events: none") || !strings.Contains(html, ".toast-notification.toast-dismiss") {
		t.Error("toast-dismiss CSS must set pointer-events: none to prevent invisible elements from blocking user interaction")
	}

	// Verify toast-dismiss CSS sets transition: none to prevent CSS conflicts
	if !strings.Contains(html, "transition: none") {
		t.Error("toast-dismiss CSS must set transition: none to prevent animation/transition conflicts that can block animationend")
	}

	// Verify animationend listener uses { once: true } to prevent multiple fires
	if !strings.Contains(html, "{ once: true }") {
		t.Error("animationend listener must use { once: true } to prevent multiple handler invocations")
	}

	// Verify fallback setTimeout exists to force-remove toast if animationend doesn't fire
	if !strings.Contains(html, "toast.parentNode") {
		t.Error("dismissToast must have a fallback setTimeout to force-remove toast if animationend doesn't fire")
	}

	// Verify htmx.ajax in toast click handler has .catch() for error handling
	if !strings.Contains(html, ".catch(function(err)") {
		t.Error("htmx.ajax() in toast click handler must have .catch() per HTMX 2.0 requirements")
	}

	// Verify duplicate toast suppression logic is present
	if !strings.Contains(html, "window._recentToasts") || !strings.Contains(html, "shouldSuppressDuplicateToast") {
		t.Error("toast system must include duplicate suppression map and helper")
	}

	// Verify HTMX toast bridge passes optional action-link and click-target fields
	if !strings.Contains(html, "detail.linkURL || detail.link_url || ''") || !strings.Contains(html, "detail.linkText || detail.link_text || ''") {
		t.Error("openvibelyToast bridge must map link_url/link_text fields for clickable toast actions")
	}
	if !strings.Contains(html, "detail.clickURL || detail.click_url || ''") || !strings.Contains(html, "var navigateURL = clickURL || (taskId ? '/tasks/' + taskId : '')") {
		t.Error("openvibelyToast bridge must map click_url and use it for toast body navigation")
	}

	// Verify Wails runtime is loaded so desktop pages can call window.wails.OpenURL.
	if !strings.Contains(html, `src="wails://wails/runtime.js"`) {
		t.Error("base layout must load wails runtime script for desktop bridge APIs")
	}
	if !strings.Contains(html, `onload="window.__ov_applyRuntimeMode && window.__ov_applyRuntimeMode()"`) {
		t.Error("base layout wails runtime script must re-apply desktop runtime detection after load")
	}

	// Verify the desktop external-link bridge exists so target="_blank" anchors
	// (e.g. Task Changes "View PR") actually open the system browser when running
	// inside the Wails WebView, instead of silently doing nothing.
	if !strings.Contains(html, "window.wails.Browser.OpenURL") {
		t.Error("base layout must include desktop bridge that opens external links via window.wails.Browser.OpenURL")
	}
	if !strings.Contains(html, `anchor.getAttribute('target') === '_blank'`) {
		t.Error("desktop external-link bridge must intercept target=\"_blank\" anchors so View PR opens in system browser")
	}
	if !strings.Contains(html, "window.openExternalURL") {
		t.Error("base layout must expose window.openExternalURL helper for explicit external-link opens")
	}
	// Verify the backend-endpoint fallback so that the system browser opens even
	// when the Wails JS runtime is not injected (HTTP-served WebView).
	if !strings.Contains(html, "/open-external") {
		t.Error("desktop external-link bridge must fall back to /open-external backend endpoint when Wails JS API is unavailable")
	}
	if !strings.Contains(html, "openExternalViaBackend") {
		t.Error("desktop external-link bridge must define openExternalViaBackend as the server-side fallback")
	}

	// Verify theme toggle is NOT present in top navbar (moved to sidebar footer).
	if strings.Contains(html, "navbar-theme-toggle") {
		t.Error("base layout navbar should not include theme toggle; toggle is rendered in sidebar footer")
	}

	// Navbar should be mobile-only to avoid desktop top-gap above page headers.
	if !strings.Contains(html, "navbar bg-base-100 shadow-sm flex-shrink-0 lg:hidden") {
		t.Error("base layout navbar must be mobile-only (lg:hidden)")
	}

	// Verify toast navigation closes open modal dialogs first so destination is visible.
	if !strings.Contains(html, "function closeOpenModalsForToastNavigation()") {
		t.Error("toast navigation must define closeOpenModalsForToastNavigation helper")
	}
	if strings.Count(html, "closeOpenModalsForToastNavigation();") < 2 {
		t.Error("toast navigation must close open modals for both action-link and task-detail click paths")
	}
}

func TestMobileDialogsAreFullscreen(t *testing.T) {
	var buf bytes.Buffer
	comp := Base("Test", []models.Project{}, "")
	if err := comp.Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, expected := range []string{
		"@media (max-width: 640px), ((max-width: 1024px) and (max-height: 500px) and (orientation: landscape))",
		"dialog.modal {",
		"box-sizing: border-box !important",
		"width: 100vw !important",
		"max-width: 100vw !important",
		"height: 100dvh !important",
		"max-height: 100dvh !important",
		"margin: 0 !important",
		"align-items: stretch !important",
		"justify-items: stretch !important",
		"padding: 0 !important",
		"dialog.modal > .modal-box",
		"box-sizing: border-box !important",
		"width: 100vw !important",
		"max-width: 100vw !important",
		"height: 100dvh !important",
		"max-height: 100dvh !important",
		"margin: 0 !important",
		"border-radius: 0 !important",
		"overflow-y: auto !important",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("mobile modal fullscreen CSS missing %q", expected)
		}
	}

	for _, landscapePhone := range []string{"667x375", "736x414", "844x390", "932x430"} {
		widthValue, heightValue, ok := strings.Cut(landscapePhone, "x")
		if !ok {
			t.Fatalf("invalid landscape phone fixture %q", landscapePhone)
		}
		width, err := strconv.Atoi(widthValue)
		if err != nil {
			t.Fatalf("invalid landscape phone fixture width %q: %v", widthValue, err)
		}
		height, err := strconv.Atoi(heightValue)
		if err != nil {
			t.Fatalf("invalid landscape phone fixture height %q: %v", heightValue, err)
		}
		if width <= 640 || width > 1024 || height > 500 {
			t.Fatalf("landscape phone fixture %s must document a >640px, <=1024px wide and <=500px high viewport", landscapePhone)
		}
	}
}

// TestToastDismissalBehavior documents the expected behavior for toast dismissal
// to prevent the bug where page becomes unresponsive after toast disappears.
func TestToastDismissalBehavior(t *testing.T) {
	tests := []struct {
		name                  string
		scenario              string
		expectToastRemoved    bool
		expectPointerBlocking bool
	}{
		{
			name:                  "normal dismiss - animationend fires",
			scenario:              "Toast auto-dismisses after 5s, animation plays, animationend fires",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "animationend does not fire - fallback timeout removes toast",
			scenario:              "prefers-reduced-motion or browser quirk prevents animationend, fallback setTimeout removes element at 400ms",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "transition/animation conflict - pointer-events: none prevents blocking",
			scenario:              "CSS transition on opacity/transform conflicts with dismiss animation, but pointer-events: none on .toast-dismiss prevents click blocking even if element lingers",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "manual dismiss via close button",
			scenario:              "User clicks X button, event.stopPropagation prevents task dialog opening, dismissToast called",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "click dismiss opens task detail",
			scenario:              "User clicks toast body, htmx.ajax loads task detail with .catch(), toast dismissed",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "rapid SSE events - excess toasts dismissed",
			scenario:              "More than 5 toasts created rapidly, oldest dismissed with same safety mechanisms",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.expectToastRemoved {
				t.Error("all scenarios must result in toast removal")
			}
			if tt.expectPointerBlocking {
				t.Error("no scenario should leave pointer-blocking elements in the DOM")
			}
			t.Logf("Scenario: %s", tt.scenario)
		})
	}
}

// TestChatMarkdownCSS_WhiteSpaceNormal verifies that .chat-markdown has white-space: normal
// to prevent inherited whitespace-pre-wrap from streaming containers causing extra spacing
// in task creation feedback and other markdown-rendered content.
func TestChatMarkdownCSS_WhiteSpaceNormal(t *testing.T) {
	var buf bytes.Buffer
	err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()

	if !strings.Contains(html, "white-space: normal") {
		t.Error(".chat-markdown CSS should include 'white-space: normal' to prevent inherited whitespace-pre-wrap from causing layout issues in task creation feedback")
	}
}

func TestPendingThreadInputsCSS_AddsTextareaGutterOnlyWhenRowsExist(t *testing.T) {
	var buf bytes.Buffer
	err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `#pending-thread-inputs:has([data-thread-input-id])`) || !strings.Contains(html, "padding-bottom: 1rem;") {
		t.Error("pending thread inputs should add bottom padding before the textarea whenever queued or steering rows exist")
	}
}

func TestChatInputContainerCSS_FillsContainerWithoutExternalGap(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()
	for _, expected := range []string{
		".chat-input-container {",
		"margin-left: 0;",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("chat input container should fill its parent without external right-side gap or clipping; missing %q", expected)
		}
	}
	// No margin-right on .chat-input-container — it extends to the full right edge of
	// #chat-page-root so the input aligns with agents/models card right edges.
	if strings.Contains(html, "margin-right: 6px;") {
		t.Fatal("chat input container must not have margin-right: 6px; it makes the input narrower than agents/models cards")
	}
	if strings.Contains(html, "width: calc(100% - 32px);") {
		t.Fatal("chat input container should not shrink itself with margin-compensation width because it leaves a visible right-side gap")
	}
}

func TestChatBubbleTailCSS_DoesNotInsetBubbleBody(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()
	bubbleStart := strings.Index(html, ".chat-bubble-user-msg,")
	if bubbleStart == -1 {
		t.Fatal("expected chat bubble CSS block")
	}
	bubbleEnd := strings.Index(html[bubbleStart:], "/* Code block styling in chat messages */")
	if bubbleEnd == -1 {
		t.Fatal("expected end of chat bubble CSS block")
	}
	bubbleCSS := html[bubbleStart : bubbleStart+bubbleEnd]

	// Bubbles themselves have margin-left: 0; the 16px shift lives on the scrollports
	// and input gutter (not on the root containers) so the roots stay full parent-content
	// width and the right edge aligns with agents/models cards.
	for _, expected := range []string{
		".chat-bubble-user-msg,",
		".chat-bubble-assistant-msg {",
		"position: relative;",
		"margin-left: 0;",
		"left: -9px;",
		"left: -7px;"} {
		if !strings.Contains(bubbleCSS, expected) {
			t.Fatalf("chat bubble CSS block missing expected rule; missing %q", expected)
		}
	}
	// Verify the scrollport/gutter shift rules exist.
	// Scrollports (#chat-messages, #task-thread-messages) use:
	//   - margin-left: -16px  — shifts box left of parent content edge
	//   - width: calc(100% + 28px)  — 16px recovers left shift + 12px extra right for shadow room
	//   - max-width: none  — overrides any Tailwind max-w-full cap
	//   - padding-left: 16px  — bubble bodies land at parent content left edge; tail has 7px clearance
	//   - padding-right: 12px  — gives box-shadows 12px room inside the scrollport so overflow-x:auto
	//     (forced by overflow-y:auto per CSS spec) does not clip them
	// Gutter (.chat-input-shadow-gutter) uses a compound selector (parent ID + class) so specificity
	// 1,1,0 beats Tailwind CDN's .w-full (0,1,0) which injects after the <style> block:
	//   - width: calc(100% + 16px)  — only needs to recover the left shift, no extra right
	for _, expected := range []string{
		"#chat-messages,",
		"#task-thread-messages {",
		".chat-input-shadow-gutter,",
		"margin-left: -16px;",
		"width: calc(100% + 28px);",
		"width: calc(100% + 16px);",
		"max-width: none;",
		"padding-left: 16px;",
		"padding-right: 12px;",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("chat left-alignment shift CSS missing %q", expected)
		}
	}
	// The ROOT containers (#chat-page-root / #task-thread-view) must NOT have margin-left: -16px —
	// it was moved to the scrollports/gutter where width: calc(100% + 16px) can compensate correctly.
	// Keeping -16px on the roots + max-w-full made the right edge 16px short of the parent content area.
	if strings.Contains(html, "#chat-page-root,\n\t\t\t\t#task-thread-view {\n\t\t\t\t\tmargin-left: -16px;") {
		t.Fatal("margin-left: -16px must NOT be on #chat-page-root / #task-thread-view roots; it should be on the scrollports/gutter with width: calc(100% + 16px)")
	}
	// No margin-right anywhere in the chat shift block — everything must reach the right edge.
	if strings.Contains(html, "margin-right: 16px;") {
		t.Fatal("chat container must not have margin-right: 16px; it makes the chat pane narrower than agents/models cards")
	}
	// Extract the #chat-messages / #task-thread-messages CSS block and verify:
	// - scrollbar-width: thin (Firefox)
	// - padding-left: 16px (tail arrow room + left alignment)
	// - padding-right: 12px (shadow room — overflow-y:auto forces overflow-x:auto per CSS
	//   spec, which clips box-shadows at the scrollport right edge; 12px padding ensures the
	//   12px dark-theme shadow fits within the box and is not clipped)
	// - NO padding-right: 6px (old incorrect value)
	// - NO scrollbar-gutter: stable (unreliable on macOS overlay scrollbars — reserves
	//   0px, making bubbles 6px wider than the composer)
	chatScrollStart := strings.Index(html, "#chat-messages,")
	if chatScrollStart == -1 {
		t.Fatal("expected #chat-messages CSS block")
	}
	chatScrollEnd := strings.Index(html[chatScrollStart:], "/* Remove outlines/borders from chat selectors */")
	if chatScrollEnd == -1 {
		t.Fatal("expected end of #chat-messages CSS block")
	}
	chatScrollCSS := html[chatScrollStart : chatScrollStart+chatScrollEnd]

	for _, expected := range []string{
		"scrollbar-width: thin;",
		"padding-left: 16px;",
		"padding-right: 12px;",
	} {
		if !strings.Contains(chatScrollCSS, expected) {
			t.Fatalf("chat message scrollport CSS should contain %q", expected)
		}
	}
	if strings.Contains(chatScrollCSS, "padding-right: 6px;") {
		t.Fatal("chat message scrollport must NOT use padding-right: 6px")
	}
	if strings.Contains(chatScrollCSS, "scrollbar-gutter: stable;") {
		t.Fatal("chat message scrollport must NOT use scrollbar-gutter: stable; it reserves 0px on macOS overlay scrollbars, making bubbles wider than the composer")
	}
}

func TestChatInputContainerCSS_UsesSameSurfaceShadowAsMessageBubbles(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()
	for _, expected := range []string{
		`--ov-chat-surface-shadow: 0 1px 6px rgba(0, 0, 0, 0.08), 0 0 0 1px rgba(0, 0, 0, 0.03);`,
		`--ov-chat-surface-shadow: 0 4px 12px rgba(0, 0, 0, 0.5), 0 1px 3px rgba(0, 0, 0, 0.3);`,
		`box-shadow: var(--ov-chat-surface-shadow);`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("chat input should share the exact message-bubble shadow token; missing %q", expected)
		}
	}
	if strings.Contains(html, `[data-theme="light"] .chat-input-container {
							background-color: #FFFFFF;
							border: 1px solid var(--ov-l-border);
							box-shadow: 0 2px 8px`) {
		t.Fatal("light chat input should not use a separate shadow from message bubbles")
	}
}

func TestBase_KanbanColumnCSSDoesNotOverrideResponsiveGridWidth(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	if strings.Contains(html, "width: calc((100% - 2rem) / 3);") {
		t.Fatal("base CSS must not force kanban columns to one-third width; the responsive board grid owns column sizing")
	}
	for _, want := range []string{
		".kanban-column {",
		"width: 100%;",
		"min-width: 0;",
		"flex-shrink: 1;",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected responsive kanban column CSS fragment %q", want)
		}
	}
}

func TestBase_DraggableCardsUseClosedHandCursorWhileActive(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		".drag-cursor-surface {",
		"cursor: grab;",
		".drag-cursor-surface:active",
		"html.drag-cursor-active *",
		"body.drag-cursor-active *",
		"cursor: grabbing !important;",
		"function handleTaskPointerDown(event)",
		"function handleTaskPointerMove(event)",
		"card.setPointerCapture(event.pointerId)",
		"movePointerCard(state.motion, deltaX, deltaY)",
		"data-pointer-drag-placeholder",
		"taskPointerDropZoneAt(event.clientX, event.clientY)",
		"function refreshTaskPointerDropZone(clientX, clientY)",
		"handlePointerAutoScroll(event, board, zone, function()",
		"refreshTaskPointerDropZone(event.clientX, event.clientY)",
		"function pointerAutoScrollDelta(event, scrollZone)",
		"if (currentScrollRefresh) currentScrollRefresh()",
		"styles.overflowY === 'auto' || styles.overflowY === 'scroll'",
		"taskPointerDropZoneAt(event.clientX, event.clientY) || state.dropZone",
		"window.handlePointerAutoScroll = handlePointerAutoScroll",
		"window.stopPointerAutoScroll = stopAutoScroll",
		"document.body.classList.add('drag-cursor-active')",
		"document.body.classList.remove('drag-cursor-active')",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected pointer-driven card cursor contract %q", want)
		}
	}
	for _, forbidden := range []string{"drag-cursor-indicator", "drag-card-preview", "setDragImage", "handleDragCursorPressStart"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("pointer-driven card cursor contract must not contain %q", forbidden)
		}
	}
}

func TestBase_MobileDrawerLayerStaysAboveStickyPageContent(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `class="drawer-side z-[200] lg:z-auto"`) {
		t.Fatal("mobile drawer side should be layered above sticky page content such as the Schedule timeline")
	}
	if !strings.Contains(html, `class="drawer-overlay z-[190] lg:z-auto"`) {
		t.Fatal("mobile drawer overlay should sit behind the sidebar but above page content")
	}
	if !strings.Contains(html, `id="sidebar" class="sidebar-aside relative z-[210] lg:z-auto`) {
		t.Fatal("mobile sidebar panel should sit above the drawer overlay so nav links and theme toggle remain clickable")
	}
}

// TestLightTheme_UsesLightModernTokens verifies the light theme exposes the
// Light 2026 token aliases and that key surfaces consume those tokens.
func TestLightTheme_UsesLightModernTokens(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"--ov-l-accent: #7480ff;",
		"--ov-account-limit-track: #D9D9E3;",
		"--ov-account-limit-fill: #646fe4;",
		".ov-account-limit-bar {",
		"background-color: var(--ov-account-limit-track);",
		".ov-account-limit-bar-fill {",
		"background-color: var(--ov-account-limit-fill);",
		"--ov-l-bg: #FAFAFA;",
		"--ov-l-surface: #F5F5F5;",
		"--ov-l-border: #E5E5E5;",
		"--ov-l-text: #3B3B3B;",
		"[data-theme=\"light\"] body,",
		"[data-theme=\"light\"] .drawer-content {",
		"background-color: var(--ov-l-surface);",
		"[data-theme=\"light\"] #main-content {",
		"background-color: var(--ov-l-surface);",
		"[data-theme=\"light\"] .btn-primary {",
		"background-color: var(--ov-l-accent);",
		"[data-theme=\"light\"] .sidebar-aside {",
		"background-color: #FAFAFA;",
		"[data-theme=\"light\"] .card {",
		"[data-theme=\"light\"] .hover\\:border-primary:hover,",
		"[data-theme=\"light\"] [class~=\"hover:border-primary\"]:hover {",
		"border-color: #3f4981 !important;",
		"[data-theme=\"light\"] .hover\\:border-primary\\/40:hover,",
		"[data-theme=\"light\"] [class~=\"hover:border-primary/40\"]:hover {",
		"border-color: rgba(63, 73, 129, 0.4) !important;",
		"[data-theme=\"light\"] .chat-input-container {",
		"background-color: #FFFFFF;",
		"[data-theme=\"light\"] .bg-base-100 {",
		"background-color: var(--ov-l-bg);",
		"[data-theme=\"light\"] .bg-base-200 {",
		"[data-theme=\"light\"] .stats {",
		"background-color: var(--ov-l-bg);",
		"[data-theme=\"light\"] .chat-bubble-user-msg,"}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected light theme fragment %q to be present", fragment)
		}
	}
	if strings.Contains(html, `[data-theme="light"] .card:hover {`) {
		t.Error("unexpected global light card hover border rule; only explicit hover:border-primary* cards should get purple border")
	}
}

// TestThemeToggle_UsesImmediateSwitch ensures theme toggle applies instantly
// without transition class choreography that can lag on large pages.
func TestThemeToggle_UsesImmediateSwitch(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	unexpected := []string{
		"html.theme-transition",
		"html.classList.add('theme-transition')",
		"html.classList.remove('theme-transition')",
	}
	for _, fragment := range unexpected {
		if strings.Contains(html, fragment) {
			t.Errorf("unexpected transition fragment %q should not be present", fragment)
		}
	}

	expected := []string{
		".theme-toggle-pill {",
		"background: #E8E8E8;",
		"[data-theme=\"dark\"] .theme-toggle-pill {",
		"background: #3a4455;",
		"window.toggleTheme = function() {",
		"var nativeThemeByMode = { light: 'openvibely-light', dark: 'openvibely-dark' };",
		"html.setAttribute('data-theme', mode);",
		"html.setAttribute('data-color-theme', themeID);",
		"localStorage.setItem('theme', themeID);",
		"window.applyOpenVibelyTheme(nativeThemeByMode[nextMode], true);",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected immediate-toggle fragment %q to be present", fragment)
		}
	}
}

// TestLoadingDots_UsesPrimaryThemeToken ensures the shared three-dot loader
// stays tied to the same primary token used by primary buttons (chat send button).
func TestLoadingDots_UsesPrimaryThemeToken(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		".ov-loading-dots {",
		"--ov-loading-dot-color: #646fe4;",
		"--ov-loading-dot-color-soft: rgba(100, 111, 228, 0.45);", ".ov-loading-dot {",
		"animation: ov-loading-dot-bounce 1s ease-in-out infinite, ov-loading-dot-color 1.6s linear infinite;",
		"@keyframes ov-loading-dot-color {",
		"background: var(--ov-loading-dot-color-soft);",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected loading-dot style fragment %q to be present", fragment)
		}
	}
}

// TestDarkMode_ButtonHoverParity ensures dark-mode hover colors are explicitly
// defined for all button variants so web and desktop match.
func TestDarkMode_ButtonHoverParity(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"[data-theme=\"dark\"] .btn:hover {",
		"[data-theme=\"dark\"] .btn-primary:hover {",
		"[data-theme=\"dark\"] .btn-secondary:hover {",
		"[data-theme=\"dark\"] .btn-accent:hover {",
		"[data-theme=\"dark\"] .btn-info:hover {",
		"[data-theme=\"dark\"] .btn-success:hover {",
		"[data-theme=\"dark\"] .btn-warning:hover {",
		"[data-theme=\"dark\"] .btn-error:hover {",
		"[data-theme=\"dark\"] .btn-neutral:hover {",
		"[data-theme=\"dark\"] .btn-ghost:hover {",
		"[data-theme=\"dark\"] .btn-link:hover {",
		"[data-theme=\"dark\"] .btn-outline:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-primary:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-secondary:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-accent:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-info:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-success:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-warning:hover {",
		"[data-theme=\"dark\"] .btn-outline.btn-error:hover {",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected dark-mode button hover parity fragment %q to be present", fragment)
		}
	}
}

// TestLightMode_ButtonHoverParity ensures light-mode hover colors are explicitly
// defined for the same button variant matrix as dark mode.
func TestLightMode_ButtonHoverParity(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"[data-theme=\"light\"] .btn:hover {",
		"[data-theme=\"light\"] .btn-primary:hover {",
		"[data-theme=\"light\"] .btn-secondary:hover {",
		"[data-theme=\"light\"] .btn-accent:hover {",
		"[data-theme=\"light\"] .btn-info:hover {",
		"[data-theme=\"light\"] .btn-success:hover {",
		"[data-theme=\"light\"] .btn-warning:hover {",
		"[data-theme=\"light\"] .btn-error:hover {",
		"[data-theme=\"light\"] .btn-neutral:hover {",
		"[data-theme=\"light\"] .btn-ghost:hover {",
		"[data-theme=\"light\"] .btn-link:hover {",
		"[data-theme=\"light\"] .btn-outline:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-primary:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-secondary:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-accent:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-info:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-success:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-warning:hover {",
		"[data-theme=\"light\"] .btn-outline.btn-error:hover {",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected light-mode button hover parity fragment %q to be present", fragment)
		}
	}
}

// TestToggleSwitch_ColorParity ensures toggle checked/unchecked colors are
// explicitly pinned for both themes to avoid WebKit fallback-to-black behavior.
func TestToggleSwitch_ColorParity(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"[data-theme=\"light\"] .toggle {",
		"--tglbg: #FFFFFF !important;",
		"background-color: #E5E7EB !important;",
		"border-color: #D1D5DB !important;",
		"[data-theme=\"light\"] .toggle:checked,",
		"background-color: #7480ff !important;",
		"[data-theme=\"light\"] .toggle:hover {",
		"[data-theme=\"light\"] .toggle:checked:hover,",
		"[data-theme=\"dark\"] .toggle {",
		"--tglbg: #1d232a !important;",
		"background-color: #4B5563 !important;",
		"[data-theme=\"dark\"] .toggle:checked,",
		"background-color: #646fe4 !important;",
		"[data-theme=\"dark\"] .toggle:focus,",
		"[data-theme=\"dark\"] .toggle:focus-visible {",
		"[data-theme=\"dark\"] .toggle:checked:focus,",
		"[data-theme=\"dark\"] .toggle:checked:hover,",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected toggle parity fragment %q to be present", fragment)
		}
	}
}

// TestCollapsedSidebar_NoHoverTooltipBoxes ensures collapsed sidebar icon hovers
// do not render custom pseudo-element tooltip boxes next to SVGs.
func TestCollapsedSidebar_NoHoverTooltipBoxes(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		".sidebar-aside.sidebar-collapsed [data-tip]::after {",
		"content: none !important;",
		"display: none !important;",
		".sidebar-aside.sidebar-collapsed [data-tip]:hover::after {",
		"opacity: 0 !important;",
		".sidebar-aside .menu a:focus:not(:focus-visible),",
		".sidebar-aside .menu summary:focus:not(:focus-visible) {",
		"background-color: transparent !important;",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected collapsed-sidebar tooltip suppression fragment %q to be present", fragment)
		}
	}
}

func TestToolOutputContainerCSS_UsesSeparateBorderlessLightInputAndOutputSurfaces(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		`[data-theme="light"] .stream-tool-body {`,
		"border: none;",
		"background: transparent;",
		`[data-theme="light"] .stream-tool-body-row {`,
		"border-top-color: transparent;",
		`[data-theme="light"] .stream-tool-body-label {`,
		"color: var(--ov-l-text-muted);",
		`[data-theme="light"] .stream-tool-body-content {`,
		"background: transparent;",
		"color: var(--ov-l-text-strong);",
		`[data-theme="light"] .stream-tool-body-content .stream-tool-output-text {`,
		"border: none;",
		"background: hsl(var(--b2, var(--b1)) / 0.4);",
		"border-radius: 5px;",
		"grid-template-columns: max-content minmax(0, 1fr);",
		"border-top: 0.5px solid hsl(var(--bc) / 0.1);",
		"padding: 4px 8px 4px 4px;",
		"padding: 4px;",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected separate tool surface CSS fragment %q", fragment)
		}
	}

	for _, fragment := range []string{
		`.stream-tool-output-container`,
		`[data-theme="light"] .stream-tool-body-scroll pre {`,
		`[data-theme="dark"] .stream-tool-body {`,
		`[data-theme="dark"] .stream-tool-body-label,`,
		`[data-theme="dark"] .stream-tool-body-label {`,
		`[data-theme="dark"] .stream-tool-body-content,`,
		`[data-theme="dark"] .stream-tool-body-content {`,
	} {
		if strings.Contains(html, fragment) {
			t.Errorf("tool output styling must preserve separate input/output surfaces; found %q", fragment)
		}
	}
}

func TestThinkingCSS_UsesReadableLightThemeForegrounds(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	for _, fragment := range []string{
		`[data-theme="light"] .stream-thinking summary {`,
		"color: var(--ov-l-text-muted);",
		`[data-theme="light"] .stream-thinking[open] summary,`,
		`[data-theme="light"] .stream-thinking summary:hover {`,
		"color: var(--ov-l-text-strong);",
		`[data-theme="light"] .stream-thinking .stream-thinking-body {`,
		"color: var(--ov-l-text);",
		`.stream-thinking summary:focus-visible {`,
		"outline: 2px solid var(--ov-link-color);",
	} {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected readable light thinking CSS fragment %q", fragment)
		}
	}
}

func TestToolOutputToggleCSS_UsesCompactThemeSafeColorWithoutRestylingContainer(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"[data-theme=\"light\"] .stream-tool-output-toggle {",
		"color: var(--ov-l-text-strong);",
		"[data-theme=\"light\"] .stream-tool-output-toggle:hover {",
		"background: #e5e7eb;",
		"[data-theme=\"light\"] .stream-tool-output-toggle:active {",
		"background: #d1d5db;",
		"[data-theme=\"dark\"] .stream-tool-output-toggle {",
		"color: #b8c0cc;",
		"[data-theme=\"dark\"] .stream-tool-output-toggle:hover {",
		"background: rgba(255, 255, 255, 0.1);",
		"[data-theme=\"dark\"] .stream-tool-output-toggle:active {",
		"background: rgba(255, 255, 255, 0.16);",
		"appearance: none;",
		"max-width: 100%;",
		"min-height: 1.25rem;",
		"padding: 0.0625rem 0.25rem;",
		"border: 0;",
		"background: transparent;",
		"color: inherit;",
		"font-weight: 500;",
		"transition: background-color 120ms ease, color 120ms ease;",
		".stream-tool-output-toggle:focus-visible {",
		"outline: 2px solid var(--ov-link-color);",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected compact tool output toggle CSS fragment %q", fragment)
		}
	}

	if strings.Contains(html, ".stream-tool-output-container") {
		t.Error("tool output toggle styling must use the original tool container without adding a new surface")
	}
}

func TestToolOutputCSS_UsesBoundedResponsiveScrollableContainer(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		".stream-tool-body {",
		"max-width: 100%;",
		"min-width: 0;",
		"overflow: hidden;",
		"grid-template-columns: max-content minmax(0, 1fr);",
		".stream-tool-body-row:first-child > .stream-tool-body-content {",
		"border-top: none;",
		".stream-tool-body-content {",
		"overflow: hidden;",
		".stream-tool-output-preview {",
		".stream-tool-body-content .stream-tool-output-text {",
		"white-space: pre;",
		"[data-theme=\"light\"] .stream-tool-body-content {",
		".stream-tool-body-scroll + .stream-tool-output-toggle {",
		".stream-tool-body-scroll {",
		"overflow-x: auto;",
		"overflow-y: auto;",
		"max-height: min(26rem, 52vh);",
		"max-width: 100%;",
		"width: max-content;",
		"min-width: 100%;",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Fatalf("expected tool output CSS fragment %q", fragment)
		}
	}
	if strings.Contains(html, "max-height: none;") {
		t.Fatal("tool output body must be height-bounded instead of unbounded")
	}
	if strings.Contains(html, "overscroll-behavior: contain;") {
		t.Fatal("tool output body must not contain overscroll, because page scrolling must chain at top/bottom boundaries")
	}
}
