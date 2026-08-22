package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestThreadInputRepo_QueuedFIFOAndApply(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	first := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first", Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal}
	second := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second"}
	if err := repo.CreateQueued(ctx, first); err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	if err := repo.CreateQueued(ctx, second); err != nil {
		t.Fatalf("CreateQueued second: %v", err)
	}

	next, err := repo.FindOldestQueuedForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("FindOldestQueuedForTask: %v", err)
	}
	if next == nil || next.ID != first.ID {
		t.Fatalf("oldest queued = %#v, want first %s", next, first.ID)
	}
	if next.Source != models.TaskOriginSystemAgent || next.OriginAgent != models.AgentSystemKindGoal {
		t.Fatalf("oldest queued lineage source=%q origin_agent=%q", next.Source, next.OriginAgent)
	}
	if err := repo.MarkApplied(ctx, first.ID, active.ID, active.ID); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	next, err = repo.FindOldestQueuedForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("FindOldestQueuedForTask after apply: %v", err)
	}
	if next == nil || next.ID != second.ID {
		t.Fatalf("oldest queued after apply = %#v, want second %s", next, second.ID)
	}
}

func TestThreadInputRepo_BindPreExecutionQueuedTaskInputsOnlyBindsUnguardedPendingRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	initial := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "initial"}
	if err := execRepo.Create(ctx, initial); err != nil {
		t.Fatalf("create initial execution: %v", err)
	}
	previous := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "previous"}
	if err := execRepo.Create(ctx, previous); err != nil {
		t.Fatalf("create previous execution: %v", err)
	}

	preExecution := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued before execution exists"}
	alreadyGuarded := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: previous.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "already guarded"}
	if err := repo.CreateQueued(ctx, preExecution); err != nil {
		t.Fatalf("CreateQueued preExecution: %v", err)
	}
	if err := repo.CreateQueued(ctx, alreadyGuarded); err != nil {
		t.Fatalf("CreateQueued alreadyGuarded: %v", err)
	}

	if err := repo.BindPreExecutionQueuedTaskInputs(ctx, task.ID, initial.ID); err != nil {
		t.Fatalf("BindPreExecutionQueuedTaskInputs: %v", err)
	}
	bound, err := repo.GetByID(ctx, preExecution.ID)
	if err != nil {
		t.Fatalf("GetByID preExecution: %v", err)
	}
	if bound.RunExecutionID != initial.ID {
		t.Fatalf("pre-execution input guard = %q, want %q", bound.RunExecutionID, initial.ID)
	}
	guarded, err := repo.GetByID(ctx, alreadyGuarded.ID)
	if err != nil {
		t.Fatalf("GetByID alreadyGuarded: %v", err)
	}
	if guarded.RunExecutionID != previous.ID {
		t.Fatalf("already guarded input was retargeted to %q, want %q", guarded.RunExecutionID, previous.ID)
	}
}

func TestThreadInputRepo_ClaimQueuedValidatesPendingSurface(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)

	chatInput := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "chat queued", ChatMode: models.ChatModePlan}
	if err := repo.CreateQueued(ctx, chatInput); err != nil {
		t.Fatalf("CreateQueued chat: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: chatInput.Content, IsFollowup: true}
	if err := repo.ClaimQueuedForTaskExecution(ctx, chatInput.ID, exec); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected wrong-surface claim conflict, got %v", err)
	}
	if exec.ID != "" {
		if got, err := NewExecutionRepo(db).GetByID(ctx, exec.ID); err != nil || got != nil {
			t.Fatalf("claim rollback left execution got=%#v err=%v", got, err)
		}
	}

	taskInput := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "task queued"}
	if err := repo.CreateQueued(ctx, taskInput); err != nil {
		t.Fatalf("CreateQueued task: %v", err)
	}
	chatTask := &models.Task{ProjectID: project.ID, Title: "Queued Chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: taskInput.Content}
	chatExec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: taskInput.Content}
	if err := repo.ClaimQueuedForChatExecution(ctx, taskInput.ID, chatTask, chatExec, nil, nil, nil); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected wrong-surface chat claim conflict, got %v", err)
	}
	if chatTask.ID != "" {
		if got, err := NewTaskRepo(db, nil).GetByID(ctx, chatTask.ID); err != nil || got != nil {
			t.Fatalf("claim rollback left chat task got=%#v err=%v", got, err)
		}
	}
}

func TestThreadInputRepo_ListRecoverableQueuedTaskIDsFindsPendingInputsBehindTerminalExecutions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	completed := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "original"}
	if err := execRepo.Create(ctx, completed); err != nil {
		t.Fatalf("create completed execution: %v", err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: completed.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued after completed worker"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	ids, err := repo.ListRecoverableQueuedTaskIDs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecoverableQueuedTaskIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("recoverable task ids = %#v, want [%s]", ids, task.ID)
	}

	running := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "another active"}
	if err := execRepo.Create(ctx, running); err != nil {
		t.Fatalf("create running execution: %v", err)
	}
	ids, err = repo.ListRecoverableQueuedTaskIDs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecoverableQueuedTaskIDs with active execution: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("recoverable task ids with active execution = %#v, want none", ids)
	}
}

func TestThreadInputRepo_ListRecoverableQueuedChatProjectIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued while draining", ChatMode: models.ChatModeOrchestrate}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	ids, err := repo.ListRecoverableQueuedChatProjectIDsAfter(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListRecoverableQueuedChatProjectIDsAfter: %v", err)
	}
	if len(ids) != 1 || ids[0] != project.ID {
		t.Fatalf("recoverable chat project ids = %#v, want [%s]", ids, project.ID)
	}

	chatTask := &models.Task{ProjectID: project.ID, Title: "Active Chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active"}
	if err := NewTaskRepo(db, nil).Create(ctx, chatTask); err != nil {
		t.Fatalf("create active chat task: %v", err)
	}
	active := &models.Execution{TaskID: chatTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := NewExecutionRepo(db).Create(ctx, active); err != nil {
		t.Fatalf("create active chat execution: %v", err)
	}
	ids, err = repo.ListRecoverableQueuedChatProjectIDsAfter(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListRecoverableQueuedChatProjectIDsAfter active: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("recoverable chat projects with active execution = %#v, want none", ids)
	}
}

