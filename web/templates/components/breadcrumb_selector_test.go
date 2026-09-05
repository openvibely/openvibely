package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBreadcrumbSelectorRendersAccessibleBoundedDialog(t *testing.T) {
	var out bytes.Buffer
	err := BreadcrumbSelector(models.BreadcrumbSelector{
		ID:          "task-resource-selector",
		Kind:        "Task",
		CurrentID:   "task-1",
		CurrentName: "A very long current task title",
		SearchURL:   "/breadcrumb-selectors/tasks?project_id=project-1&current_id=task-1",
		ContextName: "tab", ContextValue: "changes",
	}).Render(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		`data-breadcrumb-selector`, `aria-expanded="false"`, `aria-haspopup="dialog"`,
		`role="dialog"`, `aria-modal="true"`, `Switch Task`, `Search Tasks`,
		`hx-trigger="input changed delay:200ms, search"`, `hx-sync="this:replace"`,
		`hx-include="closest [data-breadcrumb-selector]"`, `name="tab" value="changes"`,
		`max-w-[calc(100vw-2rem)]`, `overflow-hidden`, `data-breadcrumb-selector-status`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in selector markup", want)
		}
	}
}

func TestBreadcrumbSelectorKeyboardFocusAndContainmentInChrome(t *testing.T) {
	chrome := testChromePath(t)
	var selector bytes.Buffer
	if err := BreadcrumbSelector(models.BreadcrumbSelector{ID: "browser-selector", Kind: "Task", CurrentID: "one", CurrentName: "A title long enough to exercise responsive truncation", SearchURL: "/results"}).Render(context.Background(), &selector); err != nil {
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
	    var box=dialog.querySelector('.modal-box').getBoundingClientRect();
	    if(box.left < 15 || box.right > innerWidth-15 || box.bottom > innerHeight-15) throw new Error('selector escaped viewport: '+JSON.stringify(box));
		    document.documentElement.setAttribute('data-theme','light');
		    var themeBox=dialog.querySelector('.modal-box'), lightStyle=getComputedStyle(themeBox);
		    var light=[lightStyle.backgroundColor,lightStyle.borderColor,lightStyle.color].join('|');
		    document.documentElement.setAttribute('data-theme','dark');
		    var darkStyle=getComputedStyle(themeBox), dark=[darkStyle.backgroundColor,darkStyle.borderColor,darkStyle.color].join('|');
		    if(light===dark) throw new Error('semantic colors did not respond to dark theme');
		    document.documentElement.setAttribute('data-theme','imported-contrast');
		    var contrastStyle=getComputedStyle(themeBox);
		    if(contrastStyle.backgroundColor!=='rgb(0, 0, 0)' || contrastStyle.color!=='rgb(255, 255, 255)' || contrastStyle.borderColor!=='rgb(255, 255, 255)') throw new Error('imported high-contrast variables were not honored: '+[contrastStyle.backgroundColor,contrastStyle.color,contrastStyle.borderColor].join('|'));
		    if(!dialog.querySelector('.bg-base-100') || !dialog.querySelector('.border-base-300')) throw new Error('theme semantic classes missing');	    document.body.setAttribute('data-test-result','pass');
	  })().catch(function(error){ document.body.setAttribute('data-test-result','fail'); document.body.setAttribute('data-test-error',String(error.stack||error)); });
	});
	</script>`
	page := `<!doctype html><html data-theme="light"><head><meta name="viewport" content="width=device-width"><style>dialog{border:0;background:transparent}.modal-box{box-sizing:border-box;color:var(--fixture-content)}.w-\[28rem\]{width:28rem}.max-w-\[calc\(100vw-2rem\)\]{max-width:calc(100vw - 2rem)}.max-h-\[min\(32rem\,calc\(100dvh-2rem\)\)\]{max-height:min(32rem,calc(100dvh - 2rem))}.bg-base-100{background-color:var(--fixture-base)}.border-base-300{border-color:var(--fixture-border)}[data-theme="light"]{--fixture-base:rgb(255,255,255);--fixture-border:rgb(210,210,210);--fixture-content:rgb(20,20,20)}[data-theme="dark"]{--fixture-base:rgb(24,24,27);--fixture-border:rgb(82,82,91);--fixture-content:rgb(244,244,245)}[data-theme="imported-contrast"]{--fixture-base:rgb(0,0,0);--fixture-border:rgb(255,255,255);--fixture-content:rgb(255,255,255)}</style><script src="/htmx.js"></script></head><body data-test-result="pending">` + selector.String() + runner + `</body></html>`
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
	runHeadlessChromeFixture(t, chrome, server.URL+"/", "breadcrumb selector keyboard", 375, 20*time.Second)
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
