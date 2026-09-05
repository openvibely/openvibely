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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAlertsInspectCopyFeedbackInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	createdAt := time.Date(2026, time.August, 4, 9, 8, 7, 0, time.UTC)
	implementationTaskID := "implementation-task-1"
	alerts := []models.Alert{
		{
			ID: "operational-1", Title: "Build failed", Message: "Compiler exited", Body: "Compiler diagnostics\nline 2", Type: models.AlertTaskFailed,
			Severity: models.SeverityError, Source: "task-runner", DecisionState: models.AlertDecisionNotRequired,
			ProcessingState: models.AlertProcessingNotApplicable, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		{
			ID: "notification-1", Title: "Review change", Body: "Check the patch.", Type: models.AlertCustom,
			Severity: models.SeverityWarning, Source: "review-agent", DecisionState: models.AlertDecisionPending,
			ProcessingState: models.AlertProcessingFailed, ProcessingError: "worker unavailable",
			ImplementationTaskID: &implementationTaskID, Metadata: map[string]any{"attempt": float64(2)},
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		{
			ID: "empty-body-1", Title: "No body", Message: "Summary only", Type: models.AlertCustom,
			Severity: models.SeverityInfo, Source: "system", DecisionState: models.AlertDecisionNotRequired,
			ProcessingState: models.AlertProcessingNotApplicable, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
	}
	var rendered bytes.Buffer
	// The list no longer embeds body/metadata or copy controls; those now live in
	// the lazily loaded per-alert detail fragment. Render an empty list first to
	// supply the shared copyAlertDetails/loadAlertDetail scripts, then render each
	// alert's detail fragment inside a matching card wrapper so the copy-feedback
	// interaction is exercised exactly as the browser sees it after lazy load.
	if err := AlertsContent(nil, "project-alerts-browser", 0).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render Alerts content scripts: %v", err)
	}
	for _, alert := range alerts {
		rendered.WriteString(fmt.Sprintf(`<div id="alert-%s">`, alert.ID))
		if err := AlertDetail(alert).Render(context.Background(), &rendered); err != nil {
			t.Fatalf("render alert detail: %v", err)
		}
		rendered.WriteString(`</div>`)
	}

	runner := `<style>
		.hidden { display: none; }
		details > div { position: relative; margin-top: 0.75rem; padding-right: 2rem; }
		[data-alert-copy] { position: absolute; right: 0; top: 0; width: 1.5rem; height: 1.5rem; }
		.min-h-6 { min-height: 1.5rem; }
		</style><script>
		window.addEventListener('DOMContentLoaded', function() {
		  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
		  function fail(message) { throw new Error(message); }
		  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
		  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
		  (async function() {
		    await report('progress', 'dom-ready');
		    var copied = [];
		    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:function(text) { copied.push(text); return Promise.resolve(); }}});
		    var operational = document.querySelector('#alert-operational-1 [data-alert-copy]');
		    var notification = document.querySelector('#alert-notification-1 [data-alert-copy]');
		    var emptyBody = document.querySelector('#alert-empty-body-1 [data-alert-copy]');
		    if (!operational || !notification) fail('missing inspect copy buttons');
		    if (emptyBody) fail('body-less alert exposed a copy button');
		    await report('progress', 'buttons-ready');
		    operational.click();
		    await wait(0);
		    if (operational.textContent.trim() !== 'Copied') fail('success feedback was not shown');
		    if (copied[0] !== 'Compiler diagnostics\nline 2') fail('operational alert copied more than its body: ' + copied[0]);
		    await report('progress', 'success-feedback-ready');
		    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:function() { return Promise.reject(new Error('denied')); }}});
		    notification.click();
		    await wait(0);
		    if (notification.textContent.trim() !== 'Copy failed') fail('failure feedback was not shown');
		    await report('progress', 'failure-feedback-ready');
		    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:function(text) { copied.push(text); return Promise.resolve(); }}});
		    notification.click();
		    await wait(0);
		    if (copied[1] !== 'Check the patch.') fail('notification copied more than its body: ' + copied[1]);
	    report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`
	page := "<!doctype html><html><head>" + runner + "</head><body>" + rendered.String() + "</body></html>"

	browserResult := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "alerts-copy-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	profileDir, err := os.MkdirTemp("", "openvibely-alerts-copy-browser-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profileDir)
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profileDir, server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome, lastProgress string
	deadline := time.After(20 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			if strings.HasPrefix(result, "progress:") {
				lastProgress = strings.TrimPrefix(result, "progress:")
				continue
			}
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result; last progress=" + lastProgress
		}
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Alerts inspect copy browser regression failed: %s\nLast browser progress: %s\nChrome stderr:\n%s", outcome, lastProgress, stderr)
	}
}

