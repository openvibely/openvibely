package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestViewerPermissionToastsInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(stores.ProjectAccess))
	org := service.NewOrganizationService(stores.Organizations, "secret")
	h.SetOrganizationService(org)
	project := createProject(t, h, "Viewer permissions")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := org.Accept(ctx, token, "browser-member", "long-password-value", "")
	require.NoError(t, err)

	require.NoError(t, stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, member.UserID, "viewer"))
	task := createTask(t, h, project.ID, "Read only task", func(task *models.Task) { task.Category = models.CategoryBacklog })
	result := make(chan string, 1)
	runner := `<script>(async function(){
 const wait=(p)=>new Promise((resolve,reject)=>{const end=Date.now()+10000;(function poll(){if(p())return resolve();if(Date.now()>end)return reject(Error('timeout'));setTimeout(poll,30)})();});
 try {
  const target=document.createElement('div');target.id='permission-test-target';target.textContent='Keep existing thread';document.body.appendChild(target);
  const form=document.createElement('form');form.innerHTML='<textarea name="message">Keep my draft</textarea>';document.body.appendChild(form);
  for(const path of ['/tasks?project_id=` + project.ID + `','/tasks/` + task.ID + `/thread?project_id=` + project.ID + `']) {
   let status=0;
   form.setAttribute('hx-post',path);form.setAttribute('hx-target','#permission-test-target');form.setAttribute('hx-swap','innerHTML');htmx.process(form);
   form.addEventListener('htmx:afterRequest',e=>{status=e.detail.xhr.status},{once:true});
   form.requestSubmit();await wait(()=>status);
   if(status!==403)throw Error('expected forbidden, got '+status);
   const required=path.includes('/thread?')?'Editor access is required to send follow-ups.':'Editor access is required to create tasks.';
   await wait(()=>document.querySelector('#toast-container')?.textContent.includes(required));
   if(target.textContent!=='Keep existing thread')throw Error('denial replaced thread');
   if(form.querySelector('textarea').value!=='Keep my draft')throw Error('lost draft');
  }
  // Exercise real routed responses, not simulated 403s. Only role denials
  // should be eligible for the shared frontend permission handler.
  const originalToast=window.showToast,denials=[];
  window.showToast=(message)=>denials.push(message);
  for(const [method,path] of [
    ['POST','/tasks?project_id=` + project.ID + `'],
    ['POST','/tasks/` + task.ID + `/thread?project_id=` + project.ID + `'],
    ['GET','/channels/webhooks/example?project_id=` + project.ID + `'],
    ['POST','/automations/yaml/parse?project_id=` + project.ID + `']
  ]) {
    const response=await fetch(path,{method});
    if(response.status!==403||response.headers.get('X-OpenVibely-Permission-Denied')!=='true')throw Error('Missing role-denial contract: '+path);
    const count=denials.length;
    if(!await openVibelyPermissionErrors.fetch(response)||denials.length!==count+1)throw Error('Expected one permission toast: '+path);
  }
  for(const path of ['/models','/agents','/skills','/personality','/workers']) {
    const response=await fetch(path),count=denials.length;
    if(response.status!==403||!await openVibelyPermissionErrors.fetch(response)||denials.length!==count+1)throw Error('Missing settings permission toast: '+path);
    if(!denials[count].includes('Organization admin access is required'))throw Error('Wrong settings permission explanation: '+path);
  }
  {
    const response=await fetch('/tasks?project_id=missing-project'),count=denials.length;
    if(response.status!==404||await openVibelyPermissionErrors.fetch(response)||denials.length!==count)throw Error('Hidden project misclassified');
  }
  window.showToast=originalToast;
  await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'pass'},body:'ok'});
 }catch(e){await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'fail'},body:String(e.stack||e)});}
})();</script>`

	owner := member.Principal
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, r.WithContext(models.WithPrincipal(r.Context(), owner)))
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := recorder.Body.String()
		if r.Header.Get("HX-Request") == "" {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", server.URL+"/tasks?project_id="+project.ID,
	)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(t, cmd)
		}
	}()
	select {
	case outcome := <-result:
		stopHandlerBrowserProcess(t, cmd)
		stopped = true
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case <-time.After(60 * time.Second):
		t.Fatal("permission toast browser test timed out")
	}
}
