package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// Instrument only the math observer, preserving the production shell's other work.
// The baseline is the old callback on this equation-free performance fixture.
func mathDiscoveryServer(t *testing.T, initial string) *httptest.Server {
	t.Helper()
	base := renderTerminalBrowserComponent(t, layout.Base("Math discovery", nil, ""))
	script := `<script src="` + static.URL("file-math.js") + `"></script>`
	var transcript strings.Builder
	transcript.WriteString(`<div id="math-transcript">`)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&transcript, `<article><header>User %d</header><div><p>Plain text with <strong>formatting</strong>.</p><pre><code>example output</code></pre></div></article>`, i)
	}
	transcript.WriteString(`</div><div id="math-fragment"></div>`)
	instrumentation := `<script>
 window.mathStats={queries:0,queryTime:0,discovery:0};window.mathBaseline=false;
 const mathQuery=document.querySelectorAll.bind(document);
 document.querySelectorAll=function(s){if(!s.includes('data-math-tex'))return mathQuery(s);const start=performance.now();const result=mathQuery(s);mathStats.queries++;mathStats.queryTime+=performance.now()-start;return result};
 const mathElementQuery=Element.prototype.querySelectorAll;
 Element.prototype.querySelectorAll=function(s){if(!s.includes('data-math-tex'))return mathElementQuery.call(this,s);const start=performance.now();const result=mathElementQuery.call(this,s);mathStats.queryTime+=performance.now()-start;return result};
 window.NativeMathObserver=window.MutationObserver;
 window.MutationObserver=class extends NativeMathObserver{constructor(callback){super(records=>{const start=performance.now();if(window.mathBaseline)Array.from(document.querySelectorAll('[data-math-tex]:not([data-math-ready])')).filter(node=>!node.closest('[data-thread-refresh-preview]'));else callback(records);mathStats.discovery+=performance.now()-start})}};
 window.mathAttempts=0;window.mathRenders=[];
 const mathAppend=document.head.append.bind(document.head);
 document.head.append=(...nodes)=>{const script=nodes.find(n=>n.src?.includes('katex.min.js'));if(script){mathAttempts++;if(location.search.includes('retry')&&mathAttempts===1){queueMicrotask(()=>script.dispatchEvent(new Event('error')));return}const loaded=script.onload;script.onload=()=>{const render=katex.render;katex.render=(tex,node,options)=>{mathRenders.push({tex,options});return render(tex,node,options)};loaded()}}mathAppend(...nodes)};
 </script>`
	replacement := transcript.String() + initial + instrumentation + script + `<script>window.MutationObserver=window.NativeMathObserver;</script>`
	if !strings.Contains(base, script) {
		t.Fatal("math script missing from production shell")
	}
	fixture := strings.Replace(base, script, replacement, 1)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, fixture)
	}))
}

func TestBrowserFunctional_MathDiscovery(t *testing.T) {
	server := mathDiscoveryServer(t, `<span id="initial-inline" data-math-tex="x=1"></span><div id="initial-display" data-math-tex="y=2" data-math-display="block"></div><div data-thread-refresh-preview><span data-math-tex="excluded"></span></div>`)
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL+"/?retry", "math-discovery", func(b *composerFocusCDP) {
		b.waitFor("failed initial load", `String(document.readyState==='complete'&&mathAttempts===1)`, "true")
		b.evaluate(`document.getElementById('math-fragment').innerHTML='<div><span data-math-tex="z=3"></span></div>'; 'ok'`)
		b.waitFor("retry renders all queued equations", `String(mathRenders.length===3 && !!document.querySelector('#initial-display .katex-display'))`, "true")
		result := b.evaluateAwait(`(async()=>{
   if(mathAttempts!==2)throw Error('wrong load attempt count');
   if(mathRenders.map(r=>r.tex).sort().join(',')!=='x=1,y=2,z=3'||!document.querySelector('#initial-inline .katex')||!document.querySelector('#math-fragment .katex'))throw Error('queued equations not rendered once');
   if(mathStats.queries!==1)throw Error('load completion rescanned document');
   for(const {tex,options} of mathRenders){if(options.throwOnError!==false||options.trust!==false||options.maxExpand!==1000||options.maxSize!==20||options.displayMode!==(tex==='y=2'))throw Error('render options changed')}
   if(document.querySelector('[data-thread-refresh-preview] [data-math-ready]'))throw Error('preview typeset');
   const fragment=document.getElementById('math-fragment');
   for(let i=0;i<20;i++){fragment.replaceChildren(document.createTextNode('stream '+i));await new Promise(resolve=>queueMicrotask(resolve))}
   const direct=document.createElement('span');direct.dataset.mathTex='a=4';fragment.append(direct);
   await new Promise(resolve=>setTimeout(resolve,0));
   if(!direct.querySelector('.katex')||mathRenders.length!==4)throw Error('added root not rendered');
   fragment.replaceChildren();fragment.append(direct);
   await new Promise(resolve=>setTimeout(resolve,0));
   if(mathRenders.length!==4||mathStats.queries!==1)throw Error('duplicate rendering or full scan');
   return 'pass';
  })()`)
		if result != "pass" {
			t.Fatal(result)
		}
		b.call("Page.navigate", map[string]any{"url": server.URL}, nil)
		b.waitFor("initial inline and display equations", `String(window.mathRenders?.length===2 && mathAttempts===1 && !!document.querySelector('#initial-inline .katex') && !!document.querySelector('#initial-display .katex-display'))`, "true")
	})
}

