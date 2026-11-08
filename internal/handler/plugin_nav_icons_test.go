package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPluginNavCustomIconAndFallback(t *testing.T) {
	env := setupPluginPages(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema_version":1,"id":"test.icons","name":"Icons","version":"1.0.0","pages":[{"id":"reports","title":"Reports","entry":"index.html","icon":"📊"},{"id":"plain","title":"Plain","entry":"index.html"}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0600))
	pkg, err := env.mgr.InstallFromDir(t.Context(), dir, "admin")
	require.NoError(t, err)
	in, err := env.mgr.CreateInstance(t.Context(), env.projA.ID, pkg.ID, "reports")
	require.NoError(t, err)
	require.NoError(t, env.mgr.Enable(t.Context(), in.ID))
	plain, err := env.mgr.CreateInstance(t.Context(), env.projA.ID, pkg.ID, "plain")
	require.NoError(t, err)
	require.NoError(t, env.mgr.Enable(t.Context(), plain.ID))
	response := env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+env.projA.ID, "", nil)
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "📊")
	require.Contains(t, response.Body.String(), `data-tip="Reports"`)
	require.Contains(t, response.Body.String(), `data-tip="Sales"`)
	require.Contains(t, response.Body.String(), `d="M4 5h16M4 12h16M4 19h10"`)
	response = env.do(t, http.MethodGet, "/plugin-pages/nav?project_id="+models.AllProjectsID, "", nil)
	require.Contains(t, response.Body.String(), `data-tip="Reports · Alpha"`)
}
