package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestChatResponseBrowserScrollsWithoutPaging(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "message browser")
	task := createTask(t, h, project.ID, "messages", func(task *models.Task) { task.Category = models.CategoryChat })
	execution := createExec(t, h, task.ID, agent.ID)
	var output strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&output, "**visible-%02d**\n[Using tool: shell | echo result]\n[Tool shell done]\nresult %d\n[/Tool]\n", i, i)
	}
	require.NoError(t, h.execRepo.Complete(context.Background(), execution.ID, models.ExecCompleted, output.String(), "", 0, 0))
	result := make(chan string, 1)
	runner := `<script>(async()=>{
 const wait=async(fn)=>{let end=Date.now()+8000;while(Date.now()<end){let x=fn();if(x)return x;await new Promise(r=>setTimeout(r,20))}throw Error('page did not render '+JSON.stringify({top:messages()?.scrollTop,height:messages()?.scrollHeight,text:messages()?.textContent.slice(0,400),hydrating:messages()?.dataset.transcriptHydrating,viewport:messages()?.getBoundingClientRect().toJSON(),first:Array.from(messages()?.querySelectorAll('strong')||[]).slice(0,2).map(el=>({text:el.textContent,rect:el.getBoundingClientRect().toJSON()}))}))};
 const paint=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 const messages=()=>document.getElementById('chat-messages');
 const shown=text=>Array.from(messages().querySelectorAll('strong')).some(el=>{
  const r=el.getBoundingClientRect(),m=messages().getBoundingClientRect();
  return el.textContent===text&&r.height>0&&r.bottom>m.top&&r.top<m.bottom;
 });
 try{
 await wait(()=>messages()&&shown('visible-39'));
 await wait(()=>messages().dataset.transcriptHydrating!=='true'&&!window._chatContentRenderActive&&!window._chatContentRenderQueue?.length);
 await paint();
 if(document.querySelector('[data-message-window]'))throw Error('response still paged');
 if(Array.from(messages().querySelectorAll('button')).some(b=>/^(Earlier|Latest) messages$/.test(b.textContent.trim())))throw Error('response paging control remains');
 for(let i=0;i<3;i++){
  const m=messages();
  m.dispatchEvent(new WheelEvent('wheel',{deltaY:-200,bubbles:true}));
  m.scrollTop=0;m.dispatchEvent(new Event('scroll'));
  await window.renderVisibleChatRows?.(m);
  await wait(()=>shown('visible-00'));
  m.scrollTop=m.scrollHeight;m.dispatchEvent(new Event('scroll'));
  await window.renderVisibleChatRows?.(m);
  await wait(()=>shown('visible-39'));
 }
 await fetch('/page-result',{method:'POST',body:'pass'});
 }catch(err){await fetch('/page-result',{method:'POST',body:String(err.stack||err)})}
 })();</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/page-result" {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case result <- string(raw):
			default:
			}
			return
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := recorder.Body.String()
		if r.URL.Path == "/chat" {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-extensions", "--no-first-run", "--window-size=1280,1000", server.URL+"/chat?project_id="+project.ID)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case got := <-result:
		require.Equal(t, "pass", got)
	case <-time.After(40 * time.Second):
		t.Fatal("continuous response browser timed out")
	}
}
