package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsReorderPreservesViewerProject(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	ctx := t.Context()
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(stores.ProjectAccess))
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "audit-reorder", "long-password-value", "")
	require.NoError(t, err)
	own := createProject(t, h, "Editable")
	other := createProject(t, h, "Viewer only")
	policy, err := stores.ProjectAccess.Get(ctx, other.ID)
	require.NoError(t, err)
	require.NoError(t, stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, policy.ProjectID, user.UserID, "viewer"))
	require.NoError(t, stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, own.ID, user.UserID, "editor"))
	a := &models.Task{ProjectID: own.ID, Title: "Editable", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, DisplayOrder: 20}
	b := &models.Task{ProjectID: other.ID, Title: "Protected", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, DisplayOrder: 50}
	require.NoError(t, stores.Tasks.Create(ctx, a))
	require.NoError(t, stores.Tasks.Create(ctx, b))
	before, err := stores.Tasks.GetByID(ctx, b.ID)
	require.NoError(t, err)
	req := httptest.NewRequest("PATCH", "/tasks/"+a.ID+"/reorder?project_id="+models.AllProjectsID, strings.NewReader("position=0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(models.WithPrincipal(ctx, user.Principal))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	after, err := stores.Tasks.GetByID(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, before.DisplayOrder, after.DisplayOrder, "viewer-only project must stay untouched")
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
}

func TestChannelsRejectsDeniedProjectMetadata(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "audit-channel-reader", "long-password-value", "")
	require.NoError(t, err)
	project := &models.Project{Name: "Private"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	hook := &models.WebhookEndpoint{ProjectID: project.ID, Name: "PRIVATE_WEBHOOK_MARKER", Enabled: true, DefaultPriority: 2}
	require.NoError(t, tc.stores.Webhooks.Create(ctx, hook))
	h.webhookRepo = tc.stores.Webhooks
	userCtx := models.WithPrincipal(ctx, user.Principal)
	require.ErrorIs(t, h.projectAccessSvc.Require(userCtx, project.ID, authz.View), authz.ErrDenied)
	for _, fragment := range []bool{false, true} {
		for _, projectID := range []string{project.ID, "nonexistent-project"} {
			req := httptest.NewRequest("GET", "/channels?project_id="+projectID, nil).WithContext(userCtx)
			if fragment {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)
			require.Equal(t, 404, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "PRIVATE_WEBHOOK_MARKER")
			require.NotContains(t, rec.Body.String(), hook.PathToken)
		}
	}
}
