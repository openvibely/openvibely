package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelContextModeActionHandlers(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Channel Runtime Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	for _, channelName := range []string{"Slack", "Telegram", "Discord", "Email"} {
		t.Run(channelName, func(t *testing.T) {
			handlers := buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
				ChannelDisplayName: channelName,
				ProjectID:          project.ID,
				ProjectRepo:        projectRepo,
			})

			currentProject, err := handlers["get_current_project"](ctx, nil)
			require.NoError(t, err)
			require.Equal(t, "Current project: Channel Runtime Project (id: "+project.ID+")", currentProject)

			chatMode, err := handlers["get_chat_mode"](ctx, nil)
			require.NoError(t, err)
			require.Equal(t, "Current chat mode: orchestrate", chatMode)

			setMode, err := handlers["set_chat_mode"](ctx, json.RawMessage(`{"mode":"plan"}`))
			require.NoError(t, err)
			require.Equal(t, "Chat mode changes are not supported on "+channelName+". "+channelName+" always uses orchestrate mode.", setMode)
		})
	}
}

func TestChannelRuntimeHandlerMapsCoverAdvertisedTools(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Channel Handler Coverage"}
	require.NoError(t, projectRepo.Create(ctx, project))

	tests := []struct {
		name     string
		surface  chatcontrol.Surface
		handlers func() map[string]chatcontrol.RuntimeActionHandler
		runtimes func() []*llmcontracts.RuntimeTools
	}{
		{
			name:    "Slack",
			surface: chatcontrol.SurfaceSlack,
			handlers: func() map[string]chatcontrol.RuntimeActionHandler {
				return (&SlackService{projectRepo: projectRepo}).slackActionHandlers(project.ID, slackActionContext{}, nil)
			},
			runtimes: func() []*llmcontracts.RuntimeTools {
				service := &SlackService{projectRepo: projectRepo}
				return []*llmcontracts.RuntimeTools{
					service.buildSlackActionToolRuntime(project.ID, slackActionContext{}, nil),
					service.buildSlackActionToolRuntime(project.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", ThreadTS: "123.456", UserID: "U1"}, nil),
				}
			},
		},
		{
			name:    "Telegram",
			surface: chatcontrol.SurfaceTelegram,
			handlers: func() map[string]chatcontrol.RuntimeActionHandler {
				return (&TelegramService{projectRepo: projectRepo}).telegramActionHandlers(project.ID, 1, 1, nil)
			},
			runtimes: func() []*llmcontracts.RuntimeTools {
				service := &TelegramService{projectRepo: projectRepo}
				return []*llmcontracts.RuntimeTools{
					service.buildTelegramActionToolRuntime(project.ID, 0, 0, nil),
					service.buildTelegramActionToolRuntime(project.ID, 1, 2, nil),
				}
			},
		},
		{
			name:    "Discord",
			surface: chatcontrol.SurfaceDiscord,
			handlers: func() map[string]chatcontrol.RuntimeActionHandler {
				return (&DiscordService{projectRepo: projectRepo}).discordActionHandlers(project.ID, discordActionContext{}, nil)
			},
			runtimes: func() []*llmcontracts.RuntimeTools {
				service := &DiscordService{projectRepo: projectRepo}
				return []*llmcontracts.RuntimeTools{
					service.buildDiscordActionToolRuntime(project.ID, discordActionContext{}, nil),
					service.buildDiscordActionToolRuntime(project.ID, discordActionContext{ChannelID: "C1", ThreadID: "T1", MessageID: "M1", UserID: "U1"}, nil),
				}
			},
		},
		{
			name:    "Email",
			surface: chatcontrol.SurfaceEmail,
			handlers: func() map[string]chatcontrol.RuntimeActionHandler {
				return (&EmailService{projectRepo: projectRepo}).emailActionHandlers(project.ID, "user@example.com")
			},
			runtimes: func() []*llmcontracts.RuntimeTools {
				return []*llmcontracts.RuntimeTools{(&EmailService{projectRepo: projectRepo}).buildEmailActionToolRuntime(project.ID, "user@example.com")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers := tt.handlers()
			if tt.surface == chatcontrol.SurfaceEmail {
				expectedDefs := make([]llmcontracts.RuntimeToolDefinition, 0, len(handlers))
				for _, def := range actionToolDefinitions(tt.surface, true) {
					if _, ok := handlers[def.Name]; ok {
						expectedDefs = append(expectedDefs, def)
					}
				}
				for _, runtime := range tt.runtimes() {
					require.NotNil(t, runtime)
					require.Equal(t, expectedDefs, runtime.Definitions)
					definitionNames := make(map[string]struct{}, len(runtime.Definitions))
					for _, def := range runtime.Definitions {
						definitionNames[def.Name] = struct{}{}
						require.Contains(t, handlers, def.Name, "advertised email runtime tool must have a handler")
					}
					require.NotContains(t, definitionNames, "create_task")

					_, handled, blocked, err := runtime.Executor(ctx, "save_automation", nil)
					require.NoError(t, err)
					require.False(t, handled)
					require.False(t, blocked)
				}
			} else {
				expectedDefs := actionToolDefinitions(tt.surface, true)
				for _, runtime := range tt.runtimes() {
					require.NotNil(t, runtime)
					require.Equal(t, expectedDefs, runtime.Definitions)
					definitionNames := make(map[string]struct{}, len(runtime.Definitions))
					for _, def := range runtime.Definitions {
						definitionNames[def.Name] = struct{}{}
					}
					require.Contains(t, definitionNames, "view_task_thread")
					require.Contains(t, definitionNames, "send_to_task")
					require.Contains(t, definitionNames, "preview_automation_description")
					require.Contains(t, definitionNames, "save_automation")

					currentProject, handled, blocked, err := runtime.Executor(ctx, "get_current_project", nil)
					require.NoError(t, err)
					require.True(t, handled)
					require.False(t, blocked)
					require.Equal(t, "Current project: Channel Handler Coverage (id: "+project.ID+")", currentProject)

					_, handled, blocked, err = runtime.Executor(ctx, "save_automation", nil)
					require.NoError(t, err)
					require.False(t, handled)
					require.False(t, blocked)
				}
			}

			currentProject, err := handlers["get_current_project"](ctx, nil)
			require.NoError(t, err)
			require.Equal(t, "Current project: Channel Handler Coverage (id: "+project.ID+")", currentProject)

			chatMode, err := handlers["get_chat_mode"](ctx, nil)
			require.NoError(t, err)
			require.Equal(t, "Current chat mode: orchestrate", chatMode)

			setMode, err := handlers["set_chat_mode"](ctx, json.RawMessage(`{"mode":"plan"}`))
			require.NoError(t, err)
			require.Equal(t, "Chat mode changes are not supported on "+tt.name+". "+tt.name+" always uses orchestrate mode.", setMode)
		})
	}
}

func channelRuntimeGenericFallbackTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "memory_view", "preview_automation_description", "save_automation", "run_automation_now", "pause_automation", "resume_automation":
		return true
	default:
		return false
	}
}

func TestAutomationNotificationCreationAllowsMissingIdempotencyKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Native existing-work dedupe"}
	require.NoError(t, repository.NewProjectRepo(db).Create(ctx, project))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, AlertSvc: alertSvc})
	automationCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, OriginTask: true})

	createdJSON, err := handlers["create_notification"](automationCtx, json.RawMessage(`{
		"type":"bug_suggestion",
		"title":"Existing-work checked notification"
	}`))
	require.NoError(t, err)
	require.Contains(t, createdJSON, "Existing-work checked notification")
}

func TestAlertRuntimeCreateNotificationIgnoresHiddenIdempotencyKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Runtime hidden idempotency"}
	require.NoError(t, repository.NewProjectRepo(db).Create(ctx, project))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, AlertSvc: alertSvc})
	input := json.RawMessage(`{"type":"bug_suggestion","title":"Hidden key notification","idempotency_key":"hidden-runtime-key"}`)

	firstJSON, err := handlers["create_notification"](ctx, input)
	require.NoError(t, err)
	secondJSON, err := handlers["create_notification"](ctx, input)
	require.NoError(t, err)
	var first, second struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(firstJSON), &first))
	require.NoError(t, json.Unmarshal([]byte(secondJSON), &second))
	require.NotEmpty(t, first.Notification.ID)
	require.NotEmpty(t, second.Notification.ID)
	require.NotEqual(t, first.Notification.ID, second.Notification.ID)
	require.Empty(t, first.Notification.IdempotencyKey)
	require.Empty(t, second.Notification.IdempotencyKey)
	var storedWithHiddenKey int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE project_id = ? AND idempotency_key = ?`, project.ID, "hidden-runtime-key").Scan(&storedWithHiddenKey))
	require.Zero(t, storedWithHiddenKey)
}

func TestAlertRuntimeSuggestionApprovalClaimAndTaskLinkage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Native SDLC"}
	foreign := &models.Project{Name: "Foreign SDLC"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, foreign))
	caller := &models.Task{ProjectID: project.ID, Title: "Scheduled notification inbox", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: alertSvc})

	createInput := json.RawMessage(`{"project_id":"` + project.ID + `","type":"product_suggestion","title":"Add approval inbox","message":"Review this","body":"Detailed implementation context","metadata":{"component":"alerts"}}`)
	createdJSON, err := handlers["create_notification"](ctx, createInput)
	require.NoError(t, err)
	var created struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
	require.NotEmpty(t, created.Notification.ID)
	require.Equal(t, project.ID, created.Notification.ProjectID)
	require.Equal(t, models.AlertDecisionPending, created.Notification.DecisionState)
	require.Equal(t, caller.ID, *created.Notification.SourceTaskID)

	_, err = handlers["list_alerts"](ctx, json.RawMessage(`{"project_id":"`+foreign.ID+`"}`))
	require.ErrorContains(t, err, "outside the caller's authorized project")
	listJSON, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"pending","limit":1,"offset":0}`))
	require.NoError(t, err)
	require.Contains(t, listJSON, created.Notification.ID)
	require.Contains(t, listJSON, `"next_offset":1`)
	require.NotContains(t, listJSON, `"body"`)
	require.NotContains(t, listJSON, `"metadata"`)
	var listed struct {
		Notifications []map[string]any `json:"notifications"`
	}
	require.NoError(t, json.Unmarshal([]byte(listJSON), &listed))
	require.Len(t, listed.Notifications, 1)
	for _, key := range []string{"id", "project_id", "type", "severity", "title", "message", "is_read", "decision_state", "processing_state", "source", "created_at", "updated_at"} {
		require.Contains(t, listed.Notifications[0], key)
	}
	detailJSON, err := handlers["get_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, detailJSON, "Detailed implementation context")
	require.Contains(t, detailJSON, `"body"`)
	require.Contains(t, detailJSON, `"metadata"`)

	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, created.Notification.ID, models.AlertDecisionApproved))
	approvedJSON, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"approved","processing_state":"unclaimed"}`))
	require.NoError(t, err)
	require.Contains(t, approvedJSON, created.Notification.ID)
	claimJSON, err := handlers["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`","lease_seconds":300}`))
	require.NoError(t, err)
	require.Contains(t, claimJSON, `"processing_state":"claimed"`)

	createTaskInput := json.RawMessage(`{"alert_id":"` + created.Notification.ID + `","title":"Implement approval inbox","prompt":"Implement the approved suggestion and leave merge/release to human review.","goal":"Complete the approved change with focused regression coverage.","priority":3,"tag":"feature"}`)
	taskJSON, err := handlers["create_alert_implementation_task"](ctx, createTaskInput)
	require.NoError(t, err)
	var linked struct {
		ImplementationTaskID string `json:"implementation_task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(taskJSON), &linked))
	require.NotEmpty(t, linked.ImplementationTaskID)
	implementationGoal, err := repository.NewTaskGoalRepo(db).GetByTaskID(ctx, linked.ImplementationTaskID)
	require.NoError(t, err)
	require.NotNil(t, implementationGoal)
	require.Equal(t, "Complete the approved change with focused regression coverage.", implementationGoal.Objective)
	secondTaskJSON, err := handlers["create_alert_implementation_task"](ctx, createTaskInput)
	require.NoError(t, err)
	var linkedAgain struct {
		ImplementationTaskID string `json:"implementation_task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(secondTaskJSON), &linkedAgain))
	require.Equal(t, linked.ImplementationTaskID, linkedAgain.ImplementationTaskID)

	completeJSON, err := handlers["complete_alert_processing"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, completeJSON, `"processing_state":"completed"`)
	final, err := alertSvc.GetByID(ctx, project.ID, created.Notification.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertProcessingCompleted, final.ProcessingState)
	require.Equal(t, linked.ImplementationTaskID, *final.ImplementationTaskID)
}

