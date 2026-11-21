package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

func TestComputerChromeConsentAcrossProfilesInPanel(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(fmt.Sprintf("expanded=%t", expanded), func(t *testing.T) { testComputerChromeConsent(t, expanded) })
	}
}

func testComputerChromeConsent(t *testing.T, expanded bool) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	var fragment bytes.Buffer
	require.NoError(t, pages.ProjectComputerContent("project", "task").Render(t.Context(), &fragment))
	runner := `<script>(async()=>{
 const wait=async check=>{for(let n=0;n<100;n++){if(check())return;await new Promise(r=>setTimeout(r,20));}throw Error('Timed out: '+check.toString()+' toasts='+JSON.stringify(window.toastMessages));};
 const q=s=>document.querySelector(s);const finish=(status,message)=>{const node=document.createElement('div');node.id='browser-result';node.dataset.status=status;node.textContent=message;document.body.append(node);};
 try{
 await wait(()=>!q('[data-computer-login]').hidden);
 if(EXPANDED)q('[data-computer-preview-open]').click();
 q('[data-computer-chrome]').click();await wait(()=>q('[data-computer-sites] input'));
 if(!q('[data-computer-import-dialog]').open)throw Error('Importer is not modal');
 q('[data-computer-import-cancel]').click();
 if(q('[data-computer-import-dialog]').open)throw Error('Cancel did not close importer');
 if(EXPANDED&&!q('[data-computer-dialog]').open)throw Error('Cancel closed computer viewer');
 q('[data-computer-chrome]').click();await wait(()=>q('[data-computer-import-dialog]').open&&q('[data-computer-sites] .cookie-row input'));
 const search=q('[data-computer-cookie-search]');search.value='first-session';search.dispatchEvent(new Event('input'));
 if(q('[data-computer-sites]').textContent.includes('unselected'))throw Error('Search did not filter');
 search.value='';search.dispatchEvent(new Event('input'));
 q('[data-computer-sites] .cookie-row input').click();
 q('[data-computer-profile]').value='Profile 1';q('[data-computer-profile]').dispatchEvent(new Event('change'));
 await wait(()=>q('[data-computer-sites]').textContent.includes('second-session'));q('[data-computer-sites] .cookie-row input').click();
 q('[data-computer-grant-scope]').value='project';q('[data-computer-grant-hours]').value='24';q('[data-computer-import]').click();
 await wait(()=>q('[data-computer-import]').textContent.includes('Resume'));q('[data-computer-import]').click();await wait(()=>q('[data-computer-chrome-form]').hidden && toastMessages.some(t=>t.message.includes('Imported 2')));
 q('[data-computer-target]').click();q('#computer-target-picker button[data-value=computer]').click();
 // Observe the queued snapshot being applied before checking the saved destination.
 await wait(()=>q('[data-computer-login-site]').textContent.includes('Status queued before target save'));
 await wait(()=>q('[data-computer-target]').dataset.value==='computer');
 finish('pass','Selected cookies across two profiles and tool destination saved');
 }catch(error){finish('fail',error.message);}
 })();</script>`
	runner = strings.ReplaceAll(runner, "EXPANDED", fmt.Sprint(expanded))
	live := `<script>
 window.openVibelyLive={computer:(project,task,options,handlers)=>{
  const emit=reason=>handlers.listeners.computer_status({data:JSON.stringify({project_id:project,task_id:task,viewer_mode:options.viewer_mode,enabled:false,ready:false,held:true,handoff_kind:'request_chrome_login',handoff_reason:reason,handoff_url:'https://example.test',chrome_available:true,tool_target:'worktree'})});
  window.queueStaleComputerStatus=()=>emit('Status queued before target save');
  setTimeout(()=>{handlers.onopen?.();emit('');},0);
  return {close(){}};
 }};
 // Deliver an old status after the response arrives, before the save handler resumes.
 // The panel debounces status rendering, so this snapshot is applied after the save.
 const realFetch=window.fetch;
 window.fetch=async(...args)=>{
  const response=await realFetch(...args);
  if(String(args[0]).includes('/target-computer'))window.queueStaleComputerStatus();
  return response;
 };
 window.toastMessages=[];window.showToast=(message,type)=>toastMessages.push({message,type});
 </script>`
	var mu sync.Mutex
	var imported struct {
		GrantID    string                         `json:"grant_id"`
		Scope      string                         `json:"scope"`
		Hours      int                            `json:"hours"`
		Selections []models.ChromeCookieSelection `json:"selections"`
	}
	target := ""
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<!doctype html><html><body><script src=\"/static/retry.js\"></script>"+live+fragment.String()+runner+"</body></html>")
		case strings.HasSuffix(r.URL.Path, "/chrome"):
			profile := r.URL.Query().Get("profile")
			if profile == "" {
				fmt.Fprint(w, `{"profiles":["Default","Profile 1"]}`)
				return
			}
			name := "first-session"
			if profile == "Profile 1" {
				name = "second-session"
			}
			json.NewEncoder(w).Encode(map[string]any{"cookies": []models.ChromeCookieSelection{{Profile: profile, Domain: "example.test", Name: name, Path: "/"}, {Profile: profile, Domain: "example.test", Name: "unselected", Path: "/"}}})
		case strings.HasSuffix(r.URL.Path, "/chrome-import"):
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&imported)
			mu.Unlock()
			attempts++
			if attempts == 1 {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"grant_id":"partial-grant","imported":1,"message":"Retry remaining profile"}`)
			} else {
				require.Equal(t, "partial-grant", imported.GrantID)
				fmt.Fprint(w, `{"imported":2}`)
			}
		case strings.HasSuffix(r.URL.Path, "/target-computer"):
			mu.Lock()
			target = "computer"
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=10000", "--dump-dom", server.URL)
	output, err := runHandlerBrowserProcess(t, cmd)
	require.NoError(t, err)
	if !bytes.Contains(output, []byte(`id="browser-result" data-status="pass"`)) {
		t.Fatalf("consent panel did not pass: %s", output[max(0, len(output)-1000):])
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "computer", target)
	require.Equal(t, "project", imported.Scope)
	require.Equal(t, 24, imported.Hours)
	require.Len(t, imported.Selections, 2)
	require.Equal(t, "Default", imported.Selections[0].Profile)
	require.Equal(t, "Profile 1", imported.Selections[1].Profile)
	for _, cookie := range imported.Selections {
		require.NotEqual(t, "unselected", cookie.Name)
	}
}

func TestComputerManualChromeConsentAcrossProfilesInPanel(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	var fragment bytes.Buffer
	require.NoError(t, pages.ProjectComputerContent("project", "task").Render(t.Context(), &fragment))
	runner := `<script>(async()=>{
 const wait=async check=>{for(let n=0;n<100;n++){if(check())return;await new Promise(r=>setTimeout(r,20));}throw Error('Timed out');};
 const q=s=>document.querySelector(s);const finish=(status,message)=>{const node=document.createElement('div');node.id='browser-result';node.dataset.status=status;node.textContent=message;document.body.append(node);};
 try{
 await wait(()=>q('[data-computer-login]').hidden);
 q('[data-computer-grants-open]').click();await wait(()=>!q('[data-computer-manual-import]').hidden);
 q('[data-computer-manual-import]').click();await wait(()=>toastMessages.some(t=>t.message==='Fixture access denied'));
 if(!q('[data-computer-chrome-form]').hidden)throw Error('Failed load opened picker');
 q('[data-computer-grants-open]').click();await wait(()=>!q('[data-computer-manual-import]').hidden);
 q('[data-computer-manual-import]').click();await wait(()=>q('[data-computer-sites] input'));
 if(q('[data-computer-chrome-form]').closest('[data-computer-login]'))throw Error('Manual picker hidden in handoff');
 q('[data-computer-sites] input').click();
 q('[data-computer-profile]').value='Profile 1';q('[data-computer-profile]').dispatchEvent(new Event('change'));
 await wait(()=>q('[data-computer-sites]').textContent.includes('second-session'));q('[data-computer-sites] input').click();
 q('[data-computer-grant-scope]').value='project';q('[data-computer-grant-hours]').value='24';q('[data-computer-import]').click();
 await wait(()=>q('[data-computer-import]').textContent.includes('Resume'));q('[data-computer-import]').click();await wait(()=>q('[data-computer-chrome-form]').hidden && toastMessages.some(t=>t.message.includes('Imported 2')));
 if(toastMessages.some(t=>t.type==='success'&&t.message.includes('Done')))throw Error('Manual import asked to resume');
 finish('pass','Selected cookies across two profiles and tool destination saved');
 }catch(error){finish('fail',error.message);}
 })();</script>`
	live := `<script>
 window.openVibelyLive={computer:(project,task,options,handlers)=>{
  const emit=reason=>handlers.listeners.computer_status({data:JSON.stringify({project_id:project,task_id:task,viewer_mode:options.viewer_mode,enabled:false,ready:false,held:false,handoff_kind:'',handoff_reason:reason,handoff_url:'https://example.test',chrome_available:true,tool_target:'worktree'})});
  window.queueStaleComputerStatus=()=>emit('Status queued before target save');
  setTimeout(()=>{handlers.onopen?.();emit('');},0);
  return {close(){}};
 }};
 // Deliver an old status after the response arrives, before the save handler resumes.
 // The panel debounces status rendering, so this snapshot is applied after the save.
 const realFetch=window.fetch;
 window.fetch=async(...args)=>{
  const response=await realFetch(...args);
  if(String(args[0]).includes('/target-computer'))window.queueStaleComputerStatus();
  return response;
 };
 window.toastMessages=[];window.showToast=(message,type)=>toastMessages.push({message,type});
 </script>`
	var mu sync.Mutex
	var imported struct {
		GrantID    string                         `json:"grant_id"`
		Scope      string                         `json:"scope"`
		Hours      int                            `json:"hours"`
		Selections []models.ChromeCookieSelection `json:"selections"`
	}
	profileLoads := 0
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<!doctype html><html><body><script src=\"/static/retry.js\"></script>"+live+fragment.String()+runner+"</body></html>")
		case strings.HasSuffix(r.URL.Path, "/chrome-grants"):
			fmt.Fprint(w, `{"grants":[],"chrome_available":true}`)
		case strings.HasSuffix(r.URL.Path, "/chrome"):
			profile := r.URL.Query().Get("profile")
			if profile == "" {
				profileLoads++
				if profileLoads == 1 {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"message":"Fixture access denied"}`)
					return
				}
				fmt.Fprint(w, `{"profiles":["Default","Profile 1"]}`)
				return
			}
			name := "first-session"
			if profile == "Profile 1" {
				name = "second-session"
			}
			json.NewEncoder(w).Encode(map[string]any{"cookies": []models.ChromeCookieSelection{{Profile: profile, Domain: "example.test", Name: name, Path: "/"}, {Profile: profile, Domain: "example.test", Name: "unselected", Path: "/"}}})
		case strings.HasSuffix(r.URL.Path, "/chrome-import"):
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&imported)
			mu.Unlock()
			attempts++
			if attempts == 1 {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"grant_id":"partial-grant","imported":1,"message":"Retry remaining profile"}`)
			} else {
				require.Equal(t, "partial-grant", imported.GrantID)
				fmt.Fprint(w, `{"imported":2}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=10000", "--dump-dom", server.URL)
	output, err := runHandlerBrowserProcess(t, cmd)
	require.NoError(t, err)
	if !bytes.Contains(output, []byte(`id="browser-result" data-status="pass"`)) {
		t.Fatalf("consent panel did not pass: %s", output[max(0, len(output)-1000):])
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, profileLoads)
	require.Equal(t, "project", imported.Scope)
	require.Equal(t, 24, imported.Hours)
	require.Len(t, imported.Selections, 2)
	require.Equal(t, "Default", imported.Selections[0].Profile)
	require.Equal(t, "Profile 1", imported.Selections[1].Profile)
	for _, cookie := range imported.Selections {
		require.NotEqual(t, "unselected", cookie.Name)
	}
}

