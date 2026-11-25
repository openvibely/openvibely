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

	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestSharedProjectChatPickerInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	const repo = "https://github.com/acme/foo.git"
	localProject := createProject(t, h, "Foo")
	setRepoURL(t, h, localProject.ID, repo)
	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, repo)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	remoteCopy := remoteid.ID(savedServerID(t, h), remoteProject.ID)
	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	shared := h.sharedProjects(ctx, h.remoteServerGroups(ctx))[0]

	runner := `<script>
 (async function(){
 function wait(predicate){return new Promise((resolve,reject)=>{let deadline=Date.now()+15000;(function poll(){if(predicate() && !document.querySelector('.htmx-request, .htmx-settling, .htmx-swapping'))return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,30)})();})}
 function assert(ok,msg){if(!ok)throw Error(msg)}
 try {
 await wait(()=>window.openVibelyNavigate && document.getElementById('shared-chat-form') && document.getElementById('shared-chat-form')['htmx-internal-data']);
 assert(document.getElementById('project-selector').value==='` + shared.ID + `','shared project not selected');
 const picker=document.getElementById('shared-chat-form-project-select');
 assert(picker && picker.textContent.trim()==='Choose a project','picker missing');
 const form=document.getElementById('shared-chat-form');
 const message=document.getElementById('shared-chat-input');
 message.value='hello copy';message.dispatchEvent(new Event('input',{bubbles:true}));
 const sent=[];
 form.addEventListener('htmx:beforeRequest',event=>{if(event.detail.elt!==form)return;event.preventDefault();sent.push(event.detail.requestConfig.path);});
 message.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 const option=id=>document.querySelector('#chat-custom-select-panel li[data-value="'+id+'"]');
 await wait(()=>option('` + remoteCopy + `'));
 assert(sent.length===0,'sent before choosing a copy');
 assert(option('` + localProject.ID + `').textContent.includes('Foo · This computer'),'local copy label');
 assert(option('` + remoteCopy + `').textContent.includes('Foo · Box'),'remote copy label');
 option('` + remoteCopy + `').click();
 await wait(()=>sent.length===1);
 assert(sent[0]==='/chat/send?project_id=` + url.QueryEscape(remoteCopy) + `','wrong send URL: '+sent[0]);
 assert(picker.textContent.trim()==='Foo · Box','picker label');
 const xhr={responseText:'<div>ok</div>',status:200,getResponseHeader:()=>null};
 form.dispatchEvent(new CustomEvent('htmx:afterRequest',{bubbles:true,detail:{elt:form,xhr:xhr,successful:true,requestConfig:{}}}));
 await wait(()=>location.pathname==='/chat' && new URLSearchParams(location.search).get('project_id')==='` + remoteCopy + `' && document.getElementById('chat-page-root'));
 assert(document.getElementById('project-selector').value==='` + remoteCopy + `','did not switch to the copy');
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
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/chat?project_id="+url.QueryEscape(shared.ID))
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(50 * time.Second):
		t.Fatal("browser timed out")
	}
}
