package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_ChatLiveCreatedViewCleanupInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	project := models.Project{ID: "controller-chat", Name: "Controller Chat"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmxJS)
		case "/chat":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			document := renderTerminalBrowserComponent(t, Chat([]models.Project{project}, project.ID, nil, nil, nil, nil, nil, false, false, 30))
			_, _ = w.Write([]byte(installTerminalBrowserPrelude(document)))
		case "/events/running-executions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "chat-controller-cleanup", func(browser *composerFocusCDP) {
		browser.waitFor("Chat initialized", `String(document.readyState==='complete'&&!!window._chatLiveEventHandlers?.chatEvent)`, "true")
		browser.evaluate(`window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_new_message',project_id:'controller-chat',exec_id:'live-created',message:'live message',source:'api'}}));'sent'`)
		browser.waitFor("live-created view", `String(!!document.getElementById('streaming-message-live-created')&&!!window.__terminalStreamFor('live-created'))`, "true")
		if got := browser.evaluate(`(function(){
			var c=document.getElementById('streaming-message-live-created');
			window.__cleanupRun=window.openVibelyThreadCache.source(c.getAttribute('data-live-snapshot-key'));
			window.__cleanupView=window._chatEventSourceByExec['live-created'];
			// Exercise the actual HTMX swap path, keeping the Chat page mounted.
			htmx.swap(document.getElementById('chat-messages'),'',{swapStyle:'innerHTML'});
			return 'swapped';
		})()`); got != "swapped" {
			t.Fatalf("remove live-created bubble: %s", got)
		}
		browser.waitFor("removed view released", `String(window.__cleanupView.closed)+':'+window.__cleanupRun.views.size`, "true:0")
		if got := browser.evaluate(`String(window._chatEventSources.length)+':'+Object.keys(window._chatEventSourceByExec).length`); got != "0:0" {
			t.Fatalf("closed views retained by legacy registries: %s", got)
		}

		if got := browser.evaluate(`(function(){
			var stream=window.__terminalStreamFor('live-created');
			if(!stream)return 'background stream closed';
			stream.emit('message','background héllo');
			return window.__cleanupRun.output;
		})()`); got != "background héllo" {
			t.Fatalf("detaching view must preserve background updates: %s", got)
		}
	})
}
