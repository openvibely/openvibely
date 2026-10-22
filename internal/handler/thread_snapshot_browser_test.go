package handler

import (
	"fmt"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
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

// Use the production snapshot owner with a controlled transport. No rendering is
// mounted: background output, retention and view lifetimes must be independent.
func TestThreadSnapshotsBackgroundLifecycleInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	result := make(chan string, 1)
	doc := `<!doctype html><html><head><script>
 const streams=[];
 let active=[], now=Date.now();
 Date.now=()=>now;
 const bytes=s=>new TextEncoder().encode(s).length;
 window.fetch=async()=>({ok:true,json:async()=>active});
 window.openVibelyLive={execution(id,offset,project){
  const s={id,offset,project,closed:false,listeners:{},close(){this.closed=true},addEventListener(n,f){(this.listeners[n]??=[]).push(f)}};
  s.emit=(name,data)=>{if(name==='message')s.offset+=bytes(data);if(name==='reset')s.offset=bytes(data);s['on'+name]?.({data});(s.listeners[name]||[]).forEach(f=>f({data}));};
  streams.push(s);return s;
 }};
 </script><script src="` + static.URL("thread-snapshots.js") + `"></script></head><body><script>
 (async()=>{
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 const tick=()=>Promise.resolve();
 const cache=openVibelyThreadCache;
 const record=(id,project='p')=>cache.runs.get(JSON.stringify([project,id]));
 function view(id,seed='',project='p'){
  let output=seed, terminal=null;
  const listener=cache.execution(id,project,seed);
  listener.onmessage=e=>output+=e.data;
  listener.addEventListener('reset',e=>output=e.data);
  listener.addEventListener('done',e=>terminal=e.data);
  return {listener,get output(){return output},get terminal(){return terminal}};
 }
 try {
  let a=view('a','hé'); await tick(); const stream=streams[0];
  stream.emit('message','llo');assert(a.output==='héllo','initial UTF8 output');
  let b=view('a','hé');await tick();assert(b.output==='héllo'&&streams.length===1,'view did not reuse snapshot/subscription');
  a.listener.close();b.listener.close();
  assert(!stream.closed&&record('a').views.size===0,'navigation closed background subscription');
  stream.emit('message',' 日本語');
  assert(record('a').output==='héllo 日本語','background output missing');
  b=view('a','hé');await tick();assert(b.output==='héllo 日本語','reopen did not catch up');
  stream.emit('reset','short');assert(b.output==='short','reset did not replace output');
  b.listener.close();
  // A status-list disappearance cannot release a stream before its final bytes.
  active=[];await cache.discover();assert(record('a').live,'discovery treated absence as completion');
  stream.emit('message',' final');stream.emit('done','cancelled');
  assert(!record('a').live,'terminal did not release keepalive');
  b=view('a','short');await tick();assert(b.output==='short final'&&b.terminal==='cancelled'&&streams.length===1,'completed reopen did not replay snapshot and terminal');
  b.listener.close();
  // Fresh HTTP output may lead the stream replay; it must not be duplicated.
  let c=view('c');await tick();const cs=streams.at(-1);c.listener.close();
  c=view('c','ahead');await tick();assert(c.output==='ahead','ahead HTTP seed discarded');
  cs.emit('message','ahead');assert(c.output==='ahead','replay duplicated HTTP seed');
  cs.emit('message','!');assert(c.output==='ahead!','future delta lost');c.listener.close();
  // Legacy display normalization must not change the server's byte cursor.
  let normalized='[Tool]';
  const normalizedView=cache.execution('normalized','p',normalized,null,bytes('[Legacy tool]'));
  normalizedView.addEventListener('reset',e=>normalized=e.data);
  normalizedView.onmessage=e=>normalized+=e.data;
  await tick();const normalizedStream=streams.at(-1);
  assert(normalizedStream.offset===0,'normalized display bytes used as server cursor');
  normalizedStream.emit('message','[Legacy tool]');normalizedStream.emit('message',' next');
  assert(normalized==='[Legacy tool] next','normalized HTTP seed corrupted canonical output');
  normalizedStream.emit('done','completed');normalizedView.close();
  // Discover running threads in other projects without ever opening them.
  active=[{id:'remote',project_id:'other',task_id:'task',category:'active'}];
  await cache.discover();const remote=streams.at(-1);remote.emit('message','unviewed');
  assert(record('remote','other').output==='unviewed','unviewed thread not retained');
  // A stale shell can be saved after discovery has already started the new run.
  cache.pages.set('stale',{threadKey:cache.threadKey('other','task'),executionIDs:['old']});
  await cache.discover();assert(!cache.pages.has('stale'),'already-discovered run left an obsolete cached shell');
  // Connection capacity is retryable and must not report a failed execution.
  let limited=view('limited');await tick();const rejected=streams.at(-1);
  rejected.emit('error','subscription limit reached');
  assert(record('limited').live&&!record('limited').terminal,'capacity error ended running snapshot');
  limited.listener.close();await cache.discover();const retry=streams.at(-1);
  assert(retry!==rejected&&retry.id==='limited','capacity rejection was not retried');
  retry.emit('message','caught up');retry.emit('done','completed');
  // Revoked/deleted executions must not retain output for future cached opens.
  let denied=view('denied','private');await tick();const deniedStream=streams.at(-1);
  deniedStream.emit('error','subscription denied');
  assert(!record('denied')&&denied.output==='','denied execution retained private output');denied.listener.close();
  // A reset received while the HTTP seed is ahead must replace that seed.
  let reset=view('reset');await tick();const resetStream=streams.at(-1);reset.listener.close();
  reset=view('reset','long stale seed');await tick();resetStream.emit('reset','new');
  assert(reset.output==='new','reset did not replace an ahead HTTP seed');resetStream.emit('done','completed');reset.listener.close();
  active=[];
  // Pin running data across idle expiry; completed data expires normally.
  now+=31*60*1000;cache.sweep();
  assert(!record('a')&&record('c')&&record('remote','other'),'idle expiry removed active runs or retained completed run');
  // Capacity eviction includes unseen finished threads and preserves live ones.
  for(let i=0;i<22;i++){const v=view('idle'+i);await tick();streams.at(-1).emit('done','completed');v.listener.close();now++;}
  cache.sweep();assert(!record('idle0')&&record('idle21')&&record('c'),'count eviction/pinning incorrect');
  const big=view('large');await tick();const largeStream=streams.at(-1);
  largeStream.emit('message','x'.repeat(33*1024*1024));big.listener.close();cache.sweep();
  assert(record('large'),'active output evicted by idle byte budget');
  largeStream.emit('done','completed');cache.sweep();assert(!record('large'),'oversize settled output retained');
  const container={closest(selector){return selector==='[data-task-id]'?{getAttribute:()=> 'same-task'}:null},setAttribute(){}};
  for(let i=0;i<7;i++){
   const listener=cache.execution('turn'+i,'p','',container);await tick();
   streams.at(-1).emit('done','completed');listener.close();now++;
  }
  assert(Array.from(cache.runs.values()).filter(run=>run.task==='same-task').length===5&&!record('turn0')&&record('turn6'),'settled execution window was not bounded');
  cache.clear();assert(cache.runs.size===0&&cache.pages.size===0&&remote.closed&&cs.closed,'cleanup leaked subscriptions/data');
  // Six 12 MiB estimates exceed the budget, but the retained five total 60 MiB.
  // Prune the sixth record before accounting for idle bytes.
  let oldestOutput;
  for(let i=0;i<6;i++){
   const listener=cache.execution('large-turn'+i,'p','',container);await tick();
   const output=String(i).repeat(6*1024*1024);
   if(i===0)oldestOutput=output;
   streams.at(-1).emit('message',output);streams.at(-1).emit('done','completed');listener.close();now++;
  }
  assert(cache.runs.size===5&&!record('large-turn0')&&record('large-turn5'),'pruned output still counted toward byte budget and evicted the retained five');
  cache.sweep();assert(cache.runs.size===5,'subsequent sweep evicted the retained five');
  // A page can still hold a pruned source. Those bytes remain resident and must
  // continue to count, rather than being subtracted merely because its run left.
  cache.pages.set('older-shell',{threadKey:cache.threadKey('p','same-task'),time:now,html:'',sources:[{text:oldestOutput}],virtual:[]});
  cache.sweep();assert(cache.runs.size===0&&cache.pages.size===0,'page-held pruned output was excluded from byte budget');
  navigator.sendBeacon('/result?value=pass');
 }catch(error){navigator.sendBeacon('/result?value='+encodeURIComponent(error.stack||error));}
 })();</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, doc)
		case "/result":
			select {
			case result <- r.URL.Query().Get("value"):
			default:
			}
			w.WriteHeader(204)
		default:
			static.Handler().ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case got := <-result:
		require.Equal(t, "pass", got)
	case <-time.After(45 * time.Second):
		t.Fatal("snapshot browser test timed out")
	}
}

// Exercise task resume streaming through the real lazy thread endpoint, including
// final output received with no mounted page and a cached return before HTTP.
func TestTaskBackgroundSnapshotNavigationInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	h, e, configs := setupTestHandler(t)
	h.SetExecutionStreamHub(events.NewExecutionStreamHub())
	agent := createAgent(t, configs)
	project := createProject(t, h, "Background tasks")
	task := createTask(t, h, project.ID, "Running task", func(task *models.Task) { task.Status = models.StatusRunning })
	other := createTask(t, h, project.ID, "Other task", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
	})
	ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.PromptSent = "Keep running while away" })
	require.NoError(t, h.execRepo.FinalizeOutput(t.Context(), ex.ID, "initial"))
	path := "/tasks/" + task.ID
	runner := fmt.Sprintf(`<script>
window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let n=0;n<1000;n++){if(fn())return;await new Promise(r=>setTimeout(r,10));}throw Error('timeout: '+fn)};
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 const source=()=>openVibelyThreadCache.source(JSON.stringify([%q,%q]));
 const view=()=>document.getElementById('task-thread-view');
 try{
  await wait(()=>source()?.views.size>0&&view()?.dataset.taskId===%q);
  await openVibelyNavigate(%q);
  await wait(()=>view()?.dataset.taskId===%q&&source()?.views.size===0);
  assert(source().live,'navigation released running data');
  testSockets[0].close();
  await fetch('/test/complete',{method:'POST'});
  await wait(()=>source()?.terminal);
  assert(source().output==='initial final'&&!source().live,'background completion lost final output');
  assert(testSockets.length===2,'background subscription did not reconnect');
  let returned=false;
  const navigation=openVibelyNavigate(%q).then(()=>{returned=true});
  await wait(()=>view()?.dataset.taskId===%q&&Array.from(view().querySelectorAll('[data-raw-content]')).some(n=>n.dataset.rawContent==='initial final'));
  assert(!returned,'current output waited for HTTP');
  await fetch('/test/release',{method:'POST'});
  await navigation;
  await wait(()=>Array.from(view().querySelectorAll('[data-raw-content]')).some(n=>n.dataset.rawContent==='initial final'));
  await fetch('/test/result?value=pass');
 }catch(error){await fetch('/test/result?value='+encodeURIComponent(error.stack||error))}
});</script>`, project.ID, ex.ID, task.ID, "/tasks/"+other.ID, other.ID, path, task.ID)
	result := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/test/result":
			select {
			case result <- r.URL.Query().Get("value"):
			default:
			}
			w.WriteHeader(204)
			return
		case "/native-test-bridge":
			// This scenario uses navigation, not wheel input; acknowledge the
			// native host handshake without installing an input driver.
			w.WriteHeader(http.StatusNoContent)
			return
		case "/test/release":
			unlock()
			w.WriteHeader(204)
			return
		case "/test/complete":
			if err := h.execRepo.Complete(r.Context(), ex.ID, models.ExecCompleted, "initial final", "", 0, 0); err != nil {
				t.Error(err)
			}
			h.executionStreamHub.CloseTerminal(ex.ID, models.ExecCompleted, "")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == path && r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Path == path && r.Header.Get("HX-Request") == "" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			w.Header().Set("Content-Type", "text/html")
			html := strings.Replace(rec.Body.String(), "<head>", `<head><script>
window.testSockets=[];
const NativeSocket=window.WebSocket;
window.WebSocket=class extends NativeSocket {constructor(...args){super(...args);testSockets.push(this)}};
</script>`, 1)
			fmt.Fprint(w, strings.Replace(html, "</body>", runner+"</body>", 1))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer unlock()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+path)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case got := <-result:
		require.Equal(t, "pass", got)
	case <-time.After(30 * time.Second):
		t.Fatal("task background snapshot timeout")
	}
}
