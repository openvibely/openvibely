package components

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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/models"
)

func TestBreadcrumbSelectorCaretOnlyUsesRealTriggerDimensions(t *testing.T) {
	var out bytes.Buffer
	if err := BreadcrumbSelector(models.BreadcrumbSelector{
		ID: "automation-resource-selector", Kind: "Automation", CurrentID: "automation-1",
		CurrentName: "Editable Automation", SearchURL: "/breadcrumb-selectors/automations", CaretOnly: true,
	}).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	buttonMatch := regexp.MustCompile(`<button[^>]*class="([^"]*)"[^>]*data-breadcrumb-selector-caret-only`).FindStringSubmatch(body)
	if len(buttonMatch) != 2 {
		t.Fatalf("caret-only selector button not found: %s", body)
	}
	classes := buttonMatch[1]
	if !strings.Contains(classes, `h-8 w-7 justify-center px-0`) {
		t.Fatalf("caret-only selector is missing its explicit trigger dimensions: %q", classes)
	}
	if strings.Contains(classes, `max-w-full gap-1 px-1 text-2xl`) {
		t.Fatal("caret-only selector rendered the normal constrained trigger classes")
	}
}

func TestBreadcrumbSelectorRendersAccessibleBoundedDialog(t *testing.T) {
	var out bytes.Buffer
	err := BreadcrumbSelector(models.BreadcrumbSelector{
		ID:          "task-resource-selector",
		Kind:        "Task",
		CurrentID:   "task-1",
		CurrentName: "A very long current task title",
		SearchURL:   "/breadcrumb-selectors/tasks?project_id=project-1&current_id=task-1",
		ContextName: "tab", ContextValue: "changes",
		OriginName: "from", OriginValue: "schedule"}).Render(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		`data-breadcrumb-selector`, `aria-expanded="false"`, `aria-haspopup="dialog"`,
		`role="dialog"`, `aria-modal="true"`, `Switch Task`, `Search Tasks`,
		`hx-trigger="input changed delay:200ms, search"`, `hx-sync="this:replace"`,
		`hx-include="closest [data-breadcrumb-selector]"`, `name="tab" value="changes"`,
		`name="from" value="schedule" data-breadcrumb-selector-origin`, `max-w-[calc(100vw-1rem)]`, `overflow-hidden`, `data-breadcrumb-selector-status`,
		`class="w-full min-w-0 border-0 bg-transparent px-4 py-2 pr-10 text-sm focus:outline-none focus:ring-0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in selector markup", want)
		}
	}
	for _, forbidden := range []string{`class="modal p-4"`, `class="input input-bordered`, `min-h-8 items-center border-t`, `1 result.`, `2 results.`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("selector markup should not contain %q", forbidden)
		}
	}
}

