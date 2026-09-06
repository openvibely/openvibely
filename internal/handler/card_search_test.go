package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

func TestCollectionSelectionBrowserContractIsShared(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		`checkbox.addEventListener('click'`, `event.stopPropagation()`, `state.last`, `event.key !== 'Escape'`,
		`data-card-select-mode`, `data-card-mobile-actions`, `data-card-bulk-confirm`, `if (selectLoaded.checked) state.ids[id] = true`, `selectLoaded.indeterminate`, `selectLoaded.checked`, `data-card-filters-popover`, `setCardFilterDropdown(dropdown, false)`,
		`data-card-query-secondary`, `data-card-selection-actions`, `data-card-mark-read-selected`,
		`[data-card-list-toolbar] .dropdown:not(.dropdown-open) > [data-card-filters-popover]`,
		`absolute left-5 top-8 z-20 hidden md:block`, `[data-card-pagination-root]:has([data-card-list-toolbar]) [data-search-card]:not([data-search-empty-state])`, `padding-left: 2rem`, `alignSelectionGutter(card, gutter)`, `_openVibelyInstallSelectionCards`, `if (!existing[id]) delete state.ids[id]`, `focus({preventScroll: true})`,
		`window.refreshCardListToolbars(nextContainer)`} {
		require.Contains(t, body, want)
	}
	require.NotContains(t, body, `card.classList.add('md:pl-8')`)
}

func TestCollectionSelectionGutterDoesNotShiftCardContent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	cards := []models.LLMConfig{{ID: "stable", Name: "Stable", Provider: models.ProviderTest, Model: "stable"}}
	var content bytes.Buffer
	require.NoError(t, pages.ModelsContentPageWithPaginationAndState(cards, cards, map[string]int{}, false, false, pages.CardListState{}).Render(t.Context(), &content))
	var base bytes.Buffer
	require.NoError(t, layout.Base("Stable selection gutter", nil, "").Render(t.Context(), &base))
	var local []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		local = append(local, line)
	}
	page := strings.Replace(strings.Join(local, "\n"), "</head>", `<style>.card{position:relative}.card-body{padding:2rem}@media (min-width:768px){.md\:pl-8{padding-left:2rem!important}}</style></head>`, 1)
	page = strings.Replace(page, "</main>", content.String()+"</main>", 1)
	page = strings.Replace(page, "</body>", `<script>
	(function() {
		var title=document.querySelector('[data-model-id="stable"] h3');
		var initialLeft=title.getBoundingClientRect().left;
		requestAnimationFrame(function(){ requestAnimationFrame(function(){
			var finalLeft=title.getBoundingClientRect().left;
			var status=Math.abs(finalLeft-initialLeft)<0.5?'pass':'fail';
			fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':status},body:'initial='+initialLeft+' final='+finalLeft,keepalive:true});
		}); });
	})();
	</script></body>`, 1)

	result := make(chan string, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ": " + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-extensions", "--no-first-run", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"), "--window-size=1024,768", fixture.URL)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()
	select {
	case got := <-result:
		stopHandlerBrowserProcess(cmd)
		stopped = true
		require.Equal(t, "pass", strings.SplitN(got, ":", 2)[0], got)
	case <-time.After(30 * time.Second):
		t.Fatal("selection gutter browser regression timed out")
	}
}