func TestThreadInputRepo_ClaimQueuedForTaskExecutionRequiresNoActiveExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := NewExecutionRepo(db).Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: queued.Content, IsFollowup: true}
	if err := repo.ClaimQueuedForTaskExecution(ctx, queued.ID, promoted); !errors.Is(err, ErrActiveTurnChanged) {
		t.Fatalf("expected active-turn conflict, got %v", err)
	}
	stored, err := repo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("GetByID queued: %v", err)
	}
	if stored.InputStatus != models.ThreadInputPending || stored.RunExecutionID != active.ID {
		t.Fatalf("queued input should remain pending behind active execution, got %#v", stored)
	}
	if promoted.ID != "" {
		if got, err := NewExecutionRepo(db).GetByID(ctx, promoted.ID); err != nil || got != nil {
			t.Fatalf("claim conflict left promoted execution got=%#v err=%v", got, err)
		}
	}
}

func TestThreadInputRepo_ClaimQueuedForTaskExecutionRetargetsRemainingQueuedGuards(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	if err := NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("terminalize task before queued promotion: %v", err)
	}

	first := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first"}
	second := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second"}
	if err := repo.CreateQueued(ctx, first); err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	if err := repo.CreateQueued(ctx, second); err != nil {
		t.Fatalf("CreateQueued second: %v", err)
	}

	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: first.Content, IsFollowup: true}
	if err := repo.ClaimQueuedForTaskExecution(ctx, first.ID, promoted); err != nil {
		t.Fatalf("ClaimQueuedForTaskExecution: %v", err)
	}
	storedSecond, err := repo.GetByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetByID second: %v", err)
	}
	if storedSecond == nil || storedSecond.RunExecutionID != promoted.ID {
		t.Fatalf("remaining queued task input guard = %#v, want promoted exec %s", storedSecond, promoted.ID)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, second.ID, promoted.ID, promoted.ID); err == nil {
		t.Fatalf("remaining queued task input should not steer before promoted execution is running")
	}
	if err := execRepo.MarkRunning(ctx, promoted.ID); err != nil {
		t.Fatalf("mark promoted execution running: %v", err)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, second.ID, promoted.ID, promoted.ID); err != nil {
		t.Fatalf("remaining queued task input should steer against promoted turn: %v", err)
	}
}

func TestThreadInputRepo_ClaimQueuedForChatExecutionRetargetsRemainingQueuedGuards(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	activeTask := &models.Task{ProjectID: project.ID, Title: "Active Chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active"}
	if err := NewTaskRepo(db, nil).Create(ctx, activeTask); err != nil {
		t.Fatalf("create active chat task: %v", err)
	}
	active := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	first := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first", ChatMode: models.ChatModeOrchestrate}
	second := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second", ChatMode: models.ChatModeOrchestrate}
	if err := repo.CreateQueued(ctx, first); err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	if err := repo.CreateQueued(ctx, second); err != nil {
		t.Fatalf("CreateQueued second: %v", err)
	}

	chatTask := &models.Task{ProjectID: project.ID, Title: "Queued Chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: first.Content}
	promoted := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: first.Content}
	if err := repo.ClaimQueuedForChatExecution(ctx, first.ID, chatTask, promoted, nil, nil, nil); err != nil {
		t.Fatalf("ClaimQueuedForChatExecution: %v", err)
	}
	storedSecond, err := repo.GetByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetByID second: %v", err)
	}
	if storedSecond == nil || storedSecond.RunExecutionID != promoted.ID {
		t.Fatalf("remaining queued chat input guard = %#v, want promoted exec %s", storedSecond, promoted.ID)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, second.ID, promoted.ID, promoted.ID); err != nil {
		t.Fatalf("remaining queued chat input should steer against promoted turn: %v", err)
	}
}

func TestThreadInputRepo_ClaimQueuedChatPersistsSlackContextWithClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)

	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeChat,
		ProjectID:      project.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		Content:        "queued slack",
		ChatMode:       models.ChatModeOrchestrate,
		Source:         models.TaskOriginSlack,
		SlackTeamID:    "T1",
		SlackChannelID: "C1",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U1",
	}
	if err := repo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Queued Slack", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: input.Content, CreatedVia: models.TaskOriginSlack}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content}
	if err := repo.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, &models.SlackTaskContext{
		SlackTeamID:    "T1",
		SlackChannelID: "C1",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U1",
	}, nil, nil); err != nil {
		t.Fatalf("ClaimQueuedForChatExecution: %v", err)
	}

	stc, err := NewSlackTaskContextRepo(db).GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if stc == nil || stc.SlackChannelID != "C1" || stc.SlackThreadTS != "1710000000.100000" {
		t.Fatalf("slack context not persisted with queued claim: %#v", stc)
	}
	stored, err := repo.GetByID(ctx, input.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputApplied || stored.RunExecutionID != exec.ID {
		t.Fatalf("queued input should be applied to created execution, got %#v", stored)
	}
}

func TestThreadInputRepo_ClaimQueuedChatPersistsEmailContextWithClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)

	input := &models.ThreadInput{
		Scope:           models.ThreadInputScopeChat,
		ProjectID:       project.ID,
		AgentConfigID:   agent.ID,
		InputMode:       models.ThreadInputModeQueued,
		Content:         "queued email",
		ChatMode:        models.ChatModeOrchestrate,
		Source:          models.TaskOriginEmail,
		EmailFrom:       "sender@example.com",
		EmailMessageID:  "<message-1@example.com>",
		EmailReferences: "<root@example.com>",
		EmailSubject:    "Original subject",
		EmailSessionKey: "sender@example.com|thread-root",
	}
	if err := repo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Queued Email", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: input.Content, CreatedVia: models.TaskOriginEmail}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content}
	if err := repo.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, nil, &models.EmailTaskContext{
		EmailFrom:       "sender@example.com",
		EmailMessageID:  "<message-1@example.com>",
		EmailReferences: "<root@example.com>",
		EmailSubject:    "Original subject",
		EmailSessionKey: "sender@example.com|thread-root",
	}, nil); err != nil {
		t.Fatalf("ClaimQueuedForChatExecution: %v", err)
	}

	etc, err := NewEmailTaskContextRepo(db).GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if etc == nil || etc.EmailFrom != "sender@example.com" || etc.EmailMessageID != "<message-1@example.com>" || etc.EmailReferences != "<root@example.com>" || etc.EmailSubject != "Original subject" || etc.EmailSessionKey != "sender@example.com|thread-root" {
		t.Fatalf("email context not persisted with queued claim: %#v", etc)
	}
	stored, err := repo.GetByID(ctx, input.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputApplied || stored.RunExecutionID != exec.ID {
		t.Fatalf("queued input should be applied to created execution, got %#v", stored)
	}
}