func TestComputerChromeImportVisibility(t *testing.T) {
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	for _, tc := range []struct {
		name      string
		task      bool
		available bool
	}{{"unsupported-task", true, false}, {"project-only", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var fragment bytes.Buffer
			taskID := ""
			if tc.task {
				taskID = "task"
			}
			require.NoError(t, pages.ProjectComputerPermissions("project", taskID).Render(t.Context(), &fragment))
			wrapper := "<div>"
			if tc.task {
				wrapper = `<div data-computer-task="task">`
			}
			runner := `<script>(async()=>{
 const q=s=>document.querySelector(s),wait=async check=>{for(let n=0;n<100;n++){if(check())return;await new Promise(r=>setTimeout(r,20));}throw Error('Timed out');};
 const result=document.createElement('div');result.id='browser-result';document.body.append(result);
 try{
 const button=q('[data-computer-manual-import]'),trigger=q('[data-computer-grants-open]'),search=q('.inspector-picker input');
 trigger.click();await wait(()=>q('[data-computer-grant-list]').textContent.includes('No Chrome permissions'));
 if(!button.hidden)throw Error('Unsupported import visible after load');
 search.value='Chrome';search.dispatchEvent(new Event('input'));
 if(!button.hidden)throw Error('Search exposed unavailable import');
 trigger.click();trigger.click();
 if(!button.hidden)throw Error('Reopen exposed unavailable import');
 result.dataset.status='pass';
 }catch(e){result.dataset.status='fail';result.textContent=e.message;}
 })();</script>`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if strings.HasSuffix(r.URL.Path, "/chrome-grants") {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"grants": []any{}, "chrome_available": tc.available})
					return
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, "<!doctype html><html><body>"+wrapper+fragment.String()+"</div>"+runner+"</body></html>")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=10000", "--dump-dom", server.URL)
			output, err := runHandlerBrowserProcess(t, cmd)
			require.NoError(t, err)
			require.Contains(t, string(output), `id="browser-result" data-status="pass"`)
		})
	}
}