func TestAlertsInspectMarkdownAndHTMXDetailLoadingInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projectID := "project-alerts-markdown"
	body := "# Heading\r\n\r\n**emphasis** with `getJSON`\r\n\r\n- first\r\n- second\r\n\r\n[external](https://example.test/link) [internal](/tasks/internal)\r\n\r\n```go\r\nline 1\r\nline 2\r\n```\r\n\r\n<img src=x onerror=alert(1)>\rbare carriage return café"
	emptyAlert := models.AlertSummary{
		ID: "empty-detail-1", ProjectID: projectID, Title: "Empty detail", Message: "No extra content",
		Type: models.AlertCustom, Severity: models.SeverityInfo, DecisionState: models.AlertDecisionNotRequired,
		ProcessingState: models.AlertProcessingNotApplicable,
	}
	summary := models.AlertSummary{
		ID: "markdown-detail-1", ProjectID: projectID, Title: "Markdown detail", Message: "Inspect this notification",
		Type: models.AlertCustom, Severity: models.SeverityInfo, DecisionState: models.AlertDecisionNotRequired,
		ProcessingState: models.AlertProcessingNotApplicable,
	}
	fullAlert := models.Alert{
		ID: summary.ID, ProjectID: projectID, Title: summary.Title, Message: summary.Message, Body: body,
		Type: summary.Type, Severity: summary.Severity, Source: "browser-test",
		DecisionState: summary.DecisionState, ProcessingState: summary.ProcessingState,
		Metadata: map[string]any{"attempt": float64(2)},
	}

	renderList := func() string {
		var rendered bytes.Buffer
		if err := AlertsContent([]models.AlertSummary{summary, emptyAlert}, projectID, 0).Render(context.Background(), &rendered); err != nil {
			t.Fatalf("render Alerts content: %v", err)
		}
		return rendered.String()
	}
	var renderedPage bytes.Buffer
	if err := Alerts(nil, projectID, []models.AlertSummary{summary, emptyAlert}, 0).Render(context.Background(), &renderedPage); err != nil {
		t.Fatalf("render Alerts page: %v", err)
	}
	page := strings.Replace(renderedPage.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
	page = strings.Replace(page, "https://cdn.jsdelivr.net/npm/marked@15.0.4/marked.min.js", "/marked.min.js", 1)
	expectedBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal expected Markdown body: %v", err)
	}
	markedScript := `window.marked = {
  setOptions: function(options) { this.options = options; },
  parse: function(value) {
    window.__markedInput = value;
    if (value.indexOf('&lt;img src=x onerror=alert(1)>') === -1) throw new Error('raw HTML was not escaped before parsing');
    return '<h1>Heading</h1>' +
      '<p><strong>emphasis</strong> with <code data-case="inline-code">getJSON</code></p>' +
      '<ul><li>first</li><li>second</li></ul>' +
      '<p><a data-case="external" href="https://example.test/link">external</a> <a data-case="internal" href="/tasks/internal">internal</a></p>' +
      '<pre><code class="language-go">line 1\nline 2</code></pre>' +
      '<p class="raw-html-example">&lt;img src=x onerror=alert(1)&gt;</p>' +
      '<script>alert(1)</script>' +
      '<a data-case="unsafe" href="javascript:alert(1)" onclick="alert(1)">unsafe</a>';
  }
};`
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {}); }
  function fail(message) { throw new Error(message); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 5000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function detailRow(id) { return document.querySelector('[data-alert-id="' + id + '"]'); }
  function openDetails(row) {
    var details = row && row.querySelector('details');
    if (!details) fail('missing inspection details');
    details.open = true;
    details.dispatchEvent(new Event('toggle'));
    return details;
  }
  function renderFallback(text, label) {
    var host = document.createElement('div');
    host.innerHTML = window.renderChatMarkdown(text);
    var fallback = host.querySelector('[data-chat-markdown-fallback="true"]');
    if (!fallback) fail(label + ' did not use the shared fallback');
    if (fallback.textContent !== text) fail(label + ' changed fallback text');
    if (host.querySelector('img,script,iframe')) fail(label + ' left dangerous HTML active');
  }
  function rgbChannels(value) {
    var match = String(value || '').match(/[\d.]+/g);
    if (!match || match.length < 3) fail('could not parse computed color: ' + value);
    return match.slice(0, 3).map(Number);
  }
  function luminance(rgb) {
    var channels = rgb.map(function(value) {
      value /= 255;
      return value <= 0.04045 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
  }
  function contrast(foreground, background) {
    var first = luminance(rgbChannels(foreground));
    var second = luminance(rgbChannels(background));
    return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    var expectedBody = ` + string(expectedBody) + `;
    await waitFor(function() { return window.htmx && detailRow('markdown-detail-1'); }, 'initial Alerts page');
    var initialRow = detailRow('markdown-detail-1');
    if (initialRow.querySelector('[data-raw-content]')) fail('initial compact summary embedded full Markdown body');

    var initialContent = document.getElementById('alerts-content');
    htmx.process(document.body);
    htmx.trigger(document.body, 'alertUpdate');
    await waitFor(function() { return document.getElementById('alerts-content') !== initialContent; }, 'HTMX Alerts refresh');
    var row = detailRow('markdown-detail-1');
    if (!row || row.querySelector('[data-raw-content]')) fail('HTMX compact summary embedded full Markdown body');

    openDetails(row);
    await waitFor(function() {
      var markdown = row.querySelector('[data-alert-markdown]');
      return markdown && markdown.querySelector('h1');
    }, 'Markdown detail hydration');
    var markdown = row.querySelector('[data-alert-markdown]');
    if (!markdown.classList.contains('chat-markdown')) fail('detail body did not use chat Markdown styling');
    if (!markdown.querySelector('h1') || markdown.querySelector('h1').textContent !== 'Heading') fail('heading did not render');
    if (!markdown.querySelector('strong') || markdown.querySelector('strong').textContent !== 'emphasis') fail('emphasis did not render');
    document.documentElement.setAttribute('data-theme', 'light');
    var inlineCode = markdown.querySelector('code[data-case="inline-code"]');
    if (!inlineCode || inlineCode.textContent !== 'getJSON') fail('inline code did not render');
    var inlineStyle = getComputedStyle(inlineCode);
    var inlineContrast = contrast(inlineStyle.color, inlineStyle.backgroundColor);
    if (inlineContrast < 4.5) fail('light-mode inline code contrast was ' + inlineContrast.toFixed(2) + ': ' + inlineStyle.color + ' on ' + inlineStyle.backgroundColor);
    if (!markdown.querySelectorAll('ul > li') || markdown.querySelectorAll('ul > li').length !== 2) fail('list did not render');
    var external = markdown.querySelector('a[data-case="external"]');
    if (!external || external.getAttribute('target') !== '_blank' || external.getAttribute('rel') !== 'noopener noreferrer' || external.getAttribute('data-openvibely-chat-external-link') !== 'true') fail('safe external link was not marked for outside-app opening');
    var internal = markdown.querySelector('a[data-case="internal"]');
    if (!internal || internal.hasAttribute('target') || internal.hasAttribute('rel') || internal.hasAttribute('data-openvibely-chat-external-link')) fail('internal link received external-link behavior');
    var code = markdown.querySelector('pre code');
    if (!code || code.textContent !== 'line 1\nline 2') fail('fenced multiline code changed');
    if (!markdown.querySelector('pre .code-copy-btn')) fail('rendered code block did not receive shared code-copy control');
    if (markdown.querySelector('script, iframe, img')) fail('dangerous HTML survived detail sanitization');
    var unsafe = markdown.querySelector('a[data-case="unsafe"]');
    if (!unsafe || unsafe.hasAttribute('href') || unsafe.hasAttribute('onclick')) fail('dangerous URL or event handler survived sanitization');
    if (!window.__markedInput || window.__markedInput.indexOf('&lt;img src=x onerror=alert(1)>') === -1) fail('shared renderer did not escape raw HTML before Marked');

    var copied = '';
    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:function(text) { copied = text; return Promise.resolve(); }}});
    var copyButton = row.querySelector('[data-alert-copy]');
    if (!copyButton) fail('missing detail copy control');
    copyButton.click();
    await wait(0);
    if (copied !== expectedBody) fail('copy payload was not the exact raw body: ' + JSON.stringify(copied));

    var emptyRow = detailRow('empty-detail-1');
    openDetails(emptyRow);
    await waitFor(function() { return emptyRow.textContent.indexOf('No additional detail.') !== -1; }, 'empty detail');
    if (emptyRow.querySelector('[data-alert-copy]')) fail('empty detail exposed a copy control');

    var parser = window.marked;
    parser.parse = function() { throw new Error('malformed Markdown'); };
    renderFallback('malformed <img src=x onerror=alert(1)>', 'parse failure fallback');
    parser.parse = function(value) { return '<p>' + value + '</p>'; };
    parser.setOptions = function() { throw new Error('Markdown configuration failed'); };
    window._chatMarkedConfiguredFor = null;
    renderFallback('configuration failure <img src=x onerror=alert(1)>', 'configuration failure fallback');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = strings.Replace(page, "</head>", "<script>"+markedScript+"</script>"+runner+"</head>", 1)

	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	browserResult := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/marked.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write([]byte(markedScript))
		case r.URL.Path == "/alerts" && r.Method == http.MethodGet && r.Header.Get("HX-Request") == "true":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderList()))
		case r.URL.Path == "/alerts/markdown-detail-1/details" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			var detail bytes.Buffer
			if err := AlertDetail(fullAlert).Render(context.Background(), &detail); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(detail.Bytes())
		case r.URL.Path == "/alerts/empty-detail-1/details" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			var detail bytes.Buffer
			if err := AlertDetail(models.Alert{ID: emptyAlert.ID, ProjectID: projectID, Title: emptyAlert.Title, Message: emptyAlert.Message}).Render(context.Background(), &detail); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(detail.Bytes())
		case r.URL.Path == "/api/system/update" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "alerts-markdown-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking",
		"--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "alerts-markdown-browser-profile"),
		server.URL+"/alerts?project_id="+projectID,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome, lastProgress string
	deadline := time.After(20 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			if strings.HasPrefix(result, "progress:") {
				lastProgress = strings.TrimPrefix(result, "progress:")
				continue
			}
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result; last progress=" + lastProgress
		}
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Alerts Markdown browser regression failed: %s\nChrome stderr:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}