func TestThreadInputRepo_ClaimQueuedChatPersistsDiscordContextWithClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)

	input := &models.ThreadInput{
		Scope:            models.ThreadInputScopeChat,
		ProjectID:        project.ID,
		AgentConfigID:    agent.ID,
		InputMode:        models.ThreadInputModeQueued,
		Content:          "queued discord",
		ChatMode:         models.ChatModeOrchestrate,
		Source:           models.TaskOriginDiscord,
		DiscordChannelID: "chan-1",
		DiscordThreadID:  "thread-1",
		DiscordMessageID: "msg-1",
		DiscordUserID:    "user-1",
	}
	if err := repo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	task := &models.Task{ProjectID: project.ID, Title: "Queued Discord", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: input.Content, CreatedVia: models.TaskOriginDiscord}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content}
	if err := repo.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, nil, nil, &models.DiscordTaskContext{
		DiscordChannelID: "chan-1",
		DiscordThreadID:  "thread-1",
		DiscordMessageID: "msg-1",
		DiscordUserID:    "user-1",
	}); err != nil {
		t.Fatalf("ClaimQueuedForChatExecution: %v", err)
	}

	dtc, err := NewDiscordTaskContextRepo(db).GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if dtc == nil || dtc.DiscordChannelID != "chan-1" || dtc.DiscordThreadID != "thread-1" || dtc.DiscordMessageID != "msg-1" || dtc.DiscordUserID != "user-1" {
		t.Fatalf("discord context not persisted with queued claim: %#v", dtc)
	}
	stored, err := repo.GetByID(ctx, input.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputApplied || stored.RunExecutionID != exec.ID {
		t.Fatalf("queued input should be applied to created execution, got %#v", stored)
	}
}

func TestThreadInputRepo_CancelPendingReportsStaleInputs(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued"}
	if err := repo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	if err := repo.MarkApplied(ctx, input.ID, active.ID, active.ID); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	if _, err := repo.CancelPending(ctx, input.ID); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected stale cancel conflict, got %v", err)
	}
	if _, err := repo.CancelPending(ctx, "missing-input"); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected missing cancel conflict, got %v", err)
	}
}

func TestThreadInputRepo_CancelPendingAllowsUnpreparedSteeringAndPreservesPreparedSteering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		RunExecutionID: active.ID,
		TurnID:         active.ID,
		ExpectedTurnID: active.ID,
		Content:        "active steering",
	}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}
	cancelled, err := repo.CancelPending(ctx, steering.ID)
	if err != nil {
		t.Fatalf("CancelPending unprepared steering: %v", err)
	}
	if cancelled == nil || cancelled.ID != steering.ID || cancelled.TaskID != task.ID || cancelled.ProjectID != project.ID {
		t.Fatalf("CancelPending should return cancelled input metadata, got %#v", cancelled)
	}
	stored, err := repo.GetByID(ctx, steering.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.InputStatus != models.ThreadInputCancelled {
		t.Fatalf("unprepared active steering should be cancellable, got %#v", stored)
	}

	prepared := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		RunExecutionID: active.ID,
		TurnID:         active.ID,
		ExpectedTurnID: active.ID,
		Content:        "prepared steering",
	}
	if err := repo.CreateSteeringForActiveExecution(ctx, prepared, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution prepared: %v", err)
	}
	preparedRows, err := repo.PreparePendingSteering(ctx, active.ID, active.ID)
	if err != nil {
		t.Fatalf("PreparePendingSteering: %v", err)
	}
	if len(preparedRows) != 1 || preparedRows[0].ID != prepared.ID {
		t.Fatalf("expected prepared steering row, got %#v", preparedRows)
	}
	if _, err := repo.CancelPending(ctx, prepared.ID); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected prepared steering cancel conflict, got %v", err)
	}
	stored, err = repo.GetByID(ctx, prepared.ID)
	if err != nil {
		t.Fatalf("GetByID prepared: %v", err)
	}
	if stored.InputStatus != models.ThreadInputPending || stored.InputMode != models.ThreadInputModeSteering || stored.ExpectedTurnID != "" {
		t.Fatalf("prepared steering should remain protected, got %#v", stored)
	}
	if err := repo.CancelPendingForTask(ctx, task.ID); err != nil {
		t.Fatalf("CancelPendingForTask: %v", err)
	}
	stored, err = repo.GetByID(ctx, prepared.ID)
	if err != nil {
		t.Fatalf("GetByID after bulk cancel: %v", err)
	}
	if stored.InputStatus != models.ThreadInputPending || stored.InputMode != models.ThreadInputModeSteering {
		t.Fatalf("bulk cancel should preserve prepared steering, got %#v", stored)
	}
	if err := execRepo.Complete(ctx, active.ID, models.ExecCancelled, "", "", 0, 1); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := repo.CancelPending(ctx, prepared.ID); err != nil {
		t.Fatalf("CancelPending after terminal execution: %v", err)
	}
	stored, err = repo.GetByID(ctx, prepared.ID)
	if err != nil {
		t.Fatalf("GetByID after terminal cancel: %v", err)
	}
	if stored.InputStatus != models.ThreadInputCancelled {
		t.Fatalf("terminal prepared steering should be cancellable, got %#v", stored)
	}
}