func TestAlertRuntimeClaimAlertValidatesLeaseSeconds(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Runtime Claim Lease Validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	caller := &models.Task{ProjectID: project.ID, Title: "Scheduled notification inbox", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	alertRepo := repository.NewAlertRepo(db)
	alertSvc := NewAlertService(alertRepo, nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: alertSvc})

	createApproved := func(t *testing.T, title string) models.Alert {
		t.Helper()
		createdJSON, err := handlers["create_notification"](ctx, json.RawMessage(`{"type":"bug_suggestion","title":"`+title+`"}`))
		require.NoError(t, err)
		var created struct {
			Notification models.Alert `json:"notification"`
		}
		require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
		require.NoError(t, alertSvc.SetDecision(ctx, project.ID, created.Notification.ID, models.AlertDecisionApproved))
		return created.Notification
	}

	for _, leaseSeconds := range []int{-1, 0, 90000} {
		t.Run(fmt.Sprintf("rejects explicit %d", leaseSeconds), func(t *testing.T) {
			alert := createApproved(t, fmt.Sprintf("Invalid lease %d", leaseSeconds))
			_, err := handlers["claim_alert"](ctx, json.RawMessage(fmt.Sprintf(`{"alert_id":"%s","lease_seconds":%d}`, alert.ID, leaseSeconds)))
			require.ErrorContains(t, err, "lease_seconds must be between 1 and 86400")

			unclaimed, err := alertSvc.GetByID(ctx, project.ID, alert.ID)
			require.NoError(t, err)
			require.Equal(t, models.AlertProcessingUnclaimed, unclaimed.ProcessingState)
			require.Empty(t, unclaimed.Claimant)
			require.Nil(t, unclaimed.ClaimedAt)
			require.Nil(t, unclaimed.ClaimExpiresAt)
		})
	}

	claimAndAssertDuration := func(t *testing.T, alertID string, input json.RawMessage, expected time.Duration) {
		t.Helper()
		_, err := handlers["claim_alert"](ctx, input)
		require.NoError(t, err)
		claimed, err := alertSvc.GetByID(ctx, project.ID, alertID)
		require.NoError(t, err)
		require.Equal(t, models.AlertProcessingClaimed, claimed.ProcessingState)
		require.NotNil(t, claimed.ClaimedAt)
		require.NotNil(t, claimed.ClaimExpiresAt)
		require.WithinDuration(t, claimed.ClaimedAt.Add(expected), *claimed.ClaimExpiresAt, time.Second)
	}

	omitted := createApproved(t, "Omitted lease uses default")
	claimAndAssertDuration(t, omitted.ID, json.RawMessage(`{"alert_id":"`+omitted.ID+`"}`), 30*time.Minute)

	oneSecond := createApproved(t, "One second lease")
	claimAndAssertDuration(t, oneSecond.ID, json.RawMessage(`{"alert_id":"`+oneSecond.ID+`","lease_seconds":1}`), time.Second)

	oneDay := createApproved(t, "One day lease")
	claimAndAssertDuration(t, oneDay.ID, json.RawMessage(`{"alert_id":"`+oneDay.ID+`","lease_seconds":86400}`), 24*time.Hour)
}

func TestNativeInboxCollectsAllPagesBeforeShrinkingEligibleSet(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Native inbox pagination"}
	require.NoError(t, projectRepo.Create(ctx, project))
	caller := &models.Task{ProjectID: project.ID, Title: "Approved inbox", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: alertSvc})

	createdIDs := make(map[string]struct{}, 3)
	for i := 0; i < 3; i++ {
		createdJSON, err := handlers["create_notification"](ctx, json.RawMessage(`{"type":"bug_suggestion","title":"Finding `+string(rune('A'+i))+`"}`))
		require.NoError(t, err)
		var created struct {
			Notification models.Alert `json:"notification"`
		}
		require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
		require.NoError(t, alertSvc.SetDecision(ctx, project.ID, created.Notification.ID, models.AlertDecisionApproved))
		createdIDs[created.Notification.ID] = struct{}{}
	}

	listPage := func(offset int) []models.Alert {
		listedJSON, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"approved","implementation_task_linked":false,"limit":2,"offset":`+fmt.Sprintf("%d", offset)+`}`))
		require.NoError(t, err)
		var listed struct {
			Notifications []models.Alert `json:"notifications"`
		}
		require.NoError(t, json.Unmarshal([]byte(listedJSON), &listed))
		return listed.Notifications
	}

	firstPage := listPage(0)
	require.Len(t, firstPage, 2)
	snapshotRemainder := listPage(2)
	require.Len(t, snapshotRemainder, 1, "the inbox must collect later pages before linking changes the filtered set")
	for _, alert := range firstPage {
		_, err := handlers["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+alert.ID+`"}`))
		require.NoError(t, err)
		_, err = handlers["create_alert_implementation_task"](ctx, json.RawMessage(`{"alert_id":"`+alert.ID+`","title":"Implement finding `+alert.ID+`","prompt":"Implement the approved finding."}`))
		require.NoError(t, err)
		delete(createdIDs, alert.ID)
	}

	require.Empty(t, listPage(2), "advancing the offset after mutation skips the remaining row")
	remaining := listPage(0)
	require.Len(t, remaining, 1)
	require.Equal(t, snapshotRemainder[0].ID, remaining[0].ID, "the pre-mutation snapshot must retain the otherwise skipped notification ID")
	_, ok := createdIDs[snapshotRemainder[0].ID]
	require.True(t, ok)
}

func TestAlertRuntimeCreateAlertPreservesOperationalContract(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Operational Alerts"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Source task", Prompt: "run", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: task.ID, AlertSvc: alertSvc, TaskRepo: taskRepo})

	result, err := handlers["create_alert"](ctx, json.RawMessage(`{"title":"Legacy operational alert","task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	var payload struct {
		Alert models.Alert `json:"alert"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &payload))
	require.NotEmpty(t, payload.Alert.ID)
	require.Equal(t, models.AlertCustom, payload.Alert.Type)
	require.Equal(t, models.AlertDecisionNotRequired, payload.Alert.DecisionState)
	require.Equal(t, models.AlertProcessingNotApplicable, payload.Alert.ProcessingState)
	require.NotNil(t, payload.Alert.TaskID)
	require.Equal(t, task.ID, *payload.Alert.TaskID)

	foreignProject := &models.Project{Name: "Foreign Operational Alerts"}
	require.NoError(t, projectRepo.Create(ctx, foreignProject))
	foreignTask := &models.Task{ProjectID: foreignProject.ID, Title: "Foreign source", Prompt: "run", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, foreignTask))
	_, err = handlers["create_alert"](ctx, json.RawMessage(`{"title":"Cross-project reference","task_id":"`+foreignTask.ID+`"}`))
	require.ErrorContains(t, err, "outside the caller's authorized project context")
}

func TestAlertRuntimeFiltersPaginationAuthorizationAndRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Runtime Project"}
	foreign := &models.Project{Name: "Foreign Runtime Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, foreign))
	caller := &models.Task{ProjectID: project.ID, Title: "Scanner", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	foreignCaller := &models.Task{ProjectID: foreign.ID, Title: "Foreign scanner", Prompt: "scan", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	require.NoError(t, taskRepo.Create(ctx, foreignCaller))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: alertSvc})
	foreignHandlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: foreign.ID, CallerTaskID: foreignCaller.ID, Source: "scheduled_task", AlertSvc: alertSvc})

	create := func(title, notificationType, source string) models.Alert {
		out, err := handlers["create_notification"](ctx, json.RawMessage(`{"type":"`+notificationType+`","title":"`+title+`","source":"`+source+`"}`))
		require.NoError(t, err)
		var payload struct {
			Notification models.Alert `json:"notification"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &payload))
		return payload.Notification
	}
	first := create("First", "product", "agent-a")
	second := create("Second", "security", "agent-b")
	third := create("Third", "product", "agent-a")
	require.NoError(t, alertSvc.MarkRead(ctx, project.ID, first.ID))
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, first.ID, models.AlertDecisionApproved))
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, second.ID, models.AlertDecisionRejected))

	approvedAcrossReadStates, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"approved","implementation_task_linked":false}`))
	require.NoError(t, err)
	require.Contains(t, approvedAcrossReadStates, first.ID, "omitting read must keep a read approved notification eligible")

	pageOne, err := handlers["list_alerts"](ctx, json.RawMessage(`{"limit":2,"offset":0}`))
	require.NoError(t, err)
	pageTwo, err := handlers["list_alerts"](ctx, json.RawMessage(`{"limit":2,"offset":2}`))
	require.NoError(t, err)
	require.NotEqual(t, pageOne, pageTwo)
	for _, id := range []string{first.ID, second.ID, third.ID} {
		require.Equal(t, 1, strings.Count(pageOne+pageTwo, id), "notification %s should appear in exactly one stable page", id)
	}
	repeated, err := handlers["list_alerts"](ctx, json.RawMessage(`{"limit":2,"offset":0}`))
	require.NoError(t, err)
	require.Equal(t, pageOne, repeated)

	filtered, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"pending","processing_state":"unclaimed","type":"product","source":"agent-a","read":false,"implementation_task_linked":false}`))
	require.NoError(t, err)
	require.Contains(t, filtered, third.ID)
	require.NotContains(t, filtered, first.ID)
	require.NotContains(t, filtered, second.ID)

	for name, input := range map[string]json.RawMessage{
		"get_alert":                        json.RawMessage(`{"alert_id":"` + third.ID + `"}`),
		"claim_alert":                      json.RawMessage(`{"alert_id":"` + third.ID + `"}`),
		"create_alert_implementation_task": json.RawMessage(`{"alert_id":"` + third.ID + `","title":"x","prompt":"y"}`),
		"link_alert_implementation_task":   json.RawMessage(`{"alert_id":"` + third.ID + `","task_id":"` + foreignCaller.ID + `"}`),
		"complete_alert_processing":        json.RawMessage(`{"alert_id":"` + third.ID + `"}`),
		"fail_alert_processing":            json.RawMessage(`{"alert_id":"` + third.ID + `","message":"failed"}`),
		"release_alert_claim":              json.RawMessage(`{"alert_id":"` + third.ID + `"}`),
	} {
		_, err := foreignHandlers[name](ctx, input)
		require.Error(t, err, "%s must reject a foreign-project notification", name)
	}

	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, third.ID, models.AlertDecisionApproved))
	_, err = handlers["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`"}`))
	require.NoError(t, err)
	_, err = handlers["fail_alert_processing"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`","message":"temporary"}`))
	require.NoError(t, err)
	_, err = handlers["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`"}`))
	require.NoError(t, err)
	_, err = handlers["release_alert_claim"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`"}`))
	require.NoError(t, err)

	_, err = handlers["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`"}`))
	require.NoError(t, err)
	implementation := &models.Task{ProjectID: project.ID, Title: "Existing implementation", Prompt: "implement", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, implementation))
	_, err = handlers["link_alert_implementation_task"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`","task_id":"`+implementation.ID+`"}`))
	require.NoError(t, err)
	_, err = handlers["complete_alert_processing"](ctx, json.RawMessage(`{"alert_id":"`+third.ID+`"}`))
	require.NoError(t, err)
	final, err := alertSvc.GetByID(ctx, project.ID, third.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertProcessingCompleted, final.ProcessingState)
	require.Equal(t, implementation.ID, *final.ImplementationTaskID)
}

