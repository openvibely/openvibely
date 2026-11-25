package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

func TestRemotePluginPageOwnerIsolation(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, revoked, owner := fakeRemoteOV(t, "secret")
	mgr := pageplugins.NewManager(owner.remoteExecutor.Stores.PagePlugins, t.TempDir())
	owner.SetPagePlugins(mgr)
	t.Cleanup(mgr.StopAll)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	p := &models.Project{Name: "Project 1"}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(context.Background(), p))
	source := t.TempDir()
	entries, err := os.ReadDir("../pageplugins/testdata/sales")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("../pageplugins/testdata/sales", entry.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(source, entry.Name()), data, 0600))
	}
	large := bytes.Repeat([]byte("remote asset bytes\n"), 20000)
	require.NoError(t, os.WriteFile(filepath.Join(source, "large.txt"), large, 0600))
	pkg, err := mgr.InstallFromDir(ctx, source, "admin")
	require.NoError(t, err)
	in, err := mgr.CreateInstance(ctx, p.ID, pkg.ID, "")
	require.NoError(t, err)
	require.NoError(t, mgr.Enable(ctx, in.ID))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest("POST", "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	sid := savedServerID(t, h)
	require.Eventually(t, func() bool { return !h.remoteDown(ctx, sid) }, 5*time.Second, 20*time.Millisecond)
	project, instance := remoteid.ID(sid, p.ID), remoteid.ID(sid, in.ID)
	request := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("Content-Type", "application/json")
		r := httptest.NewRecorder()
		e.ServeHTTP(r, req)
		return r
	}
	rec = request("GET", "/plugin-pages/nav?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), instance)
	rec = request("GET", "/p/"+instance+"?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	match := regexp.MustCompile(`data-plugin-handle="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	require.Len(t, match, 2)
	handle := match[1]
	require.True(t, strings.HasPrefix(handle, remoteid.Prefix(sid)))
	rec = request("GET", "/plugin-assets/"+handle+"/index.html", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), handle)
	rec = request("GET", "/plugin-assets/"+handle+"/large.txt", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, large, rec.Body.Bytes())
	bridge := func(typ, payload string) *httptest.ResponseRecorder {
		body := `{"v":1,"type":"` + typ + `","id":"m1","handle":"` + handle + `","payload":` + payload + `}`
		return request("POST", "/api/plugin-pages/"+instance+"/bridge?project_id="+project, body)
	}
	rec = bridge("ready", `{}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	rec = bridge("context.set", `{"summary":"Remote page context"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	rec = bridge("op.invoke", `{"operation":"get_report","input":{}}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var result pluginBridgeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.True(t, result.OK)

	// The owner supplies the page settings; no Local manager is installed.
	rec = request("GET", "/settings/plugin-pages?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), instance)
	rec = request("GET", "/breadcrumb-selectors/custom-pages?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), instance)
	rec = request("GET", "/p/"+instance+"?project_id="+models.AllProjectsID, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	// A hard refresh keeps the rail's workspace while the page bridge uses its owner.
	full := httptest.NewRequest("GET", "/p/"+instance+"?project_id="+models.AllProjectsID, nil).WithContext(ctx)
	full.Header.Set("Accept", "text/html")
	fullResponse := httptest.NewRecorder()
	e.ServeHTTP(fullResponse, full)
	require.Equal(t, http.StatusOK, fullResponse.Code, fullResponse.Body.String())
	require.Contains(t, fullResponse.Body.String(), `data-plugin-project="`+models.AllProjectsID+`"`)
	require.Contains(t, fullResponse.Body.String(), `data-project-id="`+project+`"`)

	// Live page events use the remote socket, preserving application data verbatim.
	desktop := httptest.NewServer(e)
	defer desktop.Close()
	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, "GET", desktop.URL+"/api/plugin-pages/"+instance+"/events?project_id="+project+"&handle="+url.QueryEscape(handle), nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	require.Eventually(t, func() bool { return mgr.Hub.SubscriberCount(in.ID) > 0 }, time.Second, 10*time.Millisecond)
	mgr.Hub.Publish(in.ID, pageintegration.Event{Type: "data.changed", Data: json.RawMessage(`{"checksum":"0123456789abcdef0123456789abcdef"}`)})
	reader := bufio.NewReader(response.Body)
	found := false
	for i := 0; i < 8; i++ {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		if strings.HasPrefix(line, "data: ") {
			require.Contains(t, line, `"checksum":"0123456789abcdef0123456789abcdef"`)
			found = true
			break
		}
	}
	require.True(t, found)
	response.Body.Close()
	cancel()

	// Inline analysis creates the conversation and execution on the owner.
	createAgent(t, owner.llmConfigRepo)
	starts := make(chan streamingResponseParams, 1)
	owner.startStreamingResponseOverride = func(p streamingResponseParams) error { starts <- p; return nil }
	rec = bridge("analyze.request", `{"prompt":"Explain this report"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var analyzed struct {
		Result struct {
			ExecID string `json:"exec_id"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &analyzed))
	require.True(t, strings.HasPrefix(analyzed.Result.ExecID, remoteid.Prefix(sid)))
	var started streamingResponseParams
	select {
	case started = <-starts:
	case <-time.After(time.Second):
		t.Fatal("owner did not start analysis")
	}
	require.Equal(t, p.ID, started.ProjectID)
	localTask, err := h.taskRepo.GetByID(context.Background(), started.TaskID)
	require.NoError(t, err)
	require.Nil(t, localTask, "remote chat must not create a Local task")
	rec = request("GET", "/api/plugin-pages/"+instance+"/chat/thread?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), analyzed.Result.ExecID)
	require.NoError(t, owner.execRepo.Complete(context.Background(), started.ExecID, models.ExecCompleted, "Remote answer", "", 0, 0))
	require.NoError(t, owner.taskRepo.UpdateStatus(context.Background(), started.TaskID, models.StatusCompleted))
	// Folder uploads are transferred, installed and registered only by the owner.
	work := t.TempDir()
	entries, err = os.ReadDir(source)
	require.NoError(t, err)
	var files []pluginUploadFile
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		require.NoError(t, err)
		p := filepath.Join(work, entry.Name())
		require.NoError(t, os.WriteFile(p, data, 0600))
		files = append(files, pluginUploadFile{path: p, size: int64(len(data))})
		paths = append(paths, "package/"+entry.Name())
	}
	uploaded, err := h.uploadRemotePlugin(ctx, project, files, paths, false)
	require.NoError(t, err)
	require.Equal(t, remoteid.ID(sid, pkg.ID), uploaded.ID)
	// Owner-side disable/enable works without Local storage.
	for _, action := range []string{"disable", "enable"} {
		rec = request("POST", "/settings/plugin-pages/instances/"+instance+"/"+action+"?project_id="+project, "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		stored, err := mgr.Store().GetInstance(ctx, in.ID)
		require.NoError(t, err)
		require.Equal(t, action == "enable", stored.Enabled)
	}
	rec = request("POST", "/api/plugin-pages/"+instance+"/handle/retire?project_id="+project+"&handle="+url.QueryEscape(handle), "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = request("GET", "/plugin-assets/"+handle+"/index.html", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	revoked.Store(true)
	h.remoteHub().Shutdown()
	rec = request("GET", "/p/"+instance+"?project_id="+project, "")
	require.NotEqual(t, 200, rec.Code, "disconnection must not fall back to Local")
}

func TestRemotePluginActionsEnforceOwnerPermissions(t *testing.T) {
	env, member := setupPageAccess(t)
	ctx := models.WithPrincipal(t.Context(), member)
	ref := PluginRefArgs{ProjectID: env.projA.ID, InstanceID: env.inA.ID}
	_, res := runAction[pluginOpenResult](t, env.h, ctx, "page.open", ref)
	require.NotEmpty(t, res.Error)
	require.NoError(t, env.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, env.projA.ID, member.UserID, "viewer"))
	out, res := runAction[pluginOpenResult](t, env.h, ctx, "page.open", ref)
	require.Empty(t, res.Error)
	args := PluginHandleArgs{PluginRefArgs: ref, Handle: out.View.Handle}
	_, res = runAction[pluginAssetChunk](t, env.h, ctx, "page.asset", pluginAssetArgs{PluginHandleArgs: args, Path: "index.html"})
	require.Empty(t, res.Error)
	_, res = runAction[pluginBridgeActionResult](t, env.h, ctx, "page.bridge", pluginBridgeArgs{PluginHandleArgs: args, Type: "op.invoke", Payload: json.RawMessage(`{"operation":"set_target","input":{"target":5}}`)})
	require.NotEmpty(t, res.Error)
	_, res = runAction[pluginChatResult](t, env.h, ctx, "page.chat", pluginChatArgs{PluginRefArgs: ref, Message: "change the page"})
	require.NotEmpty(t, res.Error)
	_, res = runAction[bool](t, env.h, ctx, "page.manage", pluginSettingsArgs{ProjectID: ref.ProjectID, InstanceID: ref.InstanceID, Action: "remove"})
	require.NotEmpty(t, res.Error)
	// A valid task must not bypass the page capability check on the live socket.
	require.False(t, env.h.liveSubscriptionAllowed(ctx, liveCommand{Op: "page", ProjectID: ref.ProjectID, InstanceID: ref.InstanceID, TaskID: out.View.ChatTask.ID, Handle: "invalid"}))
	require.NoError(t, env.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, env.projA.ID, member.UserID, ""))
	_, res = runAction[pluginAssetChunk](t, env.h, ctx, "page.asset", pluginAssetArgs{PluginHandleArgs: args, Path: "index.html"})
	require.NotEmpty(t, res.Error)
	require.False(t, env.h.liveSubscriptionAllowed(ctx, liveCommand{Op: "page", ProjectID: ref.ProjectID, InstanceID: ref.InstanceID, Handle: args.Handle}))
}