func TestThreadInputRepo_ConvertQueuedToSteeringRequiresActiveExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)

	chatTask := &models.Task{ProjectID: project.ID, Title: "Chat", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "active"}
	if err := NewTaskRepo(db, nil).Create(ctx, chatTask); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	active := &models.Execution{TaskID: chatTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	wrongSurfaceActive := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active task"}
	if err := execRepo.Create(ctx, wrongSurfaceActive); err != nil {
		t.Fatalf("create wrong-surface active execution: %v", err)
	}
	guardedForWrongSurface := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, RunExecutionID: wrongSurfaceActive.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "wrong surface", ChatMode: models.ChatModePlan}
	if err := repo.CreateQueued(ctx, guardedForWrongSurface); err != nil {
		t.Fatalf("CreateQueued wrong surface: %v", err)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, guardedForWrongSurface.ID, wrongSurfaceActive.ID, wrongSurfaceActive.ID); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("expected wrong surface active turn conflict, got %v", err)
	}

	unguarded := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "missing guard", ChatMode: models.ChatModePlan}
	if err := repo.CreateQueued(ctx, unguarded); err != nil {
		t.Fatalf("CreateQueued unguarded: %v", err)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, unguarded.ID, active.ID, active.ID); !errors.Is(err, ErrActiveTurnChanged) {
		t.Fatalf("expected missing queued guard conflict, got %v", err)
	}

	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "correct this", ChatMode: models.ChatModePlan}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued guarded: %v", err)
	}

	if _, err := repo.ConvertQueuedToSteering(ctx, queued.ID, active.ID, "stale-turn"); !errors.Is(err, ErrActiveTurnChanged) {
		t.Fatalf("expected stale turn conflict, got %v", err)
	}
	steering, err := repo.ConvertQueuedToSteering(ctx, queued.ID, active.ID, active.ID)
	if err != nil {
		t.Fatalf("ConvertQueuedToSteering: %v", err)
	}
	if steering == nil {
		t.Fatal("expected converted steering input")
	}
	if steering.InputMode != models.ThreadInputModeSteering || steering.InputStatus != models.ThreadInputPending || steering.TurnID != active.ID || steering.ExpectedTurnID != active.ID {
		t.Fatalf("unexpected steering state: %#v", steering)
	}
	if _, err := repo.RequeuePendingSteering(ctx, []string{steering.ID}, active.ID); err != nil {
		t.Fatalf("RequeuePendingSteering: %v", err)
	}
	requeued, err := repo.GetByID(ctx, steering.ID)
	if err != nil {
		t.Fatalf("GetByID requeued: %v", err)
	}
	if requeued.InputMode != models.ThreadInputModeQueued || requeued.InputStatus != models.ThreadInputPending || requeued.TurnID != "" || requeued.RunExecutionID != active.ID {
		t.Fatalf("unexpected requeued state: %#v", requeued)
	}
}

func TestThreadInputRepo_RequeuePendingSteeringSkipsAlreadyAppliedRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	first := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "already applied"}
	if err := repo.CreateSteeringForActiveExecution(ctx, first, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution first: %v", err)
	}
	second := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "still pending"}
	if err := repo.CreateSteeringForActiveExecution(ctx, second, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution second: %v", err)
	}
	if err := repo.MarkApplied(ctx, first.ID, active.ID, active.ID); err != nil {
		t.Fatalf("MarkApplied first: %v", err)
	}
	requeuedInputs, err := repo.RequeuePendingSteering(ctx, []string{first.ID, second.ID}, active.ID)
	if err != nil {
		t.Fatalf("RequeuePendingSteering mixed batch: %v", err)
	}
	if len(requeuedInputs) != 1 || requeuedInputs[0].ID != second.ID {
		t.Fatalf("expected only pending row returned as requeued, got %#v", requeuedInputs)
	}
	storedFirst, err := repo.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetByID first: %v", err)
	}
	if storedFirst.InputStatus != models.ThreadInputApplied || storedFirst.InputMode != models.ThreadInputModeSteering {
		t.Fatalf("already applied row should be unchanged, got %#v", storedFirst)
	}
	storedSecond, err := repo.GetByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetByID second: %v", err)
	}
	if storedSecond.InputStatus != models.ThreadInputPending || storedSecond.InputMode != models.ThreadInputModeQueued || storedSecond.TurnID != "" {
		t.Fatalf("pending row should be requeued, got %#v", storedSecond)
	}
}

func TestThreadInputRepo_RestorePreparedSteeringMakesInputClaimableAgain(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "retry me"}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}
	prepared, err := repo.PreparePendingTextSteering(ctx, active.ID, active.ID)
	if err != nil {
		t.Fatalf("PreparePendingTextSteering: %v", err)
	}
	if len(prepared) != 1 || prepared[0].ID != steering.ID {
		t.Fatalf("expected one prepared steering row, got %#v", prepared)
	}
	preparedAgain, err := repo.PreparePendingTextSteering(ctx, active.ID, active.ID)
	if err != nil {
		t.Fatalf("PreparePendingTextSteering again: %v", err)
	}
	if len(preparedAgain) != 0 {
		t.Fatalf("prepared row should not be claimable before restore, got %#v", preparedAgain)
	}
	if err := repo.RestorePreparedSteering(ctx, []string{steering.ID}, active.ID, active.ID); err != nil {
		t.Fatalf("RestorePreparedSteering: %v", err)
	}
	preparedAfterRestore, err := repo.PreparePendingTextSteering(ctx, active.ID, active.ID)
	if err != nil {
		t.Fatalf("PreparePendingTextSteering after restore: %v", err)
	}
	if len(preparedAfterRestore) != 1 || preparedAfterRestore[0].ID != steering.ID {
		t.Fatalf("expected restored row claimable again, got %#v", preparedAfterRestore)
	}
}

func TestThreadInputRepo_RequeuePendingSteeringForExecutionRecoversUnpreparedRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "created during provider call"}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}
	requeuedInputs, err := repo.RequeuePendingSteeringForExecution(ctx, active.ID)
	if err != nil {
		t.Fatalf("RequeuePendingSteeringForExecution: %v", err)
	}
	if len(requeuedInputs) != 1 || requeuedInputs[0].ID != steering.ID {
		t.Fatalf("expected recovered steering row returned, got %#v", requeuedInputs)
	}
	stored, err := repo.GetByID(ctx, steering.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.InputStatus != models.ThreadInputPending || stored.InputMode != models.ThreadInputModeQueued || stored.TurnID != "" || stored.RunExecutionID != active.ID {
		t.Fatalf("unexpected recovered steering state: %#v", stored)
	}
}

func TestThreadInputRepo_ConvertQueuedToSteeringFailsAfterTurnCompletes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "convert too late"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	if err := execRepo.Complete(ctx, active.ID, models.ExecCompleted, "done", "", 0, 1); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := repo.ConvertQueuedToSteering(ctx, queued.ID, active.ID, active.ID); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("expected completed turn conversion to fail with ErrNoActiveTurn, got %v", err)
	}
	stored, err := repo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.InputMode != models.ThreadInputModeQueued || stored.InputStatus != models.ThreadInputPending {
		t.Fatalf("late conversion should leave input queued/pending for FIFO, got %#v", stored)
	}
}

