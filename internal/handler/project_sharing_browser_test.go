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

func TestProjectSharingPanelInChrome(t *testing.T) {
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
	project := createProject(t, h, "Browser Sharing")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := org.Accept(ctx, token, "browser-member", "long-password-value", "")
	require.NoError(t, err)

	result := make(chan string, 1)
	runner := `<script>(async function(){
 const wait=(p,label)=>new Promise((resolve,reject)=>{const end=Date.now()+15000;(function poll(){let v=null;try{v=p()}catch(e){}if(v&&!document.querySelector('.htmx-request,.htmx-settling,.htmx-swapping'))return resolve(v);if(Date.now()>end)return reject(Error('timeout: '+label));setTimeout(poll,30)})();});
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 const panel=()=>document.getElementById('project-sharing');
 const reopen=async()=>{const previous=panel();document.getElementById('project-settings-btn').click();await wait(()=>panel()!==previous&&panel(),'reopen settings');document.getElementById('edit-project-sharing-tab').click();};
 const rows=()=>Array.from(panel().querySelectorAll('tbody tr')).filter(r=>r.closest('details')===null);
 let confirmQuestion='';
 try {
  window.openVibelyConfirm=q=>{confirmQuestion=q;return Promise.resolve(true)};
  document.getElementById('project-settings-btn').click();
  await wait(()=>document.getElementById('edit-project-sharing-tab')&&panel(),'sharing panel load');
  assert(document.getElementById('edit-project-sharing').hidden,'sharing should start hidden');
  document.getElementById('edit-project-sharing-tab').click();
  assert(!document.getElementById('edit-project-sharing').hidden,'sharing tab did not open');
  assert(!panel().closest('form'),'sharing forms must not nest in settings form');
  assert(document.getElementById('edit-project-save-button').closest('.modal-action').hidden,'general Save must hide on Sharing');
  document.getElementById('edit-project-general-tab').click();
  assert(document.getElementById('edit-project-sharing').hidden,'sharing did not hide on General');
  assert(!document.getElementById('edit-project-save-button').closest('.modal-action').hidden,'general Save was not restored');
  document.getElementById('edit-project-sharing-tab').click();
  assert(!panel().querySelector('select[name=org_role]'),'organization-wide access must not be offered');
  const add=panel().querySelector('[data-sharing-form]');
  add.querySelector('[data-sharing-add-user]').value='` + member.UserID + `';
  add.querySelector('[data-sharing-add-role]').value='maintainer';
  add.querySelector('[data-sharing-add]').click();
  assert(rows().some(r=>r.textContent.includes('browser-member')),'pending member missing');
  assert(!panel().querySelector('[data-sharing-save]').disabled,'Save not enabled');
  panel().querySelector('[data-sharing-cancel]').click();
  assert(!rows().some(r=>r.textContent.includes('browser-member')),'Cancel retained pending member');
  document.getElementById('edit_project_modal').showModal();
  add.querySelector('[data-sharing-add-user]').value='` + member.UserID + `';
  add.querySelector('[data-sharing-add]').click();
  // The real endpoint rejects an invalid member without partially saving the draft.
  add.elements.changes.value=JSON.stringify({'` + member.UserID + `':'maintainer','missing-member':'viewer'});
  panel().querySelector('[data-sharing-save]').click();
  await wait(()=>!panel().querySelector('[data-sharing-save]').disabled,'failed save preserves draft');
  assert(document.getElementById('edit_project_modal').open,'failed save closed modal');
  assert(panel().querySelector('[data-sharing-form]')===add,'failed save replaced the form');
  assert(rows().some(r=>r.textContent.includes('browser-member')),'failed save lost draft');
  add.dispatchEvent(new Event('change',{bubbles:true}));
  panel().querySelector('[data-sharing-save]').click();
  await wait(()=>!document.getElementById('edit_project_modal').open,'save closes modal');
  await reopen();
  const memberRow=rows().find(r=>r.textContent.includes('browser-member'));
  assert(memberRow.querySelector('select').value==='maintainer','saved role wrong');
  memberRow.querySelector('select').value='viewer';
  memberRow.querySelector('select').dispatchEvent(new Event('change',{bubbles:true}));
  panel().querySelector('[data-sharing-save]').click();
  await wait(()=>!document.getElementById('edit_project_modal').open,'role save closes modal');
  await reopen();
  assert(rows().find(r=>r.textContent.includes('browser-member')).querySelector('select').value==='viewer','role edit not persisted');
  rows().find(r=>r.textContent.includes('browser-member')).querySelector('button').click();
  assert(!panel().querySelector('[data-sharing-save]').disabled,'removal not pending');
  panel().querySelector('[data-sharing-save]').click();
  await wait(()=>!document.getElementById('edit_project_modal').open,'removal save closes modal');
  await reopen();
  assert(!rows().some(r=>r.textContent.includes('browser-member')),'removal not persisted');
  const audit=panel().querySelector('details');
  assert(audit&&audit.querySelectorAll('tbody tr').length>=2,'audit rows missing');
  const transfer=Array.from(panel().querySelectorAll('button')).find(b=>b.textContent.includes('Transfer ownership'));
  assert(transfer&&!transfer.disabled,'owner transfer unavailable');
  transfer.form.querySelector('select[name=user_id]').value='` + member.UserID + `';
  transfer.click();
  await wait(()=>panel()&&Array.from(panel().querySelectorAll('p strong')).some(n=>n.textContent.trim()==='browser-member'),'transfer completes');
  assert(!panel().querySelector('select[aria-label="New owner"] option[value="` + member.UserID + `"]'),'new owner still offered as transfer target');
  assert(confirmQuestion.includes('Transfer ownership'),'transfer was not confirmed');
  await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'pass'},body:'ok',keepalive:true});
 } catch(e) {
  await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'fail'},body:String(e&&e.stack||e)+'\n'+'confirm='+confirmQuestion+' '+(panel()?panel().outerHTML.slice(-1800):'no panel'),keepalive:true});
 }
})();</script>`

	owner := models.Principal{UserID: models.BootstrapUserID}
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
		t.Fatal("project sharing browser test timed out")
	}
	policy, err := stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "org", policy.Visibility)
	require.Equal(t, "editor", policy.OrgRole)
	require.Equal(t, member.UserID, policy.OwnerUserID)
	members, err := stores.ProjectAccess.Members(ctx, project.ID)
	require.NoError(t, err)
	for _, m := range members {
		require.NotEqual(t, member.UserID, m.UserID, "removed grant persisted")
	}
}
