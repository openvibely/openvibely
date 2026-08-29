package repository

import (
	"context"
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
	require.Equal(t, XReceiptClaimed, claim)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptActive, claim)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptClaimed, claim)
	require.NoError(t, receipts.Complete(ctx, "tweet-1", ""))
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(4*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, XReceiptCompleted, claim)
}

func TestThreadInputRepoPreservesXReplyMetadataAndPromotesContextAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := NewProjectRepo(db)
	p := &models.Project{Name: "X Project"}
	require.NoError(t, projects.Create(ctx, p))
	inputs := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: p.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "hello", Source: models.TaskOriginX, XConversationID: "conv", XReplyToTweetID: "tweet", XUserID: "123", XUsername: "alice"}
	require.NoError(t, inputs.CreateQueued(ctx, input))
	loaded, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, "tweet", loaded.XReplyToTweetID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	task := &models.Task{ProjectID: p.ID, Title: "queued X", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning, Prompt: "hello", CreatedVia: models.TaskOriginX}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "hello"}
	require.NoError(t, inputs.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, nil, nil, nil))
	meta, err := NewXTaskContextRepo(db).GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, "tweet", meta.ReplyToTweetID)
	require.Equal(t, p.ID, meta.ProjectID)
}
