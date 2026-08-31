package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestChatRenderedLinksOpenOutsideAppAndDesktopExternalBrowserBridge(t *testing.T) {
	chrome := chatNavigationChromePath(t)

	var base bytes.Buffer
	if err := layout.Base("Chat link fixture", nil, "project-links").Render(context.Background(), &base); err != nil {
		t.Fatalf("render base: %v", err)
	}

	var mu sync.Mutex
	opened := make([]string, 0, 2)
	browserResult := make(chan string, 4)

	fixtureHTML := chatLinkOpeningBrowserFixture(t, base.String())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(fixtureHTML))
		case "/open-external":
			if r.Method != http.MethodPost || r.Header.Get("X-OpenVibely-Desktop") != "1" {
				http.Error(w, "bad external open", http.StatusForbidden)
				return
			}
			mu.Lock()
			opened = append(opened, r.URL.Query().Get("url"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/opened-count":
			mu.Lock()
			body := strings.Join(opened, "\n")
			mu.Unlock()
			_, _ = w.Write([]byte(body))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chat-link-opening.html")
	stderrPath := filepath.Join(t.TempDir(), "chat-link-opening.stderr")
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
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chat-link-opening-profile"),
		server.URL+"/",
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome Chat link fixture: %v", err)
	}

	var outcome string
	deadline := time.After(15 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result"
		}
	}
	stopBrowserProcess(cmd)

	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("real Chat link-opening fixture failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || opened[0] != "https://example.com/from-chat" {
		t.Fatalf("desktop bridge opened URLs = %v, want only the external Chat link", opened)
	}
}

func chatLinkOpeningBrowserFixture(t *testing.T, baseHTML string) string {
	t.Helper()
	markdownStart := strings.Index(baseHTML, "window.configureChatMarked = function")
	markdownEnd := strings.Index(baseHTML[markdownStart:], "// Add copy buttons")
	if markdownStart < 0 || markdownEnd < 0 {
		t.Fatal("could not isolate production Markdown sanitizer script")
	}
	desktopStart := strings.Index(baseHTML, "<!-- Desktop external-link bridge -->")
	if desktopStart < 0 {
		t.Fatal("could not isolate production desktop external-link bridge")
	}
	desktopScriptEnd := strings.Index(baseHTML[desktopStart:], "</script>")
	if desktopScriptEnd < 0 {
		t.Fatal("could not isolate production desktop external-link bridge")
	}
	desktopEnd := desktopScriptEnd + len("</script>")
	return `<!DOCTYPE html><html lang="en" data-openvibely-runtime="web" data-runtime="web"><head><meta charset="UTF-8"><title>Chat links</title></head><body><script>` +
		baseHTML[markdownStart:markdownStart+markdownEnd] +
		`</script>` +
		baseHTML[desktopStart:desktopStart+desktopEnd] +
		chatLinkOpeningBrowserRunner() +
		`</body></html>`
}

