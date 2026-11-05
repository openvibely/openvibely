package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func sheetDiscoveryServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/sheet" {
			if r.URL.Query().Has("view") {
				_ = components.FilePreview("table.csv", "table", "", "/sheet").Render(r.Context(), w)
			} else {
				io.WriteString(w, "Name,Value\nAlpha,42\nBeta,84")
			}
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/initial" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			io.WriteString(w, `<div id="chat-transcript">`)
			for i := 0; i < 400; i++ {
				fmt.Fprintf(w, `<article class="chat-message"><header>User %d</header><div class="chat-markdown"><p>A fixed transcript message with <strong>formatted text</strong> and no previews.</p><pre><code>example output</code></pre></div></article>`, i)
			}
			io.WriteString(w, `</div><div id="task-fragment"></div>`)
			if r.URL.Path == "/initial" {
				return components.FilePreview("table.csv", "table", "", "/sheet").Render(ctx, w)
			}
			return nil
		})
		_ = layout.Base("Sheets", []models.Project{{ID: "p", Name: "P"}}, "p").Render(templ.WithChildren(r.Context(), child), w)
	}))
}

func TestBrowserFunctional_SheetRegistration(t *testing.T) {
	server := sheetDiscoveryServer()
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL, "sheet-registration", func(b *composerFocusCDP) {
		b.waitFor("viewer ready", `String(!!window.OVFileViewer)`, "true")
		b.waitFor("no spreadsheet download on page without controls", `String(!window.OVFileSheets && !performance.getEntriesByType('resource').some(r=>r.name.includes('file-sheets.js')))`, "true")
		b.evaluate(`const appendScript=document.head.append.bind(document.head);document.head.append=(...nodes)=>{const script=nodes.find(node=>node.src?.includes('file-sheets.js'));if(script){document.head.append=appendScript;queueMicrotask(()=>script.dispatchEvent(new Event('error')))}else appendScript(...nodes)};'ok'`)
		b.evaluate(`window.sheetScans=0;const query=document.querySelectorAll.bind(document);document.querySelectorAll=function(s){if(s.includes('data-spreadsheet-url'))window.sheetScans++;return query(s)};window.OVFileViewer.open('/sheet');'ok'`)
		b.waitFor("script download retry", `String(!!document.querySelector('[data-sheet-retry]'))`, "true")
		b.click("[data-sheet-retry]")
		b.waitFor("direct preview", `String(document.querySelector('[data-sheet-grid]')?.textContent.includes('Alpha'))`, "true")
		b.waitFor("single spreadsheet script", `String(document.querySelectorAll('script[src*="file-sheets.js"]').length)`, "1")
		b.evaluate(`document.querySelector('#image-gallery').close();document.querySelector('[data-result-content]').replaceChildren();'ok'`)
		// Use the production worker for direct opening above, then deterministic worker
		// failures and multiple sheets to exercise retries and tabs independently of parsing.
		b.evaluate(`window.RealSheetWorker=Worker;window.workerAttempts=0;window.Worker=class{postMessage(){window.workerAttempts++;setTimeout(()=>{if(window.workerAttempts===1)this.onerror();else this.onmessage({data:{sheets:[{name:'First',rows:Array.from({length:1000},(_,i)=>['Row '+i,'42']),totalRows:1000,totalColumns:2},{name:'Second',rows:[['Second sheet']],totalRows:1,totalColumns:1}]}})},0)}terminate(){}};'ok'`)
		b.evaluateAwait(`(async()=>{document.querySelector('#task-fragment').innerHTML=await (await fetch('/sheet?view=1')).text();return 'ok'})()`)
		b.waitFor("worker error retry", `String(!!document.querySelector('#task-fragment [data-sheet-retry]'))`, "true")
		b.click("#task-fragment [data-sheet-retry]")
		b.waitFor("dynamic sheet tabs and bounded rows", `String(document.querySelectorAll('#task-fragment [data-sheet-tabs] button').length===2 && document.querySelectorAll('#task-fragment tbody tr').length<50 && document.querySelector('#task-fragment [data-sheet-grid]').textContent.includes('Row 1'))`, "true")
		b.click("#task-fragment [data-sheet-tabs] button:nth-child(2)")
		b.waitFor("second sheet", `String(document.querySelector('#task-fragment [data-sheet-grid]').textContent.includes('Second sheet'))`, "true")
		b.evaluate(`window.Worker=window.RealSheetWorker;window.realFetch=fetch;window.fetch=async(...args)=>{if(String(args[0]).includes('raw=1')){window.fetch=window.realFetch;return {ok:false}}return window.realFetch(...args)};document.querySelector('#task-fragment').replaceChildren();'ok'`)
		b.evaluateAwait(`(async()=>{document.querySelector('#task-fragment').innerHTML=await (await fetch('/sheet?view=1')).text();return 'ok'})()`)
		b.waitFor("fetch error retry", `String(!!document.querySelector('#task-fragment [data-sheet-retry]'))`, "true")
		b.click("#task-fragment [data-sheet-retry]")
		b.waitFor("fetch retry recovered", `String(document.querySelector('#task-fragment [data-sheet-grid]').textContent.includes('Alpha'))`, "true")
		b.evaluate(`document.querySelector('#chat-transcript').append(document.createElement('p'));document.querySelector('#task-fragment').replaceChildren();'ok'`)
		b.waitFor("no document discovery after unrelated updates", `String(window.sheetScans)`, "0")
		b.call("Page.navigate", map[string]any{"url": server.URL + "/initial"}, nil)
		b.waitFor("initial server-rendered sheet", `String(document.querySelector('[data-sheet-grid]')?.textContent.includes('Alpha'))`, "true")
	})
}