func TestCollectionSelectionProductionBrowserInteractions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	renderWithState := func(cards []models.LLMConfig, state pages.CardListState) string {
		var out bytes.Buffer
		require.NoError(t, pages.ModelsContentPageWithPaginationAndState(cards, cards, map[string]int{}, false, false, state).Render(t.Context(), &out))
		return out.String()
	}
	render := func(cards []models.LLMConfig) string {
		return renderWithState(cards, pages.CardListState{})
	}
	initial := []models.LLMConfig{
		{ID: "default", Name: "Default", Provider: models.ProviderTest, Model: "default", IsDefault: true},
		{ID: "a", Name: "Alpha", Provider: models.ProviderTest, Model: "alpha"},
		{ID: "b", Name: "Bravo", Provider: models.ProviderTest, Model: "bravo"},
		{ID: "c", Name: "Charlie", Provider: models.ProviderTest, Model: "charlie"},
	}
	replacement := []models.LLMConfig{
		{ID: "default", Name: "Default", Provider: models.ProviderTest, Model: "default", IsDefault: true},
		{ID: "b", Name: "Bravo", Provider: models.ProviderTest, Model: "bravo"},
		{ID: "c", Name: "Charlie", Provider: models.ProviderTest, Model: "charlie"},
		{ID: "e", Name: "Echo", Provider: models.ProviderTest, Model: "echo"},
	}
	finalCards := []models.LLMConfig{
		{ID: "default", Name: "Default", Provider: models.ProviderTest, Model: "default", IsDefault: true},
		{ID: "survivor", Name: "Survivor", Provider: models.ProviderTest, Model: "survivor"},
	}
	replacementJSON, err := json.Marshal(render(replacement))
	require.NoError(t, err)
	var unmanagedSkills bytes.Buffer
	require.NoError(t, pages.SkillsContentForProjectPageWithState([]pages.SkillCard{{Handle: "read-only", Name: "Read only", Scope: "global", Source: "global", Enabled: true}}, false, "project", false, pages.CardListState{Filters: map[string]string{"enabled": "true"}}).Render(t.Context(), &unmanagedSkills))
	unmanagedSkillsJSON, err := json.Marshal(unmanagedSkills.String())
	require.NoError(t, err)
	var rejectedAlerts bytes.Buffer
	require.NoError(t, pages.AlertsContentPageWithState(nil, "project", 0, false, "", "", pages.CardListState{ProjectID: "project", Filters: map[string]string{}}).Render(t.Context(), &rejectedAlerts))
	rejectedAlertsJSON, err := json.Marshal(rejectedAlerts.String())
	require.NoError(t, err)
	var emptyChannels bytes.Buffer
	require.NoError(t, pages.SettingsContent(pages.ChannelsSettingsView{CurrentProjectID: "project"}).Render(t.Context(), &emptyChannels))
	emptyChannelsJSON, err := json.Marshal(emptyChannels.String())
	require.NoError(t, err)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Collection selection browser", nil, "").Render(t.Context(), &base))
	var local []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		local = append(local, line)
	}
	initialHTML := renderWithState(initial, pages.CardListState{Filters: map[string]string{"provider": "openai"}})
	page := strings.Replace(strings.Join(local, "\n"), "</head>", `<style>
	.hidden{display:none!important}.flex{display:flex!important}.fixed{position:fixed!important}.card{position:relative}.card-body{padding:2rem}.left-5{left:1.25rem}.top-8{top:2rem}
	@media (min-width:768px){.md\:inline-flex{display:inline-flex!important}.md\:hidden{display:none!important}.md\:pl-8{padding-left:2rem!important}}
	</style></head>`, 1)
	page = strings.Replace(page, "</main>", initialHTML+"</main>", 1)
	runner := `<script>
(function() {
	  function result(status, message) { var n=document.createElement('div'); n.id='browser-result'; n.dataset.status=status; n.textContent=message||status; document.body.appendChild(n); fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message||status, keepalive:true}).catch(function(){}); }  function fail(message) { throw new Error(message); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function card(id) { return document.querySelector('[data-model-id="'+id+'"]'); }
  function checkbox(id) { return card(id).querySelector('[data-card-selection-gutter] input'); }
	  function selectedCount() { return document.querySelector('[data-card-selected-count]').textContent.trim(); }
	  function verifyMixedCollection(key) {
	    var root=document.createElement('section'); root.setAttribute('data-card-pagination-root','');
		    var fixedAttributes=key==='channels' ? ' data-card-select-id="channel:github" data-card-select-eligible="true" data-channel-type="github"' : '';
		    root.innerHTML='<div data-card-list-toolbar="'+key+'" data-card-bulk-url="/'+key+'/bulk" data-card-entity-type="items" data-card-identity-kind="ids"><form data-card-query-form><label data-card-select-loaded-control><input type="checkbox" data-card-select-loaded></label></form><div class="hidden" data-card-selection-actions><strong data-card-selected-count>0 selected</strong></div><dialog data-card-bulk-confirm><span data-card-bulk-confirm-title></span><button data-card-bulk-confirm-delete></button><span data-card-bulk-error></span></dialog></div><article data-search-card'+fixedAttributes+'><h3>Fixed</h3></article><article data-search-card data-card-select-id="eligible" data-card-select-eligible="true"><h3>Eligible</h3></article>';
	    document.querySelector('main').appendChild(root); window.refreshCardListToolbars(root);
	    var controls=root.querySelectorAll('[data-card-selection-gutter] input'), master=root.querySelector('[data-card-select-loaded]');
	    if (controls.length!==2 || (key==='channels' ? controls[0].disabled : !controls[0].disabled) || controls[1].disabled) fail(key+' did not distinguish its fixed and eligible cards');
	    if (key!=='channels' && (!controls[0].title || controls[0].title.toLowerCase().indexOf('custom personalities')<0)) fail(key+' fixed card lacks the page-specific selection explanation');
	    master.click();
	    var expectedCount=key==='channels' ? '2 selected' : '1 selected';
	    if (!master.checked || !controls[1].checked || root.querySelector('[data-card-selected-count]').textContent.trim()!==expectedCount) fail(key+' master checkbox did not select its loaded eligible cards');
	    root.remove();
	  }
	  async function run() {    await wait(100);
    var chip=document.querySelector('[data-card-filter-chip="provider"]'), clear=document.querySelector('[data-card-clear-filters]');
    if (!chip || !clear || !chip.textContent.includes('OpenAI')) fail('server-rendered active filter chip or Clear all is missing');
	    clear.addEventListener('click', function(event) { event.preventDefault(); window.selectionNavigation=clear.getAttribute('href'); }, {once:true});
	    clear.click();    if (!window.selectionNavigation || window.selectionNavigation.includes('provider=')) fail('Clear all did not navigate without the active filter');
	    var filterButton=document.querySelector('[data-card-filters-button]'), filterDropdown=filterButton.closest('.dropdown');
	    filterButton.click();
	    if (!filterDropdown.classList.contains('dropdown-open') || filterButton.getAttribute('aria-expanded') !== 'true') fail('Filters did not open accessibly');
	    document.body.dispatchEvent(new MouseEvent('click', {bubbles:true}));
	    if (filterDropdown.classList.contains('dropdown-open') || filterButton.getAttribute('aria-expanded') !== 'false') fail('outside click did not close Filters');
	    filterButton.click();
	    filterButton.click();
	    var filterPopover=filterDropdown.querySelector('[data-card-filters-popover]'), closedSelector='[data-card-list-toolbar] .dropdown:not(.dropdown-open) > [data-card-filters-popover]';
	    if (filterDropdown.classList.contains('dropdown-open') || filterButton.getAttribute('aria-expanded') !== 'false' || getComputedStyle(filterPopover).visibility !== 'hidden') fail('second Filters click did not close the popover: class='+filterDropdown.className+' expanded='+filterButton.getAttribute('aria-expanded')+' matches='+filterPopover.matches(closedSelector)+' visibility='+getComputedStyle(filterPopover).visibility);
	    var desktopSelect=document.querySelector('[data-card-select-loaded]'), mobileSelect=document.querySelector('[data-card-select-mode]');
	    if (!desktopSelect || desktopSelect.tagName !== 'INPUT' || desktopSelect.type !== 'checkbox' || !desktopSelect.getAttribute('aria-label')) fail('loaded-card control is not an accessible checkbox');
	    var alignedCard=card('a'), alignedTitle=alignedCard.querySelector('h3'), alignedGutter=alignedCard.querySelector('[data-card-selection-gutter]');
	    var lineHeight=parseFloat(getComputedStyle(alignedTitle).lineHeight); if (!Number.isFinite(lineHeight)) lineHeight=alignedTitle.getBoundingClientRect().height;
	    var expectedTop=alignedTitle.getBoundingClientRect().top-alignedCard.getBoundingClientRect().top+Math.max(0,(lineHeight-20)/2);
	    if (!alignedGutter.style.top || Math.abs(parseFloat(alignedGutter.style.top)-expectedTop)>1) fail('card checkbox is not aligned with the title');
	    for (var cssWait=0;cssWait<80 && getComputedStyle(desktopSelect.closest('[data-card-select-loaded-control]')).display!=='none';cssWait++) await wait(25);
	    if (getComputedStyle(desktopSelect.closest('[data-card-select-loaded-control]')).display!=='none' || getComputedStyle(mobileSelect).display==='none') fail('production responsive styles did not expose mobile selection mode');    var clicks=0; ['a','b','c'].forEach(function(id) { card(id).onclick=function(){clicks++;}; });
	    checkbox('a').click();
	    if (clicks !== 0 || selectedCount() !== '1 selected' || !desktopSelect.indeterminate || desktopSelect.checked) fail('checkbox click activated card or did not update the master checkbox');
	    checkbox('b').dispatchEvent(new MouseEvent('click', {bubbles:true, shiftKey:true}));
	    if (selectedCount() !== '2 selected' || !checkbox('a').checked || !checkbox('b').checked || !desktopSelect.indeterminate) fail('Shift-click range selection failed');
	    document.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true}));
	    if (selectedCount() !== '0 selected' || checkbox('a').checked || desktopSelect.checked || desktopSelect.indeterminate) fail('Escape did not clear selection');
	    desktopSelect.click();
	    if (selectedCount() !== '3 selected' || checkbox('default').checked || !desktopSelect.checked || desktopSelect.indeterminate) fail('master checkbox included an ineligible card or missed eligible cards');
	    var queryForm=document.querySelector('[data-card-query-form]'), search=document.querySelector('[data-card-search]'), secondary=document.querySelector('[data-card-query-secondary]'), selectedActions=document.querySelector('[data-card-selection-actions]');
	    if (queryForm.classList.contains('hidden') || !search || search.offsetParent===null) fail('selection hid the master checkbox or search');
	    if (!secondary.classList.contains('hidden') || selectedActions.classList.contains('hidden')) fail('selection did not replace filters and sort with bulk actions');
	    desktopSelect.click();
	    if (selectedCount()!=='0 selected' || desktopSelect.checked || secondary.classList.contains('hidden') || !selectedActions.classList.contains('hidden')) fail('unchecking the master did not clear selection and restore filters and sort');
	    desktopSelect.click();
	    if (selectedCount()!=='3 selected' || !desktopSelect.checked || !secondary.classList.contains('hidden') || selectedActions.classList.contains('hidden')) fail('rechecking the master did not restore selection actions');
	    var disabled=checkbox('default'), help=document.getElementById(disabled.getAttribute('aria-describedby'));
    if (!disabled.disabled || !help || !help.textContent.trim()) fail('disabled selection control lacks an accessible explanation');
    window.openVibelyNavigate=function(path){window.selectionNavigation=path;};
    var provider=document.querySelector('[name="provider"]'); provider.value='openai'; provider.form.requestSubmit();
    if (Object.keys(window._openVibelyCardSelections.models.ids).length !== 0 || !window.selectionNavigation.includes('provider=openai')) fail('query change did not clear selection state');
    document.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true}));
    checkbox('a').click();
    var root=document.getElementById('models-container');
	    var appended=card('c').cloneNode(true); appended.setAttribute('data-model-id','appended'); appended.setAttribute('data-card-select-id','appended'); appended.querySelector('[data-card-selection-gutter]').remove(); document.querySelector('#models-card-list .grid').appendChild(appended);
	    var fixed=card('c').cloneNode(true); fixed.setAttribute('data-model-id','fixed'); fixed.removeAttribute('data-card-select-id'); fixed.removeAttribute('data-card-select-eligible'); fixed.querySelector('[data-card-selection-gutter]').remove(); document.querySelector('#models-card-list .grid').appendChild(fixed);
	    root._openVibelyInstallSelectionCards();
	    var fixedCheckbox=checkbox('fixed');
	    if (!checkbox('a').checked || !checkbox('appended') || checkbox('appended').checked) fail('loaded-card reconciliation changed selection or missed the new checkbox');
	    if (!fixedCheckbox || !fixedCheckbox.disabled || !fixedCheckbox.title) fail('fixed Personality/Channel-style card did not receive an explained disabled checkbox');    window.addEventListener('sse-card-refresh', function() { root=window.replaceSearchableCardContainer(root, REPLACEMENT_HTML); document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail:{elt:root}})); });
    window.dispatchEvent(new CustomEvent('sse-card-refresh'));
    await wait(25);
    if (selectedCount() !== '0 selected' || !checkbox('e')) fail('HTMX/SSE replacement did not reconcile removed and new cards');
    document.querySelector('[data-card-select-mode]').click(); checkbox('b').click();
    var mobile=document.querySelector('[data-card-mobile-actions]');
    if (!mobile.classList.contains('flex') || selectedCount() !== '1 selected' || getComputedStyle(mobile).position !== 'fixed' || getComputedStyle(mobile).display === 'none') fail('mobile Select mode did not expose fixed actions');
    mobile.querySelector('[data-card-mobile-cancel]').click();
    if (selectedCount() !== '0 selected' || !mobile.classList.contains('hidden')) fail('mobile cancel did not clear mode');
    document.querySelector('[data-card-select-loaded]').click(); document.querySelector('[data-card-delete-selected]').click();
    var dialog=document.querySelector('[data-card-bulk-confirm]');
    if (!dialog.open || !dialog.textContent.includes('3 selected models')) fail('bulk confirmation did not open with the selected count');
    dialog.querySelector('[data-card-bulk-confirm-delete]').click();
    for (var i=0;i<80 && !window.bulkFinished;i++) await wait(25);
    if (!window.bulkFinished) fail('bulk deletion did not finish');
	    for (var j=0;j<80 && document.querySelectorAll('#models-card-list [data-card-select-id]').length!==2;j++) await wait(25);
		    if (document.querySelectorAll('#models-card-list [data-card-select-id]').length !== 2 || document.activeElement !== document.querySelector('[data-card-search]')) fail('bulk refresh was not authoritative or did not restore focus');
		    var replacementMaster=document.querySelector('[data-card-select-loaded]'), replacementFilter=document.querySelector('[data-card-filters-button]'), replacementSecondary=document.querySelector('[data-card-query-secondary]');
		    replacementMaster.click();
		    if (!replacementMaster.checked || !checkbox('survivor').checked || selectedCount()!=='1 selected') fail('replacement master checkbox was not reinitialized after bulk refresh');
		    replacementMaster.click();
		    if (replacementMaster.checked || checkbox('survivor').checked || selectedCount()!=='0 selected' || replacementSecondary.classList.contains('hidden')) fail('replacement master checkbox could not clear selection after bulk refresh');
		    replacementFilter.click();
		    if (!replacementFilter.closest('.dropdown').classList.contains('dropdown-open') || replacementFilter.getAttribute('aria-expanded')!=='true') fail('replacement toolbar controls were not reinitialized after bulk refresh');
		    replacementFilter.click();
		    history.replaceState({}, '', '/models?provider=anthropic&sort=provider');	    var authoritativeMutation=window.cardCollectionActionURL(document.getElementById('models-container'), '/models/example');
	    if (!authoritativeMutation.includes('provider=openai') || authoritativeMutation.includes('anthropic') || authoritativeMutation.includes('sort=')) fail('mutation URL did not use server-rendered root state');
	    history.replaceState({}, '', '/skills?enabled=true');    var parsed=new DOMParser().parseFromString(UNMANAGED_SKILLS_HTML, 'text/html'), skillsRoot=parsed.getElementById('skills-container');
    document.querySelector('main').appendChild(document.importNode(skillsRoot, true));
    skillsRoot=document.getElementById('skills-container');
    window.refreshCardListToolbars(skillsRoot);
	    if (!skillsRoot.querySelector('[data-card-filter-chip="enabled"]') || skillsRoot.querySelector('[data-card-selection-actions]')) fail('unmanaged Skills active-filter toolbar did not initialize safely');
	    history.replaceState({}, '', '/alerts?project_id=project&source=' + 's'.repeat(101));
	    var alertParsed=new DOMParser().parseFromString(REJECTED_ALERTS_HTML, 'text/html'), alertsRoot=alertParsed.getElementById('alerts-container');
	    document.querySelector('main').appendChild(document.importNode(alertsRoot, true));
	    alertsRoot=document.getElementById('alerts-container');
	    window.refreshCardListToolbars(alertsRoot);
	    if (alertsRoot.querySelector('[data-card-filter-chip="source"]') || alertsRoot.querySelector('[name="source"]').value) fail('raw rejected Alert source was reactivated by client hydration');
	    if ((alertsRoot.getAttribute('data-card-pagination-url') || '').includes('source=')) fail('rejected Alert source contaminated authoritative root state');
		    verifyMixedCollection('personality'); verifyMixedCollection('channels');
		    var emptyChannelsParsed=new DOMParser().parseFromString(EMPTY_CHANNELS_HTML, 'text/html'), emptyChannelsRoot=emptyChannelsParsed.getElementById('channels-container');
		    emptyChannelsRoot=document.querySelector('main').appendChild(document.importNode(emptyChannelsRoot, true)); window.refreshCardListToolbars(emptyChannelsRoot);
		    var emptyState=emptyChannelsRoot.querySelector('[data-search-empty-state]'), emptyMaster=emptyChannelsRoot.querySelector('[data-card-select-loaded]');
		    if (!emptyState || emptyState.querySelector('[data-card-selection-gutter]') || emptyState.classList.contains('md:pl-8')) fail('actual Channels empty state received selection UI or padding');
		    if (!emptyMaster || !emptyMaster.disabled) fail('empty Channels master checkbox was not disabled');
		    result('pass','selection interactions passed');
	  }
	  var REPLACEMENT_HTML=` + string(replacementJSON) + `;
	  var UNMANAGED_SKILLS_HTML=` + string(unmanagedSkillsJSON) + `;
	  var REJECTED_ALERTS_HTML=` + string(rejectedAlertsJSON) + `;
	  var EMPTY_CHANNELS_HTML=` + string(emptyChannelsJSON) + `;  window.addEventListener('load', function(){run().catch(function(error){result('fail', String(error&&error.stack||error));});});
})();
</script>`
	page = strings.Replace(page, "</body>", runner+"</body>", 1)

	var deletes atomic.Int32
	var payloadMu sync.Mutex
	var deletedIDs []string
	type browserResult struct {
		status  string
		message string
	}
	browserResults := make(chan browserResult, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/browser-result" && r.Method == http.MethodPost:
			message, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case browserResults <- browserResult{status: r.Header.Get("X-Browser-Status"), message: string(message)}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/models/bulk" && r.Method == http.MethodDelete:
			var request bulkIDsRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			payloadMu.Lock()
			deletedIDs = append([]string(nil), request.IDs...)
			payloadMu.Unlock()
			deletes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deleted":3}`))
		case r.URL.Path == "/models" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(renderWithState(finalCards, pages.CardListState{Filters: map[string]string{"provider": "openai"}})))
		case r.URL.Path == "/bulk-finished":
			if deletes.Load() > 0 {
				_, _ = w.Write([]byte("true"))
			} else {
				_, _ = w.Write([]byte("false"))
			}
		default:
			_, _ = w.Write([]byte(page))
		}
	}))
	defer fixture.Close()
	page = strings.Replace(page, "for (var i=0;i<80 && !window.bulkFinished;i++) await wait(25);", "for (var i=0;i<80 && !window.bulkFinished;i++){ window.bulkFinished=(await fetch('/bulk-finished').then(function(r){return r.text();}))==='true'; if(!window.bulkFinished) await wait(25); }", 1)

	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-extensions", "--no-first-run", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"), "--window-size=390,844", fixture.URL+"?provider=openai")
	var chromeOutput bytes.Buffer
	cmd.Stdout = &chromeOutput
	cmd.Stderr = &chromeOutput
	require.NoError(t, startHandlerBrowserProcess(cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()
	var browser browserResult
	select {
	case browser = <-browserResults:
		stopHandlerBrowserProcess(cmd)
		stopped = true
	case <-time.After(45 * time.Second):
		t.Fatalf("selection browser regression timed out: %s", chromeOutput.String())
	}
	require.Equal(t, "pass", browser.status, "selection browser regression failed: %s\nChrome output: %s", browser.message, chromeOutput.String())
	require.Equal(t, int32(1), deletes.Load())
	payloadMu.Lock()
	require.ElementsMatch(t, []string{"b", "c", "e"}, deletedIDs)
	payloadMu.Unlock()
}

func TestCollectionCardToolbars(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		pageKey  string
		wantSort bool
	}{
		{name: "alerts", path: "/alerts?project_id=default", pageKey: "alerts", wantSort: true},
		{name: "automations", path: "/automations?project_id=default", pageKey: "automations", wantSort: true},
		{name: "agents", path: "/agents?project_id=default", pageKey: "agents", wantSort: true},
		{name: "skills", path: "/skills?project_id=default", pageKey: "skills", wantSort: true},
		{name: "models", path: "/models?project_id=default", pageKey: "models", wantSort: true},
		{name: "channels", path: "/channels?project_id=default", pageKey: "channels", wantSort: false},
		{name: "personality", path: "/personality?project_id=default", pageKey: "personality", wantSort: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			h.SetAgentRepo(repository.NewAgentRepo(db))
			if tt.name == "automations" {
				h.SetAutomationServices(service.NewAutomationGraphService(repository.NewAutomationRepo(db)), nil)
			}
			h.SetAgentSkillRoot(t.TempDir())
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range []string{
				`data-card-list-toolbar="` + tt.pageKey + `"`,
				`type="checkbox" class="checkbox checkbox-sm" data-card-select-loaded`,
				`aria-label="Select all loaded ` + tt.pageKey + `"`,
				`data-card-filters-button`,
				`data-card-selection-actions`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("expected %s toolbar to contain %q", tt.name, want)
				}
			}
			if strings.Contains(body, ">Select loaded<") {
				t.Errorf("expected %s toolbar to use a master checkbox without visible Select loaded text", tt.name)
			}
			if tt.pageKey == "alerts" && !strings.Contains(body, `data-card-mark-read-selected`) {
				t.Errorf("expected alerts toolbar to offer Mark as read for selected alerts")
			}
			if tt.pageKey == "channels" && !strings.Contains(body, `data-card-bulk-url="/channels/bulk"`) {
				t.Errorf("expected channels toolbar to use the atomic bulk endpoint")
			}
			if got := strings.Contains(body, `id="`+tt.pageKey+`-card-sort"`); got != tt.wantSort {
				t.Errorf("sort presence = %v, want %v", got, tt.wantSort)
			}
		})
	}
}