func TestRunChannelChatFirstTurnBuildsRuntimeAfterPersistingCallerTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Channel Runtime Identity"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Channel Agent", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	task := &models.Task{Title: "Channel chat"}
	var factoryTaskID string
	var runReq ChannelChatRunRequest

	handled, _ := runChannelChatFirstTurn(ctx, channelChatIngressFirstTurnOptions{
		Platform:  "test",
		ProjectID: project.ID,
		Message:   "review alerts",
		Source:    models.TaskOriginTelegram,
		Task:      task,
		Agent:     agent,
		TaskRepo:  taskRepo,
		ExecRepo:  execRepo,
		RuntimeToolsForTask: func(taskID string) *llmcontracts.RuntimeTools {
			factoryTaskID = taskID
			return &llmcontracts.RuntimeTools{}
		},
		ChannelChatRunner: func(_ context.Context, req ChannelChatRunRequest) { runReq = req },
	})
	require.True(t, handled)
	require.NotEmpty(t, task.ID)
	require.Equal(t, task.ID, factoryTaskID)
	require.Equal(t, task.ID, runReq.TaskID)
	require.NotNil(t, runReq.RuntimeTools)
}

func TestTaskControlRuntimeOmitsImplementationTaskToolsForLoopAuditor(t *testing.T) {
	loopAuditor := models.Task{
		ProjectID:  "project",
		Category:   models.CategoryScheduled,
		CreatedVia: "automation:github-sdlc:auditor",
	}

	runtime := (&LLMService{}).taskControlRuntimeTools(loopAuditor)
	require.NotNil(t, runtime)
	for _, tool := range []string{
		"create_task",
		"create_swarm_task",
		"execute_tasks",
		"create_alert_implementation_task",
		"link_alert_implementation_task",
	} {
		require.Falsef(t, runtime.HasDefinition(tool), "Loop Auditor must not expose %s", tool)
	}
	for _, tool := range []string{
		"list_tasks",
		"set_task_goal",
		"clear_task_goal",
		"get_task_goal",
		"pause_task_goal",
		"resume_task_goal",
		"list_schedules",
		"schedule_task",
		"delete_schedule",
		"modify_schedule",
		"create_alert",
		"create_notification",
		"list_alerts",
		"get_alert",
		"claim_alert",
		"complete_alert_processing",
		"fail_alert_processing",
		"release_alert_claim",
		"list_capabilities",
	} {
		require.Truef(t, runtime.HasDefinition(tool), "Loop Auditor must retain %s", tool)
	}
}

func TestTaskControlRuntimeScheduledAutomationTaskRemainsGenericAndExposesExistingNotificationDiscovery(t *testing.T) {
	finder := models.Task{
		ProjectID:  "project",
		Category:   models.CategoryScheduled,
		CreatedVia: "automation:native:optimization_finder",
	}

	runtime := (&LLMService{}).taskControlRuntimeTools(finder)
	require.NotNil(t, runtime)
	for _, tool := range []string{
		"create_notification",
		"list_existing_automation_notifications",
		"get_alert",
		"list_alerts",
		"list_tasks",
		"list_capabilities",
		"create_alert",
		"claim_alert",
		"create_alert_implementation_task",
		"execute_tasks",
	} {
		require.Truef(t, runtime.HasDefinition(tool), "scheduled Automation task must retain generic tool %s", tool)
	}
}

func TestTaskControlRuntimeNativeInboxKeepsApprovalProcessingTools(t *testing.T) {
	inbox := models.Task{
		ProjectID:  "project",
		Category:   models.CategoryScheduled,
		CreatedVia: "automation:native:inbox",
	}

	runtime := (&LLMService{}).taskControlRuntimeTools(inbox)
	require.NotNil(t, runtime)
	for _, tool := range []string{"list_alerts", "claim_alert", "create_alert_implementation_task", "execute_tasks"} {
		require.Truef(t, runtime.HasDefinition(tool), "Native inbox must retain %s", tool)
	}
}

func TestTaskControlRuntimeExposesExecuteTasksAndStartsExactBacklogTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Scheduled Inbox Runtime"}
	require.NoError(t, projectRepo.Create(ctx, project))
	inboxTask := &models.Task{ProjectID: project.ID, Title: "Approved inbox", Prompt: "process approvals", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, inboxTask))
	implementationTask := &models.Task{ProjectID: project.ID, Title: "Approved implementation", Prompt: "implement", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, implementationTask))
	workerSvc := NewWorkerService(nil, 1, projectRepo)
	atomic.StoreInt32(&workerSvc.totalRunning, 1) // Keep the submitted task queued; this test verifies admission, not LLM execution.
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	svc := &LLMService{taskRepo: taskRepo, taskSvc: taskSvc}

	runtime := svc.taskControlRuntimeTools(*inboxTask)
	require.NotNil(t, runtime)
	require.True(t, runtime.HasDefinition("execute_tasks"))
	payload, err := json.Marshal(TaskExecutionRequest{TaskID: implementationTask.ID})
	require.NoError(t, err)
	output, handled, isErr, err := runtime.Executor(ctx, "execute_tasks", payload)
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, implementationTask.ID)
	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, implementationTask.ID, submitted.ID)
	default:
		t.Fatal("expected exact implementation task to be submitted")
	}
	updated, err := taskRepo.GetByID(ctx, implementationTask.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, updated.Category)
	require.Equal(t, models.StatusPending, updated.Status)
}

func TestTaskControlRuntimeListAlertsUsesPersistedProjectAcrossReadStates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Scheduled Alert Inbox"}
	foreign := &models.Project{Name: "Foreign Alert Inbox"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, foreign))
	inboxTask := &models.Task{ProjectID: project.ID, Title: "Approved inbox", Prompt: "process approvals", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, inboxTask))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	createNotification := func(title string, read bool) models.Alert {
		created, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, Scope: models.AlertScopeProject, Type: "product_suggestion", Title: title, Source: "producer"})
		require.NoError(t, err)
		require.NoError(t, alertSvc.SetDecision(ctx, project.ID, created.ID, models.AlertDecisionApproved))
		if read {
			require.NoError(t, alertSvc.MarkRead(ctx, project.ID, created.ID))
		}
		return *created
	}
	readNotification := createNotification("Read approval", true)
	unreadNotification := createNotification("Unread approval", false)
	svc := &LLMService{taskRepo: taskRepo, alertSvc: alertSvc}

	runtime := svc.taskControlRuntimeTools(*inboxTask)
	require.NotNil(t, runtime)
	var listSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	for _, definition := range runtime.Definitions {
		if definition.Name == "list_alerts" {
			require.NoError(t, json.Unmarshal(definition.Parameters, &listSchema))
			break
		}
	}
	require.NotEmpty(t, listSchema.Properties)
	require.NotContains(t, listSchema.Properties, "project_id")
	require.NotContains(t, listSchema.Properties, "read")

	output, handled, isErr, err := runtime.Executor(ctx, "list_alerts", json.RawMessage(`{"project_id":"`+foreign.ID+`","read":false,"decision_state":"approved","implementation_task_linked":false,"limit":50,"offset":0}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, readNotification.ID)
	require.Contains(t, output, unreadNotification.ID)
	require.Contains(t, output, `"project_id":"`+project.ID+`"`)

	ordinaryRuntime := svc.taskControlRuntimeTools(models.Task{ProjectID: project.ID, Category: models.CategoryActive})
	require.NotNil(t, ordinaryRuntime)
	for _, definition := range ordinaryRuntime.Definitions {
		if definition.Name != "list_alerts" {
			continue
		}
		var ordinarySchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(definition.Parameters, &ordinarySchema))
		require.Contains(t, ordinarySchema.Properties, "project_id")
		require.Contains(t, ordinarySchema.Properties, "read")
		return
	}
	t.Fatal("ordinary task runtime is missing list_alerts")
}

func TestTaskControlRuntimeExposesCreateNotificationForScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Scheduled Notification Runtime"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Suggestion producer", Prompt: "inspect", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	svc := &LLMService{taskRepo: taskRepo, alertSvc: alertSvc}

	runtime := svc.taskControlRuntimeTools(*task)
	require.NotNil(t, runtime)
	require.True(t, runtime.HasDefinition("create_notification"))
	output, handled, isErr, err := runtime.Executor(ctx, "create_notification", json.RawMessage(`{"type":"maintenance_suggestion","title":"Review maintenance"}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	require.Contains(t, output, task.ID)
	alerts, err := alertSvc.ListFiltered(ctx, project.ID, models.AlertListFilter{DecisionState: models.AlertDecisionPending})
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, task.ID, *alerts[0].SourceTaskID)
}

func TestRunChannelTaskThreadSendStartsDirectFollowupWithReplyContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Task Thread Channel"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Task Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "Follow target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	var runReq ChannelTaskRunRequest
	result := runChannelTaskThreadSend(ctx, task, channelTaskThreadSendOptions{
		Platform:          "test",
		ProjectID:         project.ID,
		Message:           "continue",
		Source:            models.TaskOriginTelegram,
		Surface:           "test-surface",
		ReplyContext:      ChannelReplyContext{Source: models.TaskOriginTelegram, TelegramChatID: 123},
		TaskRepo:          taskRepo,
		ExecRepo:          execRepo,
		LLMConfigRepo:     llmConfigRepo,
		ChannelTaskRunner: func(_ context.Context, req ChannelTaskRunRequest) { runReq = req },
		CompleteExecution: func(context.Context, string, string, string, string, int, int64) {},
	})
	require.Contains(t, result, "Sent message to task")
	require.Equal(t, task.ID, runReq.TaskID)
	require.Equal(t, "continue", runReq.Message)
	require.Equal(t, int64(123), runReq.ReplyContext.TelegramChatID)
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusQueued, updated.Status)
	require.Equal(t, models.CategoryActive, updated.Category)
}

