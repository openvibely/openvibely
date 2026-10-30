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
	"testing"
	"time"
)

func TestBrowserFunctional_ProgressiveDiffChunks(t *testing.T) {
	chrome := testChromePath(t)
	diff := "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,30000 @@\n" + strings.Repeat("+line\n", 30000)
	file := ParseDiffOutput(diff)[0]
	var markup bytes.Buffer
	if err := DiffViewerWithReview(diff, "chunk-task", nil).Render(context.Background(), &markup); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	script := `<script>
 window.addEventListener('load',async()=>{
 const wait=async(fn)=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error('timeout: '+fn)};
 const assert=(v,m)=>{if(!v)throw Error(m)};
 try {
 const main=document.querySelector('#main-content');
 const panel=()=>document.querySelector('#diff-content-inline');
 await wait(()=>panel().querySelector('[data-chunk-index="0"] tr.diff-review-line'));
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
 fetch('/result?status=pass');
 } catch(error) {fetch('/result?status='+encodeURIComponent(error.stack||error));}
 });</script>`
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
			chunk, _ := strconv.Atoi(r.URL.Query().Get("chunk"))
			view := r.URL.Query().Get("view")
			chunks := DiffFileChunks(file, view)
			if chunk < 0 || chunk >= len(chunks) {
				http.Error(w, "invalid", 400)
				return
			}
			if err := DiffChunk(chunks[chunk], 0, view, "chunk-task", nil, true, chunk, len(chunks), DiffFileRevision(file, nil)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script><style>.hidden{display:none}svg{width:16px;height:16px}#main-content{height:600px;overflow:auto}.diff-file-header{display:flex}.diff-file-body{overflow:hidden}td{height:20px}table{border-collapse:collapse}</style>`+script+`</head><body><main id="main-content">`+markup.String()+`</main></body></html>`)
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
	case <-time.After(30 * time.Second):
		t.Fatal("browser timed out")
	}
}
