package handler

import (
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProjectModalComputerPreference(t *testing.T) {
	tc := NewStoreTestContext(t)
	tc.handler.computerSvc = service.NewProjectComputerService(tc.handler.projectRepo, liveComputerFixture{})
	form := url.Values{"name": {"Computer project"}, "repo_source": {"local"}, "repo_path": {""}, "computer_setting_present": {"1"}}
	rec := tc.HTTP().Post("/projects").WithForm(form).Execute()
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/tasks?project_id=")
	state, err := tc.handler.computerSvc.Get(t.Context(), id)
	require.NoError(t, err)
	require.False(t, state.Enabled)
	rec = tc.HTTP().Get("/projects/" + id + "/edit").Execute()
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `name="computer_enabled"`)
	require.Less(t, strings.Index(rec.Body.String(), `name="max_workers"`), strings.Index(rec.Body.String(), `name="computer_enabled"`))
	require.NotContains(t, rec.Body.String(), `Computer settings`)
	form.Set("computer_enabled", "on")
	rec = tc.HTTP().Put("/projects/" + id).WithForm(form).Execute()
	require.Less(t, rec.Code, 400, rec.Body.String())
	state, err = tc.handler.computerSvc.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, state.Enabled)
	rec = tc.HTTP().Get("/projects/new").Execute()
	require.Contains(t, rec.Body.String(), `name="computer_enabled"`)
	require.Less(t, strings.Index(rec.Body.String(), `name="max_workers"`), strings.Index(rec.Body.String(), `name="computer_enabled"`))
}
