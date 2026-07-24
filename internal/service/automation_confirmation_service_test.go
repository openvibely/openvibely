package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationChatConfirmationRequiresLaterExactCommandAndIsReplaySafe(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Confirmation")
	taskRepo := repository.NewTaskRepo(db, nil)
	chatTask := models.Task{ProjectID: project.ID, Title: "Chat confirmation thread", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, taskRepo.Create(context.Background(), &chatTask))
	execRepo := repository.NewExecutionRepo(db)
	planExec := models.Execution{TaskID: chatTask.ID, Status: models.ExecCompleted, PromptSent: "create an automation"}
	require.NoError(t, execRepo.Create(context.Background(), &planExec))
	planExec.StartedAt = time.Now().UTC().Add(-2 * time.Minute)
	require.NoError(t, func() error {
		_, err := db.Exec(`UPDATE executions SET started_at = ? WHERE id = ?`, planExec.StartedAt, planExec.ID)
		return err
	}())

	automationRepo := repository.NewAutomationRepo(db)
	drafts := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "chat", Candidate: candidate})
	require.NoError(t, err)
	effects := []models.AutomationPublicationEffect{{StepKey: "task:vision_driver", Operation: "create", TargetKey: "task:vision_driver", ResourceType: "task", Name: "Vision Driver"}}
	confirmation := NewAutomationConfirmationService(automationRepo, execRepo, []byte("test-confirmation-secret"))
	now := time.Now().UTC()
	confirmation.now = func() time.Time { return now }
	token, err := confirmation.Issue(context.Background(), AutomationConfirmationIssue{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, PlanMessageID: planExec.ID, AutomationName: candidate.Name, Source: "manual", Candidate: candidate})
	require.NoError(t, err)
	require.NotContains(t, token, "alice")

	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: planExec.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "later user input")

	ambiguous := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "yes please"}
	require.NoError(t, execRepo.Create(context.Background(), &ambiguous))
	pending, err := confirmation.PrepareChatConfirmation(context.Background(), project.ID, "alice", chatTask.ID, ambiguous.ID, ambiguous.PromptSent)
	require.NoError(t, err)
	require.Nil(t, pending)
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: ambiguous.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "exact confirmation command")

	confirming := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "save " + candidate.Name}
	require.NoError(t, execRepo.Create(context.Background(), &confirming))
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "not marked affirmative")
	prepared, err := confirmation.PrepareChatConfirmation(context.Background(), project.ID, "alice", chatTask.ID, confirming.ID, confirming.PromptSent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, token, prepared.Token)
	first, err := confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: prepared.Token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.NoError(t, err)
	require.NotEmpty(t, first.Attempt.ID)
	second, err := confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.NoError(t, err)
	require.Equal(t, first.Attempt.ID, second.Attempt.ID)

	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token + "tampered", ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "invalid automation confirmation token")

	otherProject := automationTestProject(t, projectRepo, "Confirmation other project")
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: otherProject.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "scope does not match")
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: "other-version", PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "scope does not match")
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "mallory", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "scope does not match")

	otherThread := models.Task{ProjectID: project.ID, Title: "Other confirmation thread", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, taskRepo.Create(context.Background(), &otherThread))
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: otherThread.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "scope does not match")

	replayInput := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "save " + candidate.Name}
	require.NoError(t, execRepo.Create(context.Background(), &replayInput))
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: token, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: replayInput.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "not marked affirmative")

	expiringToken, err := confirmation.Issue(context.Background(), AutomationConfirmationIssue{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, PlanMessageID: planExec.ID, AutomationName: candidate.Name, Source: "manual", Candidate: candidate})
	require.NoError(t, err)
	now = now.Add(31 * time.Minute)
	_, err = confirmation.ConfirmChat(context.Background(), AutomationChatConfirmation{Token: expiringToken, ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: "revision", PrincipalID: "alice", ThreadID: chatTask.ID, ConfirmingUserInputID: confirming.ID, AutomationName: candidate.Name, Effects: effects})
	require.ErrorContains(t, err, "expired")
}