func TestThreadInputRepo_CreateSteeringForActiveExecutionFailsAfterTurnCompletes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	if err := execRepo.Complete(ctx, active.ID, models.ExecCompleted, "done", "", 0, 1); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: active.ID, Content: "too late"}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, active.ID); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("expected ErrNoActiveTurn, got %v", err)
	}
	pending, err := repo.ListPendingSteering(ctx, active.ID, active.ID)
	if err != nil {
		t.Fatalf("ListPendingSteering: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("late direct steering should not insert pending rows, got %#v", pending)
	}
}

func TestExecutionRepo_FindActiveTaskExecutionTreatsQueuedTaskAsActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	taskRepo := NewTaskRepo(db, nil)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus queued: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	queuedExec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "waiting for worker slot", IsFollowup: true}
	if err := execRepo.Create(ctx, queuedExec); err != nil {
		t.Fatalf("create queued execution: %v", err)
	}

	active, err := execRepo.FindActiveTaskExecution(ctx, task.ID, "")
	if err != nil {
		t.Fatalf("FindActiveTaskExecution: %v", err)
	}
	if active == nil || active.ID != queuedExec.ID {
		t.Fatalf("expected queued running execution to be active, got %#v", active)
	}
	stored, err := execRepo.GetByID(ctx, queuedExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecRunning {
		t.Fatalf("expected queued execution to remain running, got %s", stored.Status)
	}
}

func TestExecutionRepo_RecoverPreRestartRunningTaskExecutionsTerminalizesDirectFollowupWithoutSuccessor(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)

	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "interrupted direct follow-up", IsFollowup: true}
	require.NoError(t, execRepo.Create(ctx, active))
	require.NoError(t, taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued))

	recovered, err := execRepo.RecoverPreRestartRunningTaskExecutions(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	stored, err := execRepo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, stored.Status)
	storedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, storedTask.Status)
	require.Equal(t, models.CategoryBacklog, storedTask.Category)
	activePending, err := taskRepo.ListActivePending(ctx)
	require.NoError(t, err)
	for _, candidate := range activePending {
		if candidate.ID == task.ID {
			t.Fatalf("interrupted direct follow-up task must not be offered for original-prompt dispatch")
		}
	}
}

func TestExecutionRepo_RecoverPreRestartRunningTaskExecutionsTerminalizesQueuedFollowupAndPreservesFIFO(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	inputRepo := NewThreadInputRepo(db)

	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "interrupted follow-up", IsFollowup: true}
	require.NoError(t, execRepo.Create(ctx, active))
	require.NoError(t, taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued))
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "next FIFO follow-up"}
	require.NoError(t, inputRepo.CreateQueued(ctx, queued))

	recovered, err := execRepo.RecoverPreRestartRunningTaskExecutions(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	storedActive, err := execRepo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, storedActive.Status)
	storedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, storedTask.Status)
	require.Equal(t, models.CategoryBacklog, storedTask.Category)

	ids, err := inputRepo.ListRecoverableQueuedTaskIDs(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []string{task.ID}, ids)
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: queued.Content, IsFollowup: true}
	require.NoError(t, inputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, promoted))
	require.ErrorIs(t, inputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, &models.Execution{TaskID: task.ID, Status: models.ExecRunning}), ErrInputNotPending)
}

func TestExecutionRepo_RecoverStaleRunningTaskExecutionsRepairsPendingTaskCrashLeftover(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	taskRepo := NewTaskRepo(db, nil)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
		t.Fatalf("UpdateStatus pending: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	staleExec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "pre-restart run"}
	if err := execRepo.Create(ctx, staleExec); err != nil {
		t.Fatalf("create stale execution: %v", err)
	}
	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: staleExec.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeSteering, InputStatus: models.ThreadInputPending, TurnID: staleExec.ID, ExpectedTurnID: staleExec.ID, Content: "pending steer"}
	if err := NewThreadInputRepo(db).CreateQueued(ctx, steering); err != nil {
		t.Fatalf("create steering input fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE thread_inputs SET input_mode = 'steering', turn_id = ?, expected_turn_id = ? WHERE id = ?`, staleExec.ID, staleExec.ID, steering.ID); err != nil {
		t.Fatalf("convert steering fixture: %v", err)
	}

	recovered, err := execRepo.RecoverStaleRunningTaskExecutions(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunningTaskExecutions: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered execution, got %d", recovered)
	}
	stored, err := execRepo.GetByID(ctx, staleExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecFailed {
		t.Fatalf("expected stale execution failed, got %s", stored.Status)
	}
	input, err := NewThreadInputRepo(db).GetByID(ctx, steering.ID)
	if err != nil {
		t.Fatalf("GetByID input: %v", err)
	}
	if input.InputMode != models.ThreadInputModeQueued || input.InputStatus != models.ThreadInputPending || input.TurnID != "" || input.ExpectedTurnID != "" {
		t.Fatalf("expected stale steering requeued safely, got %#v", input)
	}
}

// TestThreadInputRepo_ListPendingForTask_ExcludesPreparedSteering verifies that
// ListPendingForTask omits prepared/in-flight steering rows (expected_turn_id = NULL).
// These rows were removed from the composer UI via the SSE applied event at prepare
// time. Showing them on page refresh would leave a stale "Steering pending" row that
// the user cannot delete (the DB guard protects in-flight rows from cancellation).
//
// The test is split into two phases:
//  1. Before PreparePendingSteering — the steering row is still pending with
//     expected_turn_id set, so it should appear in the list.
//  2. After PreparePendingSteering — expected_turn_id is cleared (row is in-flight),
//     so it must be hidden from the list.
//
// TestExecutionRepo_RecoverStaleRunningTaskExecutionsPreservesRunningScheduledTask
// is a regression test for a bug where recurring scheduled tasks (e.g. the
// built-in "System: Memory Consolidation" task and any other cron-style task
// left in category="scheduled" while status="running") had their live,
// in-progress execution incorrectly reaped by the stale-recovery sweep. The
// sweep previously treated any task whose category wasn't "active" as
// inactive/stale, but the worker pool legitimately runs tasks in both the
// "active" and "scheduled" categories (see WorkerService.dispatchNext).
// RecoverStaleRunningTaskExecutions is called on virtually every
// FindActiveTaskExecution/HasActiveTaskExecution lookup (not just at server
// startup), so this bug could fail a task's execution out from under it
// mid-run, while the worker goroutine kept executing obliviously — leaving
// an inconsistent "failed execution but task still running" state.
func TestExecutionRepo_RecoverStaleRunningTaskExecutionsPreservesRunningScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "System: Memory Consolidation", Category: models.CategoryScheduled, Status: models.StatusRunning, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create scheduled task: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	liveExec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "in-progress consolidation run"}
	if err := execRepo.Create(ctx, liveExec); err != nil {
		t.Fatalf("create live execution: %v", err)
	}

	recovered, err := execRepo.RecoverStaleRunningTaskExecutions(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunningTaskExecutions: %v", err)
	}
	if recovered != 0 {
		t.Fatalf("expected 0 recovered executions for a legitimately running scheduled task, got %d", recovered)
	}
	stored, err := execRepo.GetByID(ctx, liveExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecRunning {
		t.Fatalf("expected live scheduled-task execution to remain running, got %s (error=%q)", stored.Status, stored.ErrorMessage)
	}
}

func TestExecutionRepo_CreateDirectTaskFollowupReactivatesSwarmChildBeforeRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	parent := &models.Task{ProjectID: project.ID, Title: "Swarm parent", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "parent", SwarmRole: models.SwarmRoleParent, SwarmStatus: "current"}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("create swarm parent: %v", err)
	}
	child := &models.Task{ProjectID: project.ID, Title: "Completed swarm worker", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "worker", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "completed"}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatalf("create swarm child: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	liveExec := &models.Execution{TaskID: child.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "channel follow-up", IsFollowup: true}
	if err := execRepo.CreateDirectTaskFollowup(ctx, liveExec); err != nil {
		t.Fatalf("CreateDirectTaskFollowup: %v", err)
	}

	recovered, err := execRepo.RecoverStaleRunningTaskExecutions(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunningTaskExecutions: %v", err)
	}
	if recovered != 0 {
		t.Fatalf("expected 0 recovered executions for reactivated swarm child follow-up, got %d", recovered)
	}
	stored, err := execRepo.GetByID(ctx, liveExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecQueued {
		t.Fatalf("expected active swarm child follow-up execution to wait queued, got %s (error=%q)", stored.Status, stored.ErrorMessage)
	}
	updatedChild, err := taskRepo.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetByID child: %v", err)
	}
	if updatedChild.Status != models.StatusQueued || updatedChild.Category != models.CategoryActive {
		t.Fatalf("expected child reactivated before execution was exposed, got status=%s category=%s", updatedChild.Status, updatedChild.Category)
	}
}

