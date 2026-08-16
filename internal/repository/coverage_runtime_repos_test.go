package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubPRFeedbackRepoRecordsAndSuppressesDuplicates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewGitHubPRFeedbackRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	pullRequestRepo := NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "PR Feedback Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Review PR feedback", Prompt: "Address feedback", Category: models.CategoryActive, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	pullRequest := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}
	require.NoError(t, pullRequestRepo.Upsert(ctx, pullRequest))

	feedback := &models.GitHubPRFeedbackForwarded{
		TaskPullRequestID: pullRequest.ID,
		TaskID:            task.ID,
		RepoFullName:      "OpenVibely/OpenVibely",
		PRNumber:          42,
		FeedbackKind:      "issue_comment",
		GitHubID:          "1001",
		GitHubNodeID:      "node-1001",
		AuthorLogin:       " Alice ",
		HTMLURL:           "https://github.com/openvibely/openvibely/pull/42#issuecomment-1001",
		Body:              "Please add a regression test.",
		CreatedAt:         time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	}

	recorded, err := repo.RecordForwarded(ctx, feedback)
	require.NoError(t, err)
	require.True(t, recorded)
	require.NotEmpty(t, feedback.ID)
	require.Equal(t, "openvibely/openvibely", feedback.RepoFullName)
	require.Equal(t, "alice", feedback.AuthorLogin)
	require.False(t, feedback.ForwardedAt.IsZero())

	forwarded, err := repo.AlreadyForwarded(ctx, "OPENVIBELY/openvibely", 42, "issue_comment", "1001")
	require.NoError(t, err)
	require.True(t, forwarded)

	duplicate := *feedback
	duplicate.ID = ""
	duplicate.ForwardedAt = time.Time{}
	recorded, err = repo.RecordForwarded(ctx, &duplicate)
	require.NoError(t, err)
	require.False(t, recorded)
	require.Empty(t, duplicate.ID)

	forwarded, err = repo.AlreadyForwarded(ctx, "", 42, "issue_comment", "1001")
	require.NoError(t, err)
	require.False(t, forwarded)
}

