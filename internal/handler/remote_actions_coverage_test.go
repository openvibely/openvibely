package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type actionEnv struct {
	tc      *TestContext
	h       *Handler
	ctx     context.Context
	project *models.Project
}

func newActionEnv(t *testing.T) actionEnv {
	tc := NewStoreTestContext(t)
	createAgent(t, tc.llmConfigRepo)
	return actionEnv{
		tc:      tc,
		h:       tc.handler,
		ctx:     models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID}),
		project: createProject(t, tc.handler, "Remote actions"),
	}
}

func (e actionEnv) task(t *testing.T, title string, opts ...func(*models.Task)) (*models.Task, TaskRef) {
	task := createTask(t, e.h, e.project.ID, title, opts...)
	return task, TaskRef{TaskID: task.ID, ProjectID: e.project.ID}
}

func (e actionEnv) reload(t *testing.T, id string) *models.Task {
	task, err := e.h.taskRepo.GetByID(e.ctx, id)
	require.NoError(t, err)
	return task
}

func backlog(tk *models.Task) { tk.Category = models.CategoryBacklog }

func requireOK(t *testing.T, res remote.Result) {
	t.Helper()
	require.Empty(t, res.Code+res.Error)
}

func TestRemoteTaskActions(t *testing.T) {
	e := newActionEnv(t)

	created, res := runAction[createdTask](t, e.h, e.ctx, "task.create", createTaskArgs{ProjectID: e.project.ID, Title: "Created remotely", Prompt: "do it", Category: models.CategoryBacklog, Priority: 2})
	requireOK(t, res)
	require.Equal(t, "Created remotely", created.Task.Title)
	ref := TaskRef{TaskID: created.Task.ID, ProjectID: e.project.ID}

	_, res = runAction[struct{}](t, e.h, e.ctx, "task.exists", ref)
	requireOK(t, res)
	_, res = runAction[struct{}](t, e.h, e.ctx, "task.exists", TaskRef{TaskID: "missing", ProjectID: e.project.ID})
	require.Equal(t, "status:404", res.Code)

	_, res = runAction[struct{}](t, e.h, e.ctx, "task.update", updateTaskArgs{TaskRef: ref, Title: "Renamed", Prompt: "new prompt", Priority: "3", Category: models.CategoryBacklog})
	requireOK(t, res)
	got := e.reload(t, ref.TaskID)
	require.Equal(t, "Renamed", got.Title)
	require.Equal(t, 3, got.Priority)

	_, res = runAction[string](t, e.h, e.ctx, "task.status", taskStatusArgs{TaskRef: ref, Status: models.StatusFailed})
	requireOK(t, res)
	require.Equal(t, models.StatusFailed, e.reload(t, ref.TaskID).Status)

	_, res = runAction[string](t, e.h, e.ctx, "task.category", taskCategoryArgs{TaskRef: ref, Category: models.CategoryCompleted})
	requireOK(t, res)
	require.Equal(t, models.CategoryCompleted, e.reload(t, ref.TaskID).Category)

	chained, res := runAction[*models.Task](t, e.h, e.ctx, "task.chain", taskChainArgs{TaskRef: ref, Form: chainForm{Enabled: true, Trigger: "on_completion"}})
	requireOK(t, res)
	require.Equal(t, ref.TaskID, chained.ID)
	require.NotEmpty(t, e.reload(t, ref.TaskID).ChainConfig)

	merged, res := runAction[*models.Task](t, e.h, e.ctx, "task.automerge", autoMergeArgs{TaskID: ref.TaskID, AutoMerge: true})
	requireOK(t, res)
	require.True(t, merged.AutoMerge)

	first, firstRef := e.task(t, "First", backlog)
	_, res = runAction[string](t, e.h, e.ctx, "task.reorder", reorderTaskArgs{TaskRef: firstRef, Scope: "backlog", Position: "0"})
	requireOK(t, res)
	require.NotNil(t, e.reload(t, first.ID))

	_, res = runAction[string](t, e.h, e.ctx, "task.swarm", swarmArgs{TaskRef: firstRef, Op: "nope"})
	require.Equal(t, "status:400", res.Code)
	_, res = runAction[map[string]string](t, e.h, e.ctx, "swarm.followup", swarmFollowupArgs{TaskRef: firstRef, Message: "go"})
	require.Equal(t, "status:400", res.Code, "a non-swarm task refuses swarm follow-ups")

	_, res = runAction[struct{}](t, e.h, e.ctx, "task.run", firstRef)
	requireOK(t, res)
	require.Equal(t, models.CategoryActive, e.reload(t, first.ID).Category)

	_, res = runAction[string](t, e.h, e.ctx, "task.delete", ref)
	requireOK(t, res)
	require.Nil(t, e.reload(t, ref.TaskID))
}

