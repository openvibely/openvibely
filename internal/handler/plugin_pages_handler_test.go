package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

type pluginTestEnv struct {
	stores *teststores.Env
	h      *Handler
	e      *echo.Echo
	mgr    *pageplugins.Manager
	projA  *models.Project
	projB  *models.Project
	inA    *models.PagePluginInstance
	inB    *models.PagePluginInstance
	agent  *models.LLMConfig
	starts []streamingResponseParams
}

func setupPluginPages(t *testing.T) *pluginTestEnv {
	t.Helper()
	h, e, llmRepo, stores := setupTestHandlerWithStores(t)
	mgr := pageplugins.NewManager(stores.PagePlugins, t.TempDir())
	t.Cleanup(mgr.StopAll)
	h.SetPagePlugins(mgr)
	env := &pluginTestEnv{stores: stores, h: h, e: e, mgr: mgr, projA: createProject(t, h, "Alpha"), projB: createProject(t, h, "Beta")}
	env.agent = createAgent(t, llmRepo)
	h.startStreamingResponseOverride = func(p streamingResponseParams) error {
		env.starts = append(env.starts, p)
		return nil
	}
	ctx := context.Background()
	pkg, err := mgr.InstallFromDir(ctx, "../pageplugins/testdata/sales", "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		proj *models.Project
		out  **models.PagePluginInstance
	}{{env.projA, &env.inA}, {env.projB, &env.inB}} {
		in, err := mgr.CreateInstance(ctx, target.proj.ID, pkg.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.Enable(ctx, in.ID); err != nil {
			t.Fatal(err)
		}
		*target.out = in
	}
	return env
}

func (env *pluginTestEnv) do(t *testing.T, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

var handlePattern = regexp.MustCompile(`data-plugin-handle="([0-9a-f]+)"`)

func (env *pluginTestEnv) openPage(t *testing.T, in *models.PagePluginInstance) string {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/p/"+in.ID+"?project_id="+in.ProjectID, "", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("open page: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatal("plugin iframe is not sandboxed")
	}
	m := handlePattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no bridge handle rendered")
	}
	return m[1]
}

func bridgeHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/json", "X-OpenVibely-Plugin-Bridge": "1"}
}

func (env *pluginTestEnv) bridge(t *testing.T, in *models.PagePluginInstance, projectID, handle, typ, payload string) (*httptest.ResponseRecorder, pluginBridgeResponse) {
	t.Helper()
	msg := `{"v":1,"type":"` + typ + `","id":"m1","handle":"` + handle + `","payload":` + payload + `}`
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+in.ID+"/bridge?project_id="+url.QueryEscape(projectID), msg, bridgeHeaders())
	var resp pluginBridgeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func TestPluginPage_CrossProjectAccessDenied(t *testing.T) {
	env := setupPluginPages(t)
	rec := env.do(t, http.MethodGet, "/p/"+env.inA.ID+"?project_id="+env.projB.ID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project page load = %d", rec.Code)
	}
	handleA := env.openPage(t, env.inA)
	rec, _ = env.bridge(t, env.inA, env.projB.ID, handleA, "op.invoke", `{"operation":"get_report"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project bridge = %d", rec.Code)
	}
	handleB := env.openPage(t, env.inB)
	rec, _ = env.bridge(t, env.inA, env.projA.ID, handleB, "op.invoke", `{"operation":"get_report"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign handle accepted = %d", rec.Code)
	}
	rec = env.do(t, http.MethodGet, "/p/"+env.inA.ID+"?project_id="+models.AllProjectsID, "", nil)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "project_id="+env.projA.ID) {
		t.Fatalf("all-projects open did not keep owning project: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPluginBridge_RequiresBridgeHeader(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	msg := `{"v":1,"type":"ready","id":"m1","handle":"` + handle + `","payload":{}}`
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/bridge?project_id="+env.projA.ID, msg, map[string]string{"Content-Type": "text/plain"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("simple cross-site post accepted: %d", rec.Code)
	}
	h := bridgeHeaders()
	h["Origin"] = "https://evil.example"
	rec = env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/bridge?project_id="+env.projA.ID, msg, h)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post accepted: %d", rec.Code)
	}
}

