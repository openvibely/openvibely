package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestOneCopyRepoUsesOneTabInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e := setupHubHandler(t)
	foo := createProject(t, h, "Foo")
	setRepoURL(t, h, foo.ID, "https://github.com/acme/foo.git")
	other := createProject(t, h, "Other")
	tab := models.SharedProjectID("github.com/acme/foo")
	// An old pin saved under the copy's own ID opens the repository's tab.
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferencePinnedProjectIDsKey, `["`+foo.ID+`","`+other.ID+`"]`))

	runner := `<script>
 (async function(){
 if (window.__repoTabRunner) return; window.__repoTabRunner = true;
 function wait(predicate){return new Promise((resolve,reject)=>{let deadline=Date.now()+15000;(function poll(){if(predicate() && !document.querySelector('.htmx-request'))return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,30)})();}).catch(e=>{throw Error(e.message+' tabs='+tabs())})}
 function assert(ok,msg){if(!ok)throw Error(msg)}
 const tabs=()=>Array.from(document.querySelectorAll('#desktop-project-tabs [data-project-tab]')).map(t=>t.dataset.projectTab);
 const selected=()=>{const t=document.querySelector('#desktop-project-tabs [aria-selected="true"]');return t&&t.dataset.projectTab;};
 const project=()=>new URLSearchParams(location.search).get('project_id');
 try {
 await wait(()=>document.readyState==='complete' && window.openVibelyNavigate && tabs().length===2);
 assert(JSON.stringify(tabs())===JSON.stringify(['` + tab + `','` + other.ID + `']),'tabs: '+tabs());
 assert(selected()==='` + tab + `','Foo tab not selected');
 document.querySelector('[data-project-tab="` + other.ID + `"]').click();
 await wait(()=>project()==='` + other.ID + `');
 document.querySelector('[data-project-tab="` + tab + `"]').click();
 await wait(()=>project()==='` + foo.ID + `');
 assert(selected()==='` + tab + `','Foo tab not selected after switching back');
 assert(tabs().length===2,'a second Foo tab opened: '+tabs());
 const plus=Array.from(document.querySelectorAll('[data-searchable-selector-name="Foo"]'));
 assert(plus.length===1,'+ lists Foo '+plus.length+' times');
 plus[0].click();
 await wait(()=>project()==='` + foo.ID + `');
 assert(tabs().length===2,'choosing Foo in + opened a second tab: '+tabs());
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)+' url='+location.href})}
 })();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(r))
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/tasks?project_id="+foo.ID)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(50 * time.Second):
		t.Fatal("browser timed out")
	}
}

func TestOpenPageFollowsRemoteServerInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e := setupHubHandler(t)
	backend, _, owner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	const repo = "https://github.com/acme/foo.git"
	local := createProject(t, h, "Foo")
	setRepoURL(t, h, local.ID, repo)
	remoteFoo := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteFoo.ID, repo)
	boxOnly := createProject(t, owner, "Bar")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	tab := models.SharedProjectID("github.com/acme/foo")
	bar := remoteid.ID(savedServerID(t, h), boxOnly.ID)
	require.NoError(t, h.settingsRepo.Set(ctx, uiPreferencePinnedProjectIDsKey, `["`+tab+`","`+bar+`"]`))

	runner := `<script>
 (async function(){
 if (window.__followRunner) return; window.__followRunner = true;
 function wait(predicate){return new Promise((resolve,reject)=>{let deadline=Date.now()+25000;(function poll(){if(predicate())return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,50)})();}).catch(e=>{throw Error(e.message+' tabs='+tabs()+' url='+location.href)})}
 function assert(ok,msg){if(!ok)throw Error(msg)}
 const tabs=()=>Array.from(document.querySelectorAll('#desktop-project-tabs [data-project-tab]')).map(t=>t.dataset.projectTab);
 const project=()=>new URLSearchParams(location.search).get('project_id');
 const plus=id=>document.querySelectorAll('#project-selector-dialog [data-searchable-selector-id="'+id+'"]').length;
 const step=s=>fetch('/browser-step',{method:'POST',body:s});
 try {
 await wait(()=>document.readyState==='complete' && window.openVibelyNavigate && tabs().length===2);
 assert(project()==='` + tab + `','combined page expected');
 assert(plus('` + bar + `')===1,'+ lists Box Bar');
 await step('offline');
 await wait(()=>project()==='` + local.ID + `');
 await wait(()=>JSON.stringify(tabs())===JSON.stringify(['` + tab + `']) && plus('` + bar + `')===0);
 assert(window.openVibelySharedProjects['` + tab + `'].length===1,'shared map not replaced');
 await step('online');
 await wait(()=>project()==='` + tab + `');
 await wait(()=>JSON.stringify(tabs())===JSON.stringify(['` + tab + `','` + bar + `']) && plus('` + bar + `')===1);
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}
 })();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/browser-result":
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/browser-step":
			body, _ := io.ReadAll(r.Body)
			box.setOffline(string(body) == "offline")
			w.WriteHeader(http.StatusNoContent)
			return
		case "/events/ws":
			e.ServeHTTP(w, signedIn(r))
			return
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(r))
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/tasks?project_id="+tab)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(80 * time.Second):
		t.Fatal("browser timed out")
	}
}

func TestRemoteTextLinksGetServerOnClickInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e := setupHubHandler(t)
	foo := createProject(t, h, "Foo")
	const hex = "0123456789abcdef0123456789abcdef"
	remote := remoteid.ID("fedcba9876543210", hex)
	runner := `<script>
 (async function(){
 if (window.__remoteLinkRunner) return; window.__remoteLinkRunner = true;
 function assert(ok,msg){if(!ok)throw Error(msg)}
 try {
 await new Promise(r=>{if(document.readyState==='complete')r();else addEventListener('load',r)});
 const box=document.createElement('div');
 box.innerHTML='<div data-task-id="` + remote + `"><a id="r1" href="/tasks/` + hex + `?project_id=` + hex + `#out">t</a><a id="r2" href="https://example.com/tasks/` + hex + `">x</a></div><div><a id="l1" href="/tasks/` + hex + `">l</a></div>';
 document.body.appendChild(box);
 box.addEventListener('click',e=>e.preventDefault());
 for (const id of ['r1','r2','l1']) document.getElementById(id).click();
 assert(document.getElementById('r1').getAttribute('href')==='/tasks/` + remote + `?project_id=` + remote + `#out','remote link: '+document.getElementById('r1').getAttribute('href'));
 assert(document.getElementById('r2').getAttribute('href')==='https://example.com/tasks/` + hex + `','external link changed');
 assert(document.getElementById('l1').getAttribute('href')==='/tasks/` + hex + `','local link changed');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}
 })();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(r))
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/tasks?project_id="+foo.ID)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(50 * time.Second):
		t.Fatal("browser timed out")
	}
}
