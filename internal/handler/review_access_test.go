package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestReviewIDPermissionsAndProjectScope(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetReviewCommentRepo(tc.stores.ReviewComments)
	project := createProject(t, h, "Review access")
	other := createProject(t, h, "Foreign review")
	task := &models.Task{ProjectID: project.ID, Title: "Review", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, tc.stores.Tasks.Create(ctx, task))
	comment := &models.ReviewComment{TaskID: task.ID, FilePath: "file.go", LineNumber: 1, LineType: "new", CommentText: "original", ReviewedBy: "user"}
	require.NoError(t, tc.stores.ReviewComments.Create(ctx, comment))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "review-user", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	e.PATCH("/reviews/:id", h.UpdateReviewComment)
	e.DELETE("/reviews/:id", h.DeleteReviewComment)
	call := func(method, projectID string, p models.Principal) int {
		req := httptest.NewRequest(method, "/reviews/"+comment.ID+"?project_id="+projectID, strings.NewReader("comment_text=changed")).WithContext(models.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "viewer"))
	for _, method := range []string{"PATCH", "DELETE"} {
		require.Equal(t, 403, call(method, project.ID, user.Principal))
	}
	rows, err := tc.stores.ReviewComments.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "original", rows[0].CommentText)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "editor"))
	for _, method := range []string{"PATCH", "DELETE"} {
		require.Equal(t, 404, call(method, other.ID, user.Principal), "editable foreign project must not authorize comment")
	}
	require.Equal(t, 200, call("PATCH", project.ID, user.Principal))
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.Equal(t, 404, call("DELETE", project.ID, user.Principal))
	rows, err = tc.stores.ReviewComments.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "changed", rows[0].CommentText)
	require.Equal(t, 200, call("DELETE", project.ID, models.Principal{UserID: models.BootstrapUserID}))
	rows, err = tc.stores.ReviewComments.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}