// Opt in: 30 paired runs of 500 real animation frames take about nine minutes.
func TestBrowserPerformance_SheetDiscovery(t *testing.T) {
	if os.Getenv("OPENVIBELY_SHEET_PROFILE") != "1" {
		t.Skip("set OPENVIBELY_SHEET_PROFILE=1 for the 30 x 500-frame profile")
	}
	server := sheetDiscoveryServer()
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL, "sheet-discovery-profile", func(b *composerFocusCDP) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		b.ctx = ctx
		b.waitFor("viewer ready", `String(!!window.OVFileViewer)`, "true")
		b.evaluate(`window.profileSheetUpdates=async function(baseline){
   const transcript=document.querySelector('#chat-transcript'),fragment=document.querySelector('#task-fragment');
   let callbacks=0,discovery=0;const durations=[];
   // The previous discovery callback, with only timing added. There are no sheets
   // in this fixture, so its render callback never ran in the previous version.
   const observer=baseline?new MutationObserver(()=>{const start=performance.now();callbacks++;document.querySelectorAll('[data-spreadsheet-url]:not([data-loaded])');discovery+=performance.now()-start}):null;
   observer?.observe(document.body,{childList:true,subtree:true});
   for(let i=0;i<500;i++)await new Promise(resolve=>requestAnimationFrame(()=>{
    const start=performance.now();const p=document.createElement('p');p.textContent='Streaming update '+i;
    if(i%10===0)fragment.replaceChildren(p);else transcript.lastElementChild.replaceChildren(p);
    queueMicrotask(()=>{durations.push(performance.now()-start);resolve()});
   }));
   observer?.disconnect();durations.sort((a,b)=>a-b);
   return {callbacks,discovery,p95:durations[Math.ceil(durations.length*.95)-1]};
  };window.profileResults={before:[],after:[]};'ok'`)
		for i := 0; i < 30; i++ {
			// Alternate order to reduce systematic warmup/drift bias.
			result := b.evaluateAwait(fmt.Sprintf(`(async()=>{for(const baseline of %s){const r=await profileSheetUpdates(baseline);profileResults[baseline?'before':'after'].push(r)}return JSON.stringify(profileResults)})()`, map[bool]string{true: "[true,false]", false: "[false,true]"}[i%2 == 0]))
			t.Logf("pair %d: %s", i+1, result)
		}
		result := b.evaluate(`JSON.stringify(Object.fromEntries(Object.entries(profileResults).map(([key,runs])=>{const median=k=>{const values=runs.map(r=>r[k]).sort((a,b)=>a-b);return (values[14]+values[15])/2};return [key,{callbacks:runs.reduce((n,r)=>n+r.callbacks,0),discovery:median('discovery'),p95:median('p95')}]})))`)
		t.Logf("median profile: %s", result)
		var stats map[string]struct {
			Callbacks      int
			Discovery, P95 float64
		}
		if err := json.Unmarshal([]byte(result), &stats); err != nil {
			t.Fatal(err)
		}
		if stats["before"].Callbacks != 15000 || stats["after"].Callbacks != 0 || stats["after"].Discovery > stats["before"].Discovery*.1 || stats["after"].P95 > stats["before"].P95 {
			t.Fatalf("discovery acceptance failed: %s", result)
		}
	})
}