func TestPluginNav_AllProjectsShowsOwningProject(t *testing.T) {
	env := setupPluginPages(t)
	rec := env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+models.AllProjectsID, "", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Alpha") || !strings.Contains(body, "Beta") {
		t.Fatalf("all-projects nav missing project labels: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, "project_id="+env.projA.ID) || !strings.Contains(body, "project_id="+env.projB.ID) {
		t.Fatal("nav links do not open in owning project")
	}
	rec = env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+env.projA.ID, "", nil)
	if strings.Contains(rec.Body.String(), env.inB.ID) {
		t.Fatal("project nav leaked another project's page")
	}
}

func TestPluginBridge_ButtonAndAgentToolShareOperation(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)

	rec, resp := env.bridge(t, env.inA, env.projA.ID, handle, "op.invoke", `{"operation":"refresh_data","input":{}}`)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("page refresh failed: %d %s", rec.Code, rec.Body.String())
	}
	var rep struct {
		Revision int `json:"revision"`
	}
	json.Unmarshal(resp.Result, &rep)
	if rep.Revision != 2 {
		t.Fatalf("revision after page refresh = %d", rep.Revision)
	}

	rec, _ = env.bridge(t, env.inA, env.projA.ID, handle, "context.set", `{"summary":"North region, March","filters":{"region":"north"},"selection":["r1"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("context.set failed: %s", rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/chat?project_id="+env.projA.ID, "message=Refresh+the+data+and+summarize+it", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusOK || len(env.starts) != 1 {
		t.Fatalf("side chat turn not started: %d %s", rec.Code, rec.Body.String())
	}
	p := env.starts[0]
	if p.ProjectID != env.projA.ID || p.RuntimeTools == nil {
		t.Fatalf("chat turn not bound to page project/tools: %+v", p.ProjectID)
	}
	if !strings.Contains(p.SystemContext, "North region, March") || !strings.Contains(p.SystemContext, "untrusted_page_context") {
		t.Fatalf("explicit page context missing from turn:\n%s", p.SystemContext)
	}
	out, _, isErr, err := p.RuntimeTools.Executor(context.Background(), pageintegration.ToolName("refresh_data"), json.RawMessage(`{}`))
	if err != nil || isErr {
		t.Fatalf("agent refresh tool failed: %s %v", out, err)
	}
	json.Unmarshal([]byte(out), &rep)
	if rep.Revision != 3 {
		t.Fatalf("agent tool did not run the page's operation: revision %d", rep.Revision)
	}
	snap, err := env.mgr.Store().GetTurnContext(context.Background(), p.ExecID)
	if err != nil || !strings.Contains(snap, "North region") {
		t.Fatalf("turn context not persisted: %q %v", snap, err)
	}

	// A second turn is queued without interleaving with the active turn.
	rec = env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/chat?project_id="+env.projA.ID, "message=again", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "queued-input-row") {
		t.Fatalf("concurrent turn = %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/chat/stop?project_id="+env.projA.ID, "", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d", rec.Code)
	}
	taskID, _ := env.mgr.Store().GetConversationTask(context.Background(), env.inA.ID)
	task, _ := env.h.taskRepo.GetByID(context.Background(), taskID)
	if task == nil || task.ProjectID != env.projA.ID || task.Status == models.StatusRunning {
		t.Fatalf("conversation task not persisted/stopped: %+v", task)
	}

	// Project B's page is unaffected by project A's refreshes.
	handleB := env.openPage(t, env.inB)
	_, resp = env.bridge(t, env.inB, env.projB.ID, handleB, "op.invoke", `{"operation":"get_report"}`)
	json.Unmarshal(resp.Result, &rep)
	if rep.Revision != 1 {
		t.Fatalf("project B state changed: revision %d", rep.Revision)
	}
}

func TestPluginAsset_SandboxHeadersAndBlockedFiles(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/app.js", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("missing sandbox CSP: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("opaque-origin module and font requests require CORS")
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("plugin assets must not enable credentialed CORS")
	}
	for _, p := range []string{"plugin.json", "..%2f..%2fplugin.json", "missing.js"} {
		if rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/"+p, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s served: %d", p, rec.Code)
		}
	}
	rec = env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/handle/retire?project_id="+env.projA.ID+"&handle="+handle, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("retire = %d", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/app.js", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("retired handle still serves assets: %d", rec.Code)
	}
}

func TestPluginSettings_DisableRemovesFromNavAndBlocksAPI(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	rec := env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/disable?project_id="+env.projB.ID, "", map[string]string{"HX-Request": "true"})
	if in, _ := env.mgr.Store().GetInstance(context.Background(), env.inA.ID); !in.Enabled {
		t.Fatalf("instance disabled from another project's settings (status %d)", rec.Code)
	}
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/disable?project_id="+env.projA.ID, "", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = env.bridge(t, env.inA, env.projA.ID, handle, "op.invoke", `{"operation":"get_report"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled page bridge = %d", rec.Code)
	}
	rec = env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+env.projA.ID, "", nil)
	if strings.Contains(rec.Body.String(), env.inA.ID) {
		t.Fatal("disabled page still in nav")
	}
}

