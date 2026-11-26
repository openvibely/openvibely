package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

func TestTaskCardRefreshPreservesWorkspaceScope(t *testing.T) {
	h, e, _, db := setupTestHandlerWithStores(t)
	defer db.Close()
	ctx := context.Background()
	project := &models.Project{Name: "Card project", RepoPath: t.TempDir()}
	require.NoError(t, db.Projects.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Card task", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, db.Tasks.Create(ctx, task))
	for _, scope := range []string{models.AllProjectsID, "__repo_group", project.ID} {
		t.Run(scope, func(t *testing.T) {
			ctx := templateui.WithProjectNames(ctx, []models.Project{*project})
			ctx = layout.WithWorkspaceProject(ctx, scope)
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/merge?project_id="+project.ID, nil).WithContext(ctx), rec)
			require.NoError(t, h.renderTaskCardRefresh(c, task.ID))
			require.Equal(t, scope == models.AllProjectsID, strings.Contains(rec.Body.String(), "data-project-label"))
			require.Contains(t, rec.Body.String(), "project_id="+scope)
			stored, err := db.Tasks.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, project.ID, stored.ProjectID)
		})
	}
}
