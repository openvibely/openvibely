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

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ChatRenderedLinksOpenOutsideAppAndDesktopExternalBrowserBridge(t *testing.T) {
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
		if static.ServeAsset(w, r) {
			return
		}
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
	return `<!DOCTYPE html><html lang="en" data-openvibely-runtime="web" data-runtime="web"><head><meta charset="UTF-8"><title>Chat links</title><script src="/static/vendor/marked.min.js" data-ov-asset="marked"></script><script src="/static/file-math-parser.js" data-ov-math-parser></script></head><body><script>` +
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
	    var publishedURL = '/files/707b783cc80fc7c3f59e907220b226d0?project_id=301ebd410bafc3adf718022dbed7466d';
	    var sources = [
	      '[Download](<' + publishedURL + '>)',
	      '[Download](' + publishedURL + ')',
	      '[Download](<' + publishedURL + '> "PDF")',
	      '[Download][pdf]\n\n[pdf]:\n  <' + publishedURL + '>',
	      '> [Download][pdf]\n>\n> [pdf]: <' + publishedURL + '>',
	      '![Preview](<' + publishedURL + '>)'
	    ];
	    function checkPublishedLinks(root, expected) {
	      var links = root.querySelectorAll('a, img');
	      if (links.length !== expected) fail('wrong published link count: ' + links.length);
	      links.forEach(function(link) {
	        var url = link.getAttribute(link.tagName === 'IMG' ? 'src' : 'href');
	        if (url !== publishedURL) fail('broken published URL: ' + url);
	        if (new URL(url, 'wails://localhost/tasks/task-id').pathname !== publishedURL.split('?')[0]) fail('task-relative URL');
	      });
	    }
	    sources.forEach(function(source) {
	      var root = document.createElement('div');
	      root.innerHTML = window.renderChatMarkdown(source);
	      checkPublishedLinks(root, 1);
	    });
	    var malformed = '](<img src="https://example.test/tracker.png">';
	    var safety = document.createElement('div');
	    safety.innerHTML = window.renderChatMarkdown(malformed);
	    if (safety.querySelector('img') || safety.textContent.trim() !== malformed) fail('malformed link became HTML');
	    safety.innerHTML = window.renderChatMarkdown('[bad](<javascript:alert(1)>)');
	    if (safety.querySelector('a[href]')) fail('unsafe destination survived sanitizer');
	    safety.innerHTML = window.renderChatMarkdown(String.fromCharCode(96) + sources[0] + String.fromCharCode(96));
	    if (safety.querySelector('a') || !safety.querySelector('code')) fail('code example became a link');
	    var svg = '<svg xmlns="http://www.w3.org/2000/svg"><circle r="2"/></svg>';
	    safety.innerHTML = window.renderChatMarkdown(svg);
	    if (safety.querySelector('svg') || !safety.querySelector('code.language-svg') || safety.querySelector('code').textContent.trim() !== svg) fail('SVG preview source regressed');
	    var tick = String.fromCharCode(96);
	    var svgCode = 'Inline ' + tick + svg + tick + ' example.';
	    var svgLink = '[Example ' + tick + svg + tick + '](<' + publishedURL + '>)';
	    function checkSVGCode(root, linked) {
	      if (root.querySelector('pre, svg, .chat-svg-preview')) fail('inline SVG code became a preview');
	      var code = root.querySelector(linked ? 'a > code' : 'p > code');
	      if (!code || code.textContent !== svg) fail('inline SVG code was split');
	      if (linked) checkPublishedLinks(root, 1);
	      else if (root.textContent.trim() !== 'Inline ' + svg + ' example.') fail('inline SVG prose changed');
	    }
	    safety.innerHTML = window.renderChatMarkdown(svgCode);
	    checkSVGCode(safety, false);
	    safety.innerHTML = window.renderChatMarkdown(svgLink);
	    checkSVGCode(safety, true);
	    var large = ('padding paragraph\n\n').repeat(4000) + sources.join('\n\n') + '\n\n' + malformed;
	    var fragment = await window.renderChatMarkdownAsync(large, {});
	    if (!window._markdownWorkerURL || !fragment || typeof fragment === 'string') fail('large Markdown worker did not render');
	    checkPublishedLinks(fragment, sources.length);
	    if (!fragment.textContent.includes(malformed)) fail('worker failed to preserve literal HTML');
	    var svgFragment = await window.renderChatMarkdownAsync(('padding paragraph\n\n').repeat(4000) + svgCode + '\n\n' + svgLink + '\n\n' + svg, {});
	    var svgParagraphs = svgFragment.querySelectorAll('p');
	    var codeParagraph = Array.from(svgParagraphs).find(function(p) { return p.textContent.startsWith('Inline '); });
	    var linkParagraph = Array.from(svgParagraphs).find(function(p) { return p.querySelector('a'); });
	    if (!codeParagraph || !linkParagraph) fail('worker lost SVG code or link');
	    var codeRoot = document.createElement('div');
	    codeRoot.appendChild(codeParagraph.cloneNode(true));
	    checkSVGCode(codeRoot, false);
	    var linkRoot = document.createElement('div');
	    linkRoot.appendChild(linkParagraph.cloneNode(true));
	    checkSVGCode(linkRoot, true);
	    if (svgFragment.querySelectorAll('pre > code.language-svg').length !== 1) fail('worker SVG preview count changed');
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
