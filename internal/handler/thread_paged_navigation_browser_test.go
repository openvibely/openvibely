package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// Loading older history leaves the latest execution offscreen. A fast warm
// response must not preserve its serialized placeholders before hydration finishes.
func TestTaskPagedCachedNavigationInChrome(t *testing.T) {
	testTaskPagedCachedNavigation(t, "cache")
}

func TestTaskBoundedCachedHistoryInChrome(t *testing.T) {
	testTaskPagedCachedNavigation(t, "bounded")
}

func TestTaskPagedScrollStabilityInChrome(t *testing.T) {
	testTaskPagedCachedNavigation(t, "scroll")
}

func TestTaskHiddenPagedScrollStabilityInChrome(t *testing.T) {
	testTaskPagedCachedNavigation(t, "hidden")
}

func TestTaskHiddenLargeRowScrollStabilityInChrome(t *testing.T) {
	testTaskPagedCachedNavigation(t, "hidden-row")
}

func testTaskPagedCachedNavigation(t *testing.T, mode string) {
	nativeWheel := mode == "native-wheel" || mode == "native-wheel-delayed"
	hiddenRow := mode == "hidden-row"
	bounded := mode == "bounded"
	hiddenPaging := mode == "hidden"
	scrollStability := mode == "scroll" || hiddenPaging
	nativeScript := ""
	if nativeWheel {
		data, err := os.ReadFile("testdata/native_thread_scroll.js")
		require.NoError(t, err)
		nativeScript = fmt.Sprintf("const expectDelayedHistory = %t;\n", mode == "native-wheel-delayed") + string(data)
	}
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Paged navigation")
	task := createTask(t, h, project.ID, "Paged task", func(t *models.Task) { t.Status = models.StatusCompleted; t.Category = models.CategoryCompleted })
	other := createTask(t, h, project.ID, "Other task")
	type sample struct {
		Prompt string
		Output string
	}
	var samples []sample
	count := 10
	if scrollStability {
		count = 25
	}
	for i := 0; i < count; i++ {
		samples = append(samples, sample{fmt.Sprintf("Prompt %d", i), strings.Repeat("A paragraph of **formatted text**.\n\n", 20)})
	}
	samples[len(samples)-1].Output = strings.Repeat("**Progress**\n[Using tool: shell | echo example]\n[Tool shell done]\n"+strings.Repeat("tool output line\n", 100)+"[/Tool]\n", 120) + "Final answer.\n"

	if nativeWheel {
		for i := range samples {
			samples[i].Output = strings.Repeat("Assistant progress.\n[Using tool: shell | echo example]\n[Tool shell done]\nresult\n[/Tool]\n", 40)
		}
	}

	if hiddenRow {
		samples[len(samples)-1].Output = strings.Repeat("Large **paragraph**.\n\n", 4000) + strings.Repeat("[Using tool: shell | echo tail]\n[Tool shell done]\nsmall result\n[/Tool]\n", 30) + "Latest message."
	}

	if scrollStability {
		for i := range samples {
			samples[i].Output = strings.Repeat("**Progress**\n[Using tool: shell | echo example]\n[Tool shell done]\nresult\n[/Tool]\n", 40)
		}
	}

	for i, s := range samples {
		if s.Output == "" {
			continue
		}
		ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = s.Prompt
			ex.StartedAt = time.Unix(int64(i+1), 0)
			ex.IsFollowup = i > 0
		})
		require.NoError(t, h.execRepo.Complete(t.Context(), ex.ID, models.ExecCompleted, s.Output, "", 0, 0))
	}
	path := "/tasks/" + task.ID
	runner := fmt.Sprintf(`<script>
 window.addEventListener('DOMContentLoaded',()=>{
 const bounded=%t, scrollStability=%t, hiddenPaging=%t, hiddenRow=%t;
 const paint=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 async function until(check,label){const deadline=performance.now()+10000;while(performance.now()<deadline){if(check())return;await paint()}throw Error('Timeout: '+label+' '+debug())}
 async function settled(label){
  await paint();
  await until(()=>{const m=messages();return m && m.dataset.transcriptHydrating!=='true' && m.dataset.earlierAnchorRestoring!=='true' && !window._chatContentRenderActive && !window._chatContentRenderQueue?.length && !Array.from(m.querySelectorAll('[data-chat-virtual-owner]')).some(o=>o._renderingRevision||o._scheduledChatRender||o._activeScheduledChatRender)},label);
 }
 const errors=window._pagedStartupErrors;
 const messages=()=>document.getElementById('task-thread-messages');
 const view=()=>document.getElementById('task-thread-view');
 function debug(){const m=messages();return JSON.stringify({errors,url:location.href,readyState:document.readyState,htmx:typeof window.htmx,htmxTrigger:typeof window.htmx?.trigger,view:!!view(),messages:!!m,virtualizer:typeof window.renderVisibleChatRows,queue:window._chatContentRenderQueue?.length,active:window._chatContentRenderActive,top:m?.scrollTop,height:m?.scrollHeight,viewport:m?.clientHeight,hydrating:m?.dataset.transcriptHydrating,owners:Array.from(m?.querySelectorAll('[data-chat-virtual-owner]')||[],o=>({rows:o._chatVirtualRows?.length,text:o.textContent.length,revision:!!o._renderedRevision,rendering:!!o._renderingRevision,scheduled:!!o._scheduledChatRender,active:!!o._activeScheduledChatRender,raw:!!o.getAttribute('data-raw-content'),class:o.className,rect:[o.getBoundingClientRect().top,o.getBoundingClientRect().height],ready:o._chatVirtualRows?.filter(r=>r.ready).length}))})}
 function visible(){const m=messages();if(!m||getComputedStyle(m).visibility!=='visible')return false;const b=m.getBoundingClientRect();return Array.from(m.querySelectorAll('.chat-markdown,.stream-tool,details.stream-thinking')).some(el=>{const r=el.getBoundingClientRect();return el.textContent.trim()&&r.height>0&&r.bottom>b.top&&r.top<b.bottom})}
 (async()=>{
  await until(visible,'initial messages'); await settled('initial render settled');
 %s
  if(hiddenRow){
   const m=messages(),paint=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
   const owner=Array.from(m.querySelectorAll('[data-chat-virtual-owner]')).find(o=>o._chatVirtualRows?.[0]?.model.segments.some(s=>(s.content||'').length>=65536));
   if(!owner)throw Error('Missing large owner');const large=owner._chatVirtualRows[0];
   if(large.ready)throw Error('Large row already rendered');let release;const original=window.renderChatMarkdownAsync;
   window.renderChatMarkdownAsync=function(text,...args){if(text.length<65536)return original.call(this,text,...args);return new Promise(resolve=>{release=()=>resolve(original.call(this,text,...args))})};
   m.dispatchEvent(new WheelEvent('wheel',{deltaY:-100,bubbles:true}));
   m.scrollTop+=large.placeholder.getBoundingClientRect().bottom-m.getBoundingClientRect().top+50;
   m.dispatchEvent(new Event('scroll'));await paint();await until(()=>release && large.loading,'large row preparing');
   const bounds=m.getBoundingClientRect();
   const reading=owner._chatVirtualRows.find(row=>row.ready && row.node?.isConnected && row.node.getBoundingClientRect().top>=bounds.top && row.node.getBoundingClientRect().top<bounds.bottom);
   if(!reading)throw Error('Missing reading anchor');const top=reading.node.getBoundingClientRect().top;
   window.taskWorkspace.openChanges();release();await until(()=>large.ready,'large row finished hidden');
   for(let cycle=0;cycle<3;cycle++){
    window.taskWorkspace.openThread();
    // Check before the first visible paint, then again after virtual layout settles.
    const check=()=>{const el=reading.node.isConnected?reading.node:reading.placeholder;const shift=el.getBoundingClientRect().top-top;if(Math.abs(shift)>3)throw Error('Hidden large-row completion moved reading position '+shift+'px')};
    check();await paint();check();
    if(cycle<2)window.taskWorkspace.openChanges();
   }
   if(errors.length)throw Error('Browser errors '+JSON.stringify(errors));
   await fetch('/browser-result?status=pass');return;
  }

  if(scrollStability){
   const m=messages(), paint=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
   for(let page=0;page<4;page++){
    m.dispatchEvent(new WheelEvent('wheel',{deltaY:-200,bubbles:true}));
    m.scrollTop=50;await window.renderVisibleChatRows(m);await paint();
    m.scrollTop=8;
    const anchor=m.querySelector(':scope > [data-execution-pair]'), before=anchor.getBoundingClientRect().top;
    const count=m.querySelectorAll(':scope > [data-execution-pair]').length;
    let recording=true,maxShift=0;const trace=[];
    const sample=()=>{if(!recording)return;if(!m.clientHeight){requestAnimationFrame(sample);return;}const top=anchor.getBoundingClientRect().top;maxShift=Math.max(maxShift,Math.abs(top-before));trace.push({top,scroll:m.scrollTop,restoring:m.dataset.earlierAnchorRestoring,guard:!!m._earlierAnchor});requestAnimationFrame(sample)};
    requestAnimationFrame(sample);
    m.dispatchEvent(new WheelEvent('wheel',{deltaY:-200,bubbles:true}));
    if(hiddenPaging){
     await until(()=>m.dataset.earlierRequestLoading==='true','request started');
     window.taskWorkspace.openChanges();
    }
    await until(()=>m.querySelectorAll(':scope > [data-execution-pair]').length>count,'page appended');
    if(hiddenPaging){
     await until(()=>!window._chatContentRenderActive && !window._chatContentRenderQueue?.length,'hidden page render queue drained');await paint();
     if(!m._earlierAnchor)throw Error('Paging anchor discarded while Chat hidden');
     window.taskWorkspace.openThread();
    }
    await until(()=>m.dataset.earlierAnchorRestoring!=='true','page hydration');await settled('page render settled');recording=false;
    if(maxShift>3)throw Error('Page '+page+' moved visible content by '+maxShift+'px from '+before+' '+JSON.stringify(trace)+' '+debug());
    if(!visible())throw Error('Page '+page+' left empty viewport');
   }
   if(errors.length)throw Error('Browser errors '+JSON.stringify(errors));
   await fetch('/browser-result?status=pass');return;
  }
  const m=messages();m.dispatchEvent(new WheelEvent('wheel',{deltaY:-100000,bubbles:true}));m.scrollTop=0;m.dispatchEvent(new Event('scroll'));
  await until(()=>m.querySelectorAll(':scope > [data-execution-pair]').length>5,'older page');
  await until(()=>m.dataset.earlierAnchorRestoring!=='true','older page settled');await settled('history render settled');
  for(let round=0;round<2;round++){
   const pairs=Array.from(messages().querySelectorAll(':scope > [data-execution-pair]'));
   const older=pairs.slice(0,-5).flatMap(pair=>window.captureChatVirtualData(pair));
   const latest=pairs.slice(-5).flatMap(pair=>window.captureChatVirtualData(pair));
   if(bounded && (!older.length||!latest.length))throw Error('Missing parsed history fixture');
   await openVibelyNavigate(%q);await until(()=>view()?.dataset.taskId===%q,'other task');
   if(bounded){
    if(older.some(data=>window.findCachedChatVirtualData(data.key,data.source)))throw Error('Older row models survived in cache');
    if(!latest.every(data=>window.findCachedChatVirtualData(data.key,data.source)))throw Error('Latest row models missing from cache');
   }
   let previewCount=null,previewCursorValid=false;
   const preview=event=>{if(event.detail._ovCachedPreview){const t=document.createElement('template');t.innerHTML=event.detail.serverResponse;previewCount=t.content.querySelectorAll('[data-execution-pair]').length;const loader=t.content.querySelector('[data-earlier-loader]'),first=t.content.querySelector('[data-execution-pair]');previewCursorValid=!!loader && new URL(loader.getAttribute('hx-get'),location.href).searchParams.get('before')===first?.dataset.execId && loader.dataset.loading!=='true'}};
   document.addEventListener('htmx:beforeSwap',preview,true);
   await openVibelyNavigate(%q);await until(()=>view()?.dataset.taskId===%q,'return task');
   document.removeEventListener('htmx:beforeSwap',preview,true);
   if(bounded && !previewCursorValid)throw Error('Cached preview cannot reload earlier history');
   if(bounded && previewCount!==5)throw Error('Expected five cached executions, got '+previewCount);
   await until(visible,'visible returned messages');await settled('returned render settled');
   if(!visible())throw Error('Returned messages disappeared '+debug());
   if(errors.length)throw Error('Browser errors '+JSON.stringify(errors));
   if(bounded){
    const current=messages(),loader=current.querySelector('[data-earlier-loader]');
    const first=current.querySelector(':scope > [data-execution-pair]');
    if(!loader||new URL(loader.getAttribute('hx-get'),location.href).searchParams.get('before')!==first.dataset.execId)throw Error('Wrong cached history cursor');
    current.dispatchEvent(new WheelEvent('wheel',{deltaY:-100000,bubbles:true}));current.scrollTop=0;current.dispatchEvent(new Event('scroll'));
    await until(()=>current.querySelectorAll(':scope > [data-execution-pair]').length===10,'reloaded older page');
    await until(()=>current.dataset.earlierAnchorRestoring!=='true','reloaded older page settled');await settled('history render settled');
   }
  }
  await fetch('/browser-result?status=pass');
 })().catch(e=>fetch('/browser-result?status='+encodeURIComponent(String(e)+'\n'+(e.stack||''))));
 });</script>`, bounded, scrollStability, hiddenPaging, hiddenRow, nativeScript, "/tasks/"+other.ID, other.ID, path, task.ID)
	results := make(chan string, 1)
	var documentReady chan struct{}
	if nativeWheel {
		documentReady = make(chan struct{})
	}
	var documentDelivered sync.Once
	var bridgeMu sync.Mutex
	var nativeBridge string
	nativeInputFinished := make(chan struct{})
	var finishNativeInput sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "native-wheel-delayed" && r.URL.Path == "/native-input-finished" && r.Method == "POST" {
			finishNativeInput.Do(func() { close(nativeInputFinished) })
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if nativeWheel && r.URL.Path == "/native-test-bridge" && r.Method == "POST" {
			script, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
			if err != nil {
				http.Error(w, "invalid bridge", 400)
				return
			}
			bridgeMu.Lock()
			nativeBridge = "<script>" + string(script) + "</script>"
			bridgeMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if static.ServeAsset(w, r) {
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
		// Release history only after the browser has observed input completion.
		// Native dispatch and timer cadence vary across runners; elapsed time
		// cannot establish this ordering. Cancellation also releases the wait.
		if mode == "native-wheel-delayed" && r.URL.Query().Get("before") != "" {
			select {
			case <-nativeInputFinished:
			case <-r.Context().Done():
				return
			}
		}
		if hiddenPaging && r.URL.Query().Get("before") != "" {
			time.Sleep(150 * time.Millisecond)
		}
		if bounded && r.URL.Path == path && r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
			time.Sleep(200 * time.Millisecond)
		}
		if r.URL.Path == path && r.Header.Get("HX-Request") != "true" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			w.Header().Set("Content-Type", "text/html")
			bridgeMu.Lock()
			bridge := nativeBridge
			bridgeMu.Unlock()
			// Capture startup failures before dependencies and page initializers run.
			// DOMContentLoaded-only listeners miss the error that caused a lazy
			// transcript never to mount, leaving only an unhelpful timeout.
			html := strings.Replace(rec.Body.String(), "<head>", `<head><script>
try{localStorage.setItem('openvibely.outputDetail','full')}catch(_){}
window._pagedStartupErrors=[];
window.addEventListener('error',function(event){
 const target=event.target;
 window._pagedStartupErrors.push({message:event.message||'Resource failed to load',
  source:event.filename||target?.src||target?.href||'',line:event.lineno,column:event.colno,
  stack:event.error?.stack||'',readyState:document.readyState});
},true);
window.addEventListener('unhandledrejection',function(event){
 window._pagedStartupErrors.push({message:String(event.reason),stack:event.reason?.stack||'',readyState:document.readyState});
});
document.addEventListener('htmx:responseError',function(event){
 window._pagedStartupErrors.push({message:'HTMX response error',status:event.detail?.xhr?.status,
  url:event.detail?.xhr?.responseURL||'',target:event.detail?.target?.id||''});
});
</script>`, 1)
			_, _ = w.Write([]byte(strings.Replace(html, "</body>", bridge+runner+"</body>", 1)))
			if documentReady != nil {
				documentDelivered.Do(func() { close(documentReady) })
			}
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Unblock a pending history request if the browser fails before signalling.
	defer finishNativeInput.Do(func() { close(nativeInputFinished) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	outcome := waitPagedBrowserResult(ctx, documentReady, results, 40*time.Second)
	stopHandlerBrowserProcess(cmd)
	require.Equal(t, "pass", outcome, stderr.String())
}

// Native WebKit startup can consume most of the scenario budget on CI. Bound
// startup separately, then allow the full scenario budget from document delivery.
// Asset loading and transcript initialization remain part of the scenario. Only
// the first document counts; navigation must not extend the deadline again.
// A nil documentReady preserves the single budget used by Chrome tests.
func waitPagedBrowserResult(ctx context.Context, documentReady <-chan struct{}, results <-chan string, budget time.Duration) string {
	if documentReady != nil {
		startup := time.NewTimer(budget)
		defer startup.Stop()
		select {
		case result := <-results:
			return result
		case <-documentReady:
		case <-startup.C:
			return "timeout: native startup (initial document not delivered)"
		case <-ctx.Done():
			return ctx.Err().Error()
		}
	}
	scenario := time.NewTimer(budget)
	defer scenario.Stop()
	select {
	case result := <-results:
		return result
	case <-scenario.C:
		return "timeout: browser scenario"
	case <-ctx.Done():
		return ctx.Err().Error()
	}
}