func TestBreadcrumbSelectorKeyboardFocusAndContainmentInChrome(t *testing.T) {
	chrome := testChromePath(t)
	var selector bytes.Buffer
	if err := BreadcrumbSelector(models.BreadcrumbSelector{ID: "browser-selector", Kind: "Task", CurrentID: "one", CurrentName: "Current", SearchURL: "/results"}).Render(context.Background(), &selector); err != nil {
		t.Fatal(err)
	}
	var results bytes.Buffer
	if err := BreadcrumbSelectorResults("Task", "one", []models.BreadcrumbSelectorItem{{ID: "one", Name: "Current", URL: "/tasks/one"}, {ID: "two", Name: "Other", URL: "/tasks/two"}}, false).Render(context.Background(), &results); err != nil {
		t.Fatal(err)
	}
	runner := `<script>
	window.openVibelyNavigate = function(url) { window.__selectedURL = url; return Promise.resolve(); };
	window.addEventListener('DOMContentLoaded', function() {
	  (async function() {
	    function waitFor(check) { return new Promise(function(resolve, reject) { var started=Date.now(); (function poll(){ if(check()) return resolve(); if(Date.now()-started>5000) return reject(new Error('timeout')); setTimeout(poll,20); })(); }); }
	    var button=document.querySelector('[data-breadcrumb-selector-button]'); button.focus(); button.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
	    var dialog=document.querySelector('[data-breadcrumb-selector-dialog]'), input=document.querySelector('[data-breadcrumb-selector-search]');
	    if(!dialog.open || button.getAttribute('aria-expanded')!=='true' || document.activeElement!==input) throw new Error('keyboard open or search focus failed');
	    await waitFor(function(){ return document.querySelectorAll('[data-breadcrumb-selector-option]').length===2; });
	    input.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
	    if(!document.activeElement.matches('[data-breadcrumb-selector-option]')) throw new Error('ArrowDown did not focus an option');
	    document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
	    if(dialog.open || document.activeElement!==button || button.getAttribute('aria-expanded')!=='false') throw new Error('Escape did not close and restore focus');
	    button.click(); await waitFor(function(){ return dialog.open; });
		    var box=dialog.getBoundingClientRect(), triggerBox=button.getBoundingClientRect(), caretBox=button.querySelector('[data-breadcrumb-selector-caret]').getBoundingClientRect();
		    if(box.left < 7 || box.right > innerWidth-7 || box.bottom > innerHeight-7) throw new Error('selector escaped viewport: '+JSON.stringify(box));
		    if(Math.abs(box.top-triggerBox.bottom-4) > 2) throw new Error('selector is not anchored below trigger: '+JSON.stringify({box:box,trigger:triggerBox}));
		    if(Math.abs(box.left-caretBox.left) > 2) throw new Error('selector left edge is not anchored below caret: '+JSON.stringify({box:box,caret:caretBox}));
		    document.documentElement.setAttribute('data-theme','light');
		    var themeBox=dialog, lightStyle=getComputedStyle(themeBox);
		    var light=[lightStyle.backgroundColor,lightStyle.borderColor,lightStyle.color].join('|');
		    document.documentElement.setAttribute('data-theme','dark');
		    var darkStyle=getComputedStyle(themeBox), dark=[darkStyle.backgroundColor,darkStyle.borderColor,darkStyle.color].join('|');
		    if(light===dark) throw new Error('semantic colors did not respond to dark theme');
		    document.documentElement.setAttribute('data-theme','imported-contrast');
		    var contrastStyle=getComputedStyle(themeBox);
		    if(contrastStyle.backgroundColor!=='rgb(0, 0, 0)' || contrastStyle.color!=='rgb(255, 255, 255)' || contrastStyle.borderColor!=='rgb(255, 255, 255)') throw new Error('imported high-contrast variables were not honored: '+[contrastStyle.backgroundColor,contrastStyle.color,contrastStyle.borderColor].join('|'));
		    if(!dialog.matches('.bg-base-100') || !dialog.matches('.border-base-300')) throw new Error('theme semantic classes missing');	    document.body.setAttribute('data-test-result','pass');
	  })().catch(function(error){ var message=String(error.stack||error); document.body.setAttribute('data-test-result','fail'); document.body.setAttribute('data-test-error',message); document.body.appendChild(document.createTextNode(' BREADCRUMB_TEST_ERROR: '+message)); });
	});
	</script>`
	page := `<!doctype html><html data-theme="light"><head><meta name="viewport" content="width=device-width"><style>dialog{border:0;background:transparent;box-sizing:border-box}.fixed{position:fixed}.m-0{margin:0}.w-\[28rem\]{width:28rem}.max-w-\[calc\(100vw-1rem\)\]{max-width:calc(100vw - 1rem)}.max-h-\[min\(32rem\,calc\(100dvh-1rem\)\)\]{max-height:min(32rem,calc(100dvh - 1rem))}.bg-base-100{background-color:var(--fixture-base)}.border-base-300{border:1px solid var(--fixture-border)}.text-base-content{color:var(--fixture-content)}[data-theme="light"]{--fixture-base:rgb(255,255,255);--fixture-border:rgb(210,210,210);--fixture-content:rgb(20,20,20)}[data-theme="dark"]{--fixture-base:rgb(24,24,27);--fixture-border:rgb(82,82,91);--fixture-content:rgb(244,244,245)}[data-theme="imported-contrast"]{--fixture-base:rgb(0,0,0);--fixture-border:rgb(255,255,255);--fixture-content:rgb(255,255,255)}</style><script src="/htmx.js"></script></head><body data-test-result="pending">` + selector.String() + runner + `</body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = fmt.Fprint(w, page)
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/results":
			_, _ = fmt.Fprint(w, results.String())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL+"/", "breadcrumb selector keyboard", 900, 20*time.Second)
}

func TestBreadcrumbSelectorLongTitleClampsInsideNarrowViewportInChrome(t *testing.T) {
	chrome := testChromePath(t)
	var selector bytes.Buffer
	if err := BreadcrumbSelector(models.BreadcrumbSelector{
		ID:          "narrow-selector",
		Kind:        "Task",
		CurrentID:   "one",
		CurrentName: "A very long current task title that must truncate beside the breadcrumb caret",
		SearchURL:   "/results",
	}).Render(context.Background(), &selector); err != nil {
		t.Fatal(err)
	}

	runner := `<script>
	window.addEventListener('DOMContentLoaded', function() {
	  try {
	    var button=document.querySelector('[data-breadcrumb-selector-button]');
	    var caret=button.querySelector('[data-breadcrumb-selector-caret]');
	    button.click();
	    var dialog=document.querySelector('[data-breadcrumb-selector-dialog]');
	    var box=dialog.getBoundingClientRect(), triggerBox=button.getBoundingClientRect(), caretBox=caret.getBoundingClientRect();
	    if(innerWidth > 390) throw new Error('fixture is not using a narrow viewport: '+innerWidth);
	    if(!dialog.open) throw new Error('selector did not open');
	    if(box.left < 7 || box.right > innerWidth-7 || box.bottom > innerHeight-7) throw new Error('clamped selector escaped narrow viewport: '+JSON.stringify({box:box,viewport:[innerWidth,innerHeight]}));
	    if(Math.abs(box.top-triggerBox.bottom-4) > 2) throw new Error('clamped selector is not below trigger: '+JSON.stringify({box:box,trigger:triggerBox}));
	    if(Math.abs(box.left-caretBox.left) < 8) throw new Error('long-title selector was not clamped away from overflowing caret anchor: '+JSON.stringify({box:box,caret:caretBox}));
	    if(button.scrollWidth <= button.clientWidth) throw new Error('long breadcrumb title did not exercise truncation');
	    document.body.setAttribute('data-test-result','pass');
	  } catch(error) {
	    var message=String(error.stack||error);
	    document.body.setAttribute('data-test-result','fail');
	    document.body.setAttribute('data-test-error',message);
	    document.body.appendChild(document.createTextNode(' BREADCRUMB_NARROW_TEST_ERROR: '+message));
	  }
	});
	</script>`
	page := `<!doctype html><html><head><meta name="viewport" content="width=device-width"><style>html,body{margin:0;width:100%;overflow:hidden}body{font-family:sans-serif}.fixture{box-sizing:border-box;display:flex;justify-content:flex-end;padding:16px 8px 0 110px;width:100%}.fixture>[data-breadcrumb-selector]{min-width:0}.fixture button{max-width:100%;overflow:hidden}.fixture button span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}dialog{border:1px solid #ccc;background:#fff;box-sizing:border-box}.fixed{position:fixed}.m-0{margin:0}.w-\[28rem\]{width:28rem}.max-w-\[calc\(100vw-1rem\)\]{max-width:calc(100vw - 1rem)}.max-h-\[min\(32rem\,calc\(100dvh-1rem\)\)\]{max-height:min(32rem,calc(100dvh - 1rem))}</style></head><body data-test-result="pending"><div class="fixture">` + selector.String() + `</div>` + runner + `</body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, page)
	}))
	defer server.Close()
	runBreadcrumbSelectorMobileFixture(t, chrome, server.URL+"/", 375, 667)
}

