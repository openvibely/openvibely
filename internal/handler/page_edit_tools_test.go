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
	unpublished, err := env.mgr.Store().GetInstance(t.Context(), in.ID)
	require.NoError(t, err)
	require.Equal(t, in.ContentHash, unpublished.ContentHash)
	resolved, err := env.mgr.Resolve(t.Context(), in.ID, in.ProjectID)
	require.NoError(t, err)
	assetPath, err := env.mgr.AssetPath(resolved, "index.html")
	require.NoError(t, err)
	workFile, err := os.Stat(filepath.Join(params.WorkDir, "index.html"))
	require.NoError(t, err)
	servedFile, err := os.Stat(assetPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(workFile, servedFile), "runtime and tools must use the same file")
	run("publish_custom_page", `{}`, false)
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
	// Keep the same live tool runtime across a task publication. It must read
	// and edit the new sources without capturing an obsolete editing revision.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema_version":1,"id":"test.edit-chat","name":"Edit chat","version":"2.0.0","pages":[{"id":"home","title":"Home","entry":"index.html"}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Task update</h1>"), 0600))
	_, err = env.mgr.PublishToInstance(t.Context(), dir, env.projA.ID, "", "admin", in.ID)
	require.NoError(t, err)
	require.Contains(t, run("read_file", `{"file_path":"index.html"}`, false), "Task update")
	run("edit_file", `{"file_path":"index.html","old_string":"Task update","new_string":"Chat follow-up"}`, false)
	run("write_file", `{"file_path":"plugin.json","content":"invalid"}`, false)
	run("publish_custom_page", `{}`, true)
	resumed, workDir, err := env.h.pageEditTools(t.Context(), *params.PageConversation, params.PrincipalID)
	require.NoError(t, err)
	require.Equal(t, params.WorkDir, workDir)
	out, _, bad, err := resumed.Executor(t.Context(), "read_file", json.RawMessage(`{"file_path":"plugin.json"}`))
	require.NoError(t, err)
	require.False(t, bad)
	require.Contains(t, out, "invalid", "failed edits must remain available on the next reply")
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

func TestPageFailedBuildKeepsFilesAndPublishedVersion(t *testing.T) {
	env := setupPluginPages(t)
	sendPageTest(t, env, env.inA, "change page")
	p := env.starts[0]
	before, err := env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	_, handled, bad, err := p.RuntimeTools.Executor(t.Context(), "shell", json.RawMessage(`{"command":"printf changed > index.html; exit 1"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	after, err := env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.Equal(t, before.ContentHash, after.ContentHash)
	contents, err := os.ReadFile(filepath.Join(p.WorkDir, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "changed", string(contents))
	// Successful commands do not publish; only the explicit tool does.
	_, _, bad, err = p.RuntimeTools.Executor(t.Context(), "shell", json.RawMessage(`{"command":"true"}`))
	require.NoError(t, err)
	require.False(t, bad)
	after, err = env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.Equal(t, before.ContentHash, after.ContentHash)
	_, _, bad, err = p.RuntimeTools.Executor(t.Context(), "publish_custom_page", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, bad)
	after, err = env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.NotEqual(t, before.ContentHash, after.ContentHash)
}