func TestBuildChannelGoalActionHandlersUseSharedGoalRuntime(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Goal Actions"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Goal target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	handlers := buildChannelGoalActionHandlers(channelGoalActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, TaskGoalSvc: NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)})
	payload, err := json.Marshal(TaskGoalRuntimeToolInput{Title: "Goal target", Goal: "Finish the shared refactor"})
	require.NoError(t, err)

	setResult, err := handlers["set_task_goal"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, setResult, "Finish the shared refactor")
	require.Contains(t, setResult, task.ID)

	getResult, err := handlers["get_task_goal"](ctx, []byte(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, getResult, "Finish the shared refactor")
	require.Contains(t, getResult, task.ID)

	pauseResult, err := handlers["pause_task_goal"](ctx, []byte(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, pauseResult, string(models.TaskGoalStatusPaused))

	resumeResult, err := handlers["resume_task_goal"](ctx, []byte(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, resumeResult, string(models.TaskGoalStatusActive))
}

func TestBuildChannelUtilityActionHandlersScheduleTaskAndModifyUseSharedLogic(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Utility Actions"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Scheduled target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ScheduleRepo: scheduleRepo})
	for _, tt := range []struct {
		payload string
		want    string
	}{
		{payload: `{"title":"Scheduled target","time":"09:30junk","repeat":"daily"}`, want: "Invalid time"},
		{payload: `{"title":"Scheduled target","time":"09:30:45","repeat":"daily"}`, want: "Invalid time"},
		{payload: `{"title":"Scheduled target","time":"09:30","repeat":"yearly"}`, want: "Unknown repeat type"},
		{payload: `{"title":"Scheduled target","time":"09:30","repeat":"weekly","days":["monday"]}`, want: "Invalid weekly days"},
	} {
		out, err := handlers["schedule_task"](ctx, json.RawMessage(tt.payload))
		require.NoError(t, err)
		require.Contains(t, out, tt.want)
	}
	beforeInvalidCreate, err := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, beforeInvalidCreate)

	oversizedCreateOut, err := handlers["schedule_task"](ctx, json.RawMessage(`{"title":"Scheduled target","time":"09:30","repeat":"seconds","interval":366}`))
	require.NoError(t, err)
	require.Contains(t, oversizedCreateOut, "between 1 and 365")
	beforeCreate, err := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, beforeCreate)

	scheduleOut, err := handlers["schedule_task"](ctx, json.RawMessage(`{"title":"Scheduled target","time":"09:30","repeat":"weekly","days":["mon"],"interval":2}`))
	require.NoError(t, err)
	require.Contains(t, scheduleOut, "Scheduled task")
	require.Contains(t, scheduleOut, "every 2 weeks on mon")
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, updatedTask.Category)
	require.Equal(t, models.StatusPending, updatedTask.Status)
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, 2, schedules[0].RepeatInterval)
	require.Equal(t, time.Monday, schedules[0].RunAt.Local().Weekday())
	require.True(t, schedules[0].ClearContextOnStart)
	require.NotNil(t, schedules[0].NextRun)
	require.WithinDuration(t, schedules[0].RunAt, *schedules[0].NextRun, time.Second)

	originalRunAt := schedules[0].RunAt
	for _, tt := range []struct {
		payload string
		want    string
	}{
		{payload: `{"schedule_id":"` + schedules[0].ID + `","time":"09:30junk"}`, want: "Invalid time"},
		{payload: `{"schedule_id":"` + schedules[0].ID + `","time":"09:30:45"}`, want: "Invalid time"},
		{payload: `{"schedule_id":"` + schedules[0].ID + `","repeat":"yearly"}`, want: "Unknown repeat type"},
		{payload: `{"schedule_id":"` + schedules[0].ID + `","days":["monday"]}`, want: "Invalid weekly days"},
	} {
		out, err := handlers["modify_schedule"](ctx, json.RawMessage(tt.payload))
		require.NoError(t, err)
		require.Contains(t, out, tt.want)
		unchanged, getErr := scheduleRepo.GetByID(ctx, schedules[0].ID)
		require.NoError(t, getErr)
		require.Equal(t, originalRunAt, unchanged.RunAt)
		require.Equal(t, models.RepeatWeekly, unchanged.RepeatType)
		require.Equal(t, 2, unchanged.RepeatInterval)
	}

	oversizedModifyOut, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":366}`))
	require.NoError(t, err)
	require.Contains(t, oversizedModifyOut, "between 1 and 365")
	unchanged, err := scheduleRepo.GetByID(ctx, schedules[0].ID)
	require.NoError(t, err)
	require.Equal(t, 2, unchanged.RepeatInterval)

	modifyOut, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","time":"10:45","enabled":false,"clear_context_on_start":false}`))
	require.NoError(t, err)
	require.Contains(t, modifyOut, "Updated schedule")
	require.Contains(t, modifyOut, "time→10:45")
	require.Contains(t, modifyOut, "enabled→false")
	modified, err := scheduleRepo.GetByID(ctx, schedules[0].ID)
	require.NoError(t, err)
	require.False(t, modified.ClearContextOnStart)
	require.False(t, modified.Enabled)

	lowerBoundOut, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":0}`))
	require.NoError(t, err)
	require.Contains(t, lowerBoundOut, "between 1 and 365")
	modifyDaysOut, err := handlers["modify_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`","interval":365,"days":["fri"],"enabled":true,"clear_context_on_start":true}`))
	require.NoError(t, err)
	require.Contains(t, modifyDaysOut, "Updated schedule")
	modified, err = scheduleRepo.GetByID(ctx, schedules[0].ID)
	require.NoError(t, err)
	require.Equal(t, 365, modified.RepeatInterval)
	require.Equal(t, time.Friday, modified.RunAt.Local().Weekday())
	require.True(t, modified.Enabled)
	require.True(t, modified.ClearContextOnStart)
	require.NotNil(t, modified.NextRun)
	require.WithinDuration(t, modified.RunAt, *modified.NextRun, time.Second)

	foreign := &models.Project{Name: "Foreign Utility Actions"}
	require.NoError(t, projectRepo.Create(ctx, foreign))
	foreignTask := &models.Task{ProjectID: foreign.ID, Title: "Foreign scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, foreignTask))
	foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, foreignSchedule))
	for tool, payload := range map[string]string{
		"schedule_task":   `{"task_id":"` + foreignTask.ID + `","time":"09:30"}`,
		"modify_schedule": `{"schedule_id":"` + foreignSchedule.ID + `","enabled":false}`,
		"delete_schedule": `{"schedule_id":"` + foreignSchedule.ID + `"}`,
	} {
		out, err := handlers[tool](ctx, json.RawMessage(payload))
		require.NoError(t, err)
		require.Contains(t, out, "different project")
	}

	deleteOut, err := handlers["delete_schedule"](ctx, json.RawMessage(`{"schedule_id":"`+schedules[0].ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, deleteOut, "Deleted schedule")
	remaining, err := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, remaining)
	updatedTask, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, updatedTask.Category)
}

func TestBuildChannelUtilityActionHandlersAutomationReadsRejectForeignProject(t *testing.T) {
	ctx := context.Background()
	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: "project-current"})

	_, err := handlers["list_automations"](ctx, json.RawMessage(`{"project_id":"project-foreign"}`))
	require.ErrorContains(t, err, `project_id "project-foreign" is outside the caller's authorized project context`)

	_, err = handlers["get_automation"](ctx, json.RawMessage(`{"automation_id":"automation-1","project_id":"project-foreign"}`))
	require.ErrorContains(t, err, `project_id "project-foreign" is outside the caller's authorized project context`)
}

func TestBuildChannelUtilityActionHandlersListSchedulesDiscovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule Discovery"}
	require.NoError(t, projectRepo.Create(ctx, project))
	other := &models.Project{Name: "Other Discovery"}
	require.NoError(t, projectRepo.Create(ctx, other))

	now := time.Now().UTC().Truncate(time.Second)
	mkTask := func(projectID, title string) *models.Task {
		task := &models.Task{ProjectID: projectID, Title: title, Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
		require.NoError(t, taskRepo.Create(ctx, task))
		return task
	}
	mkSched := func(taskID string, repeat models.RepeatType, enabled bool, runAt time.Time) *models.Schedule {
		s := &models.Schedule{TaskID: taskID, RunAt: runAt, RepeatType: repeat, RepeatInterval: 1, Enabled: enabled, ClearContextOnStart: true}
		require.NoError(t, scheduleRepo.Create(ctx, s))
		return s
	}

	alphaTask := mkTask(project.ID, "Alpha nightly")
	betaTask := mkTask(project.ID, "Beta weekly")
	foreignTask := mkTask(other.ID, "Foreign task")
	alphaEnabled := mkSched(alphaTask.ID, models.RepeatDaily, true, now.Add(2*time.Hour))
	alphaDisabled := mkSched(alphaTask.ID, models.RepeatHours, false, now.Add(time.Hour))
	betaEnabled := mkSched(betaTask.ID, models.RepeatWeekly, true, now.Add(3*time.Hour))
	foreignSched := mkSched(foreignTask.ID, models.RepeatDaily, true, now.Add(time.Hour))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ScheduleRepo: scheduleRepo})

	// Project isolation: only default-project schedule IDs appear.
	allOut, err := handlers["list_schedules"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, allOut, `"ok":true`)
	require.Contains(t, allOut, `"total":3`)
	require.Contains(t, allOut, alphaEnabled.ID)
	require.Contains(t, allOut, betaEnabled.ID)
	require.Contains(t, allOut, "Alpha nightly")
	require.NotContains(t, allOut, foreignSched.ID)

	// Enabled filter.
	enabledOut, err := handlers["list_schedules"](ctx, json.RawMessage(`{"enabled":true}`))
	require.NoError(t, err)
	require.Contains(t, enabledOut, `"total":2`)
	require.NotContains(t, enabledOut, alphaDisabled.ID)

	// Task identity filter.
	betaOut, err := handlers["list_schedules"](ctx, json.RawMessage(`{"task_id":"`+betaTask.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, betaOut, `"total":1`)
	require.Contains(t, betaOut, betaEnabled.ID)
	require.NotContains(t, betaOut, alphaEnabled.ID)

	// Pagination bounds the page and reports has_more.
	pageOut, err := handlers["list_schedules"](ctx, json.RawMessage(`{"limit":2,"offset":0}`))
	require.NoError(t, err)
	require.Contains(t, pageOut, `"count":2`)
	require.Contains(t, pageOut, `"has_more":true`)
}

func TestBuildChannelUtilityActionHandlersViewPulseUsesUpcomingService(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	upcomingSvc := NewUpcomingService(repository.NewUpcomingRepo(db))
	project := &models.Project{Name: "Channel Pulse"}
	require.NoError(t, projectRepo.Create(ctx, project))
	other := &models.Project{Name: "Other Channel Pulse"}
	require.NoError(t, projectRepo.Create(ctx, other))

	pending := &models.Task{ProjectID: project.ID, Title: "Channel queued", Prompt: "queued prompt", Category: models.CategoryActive, Status: models.StatusPending, Priority: 3}
	require.NoError(t, taskRepo.Create(ctx, pending))
	scheduledTask := &models.Task{ProjectID: project.ID, Title: "Channel scheduled", Prompt: strings.Repeat("p", 250), Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, scheduledTask))
	foreignTask := &models.Task{ProjectID: other.ID, Title: "Foreign channel scheduled", Prompt: "foreign", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 4}
	require.NoError(t, taskRepo.Create(ctx, foreignTask))
	now := time.Now().UTC()
	scheduled := &models.Schedule{TaskID: scheduledTask.ID, RunAt: now.Add(-time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 2, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, scheduleRepo.Create(ctx, scheduled))
	foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: now.Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, scheduleRepo.Create(ctx, foreignSchedule))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ScheduleRepo: scheduleRepo, UpcomingSvc: upcomingSvc})
	out, err := handlers["view_pulse"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NotContains(t, out, foreignTask.ID)
	require.NotContains(t, out, strings.Repeat("p", 225))

	var got struct {
		OK           bool   `json:"ok"`
		ProjectID    string `json:"project_id"`
		PendingTasks []struct {
			TaskID string `json:"task_id"`
		} `json:"pending_tasks"`
		ScheduledTasks []struct {
			TaskID      string `json:"task_id"`
			ScheduleID  string `json:"schedule_id"`
			RepeatLabel string `json:"repeat_label"`
		} `json:"scheduled_tasks"`
		TaskSummary struct {
			Scheduled struct {
				Overdue     int `json:"overdue"`
				DueToday    int `json:"due_today"`
				DueThisWeek int `json:"due_this_week"`
			} `json:"scheduled"`
		} `json:"task_summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.Equal(t, project.ID, got.ProjectID)
	require.Len(t, got.PendingTasks, 1)
	require.Equal(t, pending.ID, got.PendingTasks[0].TaskID)
	require.Len(t, got.ScheduledTasks, 1)
	require.Equal(t, scheduledTask.ID, got.ScheduledTasks[0].TaskID)
	require.Equal(t, scheduled.ID, got.ScheduledTasks[0].ScheduleID)
	require.Equal(t, "every 2 hours", got.ScheduledTasks[0].RepeatLabel)
	require.Equal(t, 1, got.TaskSummary.Scheduled.Overdue)
	require.Equal(t, 1, got.TaskSummary.Scheduled.DueToday)
	require.Equal(t, 1, got.TaskSummary.Scheduled.DueThisWeek)
}

func TestBuildChannelUtilityActionHandlersListSchedulesIncludesEmptyDaysAndNullNextRun(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule Discovery Contract"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "One time", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	_, err := db.ExecContext(ctx, `UPDATE schedules SET next_run = NULL WHERE id = ?`, schedule.ID)
	require.NoError(t, err)

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ScheduleRepo: scheduleRepo})
	out, err := handlers["list_schedules"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)

	var result struct {
		Schedules []map[string]any `json:"schedules"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Schedules, 1)
	days, ok := result.Schedules[0]["days"]
	require.True(t, ok, "days must always be present")
	require.Equal(t, "", days)
	nextRun, ok := result.Schedules[0]["next_run"]
	require.True(t, ok, "next_run must always be present")
	require.Nil(t, nextRun)
}