func runBreadcrumbSelectorMobileFixture(t *testing.T, chrome, targetURL string, width, height int) {
	t.Helper()
	debugListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Chrome debugging port: %v", err)
	}
	debugPort := debugListener.Addr().(*net.TCPAddr).Port
	_ = debugListener.Close()

	stderrPath := filepath.Join(t.TempDir(), "breadcrumb-mobile.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--no-first-run", "--no-default-browser-check",
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--user-data-dir="+filepath.Join(t.TempDir(), "breadcrumb-mobile-profile"), "about:blank",
	)
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome mobile breadcrumb fixture: %v", err)
	}
	defer stopTestBrowserProcess(cmd)

	type debugTarget struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	var target debugTarget
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, requestErr := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
		if requestErr == nil {
			var targets []debugTarget
			decodeErr := json.NewDecoder(resp.Body).Decode(&targets)
			_ = resp.Body.Close()
			if decodeErr == nil {
				for _, candidate := range targets {
					if candidate.Type == "page" && candidate.URL == "about:blank" && candidate.WebSocketDebuggerURL != "" {
						target = candidate
						break
					}
				}
			}
		}
		if target.WebSocketDebuggerURL != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if target.WebSocketDebuggerURL == "" {
		t.Fatal("find Chrome debugging target for mobile breadcrumb fixture")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("connect to Chrome debugging target: %v", err)
	}
	defer conn.CloseNow()

	nextID := 0
	call := func(method string, params any, result any) {
		t.Helper()
		nextID++
		request, marshalErr := json.Marshal(map[string]any{"id": nextID, "method": method, "params": params})
		if marshalErr != nil {
			t.Fatalf("marshal CDP %s request: %v", method, marshalErr)
		}
		if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
			t.Fatalf("write CDP %s request: %v", method, err)
		}
		for {
			_, message, readErr := conn.Read(ctx)
			if readErr != nil {
				t.Fatalf("read CDP %s response: %v", method, readErr)
			}
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(message, &response) != nil || response.ID != nextID {
				continue
			}
			if len(response.Error) > 0 {
				t.Fatalf("CDP %s error: %s", method, response.Error)
			}
			if result != nil && len(response.Result) > 0 {
				if err := json.Unmarshal(response.Result, result); err != nil {
					t.Fatalf("decode CDP %s result: %v", method, err)
				}
			}
			return
		}
	}

	call("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height, "deviceScaleFactor": 1, "mobile": true,
	}, nil)
	call("Page.navigate", map[string]any{"url": targetURL}, nil)

	result := "pending"
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		var response struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		call("Runtime.evaluate", map[string]any{
			"expression":    `document.body ? (document.body.getAttribute('data-test-result') || 'pending') + ':' + (document.body.getAttribute('data-test-error') || '') + ' | url=' + location.href + ' ready=' + document.readyState + ' width=' + innerWidth + ' tail=' + document.body.innerText.slice(-500) : 'pending: | url=' + location.href + ' ready=' + document.readyState`,
			"returnByValue": true,
		}, &response)
		result = response.Result.Value
		if strings.HasPrefix(result, "pass:") {
			return
		}
		if strings.HasPrefix(result, "fail:") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	stderr, _ := os.ReadFile(stderrPath)
	if len(stderr) > 5000 {
		stderr = stderr[len(stderr)-5000:]
	}
	t.Fatalf("mobile breadcrumb fixture failed: %s\nChrome stderr tail:\n%s", result, stderr)
}

func TestBreadcrumbSelectorResultsMarksCurrentAndUsesAuthoritativeURLs(t *testing.T) {
	var out bytes.Buffer
	err := BreadcrumbSelectorResults("Task", "task-1", []models.BreadcrumbSelectorItem{
		{ID: "task-1", Name: "Current", URL: "/tasks/task-1?project_id=project-1&tab=changes"},
		{ID: "task-2", Name: "Other", URL: "/tasks/task-2?project_id=project-1&tab=changes"},
	}, false).Render(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{`role="listbox"`, `aria-selected="true"`, `aria-current="true"`, `data-breadcrumb-selector-option`, `/tasks/task-2?project_id=project-1&amp;tab=changes`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in results markup", want)
		}
	}
}