func TestExecutionRepo_RecoverStaleRunningTaskExecutionsReproducesDirectFollowupRace(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	parent := &models.Task{ProjectID: project.ID, Title: "Swarm parent", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "parent", SwarmRole: models.SwarmRoleParent, SwarmStatus: "current"}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("create swarm parent: %v", err)
	}
	child := &models.Task{ProjectID: project.ID, Title: "Completed swarm worker", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "worker", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "completed"}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatalf("create swarm child: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	liveExec := &models.Execution{TaskID: child.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "direct follow-up", IsFollowup: true}
	if err := execRepo.Create(ctx, liveExec); err != nil {
		t.Fatalf("create live execution with old direct-start ordering: %v", err)
	}

	recovered, err := execRepo.RecoverStaleRunningTaskExecutions(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunningTaskExecutions: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected old direct-start ordering to reproduce 1 stale recovery, got %d", recovered)
	}
	stored, err := execRepo.GetByID(ctx, liveExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecFailed || !strings.Contains(stored.ErrorMessage, "Recovered stale running execution") {
		t.Fatalf("expected old direct-start race to fail execution with stale recovery error, got status=%s error=%q", stored.Status, stored.ErrorMessage)
	}
}

func TestExecutionRepo_RecoverStaleRunningTaskExecutionsStillRecoversInactiveSwarmChild(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	taskRepo := NewTaskRepo(db, nil)
	parent := &models.Task{ProjectID: project.ID, Title: "Swarm parent", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "parent", SwarmRole: models.SwarmRoleParent, SwarmStatus: "running"}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("create swarm parent: %v", err)
	}
	child := &models.Task{ProjectID: project.ID, Title: "Inactive swarm worker", Category: models.CategoryBacklog, Status: models.StatusRunning, Prompt: "worker", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "running"}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatalf("create inactive swarm child: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	staleExec := &models.Execution{TaskID: child.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "orphaned work"}
	if err := execRepo.Create(ctx, staleExec); err != nil {
		t.Fatalf("create stale execution: %v", err)
	}

	recovered, err := execRepo.RecoverStaleRunningTaskExecutions(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunningTaskExecutions: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered execution for inactive swarm child, got %d", recovered)
	}
	stored, err := execRepo.GetByID(ctx, staleExec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecFailed || !strings.Contains(stored.ErrorMessage, "Recovered stale running execution") {
		t.Fatalf("expected inactive swarm child execution recovered as failed, got status=%s error=%q", stored.Status, stored.ErrorMessage)
	}
}

func TestThreadInputRepo_ListPendingForTask_ExcludesPreparedSteering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	// A normal queued follow-up — must always appear.
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "normal queued"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	// A steering row — before PreparePendingSteering it has expected_turn_id set
	// and should be visible in the pending list.
	steer := &models.ThreadInput{
		Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeSteering,
		InputStatus: models.ThreadInputPending, RunExecutionID: active.ID,
		TurnID: active.ID, ExpectedTurnID: active.ID, Content: "rebase against main",
	}
	if err := repo.CreateSteeringForActiveExecution(ctx, steer, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}

	// Phase 1: unprepared steering should be in the list.
	pending, err := repo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingForTask before prepare: %v", err)
	}
	foundBefore := map[string]bool{}
	for _, p := range pending {
		foundBefore[p.ID] = true
	}
	if !foundBefore[queued.ID] {
		t.Errorf("phase 1: queued follow-up should appear in pending list, got %+v", pending)
	}
	if !foundBefore[steer.ID] {
		t.Errorf("phase 1: unprepared steering row (expected_turn_id set) should appear in pending list, got %+v", pending)
	}

	// Prepare the steering row (simulates what PreparePendingTextSteering does inside
	// the LLM steering callback — clears expected_turn_id).
	preparedRows, err := repo.PreparePendingSteering(ctx, active.ID, active.ID)
	if err != nil || len(preparedRows) == 0 {
		t.Fatalf("PreparePendingSteering: err=%v rows=%d", err, len(preparedRows))
	}

	// Phase 2: prepared/in-flight steering must NOT appear in the list.
	pending, err = repo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingForTask after prepare: %v", err)
	}
	foundAfter := map[string]bool{}
	for _, p := range pending {
		foundAfter[p.ID] = true
	}
	if !foundAfter[queued.ID] {
		t.Errorf("phase 2: queued follow-up must still appear after steering is prepared, got %+v", pending)
	}
	if foundAfter[steer.ID] {
		t.Errorf("phase 2: prepared/in-flight steering (expected_turn_id=NULL) must NOT appear in pending list — it was already sent to the provider, got %+v", pending)
	}
}

