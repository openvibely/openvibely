package components

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Set OPENVIBELY_VIRTUAL_SCROLL_BASELINE to the baseline chat-virtual-list.js
// to run 30 alternating paired trials and enforce the timing acceptance limits.
// The normal regression test enforces geometry scaling and visible content.
func TestBrowserPerformance_VirtualScrollRowCountScaling(t *testing.T) {
	chrome := testChromePath(t)
	current, err := os.ReadFile(filepath.Join("..", "..", "assets", "chat-virtual-list.js"))
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{"current": string(current)}
	if path := os.Getenv("OPENVIBELY_VIRTUAL_SCROLL_BASELINE"); path != "" {
		baseline, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sources["baseline"] = string(baseline)
	}
	for key, source := range sources {
		// Include refresh and ResizeObserver anchor work without double-counting
		// nested calls. The frame probe counts all row/placeholder geometry reads.
		for _, signature := range []string{"refresh(list, hydrating)", "compensateLateLayout(list)"} {
			name := strings.Split(signature, "(")[0]
			args := strings.TrimSuffix(strings.Split(signature, "(")[1], ")")
			declaration := "    function " + signature + " {"
			if !strings.Contains(source, declaration) {
				t.Fatalf("missing measurement hook %s", signature)
			}
			source = strings.Replace(source, declaration, fmt.Sprintf(`
    function %s {
        const outer = window.workDepth++ === 0, start = performance.now();
        try { return measured_%s(%s); }
        finally { window.workDepth--; if (outer) window.workMS += performance.now()-start; }
    }
    function measured_%s {`, signature, name, args, signature), 1)
		}
		sources[key] = source
	}
	type report struct {
		Error     string          `json:"error"`
		Results   json.RawMessage `json:"results"`
		UserAgent string          `json:"userAgent"`
	}
	reports := make(chan report, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/report" {
			var result report
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				result.Error = err.Error()
			}
			reports <- result
			return
		}
		if source, ok := sources[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, source)
			return
		}
		if r.URL.Path == "/fixture" {
			fmt.Fprint(w, `<!doctype html><style>body{margin:0}#messages{height:900px;overflow:auto;width:800px} [data-chat-lazy-row]{box-sizing:border-box}</style><div id="messages" data-chat-transcript><div id="owner"></div></div><script>
window.rowReads=0; window.samples=[]; window.measuring=false;window.workDepth=0;window.workMS=0;
function recordFrame(){
 if(window.measuring && (window.workMS || window.rowReads))window.samples.push({ms:window.workMS,reads:window.rowReads});
 window.workMS=0;window.rowReads=0;requestAnimationFrame(recordFrame);
}
requestAnimationFrame(recordFrame);
const originalBounds=Element.prototype.getBoundingClientRect;
Element.prototype.getBoundingClientRect=function(){if(this.hasAttribute('data-chat-lazy-row') || this.hasAttribute('data-virtual-chat-row'))window.rowReads++;return originalBounds.call(this)};
</script><script src="/`+r.URL.Query().Get("version")+`"></script><script>
const messages=document.getElementById('messages'), owner=document.getElementById('owner');
const paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
window.run=async function(count){
const models=Array.from({length:count},(_,i)=>({kind:'text',height:100,width:800,segments:[{content:'Row '+i}]}));
await installChatVirtualRows(owner,{models,ungrouped:true},(node,model,index)=>{node.style.height='100px';node.textContent='Row '+index});
await paint(); await paint();
// Same physical offsets and direction changes in each version; include distant
// windows, both transcript ends, and short reversals after long jumps.
const max=count*100-900;
const sequence=[0,300,600,900,600,900,1800,max*.25,max*.25+300,max*.5,max*.5+300,max*.5-300,max*.75,max,max-300,max-600,max-300,max*.75,max*.5,max*.25,1800,900,1200,600,0];
window.samples=[];window.workMS=0;window.rowReads=0; window.measuring=true;
for(const top of sequence){messages.scrollTop=top;await paint();
 if(Math.abs(messages.scrollTop-top)>1)throw Error('Scroll sequence drifted at '+top);
 for(let i=Math.floor(messages.scrollTop/100);i<Math.min(count,Math.ceil((messages.scrollTop+900)/100));i++){
  const el=owner.children[i];if(!el || el.hasAttribute('data-virtual-chat-row') || el.textContent!=='Row '+i)throw Error('Missing visible content at '+top);
 }
}
await paint();window.measuring=false;
if(owner.querySelectorAll('[data-chat-lazy-row]:not([data-virtual-chat-row])').length>=40)throw Error('Mounted row bound exceeded');
return window.samples;
};</script>`)
			return
		}
		fmt.Fprintf(w, `<!doctype html><script>
const paired=%t;
const median=a=>{a.sort((x,y)=>x-y);return(a[Math.floor((a.length-1)/2)]+a[Math.floor(a.length/2)])/2};
const p95=a=>a.sort((x,y)=>x-y)[Math.ceil(a.length*.95)-1];
async function trial(version,count){const frame=document.createElement('iframe');frame.width=820;frame.height=920;frame.src='/fixture?version='+version;document.body.append(frame);await new Promise(resolve=>frame.onload=resolve);try{return await frame.contentWindow.run(count)}finally{frame.remove()}}
window.onload=async()=>{try{
const results=[];
for(const count of [200,1000,5000]){
 const runs={current:[],baseline:[]};
 for(let i=0;i<(paired?30:2);i++){
  for(const version of (paired?(i%%2?['current','baseline']:['baseline','current']):['current'])){
   const samples=await trial(version,count);if(!samples.length)throw Error('No scroll samples');
   runs[version].push({ms:p95(samples.map(s=>s.ms)),reads:p95(samples.map(s=>s.reads))});
  }
 }
 const result={count};for(const version of (paired?['baseline','current']:['current']))result[version]={ms:median(runs[version].map(r=>r.ms)),reads:median(runs[version].map(r=>r.reads))};results.push(result);
}
if(results[2].current.reads>2*results[0].current.reads)throw Error('Geometry scales with row count: '+JSON.stringify(results));
if(paired && (results[2].current.ms>results[2].baseline.ms*.5 || results[0].current.ms>results[0].baseline.ms*1.1))throw Error('Timing acceptance failed: '+JSON.stringify(results));
await fetch('/report',{method:'POST',body:JSON.stringify({results,userAgent:navigator.userAgent})});
}catch(error){await fetch('/report',{method:'POST',body:JSON.stringify({error:String(error.stack||error)})})}};
</script>`, len(sources) == 2)
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-features=CalculateNativeWinOcclusion", "--window-size=1100,1100", "--no-first-run", server.URL)
	log, err := os.Create(filepath.Join(t.TempDir(), "chrome.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	configureTestBrowserProcess(cmd)
	if err := startTestBrowserProcess(t, cmd); err != nil {
		t.Fatal(err)
	}
	defer stopTestBrowserProcess(cmd)
	select {
	case result := <-reports:
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		t.Logf("%s; median run-level p95: %s", result.UserAgent, result.Results)
	case <-time.After(5 * time.Minute):
		t.Fatal("scroll scaling browser timed out")
	}
}
