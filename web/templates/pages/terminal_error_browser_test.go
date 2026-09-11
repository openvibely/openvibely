package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func terminalErrorBrowserPrelude() string {
	return `<script>
window.__terminalEventSources = [];
window.EventSource = function(url) { this.url = url; this.listeners = {}; this.closed = false; window.__terminalEventSources.push(this); };
window.EventSource.prototype.addEventListener = function(name, handler) { this.listeners[name] = handler; };
window.EventSource.prototype.close = function() { this.closed = true; };
window.EventSource.prototype.emit = function(name, data) {
  var event = {data: data};
  if (name === 'message' && this.onmessage) this.onmessage(event);
  if (this.listeners[name]) this.listeners[name](event);
};
window.__terminalStreamFor = function(execID) {
  for (var i = window.__terminalEventSources.length - 1; i >= 0; i--) {
    var source = window.__terminalEventSources[i];
    if (!source.closed && source.url.indexOf('/events/chat/' + execID) !== -1) return source;
  }
  return null;
};
</script>`
}

func renderTerminalBrowserComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var out bytes.Buffer
	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render terminal browser fixture: %v", err)
	}
	return out.String()
}

func installTerminalBrowserPrelude(document string) string {
	document = strings.Replace(document, `https://unpkg.com/htmx.org@2.0.4`, `/htmx-2.0.4.min.js`, 1)
	return strings.Replace(document, "<head>", "<head>"+terminalErrorBrowserPrelude(), 1)
}

func installTerminalBrowserRenderer(browser *composerFocusCDP) {
	browser.t.Helper()
	got := browser.evaluate(`(function(){
window.renderStreamingContent=function(el,text){
  el.textContent=text;
  if(window.setChatRawContent)window.setChatRawContent(el,text);else el.setAttribute('data-raw-content',text);
  return Promise.resolve(true);
};
window.renderLiveChatContent=window.renderStreamingContent;
return 'ready';
})()`)
	if got != "ready" {
		browser.t.Fatalf("install terminal browser renderer: %s", got)
	}
}

func installDelayedTerminalBrowserRenderer(browser *composerFocusCDP, delayMilliseconds int) {
	browser.t.Helper()
	got := browser.evaluate(`(function(){
var productionRenderer=window.renderLiveChatContent||window.renderStreamingContent;
if(typeof productionRenderer!=='function')return 'missing';
window.__terminalRenderStarted=0;
window.__terminalRenderSettled=0;
window.__terminalSyncCalls=0;
var productionSync=window.syncChatTranscriptRevision;
window.syncChatTranscriptRevision=function(execID){window.__terminalSyncCalls++;return productionSync(execID);};
window.renderLiveChatContent=function(el,text,yieldLarge){
  window.__terminalRenderStarted++;
  return new Promise(function(resolve,reject){
    setTimeout(function(){
      Promise.resolve(productionRenderer(el,text,yieldLarge)).then(function(value){window.__terminalRenderSettled++;resolve(value);},reject);
    },` + fmt.Sprintf("%d", delayMilliseconds) + `);
  });
};
return 'ready';
})()`)
	if got != "ready" {
		browser.t.Fatalf("install delayed production terminal browser renderer: %s", got)
	}
}

