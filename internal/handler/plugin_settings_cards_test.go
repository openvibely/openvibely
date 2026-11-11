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
	for _, marker := range []string{`aria-label="Add page"`, `data-card-list-toolbar="plugin-pages"`, `id="plugin-describe-modal"`, `id="plugin-install-modal"`, `id="plugin-delete-` + env.inA.ID} {
		require.Contains(t, rec.Body.String(), marker)
	}
	// Describe must use XHR: native WebKit asset transport cannot follow
	// the redirect produced by a full-document task form submission.
	require.Contains(t, rec.Body.String(), `hx-post="/tasks?from=new&amp;project_id=`+env.projA.ID+`" hx-target="#main-content"`)
	require.Contains(t, rec.Body.String(), `hx-on:htmx:config-request="event.detail.parameters.prompt=`)
	require.NotContains(t, rec.Body.String(), `<form method="post" action="/tasks`)
	rec = env.do(t, http.MethodGet, base+"&search=nonexistent", "", nil)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&search=sales", "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&enabled=false", "", nil)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	rec = env.do(t, http.MethodGet, base+"&enabled=true&sort=name_desc", "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true"}
	project := createProject(t, env.h, "Package destination")
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances?project_id="+project.ID, "package_id="+env.inA.PackageID+"&page_id="+env.inA.PageID, headers)
	require.Equal(t, 200, rec.Code)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `data-plugin-instance-row=`))
	require.NotContains(t, rec.Body.String(), "alert-success")
	require.NotContains(t, rec.Body.String(), `id="plugin-template-modal"`)
	require.Contains(t, rec.Header().Get("HX-Trigger"), "plugin-nav-refresh")
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/remove?project_id="+env.projA.ID, "", headers)
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), `data-plugin-instance-row=`)
	require.Contains(t, rec.Body.String(), `data-plugin-package=`)
	require.Contains(t, rec.Body.String(), "Available to add")
	rec = env.do(t, http.MethodGet, "/settings/plugin-pages?project_id="+env.projB.ID, "", nil)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row=`)

}

func TestPluginSettingsAllProjects(t *testing.T) {
	env := setupPluginPages(t)
	scope := "?project_id=__all_projects__"
	rec := env.do(t, http.MethodGet, "/settings/plugin-pages"+scope, "", nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `name="destination_project"`)
	require.NotContains(t, rec.Body.String(), "this.action=")
	require.Contains(t, rec.Body.String(), `/tasks?from=new&amp;project_id=__all_projects__`)
	require.Contains(t, rec.Body.String(), env.projA.Name)
	require.Contains(t, rec.Body.String(), env.projB.Name)
	require.Contains(t, rec.Body.String(), `data-plugin-instance-row="`+env.inA.ID+`"`)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true"}
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances/"+env.inA.ID+"/disable"+scope, "", headers)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "project_id=__all_projects__")
	in, err := env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.False(t, in.Enabled)
	project := createProject(t, env.h, "All projects destination")
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances"+scope, "package_id="+env.inA.PackageID+"&page_id="+env.inA.PageID+"&destination_project="+project.ID, headers)
	require.Equal(t, 200, rec.Code)
	instances, err := env.mgr.Store().ListInstances(t.Context(), project.ID, false)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Contains(t, rec.Body.String(), "project_id=__all_projects__")
}

func TestPluginSettingsAllProjectsReusePage(t *testing.T) {
	env := setupPluginPages(t)
	project := createProject(t, env.h, "Reuse destination")
	scope := "?project_id=__all_projects__"
	rec := env.do(t, http.MethodGet, "/settings/plugin-pages"+scope, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Add to another project")
	require.Contains(t, rec.Body.String(), `id="plugin-add-project-`+env.inA.ID+`"`)
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "HX-Request": "true"}
	rec = env.do(t, http.MethodPost, "/settings/plugin-pages/instances"+scope, "package_id="+env.inA.PackageID+"&page_id="+env.inA.PageID+"&destination_project="+project.ID, headers)
	require.Equal(t, http.StatusOK, rec.Code)
	instances, err := env.mgr.Store().ListInstances(t.Context(), project.ID, false)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.NotEqual(t, env.inA.ID, instances[0].ID)
	require.Equal(t, env.inA.PackageID, instances[0].PackageID)
	require.Contains(t, rec.Body.String(), "project_id=__all_projects__")
	original, err := env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.True(t, original.Enabled)
}
