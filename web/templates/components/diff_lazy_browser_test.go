package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserFunctional_DiffLazyFilesAndLayouts(t *testing.T) {
	runDiffLazyBrowser(t, "")
}

func TestBrowserFunctional_DiffLazyScrollPreservation(t *testing.T) {
	for _, kind := range []string{"added", "deleted", "mixed"} {
		t.Run(kind, func(t *testing.T) { runDiffLazyBrowser(t, kind) })
	}
}

func runDiffLazyBrowser(t *testing.T, scrollKind string) {
	chrome := testChromePath(t)
	oldCount, newCount := 1, 1
	switch scrollKind {
	case "added":
		newCount = 151
	case "deleted":
		oldCount = 151
	case "mixed":
		oldCount, newCount = 103, 103
	}
	var diff strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&diff, "diff --git a/file%d.go b/file%d.go\n--- a/file%d.go\n+++ b/file%d.go\n@@ -1,%d +1,%d @@\n-old%d\n+new%d\n", i, i, i, i, oldCount, newCount, i, i)
		switch scrollKind {
		case "added", "deleted":
			prefix := "+"
			if scrollKind == "deleted" {
				prefix = "-"
			}
			for j := 0; j < 150; j++ {
				fmt.Fprintf(&diff, "%sextra line %d\n", prefix, j)
			}
		case "mixed":
			for _, count := range []int{25, 75} {
				for j := 0; j < count; j++ {
					fmt.Fprintf(&diff, "-old block line %d\n", j)
				}
				for j := 0; j < count; j++ {
					fmt.Fprintf(&diff, "+new block line %d\n", j)
				}
				fmt.Fprint(&diff, " context divider\n")
			}
		}
	}
	comments := []models.ReviewComment{{ID: "note", TaskID: "lazy-task", FilePath: "file0.go", LineNumber: 1, LineType: "new", CommentText: "existing review"}}
	var markup bytes.Buffer
	if err := DiffViewer(diff.String(), "lazy-task", comments, "inline").Render(context.Background(), &markup); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markup.String(), `<tr`) {
		t.Fatal("initial task viewer eagerly renders diff rows")
	}
	var mu sync.Mutex
	counts := map[string]int{}
	resultChannel := make(chan string, 1)
	runner := `<script>
  let requests=[],aborts=0;
  const originalFetch=window.fetch;
  window.fetch=function(url,options){
   if(String(url).includes('/changes/file')) {
    requests.push(String(url));
    if(options.signal)options.signal.addEventListener('abort',()=>aborts++);
   }
   return originalFetch.apply(this,arguments);
  };
 window.addEventListener('load',async function(){
  const result=document.getElementById('fixture-root');
  const wait=async function(check){for(let i=0;i<400;i++){if(check())return;await new Promise(r=>requestAnimationFrame(r));}throw Error('condition timed out: '+check.toString()+'; requests='+JSON.stringify(requests)+'; statuses='+Array.from(document.querySelectorAll('[data-diff-load-status]')).map(e=>e.textContent).join('|'));};
  const assert=(value,message)=>{if(!value)throw Error(message);};
  const state=diffFileStateBucket();state[1]=false;state[2]=false;window._restoreDiffFileState();
  const count=(index,view)=>requests.filter(u=>u.includes('file_index='+index)&&u.includes('view='+view)).length;
  try {
   await wait(()=>document.querySelector('#diff-file-0 tr'));
   assert(!document.querySelector('#diff-content-split tr'),'hidden split rows loaded');
   assert(count(1,'inline')===0&&count(2,'inline')===0,'saved collapsed file fetched');
   assert(document.querySelector('#diff-file-0 .review-comment-text').textContent==='existing review','review comment missing');
   toggleDiffFile(1);
   await wait(()=>document.querySelector('#diff-file-1 [data-diff-retry]:not(.hidden)'));
   document.querySelector('#diff-file-1 [data-diff-retry]').click();
   toggleDiffFile(1); // Collapse while retry is in flight.
   await wait(()=>document.querySelector('#diff-file-1 tr'));
   assert(document.getElementById('diff-body-1').style.maxHeight==='0px','late response reopened collapsed file');
   toggleDiffFile(1);
   assert(count(1,'inline')===2,'reopening loaded file fetched it again: '+JSON.stringify(requests));
   switchDiffView('split',false);
   await wait(()=>document.querySelector('#diff-file-split-0 tr')&&document.querySelector('#diff-file-split-1 tr'));
   assert(count(2,'split')===0,'collapsed split file loaded');
   assert(document.querySelector('#diff-file-split-0 .review-comment-text').textContent==='existing review','split review comment missing');
   switchDiffView('inline',false);
   assert(count(0,'inline')===1&&count(1,'inline')===2,'switching back refetched cached layout');
   await new Promise(r=>setTimeout(r,60));
   const scroller=document.getElementById('main-content');
   const filler=document.createElement('div');filler.style.height='1000px';scroller.appendChild(filler);
   const header=document.querySelector('#diff-file-1 .diff-file-header');
   let headerScans=0;
   const queryAll=Element.prototype.querySelectorAll;
   Element.prototype.querySelectorAll=function(selector){if(selector.includes('.diff-file-header'))headerScans++;return queryAll.apply(this,arguments);};
   scroller.scrollTop+=header.getBoundingClientRect().top-scroller.getBoundingClientRect().top+5;
   await wait(()=>header.getAttribute('data-stuck')==='1');
   scroller.scrollTop=0;
   await wait(()=>!header.hasAttribute('data-stuck'));
   Element.prototype.querySelectorAll=queryAll;
   assert(headerScans===0,'scrolling scanned all file headers');
   filler.remove();
   // Live diff refresh uses innerHTML, so scripts do not execute again.
   // The shared controller must initialize the new viewer and restore state.
   const fragment=await (await originalFetch('/fragment')).text();
   scroller.innerHTML=fragment;
   switchDiffView('inline',false);
   await wait(()=>document.querySelector('#diff-file-0 tr')&&document.querySelector('#diff-file-1 tr'));
   assert(count(2,'inline')===0,'live refresh fetched a saved collapsed file');
   toggleDiffFile(2);
   await wait(()=>count(2,'inline')===1);
   await htmx.ajax('GET','/other',{target:'#main-content',swap:'innerHTML'});
   assert(aborts>0,'navigation did not abort file request');
   assert(!document.getElementById('diff-viewer'),'old viewer remained');
   result.setAttribute('data-test-result','pass');
  }catch(error){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(error));}
 await originalFetch('/result?status='+result.dataset.testResult+'&error='+encodeURIComponent(result.dataset.testError||''));
 });
 </script>`
	if scrollKind != "" {
		start := strings.Index(runner, "   assert(!document.querySelector")
		end := strings.Index(runner, "   result.setAttribute('data-test-result','pass');")
		runner = runner[:start] + `
   const scroller=document.getElementById('main-content');
   scroller.scrollTop=1700;
   await new Promise(r=>setTimeout(r,60));
   const matches=(a,b)=>(a.newLine ? a.newLine===b.newLine : a.oldLine>0 && a.oldLine===b.oldLine) && Math.abs(a.offset-b.offset)<2;
   const before=window._captureDiffScroll();
   switchDiffView('split',false);
   await wait(()=>document.querySelector('#diff-file-split-0 tr'));
   await new Promise(r=>setTimeout(r,60));
   const split=window._captureDiffScroll();
   assert(matches(before.anchor,split.anchor),'layout switch lost visible line');
   const fragment=await (await originalFetch('/fragment')).text();
   const saved=window._captureDiffScroll();
   document.getElementById('diff-viewer')._diffPerformance.destroy();
   scroller.innerHTML=fragment;
   await new Promise(r=>requestAnimationFrame(r));
   switchDiffView('split',false);
   window._restoreDiffScroll(saved,true);
   await wait(()=>document.querySelector('#diff-file-split-0 tr'));
   await new Promise(r=>setTimeout(r,60));
   const refreshed=window._captureDiffScroll();
   assert(matches(saved.anchor,refreshed.anchor),'live refresh lost visible line');
   switchDiffView('inline',false);
   await new Promise(r=>setTimeout(r,60));
   assert(matches(refreshed.anchor,window._captureDiffScroll().anchor),'switch back lost visible line');
   assert(count(1,'split')===0 && count(2,'split')===0,'restoration loaded collapsed files');
  ` + runner[end:]
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/result":
			select {
			case resultChannel <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("error"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx204)
		case "/fragment":
			fmt.Fprint(w, markup.String())
		case "/other":
			fmt.Fprint(w, `<div>Other project</div>`)
		case "/tasks/lazy-task/changes/file":
			index, _ := strconv.Atoi(r.URL.Query().Get("file_index"))
			view := r.URL.Query().Get("view")
			key := fmt.Sprint(index) + view
			mu.Lock()
			counts[key]++
			attempt := counts[key]
			mu.Unlock()
			if r.URL.Query().Get("file_path") != fmt.Sprintf("file%d.go", index) {
				http.Error(w, "missing file identity", 400)
				return
			}
			if index == 1 && view == "inline" && attempt == 1 {
				http.Error(w, "temporary failure", 500)
				return
			}
			if index == 2 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
			if err := DiffChunk(ParseDiffOutput(diff.String())[index], index, view, "lazy-task", comments, true, 0, 1, DiffCodeRevision(ParseDiffOutput(diff.String())[index])).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script>`+fmt.Sprintf(`<script>window._diffFileState={};window._diffFileState[%q]={1:false,2:false};</script>`, diffViewerStateKey(progressiveDiffMetas(ParseDiffOutput(diff.String()), nil), "lazy-task"))+runner+`<style>svg{width:16px;height:16px}.diff-file-header{display:flex;align-items:center}#main-content{height:600px;overflow:auto}.hidden{display:none}.diff-file-body{overflow:hidden}.diff-file-header{position:sticky;top:0}td{height:20px}</style></head><body><main id="fixture-root" data-test-result="pending"></main><div id="main-content">`+markup.String()+`</div></body></html>`)
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL)
	configureTestBrowserProcess(cmd)
	if err := startTestBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	defer stopTestBrowserProcess(cmd)
	select {
	case result := <-resultChannel:
		if result != "pass:" {
			t.Fatal(result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for lazy diff browser")
	}
}

func TestBrowserFunctional_DiffOversizedFilePlaceholder(t *testing.T) {
	chrome := testChromePath(t)
	diff := "diff --git a/small.go b/small.go\n--- a/small.go\n+++ b/small.go\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/huge.txt b/huge.txt\n--- a/huge.txt\n+++ b/huge.txt\n@@ -0,0 +1,50001 @@\n" + strings.Repeat("+x\n", maxDiffFileLines+1)
	var markup bytes.Buffer
	if err := DiffViewer(diff, "lazy-task", nil, "inline").Render(context.Background(), &markup); err != nil {
		t.Fatal(err)
	}
	files := ParseDiffOutput(diff)
	resultChannel := make(chan string, 1)
	runner := `<script>
 window.addEventListener('load',async function(){
  const result=document.getElementById('fixture-root');
  const requests=[];const originalFetch=window.fetch;
  window.fetch=function(url){if(String(url).includes('/changes/file'))requests.push(String(url));return originalFetch.apply(this,arguments);};
  const wait=async function(check){for(let i=0;i<400;i++){if(check())return;await new Promise(r=>requestAnimationFrame(r));}throw Error('timed out: '+check.toString());};
  const assert=(value,message)=>{if(!value)throw Error(message);};
  try {
   await wait(()=>document.querySelector('#diff-file-0 tr'));
   for (const id of ['diff-file-1','diff-file-split-1']) {
    if (id.includes('split')) switchDiffView('split',false);
    await wait(()=>document.getElementById(id));
    const card=document.getElementById(id);
    assert(card.textContent.includes('Diff too large to display'),'missing placeholder in '+id);
    assert(card.textContent.includes('huge.txt')&&card.textContent.includes('+50001')&&card.textContent.includes('-0'),'missing counts in '+id);
    assert(!card.hasAttribute('data-diff-lazy-url'),'placeholder is lazy-loadable');
   }
   toggleAllDiffFiles(false);toggleAllDiffFiles(true);
   await new Promise(r=>setTimeout(r,200));
   assert(!requests.some(u=>u.includes('file_index=1')),'oversized file fetched: '+JSON.stringify(requests));
   result.setAttribute('data-test-result','pass');
  }catch(error){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(error));}
  await originalFetch('/result?status='+result.dataset.testResult+'&error='+encodeURIComponent(result.dataset.testError||''));
 });
 </script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/result":
			select {
			case resultChannel <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("error"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx204)
		case "/tasks/lazy-task/changes/file":
			index, _ := strconv.Atoi(r.URL.Query().Get("file_index"))
			if index != 0 {
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
				return
			}
			view := r.URL.Query().Get("view")
			if err := DiffChunk(files[0], 0, view, "lazy-task", nil, true, 0, 1, DiffCodeRevision(files[0])).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script>`+runner+`<style>#main-content{height:600px;overflow:auto}.hidden{display:none}</style></head><body><main id="fixture-root" data-test-result="pending"></main><div id="main-content">`+markup.String()+`</div></body></html>`)
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-networking", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL)
	configureTestBrowserProcess(cmd)
	if err := startTestBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	defer stopTestBrowserProcess(cmd)
	select {
	case result := <-resultChannel:
		if result != "pass:" {
			t.Fatal(result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for oversized diff browser")
	}
}