func TestTaskThreadLiveFailureProductionWiringInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-terminal-thread", Name: "Terminal thread project"}
	task := &models.Task{ID: "task-terminal-thread", ProjectID: project.ID, Title: "Terminal thread task", Status: models.StatusRunning, Category: models.CategoryActive}
	longOutput := strings.Repeat("long partial task-thread output line\n", 160)
	runningOne := models.Execution{ID: "thread-live-one", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "first live turn", IsFollowup: true}
	runningTwo := models.Execution{ID: "thread-live-two", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "second live turn", IsFollowup: true}
	failedOne := runningOne
	failedOne.Status = models.ExecFailed
	failedOne.Output = longOutput
	failedOne.ErrorMessage = "first terminal failure <unsafe>"
	failedTwo := runningTwo
	failedTwo.Status = models.ExecFailed
	failedTwo.Output = longOutput
	failedTwo.ErrorMessage = "second terminal failure"

	var phase atomic.Int32
	var lazyRequests atomic.Int32
	var fragmentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/tasks/"+task.ID:
			document := renderTerminalBrowserComponent(t, TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, "chat", nil))
			_, _ = w.Write([]byte(installTerminalBrowserPrelude(document)))
		case r.URL.Path == "/tasks/"+task.ID+"/thread" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, components.TaskThreadFollowupResponse("fresh live turn", "thread-live-fresh", nil, project.ID))))
		case r.URL.Path == "/tasks/"+task.ID+"/thread":
			lazyRequests.Add(1)
			viewTask := *task
			var executions []models.Execution
			switch phase.Load() {
			case 1:
				executions = []models.Execution{failedOne}
			case 2:
				executions = []models.Execution{failedOne, failedTwo}
			}
			if phase.Load() > 0 {
				viewTask.Status = models.StatusFailed
				viewTask.Category = models.CategoryCompleted
			}
			_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, components.TaskThreadView(&viewTask, executions, nil, nil, nil, nil, false, 30))))
		case strings.HasPrefix(r.URL.Path, "/tasks/"+task.ID+"/thread/executions/") && strings.HasSuffix(r.URL.Path, "/fragment"):
			fragmentRequests.Add(1)
			exec := runningOne
			if strings.Contains(r.URL.Path, runningTwo.ID) {
				exec = runningTwo
			}
			_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, components.ChatExecutionPair(exec, task, []models.Execution{exec}, 0, true, nil, "task-thread-messages", "task-thread-view", project.ID))))
		case strings.Contains(r.URL.Path, "composer-action") || r.URL.Path == "/auth/me":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	longOutputJSON, err := json.Marshal(longOutput)
	if err != nil {
		t.Fatalf("marshal long task-thread output: %v", err)
	}
	longOutputJS := string(longOutputJSON)
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/"+task.ID+"?tab=chat", "task-thread-terminal-wiring", func(browser *composerFocusCDP) {
		browser.waitFor("real lazy Thread load", `Boolean(document.getElementById('task-thread-messages'))+':'+Boolean(document.getElementById('task-thread-view'))`, "true:true")
		browser.waitFor("lazy Thread HTMX settle", `(function(){var el=document.getElementById('thread-content');return el.dataset.loaded+':'+el.dataset.loading})()`, "true:false")
		installDelayedTerminalBrowserRenderer(browser, 350)

		if got := browser.evaluate(`(function(){window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_thread_execution_started',task_id:'` + task.ID + `',exec_id:'` + runningOne.ID + `'}}));return 'sent';})()`); got != "sent" {
			t.Fatalf("dispatch first task-thread live event: %s", got)
		}
		browser.waitFor("real HTMX execution fragment append", `Boolean(document.getElementById('chat-execution-`+runningOne.ID+`'))+':'+Boolean(window.__terminalStreamFor('`+runningOne.ID+`'))`, "true:true")
		if got := browser.evaluate(`(function(){window.__terminalStreamFor('` + runningOne.ID + `').emit('message',` + longOutputJS + `);return 'streamed';})()`); got != "streamed" {
			t.Fatalf("stream first task-thread partial output: %s", got)
		}
		browser.waitFor("first long task-thread output render", `document.getElementById('streaming-message-`+runningOne.ID+`').getAttribute('data-raw-content')===`+longOutputJS+`?'ready':'waiting'`, "ready")
		if got := browser.evaluate(`(function(){var messages=document.getElementById('task-thread-messages');messages.scrollTop=messages.scrollHeight;return 'pinned';})()`); got != "pinned" {
			t.Fatalf("pin first task-thread turn: %s", got)
		}
		phase.Store(1)
		if got := browser.evaluate(`(function(){window.__terminalStreamFor('` + runningOne.ID + `').emit('error','first terminal failure <unsafe>');return 'failed';})()`); got != "failed" {
			t.Fatalf("fail first task-thread stream: %s", got)
		}
		browser.waitFor("first task-thread terminal alert ordering and visibility", `(function(){var messages=document.getElementById('task-thread-messages'),pair=document.getElementById('chat-execution-`+runningOne.ID+`'),out=pair&&pair.querySelector('[data-raw-content]'),err=pair&&pair.querySelector('[data-terminal-error="true"]');return String(!!(out&&err&&(out.compareDocumentPosition(err)&Node.DOCUMENT_POSITION_FOLLOWING)&&err.getAttribute('role')==='alert'&&err.textContent==='Error: first terminal failure <unsafe>'&&err.innerHTML.indexOf('<unsafe>')===-1&&pair.querySelectorAll('[data-terminal-error="true"]').length===1&&(messages.scrollHeight-messages.scrollTop-messages.clientHeight)<=2));})()`, "true")

		if got := browser.evaluate(`(function(){window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_thread_execution_started',task_id:'` + task.ID + `',exec_id:'` + runningTwo.ID + `'}}));return 'sent';})()`); got != "sent" {
			t.Fatalf("dispatch second task-thread live event: %s", got)
		}
		browser.waitFor("second real HTMX execution fragment append", `Boolean(document.getElementById('chat-execution-`+runningTwo.ID+`'))+':'+Boolean(window.__terminalStreamFor('`+runningTwo.ID+`'))`, "true:true")
		if got := browser.evaluate(`(function(){window.__terminalStreamFor('` + runningTwo.ID + `').emit('message',` + longOutputJS + `);return 'streamed';})()`); got != "streamed" {
			t.Fatalf("stream second task-thread partial output: %s", got)
		}
		browser.waitFor("second task-thread output persisted before terminal render", `document.getElementById('streaming-message-`+runningTwo.ID+`').getAttribute('data-raw-content')===`+longOutputJS+`?'ready':'waiting'`, "ready")
		if got := browser.evaluate(`(function(){var messages=document.getElementById('task-thread-messages');messages.scrollTop=messages.scrollHeight;window.__threadSettledBeforeFailure=window.__terminalRenderSettled;window.__terminalStreamFor('` + runningTwo.ID + `').emit('error','second terminal failure');return String(!document.getElementById('chat-execution-` + runningTwo.ID + `').querySelector('[data-terminal-error="true"]'));})()`); got != "true" {
			t.Fatalf("resumed task-thread terminal alert must wait for pending render: %s", got)
		}
		browser.wheel("#task-thread-messages", -650)
		browser.waitFor("native upward task-thread reading intent during resumed terminal render", `(function(){var messages=document.getElementById('task-thread-messages'),tracker=window._taskThreadPageTracker;return String(!!(tracker&&tracker.userScrolledUp&&messages.scrollTop<messages.scrollHeight-messages.clientHeight-100));})()`, "true")
		browser.waitFor("stable resumed task-thread native scroll position", `(function(){var top=document.getElementById('task-thread-messages').scrollTop;if(window.__resumedThreadLastTop===top)window.__resumedThreadStable=(window.__resumedThreadStable||0)+1;else{window.__resumedThreadLastTop=top;window.__resumedThreadStable=0;}return String(window.__resumedThreadStable>=3);})()`, "true")
		if got := browser.evaluate(`(function(){window.__taskThreadReaderTop=document.getElementById('task-thread-messages').scrollTop;return 'saved';})()`); got != "saved" {
			t.Fatalf("save resumed older-reader position: %s", got)
		}
		phase.Store(2)
		browser.waitFor("older task-thread reader preserved after resumed terminal render", `(function(){var messages=document.getElementById('task-thread-messages'),pair=document.getElementById('chat-execution-`+runningTwo.ID+`'),out=pair&&pair.querySelector('[data-raw-content]'),err=pair&&pair.querySelector('[data-terminal-error="true"]');return String(!!(window.__terminalRenderSettled>window.__threadSettledBeforeFailure&&out&&err&&(out.compareDocumentPosition(err)&Node.DOCUMENT_POSITION_FOLLOWING)&&Math.abs(messages.scrollTop-window.__taskThreadReaderTop)<=2));})()`, "true")

		if got := browser.evaluate(`(function(){htmx.ajax('POST','/tasks/` + task.ID + `/thread',{target:'#task-thread-messages',swap:'beforeend',values:{message:'fresh live turn'}});return 'sent';})()`); got != "sent" {
			t.Fatalf("submit fresh task-thread follow-up: %s", got)
		}
		browser.waitFor("fresh task-thread HTMX response and stream", `Boolean(document.getElementById('chat-execution-thread-live-fresh'))+':'+Boolean(window.__terminalStreamFor('thread-live-fresh'))`, "true:true")
		if got := browser.evaluate(`(function(){var messages=document.getElementById('task-thread-messages');messages.scrollTop=messages.scrollHeight;window.__terminalStreamFor('thread-live-fresh').emit('message',` + longOutputJS + `);window.__freshSettledBeforeFailure=window.__terminalRenderSettled;window.__terminalStreamFor('thread-live-fresh').emit('error','fresh terminal failure');return String(!document.getElementById('chat-execution-thread-live-fresh').querySelector('[data-terminal-error="true"]'));})()`); got != "true" {
			t.Fatalf("fresh task-thread terminal alert must wait for pending render: %s", got)
		}
		browser.wheel("#task-thread-messages", -650)
		browser.waitFor("native upward task-thread reading intent during fresh terminal render", `(function(){var messages=document.getElementById('task-thread-messages'),tracker=window._taskThreadPageTracker;return String(!!(tracker&&tracker.userScrolledUp&&messages.scrollTop<messages.scrollHeight-messages.clientHeight-100));})()`, "true")
		browser.waitFor("stable fresh task-thread native scroll position", `(function(){var top=document.getElementById('task-thread-messages').scrollTop;if(window.__freshThreadLastTop===top)window.__freshThreadStable=(window.__freshThreadStable||0)+1;else{window.__freshThreadLastTop=top;window.__freshThreadStable=0;}return String(window.__freshThreadStable>=3);})()`, "true")
		if got := browser.evaluate(`(function(){window.__freshTaskThreadReaderTop=document.getElementById('task-thread-messages').scrollTop;return 'saved';})()`); got != "saved" {
			t.Fatalf("save fresh older-reader position: %s", got)
		}
		browser.waitFor("fresh task-thread terminal render settles", `(function(){var pair=document.getElementById('chat-execution-thread-live-fresh');return String(!!(window.__terminalRenderSettled>window.__freshSettledBeforeFailure&&pair&&pair.querySelector('[data-terminal-error="true"]')));})()`, "true")
		if got := browser.evaluate(`(function(){var messages=document.getElementById('task-thread-messages'),pair=document.getElementById('chat-execution-thread-live-fresh'),out=pair&&pair.querySelector('[data-raw-content]'),err=pair&&pair.querySelector('[data-terminal-error="true"]');return String(!!(out&&err&&(out.compareDocumentPosition(err)&Node.DOCUMENT_POSITION_FOLLOWING)&&Math.abs(messages.scrollTop-window.__freshTaskThreadReaderTop)<=2));})()`); got != "true" {
			t.Fatalf("older task-thread reader was not preserved after fresh terminal render: %s", got)
		}
	})

	if lazyRequests.Load() < 1 {
		t.Fatalf("real lazy task-thread requests = %d, want at least 1", lazyRequests.Load())
	}
	if fragmentRequests.Load() != 2 {
		t.Fatalf("real task-thread execution fragment requests = %d, want 2", fragmentRequests.Load())
	}
}

