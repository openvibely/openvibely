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
						require.Contains(t, handlers, def.Name, "advertised runtime tool must have a handler")
					}
					require.NotContains(t, definitionNames, "create_task")
				}
			} else {
				require.NoError(t, chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, tt.surface, true, handlers))
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

					currentProject, handled, blocked, err := runtime.Executor(ctx, "get_current_project", nil)
					require.NoError(t, err)
					require.True(t, handled)
					require.False(t, blocked)
					require.Equal(t, "Current project: Channel Handler Coverage (id: "+project.ID+")", currentProject)

					gated, handled, blocked, err := runtime.Executor(ctx, "save_automation", nil)
					require.NoError(t, err)
					require.True(t, handled)
					require.True(t, blocked)
					require.Contains(t, gated, "not available on "+string(tt.surface)+" surface")
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

func TestAutomationNotificationCreationRequiresStableIdempotencyKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := &models.Project{Name: "Native duplicate prevention"}
	require.NoError(t, repository.NewProjectRepo(db).Create(ctx, project))
	alertSvc := NewAlertService(repository.NewAlertRepo(db), nil)
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: project.ID, AlertSvc: alertSvc})
	automationCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, OriginTask: true})

	_, err := handlers["create_notification"](automationCtx, json.RawMessage(`{
		"type":"bug_suggestion",
		"title":"Missing key creates a duplicate-prone notification"
	}`))
	require.ErrorContains(t, err, "stable idempotency_key")
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

	createInput := json.RawMessage(`{"project_id":"` + project.ID + `","type":"product_suggestion","title":"Add approval inbox","message":"Review this","body":"Detailed implementation context","metadata":{"component":"alerts"},"idempotency_key":"suggestion:approval-inbox"}`)
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

	duplicateJSON, err := handlers["create_notification"](ctx, createInput)
	require.NoError(t, err)
	var duplicate struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(duplicateJSON), &duplicate))
	require.Equal(t, created.Notification.ID, duplicate.Notification.ID)

	_, err = handlers["list_alerts"](ctx, json.RawMessage(`{"project_id":"`+foreign.ID+`"}`))
	require.ErrorContains(t, err, "outside the caller's authorized project")
	listJSON, err := handlers["list_alerts"](ctx, json.RawMessage(`{"decision_state":"pending","limit":1,"offset":0}`))
	require.NoError(t, err)
	require.Contains(t, listJSON, created.Notification.ID)
	require.Contains(t, listJSON, `"next_offset":1`)
	detailJSON, err := handlers["get_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, detailJSON, "Detailed implementation context")

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

func TestBuildChannelUtilityActionHandlersPersonalityModelAndProjectInfo(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	project := &models.Project{Name: "Info Project", Description: "Details"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Default Model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	require.NoError(t, taskRepo.Create(ctx, &models.Task{ProjectID: project.ID, Title: "Info task", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}))

	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{ProjectID: project.ID, TaskRepo: taskRepo, ProjectRepo: projectRepo, SettingsRepo: settingsRepo, LLMConfigRepo: llmConfigRepo})
	setOut, err := handlers["set_personality"](ctx, json.RawMessage(`{"personality":"no_nonsense_pro"}`))
	require.NoError(t, err)
	require.Contains(t, setOut, "Personality changed")
	getOut, err := handlers["get_personality"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, getOut, "no_nonsense_pro")
	modelsOut, err := handlers["list_models"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, modelsOut, "Default Model")
	projectOut, err := handlers["project_info"](ctx, nil)
	require.NoError(t, err)
	require.Contains(t, projectOut, "Info Project")
	require.Contains(t, projectOut, "Total tasks: 1")
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
	payload, err := json.Marshal(channelCreateSwarmTaskInput{Title: "Shared swarm", Prompt: "Split this across workers", ProjectID: foreignProject.ID, Category: string(models.CategoryBacklog)})
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
	foreignTasks, err := taskRepo.ListByProject(ctx, foreignProject.ID, "")
	require.NoError(t, err)
	require.Empty(t, foreignTasks)
	require.Contains(t, strings.Join(collector.createdLines, "\n"), callbackTaskIDs[0])
	planner, err := taskRepo.FindSwarmChildByRole(ctx, created.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)
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
