package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type activeLaneNativeDrag struct {
	phase                            string
	firstX, firstY, secondX, secondY float64
	dropX, dropY                     float64
}

func driveActiveLaneNativeDrags(debugPort int, serverURL string, ready <-chan activeLaneNativeDrag, motion <-chan string) <-chan error {
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		type target struct {
			URL                  string `json:"url"`
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		var selected target
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && selected.WebSocketDebuggerURL == ""; {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
			if err == nil {
				var targets []target
				if json.NewDecoder(resp.Body).Decode(&targets) == nil {
					for _, candidate := range targets {
						if strings.HasPrefix(candidate.URL, serverURL) {
							selected = candidate
							break
						}
					}
				}
				_ = resp.Body.Close()
			}
			if selected.WebSocketDebuggerURL == "" {
				time.Sleep(25 * time.Millisecond)
			}
		}
		if selected.WebSocketDebuggerURL == "" {
			result <- fmt.Errorf("Chrome target not found")
			return
		}
		conn, _, err := websocket.Dial(ctx, selected.WebSocketDebuggerURL, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.CloseNow()
		nextID := 0
		dispatch := func(params map[string]any) error {
			nextID++
			payload, _ := json.Marshal(map[string]any{"id": nextID, "method": "Input.dispatchMouseEvent", "params": params})
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				return err
			}
			for {
				_, message, err := conn.Read(ctx)
				if err != nil {
					return err
				}
				var response struct {
					ID    int             `json:"id"`
					Error json.RawMessage `json:"error"`
				}
				if json.Unmarshal(message, &response) != nil || response.ID != nextID {
					continue
				}
				if len(response.Error) > 0 {
					return fmt.Errorf("CDP mouse event: %s", response.Error)
				}
				return nil
			}
		}
		for _, phase := range []string{"success"} {
			var drag activeLaneNativeDrag
			select {
			case drag = <-ready:
			case <-ctx.Done():
				result <- ctx.Err()
				return
			}
			if drag.phase != phase {
				result <- fmt.Errorf("phase %q, want %q", drag.phase, phase)
				return
			}
			for _, params := range []map[string]any{
				{"type": "mouseMoved", "x": drag.firstX, "y": drag.firstY},
				{"type": "mousePressed", "x": drag.firstX, "y": drag.firstY, "button": "left", "buttons": 1, "clickCount": 1},
				{"type": "mouseMoved", "x": drag.dropX, "y": drag.dropY, "button": "left", "buttons": 1},
			} {
				if err := dispatch(params); err != nil {
					result <- err
					return
				}
			}
			select {
			case observed := <-motion:
				if observed != phase {
					result <- fmt.Errorf("motion phase %q, want %q", observed, phase)
					return
				}
			case <-ctx.Done():
				result <- ctx.Err()
				return
			}
			if err := dispatch(map[string]any{"type": "mouseReleased", "x": drag.dropX, "y": drag.dropY, "button": "left", "buttons": 0, "clickCount": 1}); err != nil {
				result <- err
				return
			}
		}
		return
	}()
	return result
}