func TestRemoteBoardActions(t *testing.T) {
	e := newActionEnv(t)
	a, _ := e.task(t, "A", backlog)
	b, _ := e.task(t, "B", backlog)

	_, res := runAction[struct{}](t, e.h, e.ctx, "board.batch_category", batchCategoryArgs{ProjectID: e.project.ID, TaskIDs: []string{a.ID}, Category: models.CategoryCompleted})
	requireOK(t, res)
	require.Equal(t, models.CategoryCompleted, e.reload(t, a.ID).Category)

	n, res := runAction[int](t, e.h, e.ctx, "board.execute_backlog", executeBacklogArgs{ProjectID: e.project.ID})
	requireOK(t, res)
	require.Equal(t, 1, n)
	require.Equal(t, models.CategoryActive, e.reload(t, b.ID).Category)

	_, res = runAction[int](t, e.h, e.ctx, "board.stop_active", e.project.ID)
	requireOK(t, res)
	stopped := e.reload(t, b.ID)
	require.Equal(t, models.StatusCancelled, stopped.Status)
	require.Equal(t, models.CategoryBacklog, stopped.Category)

	c, _ := e.task(t, "C")
	_, res = runAction[int](t, e.h, e.ctx, "board.delete_active", e.project.ID)
	requireOK(t, res)
	require.Nil(t, e.reload(t, c.ID))

	done, _ := e.task(t, "Finished", func(tk *models.Task) { tk.Status = models.StatusCompleted })
	n, res = runAction[int](t, e.h, e.ctx, "board.move_completed", e.project.ID)
	requireOK(t, res)
	require.Equal(t, 1, n)
	require.Equal(t, models.CategoryCompleted, e.reload(t, done.ID).Category)

	n, res = runAction[int](t, e.h, e.ctx, "board.delete_completed", e.project.ID)
	requireOK(t, res)
	require.Equal(t, 2, n)
	require.Nil(t, e.reload(t, a.ID))

	n, res = runAction[int](t, e.h, e.ctx, "board.delete_backlog", e.project.ID)
	requireOK(t, res)
	require.Equal(t, 1, n)
	require.Nil(t, e.reload(t, b.ID))

	_, res = runAction[int](t, e.h, e.ctx, "board.delete_active", "")
	require.Equal(t, "status:400", res.Code)
}

func TestRemoteScheduleModifyAndMove(t *testing.T) {
	e := newActionEnv(t)
	task, _ := e.task(t, "Scheduled")
	saved, res := runAction[scheduleSaveResult](t, e.h, e.ctx, "schedule.create", service.CreateAbsoluteScheduleForTaskRequest{ProjectID: e.project.ID, TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce})
	requireOK(t, res)
	id := saved.Schedule.ID

	later := time.Now().Add(48 * time.Hour).Truncate(time.Minute)
	modified, res := runAction[scheduleSaveResult](t, e.h, e.ctx, "schedule.modify", service.ModifyAbsoluteScheduleRequest{ProjectID: e.project.ID, ScheduleID: id, RunAt: later, RepeatType: models.RepeatDaily, RepeatInterval: 1})
	requireOK(t, res)
	require.Equal(t, models.RepeatDaily, modified.Schedule.RepeatType)

	day := time.Now().Add(72 * time.Hour)
	_, res = runAction[struct{}](t, e.h, e.ctx, "schedule.move", rescheduleArgs{ProjectID: e.project.ID, ScheduleID: id, ScheduleIDs: []string{id}, Date: day, Hour: 7})
	requireOK(t, res)
	moved, err := e.h.scheduleRepo.GetByID(e.ctx, id)
	require.NoError(t, err)
	require.Equal(t, 7, moved.RunAt.Local().Hour())

	_, res = runAction[scheduleSaveResult](t, e.h, e.ctx, "schedule.modify", service.ModifyAbsoluteScheduleRequest{ProjectID: e.project.ID, ScheduleID: "missing", RunAt: later, RepeatType: models.RepeatOnce})
	require.NotEmpty(t, res.Code+res.Error)
}

