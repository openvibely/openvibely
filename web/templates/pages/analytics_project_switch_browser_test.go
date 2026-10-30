package pages

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_AnalyticsProjectSwitchRefreshesTaskListWhileProviderUsageLoads(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "a", Name: "Project A"}, {ID: "b", Name: "Project B"}}
	analyticsA := renderAnalyticsProjectSwitchShell(t, projects, &projects[0])
	analyticsB, err := renderAnalyticsComponent(&projects[1])
	if err != nil {
		t.Fatal(err)
	}

	releaseNavigation := make(chan struct{})
	var releaseOnce sync.Once
	unblockNavigation := func() { releaseOnce.Do(func() { close(releaseNavigation) }) }
	defer unblockNavigation()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			switch r.URL.Path {
			case "/tasks/sidebar":
				projectID := r.URL.Query().Get("project_id")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, `<div data-task-project="`+projectID+`">`+strings.ToUpper(projectID)+` task</div>`)
				return
			case "/analytics":
				if r.URL.Query().Get("project_id") == "b" {
					select {
					case <-releaseNavigation:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					_, _ = w.Write(analyticsB)
					return
				}
			}
			if r.Header.Get("HX-Request") == "true" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(analyticsA)
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/analytics?project_id=a&view=usage", "analytics-project-switch", func(browser *composerFocusCDP) {
		browser.waitFor("initial project provider usage is pending", `String(!!(window.pendingProvider && window.pendingProvider.a && window.pendingProvider.a.signal && !window.pendingProvider.a.signal.aborted))`, "true")
		browser.evaluate(`window.initialProviderA=window.pendingProvider.a;'ok';`)
		browser.waitFor("initial task list loaded", `String(document.querySelector('#task-list-groups [data-task-project]') ? document.querySelector('#task-list-groups [data-task-project]').dataset.taskProject : '')`, "a")
		browser.evaluate(`var selector=document.getElementById('project-selector');selector.value='b';selector.dispatchEvent(new Event('change',{bubbles:true}));'ok';`)
		browser.waitFor("task list targets the selected project immediately", `document.getElementById('task-list-groups').getAttribute('hx-get')`, "/tasks/sidebar?project_id=b")
		browser.waitFor("old provider request was cancelled", `String(!!(window.initialProviderA && window.initialProviderA.signal && window.initialProviderA.signal.aborted))`, "true")
		browser.waitFor("new project task list loads before Analytics navigation", `String(document.querySelector('#task-list-groups [data-task-project]') ? document.querySelector('#task-list-groups [data-task-project]').dataset.taskProject : '')`, "b")
		if got := browser.evaluate(`String(!!document.getElementById('analytics-data') && document.getElementById('analytics-data').dataset.projectId==='a')`); got != "true" {
			t.Fatalf("Analytics navigation unexpectedly settled before the delayed response: %s", got)
		}

		unblockNavigation()
		browser.waitFor("new project Analytics content loaded", `document.getElementById('analytics-data') && document.getElementById('analytics-data').dataset.projectId`, "b")
		browser.waitFor("new project provider request is pending", `String(!!(window.pendingProvider && window.pendingProvider.b && window.pendingProvider.b.signal && !window.pendingProvider.b.signal.aborted))`, "true")
		browser.evaluateAwait(`(async function(){window.initialProviderA.resolve(window.providerResponse({account_limits:[{provider:'openai',plan_type:'Stale Project A',limits:[]}]}));await new Promise(function(resolve){setTimeout(resolve,100);});return String(document.getElementById('usage-account-b-account') && document.getElementById('usage-account-b-account').hasAttribute('aria-busy') && !document.getElementById('accountUsageCards').textContent.includes('Stale Project A'));})()`)
		browser.waitFor("stale provider result cannot update the new project", `String(document.getElementById('usage-account-b-account') && document.getElementById('usage-account-b-account').hasAttribute('aria-busy') && !document.getElementById('accountUsageCards').textContent.includes('Stale Project A'))`, "true")
	})
}