func TestAlertsFiltersDecisionStateOnlyInsidePopover(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=default&decision_state=pending", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `aria-label="Filter by decision state"`) {
		t.Fatal("standalone decision-state selector must not be rendered")
	}
	if !strings.Contains(body, `data-card-filter-group="decision_state"`) {
		t.Fatal("decision state must be rendered as a Filters popover group")
	}
	if !strings.Contains(body, `data-card-filter-chip="decision_state"`) {
		t.Fatal("active decision state must be rendered as a removable chip")
	}
}

func TestCardSearch_PersonalityPage(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/personality?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "personality", "Search personalities...")
}

func TestCardSearch_PersonalityPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/personality?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "personality", "Search personalities...")
	// Partial should not contain full layout
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ChannelsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "channels", "Search channels...")
}

func TestCardSearch_ChannelsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "channels", "Search channels...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ChannelsMutationsRefreshContainerWithoutFullPageReload(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="channels-container"`,
		`data-card-search="channels"`,
		`hx-get="/channels?project_id=default"`,
		`hx-trigger="channels-refresh from:body"`,
		`hx-target="this"`,
		`hx-swap="outerHTML"`,
		`window.refreshCardSearches = initAllCardSearches`,
		`var webhookAvailableAgents = [];`,
		`var selectedWebhookAgentIDs = [];`,
		`var activeWebhookSection = 'config';`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Channels searchable refresh contract to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`let webhookAvailableAgents = [];`,
		`let selectedWebhookAgentIDs = [];`,
		`let activeWebhookSection = 'config';`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("expected Channels refreshed script to avoid re-execution-unsafe top-level declaration %q", forbidden)
		}
	}
}