func TestRemoteGitActions(t *testing.T) {
	e := newActionEnv(t)
	repo := createHandlerTestGitRepo(t)
	runGit(t, repo, "branch", "alternate")
	e.project.RepoPath = repo
	require.NoError(t, e.h.projectSvc.Update(e.ctx, e.project))
	e.h.SetWorktreeService(service.NewWorktreeService(e.h.taskRepo, e.h.projectRepo, e.h.settingsRepo))
	task, _ := e.task(t, "Branch work", func(tk *models.Task) { tk.Category, tk.Status = models.CategoryCompleted, models.StatusCompleted })
	dir, branch, err := e.h.worktreeSvc.SetupWorktree(e.ctx, task, repo)
	require.NoError(t, err)
	require.NoError(t, e.h.taskRepo.UpdateWorktreeInfo(e.ctx, task.ID, dir, branch))

	prefs := changesActionPreferences{Mode: "local", LocalBranch: "alternate", Method: "squash"}
	_, res := runAction[struct{}](t, e.h, e.ctx, "changes.settings.save", changesSettingsArgs{TaskID: task.ID, Prefs: prefs})
	requireOK(t, res)
	saved, err := e.h.settingsRepo.Get(e.ctx, changesPreferenceKey(e.project.ID))
	require.NoError(t, err)
	require.Contains(t, saved, `"local_branch":"alternate"`)

	elig, res := runAction[changesEligibility](t, e.h, e.ctx, "changes.eligibility", changesEligibilityArgs{TaskID: task.ID, Target: "alternate"})
	requireOK(t, res)
	require.Equal(t, "alternate", elig.Target)
	_, res = runAction[changesEligibility](t, e.h, e.ctx, "changes.eligibility", changesEligibilityArgs{TaskID: task.ID, Target: branch})
	require.Equal(t, "status:400", res.Code, "the task branch is not a valid target")

	meta, res := runAction[changesFileMeta](t, e.h, e.ctx, "changes.file", changesFileArgs{TaskID: task.ID, Index: 99})
	requireOK(t, res)
	require.False(t, meta.OK)

	_, res = runAction[changesBranchesPage](t, e.h, e.ctx, "changes.branches", changesBranchesArgs{TaskID: task.ID, Page: 0})
	require.Equal(t, "status:400", res.Code)

	for _, name := range []string{"task.conflicts.abort", "task.conflicts.resolve"} {
		out, res := runAction[mutationOutcome](t, e.h, e.ctx, name, conflictArgs{TaskID: task.ID})
		requireOK(t, res)
		require.Equal(t, http.StatusConflict, out.Code, name+" with no active merge")
	}

	out, res := runAction[mutationOutcome](t, e.h, e.ctx, "task.merge", branchMutationArgs{TaskID: task.ID, MergeType: "rebase-ish"})
	requireOK(t, res)
	require.Equal(t, http.StatusBadRequest, out.Code)

	out, res = runAction[mutationOutcome](t, e.h, e.ctx, "task.pullrequest", pullRequestArgs{TaskID: task.ID, ProjectID: e.project.ID})
	requireOK(t, res)
	require.Contains(t, out.Message, "GitHub integration is not configured")

	_, res = runAction[struct{}](t, e.h, e.ctx, "task.worktree.cleanup", cleanupWorktreeArgs{TaskID: task.ID, DeleteBranch: true})
	requireOK(t, res)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "worktree directory removed")
}

