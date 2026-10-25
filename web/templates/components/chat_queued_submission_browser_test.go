package components

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserFunctional_ChatQueuedSubmissionSnapshots(t *testing.T) {
	chrome := testChromePath(t)
	for _, tc := range []struct {
		name   string
		taskID string
		steer  bool
	}{
		{name: "chat sends"}, {name: "chat steering", steer: true}, {name: "task steering", taskID: "task", steer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content bytes.Buffer
			agents := []models.LLMConfig{{ID: "a", Name: "First", Provider: models.ProviderOpenAI, Model: "gpt-5.5"}, {ID: "b", Name: "Second", Provider: models.ProviderOpenAI, Model: "gpt-5.5"}}
			err := ChatInputForm(ChatInputFormConfig{FormID: "audit-form", InputID: "message", TargetID: "messages", PostEndpoint: "/send", SteerEndpoint: "/steer", IsRunning: true, ActiveTurnID: "turn", TaskID: tc.taskID, ProjectID: "project", Agents: agents, SelectedAgentID: "a", ShowModelSelector: true}).Render(context.Background(), &content)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var records []map[string]string
			release := make(chan struct{})
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/htmx.js":
					w.Header().Set("Content-Type", "text/javascript")
					_, _ = w.Write(htmx204)
				case "/ui/model-favorites", "/tasks/task/thread/model":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{}`)
				case "/release":
					once.Do(func() { close(release) })
				case "/records":
					mu.Lock()
					defer mu.Unlock()
					_ = json.NewEncoder(w).Encode(records)
				case "/send", "/steer":
					_ = r.ParseForm()
					record := map[string]string{"path": r.URL.Path}
					for key := range r.PostForm {
						record[key] = r.PostForm.Get(key)
					}
					mu.Lock()
					records = append(records, record)
					mu.Unlock()
					if record["message"] == "first" {
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					if r.URL.Path == "/steer" {
						http.Error(w, "turn ended", http.StatusConflict)
						return
					}
					fmt.Fprint(w, `<div>accepted</div>`)
				default:
					fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body data-test-result="pending"><div id="messages"><div data-execution-pair="true" data-exec-status="running" data-exec-id="turn"></div></div>`+content.String()+`<div id="browser-result"></div><script>
(async()=>{
 const steer=`+fmt.Sprint(tc.steer)+`,assert=(ok,msg)=>{if(!ok)throw Error(msg)},wait=()=>new Promise(r=>setTimeout(r,20));
 const until=async(fn)=>{for(let i=0;i<200;i++){if(await fn())return;await wait();}throw Error('timed out waiting for queued requests');};
 try{
 const form=document.getElementById('audit-form'),message=form.querySelector('[name=message]'),attachment=form.querySelector('[name=attachment_session_id]'),model=form.querySelector('[name=agent_id]'),effort=form.querySelector('[name=reasoning_effort]');
 await form.querySelector('.chat-model-select')._ovModelState.ready;
 await until(()=>document.readyState==='complete'); // HTMX binds ordinary Chat on DOMContentLoaded.
 const completed=[];
 form.addEventListener('htmx:afterRequest',e=>{if(e.detail.elt===form)completed.push({intent:e.detail.xhr._chatSubmittedIntent,message:e.detail.xhr._chatSubmittedMessage,fallback:e.detail.xhr._chatSubmittedFallbackFromSteer});});
 const send=(text,isSteer=false)=>{message.value=text;message.dispatchEvent(new Event('input',{bubbles:true}));message.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true,metaKey:isSteer&&message.placeholder.includes('⌘'),ctrlKey:isSteer&&!message.placeholder.includes('⌘')}));};
 send('first');await until(async()=>((await(await fetch('/records')).json())||[]).length===1);
 attachment.value='second-attachment';model.value='a';effort.value='high';send('second',steer);
 if(steer)send('second',true); // A duplicate while queued must remain deduplicated.
 attachment.value='third-attachment';model.value='b';effort.value='low';send('third',steer);
 message.value='UNSENT DRAFT';attachment.value='draft-attachment';model.value='a';effort.value='medium';
 await fetch('/release');await until(()=>completed.length===(steer?5:3));
 const records=await(await fetch('/records')).json();
 const expected=steer?[['/send','first'],['/steer','second'],['/send','second'],['/steer','third'],['/send','third']]:[['/send','first'],['/send','second'],['/send','third']];
 assert(records.length===expected.length,'exact request count: '+JSON.stringify(records));
 expected.forEach(([path,text],i)=>{
  const row=records[i];assert(row.path===path&&row.message===text,'request '+i+' preserves path and text: '+JSON.stringify(row));
  assert(completed[i].message===text&&completed[i].intent===(path==='/steer'?'steer':'normal'),'completion metadata matches request '+i);
  if(i){const second=text==='second';assert(row.attachment_session_id===(second?'second-attachment':'third-attachment'),'attachment snapshot '+i);assert(row.agent_id===(second?'a':'b')&&row.reasoning_effort===(second?'high':'low'),'model snapshot '+i);}
  if(steer&&i&&path==='/send')assert(completed[i].fallback===true,'fallback completion marker '+i);
 });
 assert(message.value==='UNSENT DRAFT'&&attachment.value==='draft-attachment'&&model.value==='a'&&effort.value==='medium','unsent draft and settings stay intact');
 assert(!form._chatNextSubmissionPayload,'no leftover handoff snapshot');
 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e);await fetch('/release');}
})();</script></body></html>`)
				}
			}))
			defer func() { once.Do(func() { close(release) }); srv.Close() }()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, tc.name, 1000, 800, 25*time.Second)
		})
	}
}