func TestBrowserFunctional_AnalyticsProviderUsageDoesNotBlockTaskListNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "a", Name: "Project A"}}
	analytics := renderAnalyticsProjectSwitchShell(t, projects, &projects[0])

	providerRequestStarted := make(chan struct{}, 32)
	providerRequestCanceled := make(chan struct{}, 32)
	navigationRequests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/api/analytics/usage" && r.URL.Query().Get("projection") == "account_limits" {
			providerRequestStarted <- struct{}{}
			<-r.Context().Done()
			providerRequestCanceled <- struct{}{}
			return
		}
		if r.URL.Path == "/tasks/sidebar" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<a data-task-list-row="task-a" href="/tasks/task-a?from=tasks" hx-get="/tasks/task-a?from=tasks" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Task A</a>`)
			return
		}
		if r.URL.Path == "/tasks/task-a" {
			navigationRequests <- r.Header.Get("HX-Request")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<div data-navigation-destination="task-a">Task A detail</div>`)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(analytics)
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/analytics?project_id=a&view=usage&provider_network=1&provider_count=3", "analytics-provider-usage-task-navigation", func(browser *composerFocusCDP) {
		browser.waitFor("provider usage cards rendered", `String(document.querySelectorAll('#accountUsageCards > .card').length)`, "3")
		browser.waitFor("three provider card requests are pending", `String(window.pendingProviderRequests&&window.pendingProviderRequests.length)`, "3")
		if got := browser.evaluate(`JSON.stringify(window.pendingProviderRequests.map(function(request){return !!request.signal&&!request.signal.aborted;}))`); got != `[true,true,true]` {
			t.Fatalf("provider usage request signals = %s, want three pending requests", got)
		}
		browser.waitFor("task list has a clickable task", `String(!!document.querySelector('#task-list-groups [data-task-list-row="task-a"]'))`, "true")
		for i := 0; i < 3; i++ {
			select {
			case <-providerRequestStarted:
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d provider usage requests reached the server; expected three card requests", i)
			}
		}
		select {
		case <-providerRequestCanceled:
			t.Fatal("provider usage request was canceled before navigation began")
		default:
		}

		browser.click(`#task-list-groups [data-task-list-row="task-a"]`)
		select {
		case header := <-navigationRequests:
			if header != "true" {
				t.Fatalf("task detail navigation HX-Request header = %q, want true", header)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("task detail request was blocked while provider usage requests were pending")
		}
		browser.waitFor("task detail loads and clears its loading indicator", `String(!!document.querySelector('#main-content [data-navigation-destination="task-a"]') && !document.getElementById('main-content').hasAttribute('data-page-loading'))`, "true")
		browser.waitFor("all provider requests are aborted on navigation", `String(window.pendingProviderRequests.every(function(request){return request.signal.aborted;}))`, "true")
		for i := 0; i < 3; i++ {
			select {
			case <-providerRequestCanceled:
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d provider requests were canceled by task navigation", i)
			}
		}
	})
}