func TestRemoteThreadActions(t *testing.T) {
	e := newActionEnv(t)
	task, ref := e.task(t, "Thread", func(tk *models.Task) { tk.Category, tk.Status = models.CategoryCompleted, models.StatusCompleted })
	agent := createAgent(t, e.h.llmConfigRepo, func(c *models.LLMConfig) { c.Name, c.IsDefault = "Second", false })

	_, res := runAction[struct{}](t, e.h, e.ctx, "thread.model", threadModelArgs{TaskRef: ref, AgentID: agent.ID})
	requireOK(t, res)
	require.Equal(t, agent.ID, *e.reload(t, task.ID).AgentID)

	// The missing-turn guard requires a running execution. Seed one on a
	// separate task so a background send cannot race this validation.
	active, activeRef := e.task(t, "Active thread", func(tk *models.Task) {
		tk.Category, tk.Status = models.CategoryActive, models.StatusRunning
		tk.AgentID = &agent.ID
	})
	createExec(t, e.h, active.ID, agent.ID)
	_, res = runAction[*models.ThreadInput](t, e.h, e.ctx, "thread.steer", threadSteerArgs{TaskRef: activeRef, Message: "steer"})
	require.Equal(t, "status:400", res.Code, "steering needs the turn being steered")

	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: e.project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued"}
	require.NoError(t, e.h.threadInputRepo.CreateQueued(e.ctx, queued))
	_, res = runAction[*models.ThreadInput](t, e.h, e.ctx, "thread.input.steer", queuedSteerArgs{InputID: queued.ID, TaskID: task.ID})
	require.NotEmpty(t, res.Code+res.Error, "no running turn to steer into")
	mutated, res := runAction[inputMutationResult](t, e.h, e.ctx, "thread.input.mutate", inputMutationArgs{InputID: queued.ID, Op: "cancel"})
	requireOK(t, res)
	require.Equal(t, "cancelled", mutated.Status)

	// Start the asynchronous send only after checking the idle thread's inputs.
	sent, res := runAction[threadSendResult](t, e.h, e.ctx, "thread.send", threadSendArgs{TaskRef: ref, Message: "follow up"})
	requireOK(t, res)
	require.Equal(t, "follow up", sent.Message)
}

func TestRemoteChatActions(t *testing.T) {
	e := newActionEnv(t)

	sent, res := runAction[threadSendResult](t, e.h, e.ctx, "chat.send", chatSendArgs{ProjectID: e.project.ID, Message: "hello"})
	requireOK(t, res)
	require.NotNil(t, sent.Task)
	require.Equal(t, models.CategoryChat, sent.Task.Category)

	_, res = runAction[*models.ThreadInput](t, e.h, e.ctx, "chat.steer", chatSteerArgs{ProjectID: e.project.ID, Message: "steer"})
	require.Equal(t, "status:400", res.Code, "steering needs the turn being steered")

	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: e.project.ID, AgentConfigID: *sent.Task.AgentID, InputMode: models.ThreadInputModeQueued, Content: "queued"}
	require.NoError(t, e.h.threadInputRepo.CreateQueued(e.ctx, queued))
	_, res = runAction[*models.ThreadInput](t, e.h, e.ctx, "chat.input.steer", queuedSteerArgs{InputID: queued.ID})
	require.NotEmpty(t, res.Code+res.Error, "no matching turn to steer into")

	path := filepath.Join(t.TempDir(), "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0600))
	att := &models.ChatAttachment{ExecutionID: sent.Execution.ID, FileName: "note.txt", FilePath: path, MediaType: "text/plain", FileSize: 1}
	require.NoError(t, e.h.chatAttachmentRepo.Create(e.ctx, att))
	_, res = runAction[[]models.ChatAttachment](t, e.h, e.ctx, "chat.attachment.delete", deleteChatAttachmentArgs{AttachmentID: att.ID, ProjectID: e.project.ID})
	requireOK(t, res)
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err))

	_, res = runAction[struct{}](t, e.h, e.ctx, "chat.clear", e.project.ID)
	requireOK(t, res)
	require.Nil(t, e.reload(t, sent.Task.ID))
}

