package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

type composerFocusCDP struct {
	t      *testing.T
	ctx    context.Context
	conn   *websocket.Conn
	nextID int
}

func (c *composerFocusCDP) call(method string, params any, result any) {
	c.t.Helper()
	c.nextID++
	payload, err := json.Marshal(map[string]any{"id": c.nextID, "method": method, "params": params})
	if err != nil {
		c.t.Fatalf("marshal CDP %s request: %v", method, err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, payload); err != nil {
		c.t.Fatalf("write CDP %s request: %v", method, err)
	}
	for {
		_, message, err := c.conn.Read(c.ctx)
		if err != nil {
			c.t.Fatalf("read CDP %s response: %v", method, err)
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(message, &response) != nil || response.ID != c.nextID {
			continue
		}
		if len(response.Error) > 0 {
			c.t.Fatalf("CDP %s error: %s", method, response.Error)
		}
		if result != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, result); err != nil {
				c.t.Fatalf("decode CDP %s result: %v", method, err)
			}
		}
		return
	}
}

func (c *composerFocusCDP) evaluate(expression string) string {
	c.t.Helper()
	var response struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &response)
	if len(response.ExceptionDetails) > 0 {
		c.t.Fatalf("evaluate JavaScript %q: %s", expression, response.ExceptionDetails)
	}
	if response.Result.Type != "string" || len(response.Result.Value) == 0 {
		c.t.Fatalf("evaluate JavaScript %q returned %q: %s", expression, response.Result.Type, response.Result.Description)
	}
	var value string
	if err := json.Unmarshal(response.Result.Value, &value); err != nil {
		c.t.Fatalf("decode JavaScript result for %q: %v", expression, err)
	}
	return value
}

func (c *composerFocusCDP) waitFor(label, expression, want string) {
	c.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = c.evaluate(expression)
		if got == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	diagnostic := c.evaluate(`JSON.stringify({ready:document.readyState,active:document.activeElement&&{name:document.activeElement.id||document.activeElement.tagName,html:document.activeElement.outerHTML.slice(0,180),dialog:document.activeElement.closest('dialog')&&document.activeElement.closest('dialog').id},input:!!document.getElementById('message-input'),taskInput:!!document.getElementById('task-message-input'),thread:document.getElementById('thread-content')&&{loaded:document.getElementById('thread-content').dataset.loaded,loading:document.getElementById('thread-content').dataset.loading,text:document.getElementById('thread-content').textContent.slice(0,100)},manager:typeof window.openVibelyRequestComposerFocus,state:window._openVibelyComposerFocusState,historyRequests:window._historyRestoreFocusRequests,overlays:Array.from(document.querySelectorAll('dialog[open], [role="dialog"][aria-modal="true"], [data-chat-actions-open="true"], [aria-haspopup][aria-expanded="true"]')).map(function(el){return el.id||el.outerHTML.slice(0,120)})})`)
	c.t.Fatalf("timed out waiting for %s: got %q, want %q; state=%s", label, got, want, diagnostic)
}

func (c *composerFocusCDP) click(selector string) {
	c.t.Helper()
	coordinates := c.evaluate(fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return 'missing';el.scrollIntoView({block:'center',inline:'center'});var r=el.getBoundingClientRect();var x=r.left+r.width/2,y=r.top+r.height/2,hit=document.elementFromPoint(x,y);return JSON.stringify({x:x,y:y,hit:hit&&(hit.id||hit.tagName),owns:!!(hit&&(hit===el||el.contains(hit)))});})()`, selector))
	if coordinates == "missing" {
		c.t.Fatalf("native click target %s is missing", selector)
	}
	var point struct {
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		Hit  string  `json:"hit"`
		Owns bool    `json:"owns"`
	}
	if err := json.Unmarshal([]byte(coordinates), &point); err != nil {
		c.t.Fatalf("decode click coordinates for %s: %v", selector, err)
	}
	if !point.Owns {
		c.t.Fatalf("native click target %s was covered by %s", selector, point.Hit)
	}
	for _, params := range []map[string]any{
		{"type": "mouseMoved", "x": point.X, "y": point.Y},
		{"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 1},
	} {
		c.call("Input.dispatchMouseEvent", params, nil)
	}
}

func (c *composerFocusCDP) wheel(selector string, deltaY float64) {
	c.t.Helper()
	coordinates := c.evaluate(fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return 'missing';var r=el.getBoundingClientRect();return JSON.stringify({x:r.left+r.width/2,y:r.top+r.height/2});})()`, selector))
	if coordinates == "missing" {
		c.t.Fatalf("native wheel target %s is missing", selector)
	}
	var point struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.Unmarshal([]byte(coordinates), &point); err != nil {
		c.t.Fatalf("decode wheel coordinates for %s: %v", selector, err)
	}
	c.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y}, nil)
	c.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": point.X, "y": point.Y, "deltaX": 0, "deltaY": deltaY}, nil)
}

