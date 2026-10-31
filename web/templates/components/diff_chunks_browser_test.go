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
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserFunctional_ProgressiveDiffChunks(t *testing.T) {
	runProgressiveDiffBrowser(t, "")
}

func TestBrowserFunctional_DiffChunkLayoutAnchors(t *testing.T) {
	for _, kind := range []string{"mixed", "deleted"} {
		t.Run(kind, func(t *testing.T) { runProgressiveDiffBrowser(t, kind) })
	}
}

func TestBrowserFunctional_DiffChunkRetryAfterEviction(t *testing.T) {
	for _, kind := range []string{"retry-recovered", "retry-fails-again"} {
		t.Run(kind, func(t *testing.T) { runProgressiveDiffBrowser(t, kind) })
	}
}

func TestBrowserFunctional_DiffChunkLateReviewResponse(t *testing.T) {
	for _, kind := range []string{"late-inline", "late-split"} {
		t.Run(kind, func(t *testing.T) { runProgressiveDiffBrowser(t, kind) })
	}
}

func runProgressiveDiffBrowser(t *testing.T, kind string) {
	chrome := testChromePath(t)
	diff := "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,30000 @@\n" + strings.Repeat("+line\n", 30000)
	if kind == "mixed" {
		diff = "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,15000 +1,15000 @@\n" + strings.Repeat("-old\n+new\n", 15000)
	} else if kind == "deleted" {
		diff = "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,30000 +0,0 @@\n" + strings.Repeat("-old\n", 30000)
	}
	file := ParseDiffOutput(diff)[0]
	var comments []models.ReviewComment
	if kind == "" {
		comments = []models.ReviewComment{{ID: "existing", FilePath: "big.txt", LineNumber: 2001, LineType: "new", CommentText: "existing review"}}
	}
	if strings.HasPrefix(kind, "late-") {
		for i := 10; i <= 12; i++ {
			comments = append(comments, models.ReviewComment{ID: fmt.Sprint(i), FilePath: "big.txt", LineNumber: i*200 + 1, LineType: "new", CommentText: "original review"})
		}
	}
	var markup bytes.Buffer
	if err := DiffViewer(diff, "chunk-task", comments, "inline").Render(context.Background(), &markup); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	script := `<script>
 window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error('timeout: '+fn+'; '+JSON.stringify({scroll:document.querySelector('#main-content').scrollTop,anchor:window._captureDiffScroll()?.anchor,rows:Array.from(document.querySelectorAll('#diff-content-inline:not(.hidden) tr.diff-review-line,#diff-content-split:not(.hidden) tr.diff-review-line')).filter(row=>row.getBoundingClientRect().bottom>document.querySelector('#main-content').getBoundingClientRect().top+40).slice(0,3).map(row=>({old:row.dataset.oldLine,new:row.dataset.newLine,top:row.getBoundingClientRect().top-document.querySelector('#main-content').getBoundingClientRect().top}))}))};
 const assert=(v,m)=>{if(!v)throw Error(m)};
 try {
 const main=document.querySelector('#main-content');
 const panel=()=>document.querySelector('#diff-content-inline');
 await wait(()=>panel().querySelector('[data-chunk-index="0"] tr.diff-review-line'));
 assert(document.querySelector('#diff-content-split').childElementCount===0,'inactive layout allocated file cards or slots');
 assert(panel().querySelector('.diff-stat-add').textContent==='+30000','header shows chunk statistics instead of file totals');
 assert(panel().querySelectorAll('[data-chunk-index]').length>=150,'large file did not become chunked');
 assert(panel().querySelectorAll('tr.diff-review-line').length<=600,'initial DOM unbounded');
 const first=panel().querySelector('[data-chunk-index="0"]');
 const distant=panel().querySelector('[data-chunk-index="10"]');
 distant.scrollIntoView();
 await wait(()=>distant.querySelector('tr[data-new-line="2001"]'));
 await wait(()=>!first.querySelector('tr'));
 assert(panel().querySelectorAll('tr.diff-review-line').length<=800,'scroll accumulated DOM');
 first.scrollIntoView();
 await wait(()=>first.querySelector('tr[data-new-line="1"]'));
 const draft=document.createElement('textarea');draft.className='review-comment-form';draft.value='keep draft';first.append(draft);draft.focus();
 distant.scrollIntoView();
 await wait(()=>distant.querySelector('tr'));
 assert(first.contains(draft)&&draft.value==='keep draft','draft unmounted');
 draft.remove();first.scrollIntoView();
 switchDiffView('split',false);main.scrollTop=0;
 await wait(()=>document.querySelector('#diff-content-split [data-chunk-index="0"] tr[data-new-line="1"]'));
 const splitFirst=document.querySelector('#diff-content-split [data-chunk-index="0"]');
 const splitDistant=document.querySelector('#diff-content-split [data-chunk-index="10"]');
 splitDistant.scrollIntoView();
 await wait(()=>splitDistant.querySelector('[data-comment-id="existing"]'));
 splitFirst.scrollIntoView();
 await wait(()=>splitFirst.querySelector('tr[data-new-line="1"]') && distant._saved && splitDistant._saved);
 const inlineTable=distant._saved.querySelector('table'),splitTable=splitDistant._saved.querySelector('table');
 const list=(text,id)=>'<div class="review-comment-item" data-comment-id="'+id+'" data-file-path="big.txt" data-line-number="2001" data-line-type="new"><p>'+text+'</p></div>';
 syncReviewCommentsFromListHtml(list('edited review','existing'));
 [distant,splitDistant].forEach(chunk=>assert(chunk._saved.querySelectorAll('tr.review-inline-comment').length===1 && chunk._saved.querySelector('.review-comment-text').textContent==='edited review','detached comment edit was not synchronized'));
 syncReviewCommentsFromListHtml(list('edited review','existing')+list('added review','added'));
 [distant,splitDistant].forEach(chunk=>assert(chunk._saved.querySelectorAll('tr.review-inline-comment').length===2,'detached comment addition was not synchronized'));
 syncReviewCommentsFromListHtml('');
 assert(distant._saved.querySelector('table')===inlineTable && splitDistant._saved.querySelector('table')===splitTable,'comment refresh discarded cached code');
 splitDistant.scrollIntoView();
 await wait(()=>splitDistant.querySelector('tr[data-new-line="2001"]'));
 assert(!splitDistant.querySelector('tr.review-inline-comment'),'deleted comments reappeared in split view');
 switchDiffView('inline',false);distant.scrollIntoView();
 await wait(()=>distant.querySelector('tr[data-new-line="2001"]'));
 assert(!distant.querySelector('tr.review-inline-comment'),'deleted comments reappeared in inline view');

 fetch('/result?status=pass');
 } catch(error) {fetch('/result?status='+encodeURIComponent(error.stack||error));}
 });</script>`
	if kind != "" {
		script = `<script>
 window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error('timeout: '+fn+'; '+JSON.stringify({scroll:document.querySelector('#main-content').scrollTop,anchor:window._captureDiffScroll()?.anchor,rows:Array.from(document.querySelectorAll('#diff-content-inline:not(.hidden) tr.diff-review-line,#diff-content-split:not(.hidden) tr.diff-review-line')).filter(row=>row.getBoundingClientRect().bottom>document.querySelector('#main-content').getBoundingClientRect().top+40).slice(0,3).map(row=>({old:row.dataset.oldLine,new:row.dataset.newLine,top:row.getBoundingClientRect().top-document.querySelector('#main-content').getBoundingClientRect().top}))}))};
 const assert=(v,m)=>{if(!v)throw Error(m)};
 const settle=()=>new Promise(r=>setTimeout(r,250));
 try {
 const main=document.querySelector('#main-content');
 const panel=mode=>document.querySelector('#diff-content-'+mode);
 await wait(()=>panel('inline').querySelector('[data-chunk-index="0"] tr.diff-review-line'));
 const target=panel('inline').querySelector('[data-chunk-index="40"]');
 target.scrollIntoView();
 await wait(()=>target.querySelector('tr.diff-review-line'));
 const before=window._captureDiffScroll().anchor;
 assert((before.newLine||before.oldLine)>1000,'fixture must start deep in the file');
 const side=before.newLine?'new':'old', line=before.newLine||before.oldLine;
 const restored=mode=>{
  const row=panel(mode).querySelector('tr.diff-review-line[data-'+side+'-line="'+line+'"]');
  return row && Math.abs(row.getBoundingClientRect().top-main.getBoundingClientRect().top-before.offset)<2;
 };
 switchDiffView('split',false);
 await wait(()=>restored('split'));
 await settle();assert(restored('split'),'split view lost the line after settling');
 assert(!document.querySelector('#diff-file-split-0')._diffScrollAnchor,'completed restore retained a stale anchor');
 // Returning through retained chunks must preserve the same line too.
 await wait(()=>!target.querySelector('tr'));
 switchDiffView('inline',false);
 await wait(()=>restored('inline'));
 switchDiffView('split',false);
 await wait(()=>restored('split'));
 // Evict the source chunk to exercise returning to an already opened layout.
 await wait(()=>!target.querySelector('tr'));
 target._saved=null;
 switchDiffView('inline',false);
 await wait(()=>restored('inline'));
 await settle();assert(restored('inline'),'return to evicted inline chunk lost the line');
 // A superseded request must not move the latest layout.
 panel('split').querySelectorAll('[data-chunk-index]').forEach(chunk=>{chunk._saved=null;});
 switchDiffView('split',false);
 document.getElementById('diff-btn-inline').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));
 switchDiffView('inline',false);
 await wait(()=>restored('inline'));
 await settle();assert(restored('inline'),'superseded request moved the reading position');
 // Yield to a deliberate new scroll while the destination request is delayed.
 panel('split').querySelectorAll('[data-chunk-index]').forEach(chunk=>{chunk._saved=null;});
 switchDiffView('split',false);
 main.dispatchEvent(new WheelEvent('wheel',{deltaY:-100,bubbles:true}));
 main.scrollTop=0;
 await wait(()=>panel('split').querySelector('[data-chunk-index="0"] tr.diff-review-line'));
 await settle();assert(main.scrollTop<100,'late anchor response undid user scroll');
 fetch('/result?status=pass');
 } catch(error) {fetch('/result?status='+encodeURIComponent(error.stack||error));}
 });</script>`
	}
	if strings.HasPrefix(kind, "retry-") {
		script = `<script>
 window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error('timeout: '+fn);};
 const assert=(v,m)=>{if(!v)throw Error(m);};
 try {
 const panel=document.querySelector('#diff-content-inline');
 await wait(()=>panel.querySelector('[data-chunk-index="0"] tr.diff-review-line'));
 const chunk=panel.querySelector('[data-chunk-index="10"]');
 chunk.scrollIntoView();
 await wait(()=>chunk.querySelector('button') && chunk.dataset.diffLoadFailed);
 // Before eviction the retained error UI must remain usable.
 const first=panel.querySelector('[data-chunk-index="0"]');first.scrollIntoView();
 await wait(()=>chunk._saved);
 chunk.scrollIntoView();
 await wait(()=>chunk.querySelector('button'));
 assert(chunk.dataset.diffLoadFailed==='true','failure state cleared before its Retry UI was evicted');
 for(let i=20;i<43;i++){
  const next=panel.querySelector('[data-chunk-index="'+i+'"]');next.scrollIntoView();
  await wait(()=>next.querySelector('tr.diff-review-line'));
 }
 await wait(()=>!chunk._saved && !chunk.firstElementChild);
 assert(!chunk.dataset.diffLoadFailed,'evicted retry left a permanent failure flag');
 chunk.scrollIntoView();
 if (` + strconv.FormatBool(kind == "retry-fails-again") + `) {
  await wait(()=>chunk.querySelector('button') && chunk.dataset.diffLoadFailed);
  chunk.querySelector('button').click();
 }
 await wait(()=>chunk.querySelector('tr[data-new-line="2001"]'));
 assert(!chunk.dataset.diffLoadFailed,'successful reload retained failure flag');
 fetch('/result?status=pass');
 } catch(error) {fetch('/result?status='+encodeURIComponent(error.stack||error));}
 });</script>`
	}
	if strings.HasPrefix(kind, "late-") {
		script = `<script>
 window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error('timeout: '+fn);};
 const assert=(v,m)=>{if(!v)throw Error(m);};
 try {
 const mode='` + strings.TrimPrefix(kind, "late-") + `';
 await wait(()=>document.querySelector('#diff-content-inline [data-chunk-index="0"] tr.diff-review-line'));
 if(mode==='split') switchDiffView('split',false);
 const panel=document.querySelector('#diff-content-'+mode);
 await wait(()=>panel.querySelector('[data-chunk-index="0"] tr.diff-review-line'));
 const originalFetch=window.fetch;
 let pendingChunk,releaseResponse;
 window.fetch=function(url,opts){
  if(Number(new URL(url,location.href).searchParams.get('chunk'))===pendingChunk) {
   return originalFetch(url,opts).then(response=>new Promise(resolve=>{releaseResponse=()=>resolve(response);}));
  }
  return originalFetch(url,opts);
 };
 let comments=[10,11,12].map(i=>({id:String(i),line:i*200+1,text:'original review'}));
 const list=()=>comments.map(c=>'<div class="review-comment-item" data-comment-id="'+c.id+'" data-file-path="big.txt" data-line-number="'+c.line+'" data-line-type="new"><p>'+c.text+'</p></div>').join('');
 for(let index=10;index<=12;index++){
  pendingChunk=index;releaseResponse=null;
  const chunk=panel.querySelector('[data-chunk-index="'+index+'"]');chunk.scrollIntoView();
  await wait(()=>releaseResponse);
  if(index===10) comments=comments.filter(c=>c.id!=='10');
  if(index===11) comments.find(c=>c.id==='11').text='edited review';
  if(index===12) comments.push({id:'added',line:2401,text:'added review'});
  syncReviewCommentsFromListHtml(list());
  releaseResponse();
  await wait(()=>chunk.querySelector('tr[data-new-line="'+(index*200+1)+'"]'));
  const rows=chunk.querySelectorAll('tr.review-inline-comment');
  assert(rows.length===index-10,'late response restored incorrect comments for '+index);
  if(index===11) assert(rows[0].querySelector('.review-comment-text').textContent==='edited review','late response undid comment edit');
  if(index===12) assert(chunk.querySelector('[data-comment-id="added"]'),'late response lost added comment');
 }
 fetch('/result?status=pass');
 } catch(error) {fetch('/result?status='+encodeURIComponent(error.stack||error));}
 });</script>`
	}
	var failedChunkRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/result":
			select {
			case result <- r.URL.Query().Get("status"):
			default:
			}
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx204)
		case "/tasks/chunk-task/changes/file":
			if r.URL.Query().Get("revision") != DiffCodeRevision(file) {
				http.Error(w, "stale code revision", 409)
				return
			}
			chunk, _ := strconv.Atoi(r.URL.Query().Get("chunk"))
			if strings.HasPrefix(kind, "retry-") && chunk == 10 {
				failures := int32(1)
				if kind == "retry-fails-again" {
					failures = 2
				}
				if failedChunkRequests.Add(1) <= failures {
					http.Error(w, "temporary failure", http.StatusServiceUnavailable)
					return
				}
			}
			view := r.URL.Query().Get("view")
			chunks := DiffFileChunks(file, view)
			oldLine, _ := strconv.Atoi(r.URL.Query().Get("anchor_old_line"))
			newLine, _ := strconv.Atoi(r.URL.Query().Get("anchor_new_line"))
			if index, found := DiffChunkContainingLine(chunks, oldLine, newLine); found {
				chunk = index
				select {
				case <-time.After(80 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			if chunk < 0 || chunk >= len(chunks) {
				http.Error(w, "invalid", 400)
				return
			}
			if err := DiffChunk(chunks[chunk], 0, view, "chunk-task", comments, chunk, len(chunks), DiffCodeRevision(file)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script><style>.hidden{display:none}svg{width:16px;height:16px}#main-content{height:600px;overflow:auto}.diff-file-header{display:flex}.diff-file-body{overflow:hidden}td{height:16px;padding:0}table{border-collapse:collapse;font-size:12px;line-height:16px}</style>`+script+`</head><body><main id="main-content">`+markup.String()+`</main></body></html>`)
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
	case status := <-result:
		if status != "pass" {
			t.Fatal(status)
		}
		if strings.HasPrefix(kind, "retry-") {
			expected := int32(2)
			if kind == "retry-fails-again" {
				expected = 3
			}
			if got := failedChunkRequests.Load(); got != expected {
				t.Fatalf("chunk requests=%d want=%d", got, expected)
			}
		}
	case <-time.After(30 * time.Second):
		t.Fatal("browser timed out")
	}
}