func TestCardSearch_AgentsPage(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	// Create an agent so there's a card to search
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		Name:        "TestSearchAgent",
		Description: "A test agent for search",
		Model:       "inherit",
	}
	err := agentRepo.Create(context.Background(), agent)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "agents", "Search agents...")
	// Verify search text attribute includes the agent name
	if !strings.Contains(body, "data-search-text") {
		t.Error("expected data-search-text attribute on agent cards")
	}
	if !strings.Contains(body, "TestSearchAgent") {
		t.Error("expected agent name in page body")
	}
}

func TestCardSearch_AgentsPartial(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "agents", "Search agents...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ModelsPage(t *testing.T) {
	h, e, llmRepo := setupTestHandler(t)
	_ = h
	createAgent(t, llmRepo, func(c *models.LLMConfig) {
		c.Name = "TestModelSearch"
		c.Model = "claude-sonnet-4-5"
	})

	req := httptest.NewRequest(http.MethodGet, "/models?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "models", "Search models...")
	if !strings.Contains(body, "data-search-card") {
		t.Error("expected data-search-card attribute on model cards")
	}
}

func TestCardSearch_ModelsPartial(t *testing.T) {
	h, e, llmRepo := setupTestHandler(t)
	_ = h
	createAgent(t, llmRepo, func(c *models.LLMConfig) {
		c.Name = "TestModelSearch"
		c.Model = "claude-sonnet-4-5"
	})

	req := httptest.NewRequest(http.MethodGet, "/models?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "models", "Search models...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_SkillsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "skills", "Search skills...")
	if !strings.Contains(body, `data-nav-base="/skills"`) {
		t.Error("expected Skills sidebar nav item")
	}
}

func TestCardSearch_SkillsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "skills", "Search skills...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_SkillsManualRefreshReappliesActiveSearch(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`window.refreshCardSearches = initAllCardSearches`,
		`window.replaceSearchableCardContainer = replaceSearchableCardContainer`,
		`window.cardPaginationRefreshURL = cardPaginationRefreshURL`,
		`window.destroyCardPagination = destroyCardPagination`,
		`destroyCardPagination(container, true);`,
		`document.body.addEventListener('htmx:configRequest'`,
		`detail.path = cardPaginationRefreshURL(root, detail.path)`,
		`rememberReplacementSnapshot(state);`,
		`nextPage: Math.max(1, Math.ceil(initialCardCount / pageSize))`,
		`data-skill-scroll-anchor`,
		`function captureSkillsViewportState(root, activeHandle)`,
		`function restoreSkillsViewportState(root, saved)`,
		`function replaceSkillsContainer(html, options)`,
		`nextContainer = window.replaceSearchableCardContainer('#skills-container', html);`,
		`state.preparedSwap = captureSkillsViewportState(document.getElementById('skills-container'), deleteSkillHandle);`,
		`swap: 'outerHTML show:none'`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Skills page search refresh contract to contain %q", want)
		}
	}
	if got := strings.Count(body, `replaceSkillsContainer(html);`); got != 4 {
		t.Fatalf("expected all four manual Skills container refresh paths to use shared search-aware replacement, got %d", got)
	}
}

