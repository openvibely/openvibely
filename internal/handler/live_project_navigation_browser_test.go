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
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

func TestLiveWebSocketBrowserCachedProjectNavigation(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	tc := NewStoreTestContext(t)
	tc.handler.SetExecutionStreamHub(events.NewExecutionStreamHub())
	ctx := context.Background()
	first := createRunningChatStreamExecution(t, tc, "partial")
	secondProject := models.Project{Name: "Project B"}
	require.NoError(t, tc.projectRepo.Create(ctx, &secondProject))
	secondTask := createTask(t, tc.handler, secondProject.ID, "Project B chat", func(task *models.Task) {
		task.Category = models.CategoryChat
		task.Status = models.StatusRunning
	})
	second := createExec(t, tc.handler, secondTask.ID, first.AgentConfigID, func(execution *models.Execution) {
		execution.Status = models.ExecRunning
	})
	require.NoError(t, tc.execRepo.FinalizeOutput(ctx, second.ID, "partial"))
	second.Output = "partial"
	projects := []models.Project{{ID: "default", Name: "Project A"}, secondProject}
	render := func(projectID string, execution *models.Execution, full bool) string {
		var out bytes.Buffer
		history := []models.Execution{*execution}
		if full {
			require.NoError(t, pages.Chat(projects, projectID, nil, history, nil, nil, nil, false, false, 30).Render(ctx, &out))
		} else {
			require.NoError(t, pages.ChatContent(nil, history, projectID, nil, nil, nil, false, false, 30).Render(ctx, &out))
		}
		return out.String()
	}
	initial := render("default", first, true)
	firstFragment, secondFragment := render("default", first, false), render(secondProject.ID, second, false)
	initial = strings.Replace(initial, "<head>", `<head><script>
window.testSockets=[];window.testCommands=[];window.testErrors=[];window.testSSE=0;
const BrowserSocket=window.WebSocket;
window.WebSocket=class extends BrowserSocket {
 constructor(...args){super(...args);testSockets.push(this);this.addEventListener('message',event=>{const frame=JSON.parse(event.data);if(frame.event==='error')testErrors.push(frame);});}
 send(raw){const command=JSON.parse(raw);testCommands.push(command);super.send(raw);}
};
window.EventSource=function(){testSSE++;throw Error('SSE connection created');};
</script>`, 1)
	initial = strings.Replace(initial, "</body>", fmt.Sprintf(`<script>
window.addEventListener('load',async()=>{
 const first=%q,second=%q,secondProject=%q;
 const wait=async(fn)=>{for(let n=0;n<500;n++){if(fn())return;await new Promise(r=>setTimeout(r,10));}throw Error('timed out: '+fn);};
 const raw=id=>document.getElementById('streaming-message-'+id)?.getAttribute('data-raw-content')||'';
 const assert=(value,message)=>{if(!value)throw Error(message);};
 try{
  await wait(()=>testCommands.some(c=>c.op==='execution'&&c.exec_id===first));
  const socket=testSockets[0];
  const selector=document.getElementById('project-selector');
  selector.value=secondProject;selector.dispatchEvent(new Event('change'));
  await wait(()=>new URLSearchParams(location.search).get('project_id')===secondProject&&testCommands.some(c=>c.op==='execution'&&c.exec_id===second));
  const before=testCommands.length;
  selector.value='default';selector.dispatchEvent(new Event('change'));
  await wait(()=>testCommands.slice(before).some(c=>c.op==='execution'&&c.exec_id===first));
  assert(document.getElementById('chat-page-root').dataset.projectId==='default','cached view did not restore project A');
  assert(new URLSearchParams(location.search).get('project_id')===secondProject,'HTTP refresh committed before the cached-view assertion');
  const resumed=testCommands.slice(before).find(c=>c.op==='execution'&&c.exec_id===first);
  assert(resumed.project_id==='default','cached project A execution subscribed under project '+resumed.project_id);
  await fetch('/test/output',{method:'POST'});
  await wait(()=>raw(first)==='partial live');
  assert(testErrors.length===0,'subscription rejected: '+JSON.stringify(testErrors));
  assert(testSockets.length===1&&socket.readyState===1&&testSSE===0,'cached project switch replaced the shared socket');
  await fetch('/test/release',{method:'POST'});
  await wait(()=>new URLSearchParams(location.search).get('project_id')==='default'&&raw(first)==='partial live');
  assert(testErrors.length===0&&testSockets.length===1,'authoritative refresh broke the subscription');
  await fetch('/test/result?result=pass');
 }catch(error){await fetch('/test/result?result='+encodeURIComponent(error.stack));}
});
</script></body>`, first.ID, second.ID, secondProject.ID), 1)

	refreshRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(refreshRelease) }) }
	result := make(chan string, 1)
	e := echo.New()
	e.Use(tc.handler.AuthMiddleware())
	e.GET("/events/ws", tc.handler.LiveWebSocket)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/events/ws":
			e.ServeHTTP(w, r)
		case "/test/output":
			require.NoError(t, tc.execRepo.FinalizeOutput(r.Context(), first.ID, "partial live"))
			tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: first.ID, Type: events.ExecutionStreamDelta, Delta: " live", Offset: len("partial live")})
			w.WriteHeader(http.StatusNoContent)
		case "/test/release":
			release()
			w.WriteHeader(http.StatusNoContent)
		case "/test/result":
			result <- r.URL.Query().Get("result")
			w.WriteHeader(http.StatusNoContent)
		case "/chat":
			w.Header().Set("Content-Type", "text/html")
			if r.Header.Get("HX-Request") != "true" {
				fmt.Fprint(w, initial)
			} else if r.URL.Query().Get("project_id") == secondProject.ID {
				fmt.Fprint(w, secondFragment)
			} else {
				select {
				case <-refreshRelease:
					fmt.Fprint(w, firstFragment)
				case <-r.Context().Done():
				}
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	defer release()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/chat?project_id=default")
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case got := <-result:
		require.Equal(t, "pass", got)
	case <-time.After(30 * time.Second):
		t.Fatal("cached project navigation browser regression timed out")
	}
}