func TestBuildChannelUtilityActionHandlersListChannelsReportsGitHubAppConnectionSafely(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Channel GitHub App Status"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModeApp))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppID, "12345"))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppSlug, "openvibely-app"))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppPrivateKey, "PRIVATE-KEY-MUST-NOT-LEAK"))
	require.NoError(t, settingsRepo.Set(ctx, githubSettingInstallationID, "67890"))
	require.NoError(t, settingsRepo.Set(ctx, githubSettingAccountLogin, "openvibely"))
	require.NoError(t, settingsRepo.Set(ctx, githubSettingAccountType, "Organization"))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, SettingsRepo: settingsRepo})
	out, err := handlers["list_channels"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NotContains(t, out, "PRIVATE-KEY-MUST-NOT-LEAK")

	var result struct {
		GitHub struct {
			Configured    bool   `json:"configured"`
			Connected     bool   `json:"connected"`
			Status        string `json:"status"`
			AuthMode      string `json:"auth_mode"`
			AccountLogin  string `json:"account_login"`
			AccountType   string `json:"account_type"`
			AppConfigured bool   `json:"app_configured"`
			PATConfigured bool   `json:"pat_configured"`
		} `json:"github"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.True(t, result.GitHub.Configured)
	require.True(t, result.GitHub.Connected)
	require.Equal(t, "connected", result.GitHub.Status)
	require.Equal(t, GitHubAuthModeApp, result.GitHub.AuthMode)
	require.Equal(t, "openvibely", result.GitHub.AccountLogin)
	require.Equal(t, "Organization", result.GitHub.AccountType)
	require.True(t, result.GitHub.AppConfigured)
	require.False(t, result.GitHub.PATConfigured)

	require.NoError(t, settingsRepo.Set(ctx, githubSettingInstallationID, ""))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "PAT-MUST-NOT-LEAK"))
	out, err = handlers["list_channels"](ctx, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NotContains(t, out, "PAT-MUST-NOT-LEAK")
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.True(t, result.GitHub.Configured)
	require.False(t, result.GitHub.Connected)
	require.Equal(t, "configured_not_connected", result.GitHub.Status)
	require.Equal(t, GitHubAuthModeApp, result.GitHub.AuthMode)
	require.True(t, result.GitHub.PATConfigured)
}

func TestChannelServiceListChannelsIncludesEmailWebhooksAndTargetsSafely(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	webhookRepo := repository.NewWebhookRepo(db)
	channelTargetRepo := repository.NewChannelTargetRepo(db)
	project := &models.Project{Name: "Channel Surface Complete Status"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingProvider, EmailProviderCustom))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingPassword, "EMAIL-PASSWORD-MUST-NOT-LEAK"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingIMAPHost, "imap.example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingSMTPHost, "smtp.example.com"))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "sender@example.com", DisplayName: "Sender", AddedBy: "test"}))
	require.NoError(t, webhookRepo.Create(ctx, &models.WebhookEndpoint{ProjectID: project.ID, Name: "Deploy", Enabled: true, PathToken: "WEBHOOK-PATH-TOKEN-MUST-NOT-LEAK", Secret: "WEBHOOK-SECRET-MUST-NOT-LEAK", DefaultPriority: 2}))
	require.NoError(t, channelTargetRepo.Upsert(ctx, models.ChannelTarget{ID: "target-1", ProjectID: project.ID, Platform: "slack", TargetKind: "channel", Name: "ops", TargetID: "RAW-TARGET-ID-MUST-NOT-LEAK", Home: true}))
	router := NewChannelMessageRouter(channelTargetRepo, settingsRepo)
	emailStatus := func(context.Context) EmailConnectionStatus {
		return EmailConnectionStatus{Configured: true, Running: true, Address: "bot@example.com", Provider: EmailProviderCustom, IMAPHost: "imap.example.com", IMAPPort: 993, SMTPHost: "smtp.example.com", SMTPPort: 587}
	}

	slackSvc := NewSlackService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	slackSvc.SetEmailStatusProvider(emailStatus)
	slackSvc.SetEmailAuthRepo(emailAuthRepo)
	slackSvc.SetWebhookRepo(webhookRepo)
	slackSvc.SetChannelMessageRouter(router)
	discordSvc := NewDiscordService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	discordSvc.SetEmailStatusProvider(emailStatus)
	discordSvc.SetEmailAuthRepo(emailAuthRepo)
	discordSvc.SetWebhookRepo(webhookRepo)
	discordSvc.SetChannelMessageRouter(router)
	telegramSvc := &TelegramService{settingsRepo: settingsRepo, projectRepo: projectRepo}
	telegramSvc.SetEmailStatusProvider(emailStatus)
	telegramSvc.SetEmailAuthRepo(emailAuthRepo)
	telegramSvc.SetWebhookRepo(webhookRepo)
	telegramSvc.SetChannelMessageRouter(router)

	for _, tc := range []struct {
		name     string
		handlers map[string]chatcontrol.RuntimeActionHandler
	}{
		{name: "slack", handlers: slackSvc.slackActionHandlers(project.ID, slackActionContext{TeamID: "T1", ChannelID: "C1", UserID: "U1"}, nil)},
		{name: "discord", handlers: discordSvc.discordActionHandlers(project.ID, discordActionContext{ChannelID: "C1", UserID: "U1"}, nil)},
		{name: "telegram", handlers: telegramSvc.telegramActionHandlers(project.ID, 1001, 2002, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.handlers["list_channels"](ctx, json.RawMessage(`{}`))
			require.NoError(t, err)
			for _, secret := range []string{"EMAIL-PASSWORD-MUST-NOT-LEAK", "WEBHOOK-PATH-TOKEN-MUST-NOT-LEAK", "WEBHOOK-SECRET-MUST-NOT-LEAK", "RAW-TARGET-ID-MUST-NOT-LEAK"} {
				require.NotContains(t, out, secret)
			}

			var result struct {
				Email struct {
					Configured            bool   `json:"configured"`
					Running               bool   `json:"running"`
					Status                string `json:"status"`
					AuthorizedSenderCount int    `json:"authorized_sender_count"`
				} `json:"email"`
				Webhooks struct {
					Total      int  `json:"total"`
					Active     int  `json:"active"`
					Configured bool `json:"configured"`
				} `json:"webhooks"`
				OutboundTargets struct {
					Total              int  `json:"total"`
					Configured         bool `json:"configured"`
					MessagingAvailable bool `json:"messaging_available"`
					ByPlatform         map[string]struct {
						Total int `json:"total"`
						Home  int `json:"home"`
					} `json:"by_platform"`
				} `json:"outbound_message_targets"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &result))
			require.True(t, result.Email.Configured)
			require.True(t, result.Email.Running)
			require.Equal(t, "running", result.Email.Status)
			require.Equal(t, 1, result.Email.AuthorizedSenderCount)
			require.True(t, result.Webhooks.Configured)
			require.Equal(t, 1, result.Webhooks.Total)
			require.Equal(t, 1, result.Webhooks.Active)
			require.True(t, result.OutboundTargets.Configured)
			require.True(t, result.OutboundTargets.MessagingAvailable)
			require.Equal(t, 1, result.OutboundTargets.Total)
			require.Equal(t, 1, result.OutboundTargets.ByPlatform["slack"].Total)
			require.Equal(t, 1, result.OutboundTargets.ByPlatform["slack"].Home)
		})
	}
}

func TestBuildChannelUtilityActionHandlersPersonalityModelAndProjectInfo(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	customPersonalityRepo := repository.NewCustomPersonalityRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Info Project", Description: "Details"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Default Model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	require.NoError(t, taskRepo.Create(ctx, &models.Task{ProjectID: project.ID, Title: "Info task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}))
	require.NoError(t, customPersonalityRepo.Create(ctx, &models.CustomPersonality{
		Name:         "Channel Custom",
		Key:          "channel_custom",
		Description:  "Custom channel personality",
		SystemPrompt: "You are a channel custom personality with enough detail.",
	}))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ProjectRepo: projectRepo, SettingsRepo: settingsRepo, CustomPersonalityRepo: customPersonalityRepo, LLMConfigRepo: llmConfigRepo})
	setOut, err := handlers["set_personality"](ctx, json.RawMessage(`{"personality":"no_nonsense_pro"}`))
	require.NoError(t, err)
	require.Contains(t, setOut, "Personality changed")
	customOut, err := handlers["set_personality"](ctx, json.RawMessage(`{"personality":"channel_custom"}`))
	require.NoError(t, err)
	require.Contains(t, customOut, "Personality changed")
	unknownOut, err := handlers["set_personality"](ctx, json.RawMessage(`{"personality":"missing_custom"}`))
	require.NoError(t, err)
	require.Contains(t, unknownOut, `Unknown personality "missing_custom"`)
	getOut, err := handlers["get_personality"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, getOut, "channel_custom")
	modelsOut, err := handlers["list_models"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, modelsOut, "Default Model")
	projectOut, err := handlers["project_info"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, projectOut, "Info Project")
	require.Contains(t, projectOut, "Total tasks: 1")
}