func TestBrowserFunctional_AnalyticsProviderUsageDoesNotBlockSidebarNavigation(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "a", Name: "Project A"}}
	analytics := renderAnalyticsProjectSwitchShell(t, projects, &projects[0])

	navigationRequests := make(chan string, 1)
	providerRequestStarted := make(chan struct{}, 1)
	providerRequestCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/api/analytics/usage" && r.URL.Query().Get("projection") == "account_limits" {
			providerRequestStarted <- struct{}{}
			<-r.Context().Done()
			providerRequestCanceled <- struct{}{}
			return
		}
		if r.URL.Path == "/tasks/sidebar" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<div>Project A task list</div>`)
			return
		}
		if r.URL.Path == "/tasks" {
			navigationRequests <- r.Header.Get("HX-Request")
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, `<div data-navigation-destination="tasks">Tasks page</div>`)
				return
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(analytics)
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL+"/analytics?project_id=a&view=usage&provider_network=1", "analytics-provider-usage-navigation", func(browser *composerFocusCDP) {
		browser.waitFor("provider usage request is pending", `String(!!(window.pendingProvider && window.pendingProvider.a && window.pendingProvider.a.signal && !window.pendingProvider.a.signal.aborted))`, "true")
		select {
		case <-providerRequestStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("provider usage request did not reach the server")
		}
		browser.click(`[data-nav-base="/tasks"]`)
		browser.waitFor("provider request is cancelled on navigation", `sessionStorage.getItem('provider-aborted')`, "yes")
		browser.waitFor("sidebar destination loads before provider response", `String(!!document.querySelector('#main-content [data-navigation-destination="tasks"]'))`, "true")
		if header := <-navigationRequests; header != "true" {
			t.Fatalf("tasks navigation HX-Request header = %q, want true", header)
		}
		select {
		case <-providerRequestCanceled:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight provider usage request was not canceled by navigation")
		}
	})
}

func renderAnalyticsProjectSwitchShell(t *testing.T, projects []models.Project, project *models.Project) []byte {
	t.Helper()
	component, err := renderAnalyticsComponent(project)
	if err != nil {
		t.Fatal(err)
	}
	child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, analyticsProjectSwitchFetchFixture); err != nil {
			return err
		}
		_, err := w.Write(component)
		return err
	})
	var rendered bytes.Buffer
	if err := layout.Base("Analytics", projects, project.ID).Render(templ.WithChildren(context.Background(), child), &rendered); err != nil {
		t.Fatal(err)
	}
	return rendered.Bytes()
}

func renderAnalyticsComponent(project *models.Project) ([]byte, error) {
	var rendered bytes.Buffer
	err := AnalyticsContent(project).Render(context.Background(), &rendered)
	return rendered.Bytes(), err
}

const analyticsProjectSwitchFetchFixture = `<script>
window.Chart=function(ctx,config){this.canvas=ctx.canvas;this.data=config.data||{};this.options=config.options||{};this.update=function(){};this.resize=function(){};this.destroy=function(){};};
window.pendingProvider={};
window.pendingProviderRequests=[];
window.enableProviderNetwork=new URLSearchParams(location.search).get('provider_network')==='1';
window.providerResponse=function(data){return {ok:true,status:200,json:function(){return Promise.resolve(data);}};};
window.nativeFetch=window.fetch.bind(window);
window.fetch=function(url,options){
 var parsed=new URL(url,location.href),path=parsed.pathname,params=parsed.searchParams,project=params.get('project_id')||'a';
 if(path==='/api/analytics/task-run-activity')return Promise.resolve(providerResponse({models:[]}));
 if(path==='/api/analytics/usage'&&params.get('projection')==='accounts'){
  var count=Number(new URLSearchParams(location.search).get('provider_count'))||1;
  var accounts=Array.from({length:count},function(_,index){return {key:project+'-account'+(count===1?'':'-'+index),provider:'openai'};});
  return Promise.resolve(providerResponse({accounts:accounts}));
 }
 if(path==='/api/analytics/usage'&&params.get('projection')==='account_limits'){
  var request={resolve:function(){},signal:options&&options.signal};
  pendingProviderRequests.push(request);
  if(!pendingProvider[project])pendingProvider[project]=request;
  if(request.signal)request.signal.addEventListener('abort',function(){sessionStorage.setItem('provider-aborted','yes');},{once:true});
  if(enableProviderNetwork)return nativeFetch(url,options);
  return new Promise(function(resolve,reject){
   request.resolve=resolve;
   if(request.signal){
    var abort=function(){reject(new DOMException('Provider request aborted','AbortError'));};
    if(request.signal.aborted)abort();else request.signal.addEventListener('abort',abort,{once:true});
   }
  });
 }
 if(path==='/api/analytics/usage')return Promise.resolve(providerResponse({totals:{},usage_rate:[],usage_rate_by_model:[],model_breakdown:[]}));
 return Promise.resolve(providerResponse({}));
};
sessionStorage.setItem('openvibely.project-location.b','/analytics?project_id=b&view=usage');
</script>`
