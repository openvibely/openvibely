package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestXChannelRepositoriesProjectIsolationAndReceiptLease(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := NewProjectRepo(db)
	p1 := &models.Project{Name: "X One"}
	p2 := &models.Project{Name: "X Two"}
	require.NoError(t, projects.Create(ctx, p1))
	require.NoError(t, projects.Create(ctx, p2))
	auth := NewXAuthRepo(db)
	u := &models.XAuthorizedUser{ProjectID: p1.ID, XUserID: "123", Username: "alice"}
	require.NoError(t, auth.Create(ctx, u))
	ok, err := auth.IsAuthorized(ctx, p1.ID, "123")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = auth.IsAuthorized(ctx, p2.ID, "123")
	require.NoError(t, err)
	require.False(t, ok)
	require.Error(t, auth.Delete(ctx, p2.ID, u.ID))
	ok, _ = auth.IsAuthorized(ctx, p1.ID, "123")
	require.True(t, ok)
	selection := NewXUserProjectRepo(db)
	require.NoError(t, selection.SetUserProject(ctx, "123", p2.ID))
	got, err := selection.GetUserProject(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, p2.ID, got)
	receipts := NewXInboundReceiptRepo(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := receipts.Claim(ctx, "tweet-1", p1.ID, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptClaimed, claim.Result)
	firstToken := claim.Token
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptActive, claim.Result)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptClaimed, claim.Result)
	require.NotEqual(t, firstToken, claim.Token)
	require.ErrorIs(t, receipts.Release(ctx, "tweet-1", firstToken), nil)
	active, err := receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(2*time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptActive, active.Result, "stale release must not remove a newer lease")
	_, err = receipts.CompleteWithHandoff(ctx, "tweet-1", claim.Token, nil, nil)
	require.NoError(t, err)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(4*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptCompleted, claim.Result)
}

func TestXReceiptCompleteWithHandoffRollsBackWorkAndKeepsLeaseRetryable(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "X Atomic Handoff"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	receipts := NewXInboundReceiptRepo(db)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := receipts.Claim(ctx, "tweet-atomic", project.ID, now, time.Minute)
	require.NoError(t, err)

	_, err = receipts.CompleteWithHandoff(ctx, "tweet-atomic", claim.Token, nil, func(exec SQLExecutor) error {
		if _, err := exec.ExecContext(ctx, `INSERT INTO x_user_projects(x_user_id, project_id) VALUES('atomic-user', ?)`, project.ID); err != nil {
			return err
		}
		return fmt.Errorf("forced handoff failure")
	})
	require.ErrorContains(t, err, "forced handoff failure")
	selected, err := NewXUserProjectRepo(db).GetUserProject(ctx, "atomic-user")
	require.NoError(t, err)
	require.Empty(t, selected, "durable work must roll back when receipt completion fails")
	active, err := receipts.Claim(ctx, "tweet-atomic", project.ID, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptActive, active.Result)
}

func TestThreadInputRepoPreservesXReplyMetadataAndPromotesContextAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := NewProjectRepo(db)
	p := &models.Project{Name: "X Project"}
	require.NoError(t, projects.Create(ctx, p))
	inputs := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: p.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "hello", Source: models.TaskOriginX, XAccountID: "bot-account", XConversationID: "conv", XReplyToTweetID: "tweet", XUserID: "123", XUsername: "alice"}
	require.NoError(t, inputs.CreateQueued(ctx, input))
	loaded, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, "tweet", loaded.XReplyToTweetID)
	require.Equal(t, "bot-account", loaded.XAccountID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	task := &models.Task{ProjectID: p.ID, Title: "queued X", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning, Prompt: "hello", CreatedVia: models.TaskOriginX}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "hello"}
	require.NoError(t, inputs.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, nil, nil, nil))
	meta, err := NewXTaskContextRepo(db).GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, "tweet", meta.ReplyToTweetID)
	require.Equal(t, "bot-account", meta.AccountID)
	require.Equal(t, p.ID, meta.ProjectID)
}

func TestXAuthRepoFirstAuthorizedProjectUsesUserKeyedIndexAndOrdering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := NewProjectRepo(db)

	defaultProject := &models.Project{Name: "Zulu"}
	nameOrderedProject := &models.Project{Name: "Alpha"}
	otherProject := &models.Project{Name: "Bravo"}
	for _, project := range []*models.Project{defaultProject, nameOrderedProject, otherProject} {
		require.NoError(t, projects.Create(ctx, project))
	}
	_, err := db.ExecContext(ctx, `UPDATE projects SET is_default = 1 WHERE id = ?`, defaultProject.ID)
	require.NoError(t, err)

	auth := NewXAuthRepo(db)
	for _, project := range []*models.Project{defaultProject, nameOrderedProject, otherProject} {
		require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	}

	plan := explainXAuthorizedProjectQueryPlan(t, db, xAuthorizedProjectQuery, "author")
	require.Contains(t, plan, "idx_x_authorized_users_user_project")
	require.NotContains(t, plan, "SCAN projects")

	projectID, err := auth.FirstAuthorizedProject(ctx, "author")
	require.NoError(t, err)
	require.Equal(t, defaultProject.ID, projectID)
}

func explainXAuthorizedProjectQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(details, "\n")
}
