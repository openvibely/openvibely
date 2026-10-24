package handler

import (
	"bytes"
	"context"
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
	var stageMu sync.Mutex
	lastStage := "waiting for document runner"
	runner := `<script>(async()=>{
 const assert=(v,m)=>{if(!v)throw Error(m)};
 const frame=()=>new Promise(r=>setTimeout(r,100));
 const stage=async value=>{await fetch('/browser-stage',{method:'POST',body:value})};
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
   const filler=document.createElement('div'); filler.style.height='4000px'; main.appendChild(filler);
   await frame();
   const rect=main.getBoundingClientRect();
   assert(rect.bottom<=innerHeight-9 && rect.bottom>=innerHeight-20,path+' lost bottom inset: '+rect.bottom+' viewport '+innerHeight);
   assert(main.scrollHeight>main.clientHeight+100,path+' expanded instead of scrolling');
   main.scrollTop=500; await frame();
   assert(main.scrollTop>=499,path+' cannot scroll');
   main.scrollTop=main.scrollHeight; await frame();
   assert(Math.abs(main.scrollHeight-main.clientHeight-main.scrollTop)<2,path+' cannot reach bottom');
   assert(document.scrollingElement.scrollTop===0,'root scrolled instead of page');
 }
 await fetch('/browser-result?status=pass');
 })().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||String(e))));</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_, _ = w.Write([]byte(strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1)))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/models")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	var outcome string
	select {
	case outcome = <-results:
	case <-ctx.Done():
		stageMu.Lock()
		outcome = "timeout: " + lastStage
		stageMu.Unlock()
	}
	stopHandlerBrowserProcess(cmd)
	require.Equal(t, "pass", outcome, stderr.String())
}
