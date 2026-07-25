package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationChatConfirmationBindsExactCandidateWithoutCreatingAutomation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Confirmation")
	taskRepo := repository.NewTaskRepo(db, nil)
	chatTask := models.Task{ProjectID: project.ID, Title: "Chat confirmation thread", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, taskRepo.Create(ctx, &chatTask))
	execRepo := repository.NewExecutionRepo(db)
	planExec := models.Execution{TaskID: chatTask.ID, Status: models.ExecCompleted, PromptSent: "create an automation"}
	require.NoError(t, execRepo.Create(ctx, &planExec))
	planTime := time.Now().UTC().Add(-2 * time.Minute)
	_, err := db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, planTime, planExec.ID)
	require.NoError(t, err)

	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	confirmation := NewAutomationConfirmationService(automationRepo, execRepo, []byte("test-confirmation-secret"))
	now := time.Now().UTC()
	confirmation.now = func() time.Time { return now }
	token, err := confirmation.Issue(ctx, AutomationConfirmationIssue{ProjectID: project.ID, PrincipalID: "alice",
		ThreadID: chatTask.ID, PlanMessageID: planExec.ID, AutomationName: candidate.Name, Source: "manual", Candidate: candidate})
	require.NoError(t, err)

	var automationCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations`).Scan(&automationCount))
	require.Zero(t, automationCount, "planning and confirmation state must not create an Automation")

	confirming := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "save " + candidate.Name}
	require.NoError(t, execRepo.Create(ctx, &confirming))
	_, err = db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, planTime.Add(time.Minute), confirming.ID)
	require.NoError(t, err)
	prepared, err := confirmation.PrepareChatConfirmation(ctx, project.ID, "alice", chatTask.ID, confirming.ID, confirming.PromptSent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, token, prepared.Token)

	tokenID, err := confirmation.ValidateChatConfirmation(ctx, AutomationChatConfirmation{Token: token, ProjectID: project.ID,
		PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name})
	require.NoError(t, err)
	require.NotEmpty(t, tokenID)
	receipt, err := automationRepo.GetAutomationConfirmationReceipt(ctx, tokenID)
	require.NoError(t, err)
	require.Nil(t, receipt.ConsumedAt, "validation alone must not consume confirmation before atomic Save")
	require.JSONEq(t, mustJSON(t, candidate), receipt.CandidateJSON)

	validator := NewAutomationSaveValidator(registry, drafts)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, repository.NewScheduleRepo(db), validator)
	tampered := candidate
	tampered.Description = "A different graph than the one displayed."
	_, err = compiler.Save(ctx, AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "chat", Candidate: tampered,
		ConfirmationTokenID: tokenID, ConfirmationPrincipal: "alice", ConfirmationThreadID: chatTask.ID,
		ConfirmingUserInputID: confirming.ID})
	require.ErrorContains(t, err, "does not match this Save")
	require.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM automations`))
	receipt, err = automationRepo.GetAutomationConfirmationReceipt(ctx, tokenID)
	require.NoError(t, err)
	require.Nil(t, receipt.ConsumedAt, "a mismatched candidate must not consume confirmation")

	saved, err := compiler.Save(ctx, AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "chat", Candidate: candidate,
		ConfirmationTokenID: tokenID, ConfirmationPrincipal: "alice", ConfirmationThreadID: chatTask.ID,
		ConfirmingUserInputID: confirming.ID})
	require.NoError(t, err)
	require.NotNil(t, saved.Definition)
	receipt, err = automationRepo.GetAutomationConfirmationReceipt(ctx, tokenID)
	require.NoError(t, err)
	require.NotNil(t, receipt.ConsumedAt)

	_, err = confirmation.ValidateChatConfirmation(ctx, AutomationChatConfirmation{Token: token + "tampered", ProjectID: project.ID,
		PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name})
	require.ErrorContains(t, err, "invalid automation confirmation token")
	otherProject := automationTestProject(t, projectRepo, "Other confirmation project")
	_, err = confirmation.ValidateChatConfirmation(ctx, AutomationChatConfirmation{Token: token, ProjectID: otherProject.ID,
		PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name})
	require.ErrorContains(t, err, "scope does not match")
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
