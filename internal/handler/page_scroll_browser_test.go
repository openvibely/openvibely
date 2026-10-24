package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// Exercise the real shell across ordinary page swaps. A tall child makes the
// scroll contract independent of how much fixture data each page contains.
func TestOrdinaryPagesKeepViewportScrollAndBottomInsetInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	_, e, _ := setupTestHandler(t)
	results := make(chan string, 1)
	runnerStarted := make(chan struct{}, 1)
	var stageMu sync.Mutex
	lastStage := "waiting for document runner"
	started := time.Now()
	var diagnostics []string
	record := func(message string) {
		stageMu.Lock()
		defer stageMu.Unlock()
		if len(diagnostics) >= 160 {
			diagnostics = diagnostics[1:]
		}
		diagnostics = append(diagnostics, fmt.Sprintf("%s %s", time.Since(started).Round(time.Millisecond), message))
	}
	// Install before assets/page scripts, so parse errors and startup failures
	// are recorded even if the scenario never starts. No layout reads here:
	// the heartbeat must remain independent of a potentially blocked layout.
	diagnosticScript := `<script>
window.__scrollTestErrors=[];
window.__scrollTestReport=function(value){fetch('/browser-diagnostic',{method:'POST',body:JSON.stringify(value)}).catch(()=>{});};
window.addEventListener('error',e=>{const value={kind:'error',message:e.message||'resource load failed',source:e.filename||e.target?.src||e.target?.href||'',line:e.lineno,stack:e.error?.stack||''};window.__scrollTestErrors.push(value);window.__scrollTestReport(value);},true);
window.addEventListener('unhandledrejection',e=>{window.__scrollTestReport({kind:'unhandledrejection',message:String(e.reason),stack:e.reason?.stack||''});});
window.__scrollTestHeartbeat=setInterval(()=>window.__scrollTestReport({kind:'heartbeat',ready:document.readyState,visibility:document.visibilityState,focus:document.hasFocus(),stage:window.__scrollTestStage||'startup',errors:window.__scrollTestErrors.length}),1000);
['htmx:beforeRequest','htmx:afterRequest','htmx:beforeSwap','htmx:afterSettle','htmx:responseError','htmx:sendError','htmx:swapError'].forEach(name=>document.addEventListener(name,e=>window.__scrollTestReport({kind:name,target:e.detail?.target?.id,status:e.detail?.xhr?.status,url:e.detail?.xhr?.responseURL,error:String(e.detail?.error||'')})));
</script>`
	runner := `<script>(async()=>{
 const assert=(v,m)=>{if(!v)throw Error(m)};
 const frame=()=>new Promise(r=>setTimeout(r,100));
 const stage=async value=>{window.__scrollTestStage=value;await fetch('/browser-stage',{method:'POST',body:value})};
 const bounded=async(promise,label)=>{
   let timer;
   try { return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('Timed out: '+label)),10000)})]); }
   finally { clearTimeout(timer); }
 };
 await stage('waiting for DOMContentLoaded');
 if(document.readyState==='loading') await bounded(new Promise(r=>document.addEventListener('DOMContentLoaded',r,{once:true})),'DOMContentLoaded');
 assert(window.htmx && typeof htmx.ajax==='function','HTMX unavailable after DOMContentLoaded');
 let current=location.pathname;
 for(const path of ['/models','/schedule','/alerts','/models']) {
   await stage('navigate '+path);
   if(path!==current) {
     // Use a per-request source, like the app's navigation helper, and wait
     // for HTMX's settle lifecycle before changing the new page's geometry.
     const source=document.createElement('a'); source.href=path; document.body.appendChild(source);
     let onSettle;
     const settled=new Promise(resolve=>{onSettle=e=>{if(e.detail.target?.id==='main-content')resolve()};document.body.addEventListener('htmx:afterSettle',onSettle)});
     try { await bounded(Promise.all([htmx.ajax('GET',path,{source,target:'#main-content',swap:'innerHTML'}),settled]),'navigation and settle '+path); }
     finally { document.body.removeEventListener('htmx:afterSettle',onSettle);source.remove(); }
   }
   await stage('check scroll range '+path);
   current=path;
   const main=document.getElementById('main-content');
   assert(main,'missing main-content '+path);
   await stage(path+' before append filler');
   const filler=document.createElement('div'); filler.style.height='4000px'; main.appendChild(filler);
   await stage(path+' after append filler; before timer');
   await frame();
   await stage(path+' after timer; before layout read');
   const rect=main.getBoundingClientRect();
   const geometry={bottom:rect.bottom,viewport:innerHeight,height:main.clientHeight,scrollHeight:main.scrollHeight,top:main.scrollTop,overflow:getComputedStyle(main).overflowY};
   await stage(path+' layout '+JSON.stringify(geometry));
   assert(rect.bottom<=innerHeight-9 && rect.bottom>=innerHeight-20,path+' lost bottom inset: '+rect.bottom+' viewport '+innerHeight);
   assert(geometry.scrollHeight>geometry.height+100,path+' expanded instead of scrolling');
   await stage(path+' before scrollTop=500');
   main.scrollTop=500;
   await stage(path+' after scrollTop=500; before timer');
   await frame();
   await stage(path+' after scroll timer; before scrollTop read');
   const middle=main.scrollTop;
   await stage(path+' middle scrollTop='+middle);
   assert(middle>=499,path+' cannot scroll');
   await stage(path+' before bottom scrollHeight read');
   const end=main.scrollHeight;
   await stage(path+' before bottom scrollTop='+end);
   main.scrollTop=end;
   await stage(path+' after bottom scroll; before timer');
   await frame();
   await stage(path+' after bottom timer; before final layout read');
   const final={height:main.clientHeight,scrollHeight:main.scrollHeight,top:main.scrollTop,rootTop:document.scrollingElement.scrollTop};
   await stage(path+' final '+JSON.stringify(final));
   assert(Math.abs(final.scrollHeight-final.height-final.top)<2,path+' cannot reach bottom');
   assert(final.rootTop===0,'root scrolled instead of page');
 }
 clearInterval(window.__scrollTestHeartbeat);
 await fetch('/browser-result?status=pass');
 })().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||String(e))));</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/browser-diagnostic" && r.URL.Path != "/browser-stage" {
			record("HTTP " + r.Method + " " + r.URL.Path)
		}
		if r.URL.Path == "/browser-diagnostic" {
			var body bytes.Buffer
			_, _ = body.ReadFrom(http.MaxBytesReader(w, r.Body, 8192))
			record("browser " + body.String())
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if static.ServeAsset(w, r) {
			return
		}
		// The optional native host registers its wheel bridge; this layout test
		// exercises the scroll range directly and does not need injected input.
		if r.URL.Path == "/native-test-bridge" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/browser-stage" {
			var body bytes.Buffer
			_, _ = body.ReadFrom(http.MaxBytesReader(w, r.Body, 1024))
			stageMu.Lock()
			lastStage = body.String()
			stageMu.Unlock()
			record("stage " + body.String())
			select {
			case runnerStarted <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/browser-result" {
			select {
			case results <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/events") {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/models" && r.Header.Get("HX-Request") != "true" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Errorf("page returned %d: %s", rec.Code, rec.Body.String())
				http.Error(w, "page failed", rec.Code)
				return
			}
			if !strings.Contains(rec.Body.String(), "</body>") {
				t.Errorf("missing layout: %.300s", rec.Body.String())
				select {
				case results <- "missing layout":
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Replace(strings.Replace(rec.Body.String(), "<head>", "<head>"+diagnosticScript, 1), "</body>", runner+"</body>", 1)))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Native WebKit startup can consume almost 30 seconds on Linux CI before
	// the first document request. Give the scenario its own unchanged budget,
	// starting when its runner executes, rather than at process launch.
	const startupTimeout = 60 * time.Second
	const scenarioTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout+scenarioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/models")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	record("host process started; waiting for document runner")
	var outcome string
	startupTimer := time.NewTimer(startupTimeout)
	select {
	case outcome = <-results:
	case <-runnerStarted:
		record("document runner started; scenario budget 30s")
		scenarioTimer := time.NewTimer(scenarioTimeout)
		select {
		case outcome = <-results:
		case <-scenarioTimer.C:
			stageMu.Lock()
			outcome = "scenario timeout: " + lastStage
			stageMu.Unlock()
		case <-ctx.Done():
			outcome = "host deadline exceeded during scenario"
		}
		scenarioTimer.Stop()
	case <-startupTimer.C:
		outcome = "startup timeout: document runner did not start within 60s"
	case <-ctx.Done():
		outcome = "host deadline exceeded during startup"
	}
	startupTimer.Stop()
	if outcome != "pass" {
		// A JavaScript heartbeat can stop when WebKit's event loop hangs. Capture
		// process state from outside that loop before terminating the test host.
		if output, err := exec.Command("ps", "-axo", "pid,ppid,stat,pcpu,comm").CombinedOutput(); err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				if strings.Contains(line, "WebKit") || strings.Contains(line, "native-chat") {
					record("process " + line)
				}
			}
		}
	}
	stopHandlerBrowserProcess(cmd)
	stageMu.Lock()
	trace := strings.Join(diagnostics, "\n")
	stageMu.Unlock()
	require.Equal(t, "pass", outcome, "Browser trace:\n%s\nHost output:\n%s", trace, stderr.String())
}
