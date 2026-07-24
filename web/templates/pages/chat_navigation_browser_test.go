package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestChatNavigationStateAndActiveStreamInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Chat navigation fixture", nil, "project-browser").Render(context.Background(), &base); err != nil {
		t.Fatalf("render base navigation script: %v", err)
	}
	baseHTML := base.String()
	navStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	navEnd := strings.Index(baseHTML[navStart:], "// Scroll position restoration for drop zones")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("could not isolate production HTMX navigation helper")
	}
	navigationScript := baseHTML[navStart : navStart+navEnd]

	history := make([]models.Execution, 0, 14)
	for i := 0; i < 14; i++ {
		history = append(history, models.Execution{
			ID:         fmt.Sprintf("history-%02d", i),
			Status:     models.ExecCompleted,
			PromptSent: strings.Repeat(fmt.Sprintf("user-%02d ", i), 8),
			Output:     "",
			StartedAt:  time.Unix(int64(i+1), 0),
		})
	}

	var stateMu sync.Mutex
	persistedOutput := ""
	streamOffsets := make([]int, 0, 8)
	latestConnection := 0
	growthRelease := make(chan struct{})
	var releaseGrowth sync.Once

	renderChatFragment := func() string {
		stateMu.Lock()
		output := persistedOutput
		stateMu.Unlock()
		currentHistory := append([]models.Execution(nil), history...)
		if output != "" {
			currentHistory = append(currentHistory, models.Execution{
				ID:         "exec-live",
				Status:     models.ExecRunning,
				PromptSent: "stream this response",
				Output:     output,
				StartedAt:  time.Unix(100, 0),
			})
		}
		var rendered bytes.Buffer
		if err := ChatContent(nil, currentHistory, "project-browser", nil, nil, false, false, 30).Render(context.Background(), &rendered); err != nil {
			t.Fatalf("render production ChatContent: %v", err)
		}
		return rendered.String()
	}

	runner := `<script>
window.requestAnimationFrame = function(callback) { return setTimeout(callback, 0); };
window.cancelAnimationFrame = function(handle) { clearTimeout(handle); };
window.renderChatMarkdown = function(text) {
  var div = document.createElement('div');
  div.textContent = String(text || '');
  return div.innerHTML;
};
window.renderChatMarkdownAsync = null;
window.addCodeCopyButtons = function() {};
window.addEventListener('DOMContentLoaded', function() {
	  function fail(message) { throw new Error(message); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 4000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function messages() { return document.getElementById('chat-messages'); }
  function bottomDistance() { var el = messages(); return el.scrollHeight - el.scrollTop - el.clientHeight; }
	  function attachmentSession() { var input = document.querySelector('#chat-form input[name="attachment_session_id"]'); return input && input.value; }
	  function attachmentSessionAttribute() { var input = document.querySelector('#chat-form input[name="attachment_session_id"]'); return input && input.getAttribute('value'); }
  function attachmentVisible() { var row = document.querySelector('#chat-form [data-pending-attachment="true"]'); return row && row.textContent.indexOf('pending.txt') !== -1; }
	  function streamNode() { return document.getElementById('streaming-message-exec-live'); }
	  function rawStream() { var node = streamNode(); return node ? (node.getAttribute('data-raw-content') || '') : ''; }
  function waitForChat(stage) { return waitFor(function() { return document.getElementById('chat-page-root') && messages() && messages().style.visibility !== 'hidden'; }, stage + ' Chat restoration'); }
  function waitForOther() { return waitFor(function() { return document.getElementById('other-page'); }, 'other page'); }
	  function assertComposerState(stage) {
	    if (attachmentSession() !== 'session-browser' || attachmentSessionAttribute() !== 'session-browser' || !attachmentVisible()) fail(stage + ' lost pending attachment state: session=' + attachmentSession() + '; attribute=' + attachmentSessionAttribute());
    var draft = document.getElementById('message-input');
    if (!draft || draft.value !== 'unsent browser draft') fail(stage + ' lost composer draft');
  }
  function reportResult(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}).catch(function() {});
  }
  function markFailure(error) {
    var message = String(error && error.stack || error);
    var root = document.getElementById('chat-page-root') || document.getElementById('other-page') || document.body;
    root.setAttribute('data-test-result', 'fail');
    root.setAttribute('data-test-error', message);
    reportResult('fail', message);
  }

  (async function() {
    await reportResult('progress', 'runner-started');
    if (window.initializeChatTranscriptScroll) {
      await reportResult('progress', 'initializing-chat');
      await window.initializeChatTranscriptScroll();
      await reportResult('progress', 'initialized-chat');
    }
    await waitForChat('initial');
	    await reportResult('progress', 'initial-chat-ready');
	    if (bottomDistance() > 1) fail('direct Chat load did not settle at true bottom: ' + bottomDistance());

	    var fileInput = document.getElementById('chat-form-file-input');
    var transfer = new DataTransfer();
    transfer.items.add(new File(['pending'], 'pending.txt', {type: 'text/plain'}));
    fileInput.files = transfer.files;
    fileInput.dispatchEvent(new Event('change', {bubbles: true}));
    await reportResult('progress', 'attachment-upload-dispatched');
	    await waitFor(function() { return attachmentSession() === 'session-browser' && attachmentSessionAttribute() === 'session-browser' && attachmentVisible(); }, 'pending attachment upload');
    await reportResult('progress', 'attachment-uploaded');

    var draft = document.getElementById('message-input');
    draft.value = 'unsent browser draft';
    draft.dispatchEvent(new Event('input', {bubbles: true}));

    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {
      type: 'chat_new_message', project_id: 'project-browser', exec_id: 'exec-live', message: 'stream this response', queued: false
    }}));
    await waitFor(function() { return rawStream() === 'hé'; }, 'first live-created stream chunk');
    var liveNode = streamNode();
    if (!liveNode || liveNode.getAttribute('data-streaming-resume') !== 'true' || liveNode.getAttribute('data-initial-byte-length') !== '3') {
      fail('live-created stream did not persist resumable UTF-8 state');
    }
    messages().scrollTop = messages().scrollHeight;

    await window.openVibelyNavigate('/other');
    await waitForOther();
    await window.openVibelyNavigate('/chat?project_id=project-browser');
	    await waitForChat('direct in-app return');
	    assertComposerState('direct in-app return');
	    await waitFor(function() { return rawStream() === 'héllo'; }, 'offset-aware direct-return stream catch-up');
    if (streamNode().getAttribute('data-initial-byte-length') !== '6') fail('direct-return stream offset was not advanced to six UTF-8 bytes');
    if (bottomDistance() > 1) fail('direct in-app return did not restore pinned bottom: ' + bottomDistance());

    await window.openVibelyNavigate('/other');
    await waitForOther();
    history.back();
	    await waitForChat('first Back');
	    assertComposerState('Back restoration');
	    await waitFor(function() { return bottomDistance() <= 1; }, 'pinned Back bottom reconciliation');
    if (bottomDistance() > 1) fail('Back restoration did not return to true bottom: ' + bottomDistance());
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {
      type: 'mixture_progress', project_id: 'project-browser', exec_id: 'exec-live', message: 'history handler restored'
    }}));
    await waitFor(function() {
      var progress = document.getElementById('mixture-progress-exec-live');
      return progress && progress.textContent === 'history handler restored';
    }, 'Chat live-event handler reattachment after Back');
    history.forward();
    await waitForOther();
    history.back();
	    await waitForChat('Forward/Back');
	    await waitFor(function() { return bottomDistance() <= 1; }, 'Forward/Back bottom reconciliation');
	    assertComposerState('Forward/Back restoration');

    var readingMessages = messages();
    readingMessages.dispatchEvent(new WheelEvent('wheel', {deltaY: -160, bubbles: true}));
    readingMessages.scrollTop = 80;
    readingMessages.dispatchEvent(new Event('scroll'));
    await waitFor(function() { return window._chatPageTracker && window._chatPageTracker.userScrolledUp; }, 'upward scroll intent');
	    var readingState = window.getChatTranscriptScrollState('chat-scroll-project-browser');
	    if (!readingState || Math.abs(readingState.scrollTop - 80) > 2 || !readingState.userScrolledUp) fail('upward reading state was not saved before navigation: ' + JSON.stringify(readingState));
	    if (window.hasChatSendScrollIntent && window.hasChatSendScrollIntent('chat-messages')) fail('unexpected pending send-scroll intent before reading-state navigation');
	    await reportResult('progress', 'reading-state=' + JSON.stringify(readingState));
	    await window.openVibelyNavigate('/other');
	    await waitForOther();
	    var awayReadingState = window.getChatTranscriptScrollState('chat-scroll-project-browser');
	    if (!awayReadingState || !awayReadingState.userScrolledUp || Math.abs(awayReadingState.scrollTop - 80) > 2) fail('navigation-away capture overwrote reading state: ' + JSON.stringify(awayReadingState));
	    history.back();
	    await waitForChat('scrolled-up Back');
	    await waitFor(function() { return Math.abs(messages().scrollTop - 80) <= 2; }, 'scrolled-up Back position reconciliation', 2500);
	    assertComposerState('scrolled-up Back restoration');
	    var restoredTop = messages().scrollTop;
	    if (Math.abs(restoredTop - 80) > 2) fail('scrolled-up Back restoration moved reading position: ' + restoredTop + '; state=' + JSON.stringify(window.getChatTranscriptScrollState('chat-scroll-project-browser')) + '; trackerUp=' + !!(window._chatPageTracker && window._chatPageTracker.userScrolledUp));

    await fetch('/release-growth', {method: 'POST'});
    await waitFor(function() { return rawStream().length > 500; }, 'post-restoration stream growth');
    await wait(150);
    if (Math.abs(messages().scrollTop - restoredTop) > 2) fail('stream growth overrode intentional upward reading position: ' + messages().scrollTop + ' vs ' + restoredTop);

    var submittedDraft = document.getElementById('message-input');
    submittedDraft.value = 'submitted browser draft';
    submittedDraft.dispatchEvent(new Event('input', {bubbles: true}));
    var submittedForm = document.getElementById('chat-form');
    submittedForm.dispatchEvent(new CustomEvent('htmx:afterRequest', {bubbles: true, detail: {
      elt: submittedForm,
      successful: true,
      xhr: {responseText: '<div>accepted</div>'}
    }}));
    await wait(600);
    if (submittedDraft.value !== '' || localStorage.getItem('chat-draft-project-browser') !== null) {
      fail('successful send did not clear the live draft and cancel its pending debounce');
    }

    var root = document.getElementById('chat-page-root');
    root.setAttribute('data-test-result', 'pass');
    await reportResult('pass', '');
  })().catch(markFailure);
});
</script>`

	style := `<style>
html, body, #main-content { height: 100%; margin: 0; }
#chat-page-root { height: 620px; }
#chat-messages { display: block; height: 260px; flex: none; overflow-y: auto; }
#chat-messages > [data-execution-pair="true"] { min-height: 92px; display: block; }
	.chat-stream-content { white-space: pre-wrap; overflow-wrap: anywhere; max-width: 420px; }
	#chat-form-attachments-preview.hidden { display: none; }
#other-page { height: 400px; }
.hidden { display: none !important; }
</style>`

	var server *httptest.Server
	browserResult := make(chan string, 32)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat":
			fragment := renderChatFragment()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(fragment))
				return
			}
			doc := `<!doctype html><html><head><meta charset="utf-8"><script src="/htmx-2.0.4.min.js"></script><script>` + navigationScript + `</script>` + style + runner + `</head><body><main id="main-content">` + fragment + `</main></body></html>`
			_, _ = w.Write([]byte(doc))
		case "/other":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<div id="other-page"><span hidden data-openvibely-page-title="Other - OpenVibely"></span><h1>Other</h1></div>`))
		case "/chat/attachments":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session_id":  "session-browser",
				"attachments": []map[string]any{{"filename": "pending.txt", "size": 7}},
			})
		case "/events/chat/exec-live":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			stateMu.Lock()
			streamOffsets = append(streamOffsets, offset)
			latestConnection++
			connection := latestConnection
			stateMu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", http.StatusInternalServerError)
				return
			}
			writeChunk := func(chunk string) {
				stateMu.Lock()
				if len(persistedOutput) == offset {
					persistedOutput += chunk
				}
				stateMu.Unlock()
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
			}
			switch offset {
			case 0:
				writeChunk("hé")
			case 3:
				writeChunk("llo")
			default:
				select {
				case <-growthRelease:
					stateMu.Lock()
					isLatest := connection == latestConnection
					stateMu.Unlock()
					if isLatest && r.Context().Err() == nil {
						writeChunk(strings.Repeat(" growth", 140))
					}
				case <-r.Context().Done():
					return
				}
			}
			<-r.Context().Done()
		case "/release-growth":
			releaseGrowth.Do(func() { close(growthRelease) })
			w.WriteHeader(http.StatusNoContent)
		case "/browser-result":
			result := r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			select {
			case browserResult <- result:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/chat/composer-action":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<button id="chat-form-primary-action" type="submit">Send</button>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chat-navigation.html")
	stderrPath := filepath.Join(t.TempDir(), "chat-navigation.stderr")
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

	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chat-navigation-profile"),
		server.URL+"/chat?project_id=project-browser",
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome Chat navigation fixture: %v", err)
	}

	var outcome string
	lastProgress := "none"
	deadline := time.After(30 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			if strings.HasPrefix(result, "progress:") {
				lastProgress = strings.TrimPrefix(result, "progress:")
				continue
			}
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result callback; last progress=" + lastProgress
		}
	}
	stopBrowserProcess(cmd)

	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("real HTMX Chat navigation fixture failed: %s\nLast browser progress: %s\nChrome stderr tail:\n%s", outcome, lastProgress, stderr)
	}

	stateMu.Lock()
	offsets := append([]int(nil), streamOffsets...)
	stateMu.Unlock()
	if !containsInt(offsets, 0) || !containsInt(offsets, 3) || !containsInt(offsets, 6) {
		t.Fatalf("stream reconnect offsets = %v, want live-created 0 then UTF-8 resume offsets 3 and 6", offsets)
	}
}

func chatNavigationChromePath(t *testing.T) string {
	t.Helper()
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err == nil {
		return chrome
	}
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	t.Skip("Chrome or Chromium is required for real HTMX Chat navigation coverage")
	return ""
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