// TestThreadInputRepo_ListPendingForChat_ExcludesPreparedSteering is the chat-scope
// equivalent of the task test above.
func TestThreadInputRepo_ListPendingForChat_ExcludesPreparedSteering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)

	chatTask := &models.Task{ProjectID: project.ID, Title: "Chat Task", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "chat"}
	if err := NewTaskRepo(db, nil).Create(ctx, chatTask); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	active := &models.Execution{TaskID: chatTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active chat"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	// Normal queued chat input.
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "normal queued chat"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	// Chat steering row (unprepared: expected_turn_id set, should be visible before prepare).
	steer := &models.ThreadInput{
		Scope: models.ThreadInputScopeChat, ProjectID: project.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeSteering,
		InputStatus: models.ThreadInputPending, RunExecutionID: active.ID,
		TurnID: active.ID, ExpectedTurnID: active.ID, Content: "steer chat",
	}
	if err := repo.CreateSteeringForActiveExecution(ctx, steer, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}

	// Phase 1: before prepare — both queued and steering visible.
	pending, err := repo.ListPendingForChat(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListPendingForChat before prepare: %v", err)
	}
	foundBefore := map[string]bool{}
	for _, p := range pending {
		foundBefore[p.ID] = true
	}
	if !foundBefore[queued.ID] {
		t.Errorf("phase 1: queued chat input should appear in pending list, got %+v", pending)
	}
	if !foundBefore[steer.ID] {
		t.Errorf("phase 1: unprepared chat steering should appear in pending list, got %+v", pending)
	}

	// Prepare (in-flight): clears expected_turn_id.
	if _, err := repo.PreparePendingSteering(ctx, active.ID, active.ID); err != nil {
		t.Fatalf("PreparePendingSteering: %v", err)
	}

	// Phase 2: after prepare — steering must be hidden.
	pending, err = repo.ListPendingForChat(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListPendingForChat after prepare: %v", err)
	}
	foundAfter := map[string]bool{}
	for _, p := range pending {
		foundAfter[p.ID] = true
	}
	if !foundAfter[queued.ID] {
		t.Errorf("phase 2: queued chat input must still appear, got %+v", pending)
	}
	if foundAfter[steer.ID] {
		t.Errorf("phase 2: prepared/in-flight chat steering must NOT appear in pending list, got %+v", pending)
	}
}

// TestThreadInputRepo_ListPendingForTask_PreparedSteeringReappearsAfterRequeue verifies
// that a requeued (recovered) steering row is visible again after provider failure.
func TestThreadInputRepo_ListPendingForTask_PreparedSteeringReappearsAfterRequeue(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}

	steering := &models.ThreadInput{
		Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeSteering,
		InputStatus: models.ThreadInputPending, RunExecutionID: active.ID,
		TurnID: active.ID, ExpectedTurnID: active.ID, Content: "rebase against main",
	}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, active.ID); err != nil {
		t.Fatalf("CreateSteeringForActiveExecution: %v", err)
	}

	// Prepare (in-flight) — should be hidden from UI.
	if _, err := repo.PreparePendingSteering(ctx, active.ID, active.ID); err != nil {
		t.Fatalf("PreparePendingSteering: %v", err)
	}
	pending, err := repo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingForTask after prepare: %v", err)
	}
	for _, p := range pending {
		if p.ID == steering.ID {
			t.Fatal("prepared in-flight steering must not appear in pending list")
		}
	}

	// Simulate provider failure: requeue the steering.
	requeued, err := repo.RequeuePendingSteeringForExecution(ctx, active.ID)
	if err != nil || len(requeued) == 0 {
		t.Fatalf("RequeuePendingSteeringForExecution: err=%v rows=%d", err, len(requeued))
	}

	// After requeue, the row is now input_mode='queued' and must reappear.
	pending, err = repo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingForTask after requeue: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.ID == steering.ID {
			found = true
			if p.InputMode != models.ThreadInputModeQueued {
				t.Errorf("requeued steering should have input_mode=queued, got %s", p.InputMode)
			}
		}
	}
	if !found {
		t.Errorf("requeued steering row should reappear in pending list after provider failure, got %+v", pending)
	}
}

func TestExecutionRepo_TaskFollowupAdmissionConcurrentDirectStartsExactlyOnce(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	if err := NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
		t.Fatalf("set task pending: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)

	const submissions = 12
	type result struct {
		content string
		started bool
		err     error
	}
	results := make(chan result, submissions)
	var wg sync.WaitGroup
	for i := 0; i < submissions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := fmt.Sprintf("direct race follow-up %02d", i)
			exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: content, IsFollowup: true}
			started, err := execRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, &models.ThreadInput{AgentConfigID: agent.ID, Content: content})
			results <- result{content: content, started: started, err: err}
		}(i)
	}
	wg.Wait()
	close(results)
	startedCount := 0
	allContent := make(map[string]bool, submissions)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		allContent[result.content] = true
		if result.started {
			startedCount++
		}
	}
	if startedCount != 1 {
		t.Fatalf("expected exactly one direct admission winner, got %d", startedCount)
	}
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 1 || execs[0].Status != models.ExecQueued {
		t.Fatalf("expected one queued direct execution, got %#v err=%v", execs, err)
	}
	if err := execRepo.MarkRunning(ctx, execs[0].ID); err != nil {
		t.Fatalf("mark direct execution running: %v", err)
	}
	promoted, err := execRepo.GetByID(ctx, execs[0].ID)
	if err != nil {
		t.Fatalf("get promoted execution: %v", err)
	}
	if promoted.Status != models.ExecRunning {
		t.Fatalf("expected promoted direct execution to be running, got %s", promoted.Status)
	}
	pending, err := NewThreadInputRepo(db).ListPendingForTask(ctx, task.ID)
	if err != nil || len(pending) != submissions-1 {
		t.Fatalf("expected %d queued losers, got %#v err=%v", submissions-1, pending, err)
	}
	delete(allContent, execs[0].PromptSent)
	for _, input := range pending {
		delete(allContent, input.Content)
	}
	if len(allContent) != 0 {
		t.Fatalf("lost concurrent submissions: %#v", allContent)
	}
}

