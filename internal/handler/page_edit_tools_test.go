package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPageChatEditsLivePageWithoutNewTask(t *testing.T) {
	env := setupPluginPages(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema_version":1,"id":"test.edit-chat","name":"Edit chat","version":"1.0.0","pages":[{"id":"home","title":"Home","entry":"index.html"}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Before</h1>"), 0600))
	result, err := env.mgr.PublishToInstance(t.Context(), dir, env.projA.ID, "", "admin", "")
	require.NoError(t, err)
	in := result.Instance
	sendPageTest(t, env, in, "change the heading")
	require.Len(t, env.starts, 1)
	params := env.starts[0]
	defer params.RuntimeCleanup()
	run := func(name, input string, wantError bool) string {
		t.Helper()
		out, handled, bad, err := params.RuntimeTools.Executor(t.Context(), name, json.RawMessage(input))
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, wantError, bad, out)
		return out
	}
	require.Contains(t, run("read_file", `{"file_path":"index.html"}`, false), "Before")
	run("write_file", `{"file_path":"../escape","content":"no"}`, true)
	run("edit_file", `{"file_path":"index.html","old_string":"Before","new_string":"After"}`, false)
	run("apply_page_changes", `{}`, false)
	require.NoError(t, params.CancellationContext.Err(), "publishing must not cancel its own chat")
	updated, err := env.mgr.Store().GetInstance(t.Context(), in.ID)
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	pkg, err := env.mgr.Store().GetPackage(t.Context(), updated.PackageID)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(pkg.InstallPath, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "<h1>After</h1>", string(content))
	run("edit_file", `{"file_path":"index.html","old_string":"After","new_string":"Again"}`, false)
	run("apply_page_changes", `{}`, false)
	run("write_file", `{"file_path":"plugin.json","content":"invalid"}`, false)
	run("apply_page_changes", `{}`, true)
	current, err := env.mgr.Store().GetInstance(t.Context(), in.ID)
	require.NoError(t, err)
	require.True(t, current.Enabled)
	chatID, err := env.mgr.Store().GetConversationTask(t.Context(), in.ID)
	require.NoError(t, err)
	require.Equal(t, params.TaskID, chatID)
	require.NoError(t, env.mgr.Disable(t.Context(), in.ID))
	run("read_file", `{"file_path":"index.html"}`, true)
	rec := env.do(t, http.MethodGet, "/p/"+in.ID+"?project_id="+in.ProjectID, "", nil)
	require.NotEqual(t, 200, rec.Code)
}
