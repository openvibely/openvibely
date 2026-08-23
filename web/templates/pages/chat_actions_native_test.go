//go:build darwin && openvibely_native_ui

package pages

import (
	"bytes"
	"context"
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

	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestChatActionsInNativeWailsWebView is an opt-in native WebKit regression.
// It intentionally does not run in ordinary headless test jobs because Wails
// creates a real desktop window. Run it from an interactive macOS session with:
// OPENVIBELY_RUN_NATIVE_UI=1 go test -tags openvibely_native_ui ./web/templates/pages -run TestChatActionsInNativeWailsWebView -count=1 -timeout 90s
func TestChatActionsInNativeWailsWebView(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("native Wails UI coverage is opt-in; set OPENVIBELY_RUN_NATIVE_UI=1")
	}

	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Native Chat actions fixture", nil, "project-native").Render(layout.WithDesktopMode(context.Background(), true), &base); err != nil {
		t.Fatalf("render production layout: %v", err)
	}
	baseHTML := base.String()
	productionStyle := extractNativeChatStyle(t, baseHTML)
	navigationScript := extractNativeChatNavigation(t, baseHTML)

	var chat bytes.Buffer
	if err := ChatContent(nil, nil, "project-native", nil, nil, false, false, 30).Render(context.Background(), &chat); err != nil {
		t.Fatalf("render production ChatContent: %v", err)
	}

	resultCh := make(chan string, 8)
	var stateMu sync.Mutex
	historyRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write(chat.Bytes())
				return
			}
			_, _ = w.Write([]byte(nativeChatDocument(productionStyle, navigationScript, chat.String(), nativeChatRunner)))
		case "/other":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<div id="other-page"><span hidden data-openvibely-page-title="Other - OpenVibely"></span><h1>Other</h1></div>`))
		case "/chat/history":
			stateMu.Lock()
			historyRequests++
			stateMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/chat-history-count":
			stateMu.Lock()
			count := historyRequests
			stateMu.Unlock()
			_, _ = fmt.Fprintf(w, "%d", count)
		case "/browser-result":
			result := r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			select {
			case resultCh <- result:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var childOutput bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"OPENVIBELY_NATIVE_UI_HELPER=1",
		"OPENVIBELY_NATIVE_UI_URL="+server.URL+"/chat?project_id=project-native",
	)
	cmd.Stdout = &childOutput
	cmd.Stderr = &childOutput
	if err := cmd.Start(); err != nil {
		t.Fatalf("start native Wails helper process: %v", err)
	}
	childDone := make(chan error, 1)
	go func() {
		childDone <- cmd.Wait()
	}()
	stopChild := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-childDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("native Wails helper did not terminate; output:\n%s", childOutput.String())
		}
	}

	var outcome string
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	select {
	case outcome = <-resultCh:
		stopChild()
	case err := <-childDone:
		t.Fatalf("native Wails helper exited before reporting a browser result: %v\n%s", err, childOutput.String())
	case <-deadline.C:
		stopChild()
		t.Fatalf("timed out waiting for native Wails Chat actions result\n%s", childOutput.String())
	}

	if !strings.HasPrefix(outcome, "pass:") {
		t.Fatalf("native Wails Chat actions fixture failed: %s\n%s", outcome, childOutput.String())
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if historyRequests != 0 {
		t.Fatalf("cancelled Chat clear action issued %d history request(s) in native WebKit", historyRequests)
	}
}

// TestMain runs the native helper on the process's locked main goroutine.
// Wails desktop event loops cannot be started from a normal Go test goroutine.
func TestMain(m *testing.M) {
	if os.Getenv("OPENVIBELY_NATIVE_UI_HELPER") == "1" {
		if err := runNativeWailsHelper(); err != nil {
			fmt.Fprintf(os.Stderr, "native Wails helper failed: %v\\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func runNativeWailsHelper() error {
	url := strings.TrimSpace(os.Getenv("OPENVIBELY_NATIVE_UI_URL"))
	if url == "" {
		return fmt.Errorf("OPENVIBELY_NATIVE_UI_URL is empty")
	}

	app := application.New(application.Options{
		Name:        "OpenVibely native Chat actions test",
		Description: "Opt-in native WebKit regression for Chat actions",
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:        "native-chat-actions-test",
		Title:       "OpenVibely native Chat actions test",
		URL:         url,
		Width:       1100,
		Height:      760,
		AlwaysOnTop: true,
	})
	if window == nil {
		return fmt.Errorf("Wails did not create the native WebView window")
	}
	return app.Run()
}

func extractNativeChatStyle(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, "<style>")
	if start < 0 {
		t.Fatal("could not isolate production layout CSS")
	}
	endOffset := strings.Index(html[start:], "</style>")
	if endOffset < 0 {
		t.Fatal("could not find end of production layout CSS")
	}
	return html[start : start+endOffset+len("</style>")]
}

func extractNativeChatNavigation(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, "window.openVibelyNavigate = function")
	if start < 0 {
		t.Fatal("could not isolate production HTMX navigation helper")
	}
	endOffset := strings.Index(html[start:], "// Scroll position restoration for drop zones")
	if endOffset < 0 {
		t.Fatal("could not find end of production HTMX navigation helper")
	}
	return html[start : start+endOffset]
}

func nativeChatDocument(style, navigation, chat, runner string) string {
	return `<!doctype html><html lang="en" data-theme="dark" data-openvibely-runtime="desktop"><head><meta charset="utf-8"><script src="/htmx-2.0.4.min.js"></script><script>` + navigation + `</script>` + style + `<style>
