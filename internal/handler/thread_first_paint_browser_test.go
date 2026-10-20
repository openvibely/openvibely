package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// A data-only cache must finish its visible window before showing the transcript.
// Checking the final scroll position alone misses the empty shell and height jumps
// readers see during the frames preceding it.
func TestTaskCachedFirstPaintInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "First paint")
	task := createTask(t, h, project.ID, "Cached transcript", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
	})
	other := createTask(t, h, project.ID, "Other task")
	for i := 0; i < 5; i++ {
		ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("Question %d", i)
			ex.StartedAt = time.Unix(int64(i+1), 0)
		})
		output := strings.Repeat("A paragraph of **formatted text** with a [link](https://example.com).\n\n", 12)
		require.NoError(t, h.execRepo.Complete(t.Context(), ex.ID, models.ExecCompleted, output, "", 0, 0))
	}
	path := "/tasks/" + task.ID
	runner := fmt.Sprintf(`<script>
const errors=[];window.addEventListener('error',e=>errors.push(e.message));window.addEventListener('unhandledrejection',e=>errors.push(String(e.reason)));
window.addEventListener('DOMContentLoaded',()=>{(async()=>{
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 const assert=(ok,label)=>{if(!ok)throw Error(label)};
 async function until(f,label){for(let i=0;i<500;i++){if(f())return;await wait(10)}throw Error('Timeout: '+label)}
 const taskID=%q,taskURL=%q,otherURL=%q;
 // Model asynchronous Markdown/worker scheduling even on a fast test machine.
 let render=window.scheduleChatElementRender;
 const delayed=function(...args){const fn=render;return wait(40).then(()=>fn.apply(this,args))};
 Object.defineProperty(window,'scheduleChatElementRender',{configurable:true,get:()=>delayed,set:fn=>{render=fn}});
 let frames=[],record=false,stopped=false;
 function sample(){
  const m=document.getElementById('task-thread-messages');
  if(record&&m&&m.closest('#task-thread-view')?.dataset.taskId===taskID&&getComputedStyle(m).visibility==='visible'){
   const b=m.getBoundingClientRect();
   const visible=Array.from(m.querySelectorAll('.chat-markdown,.stream-tool')).some(n=>{const r=n.getBoundingClientRect();return n.textContent.trim()&&r.height>0&&r.bottom>b.top&&r.top<b.bottom});
   frames.push({top:m.scrollTop,height:m.scrollHeight,viewport:m.clientHeight,visible});
  }
  if(!stopped)requestAnimationFrame(sample);
 }
 requestAnimationFrame(sample);
 await until(()=>document.querySelector('#task-thread-messages .chat-markdown')&&document.getElementById('task-thread-messages').dataset.transcriptHydrating!=='true','cold render');
 await wait(150);
 for(let round=0;round<3;round++){
  await openVibelyNavigate(otherURL);await wait(80);
  frames=[];record=true;
  let responseDone=false;
  const navigation=openVibelyNavigate(taskURL).then(()=>{responseDone=true});
  await until(()=>frames.length>0,'cached first paint');
  assert(!responseDone,'cached first paint waited for the server');
  await navigation;await wait(180);record=false;
  assert(frames.every(f=>f.visible),'painted an unfinished transcript: '+JSON.stringify(frames));
  const first=frames[0];
  assert(frames.every(f=>Math.abs(f.height-first.height)<=2&&Math.abs(f.top-first.top)<=2),'visible transcript moved while opening: '+JSON.stringify(frames));
  assert(frames.every(f=>Math.abs(f.height-f.top-f.viewport)<=2),'first paint was not at the bottom');
 }
 stopped=true;
 assert(!errors.length,'browser errors: '+errors.join('; '));
 await fetch('/browser-result?status=pass');
})().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||String(e))))});</script>`, task.ID, path, "/tasks/"+other.ID)
	results := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			select {
			case results <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == path && r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
			time.Sleep(350 * time.Millisecond)
		}
		if r.URL.Path == path && r.Header.Get("HX-Request") != "true" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1)))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	var outcome string
	select {
	case outcome = <-results:
	case <-ctx.Done():
		outcome = "timeout"
	}
	stopHandlerBrowserProcess(cmd)
	require.Equal(t, "pass", outcome, stderr.String())
}
