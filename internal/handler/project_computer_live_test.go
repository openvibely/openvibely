package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type liveComputerFixture struct{ service.ComputerConnection }

func (liveComputerFixture) Provider() string { return "local" }
func (p liveComputerFixture) Resolve(context.Context, string) (service.ComputerConnection, error) {
	return p.ComputerConnection, nil
}
func (p liveComputerFixture) Start(context.Context, string) (service.ComputerConnection, error) {
	return p.ComputerConnection, nil
}
func (liveComputerFixture) Stop(context.Context, string) error { return nil }

// Opt-in only: the endpoint must be a disposable test desktop, never a user's computer.
func TestProjectComputerLiveViewer(t *testing.T) {
	endpoint := os.Getenv("OPENVIBELY_TEST_COMPUTER")
	if endpoint == "" {
		t.Skip("set OPENVIBELY_TEST_COMPUTER to a disposable desktop")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	db := teststores.New(t)
	repo := db.Projects
	project := &models.Project{Name: "Live desktop fixture"}
	if err := repo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if err := repo.Computers().Configure(t.Context(), project.ID, "local"); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Viewer task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := db.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	fixture := liveComputerFixture{service.ComputerConnection{Endpoint: endpoint, Token: os.Getenv("OPENVIBELY_TEST_COMPUTER_TOKEN")}}
	h := &Handler{projectRepo: repo, projectSvc: service.NewProjectService(repo), computerSvc: service.NewProjectComputerService(repo, fixture)}
	h.computerSvc.SetAttachmentDirectory(t.TempDir())
	scoped, err := h.computerSvc.ForTask(t.Context(), project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := scoped.SetTaskEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	h.SetDesktopMode(true)
	e.Use(h.AuthMiddleware())
	e.GET("/computer-stream", h.ProjectComputerNativeStream)
	backend := httptest.NewServer(e)
	defer backend.Close()
	h.SetComputerDesktopBackendURL(backend.URL)
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	e.GET("/projects/:id/computer/files", h.ProjectComputerFiles)
	e.PUT("/projects/:id/computer/files", h.ProjectComputerFiles)
	e.POST("/projects/:id/computer/import-file", h.ProjectComputerImportFile)
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	e.GET("/projects/:id/computer/stream/:mode", h.ProjectComputerStream)
	var page bytes.Buffer
	if err := pages.ProjectComputer([]models.Project{*project}, project.ID, task.ID).Render(t.Context(), &page); err != nil {
		t.Fatal(err)
	}
	script := `<script>(async()=>{
 const finish=(status,message)=>{let node=document.createElement('div');node.id='computer-test-result';node.dataset.status=status;node.textContent=message;document.body.append(node);};
 const wait=async f=>{for(let n=0;n<100;n++){if(f())return;await new Promise(r=>setTimeout(r,100));}throw new Error('viewer timed out');};
 let takeoverCalls=0;const originalFetch=window.fetch;window.fetch=(url,options)=>{if(/\/(takeover|release)(?:\?|$)/.test(String(url)))takeoverCalls++;return originalFetch(url,options);};
 const liveFrame=()=>{const c=document.querySelector('[data-computer-screen] canvas:not([data-retained-frame])');return c && c.width===1440 && c.height===900 && c.getContext('2d').getImageData(720,450,1,1).data[3]>0;};
 try{
  await wait(()=>document.querySelector('[data-computer-screen] canvas'));
  await wait(liveFrame);
  const canvas=document.querySelector('[data-computer-screen] canvas');
  document.querySelector('[data-computer-preview-open]').click();
  if(!document.querySelector('[data-computer-dialog]').open || !document.querySelector('[data-computer-expanded]').contains(canvas))throw Error('full viewer did not retain live screen');

  await wait(()=>document.querySelector('[aria-label="Remote computer keyboard"]'));
  await wait(liveFrame);
  if(document.querySelector('[data-computer-status]'))throw Error('Obsolete status element remains');
  const root=document.querySelector('[data-computer-project]');
  const filesURL='/projects/'+root.dataset.computerProject+'/computer/files?task_id='+root.dataset.computerTask+'&path=viewer-fixture.txt';
  const statusURL='/projects/'+root.dataset.computerProject+'/computer/status?task_id='+root.dataset.computerTask;
  if((await (await fetch(statusURL)).json()).held)throw Error('Fullscreen acquired exclusive control');
  if(document.querySelector('[data-computer-dialog]').contains(document.querySelector('[data-computer-files]')))throw Error('Files moved into fullscreen');
  document.querySelector('[data-computer-close]').click();
  await wait(()=>!document.querySelector('[data-computer-dialog]').open && !document.querySelector('[aria-label="Remote computer keyboard"]'));
  await wait(liveFrame);
  const uploaded=await fetch(filesURL,{method:'PUT',body:'viewer-file'});if(!uploaded.ok)throw Error('upload failed');
  const downloaded=await fetch(filesURL);if(await downloaded.text()!=='viewer-file')throw Error('download differed');
  document.querySelector('[data-computer-files]').open=true;
  document.querySelector('[data-computer-files-refresh]').click();
  const importButton=()=>[...document.querySelectorAll('[data-computer-file-list] button')].find(b=>b.textContent==='Add to task'||b.textContent==='Added');
  await wait(()=>importButton());importButton().click();await wait(()=>importButton().textContent==='Added');
  const duplicate=await fetch(filesURL,{method:'PUT',body:'replacement'});if(duplicate.ok)throw Error('overwrote user file');
  if(takeoverCalls)throw Error('Normal viewer requested takeover or release');
  const handoff=await fetch('/fixture-login',{method:'POST'});if(!handoff.ok)throw Error('Private handoff failed');
  await wait(()=>!document.querySelector('[data-computer-login]').hidden);
  document.querySelector('[data-computer-preview-open]').click();await wait(()=>document.querySelector('[data-computer-dialog]').open);document.querySelector('[data-computer-close]').click();await wait(()=>!document.querySelector('[data-computer-dialog]').open);if(document.querySelector('[data-computer-login]').hidden)throw Error('Closing viewer cleared private login');
  document.querySelector('[data-computer-action="login-done"]').click();
  await wait(()=>document.querySelector('[data-computer-login]').hidden);
  finish('pass','desktop frame, shared control, files, private handoff verified');
 }catch(e){finish('fail',e.message);}
})();</script>`
	html := strings.Replace(page.String(), "</body>", script+"</body>", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/fixture-login" {
			rt := h.computerSvc.RuntimeTools(r.Context(), project.ID, task.ID)
			_, _, _, err := rt.Executor(r.Context(), "request_computer_help", json.RawMessage(`{"reason":"Verify the disposable test desktop"}`))
			if err != nil {
				http.Error(w, err.Error(), 500)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, html)
			return
		}
		r.Header.Set("X-OpenVibely-Native-Window", "1")
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	profile := t.TempDir()
	cmd := exec.CommandContext(ctx, chrome, "--headless", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+profile, "--remote-debugging-port=0", server.URL)
	if err := startHandlerBrowserProcess(t, cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { killHandlerBrowserProcess(cmd); cmd.Wait() }()
	var port string
	for n := 0; n < 200; n++ {
		data, _ := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if len(data) > 0 {
			port = strings.Split(string(data), "\n")[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if port == "" {
		t.Fatal("browser debugger did not start")
	}
	response, err := http.Get("http://127.0.0.1:" + port + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(response.Body).Decode(&targets)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, candidate := range targets {
		if candidate.Type == "page" {
			target = candidate.URL
			break
		}
	}
	socket, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	request, _ := json.Marshal(map[string]any{"id": 1, "method": "Runtime.evaluate", "params": map[string]any{"awaitPromise": true, "returnByValue": true, "expression": `new Promise(resolve=>{const timer=setInterval(()=>{const node=document.getElementById('computer-test-result');if(node){clearInterval(timer);resolve(node.dataset.status+': '+node.textContent)}},100)})`}})
	if err := socket.Write(ctx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	for {
		_, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			ID     int
			Result struct{ Result struct{ Value string } }
		}
		if bytes.Contains(data, []byte(`"error"`)) {
			time.Sleep(100 * time.Millisecond)
			if err := socket.Write(ctx, websocket.MessageText, request); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if json.Unmarshal(data, &result) == nil && result.ID == 1 {
			if !strings.HasPrefix(result.Result.Result.Value, "pass:") {
				t.Fatal(string(data))
			}
			attachments, err := db.Attachments.ListByTask(ctx, task.ID)
			if err != nil || len(attachments) != 1 || attachments[0].FileName != "viewer-fixture.txt" {
				t.Fatalf("imported attachments: %v, %v", attachments, err)
			}
			break
		}
	}
}