func (c *composerFocusCDP) typeText(text string) {
	c.t.Helper()
	for _, char := range text {
		key := string(char)
		c.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": key, "text": key, "unmodifiedText": key}, nil)
		c.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key}, nil)
	}
}

func (c *composerFocusCDP) navigateHistory(delta int) {
	c.t.Helper()
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	c.call("Page.getNavigationHistory", map[string]any{}, &history)
	target := history.CurrentIndex + delta
	if target < 0 || target >= len(history.Entries) {
		c.t.Fatalf("history delta %d from index %d is outside %d entries", delta, history.CurrentIndex, len(history.Entries))
	}
	c.call("Page.navigateToHistoryEntry", map[string]any{"entryId": history.Entries[target].ID}, nil)
}

func runComposerFocusCDP(t *testing.T, chrome, targetURL, profileName string, run func(*composerFocusCDP)) {
	t.Helper()
	debugListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Chrome debugging port: %v", err)
	}
	debugPort := debugListener.Addr().(*net.TCPAddr).Port
	_ = debugListener.Close()

	stderrPath := filepath.Join(t.TempDir(), profileName+".stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900",
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--user-data-dir="+filepath.Join(t.TempDir(), profileName+"-profile"), targetURL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopBrowserProcess(cmd)

	type debugTarget struct {
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	var target debugTarget
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && target.WebSocketDebuggerURL == ""; {
		resp, requestErr := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
		if requestErr == nil {
			var targets []debugTarget
			decodeErr := json.NewDecoder(resp.Body).Decode(&targets)
			_ = resp.Body.Close()
			if decodeErr == nil {
				for _, candidate := range targets {
					if strings.HasPrefix(candidate.URL, targetURL) && candidate.WebSocketDebuggerURL != "" {
						target = candidate
						break
					}
				}
			}
		}
		if target.WebSocketDebuggerURL == "" {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if target.WebSocketDebuggerURL == "" {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("find Chrome debugging target for %s\n%s", targetURL, stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("connect to Chrome debugging target: %v", err)
	}
	defer conn.CloseNow()
	run(&composerFocusCDP{t: t, ctx: ctx, conn: conn})
}

func TestComposerAutoFocusProductionNavigationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-focus", Name: "Focus project"}
	task := &models.Task{ID: "task-focus", ProjectID: project.ID, Title: "Focus task", Status: models.StatusCompleted, Category: models.CategoryCompleted}

	render := func(component templ.Component) string {
		t.Helper()
		var out bytes.Buffer
		if err := component.Render(context.Background(), &out); err != nil {
			t.Fatalf("render autofocus fixture: %v", err)
		}
		return out.String()
	}
	injectPageControl := func(html, control string) string {
		return strings.Replace(html, `<main id="main-content"`, control+`<main id="main-content"`, 1)
	}
	usePinnedHTMX := func(html string) string {
		return strings.Replace(html, `https://unpkg.com/htmx.org@2.0.4`, `/htmx-2.0.4.min.js`, 1)
	}
	chatFragment := func() string {
		html := render(ChatContent(nil, nil, project.ID, nil, nil, false, false, 30))
		return strings.Replace(html, `<div id="chat-page-root"`, `<a id="to-task" href="/tasks/task-focus" hx-get="/tasks/task-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open task</a><div id="chat-page-root"`, 1)
	}
	taskFragment := func(defaultTab string) string {
		html := render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, defaultTab, nil))
		return strings.Replace(html, `<div id="task-detail-content"`, `<a id="to-chat" href="/chat?project_id=project-focus" hx-get="/chat?project_id=project-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open Chat</a><a id="to-delayed-chat" href="/chat?project_id=project-focus&amp;delay=1" hx-get="/chat?project_id=project-focus&amp;delay=1" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open delayed Chat</a><div id="task-detail-content"`, 1)
	}
	threadFragment := func() string {
		return render(components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30))
	}
	controls := `<style>.hidden{display:none!important}#focus-guard,#open-focus-dialog{position:fixed;top:8px;z-index:1000}#focus-guard{right:8px}#open-focus-dialog{right:130px}</style><button id="focus-guard" type="button">Focus guard</button><button id="open-focus-dialog" type="button" onclick="document.getElementById('focus-dialog').showModal()">Open dialog</button><dialog id="focus-dialog"><a id="dialog-chat-link" href="/chat?project_id=project-focus" hx-get="/chat?project_id=project-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open Chat in dialog</a><button type="button" onclick="this.closest('dialog').close()">Close</button></dialog>`

	var chatHTMXRequests atomic.Int32
	var taskHTMXRequests atomic.Int32
	var threadHTMXRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat":
			isHTMX := r.Header.Get("HX-Request") == "true"
			if isHTMX {
				chatHTMXRequests.Add(1)
			}
			if r.URL.Query().Get("delay") == "1" {
				time.Sleep(750 * time.Millisecond)
			}
			fragment := chatFragment()
			if isHTMX {
				_, _ = w.Write([]byte(fragment))
				return
			}
			document := usePinnedHTMX(render(Chat([]models.Project{project}, project.ID, nil, nil, nil, nil, false, false, 30)))
			document = strings.Replace(document, render(ChatContent(nil, nil, project.ID, nil, nil, false, false, 30)), fragment, 1)
			_, _ = w.Write([]byte(injectPageControl(document, controls)))
		case "/tasks/task-focus":
			defaultTab := "details"
			if r.URL.Query().Get("tab") == "chat" {
				defaultTab = "chat"
			}
			fragment := taskFragment(defaultTab)
			if r.Header.Get("HX-Request") == "true" {
				taskHTMXRequests.Add(1)
				_, _ = w.Write([]byte(fragment))
				return
			}
			document := usePinnedHTMX(render(TaskDetailPage([]models.Project{project}, task, nil, nil, nil, nil, nil, nil, defaultTab, nil)))
			document = strings.Replace(document, render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, defaultTab, nil)), fragment, 1)
			_, _ = w.Write([]byte(injectPageControl(document, controls)))
		case "/tasks/task-focus/thread":
			if r.Header.Get("HX-Request") == "true" {
				threadHTMXRequests.Add(1)
			}
			_, _ = w.Write([]byte(threadFragment()))
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authenticated":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id=project-focus", "composer-focus-navigation", func(browser *composerFocusCDP) {
		browser.waitFor("direct Chat focus", `document.readyState+':'+(document.activeElement&&document.activeElement.id)`, "complete:message-input")
		browser.typeText("direct chat")
		browser.waitFor("native Chat typing", `document.getElementById('message-input').value`, "direct chat")

		browser.click("#to-task")
		browser.waitFor("real HTMX Task Detail navigation", `location.pathname+':'+Boolean(document.getElementById('task-detail-content'))`, "/tasks/task-focus:true")
		browser.waitFor("Task Detail HTMX settle", `(function(){var el=document.getElementById('main-content');return el.classList.contains('htmx-swapping')+':'+el.classList.contains('htmx-settling')})()`, "false:false")
		browser.click(`[data-tab="chat"]`)
		browser.waitFor("lazy Thread focus", `document.activeElement&&document.activeElement.id`, "task-message-input")
		browser.typeText("lazy thread")
		browser.waitFor("native Thread typing", `document.getElementById('task-message-input').value`, "lazy thread")

		browser.click(`[data-nav-base="/chat"]`)
		browser.waitFor("sidebar HTMX return to Chat focus", `location.pathname+':'+(document.activeElement&&document.activeElement.id)`, "/chat:message-input")
		browser.typeText("return chat")
		browser.waitFor("native returned Chat typing with restored draft", `document.getElementById('message-input').value`, "direct chatreturn chat")

		if got := browser.evaluate(`(function(){var requestFocus=window.openVibelyRequestComposerFocus;window._historyRestoreFocusRequests=0;window.openVibelyRequestComposerFocus=function(options){if(options&&options.reason==='history-restore')window._historyRestoreFocusRequests++;return requestFocus.apply(this,arguments);};document.addEventListener('htmx:historyRestore',function(){document.getElementById('focus-guard').focus();},{once:true});return 'ready';})()`); got != "ready" {
			t.Fatalf("install history restoration focus guard: %s", got)
		}
		browser.navigateHistory(-1)
		browser.waitFor("real Back history restoration", `location.pathname+':'+Boolean(document.getElementById('task-detail-content'))`, "/tasks/task-focus:true")
		browser.waitFor("cancelled history focus callback", `(function(){var state=window._openVibelyComposerFocusState;return (document.activeElement&&document.activeElement.id)+':'+window._historyRestoreFocusRequests+':'+Boolean(state&&state.historyTimer)+':'+Boolean(state&&state.timer)})()`, "focus-guard:0:false:false")
		browser.navigateHistory(1)
		browser.waitFor("real Forward history composer focus", `location.pathname+':'+(document.activeElement&&document.activeElement.id)+':'+window._historyRestoreFocusRequests`, "/chat:message-input:1")
		expectedHistoryDraft := browser.evaluate(`(function(){var input=document.getElementById('message-input');return input.value.slice(0,input.selectionStart)+' history'+input.value.slice(input.selectionEnd);})()`)
		browser.typeText(" history")
		browser.waitFor("native typing after Forward history", `document.getElementById('message-input').value`, expectedHistoryDraft)

		browser.click("#to-task")
		browser.waitFor("second Task Detail navigation", `location.pathname+':'+Boolean(document.getElementById('task-detail-content'))`, "/tasks/task-focus:true")
		browser.waitFor("second Task Detail HTMX settle", `(function(){var el=document.getElementById('main-content');return el.classList.contains('htmx-swapping')+':'+el.classList.contains('htmx-settling')})()`, "false:false")
		chatRequestsBeforeDelay := chatHTMXRequests.Load()
		browser.click("#to-delayed-chat")
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && chatHTMXRequests.Load() == chatRequestsBeforeDelay; {
			time.Sleep(10 * time.Millisecond)
		}
		if chatHTMXRequests.Load() == chatRequestsBeforeDelay {
			t.Fatalf("delayed Chat navigation did not issue a real HTMX request; browser=%s", browser.evaluate(`location.href+':'+(document.activeElement&&document.activeElement.id)+':'+document.getElementById('to-delayed-chat').getAttribute('hx-get')`))
		}
		browser.click("#focus-guard")
		browser.waitFor("delayed HTMX Chat response", `location.pathname+':'+Boolean(document.getElementById('message-input'))`, "/chat:true")
		browser.waitFor("delayed Chat focus lifecycle settle", `(function(){var el=document.getElementById('main-content'),state=window._openVibelyComposerFocusState;return el.classList.contains('htmx-swapping')+':'+el.classList.contains('htmx-settling')+':'+Boolean(state&&state.timer)})()`, "false:false:false")
		browser.waitFor("meaningful-control focus protection", `document.activeElement&&document.activeElement.id`, "focus-guard")

		browser.click("#to-task")
		browser.waitFor("third Task Detail navigation", `location.pathname+':'+Boolean(document.getElementById('task-detail-content'))`, "/tasks/task-focus:true")
		browser.waitFor("third Task Detail HTMX settle", `(function(){var el=document.getElementById('main-content');return el.classList.contains('htmx-swapping')+':'+el.classList.contains('htmx-settling')})()`, "false:false")
		browser.click("#open-focus-dialog")
		browser.waitFor("native dialog open", `document.getElementById('focus-dialog').open+':'+(document.activeElement&&document.activeElement.id)`, "true:dialog-chat-link")
		browser.click("#dialog-chat-link")
		browser.waitFor("dialog HTMX Chat response", `location.pathname+':'+Boolean(document.getElementById('message-input'))`, "/chat:true")
		browser.waitFor("dialog Chat focus lifecycle settle", `(function(){var el=document.getElementById('main-content'),state=window._openVibelyComposerFocusState;return el.classList.contains('htmx-swapping')+':'+el.classList.contains('htmx-settling')+':'+Boolean(state&&state.timer)})()`, "false:false:false")
		browser.waitFor("dialog focus protection", `document.getElementById('focus-dialog').open+':'+(document.activeElement&&document.activeElement.id)`, "true:dialog-chat-link")
	})

	runComposerFocusCDP(t, chrome, server.URL+"/tasks/task-focus?tab=chat", "composer-focus-direct-thread", func(browser *composerFocusCDP) {
		browser.waitFor("direct full Task Thread lazy focus", `document.readyState+':'+(document.activeElement&&document.activeElement.id)`, "complete:task-message-input")
		browser.typeText("direct thread")
		browser.waitFor("native direct Thread typing", `document.getElementById('task-message-input').value`, "direct thread")
	})

	if chatHTMXRequests.Load() < 3 {
		t.Fatalf("real Chat HTMX requests = %d, want at least 3", chatHTMXRequests.Load())
	}
	if taskHTMXRequests.Load() < 2 {
		t.Fatalf("real Task Detail HTMX requests = %d, want at least 2", taskHTMXRequests.Load())
	}
	if threadHTMXRequests.Load() < 2 {
		t.Fatalf("real lazy Thread HTMX requests = %d, want at least 2", threadHTMXRequests.Load())
	}
}
