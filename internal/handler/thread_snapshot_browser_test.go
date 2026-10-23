package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
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
 const streams=[], releasedToolStates=[];
 window.releaseToolOutputState=id=>releasedToolStates.push(id);
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
  assert(!record('denied')&&denied.output==='','denied execution retained private output');assert(releasedToolStates.includes('denied'),'denied execution retained tool state');denied.listener.close();
  // A reset received while the HTTP seed is ahead must replace that seed.
  let reset=view('reset');await tick();const resetStream=streams.at(-1);reset.listener.close();
  reset=view('reset','long stale seed');await tick();resetStream.emit('reset','new');
  assert(reset.output==='new','reset did not replace an ahead HTTP seed');resetStream.emit('done','completed');reset.listener.close();
  active=[];
  // Only a mounted viewer is exempt. Background output must not renew idle age.
  const foreground=view('foreground');await tick();
  now+=29*60*1000;remote.emit('message',' still running');
  now+=2*60*1000;cache.sweep();
  assert(!record('a')&&!record('c')&&!record('remote','other')&&record('foreground'),'idle running snapshots did not expire or the viewer was evicted');
  assert(cs.closed&&remote.closed,'expiry did not unsubscribe background streams');
  for(let i=0;i<22;i++){const v=view('idle'+i);await tick();streams.at(-1).emit('done','completed');v.listener.close();now++;}
  cache.sweep();assert(!record('idle0')&&record('idle21')&&record('foreground'),'count eviction/viewer exemption incorrect');
  const big=view('large');await tick();const largeStream=streams.at(-1);
  largeStream.emit('message','x'.repeat(33*1024*1024));cache.sweep();
  assert(record('large'),'displayed output evicted by background byte budget');
  big.listener.close();cache.sweep();
  assert(!record('large')&&largeStream.closed,'oversize running output retained after leaving');
  largeStream.emit('message','late');largeStream.emit('done','completed');
  assert(!record('large'),'late callbacks resurrected an evicted run');
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
  cache.clear();
  // Mixed accounting: unique output, HTML and parsed-row strings total
  // exactly 64 MiB including one segment's 128-byte estimate. References to
  // the same strings across both page shells and source/model fields count
  // only once within the thread. No single category is over budget alone.
  active=[];
  const MiB=1024*1024;
  const mixedOutput='o'.repeat(8*MiB), mixedHTML='h'.repeat(8*MiB);
  const mixedNormalized='n'.repeat(4*MiB), mixedContent='c'.repeat(4*MiB);
  const mixedResult='r'.repeat(4*MiB), mixedSecondary='s'.repeat(4*MiB-64);
  const accountingView=view('accounting',mixedOutput);await tick();
  const accountingStream=streams.at(-1);accountingView.listener.close();
  const accountingSegment={content:mixedContent,resultOutput:mixedResult,secondary:mixedSecondary};
  const accountingPage={threadKey:cache.threadKey('p','accounting'),time:now,
   runKeys:[record('accounting').key],html:mixedHTML,
   sources:[{text:mixedOutput},{text:mixedNormalized}],
   virtual:[{source:mixedOutput,normalized:mixedNormalized,models:[{segments:[accountingSegment]}]}]};
  cache.pages.set('accounting-page',accountingPage);
  cache.pages.set('accounting-alias',{threadKey:accountingPage.threadKey,time:now,
   runKeys:accountingPage.runKeys,html:mixedHTML,sources:[{text:mixedContent},{text:mixedResult},{text:mixedSecondary}],virtual:[]});
  cache.sweep();
  assert(record('accounting')&&cache.pages.size===2&&!accountingStream.closed,'shared strings counted twice or exact size limit rejected');
  // Change both references together so only two accounted bytes are added.
  accountingSegment.secondary += 'x';
  cache.pages.get('accounting-alias').sources[2].text=accountingSegment.secondary;
  cache.sweep();
  assert(!record('accounting')&&cache.pages.size===0&&accountingStream.closed,'combined HTML/output/parsed-row size did not evict the whole thread');
  cache.clear();
  // Admit new running tasks one at a time: 21 and 22 each displace the
  // least recently viewed background task, not the mounted foreground view.
  active=[];
  const countForeground=view('count-foreground');await tick();
  const countStreams=[];
  for(let i=0;i<20;i++){
   now++;
   active.push({id:'count'+i,project_id:'p',task_id:'count-task'+i,category:'active'});
   await cache.discover();countStreams.push(streams.at(-1));
  }
  assert(cache.runs.size===21&&countStreams.every(s=>!s.closed),'initial 20 background tasks plus foreground not retained');
  // New output must not make the oldest unviewed task most recently viewed.
  countStreams[0].emit('message','still running');
  for(let i=20;i<22;i++){
   now++;
   active.push({id:'count'+i,project_id:'p',task_id:'count-task'+i,category:'active'});
   await cache.discover();countStreams.push(streams.at(-1));
   const firstRetained=i-19;
   assert(cache.runs.size===21&&record('count-foreground'),'sequential admission evicted foreground or exceeded count');
   countStreams.forEach((stream,index)=>{
    assert(!!record('count'+index)===(index>=firstRetained),'wrong count victim after admitting '+i+': '+index);
    assert(stream.closed===(index<firstRetained),'wrong subscription lifetime after admitting '+i+': '+index);
   });
   const subscriptions=streams.length;
   countStreams.slice(0,firstRetained).forEach(s=>s.emit('message','late output'));
   await cache.discover();await cache.discover();
   assert(streams.length===subscriptions&&!record('count0'),'count victims were rediscovered or resurrected');
  }
  countForeground.listener.close();cache.clear();

  // Four 12 MiB background records start below the 64 MiB budget. A viewed
  // record exceeding that budget is exempt. Grow the newest background record
  // twice: first evict one old record, then evict two more in the same sweep.
  active=[];
  const sizeForeground=view('size-foreground');await tick();
  const sizeForegroundStream=streams.at(-1);
  sizeForegroundStream.emit('message','f'.repeat(33*1024*1024));
  const sizeStreams=[];
  for(let i=0;i<4;i++){
   now++;
   active.push({id:'size'+i,project_id:'p',task_id:'size-task'+i,category:'active'});
   await cache.discover();const s=streams.at(-1);sizeStreams.push(s);
   s.emit('message',String(i).repeat(6*1024*1024));cache.sweep();
  }
  assert(cache.runs.size===5&&sizeStreams.every(s=>!s.closed),'under-budget background records were evicted');
  const growths=[9,13], victims=[1,3];
  for(let step=0;step<growths.length;step++){
   sizeStreams[3].emit('message','g'.repeat(growths[step]*1024*1024));cache.sweep();
   assert(record('size-foreground')&&!sizeForegroundStream.closed,'byte eviction removed displayed record');
   assert(cache.runs.size===5-victims[step],'wrong number of byte-budget evictions');
   sizeStreams.forEach((stream,index)=>{
    assert(!!record('size'+index)===(index>=victims[step]),'wrong byte-budget victim: '+index);
    assert(stream.closed===(index<victims[step]),'byte-budget victim subscription not closed: '+index);
   });
   const retainedBytes=Array.from(cache.runs.values()).filter(r=>r.id!=='size-foreground').reduce((sum,r)=>sum+2*r.output.length,0);
   assert(retainedBytes<=64*1024*1024,'retained background output still exceeds budget');
   const subscriptions=streams.length;
   sizeStreams.slice(0,victims[step]).forEach(s=>s.emit('message','late output'));
   await cache.discover();await cache.discover();
   assert(streams.length===subscriptions&&cache.runs.size===5-victims[step],'byte-budget victims were rediscovered or resurrected');
  }
  sizeForeground.listener.close();cache.clear();
  // Automatic discovery shares the 20-thread budget and does not cycle through
  // evicted work on every scan. Reopening explicitly admits an evicted thread.
  active=Array.from({length:24},(_,i)=>({id:'discover'+i,project_id:'p',task_id:'task'+i,category:'active'}));
  await cache.discover();assert(cache.runs.size===20,'discovery exceeded thread count');
  const discoveredCount=streams.length;
  const evicted=active.find(item=>!record(item.id));
  const evictedStream=streams.find(s=>s.id===evicted.id);
  assert(evictedStream.closed,'count eviction kept its subscription');
  assert(releasedToolStates.includes(evicted.id),'count eviction retained tool state');
  await cache.discover();await cache.discover();
  assert(streams.length===discoveredCount,'discovery immediately reloaded evicted output');
  const owner={closest(selector){return selector==='[data-task-id]'?{getAttribute:()=>evicted.task_id}:null},setAttribute(){}};
  const reopened=cache.execution(evicted.id,'p','current HTTP output',owner);await tick();
  assert(record(evicted.id)?.output==='current HTTP output'&&streams.at(-1).offset===bytes('current HTTP output'),'reopening failed to resume evicted run from HTTP');
  reopened.close();const afterReopen=streams.length;
  await cache.discover();assert(cache.runs.size===20&&streams.length===afterReopen,'reopened admission caused discovery churn');
  cache.clear();
  assert(releasedToolStates.includes(undefined),'clear retained tool state');
  // Background growth is enforced without waiting for terminal output or the
  // five-second discovery interval; completed and running data share one budget.
  active=[{id:'growing',project_id:'p',task_id:'growing-task',category:'active'}];
  await cache.discover();const growing=streams.at(-1);
  growing.emit('message','g'.repeat(33*1024*1024));
  await new Promise(r=>setTimeout(r,30));
  assert(!record('growing')&&growing.closed,'growing background output exceeded byte budget');
  const afterGrowth=streams.length;await cache.discover();assert(streams.length===afterGrowth,'oversize run rediscovered');
  // Expiration likewise remains suppressed while the backend still runs it.
  cache.clear();await cache.discover();const expiring=streams.at(-1);
  now+=29*60*1000;expiring.emit('message','busy');now+=2*60*1000;cache.sweep();
  const afterExpiry=streams.length;await cache.discover();
  assert(!record('growing')&&expiring.closed&&streams.length===afterExpiry,'streaming or discovery renewed idle expiry');
  cache.clear();
  // Starting another execution while away must not renew the thread's idle age.
  active=[{id:'older-run',project_id:'p',task_id:'aging-thread',category:'active'}];
  await cache.discover();now+=29*60*1000;
  active.push({id:'newer-run',project_id:'p',task_id:'aging-thread',category:'active'});
  await cache.discover();now+=2*60*1000;cache.sweep();
  assert(!record('older-run')&&!record('newer-run'),'new background execution renewed thread expiry');
  cache.clear();
  // A completed thread can still be displayed after its stream listener closes.
  const displayed=document.createElement('div');displayed.id='task-thread-view';
  displayed.dataset.taskId='displayed-task';displayed.dataset.projectId='p';displayed.textContent='Visible thread';document.body.appendChild(displayed);
  const displayedListener=cache.execution('displayed','p','',displayed);await tick();
  streams.at(-1).emit('message','v'.repeat(33*1024*1024));streams.at(-1).emit('done','completed');displayedListener.close();
  now+=31*60*1000;cache.sweep();assert(record('displayed'),'displayed completed thread lost its exemption');
  displayed.remove();cache.sweep();assert(!record('displayed'),'departed thread kept display exemption');
  cache.clear();
  // Opening the newest run before discovery must not make it the first one
  // pruned. Include sub-millisecond timestamps and equal-time insertion-order tie breaks.
  let knownStart=null;
  const executionOwner={closest(selector){
   if(selector==='[data-task-id]')return {getAttribute:()=> 'ordered-task'};
   if(selector==='[data-execution-pair]')return {getAttribute:()=> knownStart};
   return null;
  },setAttribute(){}};
  const newest=cache.execution('ordered-6','p','latest output',executionOwner);await tick();
  const newestStream=streams.at(-1);
  active=Array.from({length:6},(_,i)=>({id:'ordered'+String.fromCharCode(122-i),project_id:'p',task_id:'ordered-task',category:'active',started_at:'2026-10-22T01:00:00.00000'+(i+1)+'Z'}));
  active[5].id='ordered-6';
  await cache.discover();newest.close();
  assert(record('ordered-6')&&!newestStream.closed&&!record('orderedz'),'admission order evicted newest viewed execution');
  const orderedCount=streams.length;await cache.discover();
  assert(streams.length===orderedCount&&record('ordered-6'),'discovery lost newest execution or restarted pruned output');
  newestStream.emit('message',' background');newestStream.emit('done','completed');
  assert(record('ordered-6').output==='latest output background','newest background final output missing');
  cache.clear();
  // Discovering old work after a newer run completed must preserve the latter,
  // even when that completed run is no longer in the active inventory.
  knownStart='2026-10-22T01:00:00.000006Z';
  const settled=cache.execution('settled-newest','p','final output',executionOwner);await tick();
  streams.at(-1).emit('done','completed');settled.close();
  active=active.slice(0,5).reverse();await cache.discover();
  assert(record('settled-newest')&&!record('orderedz'),'old discovery displaced newer completed output');
  cache.clear();
  active=Array.from({length:6},(_,i)=>({id:'tie-'+i,project_id:'p',task_id:'ties',category:'active',started_at:'2026-10-22T01:00:00Z',history_order:String(9007199254740990n+BigInt(5-i))})).reverse();
  await cache.discover();assert(record('tie-0')&&!record('tie-5'),'equal timestamps retained random ID order instead of history insertion order');
  cache.clear();
  // A run completed before discovery uses metadata from its rendered pair.
  const tiedOwner={closest(selector){
   if(selector==='[data-task-id]')return {getAttribute:()=> 'ties'};
   if(selector==='[data-execution-pair]')return {getAttribute:name=>name==='data-started-at'?'2026-10-22T01:00:00Z':'9007199254740995'};
   return null;
  },setAttribute(){}};
  const completedTie=cache.execution('a-completed','p','final',tiedOwner);await tick();
  streams.at(-1).emit('done','completed');completedTie.close();
  active=active.filter(item=>item.id!=='tie-0');await cache.discover();
  assert(record('a-completed')&&!record('tie-5'),'completed page metadata lost insertion order');
  cache.clear();
  // Five executions is a limit on running history too, not just finished runs.
  active=Array.from({length:8},(_,i)=>({id:'parallel'+i,project_id:'p',task_id:'parallel-task',category:'active'}));
  await cache.discover();assert(cache.runs.size===5&&!record('parallel0')&&record('parallel7'),'running execution window was not bounded');
  const parallelCount=streams.length;await cache.discover();assert(streams.length===parallelCount,'pruned executions were rediscovered');
  cache.clear();
  // Revisiting an old thread changes LRU order; it is not FIFO admission.
  active=[];
  for(let i=0;i<20;i++){
   now++;active.push({id:'lru'+i,project_id:'p',task_id:'lru-task'+i,category:'active'});
   await cache.discover();
  }
  now++;
  const lruOwner={closest:selector=>selector==='[data-task-id]'?{getAttribute:()=> 'lru-task0'}:null,setAttribute(){}};
  const revisited=cache.execution('lru0','p','',lruOwner);await tick();revisited.close();
  now++;active.push({id:'lru20',project_id:'p',task_id:'lru-task20',category:'active'});
  const lruVictim=streams.find(s=>s.id==='lru1');
  await cache.discover();
  assert(record('lru0')&&!record('lru1')&&record('lru20')&&lruVictim.closed,'revisit did not change eviction order');
  cache.clear();active=[];

  // Four projects share the budget. Chat is one thread per project, even when
  // its executions have different backend task IDs.
  for(let project=0;project<4;project++){
   for(let index=0;index<5;index++){
    now++;active.push({id:'mixed'+project+'-'+index,project_id:'project'+project,task_id:'task'+index,category:index===4?'chat':'active'});
    await cache.discover();
   }
  }
  now++;active.push({id:'chat-second',project_id:'project3',task_id:'another-chat-task',category:'chat'});
  await cache.discover();
  assert(cache.runs.size===21&&record('mixed0-0','project0'),'Chat executions counted as separate threads');
  assert(record('mixed3-4','project3').task==='chat'&&record('chat-second','project3').task==='chat','Chat grouping lost');
  const mixedVictim=streams.find(s=>s.id==='mixed0-0'&&s.project==='project0');
  now++;active.push({id:'mixed-new',project_id:'project3',task_id:'new-task',category:'active'});
  await cache.discover();
  assert(!record('mixed0-0','project0')&&mixedVictim.closed&&record('mixed-new','project3'),'global limit did not evict across projects');
  assert(record('mixed0-1','project0')&&record('mixed3-4','project3')&&record('chat-second','project3'),'global eviction removed unrelated task or Chat');
  const mixedSubscriptions=streams.length;await cache.discover();
  assert(streams.length===mixedSubscriptions,'cross-project victim rediscovered');
  cache.clear();active=[];

  // Network, server and malformed-response failures preserve retained output.
  const normalFetch=window.fetch;
  const preserved=view('preserved','saved output');await tick();preserved.listener.close();
  const preservedStream=streams.at(-1);
  const failures=[async()=>{throw Error('offline')},async()=>({ok:false,status:500}),
   async()=>({ok:true,json:async()=>{throw Error('invalid JSON')}}),async()=>({ok:true,json:async()=>({unexpected:true})})];
  for(const failure of failures){
   window.fetch=failure;await cache.discover();
   assert(record('preserved')?.output==='saved output'&&!preservedStream.closed,'discovery failure discarded retained data');
  }
  for(const status of [401,403]){
   const authorized=view('authorized'+status,'private output');await tick();
   const privateStream=streams.at(-1);
   cache.pages.set('private-page',{threadKey:cache.threadKey('p','authorized'+status),time:now,html:'private',sources:[]});
   window.fetch=async()=>({ok:false,status});await cache.discover();
   assert(cache.runs.size===0&&cache.pages.size===0&&privateStream.closed&&authorized.listener.closed,'authorization failure retained data or listeners');
  }
  window.fetch=normalFetch;

  // Explicit cleanup invalidates an in-flight discovery, even if its fetch
  // implementation delivers an already-buffered body after abort.
  let resolveBody, requestedSignal;
  window.fetch=async(_,options)=>{
   requestedSignal=options.signal;
   return {ok:true,json:()=>new Promise(resolve=>{resolveBody=resolve})};
  };
  const pendingDiscovery=cache.discover();await tick();
  assert(resolveBody,'discovery has not reached response body');
  cache.clear();const beforeLateDiscovery=streams.length;
  resolveBody([{id:'late-discovery',project_id:'p',task_id:'late-task',category:'active'}]);
  await pendingDiscovery;
  assert(cache.runs.size===0&&streams.length===beforeLateDiscovery,'late discovery recreated subscriptions after cleanup');
  assert(requestedSignal.aborted,'cleanup did not abort discovery');
  window.fetch=normalFetch;active=[{id:'after-clear',project_id:'p',task_id:'after-clear',category:'active'}];
  await cache.discover();assert(record('after-clear'),'cleanup prevented subsequent discovery');
  cache.clear();

  // An old request's finally block must not clear a newer in-flight request.
  let finishOld, finishNew, requests=0;
  window.fetch=async()=>{requests++;return {ok:true,json:()=>new Promise(resolve=>{if(requests===1)finishOld=resolve;else finishNew=resolve})}};
  const oldRequest=cache.discover();await tick();cache.clear();
  const newRequest=cache.discover();await tick();
  finishOld([]);await oldRequest;
  await cache.discover();assert(requests===2,'old discovery completion unlocked an active newer request');
  finishNew([{id:'new-request',project_id:'p',task_id:'new-task',category:'active'}]);await newRequest;
  assert(record('new-request'),'new discovery was discarded after old request cleanup');
  cache.clear();

  // Leaving the document stops discovery permanently. A late response cannot
  // repopulate the cache; ordinary in-app navigation still keeps subscriptions.
  window.fetch=async()=>({ok:true,json:()=>new Promise(resolve=>{resolveBody=resolve})});
  const leavingDiscovery=cache.discover();await tick();
  const beforeLeave=streams.length;
  window.dispatchEvent(new Event('pagehide'));
  resolveBody(active);await leavingDiscovery;
  assert(cache.runs.size===0&&streams.length===beforeLeave,'pagehide allowed late discovery');
  navigator.sendBeacon('/result?value=pass');
 }catch(error){navigator.sendBeacon('/result?value='+encodeURIComponent(error.stack||error));}
 })();</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, doc)
		case "/native-test-bridge":
			// This fixture needs no native input, but can also run in the Wails host.
			w.WriteHeader(http.StatusNoContent)
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
	testTaskBackgroundSnapshotNavigation(t, false, false)
}