// Match browser requests: only the parent page carries the login cookie;
// sandboxed subresources authenticate with the page-load capability.
func TestPluginAsset_CookieFreeCapabilityWithAuthentication(t *testing.T) {
	for _, organization := range []bool{false, true} {
		name := "local"
		if organization {
			name = "organization"
		}
		t.Run(name, func(t *testing.T) {
			env := setupPluginPages(t)
			username, password := "admin", "secret"
			env.h.SetAuthConfig(auth.Config{Enabled: true, Username: username, Password: password, SessionSecret: "plugin-test-secret"})
			if organization {
				tc, _ := organizationTestApp(t, true)
				env.h.SetAuthConfig(*tc.handler.authCfg)
				env.h.SetOrganizationService(tc.handler.organizationSvc)
				username, password = "alice", "alice-password-long"
			}
			env.e = echo.New()
			env.e.Use(env.h.AuthMiddleware())
			env.h.RegisterRoutes(env.e)
			login := orgRequest(env.e, http.MethodPost, "/login", url.Values{"username": {username}, "password": {password}}, nil)
			cookie := orgCookie(t, login)
			pageURL := "/p/" + env.inA.ID + "?project_id=" + env.inA.ProjectID
			page := env.do(t, http.MethodGet, pageURL, "", map[string]string{"Cookie": cookie.String()})
			match := handlePattern.FindStringSubmatch(page.Body.String())
			if page.Code != http.StatusOK || match == nil {
				t.Fatalf("authenticated page: %d %s", page.Code, page.Body.String())
			}
			handle := match[1]
			for _, file := range []string{"index.html", "app.js"} {
				rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/"+file, "", map[string]string{"Origin": "null"})
				if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
					t.Fatalf("cookie-free %s: %d %s", file, rec.Code, rec.Body.String())
				}
				if rec.Header().Get("Content-Security-Policy") != pluginAssetCSP {
					t.Fatal("asset lost sandbox CSP")
				}
			}
			for _, asset := range []string{"invalid/app.js", handle + "/plugin.json"} {
				if rec := env.do(t, http.MethodGet, "/plugin-assets/"+asset, "", nil); rec.Code != http.StatusNotFound {
					t.Fatalf("restricted asset %s: %d", asset, rec.Code)
				}
			}
			// A valid asset capability must not bypass authentication for page/API access.
			for _, target := range []string{pageURL, "/api/plugin-pages/" + env.inA.ID + "/events?project_id=" + env.inA.ProjectID + "&handle=" + handle} {
				rec := env.do(t, http.MethodGet, target, "", nil)
				if rec.Code != http.StatusFound {
					t.Fatalf("protected route %s: %d", target, rec.Code)
				}
			}
			resolved, err := env.mgr.Resolve(t.Context(), env.inA.ID, env.inA.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if organization {
				orphan, err := env.mgr.IssueHandle(resolved, "user:removed-user")
				if err != nil {
					t.Fatal(err)
				}
				if rec := env.do(t, http.MethodGet, "/plugin-assets/"+orphan.Token+"/app.js", "", nil); rec.Code != http.StatusNotFound {
					t.Fatalf("removed account asset: %d", rec.Code)
				}
			}
			env.mgr.RetireHandle(handle)
			if rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/app.js", "", nil); rec.Code != http.StatusNotFound {
				t.Fatalf("retired asset: %d", rec.Code)
			}
		})
	}
}

