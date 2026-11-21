package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/pages"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerGrantPagesAndRevocation(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	project := createProject(t, h, "Grant pages")
	task := createTask(t, h, project.ID, "Desktop")
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{})
	scoped, err := h.computerSvc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	repo, err := h.projectRepo.Computers().ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, h.projectRepo.Computers().SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	require.NoError(t, repo.RequestHandoff(t.Context(), project.ID, "request_chrome_login", "https://example.test", "", ""))
	choices := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
	old, err := repo.CreateChromeGrant(t.Context(), project.ID, "task", choices, 0)
	require.NoError(t, err)
	for i := 0; i < 200; i++ {
		_, err := repo.CreateChromeGrant(t.Context(), project.ID, "task", choices, time.Now().Add(time.Hour).Unix())
		require.NoError(t, err)
	}
	e := echo.New()
	e.GET("/projects/:id/computer/chrome-grants", h.ProjectComputerChromeGrants)
	e.POST("/projects/:id/computer/chrome-grants", h.ProjectComputerChromeGrants)
	base := "/projects/" + project.ID + "/computer/chrome-grants?task_id=" + task.ID
	for _, offset := range []string{"-1", "oops", "99999999999999999999999"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest("GET", base+"&offset="+offset, nil))
		require.Equal(t, 400, rec.Code)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", base+"&offset=200", nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var page struct {
		Grants []models.ComputerChromeGrant `json:"grants"`
		Offset int                          `json:"offset"`
		More   bool                         `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Equal(t, 200, page.Offset)
	require.False(t, page.More)
	require.Len(t, page.Grants, 1)
	require.Equal(t, old.ID, page.Grants[0].ID)
	req := httptest.NewRequest("POST", base+"&offset=200", strings.NewReader(`{"grant_id":"`+old.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Empty(t, page.Grants, "fully revoked permissions should disappear after cleanup")
}

func TestComputerGrantPaginationBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	var page bytes.Buffer
	require.NoError(t, pages.ProjectComputer(nil, "project", "task").Render(t.Context(), &page))
	script := `<script>(async()=>{
 const q=s=>document.querySelector(s);const wait=async(f,label)=>{for(let n=0;n<200;n++){if(f())return;await new Promise(r=>setTimeout(r,25));}throw Error('Timed out '+label);};
 const result=document.createElement('div');result.id='grant-result';
 try {
 q('[data-computer-grants-open]').click();await wait(()=>q('[data-computer-grant-list]').textContent.includes('old.example'),'all permissions loaded');
 if(!q('#computer-grants-picker').matches(':popover-open'))throw Error('Permissions dropdown closed');
 if(q('[data-computer-grants-next]'))throw Error('Obsolete pagination controls');
 const searchInput=q('#computer-grants-picker input');
 const grantButton=q('[data-computer-grant-list] button');let activations=0;
 grantButton.addEventListener('click',()=>activations++);
 if(grantButton.hasAttribute('data-active'))throw Error('Unexpected default highlight');
 searchInput.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
 if(activations!==0)throw Error('Enter activated an unhighlighted permission');
 searchInput.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
 if(!grantButton.hasAttribute('data-active'))throw Error('ArrowDown did not highlight permission');
 grantButton.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
 if(activations!==1)throw Error('Enter did not activate highlighted permission');
 await wait(()=>q('[data-computer-grant-list]').textContent.includes('Revoked'),'permission revoked');
 const search=q('#computer-grants-picker input');search.value='missing.example';search.dispatchEvent(new Event('input'));if(!q('[data-computer-grant-list] button').hidden)throw Error('Search did not filter grant');
 search.value='old.example';search.dispatchEvent(new Event('input'));if(q('[data-computer-grant-list] button').hidden)throw Error('Matching grant hidden');

 result.dataset.status='pass';
 } catch(e) { result.dataset.status='fail';result.textContent=e.message; }
 document.body.append(result);
 })();</script>`
	html := strings.Replace(page.String(), "</body>", script+"</body>", 1)
	var mu sync.Mutex
	grantRevoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, html)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chrome-grants"):
			if r.Method == "POST" {
				var input struct {
					GrantID string `json:"grant_id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.GrantID != "old-grant" {
					t.Errorf("unexpected grant %q", input.GrantID)
				}
				grantRevoked = true
			}
			if r.URL.Query().Get("offset") == "200" {
				fmt.Fprintf(w, `{"grants":[{"id":"old-grant","scope":"task","selections":[{"domain":"old.example","name":"session","profile":"Default"}],"revoked":%t}],"offset":200,"has_more":false,"next_offset":201}`, grantRevoked)
			} else {
				fmt.Fprint(w, `{"grants":[],"offset":0,"has_more":true,"next_offset":200}`)
			}

		default:
			fmt.Fprint(w, `{"enabled":false,"ready":false}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=12000", "--dump-dom", server.URL)
	out, err := runHandlerBrowserProcess(t, cmd)
	require.NoError(t, err)
	if !bytes.Contains(out, []byte(`id="grant-result" data-status="pass"`)) {
		t.Fatalf("pagination: %s", out[max(0, len(out)-3000):])
	}
	mu.Lock()
	defer mu.Unlock()
	require.True(t, grantRevoked)
}