func TestRemoteTaskAttachmentActions(t *testing.T) {
	e := newActionEnv(t)
	old := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = old })
	_, ref := e.task(t, "Attachments")
	token := generateSessionID()
	_, res := runAction[struct{}](t, e.h, e.ctx, "upload.stage", stageUploadArgs{TaskID: ref.TaskID, Token: token, Name: "spec.txt", Data: []byte("spec")})
	requireOK(t, res)

	projectID, res := runAction[string](t, e.h, e.ctx, "task.attachments.upload", uploadAttachmentsArgs{TaskRef: ref, Files: []uploadFile{{Name: "spec.txt", Token: token, Size: 4}}})
	requireOK(t, res)
	require.Equal(t, e.project.ID, projectID)
	rows, err := e.h.attachmentRepo.ListByTask(e.ctx, ref.TaskID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	deleted, res := runAction[TaskRef](t, e.h, e.ctx, "task.attachments.delete", deleteAttachmentArgs{AttachmentID: rows[0].ID, ProjectID: e.project.ID})
	requireOK(t, res)
	require.Equal(t, ref, deleted)
	rows, err = e.h.attachmentRepo.ListByTask(e.ctx, ref.TaskID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestRemoteReviewActions(t *testing.T) {
	e := newActionEnv(t)
	e.h.SetReviewCommentRepo(e.tc.stores.ReviewComments)
	_, ref := e.task(t, "Review", func(tk *models.Task) { tk.Category, tk.Status = models.CategoryCompleted, models.StatusCompleted })

	comments, res := runAction[[]models.ReviewComment](t, e.h, e.ctx, "review.add", addReviewArgs{TaskRef: ref, FilePath: "main.go", LineNumber: "3", CommentText: "rename this"})
	requireOK(t, res)
	require.Len(t, comments, 1)

	_, res = runAction[struct{}](t, e.h, e.ctx, "review.comment", reviewCommentArgs{ProjectID: e.project.ID, CommentID: comments[0].ID, Text: "rename this var"})
	requireOK(t, res)
	stored, err := e.tc.stores.ReviewComments.ListByTask(e.ctx, ref.TaskID)
	require.NoError(t, err)
	require.Equal(t, "rename this var", stored[0].CommentText)

	_, res = runAction[struct{}](t, e.h, e.ctx, "review.comment", reviewCommentArgs{ProjectID: e.project.ID, CommentID: comments[0].ID, Delete: true})
	requireOK(t, res)
	stored, err = e.tc.stores.ReviewComments.ListByTask(e.ctx, ref.TaskID)
	require.NoError(t, err)
	require.Empty(t, stored)

	_, res = runAction[string](t, e.h, e.ctx, "review.submit", ref)
	require.Equal(t, "status:400", res.Code, "nothing to submit")
	_, res = runAction[[]models.ReviewComment](t, e.h, e.ctx, "review.add", addReviewArgs{TaskRef: ref, FilePath: "main.go", LineNumber: "4", CommentText: "and this"})
	requireOK(t, res)
	_, res = runAction[string](t, e.h, e.ctx, "review.submit", ref)
	requireOK(t, res)
	execs, err := e.h.execRepo.ListByTask(e.ctx, ref.TaskID)
	require.NoError(t, err)
	require.NotEmpty(t, execs, "submitting starts a review turn")
}

func TestRemoteWebhookActions(t *testing.T) {
	e := newActionEnv(t)
	e.h.SetWebhookRepo(e.tc.stores.Webhooks)
	args := webhookMutationArgs{ProjectID: e.project.ID, Form: webhookEndpointForm{Name: "Deploys", Enabled: true, DefaultPriority: 2}}

	created, res := runAction[*models.WebhookEndpoint](t, e.h, e.ctx, "webhook.create", args)
	requireOK(t, res)
	require.Equal(t, "Deploys", created.Name)
	args.WebhookID = created.ID

	args.Form.Name = "Deploys v2"
	updated, res := runAction[*models.WebhookEndpoint](t, e.h, e.ctx, "webhook.update", args)
	requireOK(t, res)
	require.Equal(t, "Deploys v2", updated.Name)

	secret, res := runAction[string](t, e.h, e.ctx, "webhook.rotate", args)
	requireOK(t, res)
	require.NotEmpty(t, secret)

	task, res := runAction[*models.Task](t, e.h, e.ctx, "webhook.test", args)
	requireOK(t, res)
	require.Equal(t, e.project.ID, task.ProjectID)

	_, res = runAction[struct{}](t, e.h, e.ctx, "webhook.delete", args)
	requireOK(t, res)
	_, res = runAction[*models.WebhookEndpoint](t, e.h, e.ctx, "webhook.update", args)
	require.Equal(t, "status:404", res.Code)
}

func TestRemoteInsightActions(t *testing.T) {
	env := wireTestHandler(t, teststores.New(t).Stores, testHandlerOptions{insights: true})
	h := env.Handler
	createAgent(t, h.llmConfigRepo)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Insights")
	createTask(t, h, project.ID, "Something to analyze", func(tk *models.Task) { tk.Category, tk.Status = models.CategoryCompleted, models.StatusCompleted })

	report, res := runAction[*models.InsightReport](t, h, ctx, "insights.analyze", project.ID)
	requireOK(t, res)
	require.Equal(t, project.ID, report.ProjectID)

	_, res = runAction[struct{}](t, h, ctx, "insights.mutate", insightMutationArgs{ProjectID: project.ID, InsightID: "missing", Operation: "delete"})
	require.Equal(t, "status:404", res.Code)
	_, res = runAction[struct{}](t, h, ctx, "insights.mutate", insightMutationArgs{ProjectID: project.ID, Operation: "bogus"})
	require.Equal(t, "status:400", res.Code)
}

func TestRemoteProjectAdminActions(t *testing.T) {
	e := newActionEnv(t)
	e.h.SetProjectAccessService(service.NewProjectAccessService(e.tc.stores.ProjectAccess))
	org := service.NewOrganizationService(e.tc.stores.Organizations, "secret")
	e.h.SetOrganizationService(org)
	token, err := org.Invite(e.ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(e.ctx, token, "remote-member", "long-password-value", "")
	require.NoError(t, err)

	state, res := runAction[projectSharingState](t, e.h, e.ctx, "project.sharing", e.project.ID)
	requireOK(t, res)
	require.True(t, state.Owner)

	_, res = runAction[projectSharingResult](t, e.h, e.ctx, "project.sharing.update", projectSharingArgs{ProjectID: e.project.ID, Operation: "member", UserID: user.UserID, Role: "maintainer"})
	requireOK(t, res)
	members, err := e.h.projectAccessSvc.Members(e.ctx, e.project.ID)
	require.NoError(t, err)
	var roles []string
	for _, m := range members {
		if m.UserID == user.UserID {
			roles = append(roles, m.Role)
		}
	}
	require.Equal(t, []string{"maintainer"}, roles)

	require.NoError(t, e.h.githubAuthRepo.UpsertProjectInbox(e.ctx, &models.GitHubProjectInbox{ProjectID: e.project.ID, GitHubLogin: "octo-bot", Enabled: true}))
	gh, res := runAction[githubRuntimeState](t, e.h, e.ctx, "github.runtime_state", e.project.ID)
	requireOK(t, res)
	require.Equal(t, "octo-bot", gh.Inbox.GitHubLogin)

	recloned, res := runAction[projectUpdateResult](t, e.h, e.ctx, "project.reclone", e.project.ID)
	requireOK(t, res)
	require.Equal(t, http.StatusConflict, recloned.Code, "only a missing GitHub clone can be re-cloned")
}

func TestRemoteAutomationActions(t *testing.T) {
	e := newActionEnv(t)
	automations := e.tc.stores.Automations
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automations, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automations, e.h.taskSvc, e.h.taskRepo, e.h.scheduleRepo, validator)
	e.h.SetAutomationServices(service.NewAutomationGraphService(automations), nil)
	e.h.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automations, e.h.scheduleRepo))

	candidate, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Nightly"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "followup", Name: "Follow up", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Run after the schedule.", "category": "backlog", "priority": 2}},
	}
	candidate.Edges = []models.AutomationDraftEdge{{Key: "schedule_followup", From: "schedule", To: "followup", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}

	preview, res := runAction[*models.AutomationDraftResult](t, e.h, e.ctx, "automation.preview", automationPreviewArgs{ProjectID: e.project.ID, Candidate: candidate})
	requireOK(t, res)
	require.Empty(t, preview.ValidationErrors)

	saved, res := runAction[*service.AutomationSaveResult](t, e.h, e.ctx, "automation.save", automationSaveArgs{ProjectID: e.project.ID, Candidate: candidate})
	requireOK(t, res)
	require.NotNil(t, saved.Definition)
	require.Equal(t, "Nightly", saved.Definition.Automation.Name)

	_, res = runAction[*models.AutomationDraftResult](t, e.h, e.ctx, "automation.describe", automationDescriptionArgs{ProjectID: e.project.ID, Description: "every night"})
	require.Contains(t, res.Error, "unavailable", "describing needs a model-backed draft service")
	_, res = runAction[struct{}](t, e.h, e.ctx, "automation.refresh", automationMutationArgs{ProjectID: e.project.ID, AutomationID: saved.Definition.Automation.ID})
	require.Equal(t, "status:503", res.Code, "refresh needs the external-state service")
}