func TestSystemUpdateSurfacesShareNormalizedSnapshotStateInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)

	currentSnapshot := `{"current_version":"0.3.0","state":"available","distribution":"standalone","channel":"stable","manual":false,"staged":true,"release":{"metadata":{"version":"0.4.0","release_notes_url":"https://example.test/releases/0.4.0"},"target":{"image_ref":""},"apply_supported":true},"drain":{"active":{}}}`
	snapshots := map[string]string{
		"actionable":  currentSnapshot,
		"succeeded":   `{"current_version":"0.4.0","state":"succeeded","distribution":"standalone","channel":"stable","manual":false,"staged":true,"release":{"metadata":{"version":"0.4.0"},"target":{"image_ref":""},"apply_supported":true},"drain":{"active":{}}}`,
		"uptodate":    `{"current_version":"0.4.0","state":"available","distribution":"standalone","channel":"stable","manual":false,"staged":true,"release":{"metadata":{"version":"0.4.0"},"target":{"image_ref":""},"apply_supported":true},"drain":{"active":{}}}`,
		"hosted":      `{"current_version":"0.3.0","state":"available","distribution":"hosted","channel":"stable","manual":false,"staged":false,"release":{"metadata":{"version":"0.4.1"},"target":{"image_ref":""},"apply_supported":true},"drain":{"active":{}}}`,
		"unsupported": `{"current_version":"0.3.0","state":"available","distribution":"standalone","channel":"stable","manual":false,"staged":true,"release":{"metadata":{"version":"0.4.2"},"target":{"image_ref":""},"apply_supported":false},"drain":{"active":{}}}`,
		"manualReady": `{"current_version":"0.3.0","state":"available","distribution":"docker","channel":"stable","manual":true,"staged":false,"release":{"metadata":{"version":"0.5.0"},"target":{"image_ref":"openvibely:0.5.0"},"apply_supported":true},"drain":{"active":{}}}`,
		"manualBusy":  `{"current_version":"0.3.0","state":"waiting_for_idle","distribution":"docker","channel":"stable","manual":true,"staged":false,"release":{"metadata":{"version":"0.5.1"},"target":{"image_ref":"openvibely:0.5.1"},"apply_supported":true},"drain":{"active":{"task_executions":1,"chat_executions":2,"automation_activities":3}}}`,
	}
	var mu sync.Mutex

	setSnapshot := func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		if snapshot, ok := snapshots[name]; ok {
			currentSnapshot = snapshot
		}
		return currentSnapshot
	}
	getSnapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return currentSnapshot
	}

	var rendered bytes.Buffer
	if err := Alerts(nil, "project-update-browser", nil, 0).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render Alerts page: %v", err)
	}

	runner := `<script>
	window.addEventListener('DOMContentLoaded', function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
	  function fail(message) { throw new Error(message); }
	  function hidden(id) { var el = document.getElementById(id); if (!el) fail('missing ' + id); return el.classList.contains('hidden'); }
	  function visibleToast() { return document.querySelector('[data-system-update-toast]:not(.toast-dismiss)'); }
	  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
	  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
	  (async function() {
	    localStorage.clear();
	    if (window.openVibelySystemUpdatePoll) clearInterval(window.openVibelySystemUpdatePoll);
	    if (window.openVibelySystemUpdateIndicatorPoll) clearInterval(window.openVibelySystemUpdateIndicatorPoll);
	    if (typeof window.openVibelyNormalizeSystemUpdateSnapshot !== 'function') fail('missing shared system update normalizer');
	    var cases = [
	      {name:'actionable', hidden:false, actionable:true, card:true, accept:true, badge:true, toast:true, acceptText:'Update OpenVibely'},
	      {name:'succeeded', hidden:true, actionable:false, card:false, accept:false, badge:false, toast:false},
	      {name:'uptodate', hidden:true, actionable:false, card:false, accept:false, badge:false, toast:false},
	      {name:'hosted', hidden:false, actionable:false, card:true, accept:false, badge:false, toast:false},
	      {name:'unsupported', hidden:false, actionable:false, card:true, accept:false, badge:false, toast:false},
	      {name:'manualBusy', hidden:false, actionable:false, card:true, accept:false, badge:false, toast:false, cancel:true},
	      {name:'manualReady', hidden:false, actionable:true, card:true, accept:true, badge:true, toast:true, acceptText:'Prepare for update'}
	    ];
	    for (var i = 0; i < cases.length; i++) {
	      var c = cases[i];
	      var response = await fetch('/browser-scenario?name=' + encodeURIComponent(c.name), {method:'POST'});
	      var snapshot = await response.json();
	      var view = window.openVibelyNormalizeSystemUpdateSnapshot(snapshot);
	      if (view.hidden !== c.hidden) fail(c.name + ' normalized hidden mismatch');
	      if (view.actionable !== c.actionable) fail(c.name + ' normalized actionable mismatch');
	      await refreshSystemUpdateCard();
	      if (window.openVibelyHandleSystemUpdateSnapshot) window.openVibelyHandleSystemUpdateSnapshot(snapshot);
	      await wait(40);
	      if (window.openVibelyHandleSystemUpdateSnapshot) window.openVibelyHandleSystemUpdateSnapshot(snapshot);
	      if (hidden('system-update-card') === c.card) fail(c.name + ' Alerts card visibility disagrees with normalized hidden state');
	      if (!hidden('system-update-accept') !== c.accept && c.card) fail(c.name + ' Alerts accept visibility disagrees with normalized actionable state');
	      if (!hidden('system-update-nav-badge') !== c.badge) fail(c.name + ' global badge visibility disagrees with normalized actionable state');
	      if (!!visibleToast() !== c.toast) fail(c.name + ' global toast visibility disagrees with normalized actionable state');
	      if (c.acceptText && document.getElementById('system-update-accept').textContent !== c.acceptText) fail(c.name + ' accept copy was not rendered from normalized state');
	      if (c.cancel !== undefined && !hidden('system-update-cancel') !== c.cancel) fail(c.name + ' cancel visibility mismatch');
	    }
	    await report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`
	page := strings.Replace(rendered.String(), "</head>", runner+"</head>", 1)

	browserResult := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/system/update" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(getSnapshot()))
		case r.URL.Path == "/browser-scenario" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(setSnapshot(r.URL.Query().Get("name"))))
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "system-update-surfaces-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "system-update-surfaces-browser-profile"),
		server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(15 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("system update shared-state browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}

func TestAlertsLiveRefreshAndSingleDeletePreserveViewportInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var mu sync.Mutex
	alerts := make([]models.AlertSummary, 30)
	for i := range alerts {
		message := strings.Repeat("Long alert content ", 5)
		if i == 2 || i == 5 || i == 8 || i == 11 || i == 14 || i == 17 || i == 20 || i == 24 || i == 27 {
			message += " filtered-focus"
		}
		alerts[i] = models.AlertSummary{
			ID:        fmt.Sprintf("item-%02d", i),
			ProjectID: "project-alerts-browser",
			Title:     fmt.Sprintf("Notification %02d", i),
			Message:   message,
			IsRead:    i%3 == 0,
		}
		if i%2 == 1 {
			alerts[i].DecisionState = models.AlertDecisionPending
		} else {
			alerts[i].DecisionState = models.AlertDecisionNotRequired
		}
	}
	renderAlerts := func() string {
		mu.Lock()
		defer mu.Unlock()
		unread := 0
		for _, alert := range alerts {
			if !alert.IsRead {
				unread++
			}
		}
		var out bytes.Buffer
		if err := AlertsContent(append([]models.AlertSummary(nil), alerts...), "project-alerts-browser", unread).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Alerts content: %v", err)
		}
		return out.String()
	}
	renderAlertsPage := func() string {
		mu.Lock()
		defer mu.Unlock()
		unread := 0
		for _, alert := range alerts {
			if !alert.IsRead {
				unread++
			}
		}
		var out bytes.Buffer
		if err := Alerts(nil, "project-alerts-browser", append([]models.AlertSummary(nil), alerts...), unread).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Alerts page: %v", err)
		}
		return out.String()
	}
	deleteAlert := func(id string) {
		mu.Lock()
		defer mu.Unlock()
		for i := range alerts {
			if alerts[i].ID == id {
				alerts = append(alerts[:i], alerts[i+1:]...)
				return
			}
		}
	}
	prependAlert := func(kind string) {
		mu.Lock()
		defer mu.Unlock()
		alert := models.AlertSummary{
			ID:            "live-" + kind,
			ProjectID:     "project-alerts-browser",
			Title:         "Live " + kind,
			Message:       strings.Repeat("New alert content ", 5),
			DecisionState: models.AlertDecisionNotRequired,
		}
		if kind == "notification" {
			alert.DecisionState = models.AlertDecisionPending
		}
		alerts = append([]models.AlertSummary{alert}, alerts...)
	}

	runner := `<script>
	window.handleDropdownToggle = function(event) { event.stopPropagation(); };
	window.addEventListener('DOMContentLoaded', function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
	  function fail(message) { throw new Error(message); }
	  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
	  function waitFor(check, label) {
	    var started = performance.now();
	    return new Promise(function(resolve, reject) {
	      function poll() {
	        try { if (check()) return resolve(); } catch (error) { return reject(error); }
	        if (performance.now() - started > 5000) return reject(new Error('timed out waiting for ' + label));
	        setTimeout(poll, 10);
	      }
	      poll();
	    });
	  }
	  function row(id) { return document.querySelector('[data-alert-scroll-anchor="' + id + '"]'); }
	  function remove(id) {
	    var button = row(id) && row(id).querySelector('[data-alert-delete]');
	    if (!button) fail('missing delete control for ' + id);
	    button.focus({preventScroll:true});
	    button.click();
	    return waitFor(function() { return !row(id); }, id + ' deletion');
	  }
	  function assertNear(actual, expected, label) {
	    if (Math.abs(actual - expected) > 3) fail(label + ' moved by ' + (actual - expected) + 'px');
	  }
	  var detectTransientTopJump = false;
	  var transientTopJump = false;
	  document.body.addEventListener('htmx:afterSwap', function(event) {
	    var target = event.detail && event.detail.target;
	    if (!detectTransientTopJump || !target || target.id !== 'alerts-content') return;
	    requestAnimationFrame(function() {
	      var liveRoot = document.getElementById('alerts-container');
	      if (liveRoot && liveRoot.scrollTop < 100) transientTopJump = true;
	    });
	  });
	  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
	  (async function() {
	    await waitFor(function() { return window.htmx && row('item-15'); }, 'Alerts hydration');
	    htmx.process(document.body);
		    await waitFor(function() {
		      var card = document.getElementById('system-update-card');
		      return card && !card.classList.contains('hidden');
		    }, 'active system update card');
		    var originalUpdateCard = document.getElementById('system-update-card');
		    var root = document.getElementById('alerts-container');
		    var originalScrollport = root;
		    root.scrollTop = row('item-15').offsetTop - root.offsetTop - 70;
		    await wait(50);
		    var liveAnchorTop = row('item-14').getBoundingClientRect().top;
		    detectTransientTopJump = true;
		    await fetch('/browser-add?kind=operational', {method:'POST'});
		    htmx.trigger(document.body, 'alertUpdate');
		    await waitFor(function() { return !!row('live-operational'); }, 'live operational alert refresh');
		    await wait(1250);
		    detectTransientTopJump = false;
		    root = document.getElementById('alerts-container');
		    if (document.getElementById('system-update-card') !== originalUpdateCard) fail('live operational alert replaced the active system update card');
		    if (originalUpdateCard.classList.contains('hidden')) fail('active system update card became hidden after live operational refresh');
		    if (root !== originalScrollport) fail('live operational alert replaced the Alerts scrollport');
		    if (transientTopJump) fail('live operational alert painted the Alerts scrollport at the top before restoration');
		    assertNear(row('item-14').getBoundingClientRect().top, liveAnchorTop, 'live operational alert visible anchor');
		    if (root.scrollTop < 100) fail('live operational alert reset the Alerts scrollport');
		    if (row('live-operational').contains(document.activeElement)) fail('live operational alert received focus');
		    var liveSearch = document.querySelector('input[data-card-search="alerts"]');
		    liveSearch.value = 'notification';
		    liveSearch.dispatchEvent(new Event('input', {bubbles:true}));
		    await wait(50);

		    liveAnchorTop = row('item-14').getBoundingClientRect().top;
		    transientTopJump = false;
		    detectTransientTopJump = true;
		    await fetch('/browser-add?kind=notification', {method:'POST'});
		    htmx.trigger(document.body, 'alertUpdate');
		    await waitFor(function() { return !!row('live-notification'); }, 'live actionable notification refresh');
		    await wait(1250);
		    detectTransientTopJump = false;
		    root = document.getElementById('alerts-container');
		    if (document.getElementById('system-update-card') !== originalUpdateCard) fail('live actionable notification replaced the active system update card');
		    if (originalUpdateCard.classList.contains('hidden')) fail('active system update card became hidden after live notification refresh');
		    if (root !== originalScrollport) fail('live actionable notification replaced the Alerts scrollport');
		    if (transientTopJump) fail('live actionable notification painted the Alerts scrollport at the top before restoration');
		    assertNear(row('item-14').getBoundingClientRect().top, liveAnchorTop, 'live actionable notification visible anchor');
		    if (!row('live-notification').textContent.includes('pending')) fail('actionable notification state was not authoritatively refreshed');
		    if (row('live-notification').contains(document.activeElement)) fail('live actionable notification received focus');
		    liveSearch = document.querySelector('input[data-card-search="alerts"]');
		    if (liveSearch.value !== 'notification') fail('card search state was not restored after live refresh');
		    if (getComputedStyle(row('live-operational')).display !== 'none') fail('card search was not reapplied before live refresh settled');
		    liveSearch.value = '';
		    liveSearch.dispatchEvent(new Event('input', {bubbles:true}));
		    await waitFor(function() { return !new URL(window.location.href).searchParams.has('search'); }, 'search URL clear');
		    await wait(50);

		    var stableTop = row('item-14').getBoundingClientRect().top;
	    detectTransientTopJump = true;
	    await remove('item-15');
	    await wait(250);
	    detectTransientTopJump = false;
	    root = document.getElementById('alerts-container');
	    if (root !== originalScrollport) fail('single delete replaced the Alerts scrollport');
	    if (transientTopJump) fail('single delete painted the Alerts scrollport at the top before restoration');
	    assertNear(row('item-14').getBoundingClientRect().top, stableTop, 'actionable notification nearest surviving anchor');
	    if (document.activeElement !== row('item-16').querySelector('[data-alert-delete]')) fail('focus did not move to the next delete control');
	    if (root.scrollTop < 100) fail('single delete reset the Alerts scrollport to the top');
	    if (!document.getElementById('alert-badge').textContent.trim()) fail('unread badge was not authoritatively refreshed');

	    var repeatedTop = row('item-14').getBoundingClientRect().top;
	    await remove('item-16');
	    await wait(250);
	    assertNear(row('item-14').getBoundingClientRect().top, repeatedTop, 'repeated delete nearest surviving anchor');
	    if (document.activeElement !== row('item-17').querySelector('[data-alert-delete]')) fail('repeated delete focus was not predictable');

	    var visibleAnchor = row('item-17').getBoundingClientRect().top;
	    await remove('item-01');
	    await wait(250);
	    assertNear(row('item-17').getBoundingClientRect().top, visibleAnchor, 'deletion above viewport');
	    var beforeBelow = row('item-17').getBoundingClientRect().top;
	    await remove('item-29');
	    await wait(250);
	    assertNear(row('item-17').getBoundingClientRect().top, beforeBelow, 'deletion below viewport');
	    if (document.getElementById('alerts-container').scrollTop < 100) fail('off-viewport deletion reset the scrollport');

	    root = document.getElementById('alerts-container');
	    root.scrollTop = root.scrollHeight;
	    await wait(50);
	    await remove('item-28');
	    await wait(250);
	    root = document.getElementById('alerts-container');
	    if (Math.abs((root.scrollHeight - root.clientHeight) - root.scrollTop) > 3) fail('deleting the last visible item did not preserve the end-of-list anchor');
	    if (document.activeElement !== row('item-27').querySelector('[data-alert-delete]')) fail('deleting the last visible item did not focus the previous delete control');
	    if (document.getElementById('alerts-container').scrollTop < 100) fail('deleting the last visible item reset the scrollport');

	    var search = document.querySelector('input[data-card-search="alerts"]');
	    search.value = 'filtered-focus';
	    search.dispatchEvent(new Event('input', {bubbles:true}));
	    await wait(50);
	    root = document.getElementById('alerts-container');
	    root.scrollTop = row('item-11').offsetTop - root.offsetTop - 70;
	    await wait(50);
	    if (getComputedStyle(row('item-12')).display !== 'none') fail('card search did not hide the adjacent non-matching row');
	    if (getComputedStyle(row('item-14')).display === 'none') fail('card search hid the expected focus fallback row');
	    var filteredTop = row('item-08').getBoundingClientRect().top;
	    await remove('item-11');
	    await wait(250);
		    assertNear(row('item-08').getBoundingClientRect().top, filteredTop, 'filtered delete nearest surviving visible anchor');	    if (document.activeElement !== row('item-14').querySelector('[data-alert-delete]')) fail('filtered delete did not focus the next visible delete control');
	    if (getComputedStyle(row('item-12')).display !== 'none') fail('persisted card search was not reapplied after deletion');
	    await report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`
	style := `<style>
	body { margin: 0; }
	#alerts-container { box-sizing: border-box; height: 420px !important; overflow-y: auto !important; padding: 12px; }
	[data-alert-scroll-anchor] { box-sizing: border-box; min-height: 132px; margin-bottom: 16px; }
	</style>`

	browserResult := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/alerts" && r.Method == http.MethodGet:
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderAlerts()))
				return
			}
			page := renderAlertsPage()
			page = strings.Replace(page, "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", style+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case r.URL.Path == "/api/system/update" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"current_version":"0.3.0","state":"available","distribution":"standalone","channel":"stable","manual":false,"staged":true,"release":{"metadata":{"version":"0.4.0"},"target":{"image_ref":""},"apply_supported":true},"drain":{"active":{}}}`))
		case r.URL.Path == "/browser-add" && r.Method == http.MethodPost:
			prependAlert(r.URL.Query().Get("kind"))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/alerts/item-") && r.Method == http.MethodDelete:
			deleteAlert(strings.TrimPrefix(r.URL.Path, "/alerts/"))
			w.Header().Set("HX-Trigger", "alertUpdate")
			_, _ = w.Write([]byte(renderAlerts()))
		case r.URL.Path == "/alerts/unread-count":
			mu.Lock()
			unread := 0
			for _, alert := range alerts {
				if !alert.IsRead {
					unread++
				}
			}
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `<span>%d</span>`, unread)
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "alerts-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "alerts-browser-profile"),
		server.URL+"/alerts?project_id=project-alerts-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(15 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Alerts browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
