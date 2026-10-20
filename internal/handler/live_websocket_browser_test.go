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

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

// Real Chromium, real WebSocket server, real streaming renderer. The only
// synthetic component is the producer: deterministic output replaces a model.
func TestLiveWebSocketBrowserSharedConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	tc := NewStoreTestContext(t)
	tc.handler.SetExecutionStreamHub(events.NewExecutionStreamHub())
	tc.handler.broadcaster = events.NewBroadcaster()
	tc.handler.chatBroadcaster = events.NewChatBroadcaster()
	first := createRunningChatStreamExecution(t, tc, "")
	second := createRunningChatStreamExecution(t, tc, "")
	followup := createExec(t, tc.handler, first.TaskID, first.AgentConfigID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	render := func(component templ.Component) string {
		var out bytes.Buffer
		require.NoError(t, component.Render(context.Background(), &out))
		return out.String()
	}
	bubble := func(id string) string {
		return render(components.ChatBubbleStreaming("assistant", id, "browser-messages", "", true))
	}
	base := render(layout.Base("Live transport", []models.Project{{ID: "default", Name: "Default"}}, "default"))
	instrumentation := `<script>
 window.testSockets=[];window.testSSE=0;
 const BrowserSocket=window.WebSocket;
 window.WebSocket=class extends BrowserSocket {constructor(url){super(url);window.testSockets.push(this);}};
 window.EventSource=function(){window.testSSE++;throw Error('SSE connection created');};
 </script>`
	base = strings.Replace(base, "<head>", "<head>"+instrumentation, 1)
	view := render(components.ChatAutoScrollScript()) + `<textarea id="test-draft">keep this draft</textarea><div id="browser-messages" style="height:250px;overflow:auto"><div id="test-view">` + bubble(first.ID) + `</div>` + bubble(second.ID) + `</div>`
	runner := fmt.Sprintf(`<script>
 (async function(){
 const first=%q,second=%q,followup=%q;
 const wait=async(fn)=>{for(let n=0;n<300;n++){if(fn())return;await new Promise(r=>setTimeout(r,25));}throw Error('wait timed out: '+fn);};
 const raw=id=>document.getElementById('streaming-message-'+id)?.getAttribute('data-raw-content')||'';
 const action=phase=>fetch('/test/action?phase='+phase).then(r=>{if(!r.ok)throw Error('action '+phase);});
 try {
  await wait(()=>window.testSockets.length===1&&window.testSockets[0].readyState===1);
  await action('start');await wait(()=>raw(first)==='héllo'&&raw(second)==='parallel');
  const socket=window.testSockets[0];
  // Project selection retargets notifications before navigation commits. A
  // failed navigation must leave the current view's output subscription alive.
  const currentOutput=window._threadEventSources[0];
  const pendingScope=window.openVibelyLive.live('pending-project','',{});
  pendingScope.close();
  if(currentOutput.readyState!==1)throw Error('pending project switch closed current output');
  // View-level navigation unsubscribes the outgoing execution. The new view
  // loads its HTTP snapshot and resumes on the existing socket.
  window._threadEventSources[0].close();
  await htmx.ajax('GET','/test/view',{target:'#test-view',swap:'innerHTML'});
  await wait(()=>raw(first)==='héllo');
  if(window.testSockets.length!==1||socket.readyState!==1)throw Error('navigation reopened socket');
  await action('complete-first');await wait(()=>raw(first)==='héllo finished');
  await wait(()=>document.getElementById('streaming-dots-'+first)?.classList.contains('hidden'));
  if(socket.readyState!==1||window.testSockets.length!==1)throw Error('completion closed socket');
  await htmx.ajax('GET','/test/followup',{target:'#test-view',swap:'innerHTML'});
  await action('followup');await wait(()=>raw(followup)==='next run');
  // Disconnect while output, status and shared notification state change.
  socket.close();await wait(()=>socket.readyState===3);
  await action('offline');
  await wait(()=>window.testSockets.length===2&&raw(second)==='parallel recovered 日本語'&&raw(followup)==='next run done');
  await wait(()=>document.getElementById('streaming-dots-'+second)?.classList.contains('hidden'));
  if(document.getElementById('test-draft').value!=='keep this draft')throw Error('draft lost');
  if(window.testSSE!==0)throw Error('SSE used');
  if(window.testSockets.filter(s=>s.readyState===1).length!==1)throw Error('multiple live sockets');
  const denied=window.openVibelyLive.execution(first,0,'missing-project');
  await new Promise((resolve,reject)=>{denied.addEventListener('error',e=>e.data==='subscription denied'?resolve():reject(Error(e.data)));});
  await fetch('/test/result?result='+encodeURIComponent('pass sockets=1 initial,1 reconnect; SSE=0'));
 }catch(error){await fetch('/test/result?result='+encodeURIComponent('fail '+error.stack));}
 })();</script>`, first.ID, second.ID, followup.ID)
	page := strings.Replace(base, "</body>", view+runner+"</body>", 1)
	result := make(chan string, 1)
	e := echo.New()
	e.GET("/events/ws", tc.handler.LiveWebSocket)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/events/ws":
			e.ServeHTTP(w, r)
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, page)
		case "/test/view":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, bubble(first.ID))
		case "/test/followup":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, bubble(followup.ID))
		case "/test/action":
			appendOutput := func(id, output string) {
				require.NoError(t, tc.execRepo.FinalizeOutput(r.Context(), id, output))
				tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: id, Type: events.ExecutionStreamDelta, Delta: output, Offset: len(output)})
			}
			finish := func(id, output string, status models.ExecutionStatus) {
				require.NoError(t, tc.execRepo.Complete(r.Context(), id, status, output, "", 0, 0))
				tc.handler.executionStreamHub.CloseTerminal(id, status, "")
			}
			switch r.URL.Query().Get("phase") {
			case "start":
				appendOutput(first.ID, "héllo")
				appendOutput(second.ID, "parallel")
			case "complete-first":
				finish(first.ID, "héllo finished", models.ExecCompleted)
			case "followup":
				appendOutput(followup.ID, "next run")
			case "offline":
				finish(second.ID, "parallel recovered 日本語", models.ExecCancelled)
				finish(followup.ID, "next run done", models.ExecCompleted)
			}
			w.WriteHeader(204)
		case "/test/result":
			result <- r.URL.Query().Get("result")
			w.WriteHeader(204)
		default:
			w.WriteHeader(204)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case got := <-result:
		require.True(t, strings.HasPrefix(got, "pass"), got)
		t.Log(got)
	case <-time.After(45 * time.Second):
		t.Fatal("browser transport regression timed out")
	}
}