html, body, #main-content { height: 100%; margin: 0; }
.dropdown-content { display: none; }
.dropdown:focus-within > .dropdown-content { display: block; }
#chat-page-root [data-chat-actions-dropdown][data-chat-actions-open="true"] > .dropdown-content {
  visibility: visible !important;
  opacity: 1 !important;
  pointer-events: auto !important;
}
.hidden { display: none !important; }
</style></head><body><main id="main-content">` + chat + `</main>` + runner + `</body></html>`
}

const nativeChatRunner = `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try {
          if (check()) { resolve(); return; }
        } catch (error) {
          reject(error);
          return;
        }
        if (performance.now() - started > (timeout || 7000)) {
          reject(new Error('timed out waiting for ' + label));
          return;
        }
        setTimeout(poll, 20);
      }
      poll();
    });
  }
  function root() { return document.getElementById('chat-page-root'); }
  function dropdown() { return document.querySelector('[data-chat-actions-dropdown]'); }
  function trigger() { return document.querySelector('[data-chat-actions-dropdown] [aria-controls="chat-actions-menu"]'); }
  function menu() { return document.getElementById('chat-actions-menu'); }
  function clearItem() { return document.querySelector('[data-chat-actions-dropdown] [role="menuitem"]'); }
  function visible() {
    var currentMenu = menu();
    if (!currentMenu) return false;
    var styles = getComputedStyle(currentMenu);
    return styles.visibility !== 'hidden' && styles.opacity !== '0' && styles.pointerEvents !== 'none';
  }
  function menuState() {
    var currentDropdown = dropdown();
    var currentTrigger = trigger();
    var currentMenu = menu();
    var styles = currentMenu ? getComputedStyle(currentMenu) : null;
    return 'open=' + (currentDropdown && currentDropdown.getAttribute('data-chat-actions-open')) +
      ' aria=' + (currentTrigger && currentTrigger.getAttribute('aria-expanded')) +
      ' display=' + (styles && styles.display) +
      ' visibility=' + (styles && styles.visibility) +
      ' opacity=' + (styles && styles.opacity) +
      ' pointerEvents=' + (styles && styles.pointerEvents) +
      ' active=' + (document.activeElement && document.activeElement.id);
  }
  function assertClosed(stage) {
    var currentRoot = root();
    var currentDropdown = dropdown();
    var currentTrigger = trigger();
    var currentMenu = menu();
    if (!currentRoot || !currentDropdown || !currentTrigger || !currentMenu) fail(stage + ' lost Chat actions controls');
    if (currentDropdown.hasAttribute('data-chat-actions-open')) fail(stage + ' left the explicit open state set');
    if (currentTrigger.getAttribute('aria-expanded') !== 'false') fail(stage + ' left aria-expanded open');
    if (visible()) fail(stage + ' left the menu visible');
    if (document.activeElement && currentMenu.contains(document.activeElement)) fail(stage + ' left focus inside the hidden menu');
  }
  function assertOpen(stage) {
    var currentDropdown = dropdown();
    var currentTrigger = trigger();
    if (!currentDropdown || !currentTrigger || !visible()) fail(stage + ' did not open the menu: ' + menuState());
    if (currentDropdown.getAttribute('data-chat-actions-open') !== 'true') fail(stage + ' did not set explicit open state: ' + menuState());
    if (currentTrigger.getAttribute('aria-expanded') !== 'true') fail(stage + ' did not update aria-expanded: ' + menuState());
  }
  function key(target, value) {
    target.dispatchEvent(new KeyboardEvent('keydown', {key: value, bubbles: true, cancelable: true}));
  }
  function reportResult(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}).catch(function() {});
  }
  function markFailure(error) {
    var errorText = String(error);
    var stack = error && error.stack ? String(error.stack) : '';
    var message = errorText + (stack && stack.indexOf(errorText) === -1 ? '\\n' + stack : '');
    var currentRoot = root() || document.body;
    currentRoot.setAttribute('data-test-result', 'fail');
    currentRoot.setAttribute('data-test-error', message);
    reportResult('fail', message);
  }

  (async function() {
    var reloadStage = sessionStorage.getItem('native-chat-actions-reload-stage');
    await waitFor(function() { return root() && trigger() && menu(); }, reloadStage ? 'reloaded Chat actions controls' : 'initial Chat actions controls');
    await waitFor(function() { return typeof htmx !== 'undefined'; }, 'HTMX runtime');

    // Check the state produced by the real native WebKit page load before
    // forcing the trigger focus that reproduces WebView startup focus.
    assertClosed(reloadStage ? 'native reload startup' : 'native startup');
    trigger().focus();
    await wait(80);
    assertClosed(reloadStage ? 'native reload focus' : 'native startup focus');

    if (!reloadStage) {
      trigger().click();
      await wait(40);
      assertOpen('native mouse activation');
      trigger().click();
      await wait(40);
      assertClosed('native mouse close');
      if (document.activeElement !== trigger()) fail('native mouse close did not restore focus to the trigger');

      key(trigger(), 'Enter');
      await wait(40);
      assertOpen('native Enter activation');
      menu().querySelector('[role="menuitem"]').focus();
      key(menu().querySelector('[role="menuitem"]'), 'Escape');
      await wait(40);
      assertClosed('native Escape from menu item');
      if (document.activeElement !== trigger()) fail('native Escape from menu item did not restore focus to the trigger');

      key(trigger(), ' ');
      await wait(40);
      assertOpen('native Space activation');
      key(trigger(), 'Escape');
      await wait(40);
      assertClosed('native Escape from trigger');
      if (document.activeElement !== trigger()) fail('native Escape from trigger did not retain focus on the trigger');

      var confirmationMessage = '';
      window.confirm = function(message) {
        confirmationMessage = String(message);
        return false;
      };
      trigger().click();
      await wait(40);
      assertOpen('native confirmation setup');
      clearItem().click();
      await wait(120);
      if (confirmationMessage !== 'Clear all chat history? This cannot be undone.') fail('native clear action used unexpected confirmation text: ' + confirmationMessage);
      var historyCount = await fetch('/chat-history-count').then(function(response) { return response.text(); });
      if (historyCount !== '0') fail('native cancelled clear action issued ' + historyCount + ' history request(s)');
      document.body.dispatchEvent(new Event('pointerdown', {bubbles: true}));
      await wait(40);
      assertClosed('native outside close after cancelled confirmation');

      trigger().click();
      await wait(40);
      assertOpen('native navigation setup');
      await window.openVibelyNavigate('/other');
      await waitFor(function() { return document.getElementById('other-page'); }, 'native other page');
      history.back();
      await waitFor(function() { return root() && trigger() && menu(); }, 'native history-back Chat');
      await wait(120);
      assertClosed('native history-back restoration');
      sessionStorage.setItem('native-chat-actions-reload-stage', 'pending');
      location.reload();
      return;
    }

    sessionStorage.removeItem('native-chat-actions-reload-stage');
    key(trigger(), 'Enter');
    await wait(40);
    assertOpen('native post-reload Enter activation');
    key(trigger(), 'Escape');
    await wait(40);
    assertClosed('native post-reload Escape');
    if (document.activeElement !== trigger()) fail('native post-reload Escape did not retain focus on the trigger');
    root().setAttribute('data-test-result', 'pass');
    await reportResult('pass', 'native-wails-webkit');
  })().catch(markFailure);
});
</script>`