func freeTCPPortForActiveLaneBrowser(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func TestActiveLaneGroupedDragUsesRealHandlerPersistenceAndRollbackInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	htmxJS, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Native Active lane persistence").Build()
	model := tc.CreateLLMConfig().WithName("Native Active lane model").WithProvider(models.ProviderTest).WithModel("test-model").AsDefault().Build()
	tail := tc.CreateTask(project.ID).WithTitle("Existing running tail").WithCategory(models.CategoryActive).WithStatus(models.StatusRunning).Build()
	makeBacklog := func(title string) *models.Task {
		task := tc.CreateTask(project.ID).WithTitle(title).WithCategory(models.CategoryBacklog).Build()
		task.AgentID = &model.ID
		if err := tc.taskRepo.Update(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	first, second := makeBacklog("Success first"), makeBacklog("Success second")
	failFirst, failSecond := makeBacklog("Failure first"), makeBacklog("Failure second")
	if _, err := tc.db.ExecContext(ctx, `CREATE TRIGGER fail_native_second_active_lane BEFORE UPDATE ON tasks WHEN OLD.id = '`+failSecond.ID+`' AND NEW.category = 'active' BEGIN SELECT RAISE(FAIL, 'forced native later failure'); END`); err != nil {
		t.Fatal(err)
	}

	ready := make(chan activeLaneNativeDrag, 2)
	motion := make(chan string, 2)
	result := make(chan string, 1)
	runner := `<script>window.addEventListener('DOMContentLoaded',function(){
	function wait(check){return new Promise(function(resolve,reject){var start=performance.now();(function poll(){try{if(check())return resolve()}catch(e){return reject(e)}if(performance.now()-start>8000)return reject(new Error('timeout'));setTimeout(poll,10)})()})}
	async function coords(phase,a,b){var ac=document.getElementById('task-'+a),bc=document.getElementById('task-'+b);ac.scrollIntoView({block:'center',inline:'center'});await new Promise(function(resolve){requestAnimationFrame(function(){requestAnimationFrame(resolve)})});bc.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,ctrlKey:true}));ac.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,ctrlKey:true}));await wait(function(){return ac.classList.contains('task-selected')&&bc.classList.contains('task-selected')});var ar=ac.getBoundingClientRect(),br=bc.getBoundingClientRect(),zr=document.querySelector('.task-drop-zone[data-status="running"]').getBoundingClientRect();await fetch('/native-ready?phase='+phase+'&fx='+(ar.left+12)+'&fy='+(ar.top+12)+'&sx='+(br.left+12)+'&sy='+(br.top+12)+'&dx='+(zr.left+zr.width/2)+'&dy='+(zr.top+Math.min(40,zr.height/2)),{method:'POST'});await wait(function(){return ac.classList.contains('dragging')&&bc.classList.contains('dragging')});await fetch('/native-motion?phase='+phase,{method:'POST'})}
	(async function(){await wait(function(){return window.htmx&&document.getElementById('task-` + first.ID + `')});await coords('success','` + first.ID + `','` + second.ID + `');await wait(function(){var z=document.querySelector('.task-drop-zone[data-status="running"]'),ids=Array.from(z.querySelectorAll(':scope > [data-task-id]')).map(function(c){return c.dataset.taskId});return ids.slice(-2).join(',')==='` + first.ID + `,` + second.ID + `'&&!document.querySelector('[data-kanban-move-generation]')});await htmx.ajax('GET','/tasks?project_id=` + project.ID + `',{target:'#kanban-board',swap:'outerHTML'});await wait(function(){return document.getElementById('task-` + first.ID + `').dataset.taskStatus==='running'});window.beginKanbanMove(['` + failFirst.ID + `','` + failSecond.ID + `'],{category:'active',status:'running'},'PATCH','/tasks/batch-category',{task_ids:'` + failFirst.ID + `,` + failSecond.ID + `',category:'active',target_status:'running',project_id:'` + project.ID + `'},'forced failure');await wait(function(){return document.getElementById('task-` + failFirst.ID + `').dataset.taskCategory==='backlog'&&document.getElementById('task-` + failSecond.ID + `').dataset.taskCategory==='backlog'&&!document.querySelector('[data-kanban-move-generation]')});fetch('/browser-result?status=pass',{method:'POST'})})().catch(function(e){fetch('/browser-result?status=fail&message='+encodeURIComponent(e.stack||e),{method:'POST'})})});</script>`
	mux := http.NewServeMux()
	mux.HandleFunc("/htmx-2.0.4.min.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(htmxJS)
	})
	mux.HandleFunc("/native-ready", func(w http.ResponseWriter, r *http.Request) {
		parse := func(key string) float64 {
			var value float64
			_, _ = fmt.Sscan(r.URL.Query().Get(key), &value)
			return value
		}
		ready <- activeLaneNativeDrag{phase: r.URL.Query().Get("phase"), firstX: parse("fx"), firstY: parse("fy"), secondX: parse("sx"), secondY: parse("sy"), dropX: parse("dx"), dropY: parse("dy")}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/native-motion", func(w http.ResponseWriter, r *http.Request) {
		motion <- r.URL.Query().Get("phase")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/browser-result", func(w http.ResponseWriter, r *http.Request) {
		result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			tc.echo.ServeHTTP(w, r)
			return
		}
		tasks, loadErr := tc.handler.taskSvc.ListBoardByProjectWithCategorySorts(r.Context(), project.ID, "", "", "")
		if loadErr != nil {
			http.Error(w, loadErr.Error(), 500)
			return
		}
		var out bytes.Buffer
		if r.Header.Get("HX-Request") != "" {
			loadErr = components.KanbanBoard(tasks, project.ID, "", "", nil, nil).Render(r.Context(), &out)
		} else {
			loadErr = pages.Tasks([]models.Project{*project}, project, tasks, nil, nil, "", "").Render(r.Context(), &out)
		}
		if loadErr != nil {
			http.Error(w, loadErr.Error(), 500)
			return
		}
		body := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
		if r.Header.Get("HX-Request") == "" {
			body = strings.Replace(body, "</head>", runner+"</head>", 1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
	mux.Handle("/", tc.echo)
	server := httptest.NewServer(mux)
	defer server.Close()
	debugPort := freeTCPPortForActiveLaneBrowser(t)
	stderrPath := filepath.Join(t.TempDir(), "active-lane.stderr")
	stderr, _ := os.Create(stderrPath)
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--no-first-run", "--window-size=1280,900", fmt.Sprintf("--remote-debugging-port=%d", debugPort), "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/tasks?project_id="+project.ID)
	cmd.Stderr = stderr
	if err := startHandlerBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	driverErr := driveActiveLaneNativeDrags(debugPort, server.URL, ready, motion)
	var outcome string
	select {
	case outcome = <-result:
	case err := <-driverErr:
		outcome = "fail:" + err.Error()
	case <-time.After(35 * time.Second):
		outcome = "fail:timeout"
	}
	stopHandlerBrowserProcess(cmd)
	_ = stderr.Close()
	if !strings.HasPrefix(outcome, "pass:") {
		data, _ := os.ReadFile(stderrPath)
		t.Fatalf("active lane browser: %s\n%s", outcome, data)
	}
	for _, task := range []*models.Task{first, second} {
		loaded, _ := tc.taskRepo.GetByID(ctx, task.ID)
		if loaded.Status != models.StatusRunning || loaded.Category != models.CategoryActive || loaded.DisplayOrder <= tail.DisplayOrder {
			t.Fatalf("persisted success task = %#v", loaded)
		}
	}
	for _, task := range []*models.Task{failFirst, failSecond} {
		loaded, _ := tc.taskRepo.GetByID(ctx, task.ID)
		if loaded.Status != models.StatusPending || loaded.Category != models.CategoryBacklog {
			t.Fatalf("failed task changed = %#v", loaded)
		}
	}
}