func TestEmailInboundReceiptRepoWithHandoffIsAtomicAndIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewEmailInboundReceiptRepo(db)

	persistCalls := 0
	already, err := repo.WithHandoff(ctx, "inbox@example.com", "message-1", func(exec SQLExecutor) error {
		persistCalls++
		_, err := exec.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, ?)`, "email-handoff-test", "created")
		return err
	})
	require.NoError(t, err)
	require.False(t, already)
	require.Equal(t, 1, persistCalls)

	exists, err := repo.Exists(ctx, "inbox@example.com", "message-1")
	require.NoError(t, err)
	require.True(t, exists)

	already, err = repo.WithHandoff(ctx, "inbox@example.com", "message-1", func(exec SQLExecutor) error {
		persistCalls++
		return nil
	})
	require.NoError(t, err)
	require.True(t, already)
	require.Equal(t, 1, persistCalls)

	already, err = repo.WithHandoff(ctx, "inbox@example.com", "message-2", func(exec SQLExecutor) error {
		persistCalls++
		return errors.New("persist failed")
	})
	require.ErrorContains(t, err, "persist failed")
	require.False(t, already)

	exists, err = repo.Exists(ctx, "inbox@example.com", "message-2")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestSettingsRepoGetManyAndObservers(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewSettingsRepo(db)

	var observed []string
	var acquired []string
	repo.SetQueryObserver(func(query string) { observed = append(observed, query) })
	repo.SetQueryAcquiredObserver(func(query string) { acquired = append(acquired, query) })

	values, err := repo.GetMany(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, values)
	require.Empty(t, observed)
	require.Empty(t, acquired)

	require.NoError(t, repo.Set(ctx, "alpha", "one"))
	require.NoError(t, repo.Set(ctx, "beta", "two"))
	got, err := repo.Get(ctx, "alpha")
	require.NoError(t, err)
	require.Equal(t, "one", got)
	missing, err := repo.Get(ctx, "missing")
	require.NoError(t, err)
	require.Empty(t, missing)

	many, err := repo.GetMany(ctx, []string{"alpha", "missing", "beta"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"alpha": "one", "beta": "two"}, many)
	require.Len(t, observed, 3)
	require.Len(t, acquired, 3)
	require.Contains(t, observed[2], "key IN")
}

func TestWorkerRepoRoundTripsMaxWorkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewWorkerRepo(db)

	initial, err := repo.GetMaxWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, initial)

	require.NoError(t, repo.SetMaxWorkers(ctx, 7))
	updated, err := repo.GetMaxWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 7, updated)

	require.NoError(t, repo.SetMaxWorkers(ctx, 0))
	unlimited, err := repo.GetMaxWorkers(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, unlimited)
}

func TestTaskRepoUpdateAgentDefinition(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewAgentRepo(db)

	project := &models.Project{Name: "Lifecycle Task Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.Agent{Name: "Lifecycle Agent", Key: "lifecycle-agent", Model: "inherit", Scope: models.AgentScopeProject}
	require.NoError(t, agentRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "Route lifecycle", Prompt: "Use selected agent", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))

	require.ErrorContains(t, taskRepo.UpdateAgentDefinition(ctx, "", &agent.ID), "missing task id")
	require.NoError(t, taskRepo.UpdateAgentDefinition(ctx, task.ID, &agent.ID))
	loaded, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.AgentDefinitionID)
	require.Equal(t, agent.ID, *loaded.AgentDefinitionID)

	require.NoError(t, taskRepo.UpdateAgentDefinition(ctx, task.ID, nil))
	loaded, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, loaded.AgentDefinitionID)
}

func TestAutomationConfirmationReceiptsRoundTripAndMatchInputScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	automationRepo := NewAutomationRepo(db)

	project := &models.Project{Name: "Confirmation Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	thread := &models.Task{ProjectID: project.ID, Title: "Chat confirmation thread", Prompt: "chat", Category: models.CategoryChat, Status: models.StatusCompleted, Priority: 1}
	require.NoError(t, taskRepo.Create(ctx, thread))
	planExec := &models.Execution{TaskID: thread.ID, Status: models.ExecCompleted, PromptSent: "plan", Output: "approve?"}
	require.NoError(t, execRepo.Create(ctx, planExec))

	receipt := &models.AutomationChatConfirmationReceipt{
		TokenID:        "token-1",
		ProjectID:      project.ID,
		PrincipalID:    "principal-1",
		ThreadID:       thread.ID,
		PlanMessageID:  planExec.ID,
		AutomationName: "Nightly audit",
		Source:         "manual",
		CandidateJSON:  `{"name":"Nightly audit"}`,
		ExpiresAt:      time.Now().Add(time.Hour).UTC(),
	}
	require.NoError(t, automationRepo.CreateAutomationConfirmationReceipt(ctx, receipt))

	loaded, err := automationRepo.GetAutomationConfirmationReceipt(ctx, receipt.TokenID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, receipt.AutomationName, loaded.AutomationName)
	require.Empty(t, loaded.ConfirmingUserInputID)

	pending, name, err := automationRepo.GetPendingAutomationConfirmation(ctx, project.ID, receipt.PrincipalID, thread.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, "Nightly audit", name)

	marker := AutomationConfirmationInputMarker{InputID: planExec.ID, TokenID: receipt.TokenID, ProjectID: project.ID, PrincipalID: receipt.PrincipalID, ThreadID: thread.ID, Method: "command"}
	require.NoError(t, automationRepo.MarkAutomationConfirmationInput(ctx, marker))
	matched, err := automationRepo.HasAutomationConfirmationInput(ctx, marker)
	require.NoError(t, err)
	require.True(t, matched)

	wrongScope := marker
	wrongScope.PrincipalID = "other"
	require.ErrorContains(t, automationRepo.MarkAutomationConfirmationInput(ctx, wrongScope), "scope does not match")

	missing, err := automationRepo.GetAutomationConfirmationReceipt(ctx, "missing")
	require.NoError(t, err)
	require.Nil(t, missing)
}
