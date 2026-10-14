package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/web/static"
)

// Opt-in because this deliberately saturates a disposable database while Chrome
// navigates production pages. Keep it outside the normal unit-test time budget.
func TestProjectTabsUnderLoadInChrome(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_PROJECT_STRESS") != "1" {
		t.Skip("set OPENVIBELY_RUN_PROJECT_STRESS=1")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Fatal("Chrome is required for the requested stress test")
	}
	for _, scenario := range []struct {
		tasks int
		delay time.Duration
		burst bool
	}{{10, 0, false}, {30, 0, false}, {30, 150 * time.Millisecond, false}, {30, 150 * time.Millisecond, true}} {
		t.Run(fmt.Sprintf("3x%d/delay=%s/burst=%t", scenario.tasks, scenario.delay, scenario.burst), func(t *testing.T) {
			tasks := scenario.tasks

			broadcaster := events.NewBroadcaster()
			stores := teststores.New(t, teststores.WithProductionTopology(), teststores.WithBroadcaster(broadcaster))
			env := wireTestHandler(t, stores.Stores, testHandlerOptions{broadcaster: broadcaster})
			taskRepo := stores.Tasks
			execRepo := stores.Executions
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var ids []string
			for p := 0; p < 3; p++ {
				project := fmt.Sprintf("stress-project-%d", p)
				if err := stores.Fixtures.SeedProjects(ctx, []models.Project{{ID: project, Name: project}}); err != nil {
					t.Fatal(err)
				}
				for j := 0; j < tasks; j++ {
					id := fmt.Sprintf("stress-task-%d-%d", p, j)
					if err := stores.Fixtures.SeedPendingTasks(ctx, []storefixture.PendingTask{{ID: id, ProjectID: project, Title: id, Prompt: "stress", Category: models.CategoryActive}}); err != nil {
						t.Fatal(err)
					}
					if claimed, err := taskRepo.ClaimTask(ctx, id); err != nil || !claimed {
						t.Fatalf("claim: %v %v", claimed, err)
					}
					if err := stores.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{{ID: id, TaskID: id, Status: models.ExecRunning, PromptSent: "stress", StartedAt: time.Now().UTC()}}); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
			}
			var wg sync.WaitGroup
			var operations atomic.Int64
			failures := make(chan error, len(ids))
			payload := strings.Repeat("stream output\n", 80659) // roughly 1 MiB per execution
			for n, id := range ids {
				project := fmt.Sprintf("stress-project-%d", n/tasks)
				wg.Add(1)
				go func() {
					defer wg.Done()
					for sequence := 0; ctx.Err() == nil; sequence++ {
						if err := execRepo.FinalizeOutput(ctx, id, fmt.Sprintf("%d\n%s", sequence, payload)); err != nil {
							if ctx.Err() == nil {
								failures <- err
							}
							return
						}
						if _, err := env.ExecRepo.GetByID(ctx, id); err != nil {
							if ctx.Err() == nil {
								failures <- err
							}
							return
						}
						broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, ProjectID: project, TaskID: id})
						operations.Add(1)
					}
				}()
			}
			defer func() { cancel(); wg.Wait() }()
			result := make(chan string, 1)
			var navigations, streamConnections atomic.Int64
			runner := `<script>
 (async function(){
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 const errors=[], times=[];let received=0;
 function check(){if(errors.length)throw new Error(JSON.stringify(errors));}
 try {
 if(document.readyState!=='complete')await new Promise(r=>window.addEventListener('load',r,{once:true}));
 for(let p=0;p<3;p++)sessionStorage.setItem('openvibely.project-location.stress-project-'+p,'/tasks?project_id=stress-project-'+p);
 const toast=window.showToast;window.showToast=function(message,type){if(String(message).includes('switch projects'))errors.push(message);return toast.apply(this,arguments);};
 const nav=window.openVibelyNavigate;
 window.openVibelyNavigate=function(){return nav.apply(this,arguments).catch(e=>{errors.push('navigation status='+e.status+' '+e.message);throw e;});};
 window.addEventListener('sse-task-event',()=>received++);
 function switchTo(id){
 let tab=document.querySelector('[data-project-tab="'+id+'"]');
 if(tab){tab.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,isPrimary:true,pointerId:1}));document.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:1}));}
 else {const sel=document.getElementById('project-selector');sel.value=id;sel.dispatchEvent(new CustomEvent('change',{bubbles:true,detail:{}}));}
 }
 for(let i=0;i<60;i++) {
 const id='stress-project-'+((i+1)%3), start=performance.now();
 if(BURST_SWITCHES){switchTo('stress-project-'+((i+2)%3));await wait(20);}
 switchTo(id);
 while(performance.now()-start<10000){check();if(new URLSearchParams(location.search).get('project_id')===id && !window.openVibelyProtectedProjectID)break;await wait(10);}
 check();if(new URLSearchParams(location.search).get('project_id')!==id || window.openVibelyProtectedProjectID)throw new Error('switch timed out: '+id);
 if(!document.getElementById('main-content').textContent.includes('stress-task-'+((i+1)%3)+'-0'))throw new Error('wrong project content');
 times.push(performance.now()-start);await wait(100);
 }
 if(!received)throw new Error('no live task events received');
 times.sort((a,b)=>a-b);
 await fetch('/stress-result',{method:'POST',body:'pass:'+JSON.stringify({switches:times.length,p95:times[56],max:times[59],received})});
 }catch(e){await fetch('/stress-result',{method:'POST',body:'fail:'+String(e.stack||e)});}
 })();</script>`
			runner = strings.ReplaceAll(runner, "BURST_SWITCHES", fmt.Sprint(scenario.burst))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/stress-result" {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
					select {
					case result <- string(body):
					default:
					}
					w.WriteHeader(204)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/events/") {
					streamConnections.Add(1)
					env.Echo.ServeHTTP(w, r)
					return
				}
				if r.URL.Path == "/tasks" && r.Header.Get("HX-Request") != "" && scenario.delay > 0 {
					select {
					case <-time.After(scenario.delay):
					case <-r.Context().Done():
						return
					}
				}
				if r.Header.Get("HX-Target") == "main-content" {
					navigations.Add(1)
				}
				if r.URL.Path == "/tasks" && r.Header.Get("HX-Request") == "" {
					recorder := httptest.NewRecorder()
					env.Echo.ServeHTTP(recorder, r)
					for k, v := range recorder.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(recorder.Code)
					io.WriteString(w, strings.Replace(recorder.Body.String(), "</body>", runner+"</body>", 1))
					return
				}
				env.Echo.ServeHTTP(w, r)
			}))
			defer server.Close()
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--window-size=1440,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/tasks?project_id=stress-project-0")
			if err := startHandlerBrowserProcess(cmd); err != nil {
				t.Fatal(err)
			}
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-result:
				t.Logf("%s; operations=%d navigations=%d SSE connections=%d reader wait=%v writer wait=%v", outcome, operations.Load(), navigations.Load(), streamConnections.Load(), stores.Contention().Reads.WaitDuration, stores.Contention().Writes.WaitDuration)
				if !strings.HasPrefix(outcome, "pass:") {
					t.Error(outcome)
				}
			case <-time.After(120 * time.Second):
				t.Error("browser stress timed out")
			}
			cancel()
			wg.Wait()
			select {
			case err := <-failures:
				t.Errorf("background workload: %v", err)
			default:
			}
			if operations.Load() == 0 || streamConnections.Load() < 3 {
				t.Error("database/SSE load was not exercised")
			}
		})
	}
}
