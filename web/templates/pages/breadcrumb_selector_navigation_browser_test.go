package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBreadcrumbSelectorsNavigateAndCleanUpAcrossHTMXHistoryInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Breadcrumb navigation fixture", nil, "project-browser").Render(context.Background(), &base); err != nil {
		t.Fatalf("render navigation helper: %v", err)
	}
	baseHTML := base.String()
	navStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	navEnd := strings.Index(baseHTML[navStart:], "// Scroll position restoration for drop zones")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("could not isolate production HTMX navigation and title helper")
	}
	navigationScript := baseHTML[navStart : navStart+navEnd]

	renderTask := func(id, title string) string {
		t.Helper()
		task := &models.Task{ID: id, ProjectID: "project-browser", Title: title, Status: models.StatusPending, Category: models.CategoryBacklog}
		var out bytes.Buffer
		if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Task detail: %v", err)
		}
		return `<section data-fixture-route="task" data-resource-id="` + id + `">` + out.String() + `</section>`
	}
	renderLive := func(id, name string) string {
		t.Helper()
		graph := models.AutomationLiveGraph{Automation: models.Automation{ID: id, ProjectID: "project-browser", Name: name, LifecycleState: models.AutomationActive}, RecentCutoff: time.Unix(1, 0)}
		var out bytes.Buffer
		if err := AutomationLiveContent(graph, "project-browser", true).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation Live: %v", err)
		}
		return `<section data-fixture-route="automation-live" data-resource-id="` + id + `">` + out.String() + `</section>`
	}
	renderEdit := func(id, name string) string {
		t.Helper()
		page := models.AutomationBuilderPage{AutomationID: id, Source: "blank", LifecycleState: models.AutomationActive, Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: name, AutomationType: "custom", AdapterKey: "custom"}}}
		var out bytes.Buffer
		if err := AutomationBuilderContent(page, "project-browser").Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation Edit: %v", err)
		}
		return `<section data-fixture-route="automation-edit" data-resource-id="` + id + `">` + out.String() + `</section>`
	}
	renderResults := func(kind, currentID string, items []models.BreadcrumbSelectorItem) string {
		t.Helper()
		var out bytes.Buffer
		if err := components.BreadcrumbSelectorResults(kind, currentID, items, false).Render(context.Background(), &out); err != nil {
			t.Fatalf("render selector results: %v", err)
		}
		return out.String()
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      (function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > (timeout || 6000)) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 15);
      })();
    });
  }
  function fail(message) { throw new Error(message); }
  function selector() { return document.querySelector('[data-breadcrumb-selector]'); }
  function button() { return selector() && selector().querySelector('[data-breadcrumb-selector-button]'); }
  function dialog() { return selector() && selector().querySelector('[data-breadcrumb-selector-dialog]'); }
  function input() { return selector() && selector().querySelector('[data-breadcrumb-selector-search]'); }
  function options() { return Array.prototype.slice.call(document.querySelectorAll('[data-breadcrumb-selector-option]')); }
  function route(kind, id) { var el=document.querySelector('[data-fixture-route="'+kind+'"]'); return !!el && (!id || el.dataset.resourceId===id); }
  function key(target, value) { target.dispatchEvent(new KeyboardEvent('keydown', {key:value, bubbles:true, cancelable:true})); }
  function setSearch(value) { var el=input(); el.value=value; htmx.trigger(el, 'search'); }
  async function counts() { return fetch('/counts').then(function(response){ return response.json(); }); }
  async function waitCount(key, want, label) {
    var started=performance.now(), value={};
    while (performance.now()-started < 6000) { value=await counts(); if (value[key]===want) return; await new Promise(function(resolve){setTimeout(resolve,15);}); }
    throw new Error('timed out waiting for '+label+'; want='+want+'; counts='+JSON.stringify(value));
  }
  async function report(status, message) { try { await fetch('/browser-result?status='+encodeURIComponent(status)+'&message='+encodeURIComponent(message||''), {method:'POST'}); } catch (_) {} }

  (async function() {
    await waitFor(function(){ return route('task', 'task-one'); }, 'initial Task detail');
    var changesTab=document.querySelector('[data-tab="changes"]');
    changesTab.click();
    await waitFor(function(){ return changesTab.classList.contains('tab-active') && !document.getElementById('tab-changes').classList.contains('hidden'); }, 'client-side Changes tab transition');
    key(button(), 'Enter');
    await waitFor(function(){ return dialog() && dialog().open; }, 'Task selector dialog open');
    await waitFor(function(){ return document.activeElement===input(); }, 'Task selector search focus');
    await waitFor(function(){ return options().length>0; }, 'Task selector initial results');

    setSearch('slow');
    await waitCount('taskSlow', 1, 'slow search request');
    setSearch('two');
    await waitFor(function(){ return options().some(function(option){return option.textContent.indexOf('Task Two')>=0;}); }, 'fresh Task search response');
    await new Promise(function(resolve){ setTimeout(resolve, 450); });
    if (options().some(function(option){return option.textContent.indexOf('Stale Task')>=0;})) fail('stale Task response replaced newer results');
    input().focus();
    key(input(), 'ArrowDown');
    if (!document.activeElement.matches('[data-breadcrumb-selector-option]')) fail('ArrowDown did not focus Task result; active='+document.activeElement.outerHTML+'; options='+options().length+'; installed='+window.openVibelyBreadcrumbSelectorInstalled);
    key(document.activeElement, 'Enter');
    await waitFor(function(){ return route('task', 'task-two'); }, 'Task switch');
    if (location.pathname!='/tasks/task-two' || new URLSearchParams(location.search).get('tab')!=='changes' || new URLSearchParams(location.search).get('project_id')!=='project-browser') fail('Task switch lost project or tab context: '+location.href);
    if (document.title!=='Task Two - OpenVibely') fail('Task switch did not update title: '+document.title);
    if (document.querySelector('[data-nav-base="/tasks"]').getAttribute('href')!='/tasks?project_id=project-browser') fail('Task sidebar destination changed');

    var reopenedDialog=dialog(), showCalls=0, originalShow=reopenedDialog.showModal.bind(reopenedDialog);
    reopenedDialog.showModal=function(){ showCalls++; return originalShow(); };
    button().click();
    await waitFor(function(){ return dialog().open; }, 'Task selector reopen');
    if (showCalls!==1) fail('selector initialized duplicate click listeners: '+showCalls);
		dialog().dispatchEvent(new MouseEvent('click',{bubbles:true}));
		await waitFor(function(){ return !dialog().open && button().getAttribute('aria-expanded')==='false'; }, 'outside dismissal');
    button().click();
    await waitFor(function(){ return dialog().open; }, 'Task selector before route change');
    await window.openVibelyNavigate('/other');
    await waitFor(function(){ return route('other'); }, 'route change');
    if (document.querySelector('[data-breadcrumb-selector-dialog][open]')) fail('route change retained open selector');
    history.back();
    await waitFor(function(){ return route('task', 'task-two'); }, 'Task history restoration');
    if (document.querySelector('[data-breadcrumb-selector-dialog][open]')) fail('history restoration reopened selector');
    if (document.title!=='Task Two - OpenVibely') fail('Task history restoration lost title');
    history.forward();
    await waitFor(function(){ return route('other'); }, 'history forward');
    history.back();
    await waitFor(function(){ return route('task', 'task-two'); }, 'history back again');

    await window.openVibelyNavigate('/automations/auto-one?project_id=project-browser');
    await waitFor(function(){ return route('automation-live', 'auto-one'); }, 'Automation Live');
    htmx.process(selector());
    var liveName=button().querySelector('span');
    var liveNameLeft=liveName.getBoundingClientRect().left;
    var liveSlash=document.querySelector('[data-automation-breadcrumb] > span').getBoundingClientRect();
    var liveButtonBox=button().getBoundingClientRect();
    button().click();
    await waitFor(function(){ return dialog().open && options().length>0; }, 'Automation Live selector');
    setSearch('two');
    await waitFor(function(){ return options().length===1 && options()[0].textContent.indexOf('Automation Two')>=0; }, 'Automation Live search');
    key(input(), 'ArrowDown'); key(document.activeElement, 'Enter');
    await waitFor(function(){ return route('automation-live', 'auto-two'); }, 'Automation Live switch');
    if (location.pathname!='/automations/auto-two' || new URLSearchParams(location.search).get('project_id')!=='project-browser') fail('Automation Live switch lost context: '+location.href);
    if (document.title!=='Automation Two - OpenVibely') fail('Automation Live switch did not update title');
    if (document.querySelector('[data-nav-base="/automations"]').getAttribute('href')!='/automations?project_id=project-browser') fail('Automation sidebar destination changed');

    await window.openVibelyNavigate('/automations/auto-one/builder?project_id=project-browser');
    await waitFor(function(){ return route('automation-edit', 'auto-one'); }, 'Automation Edit');
    var editName=document.querySelector('[data-automation-name]');
    var editNameStyle=getComputedStyle(editName);
    var editNameTextLeft=editName.getBoundingClientRect().left+parseFloat(editNameStyle.borderLeftWidth)+parseFloat(editNameStyle.paddingLeft);
    var editSlash=document.querySelector('[data-automation-editable-breadcrumb] > span').getBoundingClientRect();
    if(Math.abs(editNameTextLeft-liveNameLeft)>1) fail('Automation name shifted horizontally when Edit opened: '+JSON.stringify({liveNameLeft:liveNameLeft,editNameTextLeft:editNameTextLeft,liveSlashRight:liveSlash.right,editSlashRight:editSlash.right,liveButtonLeft:liveButtonBox.left,inputLeft:editName.getBoundingClientRect().left,inputPadding:editNameStyle.paddingLeft,inputBorder:editNameStyle.borderLeftWidth}));
    var editSelector=editName && editName.nextElementSibling;
    var editButton=editSelector && editSelector.querySelector('[data-breadcrumb-selector-button]');
    var editDialog=editSelector && editSelector.querySelector('[data-breadcrumb-selector-dialog]');
    var editSearch=editSelector && editSelector.querySelector('[data-breadcrumb-selector-search]');
    if(!editName || !editSelector || !editButton) fail('Automation Edit selector caret is not attached after the editable name');
    if(!editButton.hasAttribute('data-breadcrumb-selector-caret-only') || editButton.textContent.trim()!=='') fail('Automation Edit selector must render only the caret trigger');
    await waitFor(function(){ return editButton.classList.contains('w-7') && editButton.classList.contains('h-8'); }, 'Automation Edit caret class settling');
    var selectorBox=editSelector.getBoundingClientRect();
    if(selectorBox.width > 0.5) fail('Automation Edit selector wrapper changed the name input layout width: '+JSON.stringify(selectorBox));
    var caretButtonBox=editButton.getBoundingClientRect();
    if(caretButtonBox.width < 27 || caretButtonBox.height < 31) fail('Automation Edit selector caret has a collapsed hit area: '+JSON.stringify({rect:caretButtonBox,className:editButton.className,width:getComputedStyle(editButton).width,minWidth:getComputedStyle(editButton).minWidth,maxWidth:getComputedStyle(editButton).maxWidth,height:getComputedStyle(editButton).height}));
    var caretHit=document.elementFromPoint(caretButtonBox.left+caretButtonBox.width/2, caretButtonBox.top+caretButtonBox.height/2);
    if(!caretHit || (caretHit!==editButton && !editButton.contains(caretHit))) fail('Automation Edit selector caret does not own its pointer hit area');
    editButton.focus();
    if(document.activeElement!==editButton) fail('Automation Edit selector caret cannot receive focus');
    htmx.process(editSelector);
    editButton.click();
    await waitFor(function(){ return editDialog.open && editSelector.querySelectorAll('[data-breadcrumb-selector-option]').length>0; }, 'Automation Edit selector');
    editSearch.value='two'; htmx.trigger(editSearch, 'search');
    await waitFor(function(){ return editSelector.querySelectorAll('[data-breadcrumb-selector-option]').length===1; }, 'Automation Edit search');
    key(editSearch, 'ArrowDown'); key(document.activeElement, 'Enter');
    await waitFor(function(){ return route('automation-edit', 'auto-two'); }, 'Automation Edit switch');
    if (location.pathname!='/automations/auto-two/builder' || new URLSearchParams(location.search).get('project_id')!=='project-browser') fail('Automation Edit switch lost context: '+location.href);
    if (document.title!=='Automation Two - OpenVibely') fail('Automation Edit switch did not update title');

    var finalCounts=await counts();
    if (finalCounts.taskSlow!==1 || finalCounts.taskTwo!==1) fail('unexpected Task search request counts: '+JSON.stringify(finalCounts));
    document.body.setAttribute('data-test-result', 'pass');
    await report('pass', '');
  })().catch(async function(error) {
    var message=String(error && error.stack || error);
    document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', message);
    await report('fail', message);
  });
});
</script>`

	style := `<style>
		.hidden{display:none!important} .flex{display:flex}.items-center{align-items:center}.gap-2{gap:.5rem}.px-1{padding-left:.25rem;padding-right:.25rem}.ml-1{margin-left:.25rem}.ml-2{margin-left:.5rem}.-ml-1{margin-left:-.25rem}.px-\[3px\]{padding-left:3px;padding-right:3px}.input-bordered{border:1px solid transparent}.relative{position:relative}.z-10{z-index:10}button{border:0} dialog{border:0;background:transparent}.modal-box{box-sizing:border-box}.modal-backdrop{position:fixed;inset:0}.modal-backdrop button{width:100%;height:100%}		.w-0{width:0}.w-7{width:1.75rem}.h-8{height:2rem}.max-w-full{max-width:100%}.overflow-visible{overflow:visible}
		[data-breadcrumb-selector-dialog][open]{display:grid}.max-w-\[calc\(100vw-2rem\)\]{max-width:calc(100vw - 2rem)}</style>`

	var taskSlow, taskTwo, taskBlank atomic.Int32
	browserResult := make(chan string, 8)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/tasks/task-one":
			fragment := renderTask("task-one", "Task One")
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(fragment))
				return
			}
			document := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Task One - OpenVibely</title><script src="/htmx.js"></script><script>` + navigationScript + `</script>` + style + runner + `</head><body><nav><a data-nav-base="/tasks" href="/tasks?project_id=project-browser">Tasks</a><a data-nav-base="/automations" href="/automations?project_id=project-browser">Automations</a></nav><main id="main-content" hx-history-elt>` + fragment + `</main></body></html>`
			_, _ = w.Write([]byte(document))
		case "/tasks/task-two":
			_, _ = w.Write([]byte(renderTask("task-two", "Task Two")))
		case "/automations/auto-one":
			_, _ = w.Write([]byte(renderLive("auto-one", "Automation One")))
		case "/automations/auto-two":
			_, _ = w.Write([]byte(renderLive("auto-two", "Automation Two")))
		case "/automations/auto-one/builder":
			_, _ = w.Write([]byte(renderEdit("auto-one", "Automation One")))
		case "/automations/auto-two/builder":
			_, _ = w.Write([]byte(renderEdit("auto-two", "Automation Two")))
		case "/other":
			_, _ = w.Write([]byte(`<section data-fixture-route="other"><span hidden data-openvibely-page-title="Other - OpenVibely"></span>Other</section>`))
		case "/breadcrumb-selectors/tasks":
			search := r.URL.Query().Get("search")
			taskDestination := "/tasks/task-two?project_id=project-browser"
			if tab := r.URL.Query().Get("tab"); tab != "" {
				taskDestination += "&tab=" + tab
			}
			if search == "slow" {
				taskSlow.Add(1)
				time.Sleep(350 * time.Millisecond)
				_, _ = w.Write([]byte(renderResults("Task", "task-one", []models.BreadcrumbSelectorItem{{ID: "stale", Name: "Stale Task", URL: "/tasks/stale?project_id=project-browser&tab=changes"}})))
				return
			}
			if search == "two" {
				taskTwo.Add(1)
				_, _ = w.Write([]byte(renderResults("Task", r.URL.Query().Get("current_id"), []models.BreadcrumbSelectorItem{{ID: "task-two", Name: "Task Two", URL: taskDestination}})))
				return
			}
			taskBlank.Add(1)
			_, _ = w.Write([]byte(renderResults("Task", r.URL.Query().Get("current_id"), []models.BreadcrumbSelectorItem{{ID: r.URL.Query().Get("current_id"), Name: "Current Task", URL: r.URL.Query().Get("current_id")}, {ID: "task-two", Name: "Task Two", URL: taskDestination}})))
		case "/breadcrumb-selectors/automations":
			view := r.URL.Query().Get("view")
			destination := "/automations/auto-two?project_id=project-browser"
			if view == "edit" {
				destination = "/automations/auto-two/builder?project_id=project-browser"
			}
			_, _ = w.Write([]byte(renderResults("Automation", r.URL.Query().Get("current_id"), []models.BreadcrumbSelectorItem{{ID: "auto-two", Name: "Automation Two", URL: destination}})))
		case "/counts":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int32{"taskSlow": taskSlow.Load(), "taskTwo": taskTwo.Load(), "taskBlank": taskBlank.Load()})
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "breadcrumb-navigation.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check", "--window-size=390,844", "--user-data-dir="+filepath.Join(t.TempDir(), "breadcrumb-navigation-profile"), server.URL+"/tasks/task-one?project_id=project-browser&tab=details")
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome breadcrumb fixture: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(35 * time.Second):
		outcome = "fail:timed out waiting for browser result callback"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("real HTMX breadcrumb navigation fixture failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}
}
