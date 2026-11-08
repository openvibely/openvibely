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
 const installModal=document.getElementById('plugin-install-modal');
 if(installModal.querySelector('[data-plugin-package]')||installModal.textContent.includes('Installed packages'))throw Error('Install modal contains package management');
 const templateModal=document.getElementById('plugin-template-modal');
 if(!templateModal.querySelector('select[name="template_key"]')||!templateModal.querySelector('.modal-action button[type="submit"]'))throw Error('Template modal does not use standard selector and footer');
 if(document.querySelector('[id^="plugin-details-"]'))throw Error('Obsolete details modal present');
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
 if(entry.querySelector('a').hasAttribute('data-tooltip-disabled') || entry.querySelector('a').dataset.tip!=='Sales')throw Error('Sales rail tooltip missing');
 if(!entry.textContent.includes('📊'))throw Error('Sales custom icon missing');
 if(!entry.getClientRects().length)throw Error('Sales entry hidden: '+[entry,...(function*(){let p=entry.parentElement;while(p){yield p;p=p.parentElement}})()].map(e=>e.tagName+'#'+e.id+'.'+e.className+':'+getComputedStyle(e).display).join(' / '));
 const clickAction=async action=>{
  await wait(()=>document.querySelector('#plugin-settings button[hx-post*="/'+action+'?"]'),'Missing '+action+' control');
  document.querySelector('#plugin-settings button[hx-post*="/'+action+'?"]').click();
 };
 const pageLink=document.querySelector('#plugin-pages-nav [data-plugin-nav-entry] a');
 pageLink.click();
 await wait(()=>document.querySelector('#plugin-page-root'),'Page did not open');
 await wait(()=>document.querySelector('#plugin-startup')?.dataset.state==='loaded','Initial frame failed');
 const pageFrame=document.getElementById('plugin-frame');
 if(!pageFrame.hasAttribute('data-tooltip-disabled'))throw Error('Page frame tooltip enabled');
 await wait(()=>pageFrame.style.getPropertyValue('--superellipse-clip'),'Page corners not shaped');
 const clip=pageFrame.style.getPropertyValue('--superellipse-clip');
 if(!clip.includes(pageFrame.clientWidth.toFixed(2)+'px '+pageFrame.clientHeight.toFixed(2)+'px') || !clip.includes('0.00px '+pageFrame.clientHeight.toFixed(2)+'px'))throw Error('Page bottom corners are not square');
 const pageHeader=document.querySelector('#plugin-page-root > [data-page-header]');
 const toggle=document.querySelector('#page-chat-toggle');
 const headerRect=pageHeader.getBoundingClientRect(), toggleRect=toggle.getBoundingClientRect();
 if(Math.abs(toggleRect.top-headerRect.top-8)>1 || Math.abs(toggleRect.right-headerRect.right)>1 || toggleRect.width!==32 || toggleRect.height!==32)throw Error('Page toggle does not match standard thread toolbar alignment: '+JSON.stringify({header:headerRect,toggle:toggleRect}));
 if(getComputedStyle(pageHeader).flexShrink!=='0')throw Error('Page toolbar can shrink out of alignment');
 const oldHandle=document.querySelector('#plugin-page-root').getAttribute('data-plugin-handle');
 document.querySelector('#appearance-settings-nav a[data-nav-base="/settings/plugin-pages"]').click();
 await wait(()=>document.querySelector('#plugin-settings'),'Settings did not open');
 await new Promise(r=>setTimeout(r,400));
 if(document.querySelector('#settings-retained-workspace #plugin-page-root'))throw Error('Plugin capability retained in settings');
 await clickAction('disable');
 await wait(()=>!document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Disabled Sales still in rail');
 await clickAction('enable');
 await wait(()=>document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]'),'Enabled Sales missing after settings action');
 await wait(()=>document.querySelector('#plugin-pages-nav [data-plugin-nav-entry]').getClientRects().length,'Reenabled Sales hidden');
 checkSettings();
 document.querySelector('#plugin-pages-nav [data-plugin-nav-entry] a').click();
 await wait(()=>document.querySelector('#plugin-page-root'),'Reenabled page did not open');
 await wait(()=>document.querySelector('#plugin-startup')?.dataset.state==='loaded','Reenabled frame failed');
 if(document.querySelector('#plugin-page-root').getAttribute('data-plugin-handle')===oldHandle)throw Error('Reused expired page capability');
 if(document.querySelector('#plugin-notice').textContent.includes('navigated away'))throw Error('Frame disconnected');
 document.querySelector('#appearance-settings-nav a[data-nav-base="/settings/plugin-pages"]').click();
 await wait(()=>document.querySelector('#plugin-settings'),'Settings did not open');
 await new Promise(r=>setTimeout(r,400));
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
 const install=document.getElementById('plugin-install-modal');install.showModal();
 install.querySelector('details').open=true;const form=install.querySelector('form[hx-post*="/install?"]');const badPath='/nonexistent/plugin-browser-test';
 let installSettled=false;
 const onInstallSettled=e=>{
  if(e.detail.elt && e.detail.elt.id==='plugin-settings'){
   installSettled=true;
   document.body.removeEventListener('htmx:afterSettle',onInstallSettled);
  }
 };
 document.body.addEventListener('htmx:afterSettle',onInstallSettled);
 form.elements.path.value=badPath;form.requestSubmit();
 await wait(()=>installSettled,'Installation response did not finish settling');
 await wait(()=>document.getElementById('plugin-install-error'),'Installation error missing');
 const reopened=document.getElementById('plugin-install-modal');
 await wait(()=>reopened.open,'Install modal did not remain open');
 if(!reopened.contains(document.getElementById('plugin-install-error')))throw Error('Error is outside modal');
 if(reopened.querySelector('[name="path"]').value!==badPath)throw Error('Failed path not preserved');
 if(!document.getElementById('plugin-install-error').getClientRects().length)throw Error('Installation error hidden');

 reopened.close();
 const uploadModal=document.getElementById('plugin-install-modal');uploadModal.showModal();
 const uploadForm=uploadModal.querySelector('form[hx-encoding]');
 const transfer=new DataTransfer();
 const manifest=JSON.stringify({schema_version:1,id:'test.browser-upload',name:'Browser uploaded',version:'1.0.0',pages:[{id:'home',title:'Home',entry:'assets/index.html'}]});
 for(const [name,relative,content] of [['plugin.json','bundle/plugin.json',manifest],['index.html','bundle/assets/index.html','<html>hello</html>']]){
  const file=new File([content],name);Object.defineProperty(file,'webkitRelativePath',{value:relative});transfer.items.add(file);
 }
 uploadForm.querySelector('input[type=file]').files=transfer.files;
 uploadForm.requestSubmit();
 await wait(()=>toasts.some(t=>t.includes('Installed Browser uploaded')),'Folder upload did not install');
 if(document.getElementById('plugin-install-modal').open)throw Error('Upload success left modal open');
 const nav=document.getElementById('plugin-pages-nav');
 const originalProject=nav.dataset.pluginProject;
 let railRequests=0;
 nav.addEventListener('htmx:beforeRequest',()=>railRequests++);
 window.openVibelyRefreshPluginNav('__all_projects__');
 window.openVibelyRefreshPluginNav('__all_projects__');
 await new Promise(r=>setTimeout(r,400));
 if(railRequests!==1)throw Error('Duplicate rail refresh on project switch: '+railRequests);
 window.openVibelyRefreshPluginNav(originalProject);
 if(nav.children.length)throw Error('Previous project icons retained during switch');
 await new Promise(r=>setTimeout(r,400));
 if(nav.dataset.pluginProject!==originalProject)throw Error('Rail scope was overwritten');
 const stale=new Event('htmx:beforeSwap');
 stale.detail={shouldSwap:true,xhr:{responseURL:location.origin+'/plugin-pages/nav?project_id=__all_projects__'}};
 nav.dispatchEvent(stale);
 if(stale.detail.shouldSwap)throw Error('Stale project response allowed to flash');

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