func TestPluginChat_ConversationScopeSurvivesReopen(t *testing.T) {
	env := setupPluginPages(t)
	send := func(in *models.PagePluginInstance) streamingResponseParams {
		t.Helper()
		rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+in.ID+"/chat?project_id="+in.ProjectID, "message=hello", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
		if rec.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
		}
		p := env.starts[len(env.starts)-1]
		if p.IsTaskFollowup || !p.TaskScopedChat {
			t.Fatal("page chat must isolate state without selecting coding-agent behavior")
		}
		if streamingTransportScope(p) != "task:"+p.TaskID {
			t.Fatal("page scope must use persisted conversation task")
		}
		return p
	}
	a := send(env.inA)
	b := send(env.inB)
	if streamingTransportScope(a) == streamingTransportScope(b) {
		t.Fatal("pages share a scope")
	}
	if streamingTransportScope(a) == streamingTransportScope(streamingResponseParams{ProjectID: env.projA.ID}) {
		t.Fatal("page shares project-chat scope")
	}
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/chat/stop?project_id="+env.projA.ID, "", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop: %d", rec.Code)
	}
	// Recreate the manager to discard all in-memory page state, as on restart.
	env.h.SetPagePlugins(pageplugins.NewManager(env.mgr.Store(), t.TempDir()))
	env.openPage(t, env.inA)
	reopened := send(env.inA)
	if reopened.ExecID == a.ExecID || reopened.TaskID != a.TaskID || streamingTransportScope(reopened) != streamingTransportScope(a) {
		t.Fatal("reopened page did not resume its persisted conversation scope")
	}
}

