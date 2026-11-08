package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginSettingsCardsAndDialogs(t *testing.T) {
	env := setupPluginPages(t)
	base := "/settings/plugin-pages?project_id=" + env.projA.ID
	rec := env.do(t, http.MethodGet, base, "", nil)
	require.Equal(t, 200, rec.Code)
	for _, marker := range []string{`aria-label="Add page"`, `data-card-list-toolbar="plugin-pages"`, `id="plugin-template-modal"`, `id="plugin-describe-modal"`, `id="plugin-install-modal"`, `id="plugin-delete-` + env.inA.ID, `id="plugin-details-` + env.inA.ID} {
		require.Contains(t, rec.Body.String(), marker)
	}
	rec = env.do(t, http.MethodGet, base+"&search=nonexistent", "", nil)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&search=sales", "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&enabled=false", "", nil)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&enabled=true&sort=name_desc", "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true"}
	project := createProject(t, env.h, "Templates")
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/install-example?project_id="+project.ID, "create_page=true", headers)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `data-plugin-instance-row=`))
	require.NotContains(t, rec.Body.String(), "alert-success")
	require.Contains(t, rec.Header().Get("HX-Trigger"), "Added Sales")
	require.Contains(t, rec.Header().Get("HX-Trigger"), "plugin-nav-refresh")
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/remove?project_id="+env.projA.ID, "", headers)
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, "/settings/plugin-pages?project_id="+env.projB.ID, "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)

}
