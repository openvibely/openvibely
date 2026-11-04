package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
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
	require.Equal(t, persistence.XReceiptClaimed, claim.Result)
	firstToken := claim.Token
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, persistence.XReceiptActive, claim.Result)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, persistence.XReceiptClaimed, claim.Result)
	require.NotEqual(t, firstToken, claim.Token)
	require.ErrorIs(t, receipts.Release(ctx, "tweet-1", firstToken), nil)
	active, err := receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(2*time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, persistence.XReceiptActive, active.Result, "stale release must not remove a newer lease")
	_, err = receipts.CompleteWithHandoff(ctx, "tweet-1", claim.Token, nil, nil)
	require.NoError(t, err)
	claim, err = receipts.Claim(ctx, "tweet-1", p1.ID, now.Add(4*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Equal(t, persistence.XReceiptCompleted, claim.Result)
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

	_, err = receipts.CompleteWithHandoff(ctx, "tweet-atomic", claim.Token, nil, func(tx persistence.Tx) error {
		exec, err := executorFromTx(tx)
		if err != nil {
			return err
		}
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
	require.Equal(t, persistence.XReceiptActive, active.Result)
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

func TestXHandoffRechecksInstallerAndAuditsAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewXInboundReceiptRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('installer','installer'); INSERT INTO org_memberships VALUES('default','installer','admin'); INSERT INTO app_settings(key,value) VALUES('x_run_as_user_id','installer')`)
	require.NoError(t, err)
	claim, err := repo.Claim(ctx, "tweet", "default", time.Now(), time.Minute)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "installer"})
	called := false
	persist := func(tx persistence.Tx) error {
		called = true
		exec, err := executorFromTx(tx)
		if err != nil {
			return err
		}
		_, err = exec.ExecContext(ctx, `INSERT INTO app_settings(key,value) VALUES('handoff_test','saved')`)
		return err
	}
	_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='installer'`)
	require.NoError(t, err)
	_, err = repo.CompleteWithHandoff(actor, "tweet", claim.Token, nil, persist)
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
	require.False(t, called)
	_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='installer'`)
	require.NoError(t, err)
	_, err = repo.CompleteWithHandoff(models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID}), "tweet", claim.Token, nil, persist)
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
	require.False(t, called)
	_, err = db.Exec(`CREATE TRIGGER fail_x_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = repo.CompleteWithHandoff(actor, "tweet", claim.Token, nil, persist)
	require.ErrorContains(t, err, "audit unavailable")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key='handoff_test'`).Scan(&count))
	require.Zero(t, count)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM x_inbound_receipts WHERE tweet_id='tweet'`).Scan(&status))
	require.Equal(t, "processing", status)
	_, err = db.Exec(`DROP TRIGGER fail_x_audit`)
	require.NoError(t, err)
	_, err = repo.CompleteWithHandoff(actor, "tweet", claim.Token, nil, persist)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='installer'`)
	require.NoError(t, err)
	_, err = repo.CompleteWithHandoff(actor, "tweet", claim.Token, nil, nil)
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
}

func TestXVerifiedSenderBindingAndHandoff(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	auth := NewXAuthRepo(db)
	receipts := NewXInboundReceiptRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "sender"})
	binding := &models.XAuthorizedUser{ProjectID: "default", XUserID: "123", ApplicationUserID: "sender"}
	require.ErrorIs(t, auth.Create(sender, binding), persistence.ErrOrgDenied)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, auth.Create(owner, binding))
	require.Equal(t, models.BootstrapUserID, binding.VerifiedBy)
	recovered := NewXAuthRepo(db)
	id, err := recovered.ResolveApplicationUser(ctx, "default", "123")
	require.NoError(t, err)
	require.Equal(t, "sender", id)
	_, err = recovered.ResolveApplicationUser(ctx, "foreign", "123")
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	claim, err := receipts.Claim(ctx, "mapped-tweet", "default", time.Now(), time.Minute)
	require.NoError(t, err)
	ingress := models.WithXIngressIdentity(sender, models.XIngressIdentity{InstallerUserID: models.BootstrapUserID, UserID: "sender", ExternalUserID: "123", ProjectID: "default"})
	_, err = receipts.CompleteWithHandoff(ingress, "mapped-tweet", claim.Token, nil, nil)
	require.NoError(t, err)
	var actor string
	require.NoError(t, db.QueryRow(`SELECT actor_user_id FROM project_audit_events WHERE action='x.message_handed_off' AND target_id='mapped-tweet'`).Scan(&actor))
	require.Equal(t, "sender", actor)
	// Even an owner cannot supply their own principal for a bound sender.
	forged := models.WithXIngressIdentity(owner, models.XIngressIdentity{InstallerUserID: models.BootstrapUserID, UserID: models.BootstrapUserID, ExternalUserID: "123", ProjectID: "default"})
	_, err = receipts.CompleteWithHandoff(forged, "mapped-tweet", claim.Token, nil, nil)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='sender'`)
	require.NoError(t, err)
	_, err = receipts.CompleteWithHandoff(ingress, "mapped-tweet", claim.Token, nil, nil)
	require.Error(t, err)
}

func TestXSenderManagementRechecksAndRollsBack(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewXAuthRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('manager','manager'); INSERT INTO org_memberships VALUES('default','manager','admin')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "manager", Role: "owner"})
	entry := &models.XAuthorizedUser{ProjectID: "default", XUserID: "42"}
	require.NoError(t, repo.Create(actor, entry))
	_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='manager'`)
	require.NoError(t, err)
	require.ErrorIs(t, repo.Delete(actor, "default", entry.ID), persistence.ErrOrgDenied)
	require.ErrorIs(t, repo.Create(actor, &models.XAuthorizedUser{ProjectID: "default", XUserID: "43"}), persistence.ErrOrgDenied)
	require.ErrorIs(t, repo.Delete(owner, "foreign", entry.ID), persistence.ErrNotFound)
	_, err = db.Exec(`CREATE TRIGGER fail_sender_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.Delete(owner, "default", entry.ID), "audit unavailable")
	require.ErrorContains(t, repo.Create(owner, &models.XAuthorizedUser{ProjectID: "default", XUserID: "43"}), "audit unavailable")
	entries, err := repo.ListByProject(ctx, "default")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, entry.ID, entries[0].ID)
	_, err = db.Exec(`DROP TRIGGER fail_sender_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(owner, "default", entry.ID))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='x.sender_removed'`).Scan(&count))
	require.Equal(t, 1, count)
}