func TestExecutionRepo_TaskFollowupAdmissionConcurrentSubmissionsAreDurable(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)

	const submissions = 12
	errs := make(chan error, submissions)
	var wg sync.WaitGroup
	for i := 0; i < submissions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := fmt.Sprintf("concurrent follow-up %02d", i)
			exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: content, IsFollowup: true}
			started, err := execRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, &models.ThreadInput{AgentConfigID: agent.ID, Content: content})
			if err != nil {
				errs <- err
				return
			}
			if started {
				errs <- fmt.Errorf("submission %d started behind claimed task", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	pending, err := NewThreadInputRepo(db).ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != submissions {
		t.Fatalf("expected %d durable inputs, got %d", submissions, len(pending))
	}
	seen := make(map[string]bool, submissions)
	for _, input := range pending {
		if seen[input.Content] {
			t.Fatalf("duplicate queued input %q", input.Content)
		}
		seen[input.Content] = true
	}
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 0 {
		t.Fatalf("expected no direct execution behind claim, got %#v err=%v", execs, err)
	}
}

func TestExecutionRepo_TaskFollowupAdmissionSerializesClaimAndFIFORecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	inputRepo := NewThreadInputRepo(db)

	prior := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "prior turn"}
	if err := execRepo.Create(ctx, prior); err != nil {
		t.Fatalf("create prior execution: %v", err)
	}
	for _, content := range []string{"first follow-up", "second follow-up"} {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: content, IsFollowup: true}
		input := &models.ThreadInput{AgentConfigID: agent.ID, Content: content}
		started, err := execRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, input)
		if err != nil {
			t.Fatalf("admit %q: %v", content, err)
		}
		if started {
			t.Fatalf("claimed ordinary run must queue %q", content)
		}
	}
	pending, err := inputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 || pending[0].Content != "first follow-up" || pending[1].Content != "second follow-up" {
		t.Fatalf("expected FIFO queued inputs, got %#v", pending)
	}

	reset, err := NewTaskRepo(db, nil).ResetOrphanedRunning(ctx)
	if err != nil || reset != 1 {
		t.Fatalf("recover claimed task: reset=%d err=%v", reset, err)
	}
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: pending[0].Content, IsFollowup: true}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, pending[0].ID, promoted); err != nil {
		t.Fatalf("promote oldest input: %v", err)
	}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, pending[0].ID, &models.Execution{TaskID: task.ID, Status: models.ExecRunning}); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected exactly-once promotion guard, got %v", err)
	}
	remaining, err := inputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil || len(remaining) != 1 || remaining[0].Content != "second follow-up" {
		t.Fatalf("expected second FIFO input to remain pending, got %#v err=%v", remaining, err)
	}
}

func TestExecutionRepo_TaskFollowupAdmissionWinsBeforeSchedulerClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	taskRepo := NewTaskRepo(db, nil)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	agent := createThreadInputLLMConfig(t, ctx, db)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "direct follow-up", IsFollowup: true}
	input := &models.ThreadInput{AgentConfigID: agent.ID, Content: exec.PromptSent}
	started, err := NewExecutionRepo(db).CreateDirectTaskFollowupOrQueue(ctx, exec, input)
	if err != nil || !started {
		t.Fatalf("direct admission: started=%v err=%v", started, err)
	}
	if _, claimed, err := taskRepo.ClaimTaskForDispatch(ctx, task.ID); err != nil || claimed {
		t.Fatalf("scheduler must not claim behind direct follow-up: claimed=%v err=%v", claimed, err)
	}
}

func TestThreadInputRepo_QueuedPromotionBlockedByOrdinaryTaskClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	inputRepo := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, Content: "queued"}
	if err := inputRepo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("create queued input: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content, IsFollowup: true}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, input.ID, exec); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("claimed ordinary task must block queued promotion, got %v", err)
	}
	if exec.ID != "" {
		t.Fatalf("blocked promotion created execution %s", exec.ID)
	}
}

func createThreadInputProject(t *testing.T, ctx context.Context, db *sql.DB) *models.Project {
	t.Helper()
	repo := NewProjectRepo(db)
	project := &models.Project{Name: "Thread Input Project", RepoPath: t.TempDir()}
	if err := repo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func createThreadInputTask(t *testing.T, ctx context.Context, db *sql.DB, projectID string) *models.Task {
	t.Helper()
	repo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: projectID, Title: "Thread Input Task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "test"}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func createThreadInputLLMConfig(t *testing.T, ctx context.Context, db *sql.DB) *models.LLMConfig {
	t.Helper()
	repo := NewLLMConfigRepo(db)
	agent, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default agent: %v", err)
	}
	if agent == nil {
		t.Fatal("expected default agent")
	}
	return agent
}

func TestThreadInputRepo_CancelPendingForProjectScopesTaskThreadInputs(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewThreadInputRepo(db)
	owner := createThreadInputProject(t, ctx, db)
	foreign := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, owner.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	execRepo := NewExecutionRepo(db)
	active := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "active"}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatalf("create active execution: %v", err)
	}
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: owner.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued"}
	if err := repo.CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}

	if _, err := repo.CancelPendingForProject(ctx, input.ID, foreign.ID); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("expected foreign-project stale cancel, got %v", err)
	}
	stored, err := repo.GetByID(ctx, input.ID)
	if err != nil {
		t.Fatalf("GetByID after foreign cancel: %v", err)
	}
	if stored == nil || stored.InputStatus != models.ThreadInputPending {
		t.Fatalf("foreign cancel should leave row pending, got %#v", stored)
	}
	cancelled, err := repo.CancelPendingForProject(ctx, input.ID, owner.ID)
	if err != nil {
		t.Fatalf("same-project CancelPendingForProject: %v", err)
	}
	if cancelled == nil || cancelled.ID != input.ID || cancelled.ProjectID != owner.ID || cancelled.TaskID != task.ID || cancelled.InputStatus != models.ThreadInputCancelled {
		t.Fatalf("same-project cancel returned unexpected row: %#v", cancelled)
	}
}
