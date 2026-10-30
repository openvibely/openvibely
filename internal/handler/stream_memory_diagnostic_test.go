package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// TestStreamMemoryDiagnostic is an opt-in measurement, not a memory-budget assertion.
//
// Run from the repository root on a graphical macOS desktop:
//
//	OPENVIBELY_RUN_STREAM_MEMORY=1 \
//	OPENVIBELY_STREAM_RESULTS="$PWD/artifacts/stream-memory" \
//	go test ./internal/handler -run '^TestStreamMemoryDiagnostic$' -count=1 -v -timeout 6m
//
// Requires Xcode command-line tools, vmmap, footprint, and caffeinate. The test
// builds internal/testutil/nativechat itself and enables its visible-window mode;
// no prebuilt native-visible binary, source overlay, or old /tmp files are needed.
// Keep the window visible and avoid launching other WebKit apps during the run.
// Only an isolated test database is used; no user database or live model is accessed.
//
// Each run prints a fresh ov-stream-memory-* results directory containing stage
// JSON, vmmap summaries, footprint samples, and host.log. If the results variable
// is unset, the directory is created in the OS temporary directory. The workload
// sends 1,000 chunks and includes a full 30-second idle wait after streaming.
//
// For A/B measurements, run on each implementation with the same throttle and
// viewport, use separate output directories, repeat, and alternate run order.
// These samples measure process/graphics memory, not allocation churn or GC.
// See ../testutil/nativechat/README.md for the workload and interpretation limits.
func TestStreamMemoryDiagnostic(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_STREAM_MEMORY") != "1" {
		t.Skip("set OPENVIBELY_RUN_STREAM_MEMORY=1 on a graphical macOS desktop")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("vmmap and footprint measurements require macOS")
	}
	for _, tool := range []string{"vmmap", "footprint", "caffeinate"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	host := filepath.Join(t.TempDir(), "native-visible")
	build := exec.CommandContext(ctx, "go", "build", "-tags=openvibely_native_ui", "-o", host, "../testutil/nativechat")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native host: %v\n%s", err, output)
	}

	parent := os.Getenv("OPENVIBELY_STREAM_RESULTS")
	if parent != "" {
		require.NoError(t, os.MkdirAll(parent, 0700))
	}
	dir, err := os.MkdirTemp(parent, "ov-stream-memory-")
	require.NoError(t, err)
	t.Logf("memory artifacts: %s", dir)
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Streaming memory diagnostic")
	task := createTask(t, h, project.ID, "Streaming memory", func(t *models.Task) { t.Status = models.StatusRunning; t.Category = models.CategoryActive })
	ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Synthetic streaming measurement"
	})
	require.NoError(t, h.execRepo.AppendOutput(t.Context(), ex.ID, strings.Repeat("Initial streaming paragraph for the memory experiment.\n\n", 300)))
	webPIDs := func() (map[string]bool, error) {
		out, err := exec.CommandContext(ctx, "ps", "-axo", "pid,comm").Output()
		if err != nil {
			return nil, err
		}
		m := map[string]bool{}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, "com.apple.WebKit.WebContent.xpc/Contents/MacOS/") {
				m[strings.Fields(l)[0]] = true
			}
		}
		return m, nil
	}
	before, err := webPIDs()
	require.NoError(t, err)
	pid := ""
	done := make(chan string, 1)
	finish := func(result string) {
		select {
		case done <- result:
		default:
		}
	}

	stub := `<script>window._fixtureErrors=[];window.addEventListener("error",e=>(window._fixtureErrors.push(e.message),fetch("/diagnostic?error="+encodeURIComponent(e.message))));window._fixtureStreams=[];const fixtureController={attach(id,project,text,container){const es={addEventListener(){},close(){},onmessage:null};window._fixtureStreams.push({es,container,text});return es;}};let realController;Object.defineProperty(window,'openVibelyThreadController',{get(){return realController},set(v){v.attach=fixtureController.attach;realController=v},configurable:false});</script>`
	runner := `<script>window.addEventListener('DOMContentLoaded',()=>{(async()=>{
 const pause=ms=>new Promise(r=>setTimeout(r,ms));let streams;for(let i=0;i<200;i++){streams=window._fixtureStreams.filter(x=>x.es.onmessage);if(streams.length)break;await pause(50)}if(!streams.length)throw Error('no stream attached '+JSON.stringify({errors:window._fixtureErrors,attached:window._fixtureStreams.length,init:typeof window._initThreadStreaming,resume:document.querySelectorAll('[data-streaming-resume]').length,html:document.getElementById('task-thread-messages')?.innerHTML.slice(-1000)}));
 const original=window.renderLiveChatContent;let renders=0;window.renderLiveChatContent=function(...args){renders++;return original.apply(this,args)};
 const s=streams[streams.length-1];const chunk='More streaming text.\n';let sent=0;const start=performance.now();
 async function mark(stage){if(document.visibilityState!=='visible')throw Error('window hidden');if(!s.container.isConnected)throw Error('detached stream');if(window._fixtureErrors.length)throw Error(JSON.stringify(window._fixtureErrors));if(sent>0&&!s.container.textContent.includes('More streaming text.'))throw Error('stream not rendered');const response=await fetch('/mark',{method:'POST',body:JSON.stringify({stage,sent,renders,elapsed:(performance.now()-start)/1000,connected:s.container.isConnected,rendered:s.container.textContent.length,errors:window._fixtureErrors,streams:streams.length,nodes:document.querySelectorAll('*').length,bytes:s.text.length+sent*chunk.length,visible:document.visibilityState,width:innerWidth,height:innerHeight,dpr:devicePixelRatio})});if(!response.ok)throw Error(await response.text())}
 setInterval(()=>fetch('/diagnostic?state='+encodeURIComponent(JSON.stringify({sent,renders,connected:s.container.isConnected,text:s.container.textContent.slice(-100),length:s.container.textContent.length}))),2000);await pause(1500);await mark('baseline');
 const timer=setInterval(()=>{if(sent>=1000){clearInterval(timer);return}try{s.es.onmessage({data:chunk});sent++}catch(e){clearInterval(timer);fetch('/done?result='+encodeURIComponent(e.stack||String(e)))}},20);
 for(let batch=1;batch<=4;batch++){while(sent<batch*250)await pause(100);await mark('chunks'+batch*250)}
 clearInterval(timer);await pause(2000);await mark('settled');
 const actual=window.getChatRawContent?window.getChatRawContent(s.container):s.container.getAttribute('data-raw-content');if(actual!==s.text+chunk.repeat(sent))throw Error('content mismatch '+JSON.stringify({expected:s.text.length+sent*chunk.length,actual:actual?.length}));
 await pause(30000);await mark('idle30');await fetch('/done?result=pass');
 })().catch(e=>fetch('/done?result='+encodeURIComponent(String(e))))});</script>`
	var marks sync.Mutex
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/diagnostic" {
			fmt.Println("JS diagnostic", r.URL.RawQuery)
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/native-test-bridge" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/done" {
			finish(r.URL.Query().Get("result"))
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/mark" {
			marks.Lock()
			defer marks.Unlock()
			fail := func(err error) { finish(err.Error()); http.Error(w, err.Error(), 500) }
			index++
			var row json.RawMessage
			if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&row); err != nil {
				fail(err)
				return
			}
			if pid == "" {
				current, err := webPIDs()
				if err != nil {
					fail(err)
					return
				}
				candidates := []string{}
				for id := range current {
					if !before[id] {
						candidates = append(candidates, id)
					}
				}
				if len(candidates) != 1 {
					fail(fmt.Errorf("expected one new WebContent process, found %d; close unrelated browser launches and retry", len(candidates)))
					return
				}
				pid = candidates[0]
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.json", index)), row, 0600); err != nil {
				fail(err)
				return
			}
			for _, tool := range []string{"vmmap", "footprint"} {
				args := []string{"-summary", pid}
				if tool == "footprint" {
					args = []string{"-p", pid, "--noCategories", "-f", "bytes"}
				}
				sampleCtx, stop := context.WithTimeout(ctx, 15*time.Second)
				output, err := exec.CommandContext(sampleCtx, tool, args...).CombinedOutput()
				stop()
				writeErr := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.%s", index, tool)), output, 0600)
				if err != nil {
					fail(fmt.Errorf("%s: %w: %s", tool, err, output))
					return
				}
				if writeErr != nil {
					fail(writeErr)
					return
				}
			}
			t.Logf("sample pid=%s %s", pid, row)
			w.WriteHeader(204)
			return
		}
		if static.ServeAsset(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/events") {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/tasks/"+task.ID && r.Header.Get("HX-Request") == "" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			body := rec.Body.String()
			body = strings.Replace(body, "<head>", "<head>"+stub, 1)
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	logFile, err := os.Create(filepath.Join(dir, "host.log"))
	require.NoError(t, err)
	defer logFile.Close()
	cmd := exec.CommandContext(ctx, host, server.URL+"/tasks/"+task.ID+"?project_id="+project.ID)
	cmd.Env = append(os.Environ(), "OPENVIBELY_NATIVE_VISIBLE=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	awake := exec.CommandContext(ctx, "caffeinate", "-di", "-w", fmt.Sprint(cmd.Process.Pid))
	require.NoError(t, awake.Start())
	defer func() { _ = awake.Process.Kill(); _ = awake.Wait() }()
	select {
	case result := <-done:
		require.Equal(t, "pass", result, "see artifacts in %s", dir)
	case <-ctx.Done():
		t.Fatalf("diagnostic timeout; see %s", dir)
	}
}
