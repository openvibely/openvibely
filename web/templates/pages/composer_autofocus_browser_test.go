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
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

type composerFocusCDP struct {
	t      *testing.T
	ctx    context.Context
	conn   *websocket.Conn
	nextID int
	events []string
	loads  int
}

func (c *composerFocusCDP) captureEvent(message []byte) {
	var event struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(message, &event) != nil || event.Method == "" {
		return
	}
	switch event.Method {
	case "Page.loadEventFired":
		c.loads++
	case "Runtime.exceptionThrown", "Runtime.consoleAPICalled", "Log.entryAdded":
		entry := event.Method + ": " + string(event.Params)
		if len(entry) > 1500 {
			entry = entry[:1500] + "..."
		}
		c.events = append(c.events, entry)
		if len(c.events) > 20 {
			c.events = c.events[len(c.events)-20:]
		}
	}
}

func (c *composerFocusCDP) eventDiagnostic() string {
	if len(c.events) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(c.events)
	if err != nil {
		return "[unavailable]"
	}
	return string(encoded)
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
		c.captureEvent(message)
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
	return c.evaluateExpression(expression, false)
}

func (c *composerFocusCDP) evaluateAwait(expression string) string {
	c.t.Helper()
	return c.evaluateExpression(expression, true)
}

func (c *composerFocusCDP) evaluateExpression(expression string, awaitPromise bool) string {
	c.t.Helper()
	var response struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": awaitPromise}, &response)
	if len(response.ExceptionDetails) > 0 {
		c.t.Fatalf("evaluate JavaScript %q: %s; browserEvents=%s", expression, response.ExceptionDetails, c.eventDiagnostic())
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
	c.waitForTimeout(label, expression, want, 15*time.Second)
}

func (c *composerFocusCDP) waitForTimeout(label, expression, want string, timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	var got string
	for time.Now().Before(deadline) {
		got = c.evaluate(expression)
		if got == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	diagnostic := c.evaluate(`JSON.stringify({ready:document.readyState,active:document.activeElement&&{name:document.activeElement.id||document.activeElement.tagName,html:document.activeElement.outerHTML.slice(0,180),dialog:document.activeElement.closest('dialog')&&document.activeElement.closest('dialog').id},input:!!document.getElementById('message-input'),taskInput:!!document.getElementById('task-message-input'),thread:document.getElementById('thread-content')&&{loaded:document.getElementById('thread-content').dataset.loaded,loading:document.getElementById('thread-content').dataset.loading,text:document.getElementById('thread-content').textContent.slice(0,100)},manager:typeof window.openVibelyRequestComposerFocus,state:window._openVibelyComposerFocusState,historyRequests:window._historyRestoreFocusRequests,terminal:{started:window.__terminalRenderStarted,settled:window.__terminalRenderSettled,queued:window.__terminalRenderQueue&&window.__terminalRenderQueue.length,syncCalls:window.__terminalSyncCalls},overlays:Array.from(document.querySelectorAll('dialog[open], [role="dialog"][aria-modal="true"]:not(dialog), [data-chat-actions-open="true"], [aria-haspopup][aria-expanded="true"]')).filter(function(el){var style=getComputedStyle(el);return style.display!=='none'&&style.visibility!=='hidden'}).map(function(el){return el.id||el.outerHTML.slice(0,120)})})`)
	c.t.Fatalf("timed out waiting for %s: got %q, want %q; state=%s; browserEvents=%s", label, got, want, diagnostic, c.eventDiagnostic())
}

func (c *composerFocusCDP) click(selector string) {
	c.t.Helper()
	// Opening dialogs scale their content in; measuring mid-transition makes the press miss the moving target.
	c.evaluateAwait(fmt.Sprintf(`(function(){var el=document.querySelector(%q),running=[];for(var node=el;node;node=node.parentElement){running=running.concat(node.getAnimations().filter(function(a){return a.effect&&a.effect.getComputedTiming().endTime!==Infinity;}));}return Promise.all(running.map(function(a){return a.finished.catch(function(){});})).then(function(){return 'settled';});})()`, selector))
	coordinates := c.evaluate(fmt.Sprintf(`(function(){var el=document.querySelector(%q);if(!el)return 'missing';el.scrollIntoView({block:'center',inline:'center'});var r=el.getBoundingClientRect(),fractions=[.5,.25,.75],x,y,hit,owns=false;for(var yi=0;yi<fractions.length&&!owns;yi++){for(var xi=0;xi<fractions.length&&!owns;xi++){x=r.left+r.width*fractions[xi];y=r.top+r.height*fractions[yi];hit=document.elementFromPoint(x,y);owns=!!(hit&&(hit===el||el.contains(hit)));}}return JSON.stringify({x:x,y:y,hit:hit&&(hit.id||hit.tagName),hitHTML:hit&&hit.outerHTML.slice(0,180),targetRect:[r.left,r.top,r.right,r.bottom],hitRect:hit&&function(){var h=hit.getBoundingClientRect();return [h.left,h.top,h.right,h.bottom]}(),owns:owns,visibility:getComputedStyle(el).visibility,active:document.activeElement&&(document.activeElement.id||document.activeElement.tagName)});})()`, selector))
	if coordinates == "missing" {
		c.t.Fatalf("native click target %s is missing", selector)
	}
	var point struct {
		X          float64   `json:"x"`
		Y          float64   `json:"y"`
		Hit        string    `json:"hit"`
		HitHTML    string    `json:"hitHTML"`
		TargetRect []float64 `json:"targetRect"`
		HitRect    []float64 `json:"hitRect"`
		Owns       bool      `json:"owns"`
		Visibility string    `json:"visibility"`
		Active     string    `json:"active"`
	}
	if err := json.Unmarshal([]byte(coordinates), &point); err != nil {
		c.t.Fatalf("decode click coordinates for %s: %v", selector, err)
	}
	if !point.Owns {
		c.t.Fatalf("native click target %s was covered by %s (target visibility %s, focus on %s, target rect %v, hit rect %v): %s", selector, point.Hit, point.Visibility, point.Active, point.TargetRect, point.HitRect, point.HitHTML)
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

// Page.reload returns once navigation starts, before the old document is replaced.
func (c *composerFocusCDP) reload() {
	c.t.Helper()
	loads := c.loads
	c.call("Page.reload", map[string]any{}, nil)
	for c.loads == loads {
		_, message, err := c.conn.Read(c.ctx)
		if err != nil {
			c.t.Fatalf("wait for reload load event: %v", err)
		}
		c.captureEvent(message)
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
		"--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900",
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--user-data-dir="+filepath.Join(t.TempDir(), profileName+"-profile"), "about:blank",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopBrowserProcess(cmd)

	type debugTarget struct {
		Type                 string `json:"type"`
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
					if candidate.Type == "page" && candidate.WebSocketDebuggerURL != "" {
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
		t.Fatalf("find Chrome debugging page target\n%s", stderr)
	}

	// Some fixtures perform several independent 15-second condition waits. Keep
	// the connection lifetime from expiring underneath a later wait.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("connect to Chrome debugging target: %v", err)
	}
	defer conn.CloseNow()
	browser := &composerFocusCDP{t: t, ctx: ctx, conn: conn}
	browser.call("Runtime.enable", map[string]any{}, nil)
	browser.call("Log.enable", map[string]any{}, nil)
	browser.call("Page.enable", map[string]any{}, nil)
	// A busy Chrome lists a command-line URL before the tab starts loading it, so load the fixture through this session.
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	browser.call("Page.navigate", map[string]any{"url": targetURL}, &navigation)
	if navigation.ErrorText != "" {
		t.Fatalf("navigate Chrome to %s: %s", targetURL, navigation.ErrorText)
	}
	run(browser)
}

func TestBrowserFunctional_ComposerAutoFocusProductionNavigationInChrome(t *testing.T) {
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
		return strings.Replace(html, static.URL("vendor/htmx.min.js"), `/htmx-2.0.4.min.js`, 1)
	}
	// Inject fixture links by their page root, independent of deduplicated styles.
	addChatControl := func(html string) string {
		return strings.Replace(html, `<div id="chat-page-root"`, `<a id="to-task" href="/tasks/task-focus" hx-get="/tasks/task-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open task</a><div id="chat-page-root"`, 1)
	}
	addTaskControls := func(html string) string {
		return strings.Replace(html, `<div id="task-detail-content"`, `<a id="to-chat" href="/chat?project_id=project-focus" hx-get="/chat?project_id=project-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open Chat</a><a id="to-delayed-chat" href="/chat?project_id=project-focus&amp;delay=1" hx-get="/chat?project_id=project-focus&amp;delay=1" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open delayed Chat</a><div id="task-detail-content"`, 1)
	}
	chatFragment := func() string {
		return addChatControl(render(ChatContent(nil, nil, project.ID, nil, nil, false, false, 30)))
	}
	taskFragment := func(defaultTab string) string {
		return addTaskControls(render(TaskDetailContent(task, nil, nil, nil, nil, nil, nil, defaultTab, nil)))
	}
	threadFragment := func() string {
		return render(components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30))
	}
	controls := `<style>.hidden{display:none!important}#focus-guard,#open-focus-dialog{position:fixed;top:8px;z-index:1000}#focus-guard{right:8px}#open-focus-dialog{right:130px}</style><button id="focus-guard" type="button">Focus guard</button><button id="open-focus-dialog" type="button" onclick="document.getElementById('focus-dialog').showModal()">Open dialog</button><dialog id="focus-dialog"><a id="dialog-chat-link" href="/chat?project_id=project-focus" hx-get="/chat?project_id=project-focus" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Open Chat in dialog</a><button type="button" onclick="this.closest('dialog').close()">Close</button></dialog>`

	var chatHTMXRequests atomic.Int32
	var taskHTMXRequests atomic.Int32
	var threadHTMXRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/ui/preferences":
			w.WriteHeader(http.StatusNoContent)
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
			document = addChatControl(document)
			_, _ = w.Write([]byte(injectPageControl(document, controls)))
		case "/tasks/task-focus":
			defaultTab := "chat"
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
			document = addTaskControls(document)
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
		// The permanent thread loads without a page-tab click.
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
		// The shell now survives history restoration. Explicitly leave the
		// intentional shell focus owner before expecting composer autofocus;
		// history must not steal focus from a still-focused shell control.
		browser.evaluate(`document.activeElement.blur(); 'blurred'`)
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