func TestTaskEvictedSnapshotNavigationInChrome(t *testing.T) {
	testTaskBackgroundSnapshotNavigation(t, true, false)
}

// Keep deltas arriving faster than the renderer's quiet period. Cached history
// must become visible without waiting for the run (or its HTTP refresh) to finish.
func TestTaskRunningSnapshotNavigationInChrome(t *testing.T) {
	testTaskBackgroundSnapshotNavigation(t, false, true)
}

func testTaskBackgroundSnapshotNavigation(t *testing.T, evict, running bool) {
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
	if running {
		prior := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted; ex.PromptSent = "Earlier question" })
		require.NoError(t, h.execRepo.Complete(t.Context(), prior.ID, models.ExecCompleted, "Earlier answer", "", 0, 0))
	}
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
  if(%t){
   source().time=Date.now()-31*60*1000;
   openVibelyThreadCache.pages.forEach(page=>page.time=Date.now()-31*60*1000);
   openVibelyThreadCache.sweep();assert(!source(),'expired background task was not evicted');
   await openVibelyThreadCache.discover();assert(!source(),'discovery reloaded evicted task');
   await fetch('/test/complete',{method:'POST'});
   await openVibelyNavigate(%q);
   await wait(()=>view()?.dataset.taskId===%q&&Array.from(view().querySelectorAll('[data-raw-content]')).some(n=>n.dataset.rawContent==='initial final'));
   await fetch('/test/result?value=pass');return;
  }
  if(%t){
   // Cached restoration must not depend on the stream's quiet-period deadline.
   window._chatRenderMaxLiveDeferralMS=60000;
   await fetch('/test/start',{method:'POST'});
   const visible=()=>{const m=document.getElementById('task-thread-messages');return m&&getComputedStyle(m).visibility==='visible'&&m.textContent.includes('initial')&&m.textContent.includes('Earlier answer')};
   let returned=false;
   const navigation=openVibelyNavigate(%q).then(()=>returned=true);
   await wait(()=>view()?.dataset.taskId===%q&&visible());
   assert(!returned,'running preview waited for HTTP');
   await fetch('/test/release',{method:'POST'});await navigation;
   await wait(visible);
   await wait(()=>visible()&&document.getElementById('task-thread-messages').textContent.includes('continuing'));
   const renderedLength=document.getElementById('task-thread-messages').textContent.length;
   await wait(()=>visible()&&document.getElementById('task-thread-messages').textContent.length>renderedLength);
   assert(source().live&&!source().terminal,'test completed before live return');
   await fetch('/test/result?value=pass');return;
  }
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
 }catch(error){await fetch('/test/result?value='+encodeURIComponent(String(error)+'\n'+(error.stack||'')))}
});</script>`, project.ID, ex.ID, task.ID, "/tasks/"+other.ID, other.ID, evict, path, task.ID, running, path, task.ID, path, task.ID)
	result := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	stopOutput := make(chan struct{})
	var outputWorkers sync.WaitGroup
	defer func() { close(stopOutput); outputWorkers.Wait() }()
	var startOutput sync.Once

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
		case "/test/start":
			startOutput.Do(func() {
				outputWorkers.Add(1)
				go func() {
					defer outputWorkers.Done()
					ticker := time.NewTicker(50 * time.Millisecond)
					defer ticker.Stop()
					output := "initial"
					for {
						select {
						case <-stopOutput:
							return
						case <-ticker.C:
							output += " continuing"
							if err := h.execRepo.FinalizeOutput(t.Context(), ex.ID, output); err != nil {
								return
							}
							h.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: ex.ID, Type: events.ExecutionStreamDelta, Delta: " continuing", Offset: len(output)})
						}
					}
				}()
			})
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