// Opt in: 30 paired runs of 500 animation frames take about nine minutes.
func TestBrowserPerformance_MathDiscovery(t *testing.T) {
	if os.Getenv("OPENVIBELY_MATH_PROFILE") != "1" {
		t.Skip("set OPENVIBELY_MATH_PROFILE=1 for the 30 x 500-frame profile")
	}
	server := mathDiscoveryServer(t, "")
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL, "math-discovery-profile", func(b *composerFocusCDP) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		b.ctx = ctx
		b.waitFor("page ready without loading KaTeX", `String(document.readyState==='complete' && mathAttempts===0 && !window.katex)`, "true")
		b.evaluate(`window.profileMathUpdates=async function(baseline){
   window.mathBaseline=baseline;window.mathStats={queries:0,queryTime:0,discovery:0};const durations=[];
   const transcript=document.getElementById('math-transcript'),fragment=document.getElementById('math-fragment');
   for(let i=0;i<500;i++)await new Promise(resolve=>requestAnimationFrame(()=>{
    const start=performance.now(),p=document.createElement('p');p.textContent='Streaming update '+i;
    if(i%10===0)fragment.replaceChildren(p);else transcript.lastElementChild.replaceChildren(p);
    queueMicrotask(()=>{durations.push(performance.now()-start);resolve()});
   }));
   durations.sort((a,b)=>a-b);return {...mathStats,p95:durations[Math.ceil(durations.length*.95)-1]};
  };window.mathResults={before:[],after:[]};'ok'`)
		for i := 0; i < 30; i++ {
			result := b.evaluateAwait(fmt.Sprintf(`(async()=>{for(const baseline of %s){const r=await profileMathUpdates(baseline);mathResults[baseline?'before':'after'].push(r)}return JSON.stringify(mathResults)})()`, map[bool]string{true: "[true,false]", false: "[false,true]"}[i%2 == 0]))
			t.Logf("pair %d: %s", i+1, result)
		}
		result := b.evaluate(`JSON.stringify(Object.fromEntries(Object.entries(mathResults).map(([key,runs])=>{const median=k=>{const values=runs.map(r=>r[k]).sort((a,b)=>a-b);return (values[14]+values[15])/2};return [key,{queries:runs.reduce((n,r)=>n+r.queries,0),queryTime:median('queryTime'),discovery:median('discovery'),p95:median('p95')}]})))`)
		t.Logf("median profile: %s", result)
		var stats map[string]struct {
			Queries                   int
			QueryTime, Discovery, P95 float64
		}
		if err := json.Unmarshal([]byte(result), &stats); err != nil {
			t.Fatal(err)
		}
		if stats["before"].Queries != 15000 || stats["after"].Queries != 0 || stats["after"].QueryTime > stats["before"].QueryTime*.1 || stats["after"].P95 > stats["before"].P95 {
			t.Fatalf("discovery acceptance failed: %s", result)
		}
	})
}