func TestBuildChannelUtilityActionHandlersModelStatusToolsUseCompactRuntimeSummaries(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Channel Compact Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	_, err := db.Exec(`DELETE FROM agent_configs`)
	require.NoError(t, err)

	largeBody := strings.Repeat("large-provider-json", 4096)
	defaultModel := &models.LLMConfig{
		Name: "Channel Default Model", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey,
		Model: "channel-default-model", IsDefault: true, APIKey: "secret-key",
		ExtraBodyJSON: largeBody, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, defaultModel))
	customModel := &models.LLMConfig{
		Name: "Channel Compact Model", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "channel-compact-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client", BaseURL: "https://example.com/v1/",
		ModelsURL: "https://example.com/models", OAuthAuthorizeURL: "https://example.com/auth", OAuthTokenURL: "https://example.com/token",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`, MaxWorkers: 4, WorkerTimeout: 30,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, customModel))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{
		ProjectID: project.ID, ProjectRepo: projectRepo, SettingsRepo: settingsRepo, LLMConfigRepo: llmConfigRepo,
	})

	counter.Reset()
	counter.SetEnabled(true)
	modelsOut, err := handlers["list_models"](ctx, nil)
	require.NoError(t, err)
	getOut, err := handlers["get_model"](ctx, json.RawMessage(`{"name":" channel compact model "}`))
	require.NoError(t, err)
	settingsOut, err := handlers["view_settings"](ctx, nil)
	require.NoError(t, err)
	counter.SetEnabled(false)

	require.Contains(t, modelsOut, "Channel Default Model (default)")
	require.Contains(t, modelsOut, "Channel Compact Model")
	require.Contains(t, modelsOut, "max_workers: 4")
	require.Contains(t, getOut, "Model: Channel Compact Model")
	require.Contains(t, getOut, "Provider: openai_compatible")
	require.Contains(t, getOut, "max_workers: 4")
	require.Contains(t, settingsOut, "- Configured models: 2")
	for _, out := range []string{modelsOut, getOut, settingsOut} {
		require.NotContains(t, out, "secret")
		require.NotContains(t, out, largeBody)
	}
	assertChannelModelStatusStatementsCompact(t, counter.Statements())
}

func assertChannelModelStatusStatementsCompact(t *testing.T, statements []string) {
	t.Helper()
	var runtimeListQueries, targetedLookupQueries int
	for _, raw := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if !strings.Contains(stmt, " from agent_configs") {
			continue
		}
		projection := strings.Split(stmt, " from agent_configs")[0]
		for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("channel model/status query selected forbidden column %q: %s", forbidden, raw)
			}
		}
		if strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") && strings.Contains(stmt, "where name = ? collate nocase") {
			targetedLookupQueries++
			continue
		}
		if strings.Contains(projection, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") && strings.Contains(stmt, "order by is_default desc, name asc") && !strings.Contains(stmt, " where ") {
			runtimeListQueries++
			continue
		}
		if strings.Contains(projection, "select id, name, provider, model, reasoning_effort") {
			t.Fatalf("channel model/status tool used full model list query: %s", raw)
		}
	}
	if runtimeListQueries != 2 {
		t.Fatalf("runtime list compact query count = %d, want 2; statements: %#v", runtimeListQueries, statements)
	}
	if targetedLookupQueries != 1 {
		t.Fatalf("targeted lookup compact query count = %d, want 1; statements: %#v", targetedLookupQueries, statements)
	}
}

func TestBuildChannelTaskActionHandlersCreateTaskUsesSharedLogicAndOriginCallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Channel Actions"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Default", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	collector := newChannelActionSummaryCollector()
	var callbackTaskIDs []string
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID:     project.ID,
		TaskSvc:       NewTaskService(taskRepo, nil, nil),
		LLMConfigRepo: llmConfigRepo,
		Collector:     collector,
		OnTasksCreated: func(_ context.Context, _ []TaskCreationRequest, tasks []models.Task) error {
			for _, task := range tasks {
				callbackTaskIDs = append(callbackTaskIDs, task.ID)
			}
			return nil
		},
	})
	payload, err := json.Marshal(TaskCreationRequest{Title: "Shared action task", Prompt: "Do shared work"})
	require.NoError(t, err)

	summary, err := handlers["create_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, summary, "Shared action task")
	require.Contains(t, summary, "[TASK_ID:")
	require.Len(t, callbackTaskIDs, 1)
	created, err := taskRepo.GetByID(ctx, callbackTaskIDs[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, project.ID, created.ProjectID)
	require.Equal(t, 2, created.Priority)
	require.Contains(t, strings.Join(collector.createdLines, "\n"), callbackTaskIDs[0])
}

func TestBuildChannelTaskActionHandlersEditTaskUpdatesPrimaryAgentDefinition(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Channel Edit Primary Agent"}
	require.NoError(t, projectRepo.Create(ctx, project))
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	modelConfig := &models.LLMConfig{Name: "Channel Model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmConfigRepo.Create(ctx, modelConfig))
	agentRepo := repository.NewAgentRepo(db)
	agentDef := &models.Agent{Name: "Channel Reviewer", Key: "channel_reviewer", Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agentDef))
	taskSvc := NewTaskService(taskRepo, nil, nil)
	taskSvc.SetAgentRepo(agentRepo)
	task := &models.Task{ProjectID: project.ID, Title: "Channel edit target", Prompt: "Prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, AgentID: &modelConfig.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	collector := newChannelActionSummaryCollector()
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{ProjectID: project.ID, TaskSvc: taskSvc, Collector: collector})

	payload, err := json.Marshal(TaskEditRequest{ID: task.ID, Agent: agentDef.Name})
	require.NoError(t, err)
	summary, err := handlers["edit_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, summary, "Edited 1 task(s)")
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.AgentDefinitionID)
	require.Equal(t, agentDef.ID, *updated.AgentDefinitionID)
	require.NotNil(t, updated.AgentID)
	require.Equal(t, modelConfig.ID, *updated.AgentID)

	clearPayload, err := json.Marshal(TaskEditRequest{ID: task.ID, ClearAgentDefinition: true})
	require.NoError(t, err)
	clearSummary, err := handlers["edit_task"](ctx, clearPayload)
	require.NoError(t, err)
	require.Contains(t, clearSummary, "Edited 1 task(s)")
	updated, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Nil(t, updated.AgentDefinitionID)
	require.NotNil(t, updated.AgentID)
	require.Equal(t, modelConfig.ID, *updated.AgentID)
	require.Contains(t, strings.Join(collector.editedLines, "\n"), task.ID)
}

func TestBuildChannelTaskActionHandlersEditTaskRejectsInvalidPriority(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Channel Edit Invalid Priority"}
	require.NoError(t, projectRepo.Create(ctx, project))
	taskSvc := NewTaskService(taskRepo, nil, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Channel invalid priority target", Prompt: "Prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 3}
	require.NoError(t, taskRepo.Create(ctx, task))
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{ProjectID: project.ID, TaskSvc: taskSvc})

	for _, priority := range []int{0, 5} {
		t.Run(fmt.Sprintf("priority %d", priority), func(t *testing.T) {
			payload := json.RawMessage(fmt.Sprintf(`{"id":%q,"title":"Should Not Persist","priority":%d}`, task.ID, priority))
			summary, err := handlers["edit_task"](ctx, payload)
			require.Error(t, err)
			require.Contains(t, err.Error(), "edit_task: no tasks were updated")
			require.Contains(t, summary, "Failed to edit 1 task(s)")
			require.Contains(t, summary, ErrInvalidTaskPriority.Error())

			updated, err := taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.NotNil(t, updated)
			require.Equal(t, "Channel invalid priority target", updated.Title)
			require.Equal(t, 3, updated.Priority)
		})
	}
}

func TestBuildChannelTaskActionHandlersCreateSwarmTaskUsesSharedSwarmService(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	project := &models.Project{Name: "Channel Swarm Actions"}
	require.NoError(t, projectRepo.Create(ctx, project))
	foreignProject := &models.Project{Name: "Foreign Channel Swarm Project"}
	require.NoError(t, projectRepo.Create(ctx, foreignProject))
	taskSvc := NewTaskService(taskRepo, nil, nil)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, nil)
	taskSvc.SetSwarmService(swarmSvc)
	collector := newChannelActionSummaryCollector()
	var callbackTaskIDs []string
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID: project.ID,
		TaskSvc:   taskSvc,
		Collector: collector,
		OnTasksCreated: func(_ context.Context, _ []TaskCreationRequest, tasks []models.Task) error {
			for _, task := range tasks {
				callbackTaskIDs = append(callbackTaskIDs, task.ID)
			}
			return nil
		},
	})
	payload, err := json.Marshal(SwarmTaskRuntimeInput{Title: "Shared swarm", Prompt: "Split this across workers", ProjectID: foreignProject.ID, Category: string(models.CategoryBacklog)})
	require.NoError(t, err)

	summary, err := handlers["create_swarm_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, summary, "Created swarm task: Shared swarm")
	require.Contains(t, summary, "Planner starts when the swarm parent is Active")
	require.Contains(t, summary, "(backlog)")
	require.Len(t, callbackTaskIDs, 1)
	created, err := taskRepo.GetByID(ctx, callbackTaskIDs[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, project.ID, created.ProjectID)
	require.Equal(t, models.SwarmRoleParent, created.SwarmRole)
	require.Equal(t, models.StatusBlocked, created.Status)
	require.Equal(t, 2, created.Priority)
	require.Equal(t, models.TagNone, created.Tag)
	cfg, err := models.ParseSwarmConfig(created.SwarmConfig)
	require.NoError(t, err)
	require.True(t, cfg.ReviewerEnabled)
	require.True(t, cfg.MergerEnabled)
	foreignTasks, err := taskRepo.ListByProject(ctx, foreignProject.ID, "")
	require.NoError(t, err)
	require.Empty(t, foreignTasks)
	require.Contains(t, strings.Join(collector.createdLines, "\n"), callbackTaskIDs[0])
	planner, err := taskRepo.FindSwarmChildByRole(ctx, created.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)
}

func TestBuildChannelTaskActionHandlersCreateSwarmTaskPersistsMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	directProject := &models.Project{Name: "Direct Swarm Metadata"}
	require.NoError(t, projectRepo.Create(ctx, directProject))
	channelProject := &models.Project{Name: "Channel Swarm Metadata"}
	require.NoError(t, projectRepo.Create(ctx, channelProject))
	foreignProject := &models.Project{Name: "Foreign Channel Swarm Metadata"}
	require.NoError(t, projectRepo.Create(ctx, foreignProject))
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	modelConfig := &models.LLMConfig{Name: "Channel Swarm Model", Provider: models.ProviderTest, Model: "claude-sonnet-4-5", MaxTokens: 4096}
	require.NoError(t, llmConfigRepo.Create(ctx, modelConfig))
	agentRepo := repository.NewAgentRepo(db)
	agentDef := &models.Agent{Name: "Channel Swarm Agent", Key: "channel-swarm-agent", Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agentDef))
	taskSvc := NewTaskService(taskRepo, nil, nil)
	taskSvc.SetAgentRepo(agentRepo)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, nil)
	taskSvc.SetSwarmService(swarmSvc)
	reviewerEnabled := false
	mergerEnabled := false
	input := SwarmTaskRuntimeInput{
		Title:             "Runtime metadata swarm",
		Prompt:            "Split this channel bug",
		Goal:              "Channel tests pass",
		ProjectID:         foreignProject.ID,
		Category:          string(models.CategoryBacklog),
		Priority:          4,
		AgentID:           modelConfig.ID,
		Agent:             agentDef.Name,
		Tag:               string(models.TagFeature),
		ReviewerEnabled:   &reviewerEnabled,
		MergerEnabled:     &mergerEnabled,
		MergeTargetBranch: "integration/channel",
	}

	directParent, directSummary, err := ExecuteCreateSwarmTaskRuntime(ctx, CreateSwarmTaskRuntimeOptions{ProjectID: directProject.ID, Input: input, SwarmSvc: swarmSvc, TaskSvc: taskSvc})
	require.NoError(t, err)
	require.Contains(t, directSummary, "Created swarm task: Runtime metadata swarm")

	var callbackTaskIDs []string
	handlers := buildChannelTaskActionHandlers(channelTaskActionHandlerOptions{
		ProjectID: channelProject.ID,
		TaskSvc:   taskSvc,
		SwarmSvc:  swarmSvc,
		OnTasksCreated: func(_ context.Context, _ []TaskCreationRequest, tasks []models.Task) error {
			for _, task := range tasks {
				callbackTaskIDs = append(callbackTaskIDs, task.ID)
			}
			return nil
		},
	})
	payload, err := json.Marshal(input)
	require.NoError(t, err)

	summary, err := handlers["create_swarm_task"](ctx, payload)
	require.NoError(t, err)
	require.Contains(t, summary, "Created swarm task: Runtime metadata swarm")
	require.Len(t, callbackTaskIDs, 1)
	created, err := taskRepo.GetByID(ctx, callbackTaskIDs[0])
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, channelProject.ID, created.ProjectID)

	type persistedSwarmMetadata struct {
		Category          models.TaskCategory
		Priority          int
		Tag               models.TaskTag
		AgentID           string
		AgentDefinitionID string
		MergeTargetBranch string
		ReviewerEnabled   bool
		MergerEnabled     bool
		Goal              string
	}
	loadMetadata := func(task *models.Task) persistedSwarmMetadata {
		t.Helper()
		require.NotNil(t, task)
		require.NotNil(t, task.AgentID)
		require.NotNil(t, task.AgentDefinitionID)
		cfg, err := models.ParseSwarmConfig(task.SwarmConfig)
		require.NoError(t, err)
		goal, err := repository.NewTaskGoalRepo(db).GetByTaskID(ctx, task.ID)
		require.NoError(t, err)
		require.NotNil(t, goal)
		return persistedSwarmMetadata{
			Category:          task.Category,
			Priority:          task.Priority,
			Tag:               task.Tag,
			AgentID:           *task.AgentID,
			AgentDefinitionID: *task.AgentDefinitionID,
			MergeTargetBranch: task.MergeTargetBranch,
			ReviewerEnabled:   cfg.ReviewerEnabled,
			MergerEnabled:     cfg.MergerEnabled,
			Goal:              goal.Objective,
		}
	}
	require.Equal(t, loadMetadata(directParent), loadMetadata(created))
	foreignTasks, err := taskRepo.ListByProject(ctx, foreignProject.ID, "")
	require.NoError(t, err)
	require.Empty(t, foreignTasks)
}

func TestChannelListAgentsResultUsesRuntimeSummariesAndPreservesOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := repository.NewAgentRepo(db)
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}

	agents := []*models.Agent{
		{Name: "Alpha Agent", Description: "alpha description", Model: "inherit", Skills: []models.SkillConfig{{Name: "one"}}, Enabled: true, SelectableAsPrimary: true},
		{Name: "Bravo Agent", Description: "bravo description", Model: "gpt-5", MCPServers: []models.MCPServerConfig{{Name: "one"}, {Name: "two"}}, Enabled: true, SelectableAsPrimary: true},
		{Name: "Archived Agent", Description: "hidden", Model: "gpt-5", GeneratedStatus: models.AgentStatusArchived, Enabled: true, SelectableAsPrimary: true},
	}
	for _, agent := range agents {
		require.NoError(t, repo.Create(ctx, agent))
	}

	out := channelListAgentsResult(ctx, repo, "")
	alphaLine := "- Alpha Agent — alpha description, 1 skills, 0 MCP servers"
	bravoLine := "- Bravo Agent — bravo description, model: gpt-5, 0 skills, 2 MCP servers"
	require.Contains(t, out, "Configured Agents:")
	require.Contains(t, out, alphaLine)
	require.Contains(t, out, bravoLine)
	require.NotContains(t, out, "Archived Agent")
	require.Less(t, strings.Index(out, alphaLine), strings.Index(out, bravoLine), "agents should remain ordered by name ASC")

	require.Equal(t, "Agent definitions not available.", channelListAgentsResult(ctx, nil, ""))
	require.Equal(t, "Channel unavailable.", channelListAgentsResult(ctx, nil, "Channel unavailable."))

	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents for empty result: %v", err)
	}
	require.Equal(t, "No agents configured.", channelListAgentsResult(ctx, repo, ""))
}

type fakeProjectCloneProvider struct {
	cloneFn func(ctx context.Context, projectID, repoURL string) (string, string, error)
}

func (f fakeProjectCloneProvider) CloneProjectRepo(ctx context.Context, projectID, repoURL string) (string, string, error) {
	if f.cloneFn != nil {
		return f.cloneFn(ctx, projectID, repoURL)
	}
	return "/tmp/openvibely-test/" + projectID, "https://github.com/acme/widgets", nil
}

func TestExecuteCreateGitHubProjectRuntimeCreatesGitHubBackedProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)
	modelRepo := repository.NewLLMConfigRepo(db)
	model := &models.LLMConfig{Name: "Project Default", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, modelRepo.Create(ctx, model))

	var cloneProjectID, cloneRepoURL string
	out, err := ExecuteCreateGitHubProjectRuntime(ctx, json.RawMessage(fmt.Sprintf(`{"name":" Runtime GitHub Project ","description":"from chat","repo_url":"https://github.com/acme/widgets","default_agent_config_id":%q,"max_workers":3}`, model.ID)), CreateGitHubProjectRuntimeOptions{
		ProjectSvc: projectSvc,
		GitHubSvc: fakeProjectCloneProvider{cloneFn: func(ctx context.Context, projectID, repoURL string) (string, string, error) {
			cloneProjectID = projectID
			cloneRepoURL = repoURL
			return "/repos/" + projectID, "https://github.com/acme/widgets", nil
		}},
	})
	require.NoError(t, err)

	var resp createGitHubProjectRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.NotEmpty(t, resp.ProjectID)
	require.Equal(t, "Runtime GitHub Project", resp.Name)
	require.Equal(t, "https://github.com/acme/widgets", resp.RepoURL)
	require.True(t, resp.RepoPathPresent)
	require.False(t, resp.Switched)
	require.Equal(t, resp.ProjectID, cloneProjectID)
	require.Equal(t, "https://github.com/acme/widgets", cloneRepoURL)

	created, err := projectRepo.GetByID(ctx, resp.ProjectID)
	require.NoError(t, err)
	require.NotNil(t, created)
	require.Equal(t, "Runtime GitHub Project", created.Name)
	require.Equal(t, "from chat", created.Description)
	require.Equal(t, "/repos/"+resp.ProjectID, created.RepoPath)
	require.Equal(t, "https://github.com/acme/widgets", created.RepoURL)
	require.NotNil(t, created.DefaultAgentConfigID)
	require.Equal(t, model.ID, *created.DefaultAgentConfigID)
	require.NotNil(t, created.MaxWorkers)
	require.Equal(t, 3, *created.MaxWorkers)
}

func TestExecuteCreateGitHubProjectRuntimeRollsBackOnCloneFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)

	out, err := ExecuteCreateGitHubProjectRuntime(ctx, json.RawMessage(`{"name":"Rollback Project","repo_url":"https://github.com/acme/fails"}`), CreateGitHubProjectRuntimeOptions{
		ProjectSvc: projectSvc,
		GitHubSvc: fakeProjectCloneProvider{cloneFn: func(ctx context.Context, projectID, repoURL string) (string, string, error) {
			return "", "", fmt.Errorf("clone failed for safe test")
		}},
	})
	require.NoError(t, err)
	var resp createGitHubProjectRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.False(t, resp.OK)
	require.Contains(t, resp.Error, "failed to clone")

	projects, err := projectRepo.List(ctx)
	require.NoError(t, err)
	for _, p := range projects {
		require.NotEqual(t, "Rollback Project", p.Name)
	}
}

func TestExecuteCreateGitHubProjectRuntimeRejectsLocalPathAndCreateDirectoryInput(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectSvc := NewProjectService(repository.NewProjectRepo(db))
	opts := CreateGitHubProjectRuntimeOptions{ProjectSvc: projectSvc, GitHubSvc: fakeProjectCloneProvider{}}

	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "repo_path field", payload: `{"name":"Local","repo_url":"https://github.com/acme/widgets","repo_path":"/tmp/widgets"}`, want: `repo_path`},
		{name: "create_directory field", payload: `{"name":"Local","repo_url":"https://github.com/acme/widgets","create_directory":true}`, want: `create_directory`},
		{name: "absolute local repo_url", payload: `{"name":"Local","repo_url":"/tmp/widgets"}`, want: `local filesystem paths`},
		{name: "bare path repo_url", payload: `{"name":"Local","repo_url":"tmp/widgets"}`, want: `local filesystem paths`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExecuteCreateGitHubProjectRuntime(ctx, json.RawMessage(tc.payload), opts)
			require.NoError(t, err)
			var resp createGitHubProjectRuntimeResponse
			require.NoError(t, json.Unmarshal([]byte(out), &resp))
			require.False(t, resp.OK)
			require.Contains(t, resp.Error, tc.want)
		})
	}
}

func TestExecuteCreateGitHubProjectRuntimeSwitchAfterCreateWhenSupported(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectSvc := NewProjectService(repository.NewProjectRepo(db))
	var switchedTo string

	out, err := ExecuteCreateGitHubProjectRuntime(ctx, json.RawMessage(`{"name":"Switchable","repo_url":"https://github.com/acme/switchable","switch_after_create":true}`), CreateGitHubProjectRuntimeOptions{
		ProjectSvc: projectSvc,
		GitHubSvc:  fakeProjectCloneProvider{},
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			switchedTo = project.ID
			return nil
		},
	})
	require.NoError(t, err)
	var resp createGitHubProjectRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK)
	require.True(t, resp.Switched)
	require.Equal(t, resp.ProjectID, switchedTo)
}

func TestSlackTelegramDiscordRuntimesCreateGitHubProject(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		repoSuffix string
		runtime    func(projectID string, projectRepo *repository.ProjectRepo, projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider) *llmcontracts.RuntimeTools
	}{
		{
			name:       "Slack",
			repoSuffix: "slack-runtime",
			runtime: func(projectID string, projectRepo *repository.ProjectRepo, projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider) *llmcontracts.RuntimeTools {
				svc := &SlackService{projectRepo: projectRepo, userProjects: map[string]string{}}
				svc.SetProjectCreationServices(projectSvc, githubSvc, nil, nil)
				return svc.buildSlackActionToolRuntime(projectID, slackActionContext{TeamID: "T1", UserID: "U1"}, nil)
			},
		},
		{
			name:       "Telegram",
			repoSuffix: "telegram-runtime",
			runtime: func(projectID string, projectRepo *repository.ProjectRepo, projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider) *llmcontracts.RuntimeTools {
				svc := &TelegramService{projectRepo: projectRepo, userProjects: map[int64]string{}, userProjectVersions: map[int64]uint64{}}
				svc.SetProjectCreationServices(projectSvc, githubSvc, nil, nil)
				return svc.buildTelegramActionToolRuntime(projectID, 12345, 67890, nil)
			},
		},
		{
			name:       "Discord",
			repoSuffix: "discord-runtime",
			runtime: func(projectID string, projectRepo *repository.ProjectRepo, projectSvc *ProjectService, githubSvc GitHubProjectCloneProvider) *llmcontracts.RuntimeTools {
				svc := &DiscordService{projectRepo: projectRepo, userProjects: map[string]string{}}
				svc.SetProjectCreationServices(projectSvc, githubSvc, nil, nil)
				return svc.buildDiscordActionToolRuntime(projectID, discordActionContext{ChannelID: "C1", UserID: "U1"}, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			projectRepo := repository.NewProjectRepo(db)
			projectSvc := NewProjectService(projectRepo)
			current := &models.Project{Name: tt.name + " Current Project"}
			require.NoError(t, projectRepo.Create(ctx, current))

			normalizedURL := "https://github.com/acme/" + tt.repoSuffix
			cloneSvc := fakeProjectCloneProvider{cloneFn: func(ctx context.Context, projectID, repoURL string) (string, string, error) {
				require.Equal(t, normalizedURL, repoURL)
				return "/repos/" + projectID, normalizedURL, nil
			}}
			rt := tt.runtime(current.ID, projectRepo, projectSvc, cloneSvc)
			require.NotNil(t, rt)

			payload := json.RawMessage(fmt.Sprintf(`{"name":%q,"repo_url":%q,"switch_after_create":true}`, tt.name+" Created Project", normalizedURL))
			out, handled, isErr, err := rt.Executor(ctx, "create_project", payload)
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, isErr, out)

			var resp createGitHubProjectRuntimeResponse
			require.NoError(t, json.Unmarshal([]byte(out), &resp))
			require.True(t, resp.OK, resp.Error)
			require.NotEmpty(t, resp.ProjectID)
			require.Equal(t, tt.name+" Created Project", resp.Name)
			require.Equal(t, normalizedURL, resp.RepoURL)
			require.True(t, resp.RepoPathPresent)
			require.True(t, resp.Switched)

			created, err := projectRepo.GetByID(ctx, resp.ProjectID)
			require.NoError(t, err)
			require.NotNil(t, created)
			require.Equal(t, "/repos/"+resp.ProjectID, created.RepoPath)
			require.Equal(t, normalizedURL, created.RepoURL)
		})
	}
}

func TestExecuteUpdateProjectSettingsRuntimeUpdatesAndClearsDefaults(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	projectLimit := 1
	project := &models.Project{Name: "Runtime Settings Project", Description: "old", MaxWorkers: &projectLimit}
	require.NoError(t, projectRepo.Create(ctx, project))
	modelA := &models.LLMConfig{Name: "Runtime Model A", Provider: models.ProviderTest, Model: "test-a"}
	require.NoError(t, llmConfigRepo.Create(ctx, modelA))
	modelB := &models.LLMConfig{Name: "Runtime Model B", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test"}
	require.NoError(t, llmConfigRepo.Create(ctx, modelB))

	dispatchCalls := 0
	out, err := ExecuteUpdateProjectSettingsRuntime(ctx, UpdateProjectSettingsRuntimeOptions{
		ProjectID:          project.ID,
		Input:              json.RawMessage(`{"project_id":"` + project.ID + `","project_name":"Runtime Settings Project","new_name":"Renamed Runtime Project","description":"updated","default_model":"runtime model b","max_workers":2}`),
		ProjectSvc:         projectSvc,
		ProjectRepo:        projectRepo,
		LLMConfigRepo:      llmConfigRepo,
		DispatchQueuedWork: func() { dispatchCalls++ },
	})
	require.NoError(t, err)
	var resp updateProjectSettingsRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK, resp.Error)
	require.Equal(t, project.ID, resp.ProjectID)
	require.Equal(t, "Renamed Runtime Project", resp.Name)
	require.True(t, resp.DefaultModel.Set)
	require.Equal(t, modelB.ID, resp.DefaultModel.ModelID)
	require.True(t, resp.WorkerLimit.Set)
	require.Equal(t, 2, resp.WorkerLimit.MaxWorkers)
	require.Equal(t, 1, dispatchCalls, "increasing finite max_workers should dispatch queued work")

	updated, err := projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.DefaultAgentConfigID)
	require.Equal(t, modelB.ID, *updated.DefaultAgentConfigID)
	require.NotNil(t, updated.MaxWorkers)
	require.Equal(t, 2, *updated.MaxWorkers)
	require.Equal(t, "Renamed Runtime Project", updated.Name)
	require.Equal(t, "updated", updated.Description)

	out, err = ExecuteUpdateProjectSettingsRuntime(ctx, UpdateProjectSettingsRuntimeOptions{
		ProjectID:          project.ID,
		Input:              json.RawMessage(`{"clear_default_model":true,"max_workers":0}`),
		ProjectSvc:         projectSvc,
		ProjectRepo:        projectRepo,
		LLMConfigRepo:      llmConfigRepo,
		DispatchQueuedWork: func() { dispatchCalls++ },
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK, resp.Error)
	require.False(t, resp.DefaultModel.Set)
	require.False(t, resp.WorkerLimit.Set)
	require.Equal(t, 2, dispatchCalls, "clearing finite max_workers should dispatch queued work")

	updated, err = projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Nil(t, updated.DefaultAgentConfigID)
	require.Nil(t, updated.MaxWorkers)
}

func TestExecuteUpdateProjectSettingsRuntimeRejectsInvalidInputsWithoutPartialUpdate(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	limit := 2
	project := &models.Project{Name: "No Partial Project", Description: "keep", MaxWorkers: &limit, RepoPath: t.TempDir()}
	require.NoError(t, projectRepo.Create(ctx, project))
	model := &models.LLMConfig{Name: "Safe Model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmConfigRepo.Create(ctx, model))
	project.DefaultAgentConfigID = &model.ID
	require.NoError(t, projectSvc.Update(ctx, project))

	run := func(input string) updateProjectSettingsRuntimeResponse {
		out, err := ExecuteUpdateProjectSettingsRuntime(ctx, UpdateProjectSettingsRuntimeOptions{
			ProjectID:     project.ID,
			Input:         json.RawMessage(input),
			ProjectSvc:    projectSvc,
			ProjectRepo:   projectRepo,
			LLMConfigRepo: llmConfigRepo,
		})
		require.NoError(t, err)
		var resp updateProjectSettingsRuntimeResponse
		require.NoError(t, json.Unmarshal([]byte(out), &resp))
		return resp
	}

	for _, input := range []string{
		`{"default_model":"missing","max_workers":3}`,
		`{"project_id":"different-project","max_workers":3}`,
		`{"max_workers":-1}`,
		`{"repo_path":"/tmp/other","max_workers":3}`,
		`{"clear_default_model":true,"default_model":"Safe Model"}`,
	} {
		resp := run(input)
		require.False(t, resp.OK, input)
		updated, err := projectRepo.GetByID(ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, "No Partial Project", updated.Name)
		require.Equal(t, "keep", updated.Description)
		require.NotNil(t, updated.DefaultAgentConfigID)
		require.Equal(t, model.ID, *updated.DefaultAgentConfigID)
		require.NotNil(t, updated.MaxWorkers)
		require.Equal(t, 2, *updated.MaxWorkers)
		require.Equal(t, project.RepoPath, updated.RepoPath)
	}
}

func TestExecuteUpdateProjectSettingsRuntimeRejectsAmbiguousModelName(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Ambiguous Model Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	_, err := db.ExecContext(ctx, `INSERT INTO agent_configs (id, name, provider, model, is_default, auth_method) VALUES
		('ambiguous-model-a', 'Ambiguous Runtime Model', 'test', 'test-a', 0, 'api_key'),
		('ambiguous-model-b', 'ambiguous runtime model', 'test', 'test-b', 0, 'api_key')`)
	require.NoError(t, err)

	out, err := ExecuteUpdateProjectSettingsRuntime(ctx, UpdateProjectSettingsRuntimeOptions{
		ProjectID:     project.ID,
		Input:         json.RawMessage(`{"default_model":"AMBIGUOUS RUNTIME MODEL"}`),
		ProjectSvc:    projectSvc,
		ProjectRepo:   projectRepo,
		LLMConfigRepo: llmConfigRepo,
	})
	require.NoError(t, err)
	var resp updateProjectSettingsRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.False(t, resp.OK)
	require.Contains(t, resp.Error, "ambiguous")

	updated, err := projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Nil(t, updated.DefaultAgentConfigID)
}

func TestBuildChannelProjectActionHandlersUpdateProjectSettings(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := NewProjectService(projectRepo)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Channel Settings Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	model := &models.LLMConfig{Name: "Channel Settings Model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmConfigRepo.Create(ctx, model))

	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID: project.ID, ProjectRepo: projectRepo, ProjectSvc: projectSvc, LLMConfigRepo: llmConfigRepo,
	})
	out, err := handlers["update_project_settings"](ctx, json.RawMessage(`{"default_model_id":"`+model.ID+`","max_workers":1}`))
	require.NoError(t, err)
	var resp updateProjectSettingsRuntimeResponse
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.True(t, resp.OK, resp.Error)
	require.Equal(t, model.ID, resp.DefaultModel.ModelID)
	require.True(t, resp.WorkerLimit.Set)
}

func TestCreateAgentRuntimeCreatesAgentAndRejectsUnsafeInputs(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Runtime Agent Service Project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	out, agent, err := ExecuteCreateAgentRuntime(ctx, CreateAgentRuntimeOptions{
		ProjectID:     project.ID,
		AgentRepo:     agentRepo,
		LLMConfigRepo: llmConfigRepo,
		ProjectRepo:   projectRepo,
		Input: CreateAgentRuntimeInput{
			Name:         "Docs Reviewer",
			Description:  "Reviews docs only.",
			SystemPrompt: "Review documentation changes only.",
			Model:        "inherit",
			Tools:        []string{"Read", "Grep", "read"},
			ScopedFiles:  []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read", "write", "read"}}},
			Scope:        "project",
		},
	})
	require.NoError(t, err, out)
	require.NotNil(t, agent)
	require.Contains(t, out, `"ok":true`)
	require.Equal(t, project.ID, agent.ProjectID)
	require.Equal(t, models.AgentScopeProject, agent.Scope)
	require.True(t, agent.Enabled)
	require.True(t, agent.SelectableAsPrimary)
	require.Equal(t, []string{"Read", "Grep", models.AgentToolScopedFiles}, agent.Tools)
	require.Equal(t, []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read", "write"}}}, agent.ToolConfig.ScopedFiles)

	stored, err := agentRepo.GetUniqueSelectableByName(ctx, "Docs Reviewer")
	require.NoError(t, err)
	require.NotNil(t, stored)

	for _, tc := range []struct {
		name      string
		input     CreateAgentRuntimeInput
		wantError string
	}{
		{name: "duplicate selectable name", input: CreateAgentRuntimeInput{Name: " docs reviewer ", SystemPrompt: "duplicate"}, wantError: "enabled selectable primary agent name already exists"},
		{name: "unknown tool", input: CreateAgentRuntimeInput{Name: "Tool Bad", SystemPrompt: "bad", Tools: []string{"Read", "RootShell"}}, wantError: `unknown tool "RootShell"`},
		{name: "invalid scoped file directory", input: CreateAgentRuntimeInput{Name: "Scoped Bad", SystemPrompt: "bad", ScopedFiles: []models.ScopedFilesConfig{{Directory: "../secrets", Permissions: []string{"read"}}}}, wantError: "scoped file directory must stay inside the project"},
		{name: "invalid scoped file permission", input: CreateAgentRuntimeInput{Name: "Perm Bad", SystemPrompt: "bad", ScopedFiles: []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"admin"}}}}, wantError: `unknown scoped file permission "admin"`},
		{name: "foreign project id", input: CreateAgentRuntimeInput{Name: "Foreign", SystemPrompt: "bad", Scope: "project", ProjectID: "other-project"}, wantError: "project_id must match the current project context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, created, err := ExecuteCreateAgentRuntime(ctx, CreateAgentRuntimeOptions{ProjectID: project.ID, AgentRepo: agentRepo, LLMConfigRepo: llmConfigRepo, ProjectRepo: projectRepo, Input: tc.input})
			require.Error(t, err)
			require.Nil(t, created)
			require.Contains(t, err.Error(), tc.wantError)
			matches, getErr := agentRepo.ListByName(ctx, tc.input.Name)
			require.NoError(t, getErr)
			if tc.name == "duplicate selectable name" {
				require.Len(t, matches, 1)
			} else {
				require.Empty(t, matches)
			}
		})
	}

	_, decodeErr := DecodeCreateAgentRuntimeInput(json.RawMessage(`{"name":"Unsafe","system_prompt":"x","mcp_servers":[{"env":{"API_KEY":"secret"}}]}`))
	require.Error(t, decodeErr)
	require.Contains(t, decodeErr.Error(), `create_agent does not support "mcp_servers"`)
	_, decodeErr = DecodeCreateAgentRuntimeInput(json.RawMessage(`{"name":"Unsafe","system_prompt":"x","plugins":["github@marketplace"]}`))
	require.Error(t, decodeErr)
	require.Contains(t, decodeErr.Error(), `create_agent does not support "plugins"`)
	_, decodeErr = DecodeCreateAgentRuntimeInput(json.RawMessage(`{"id":"protected","name":"Unsafe","system_prompt":"x"}`))
	require.Error(t, decodeErr)
	require.Contains(t, decodeErr.Error(), `create_agent does not support "id"`)
}

func TestChannelUtilityCreateAgentRuntimeAndCompactListAgents(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Channel Runtime Agent Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, AgentRepo: agentRepo, LLMConfigRepo: llmConfigRepo, ProjectRepo: projectRepo})

	createHandler := handlers["create_agent"]
	require.NotNil(t, createHandler)
	out, err := createHandler(ctx, json.RawMessage(`{"name":"Channel Reuser","description":"Reusable from channel.","system_prompt":"Act as a reusable channel-created Agent.","model":"inherit","tools":["Read"]}`))
	require.NoError(t, err, out)
	require.Contains(t, out, `"ok":true`)

	stored, err := agentRepo.GetUniqueSelectableByName(ctx, "Channel Reuser")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, project.ID, stored.ProjectID)
	require.Equal(t, []string{"Read"}, stored.Tools)

	listOut, err := handlers["list_agents"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, listOut, "Channel Reuser")
	require.Contains(t, listOut, "Reusable from channel.")
	require.NotContains(t, listOut, "Act as a reusable channel-created Agent")
}