func TestCardSearch_AgentsManualDeleteRefreshReappliesActiveSearch(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-card-search="agents"`,
		`window.replaceSearchableCardContainer('#agents-container', html)`,
		`cardPaginationRefreshURL`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Agents delete search refresh contract to contain %q", want)
		}
	}
}

func TestCardSearch_AlertsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	createAlert(t, h, project.ID, "SearchableAlert")

	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "alerts", "Search alerts...")
	if !strings.Contains(body, "SearchableAlert") {
		t.Error("expected alert title in page body")
	}
}

func TestCardSearch_AlertsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	createAlert(t, h, project.ID, "SearchableAlert")

	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "alerts", "Search alerts...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

// assertSearch checks that the response body contains the search input with
// the expected page key and placeholder, plus the no-results element.
func assertSearch(t *testing.T, body, pageKey, placeholder string) {
	t.Helper()
	if !strings.Contains(body, `data-card-search="`+pageKey+`"`) {
		t.Errorf("expected data-card-search=%q attribute in body", pageKey)
	}
	if !strings.Contains(body, `placeholder="`+placeholder+`"`) {
		t.Errorf("expected placeholder=%q in body", placeholder)
	}
	if !strings.Contains(body, `data-search-container`) {
		t.Errorf("expected data-search-container attribute in body")
	}
	if !strings.Contains(body, `data-search-no-results`) {
		t.Errorf("expected data-search-no-results element in body")
	}
}
