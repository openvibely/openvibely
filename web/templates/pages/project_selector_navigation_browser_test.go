package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestSidebarProjectSelectorSearchAndSwitchInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{
		{ID: "default", Name: "Default", IsDefault: true},
		{ID: "payments-api", Name: "Payments API"},
		{ID: "payments-web", Name: "Payments Web"},
		{ID: "payments-web-copy", Name: "Payments Web"},
	}

	var rendered bytes.Buffer
	if err := layout.Sidebar(projects, "default").Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render Sidebar: %v", err)
	}
	sidebarHTML := rendered.String()
	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	var selectorScripts strings.Builder
	for _, match := range scriptPattern.FindAllStringSubmatch(sidebarHTML, -1) {
		body := match[1]
		if strings.Contains(body, "Project selector behavior") || strings.Contains(body, "function persistSelectedProject") {
			selectorScripts.WriteString("<script>")
			selectorScripts.WriteString(body)
			selectorScripts.WriteString("</script>")
		}
	}
	if !strings.Contains(selectorScripts.String(), "Project selector behavior") || !strings.Contains(selectorScripts.String(), "function persistSelectedProject") {
		t.Fatal("could not isolate the production project selector scripts")
	}
	markup := scriptPattern.ReplaceAllString(sidebarHTML, "")

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'});
  }
  function fail(message) { throw new Error(message); }
  function key(target, value) {
    target.dispatchEvent(new KeyboardEvent('keydown', {key: value, bubbles: true, cancelable: true}));
  }
  function visibleOptions() {
    return Array.prototype.slice.call(document.querySelectorAll('[data-project-selector-option]')).filter(function(option) { return option.getClientRects().length > 0; });
  }
  function typeSearch(input, value) {
    input.focus();
    input.select();
    if (!document.execCommand('insertText', false, value)) fail('browser text insertion was not supported');
  }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  (async function() {
    var trigger = document.getElementById('project-selector-trigger');
    var dialog = document.getElementById('project-selector-dialog');
    var search = document.getElementById('project-selector-search');
    var select = document.getElementById('project-selector');
    var clear = document.querySelector('[data-project-selector-clear]');
    var noMatch = document.querySelector('[data-project-selector-no-match]');
    var main = document.createElement('main');
    main.id = 'main-content';
    document.body.appendChild(main);
    if (!trigger || !dialog || !search || !select || !clear || !noMatch) fail('project selector fixture is incomplete');
    if (trigger.textContent.trim() !== 'Default') fail('current project label is not rendered');
    ['select', 'select-bordered', 'select-sm', 'w-full', 'sidebar-project-select'].forEach(function(className) {
      if (!trigger.classList.contains(className)) fail('collapsed selector lost its prior visual class: ' + className);
    });
    if (trigger.querySelector('svg') || trigger.hasAttribute('data-project-selector-caret') || trigger.classList.contains('bg-none')) fail('collapsed selector replaced the original select arrow');
    if (document.querySelectorAll('[data-project-selector-option]').length !== 4) fail('all identity-only project options are not rendered');

    trigger.focus();
    if (document.activeElement !== trigger) fail('project selector trigger could not receive focus: active=' + (document.activeElement && document.activeElement.outerHTML));
    key(trigger, 'Enter');
    if (!dialog.open) fail('Enter did not open the project selector');
    if (document.activeElement !== search) fail('opening the selector did not focus search');
    var triggerRect = trigger.getBoundingClientRect();
    var dialogRect = dialog.getBoundingClientRect();
    if (dialogRect.right > window.innerWidth + 1 || dialogRect.left < -1) fail('selector is not contained on the mobile viewport');
    if (Math.abs(dialogRect.top - (triggerRect.bottom + 4)) > 2) fail('selector popup is not attached below its trigger');
    if (Math.abs(dialogRect.left - triggerRect.left) > 2) fail('selector popup is not aligned underneath its trigger');
    if (visibleOptions().length !== 4) fail('initial project options are not all visible');

    typeSearch(search, '  pAyMeNtS wEb  ');
    var filtered = visibleOptions();
    if (filtered.length !== 3) fail('trimmed case-insensitive search did not retain the current project and duplicate matches');
    if (!filtered.some(function(option) { return option.dataset.projectId === 'default' && option.getAttribute('aria-selected') === 'true'; })) fail('current project is not identifiable while searching');
    if (filtered.filter(function(option) { return option.dataset.projectName === 'Payments Web'; }).length !== 2) fail('duplicate project names were not independently searchable');
    if (clear.hidden) fail('clear control stayed hidden after searching');

    typeSearch(search, '  does-not-exist  ');
    if (visibleOptions().length !== 1 || visibleOptions()[0].dataset.projectId !== 'default') fail('no-match filtering did not retain only the current project');
    if (noMatch.hidden || noMatch.textContent.indexOf('No projects match') === -1) fail('no-match state is not clear');
    clear.click();
    if (search.value !== '' || !noMatch.hidden || visibleOptions().length !== 4) fail('clearing search did not restore all projects: value=' + JSON.stringify(search.value) + ', noMatchHidden=' + noMatch.hidden + ', visible=' + visibleOptions().length + ', hidden=' + Array.prototype.slice.call(document.querySelectorAll('[data-project-selector-option]')).map(function(option) { return option.dataset.projectId + ':' + option.hidden; }).join(','));

    key(search, 'ArrowDown');
    if (document.activeElement.dataset.projectId !== 'default') fail('ArrowDown did not focus the first project result');
    key(document.activeElement, 'ArrowDown');
    if (document.activeElement.dataset.projectId !== 'payments-api') fail('ArrowDown did not move through project results');
    key(document.activeElement, 'Escape');
    if (dialog.open || document.activeElement !== trigger) fail('Escape did not close the selector and restore trigger focus');

    var navigations = [];
    var preferences = 0;
    window.openVibelyNavigate = function(url) { navigations.push(url); history.pushState({}, '', url); return Promise.resolve(); };
    window.fetch = (function(originalFetch) {
      return function(url, options) {
        if (String(url) === '/ui/preferences') { preferences++; return Promise.resolve({ok: true, status: 204}); }
        return originalFetch(url, options);
      };
    })(window.fetch.bind(window));

    trigger.click();
    var unsaved = document.createElement('dialog');
    unsaved.id = 'unsaved-dialog';
    unsaved.setAttribute('open', '');
    document.body.appendChild(unsaved);
    var confirmCalls = 0;
    window.confirm = function() { confirmCalls++; return false; };
    document.querySelector('[data-project-id="payments-api"]').click();
    if (confirmCalls !== 1 || select.value !== 'default' || trigger.textContent.trim() !== 'Default' || navigations.length !== 0 || preferences !== 0) fail('cancelled unsaved project switch changed state');
    if (!unsaved.hasAttribute('open')) fail('cancelled project switch closed the unsaved dialog');
    unsaved.remove();

    window.confirm = function() { confirmCalls++; return true; };
    trigger.click();
    var confirmed = document.createElement('dialog');
    confirmed.id = 'confirmed-dialog';
    confirmed.setAttribute('open', '');
    document.body.appendChild(confirmed);
    document.querySelector('[data-project-id="payments-api"]').click();
    await wait(0);
    if (select.value !== 'payments-api' || trigger.textContent.trim() !== 'Payments API') fail('confirmed project switch did not update the selected project');
    if (confirmed.hasAttribute('open') || navigations[navigations.length - 1] !== '/analytics?project_id=payments-api' || preferences !== 1) fail('confirmed project switch did not persist and navigate on the current page family');

    history.replaceState({}, '', '/channels?project_id=payments-api');
    trigger.click();
    document.querySelector('[data-project-id="payments-web"]').click();
    await wait(0);
    if (navigations[navigations.length - 1] !== '/channels?project_id=payments-web') fail('integration page switch did not preserve its route family');

    trigger.click();
    if (!dialog.open) fail('selector did not reopen after navigation');
    document.dispatchEvent(new CustomEvent('htmx:beforeSwap', {bubbles: true, detail: {target: main}}));
    if (dialog.open) fail('main-content HTMX swap retained an open selector');
    history.replaceState({}, '', '/analytics?project_id=payments-web-copy');
    select.value = 'payments-web';
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', {bubbles: true, detail: {target: main}}));
    if (select.value !== 'payments-web-copy' || trigger.textContent.trim() !== 'Payments Web') fail('HTMX route synchronization did not update the project selector');

    document.body.setAttribute('data-test-result', 'pass');
    await report('pass', '');
  })().catch(async function(error) {
    document.body.setAttribute('data-test-result', 'fail');
    document.body.setAttribute('data-test-error', String(error && error.stack || error));
    await report('fail', String(error && error.stack || error));
  });
});
</script>`

	page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<style>[hidden]{display:none!important} dialog[open]{display:block;position:fixed;box-sizing:border-box;width:448px;max-width:calc(100vw - 16px);max-height:calc(100vh - 16px);margin:0;padding:0} .sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0} .flex{display:flex}.w-full{width:100%}.min-w-0{min-width:0}.truncate{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.sidebar-aside{width:256px}.sidebar-inner{padding:16px}.sidebar-project-select{box-sizing:border-box;height:32px}</style>` +
		`<script>window._tabVisibility={dispatchSSEEvent:function(){},registerSSE:function(){return {close:function(){}}}};window.htmx={process:function(){},trigger:function(){},ajax:function(){return Promise.resolve();}};window.toggleTheme=function(){};window.openVibelyNavigate=function(){return Promise.resolve();};</script></head><body>` +
		markup + selectorScripts.String() + runner + `</body></html>`

	browserResult := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/ui/preferences" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "sidebar-project-selector-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "sidebar-project-selector-browser-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=390,844",
		"--user-data-dir="+profileDir, server.URL+"/analytics?project_id=default",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome project selector fixture: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("project selector browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}
}
