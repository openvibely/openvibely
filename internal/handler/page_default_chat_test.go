package handler

import (
	"github.com/stretchr/testify/require"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginChatAndToolsWithoutOptIn(t *testing.T) {
	env := setupPluginPages(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema_version":1,"id":"test.default-chat","name":"Default chat","version":"1.0.0","pages":[{"id":"home","title":"Home","entry":"index.html"}],"backend":{"builtin":"sales-demo"},"permissions":["storage","events"],"operations":[{"name":"get_report","description":"Read items","kind":"read","expose_as_tool":false},{"name":"set_target","description":"Update items","kind":"action"}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0600))
	pkg, err := env.mgr.InstallFromDir(t.Context(), dir, "admin")
	require.NoError(t, err)
	in, err := env.mgr.CreateInstance(t.Context(), env.projA.ID, pkg.ID, "home")
	require.NoError(t, err)
	require.NoError(t, env.mgr.Enable(t.Context(), in.ID))
	handle := env.openPage(t, in)
	rec := env.do(t, http.MethodGet, "/p/"+in.ID+"?project_id="+in.ProjectID, "", nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `id="page-chat-toggle"`)
	require.Contains(t, rec.Body.String(), `id="page-side-chat"`)
	sendPageTest(t, env, in, "hello")
	require.Len(t, env.starts, 1)
	require.NotNil(t, env.starts[0].RuntimeTools)
	require.Len(t, env.starts[0].RuntimeTools.Definitions, 2)
	require.Contains(t, env.starts[0].SystemContext, "page_get_report")
	for _, suffix := range []string{"/chat/thread", "/chat/thread/composer-action"} {
		rec = env.do(t, http.MethodGet, pluginAPI(in.ID, in.ProjectID, suffix), "", nil)
		require.Equal(t, 200, rec.Code)
	}
	rec = env.do(t, http.MethodPost, pluginAPI(in.ID, in.ProjectID, "/chat/stop"), "", map[string]string{"HX-Request": "true"})
	require.Equal(t, 200, rec.Code)
	rec, _ = env.bridge(t, in, in.ProjectID, handle, "analyze.request", `{"prompt":"summarize"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
}
