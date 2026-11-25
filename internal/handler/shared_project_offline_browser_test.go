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

	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestSharedTabDropsOfflineMachineTasksInChrome(t *testing.T) {
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
	localProject := createProject(t, h, "Foo")
	setRepoURL(t, h, localProject.ID, repo)
	createTask(t, h, localProject.ID, "Local shared task")
	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, repo)
	createTask(t, owner, remoteProject.ID, "Remote shared task")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	sharedID := h.sharedProjects(ctx, h.remoteServerGroups(ctx))[0].ID

	runner := `<script>
 (async function(){
 if (window.__offlineRunner) return; window.__offlineRunner = true;
 function wait(predicate,ms){return new Promise((resolve,reject)=>{let deadline=Date.now()+(ms||15000);(function poll(){if(predicate())return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,50)})();})}
 const text=()=>document.body.innerText;
 try {
 await wait(()=>text().includes('Remote shared task') && text().includes('Local shared task'));
 const marker=document.createElement('span');marker.id='no-reload-marker';document.body.appendChild(marker);
 await fetch('/box?offline=1',{method:'POST'});
 await wait(()=>!text().includes('Remote shared task'),25000);
 if(!text().includes('Local shared task')) throw Error('local task gone');
 if(!document.getElementById('no-reload-marker')) throw Error('page reloaded');
 await fetch('/box?offline=0',{method:'POST'});
 await wait(()=>text().includes('Remote shared task'),40000);
 if(!document.getElementById('no-reload-marker')) throw Error('page reloaded');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)+' url='+location.href})}
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
		case "/box":
			box.setOffline(r.URL.Query().Get("offline") == "1")
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
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/tasks?project_id="+url.QueryEscape(sharedID))
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(90 * time.Second):
		t.Fatal("browser timed out")
	}
}
