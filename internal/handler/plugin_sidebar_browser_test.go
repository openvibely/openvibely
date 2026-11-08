package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestPluginSidebarLoadsEnabledPagesBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	env := setupPluginPages(t)
	runner := `<script>(async function(){
 const wait=async(fn,label)=>{const until=Date.now()+6000;while(Date.now()<until){if(fn())return;await new Promise(r=>setTimeout(r,30));}throw Error(label);};
 try{
 if(document.readyState==='loading')await new Promise(r=>document.addEventListener('DOMContentLoaded',r,{once:true}));
 const checkSettings=()=>{
  if(!document.documentElement.classList.contains('appearance-settings'))throw Error('Settings navigation reverted to tasks');
  if(document.getElementById('task-list-panel').getAttribute('aria-label')!=='Settings')throw Error('Wrong navigation context');
 };
 await wait(()=>document.documentElement.classList.contains('appearance-settings'),'Settings navigation missing on direct load');
 checkSettings();
 if(!window.isSettingsWorkspaceURL(location.href))throw Error('Plugin settings route not recognized');
 document.querySelector('#appearance-settings-nav a[data-nav-base="/settings/plugin-pages"]').click();
 await new Promise(r=>setTimeout(r,400));
 checkSettings();
 for (const id of ['plugin-template-modal','plugin-describe-modal','plugin-install-modal']) {
  const button=Array.from(document.querySelectorAll('#plugin-settings button')).find(b=>(b.getAttribute('onclick')||'').includes(id));
  if(!button)throw Error('Missing add menu entry '+id);
  button.click();
  if(!document.getElementById(id).open)throw Error('Dialog did not open '+id);
  document.getElementById(id).close();
 }
 const details=document.querySelector('[id^="plugin-details-"]');
 details.showModal();details.close();
 const search=document.querySelector('#plugin-settings [data-card-search]');
 search.value='not-a-page';search.dispatchEvent(new Event('input',{bubbles:true}));
 await wait(()=>!Array.from(document.querySelectorAll('[data-plugin-instance-row]')).some(e=>e.getClientRects().length),'Search did not hide Sales');
 await new Promise(r=>setTimeout(r,500));
 search.value='';search.dispatchEvent(new Event('input',{bubbles:true}));
 await wait(()=>Array.from(document.querySelectorAll('[data-plugin-instance-row]')).some(e=>e.getClientRects().length),'Clearing search did not restore Sales');
 const sidebar=document.getElementById('sidebar');
 sidebar.classList.add('sidebar-collapsed');
 for(const el of sidebar.querySelectorAll('style,script')){
  if(getComputedStyle(el).display!=='none')throw Error('Code visible in rail: '+el.tagName);
 }
 await wait(()=>document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Enabled Sales missing at startup');
 const entry=document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]');
 if(!entry.textContent.includes('Sales'))throw Error('Sales label missing');
 if(!entry.getClientRects().length)throw Error('Sales entry hidden: '+[entry,...(function*(){let p=entry.parentElement;while(p){yield p;p=p.parentElement}})()].map(e=>e.tagName+'#'+e.id+'.'+e.className+':'+getComputedStyle(e).display).join(' / '));
 const clickAction=async action=>{
  await wait(()=>document.querySelector('#plugin-settings button[hx-post*="/'+action+'?"]'),'Missing '+action+' control');
  document.querySelector('#plugin-settings button[hx-post*="/'+action+'?"]').click();
 };
 await clickAction('disable');
 await wait(()=>!document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Disabled Sales still in rail');
 await clickAction('enable');
 await wait(()=>document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Enabled Sales missing after settings action');
 await wait(()=>document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]').getClientRects().length,'Reenabled Sales hidden');
 checkSettings();
 const toasts=[];
 document.body.addEventListener('openvibelyToast',e=>toasts.push(e.detail.message));
 await htmx.ajax('GET',location.pathname+location.search+'&enabled=true&sort=name_desc',{target:'#main-content',swap:'innerHTML'});
 await new Promise(r=>setTimeout(r,500));
 await clickAction('disable');
 await wait(()=>!document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Filtered disable failed');
 await wait(()=>!document.querySelector('#plugin-settings [data-plugin-instance-row]'),'Disabled card remained in enabled filter');
 if(document.querySelector('#plugin-settings [name="enabled"]').value!=='true')throw Error('Status filter reset');
 if(document.querySelector('#plugin-settings [name="sort"]').value!=='name_desc')throw Error('Sort reset');
 if(document.querySelector('#plugin-settings .alert-success'))throw Error('Success banner still rendered');
 await wait(()=>toasts.includes('Disabled'),'Standard success toast missing');
 checkSettings();
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)});}
})();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(204)
			return
		}

		rec := httptest.NewRecorder()
		env.e.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		body := rec.Body.String()
		if r.URL.Path == "/settings/plugin-pages" {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--window-size=1440,1000", "--user-data-dir="+t.TempDir(), server.URL+"/settings/plugin-pages?project_id="+env.projA.ID)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(30 * time.Second):
		t.Fatal("browser timed out")
	}
}