func chatLinkOpeningBrowserRunner() string {
	return `<div id="fixture" class="chat-stream-content"></div>
	<script>
	window.addEventListener('DOMContentLoaded', function() {
	  function fail(message) { throw new Error(message); }
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), { method: 'POST' }).catch(function() {}); }
	  function markFailure(error) {
	    var message = String(error && error.stack || error);
	    document.documentElement.setAttribute('data-test-result', 'fail');
	    document.documentElement.setAttribute('data-test-error', message);
	    report('fail', message);
	  }
	  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
	  function openedURLs() { return fetch('/opened-count').then(function(resp) { return resp.text(); }); }
	  function setRuntime(mode) {
	    document.documentElement.setAttribute('data-openvibely-runtime', mode);
	    document.documentElement.setAttribute('data-runtime', mode);
	  }
	  function renderFixture() {
	    window.marked = { setOptions: function() {}, parse: function() {
	      return '<p><a data-case="external" href="https://example.com/from-chat">external</a> ' +
	        '<a data-case="autolink" href="https://bare.example/path">https://bare.example/path</a> ' +
	        '<a data-case="mailto" href="mailto:hello@example.com">mail</a> ' +
	        '<a data-case="internal" href="/tasks/internal">internal</a> ' +
	        '<a data-case="same-origin" href="' + location.origin + '/tasks/same">same</a> ' +
	        '<a data-case="unsafe" href="javascript:alert(1)" target="_blank" rel="opener">unsafe</a></p>' +
	        '<pre><code>https://code.example/not-clickable</code></pre>';
	    } };
	    var fixture = document.getElementById('fixture');
	    fixture.innerHTML = window.renderChatMarkdown('[external](https://example.com/from-chat)');
	    return fixture;
	  }
	  function anchor(name) { return document.querySelector('#fixture a[data-case="' + name + '"]'); }
	  (async function() {
	    var webOpened = [];
	    var originalOpen = window.open;
	    window.open = function(url, target, features) { webOpened.push([String(url), String(target || ''), String(features || '')]); return { opener: null }; };

	    setRuntime('web');
	    renderFixture();
	    ['external', 'autolink', 'mailto'].forEach(function(name) {
	      var a = anchor(name);
	      if (!a) fail('missing safe external anchor ' + name);
	      if (a.getAttribute('target') !== '_blank') fail(name + ' did not get target=_blank');
	      if (a.getAttribute('rel') !== 'noopener noreferrer') fail(name + ' did not get safe rel');
	      if (a.getAttribute('data-openvibely-chat-external-link') !== 'true') fail(name + ' missing chat external marker');
	    });
	    ['internal', 'same-origin'].forEach(function(name) {
	      var a = anchor(name);
	      if (!a) fail('missing internal anchor ' + name);
	      if (a.hasAttribute('target') || a.hasAttribute('rel') || a.hasAttribute('data-openvibely-chat-external-link')) fail(name + ' should preserve normal internal navigation without external markers');
	    });
	    var unsafe = anchor('unsafe');
	    if (!unsafe) fail('missing unsafe anchor');
	    if (unsafe.hasAttribute('href') || unsafe.hasAttribute('target') || unsafe.hasAttribute('rel') || unsafe.hasAttribute('data-openvibely-chat-external-link')) fail('unsafe link was not neutralized');
	    if (document.querySelector('#fixture pre code a')) fail('code URL was converted into a link');

	    anchor('internal').addEventListener('click', function(event) { event.preventDefault(); });
	    anchor('internal').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
	    if (webOpened.length !== 0) fail('web internal link opened externally: ' + JSON.stringify(webOpened));
	    anchor('external').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
	    if (webOpened.length !== 1 || webOpened[0][0] !== 'https://example.com/from-chat' || webOpened[0][1] !== '_blank' || webOpened[0][2].indexOf('noopener') === -1 || webOpened[0][2].indexOf('noreferrer') === -1) fail('web external link did not call window.open safely: ' + JSON.stringify(webOpened));

	    window.open = originalOpen;
	    setRuntime('desktop');
	    renderFixture();
	    if (document.documentElement.getAttribute('data-openvibely-runtime') !== 'desktop') fail('server runtime marker was not desktop');
	    if (document.documentElement.getAttribute('data-runtime') !== 'desktop') fail('client runtime marker did not preserve authoritative desktop mode');
	    var before = await openedURLs();
	    if (before !== '') fail('desktop opener called before click: ' + before);
	    anchor('internal').addEventListener('click', function(event) { event.preventDefault(); });
	    anchor('internal').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
	    await wait(100);
	    var afterInternal = await openedURLs();
	    if (afterInternal !== '') fail('internal app link opened externally: ' + afterInternal);
	    anchor('external').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
	    for (var i = 0; i < 20; i++) {
	      var opened = await openedURLs();
	      if (opened === 'https://example.com/from-chat') { await report('pass', ''); return; }
	      await wait(50);
	    }
	    fail('desktop external link did not call backend opener');
	  })().catch(markFailure);
	});
	</script>`
}