func TestChatLiveCreatedFailureProductionWiringInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-terminal-chat", Name: "Terminal Chat project"}
	execID := "chat-live-created-failure"
	longOutput := strings.Repeat("long partial live Chat output line\n", 160)
	terminal := models.Execution{ID: execID, Status: models.ExecFailed, PromptSent: "live Chat failure", Output: longOutput, ErrorMessage: "live Chat terminal <unsafe>"}
	var phase atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat":
			chatRequests.Add(1)
			var executions []models.Execution
			if phase.Load() == 1 {
				executions = []models.Execution{terminal}
			}
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, ChatContent(nil, executions, project.ID, nil, nil, false, false, 30))))
				return
			}
			document := renderTerminalBrowserComponent(t, Chat([]models.Project{project}, project.ID, nil, executions, nil, nil, false, false, 30))
			_, _ = w.Write([]byte(installTerminalBrowserPrelude(document)))
		case "/auth/me":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	longOutputJSON, err := json.Marshal(longOutput)
	if err != nil {
		t.Fatalf("marshal long Chat output: %v", err)
	}
	longOutputJS := string(longOutputJSON)
	runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "chat-live-created-terminal-wiring", func(browser *composerFocusCDP) {
		browser.waitFor("production Chat page", `Boolean(document.getElementById('chat-page-root'))+':'+Boolean(document.getElementById('chat-messages'))`, "true:true")
		installDelayedTerminalBrowserRenderer(browser, 350)
		if got := browser.evaluate(`(function(){window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_new_message',project_id:'` + project.ID + `',exec_id:'` + execID + `',message:'live Chat failure',source:'api'}}));return 'sent';})()`); got != "sent" {
			t.Fatalf("dispatch production Chat live event: %s", got)
		}
		browser.waitFor("page-level createStreamingBubble path", `Boolean(document.getElementById('chat-execution-`+execID+`'))+':'+Boolean(window.__terminalStreamFor('`+execID+`'))`, "true:true")
		if got := browser.evaluate(`(function(){window.__terminalStreamFor('` + execID + `').emit('message',` + longOutputJS + `);return 'streamed';})()`); got != "streamed" {
			t.Fatalf("stream page-level Chat partial output: %s", got)
		}
		browser.waitFor("long page-level Chat output render", `document.getElementById('streaming-message-`+execID+`').getAttribute('data-raw-content')===`+longOutputJS+`?'ready':'waiting'`, "ready")
		browser.waitFor("initial long page-level Chat render settled", `window.__terminalRenderSettled>=1?'ready':'waiting'`, "ready")
		if got := browser.evaluate(`(function(){var messages=document.getElementById('chat-messages');messages.scrollTop=messages.scrollHeight;return 'pinned';})()`); got != "pinned" {
			t.Fatalf("pin live Chat turn: %s", got)
		}
		phase.Store(1)
		if got := browser.evaluate(`(function(){window.__terminalSettledBeforeFailure=window.__terminalRenderSettled;window.__terminalSyncBeforeFailure=window.__terminalSyncCalls;window.__terminalStreamFor('` + execID + `').emit('error','live Chat terminal <unsafe>');return 'failed';})()`); got != "failed" {
			t.Fatalf("fail page-level Chat stream: %s", got)
		}
		if got := browser.evaluate(`(function(){var pair=document.getElementById('chat-execution-` + execID + `');return String(window.__terminalRenderSettled===window.__terminalSettledBeforeFailure)+':'+String(!!(pair&&pair.querySelector('[data-terminal-error="true"]')))+':'+String(window.__terminalSyncCalls===window.__terminalSyncBeforeFailure);})()`); got != "true:false:true" {
			t.Fatalf("terminal alert and sync must wait for pending final production render, got %s", got)
		}
		browser.wheel("#chat-messages", -650)
		browser.waitFor("native upward Chat reading intent during terminal render", `(function(){var messages=document.getElementById('chat-messages'),tracker=window._chatPageTracker;return String(!!(tracker&&tracker.userScrolledUp&&messages.scrollTop<messages.scrollHeight-messages.clientHeight-100));})()`, "true")
		if got := browser.evaluate(`(function(){window.__terminalReaderTop=document.getElementById('chat-messages').scrollTop;return 'saved';})()`); got != "saved" {
			t.Fatalf("save live Chat older-reader position: %s", got)
		}
		browser.waitFor("page-level Chat terminal alert behavior after delayed production render", `(function(){var messages=document.getElementById('chat-messages'),pair=document.getElementById('chat-execution-`+execID+`'),out=pair&&pair.querySelector('[data-raw-content]'),err=pair&&pair.querySelector('[data-terminal-error="true"]');return String(!!(window.__terminalRenderSettled>=window.__terminalSettledBeforeFailure+1&&window.__terminalSyncCalls>window.__terminalSyncBeforeFailure&&out&&err&&(out.compareDocumentPosition(err)&Node.DOCUMENT_POSITION_FOLLOWING)&&err.getAttribute('role')==='alert'&&err.textContent==='Error: live Chat terminal <unsafe>'&&err.innerHTML.indexOf('<unsafe>')===-1&&pair.querySelectorAll('[data-terminal-error="true"]').length===1&&Math.abs(messages.scrollTop-window.__terminalReaderTop)<=2));})()`, "true")
	})
	if chatRequests.Load() < 2 {
		t.Fatalf("production Chat requests = %d, want initial page plus authoritative terminal sync", chatRequests.Load())
	}
}