func TestPluginDisableCancelsOnlyOwningConversation(t *testing.T) {
	for _, inline := range []bool{false, true} {
		name := "side-chat"
		if inline {
			name = "inline-analysis"
		}
		t.Run(name, func(t *testing.T) {
			env := setupPluginPages(t)
			handle := env.openPage(t, env.inA)
			send := func(in *models.PagePluginInstance) {
				t.Helper()
				rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+in.ID+"/chat?project_id="+in.ProjectID, "message=hello", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
				if rec.Code != http.StatusOK {
					t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
				}
			}
			if inline {
				rec, _ := env.bridge(t, env.inA, env.projA.ID, handle, "analyze.request", `{"prompt":"analyze this report"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("analyze: %d %s", rec.Code, rec.Body.String())
				}
			} else {
				send(env.inA)
			}
			a := env.starts[0]
			send(env.inB)
			b := env.starts[1]
			cancelled := make(chan struct{}, 1)
			env.h.workerSvc.RegisterCancel(a.TaskID, func() { cancelled <- struct{}{} })
			rec := env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/disable?project_id="+env.projA.ID, "", map[string]string{"HX-Request": "true"})
			if rec.Code != http.StatusOK {
				t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
			}
			select {
			case <-cancelled:
			default:
				t.Fatal("existing Stop mechanism was not invoked")
			}
			if a.CancellationContext == nil || a.CancellationContext.Err() == nil {
				t.Fatal("late-starting AI work not cancelled")
			}
			if b.CancellationContext == nil || b.CancellationContext.Err() != nil {
				t.Fatal("other page cancelled")
			}
			exec, err := env.h.execRepo.GetByID(t.Context(), a.ExecID)
			if err != nil || exec.Status != models.ExecCancelled {
				t.Fatalf("execution not cancelled: %+v %v", exec, err)
			}
			other, err := env.h.execRepo.GetByID(t.Context(), b.ExecID)
			if err != nil || other.Status != models.ExecRunning {
				t.Fatalf("other execution affected: %+v %v", other, err)
			}
			taskID, err := env.mgr.Store().GetConversationTask(t.Context(), env.inA.ID)
			if err != nil || taskID != a.TaskID {
				t.Fatal("disable removed saved conversation")
			}
			if err := env.mgr.Enable(t.Context(), env.inA.ID); err != nil {
				t.Fatal(err)
			}
			send(env.inA)
			fresh := env.starts[len(env.starts)-1]
			if fresh.TaskID != a.TaskID || fresh.CancellationContext.Err() != nil || a.CancellationContext.Err() == nil {
				t.Fatal("enable did not create a new lifetime for the existing conversation")
			}
		})
	}
}

// Navigation revokes existing subscriptions but must not act as Stop.
func TestPluginNavigationRetiresSubscriptionNotConversation(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/chat?project_id="+env.inA.ProjectID, "message=hello", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	started := env.starts[0]
	server := httptest.NewServer(env.e)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/plugin-pages/" + env.inA.ID + "/events?project_id=" + env.inA.ProjectID + "&handle=" + handle)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", response.StatusCode)
	}
	finished := make(chan string, 1)
	go func() { raw, _ := io.ReadAll(response.Body); finished <- string(raw) }()
	env.mgr.RetireHandle(handle)
	select {
	case body := <-finished:
		if !strings.Contains(body, "event: closed") {
			t.Fatal(body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retired subscription stayed open")
	}
	if started.CancellationContext.Err() != nil {
		t.Fatal("navigation cancelled response")
	}
	exec, err := env.h.execRepo.GetByID(t.Context(), started.ExecID)
	if err != nil || exec.Status != models.ExecRunning {
		t.Fatal("navigation stopped persisted execution")
	}
	if n := env.mgr.Hub.SubscriberCount(env.inA.ID); n != 0 {
		t.Fatalf("leaked %d subscriptions", n)
	}
	env.openPage(t, env.inA)
	taskID, err := env.mgr.Store().GetConversationTask(t.Context(), env.inA.ID)
	if err != nil || taskID != started.TaskID {
		t.Fatal("navigation lost conversation")
	}
}

func TestPluginSettingsHasNoSeparateApprovalAction(t *testing.T) {
	env := setupPluginPages(t)
	rec := env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/approve?project_id="+env.projA.ID, "", map[string]string{"HX-Request": "true"})
	if rec.Code != 404 {
		t.Fatalf("obsolete approval route: %d", rec.Code)
	}
}

func TestPluginChatComposerStaleStop(t *testing.T) {
	env := setupPluginPages(t)
	endpoint := "/api/plugin-pages/" + env.inA.ID
	query := "?project_id=" + env.projA.ID
	rec := env.do(t, http.MethodPost, endpoint+"/chat"+query, "message=hello", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	turn := env.starts[len(env.starts)-1]
	rec = env.do(t, http.MethodPost, endpoint+"/chat/stop"+query+"&expected_turn_id=old-turn", "", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), turn.ExecID) {
		t.Fatal("stale stop lost current composer state", rec.Body.String())
	}
	active, err := env.h.execRepo.GetByID(context.Background(), turn.ExecID)
	if err != nil || active.Status != models.ExecRunning {
		t.Fatal("stale stop cancelled current turn", err)
	}
	rec = env.do(t, http.MethodGet, endpoint+"/chat/composer-action?project_id="+env.projB.ID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatal("composer state leaked across projects", rec.Code)
	}
}

func TestPluginAnalyzeUsesComposerSettings(t *testing.T) {
	env := setupPluginPages(t)
	agent := createAgent(t, env.h.llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Selected composer model"
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-6-astra"
		a.ReasoningEffort = "medium"
	})
	handle := env.openPage(t, env.inA)
	query := url.Values{"project_id": {env.projA.ID}, "agent_id": {agent.ID}, "reasoning_effort": {"high"}, "service_tier": {"priority"}}
	body := `{"v":1,"type":"analyze.request","id":"summary","handle":"` + handle + `","payload":{"prompt":"Summarize the headlines"}}`
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+env.inA.ID+"/bridge?"+query.Encode(), body, bridgeHeaders())
	if rec.Code != http.StatusOK || len(env.starts) != 1 {
		t.Fatalf("analyze: %d %s", rec.Code, rec.Body.String())
	}
	got := env.starts[0].Agent
	if got.ID != agent.ID || got.ReasoningEffort != "high" || got.ServiceTier != "priority" {
		t.Fatalf("wrong composer settings: id=%s effort=%s tier=%s", got.ID, got.ReasoningEffort, got.ServiceTier)
	}
}
