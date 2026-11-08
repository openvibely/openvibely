package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewScopedMutationAuditRollbackAndRevocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	project := &models.Project{Name: "Review audit"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Review", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	repo := NewReviewCommentRepo(db)
	comment := &models.ReviewComment{TaskID: task.ID, FilePath: "file.go", LineNumber: 1, LineType: "new", CommentText: "original", ReviewedBy: "user"}
	require.NoError(t, repo.Create(ctx, comment))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`CREATE TRIGGER fail_review_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.UpdateTextForProject(owner, project.ID, comment.ID, "changed"), "audit failure")
	require.ErrorContains(t, repo.DeleteForProject(owner, project.ID, comment.ID), "audit failure")
	require.ErrorContains(t, repo.DeleteByTask(owner, task.ID), "audit failure")
	attempted := &models.ReviewComment{TaskID: task.ID, FilePath: "other.go", LineNumber: 2, LineType: "new", CommentText: "not saved", ReviewedBy: "user"}
	require.ErrorContains(t, repo.Create(owner, attempted), "audit failure")
	require.Empty(t, attempted.ID, "rolled back insert must not expose a persisted identity")
	rows, err := repo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "original", rows[0].CommentText)
	_, err = db.Exec(`DROP TRIGGER fail_review_audit; INSERT INTO users(id,username) VALUES('reviewer','reviewer'); INSERT INTO org_memberships VALUES('default','reviewer','member')`)
	require.NoError(t, err)
	policyRepo := NewProjectAccessRepo(db)
	require.NoError(t, policyRepo.SetMember(ctx, models.BootstrapUserID, project.ID, "reviewer", "viewer"))
	editor := models.WithPrincipal(ctx, models.Principal{UserID: "reviewer", Role: "owner"})
	require.ErrorIs(t, repo.UpdateTextForProject(editor, project.ID, comment.ID, "denied"), persistence.ErrProjectDenied)
	require.NoError(t, policyRepo.SetMember(ctx, models.BootstrapUserID, project.ID, "reviewer", "editor"))
	require.NoError(t, repo.UpdateTextForProject(editor, project.ID, comment.ID, "changed"))
	events, err := policyRepo.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "review.comment.updated", events[0].Action)
	require.Equal(t, "reviewer", events[0].ActorUserID)
	require.NoError(t, policyRepo.SetMember(ctx, models.BootstrapUserID, project.ID, "reviewer", ""))
	require.ErrorIs(t, repo.DeleteForProject(editor, project.ID, comment.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.DeleteByTask(editor, task.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.Create(editor, attempted), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.DeleteForProject(owner, "foreign", comment.ID), persistence.ErrNotFound)
	require.NoError(t, repo.DeleteForProject(owner, project.ID, comment.ID))
	events, err = policyRepo.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "review.comment.deleted", events[0].Action)
	require.NoError(t, repo.Create(owner, attempted))
	events, err = policyRepo.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "review.comment.created", events[0].Action)
	require.Equal(t, attempted.ID, events[0].TargetID)
	require.NoError(t, repo.DeleteByTask(owner, task.ID))
	events, err = policyRepo.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "review.comments.cleared", events[0].Action)
	rows, err = repo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}
