package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

func customPageTask(t *testing.T, env *pluginTestEnv) (*models.Task, string) {
	t.Helper()
	dir := t.TempDir()
	task := &models.Task{ProjectID: env.projA.ID, Title: "Create custom page", Prompt: "Create a sales page", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := env.h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := env.h.taskRepo.UpdateWorktreeInfo(context.Background(), task.ID, dir, "task/test"); err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = dir
	return task, dir
}
func customPageBundle(t *testing.T, root string, native bool) {
	t.Helper()
	dir := filepath.Join(root, "custom-pages", "report")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	backend := ""
	if native {
		backend = `,"permissions":["backend.exec"],"backend":{"executables":{"any":"backend"}}`
	}
	manifest := `{"schema_version":1,"id":"custom.report","name":"Report","version":"1.0.0","pages":[{"id":"report","title":"My report","entry":"index.html"}]` + backend + `}`
	for name, content := range map[string]string{"plugin.json": manifest, "index.html": "<!doctype html><h1>My report</h1>", "backend": "not an executable: must never be launched"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCustomPagePublishFromInitialTaskAndFollowup(t *testing.T) {
	env := setupPluginPages(t)
	task, root := customPageTask(t, env)
	customPageBundle(t, root, false)
	ctx := context.Background()
	env.h.broadcaster = events.NewBroadcaster()
	sub, err := env.h.broadcaster.SubscribeProject(task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer env.h.broadcaster.Unsubscribe(sub)
	rt := env.h.initialTaskRuntimeTools(ctx, *task, "exec", map[string]bool{"publish_custom_page": true, "get_custom_page_guide": true})
	guide, handled, bad, err := rt.Executor(ctx, "get_custom_page_guide", json.RawMessage(`{}`))
	if err != nil || bad || !handled || !strings.Contains(guide, "publish_custom_page") {
		t.Fatalf("guide unavailable: %s %v", guide, err)
	}
	input := json.RawMessage(`{"package_path":"custom-pages/report"}`)
	raw, handled, bad, err := rt.Executor(ctx, "publish_custom_page", input)
	if err != nil || bad || !handled {
		t.Fatalf("publish: %s %v", raw, err)
	}
	var first struct {
		InstanceID string `json:"instance_id"`
		Enabled    bool   `json:"enabled"`
		URL        string `json:"url"`
	}
	if err = json.Unmarshal([]byte(raw), &first); err != nil {
		t.Fatal(err)
	}
	if !first.Enabled || first.InstanceID == "" {
		t.Fatalf("not enabled: %s", raw)
	}
	select {
	case ev := <-sub:
		if ev.Type != events.PluginPagesChanged {
			t.Fatalf("wrong event: %v", ev)
		}
	default:
		t.Fatal("missing navigation event")
	}
	params := streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID, IsTaskFollowup: true}
	followup := env.h.buildChatActionToolRuntime(params, nil)
	retry, handled, bad, err := followup.Executor(ctx, "publish_custom_page", input)
	if err != nil || bad || !handled || !strings.Contains(retry, first.InstanceID) {
		t.Fatalf("retry failed/duplicated: %s %v", retry, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodGet, first.URL, "", map[string]string{"HX-Request": "true"})
	if rec.Code != 200 {
		t.Fatalf("page did not survive worktree deletion: %d", rec.Code)
	}
	handle := handlePattern.FindStringSubmatch(rec.Body.String())[1]
	asset := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/index.html", "", nil)
	if asset.Code != 200 || !strings.Contains(asset.Body.String(), "My report") {
		t.Fatal("installed asset missing")
	}
	nav := env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+env.projA.ID, "", nil)
	if !strings.Contains(nav.Body.String(), first.InstanceID) {
		t.Fatal("published page missing in navigation")
	}
}
func TestCustomPagePublishBoundaries(t *testing.T) {
	env := setupPluginPages(t)
	task, root := customPageTask(t, env)
	customPageBundle(t, root, false)
	params := streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID, IsTaskFollowup: true}
	for _, input := range []string{`{"package_path":"../escape"}`, `{"package_path":"/tmp"}`, `{"package_path":"custom-pages/report","project_id":"other"}`} {
		if _, err := env.h.publishCustomPageTool(context.Background(), params, json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	outside := t.TempDir()
	customPageBundle(t, outside, false)
	if err := os.Symlink(filepath.Join(outside, "custom-pages", "report"), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := env.h.publishCustomPageTool(context.Background(), params, json.RawMessage(`{"package_path":"outside"}`)); err == nil {
		t.Fatal("accepted symlink escape")
	}
	params.ProjectID = env.projB.ID
	if _, err := env.h.publishCustomPageTool(context.Background(), params, json.RawMessage(`{"package_path":"custom-pages/report"}`)); err == nil {
		t.Fatal("accepted foreign task")
	}
	params.ProjectID = task.ProjectID
	params.IsTaskFollowup = false
	if _, err := env.h.publishCustomPageTool(context.Background(), params, json.RawMessage(`{"package_path":"custom-pages/report"}`)); err == nil {
		t.Fatal("accepted project chat")
	}
	if chatcontrol.IsAllowed("publish_custom_page", models.ChatModePlan, chatcontrol.SurfaceWeb) == nil {
		t.Fatal("publication permitted in plan mode")
	}
	if chatcontrol.TaskToolNames("automation:test:auditor")["publish_custom_page"] {
		t.Fatal("auditor can publish")
	}
}
func TestCustomPageNativePublicationNeverExecutes(t *testing.T) {
	env := setupPluginPages(t)
	task, root := customPageTask(t, env)
	customPageBundle(t, root, true)
	raw, err := env.h.publishCustomPageTool(context.Background(), streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID, IsTaskFollowup: true}, json.RawMessage(`{"package_path":"custom-pages/report"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		InstanceID       string `json:"instance_id"`
		ApprovalRequired bool   `json:"approval_required"`
		SettingsURL      string `json:"settings_url"`
		Enabled          bool   `json:"enabled"`
	}
	if err = json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.ApprovalRequired || result.SettingsURL == "" || result.Enabled {
		t.Fatalf("bad approval result: %s", raw)
	}
	instance, err := env.mgr.Store().GetInstance(context.Background(), result.InstanceID)
	if err != nil || instance.ApprovedHash != "" || instance.Enabled {
		t.Fatalf("native backend authorized: %+v %v", instance, err)
	}
	if status, _ := env.mgr.ProcessStatus(instance.ID); status != "none" {
		t.Fatalf("native backend started: %s", status)
	}
}

func TestCustomPageStartupStatusTool(t *testing.T) {
	env := setupPluginPages(t)
	task, _ := customPageTask(t, env)
	rt := env.h.initialTaskRuntimeTools(t.Context(), *task, "exec", map[string]bool{"get_custom_page_status": true})
	instances, err := env.h.pagePlugins.ListEnabled(t.Context(), task.ProjectID)
	if err != nil || len(instances) == 0 {
		t.Fatal(err)
	}
	id := instances[0].ID
	call := func(id string) (string, bool) {
		t.Helper()
		raw, handled, bad, err := rt.Executor(t.Context(), "get_custom_page_status", json.RawMessage(`{"instance_id":"`+id+`"}`))
		if !handled || err != nil {
			t.Fatalf("status unavailable: %s %v", raw, err)
		}
		return raw, bad
	}
	raw, bad := call(id)
	if bad || !strings.Contains(raw, "not_opened") {
		t.Fatal(raw)
	}
	r, err := env.h.pagePlugins.Resolve(t.Context(), id, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	hd, err := env.h.pagePlugins.IssueHandle(r, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err = env.h.pagePlugins.ReportStartup(hd.Token, "loaded", ""); err != nil {
		t.Fatal(err)
	}
	raw, bad = call(id)
	if bad || !strings.Contains(raw, `"state":"loaded"`) {
		t.Fatal(raw)
	}
	other, err := env.h.pagePlugins.ListEnabled(t.Context(), env.projB.ID)
	if err != nil || len(other) == 0 {
		t.Fatal(err)
	}
	_, _, bad, err = rt.Executor(t.Context(), "get_custom_page_status", json.RawMessage(`{"instance_id":"`+other[0].ID+`"}`))
	if !bad && err == nil {
		t.Fatal("cross-project status exposed")
	}
}
