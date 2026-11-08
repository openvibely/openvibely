package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

const pageConversationCategory = models.CategoryPageChat

type pageChatQueued struct{ Input models.ThreadInput }

func (e *pageChatQueued) Error() string { return "message queued" }

type pageQueuedContext struct {
	Principal string                    `json:"principal"`
	Snapshot  *pageintegration.Snapshot `json:"snapshot,omitempty"`
}

var errPageTurnUnauthorized = errors.New("page turn submitting identity is unavailable")

var errPageChatBusy = errors.New("a response is already in progress for this page")

// pageChatInput carries optional composer selections for one side-chat turn.
type pageChatInput struct {
	AgentID             string
	AttachmentSessionID string
	AllowQueue          bool
}

// pageConversationTask resumes a persisted page conversation or creates it on first use.
func (h *Handler) pageConversationTask(ctx context.Context, binding pageintegration.Conversation, message, agentID string) (*models.Task, error) {
	store := binding.Store
	taskID, err := store.TaskID(ctx)
	if err != nil {
		return nil, err
	}
	if taskID != "" {
		task, err := h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task == nil || task.ProjectID != binding.Page.ProjectID {
			return nil, errors.New("invalid page conversation binding")
		}
		return task, nil
	}
	task := &models.Task{
		ProjectID: binding.Page.ProjectID,
		Title:     fmt.Sprintf("Page chat: %s [%s]", binding.Page.Title, binding.Page.Key()),
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  pageConversationCategory,
		AgentID:   &agentID,
	}
	if agentID == "" {
		task.AgentID = nil
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		return nil, err
	}
	if err := store.BindTask(ctx, task.ID); err != nil {
		_ = h.taskRepo.Delete(ctx, task.ID)
		return nil, err
	}
	return task, nil
}

// startPageChatTurn runs one side-chat turn through the shared streaming
// pipeline with a per-turn page context snapshot and server-resolved page tools.
func (h *Handler) startPageChatTurn(c echo.Context, binding pageintegration.Conversation, message string, input pageChatInput) (*models.Execution, []models.ChatAttachment, error) {
	if err := h.requirePageAccess(c.Request().Context(), binding.Page.ProjectID, authz.Edit); err != nil {
		return nil, nil, err
	}
	if input.AttachmentSessionID != "" && !isValidPendingAttachmentSessionID(input.AttachmentSessionID) {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "invalid attachment session ID")
	}
	ctx, done, err := binding.Begin(c.Request().Context())
	if err != nil {
		return nil, nil, err
	}
	defer done()
	ref := binding.Page
	integration, err := binding.Resolve(ctx)
	if err != nil {
		return nil, nil, err
	}
	if integration.Ref().Key() != ref.Key() || integration.Ref().ProjectID != ref.ProjectID {
		return nil, nil, errors.New("page conversation identity changed")
	}
	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		return nil, nil, err
	}
	if !hasModels {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "configure a model before chatting")
	}
	projectID := binding.Page.ProjectID
	agent, err := h.selectTaskAgent(ctx, projectID, input.AgentID, message, hasPendingImages(input.AttachmentSessionID))
	if err != nil {
		return nil, nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.applyConversationEffort(c, "", agent); err != nil {
		return nil, nil, err
	}
	lock := persistence.LockTaskLifecycle(binding.LockKey())
	defer lock()
	task, err := h.pageConversationTask(ctx, binding, message, agent.ID)
	if err != nil {
		return nil, nil, err
	}
	active, err := h.pageChatActiveExecution(ctx, task.ID)
	if err != nil {
		return nil, nil, err
	}
	turn := pageQueuedContext{Principal: h.authPrincipalID(c)}
	if snap, ok := binding.Contexts.Get(turn.Principal, ref); ok {
		turn.Snapshot = &snap
	}
	var pending *models.ThreadInput
	if h.threadInputRepo != nil {
		pending, err = h.threadInputRepo.FindOldestQueuedForTask(ctx, task.ID)
		if err != nil {
			return nil, nil, err
		}
	}
	if active != nil || pending != nil || task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		if !input.AllowQueue || h.threadInputRepo == nil {
			return nil, nil, errPageChatBusy
		}
		queued := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: ref.ProjectID, TaskID: task.ID, AgentConfigID: agent.ID, ReasoningEffort: agent.ReasoningEffort, ServiceTier: agent.EffectiveServiceTier(), InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: message, AttachmentSessionID: input.AttachmentSessionID, Source: models.TaskOriginWeb}
		if active != nil {
			queued.RunExecutionID = active.ID
		}
		if err = h.threadInputRepo.CreateQueued(ctx, &queued); err != nil {
			return nil, nil, err
		}
		raw, _ := json.Marshal(turn)
		if err = binding.Store.SaveQueuedContext(ctx, queued.ID, string(raw)); err != nil {
			_, _ = h.threadInputRepo.CancelPending(ctx, queued.ID)
			return nil, nil, err
		}
		if active == nil {
			h.goStartNextQueuedTurnAfter(streamingResponseParams{ProjectID: binding.Page.ProjectID, TaskID: task.ID, PageConversation: &binding}, "")
		}
		return nil, nil, &pageChatQueued{Input: queued}
	}
	if task.Status != models.StatusPending {
		if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			return nil, nil, err
		}
	}
	claimed, err := h.taskRepo.ClaimTask(ctx, task.ID)
	if err != nil {
		return nil, nil, err
	}
	if !claimed {
		return nil, nil, errPageChatBusy
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: message, IsFollowup: true}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		_ = h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed)
		return nil, nil, err
	}
	attachments, err := h.launchPageChat(ctx, binding, task, exec, agent, message, input.AttachmentSessionID, turn)
	return exec, attachments, err
}

func (h *Handler) launchPageChat(ctx context.Context, binding pageintegration.Conversation, task *models.Task, exec *models.Execution, agent *models.LLMConfig, message, sessionID string, turn pageQueuedContext) (attachments []models.ChatAttachment, launchErr error) {
	defer func() {
		if launchErr != nil {
			h.completeWithFailure(context.Background(), exec.ID, task.ID, launchErr.Error(), 0)
		}
	}()
	params, err := h.pageChatParams(ctx, binding, task, exec, agent, message, turn)
	if err != nil {
		return nil, err
	}
	var attachmentContext string
	var images []models.Attachment
	var chatAttachments []models.ChatAttachment
	if sessionID != "" {
		var attErr error
		attachmentContext, images, chatAttachments, attErr = h.processAttachmentsWithReturn(ctx, sessionID, exec.ID)
		if attErr != nil {
			applog.Infof("[page-chat] attachments exec=%s: %v", exec.ID, attErr)
			attachmentContext = fmt.Sprintf("Attachment processing error: %v", attErr)
		}
	}
	params.AttachmentContext = attachmentContext
	params.ImageAttachments = images
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.TaskThreadExecutionStarted, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: exec.ID, Message: message})
	}
	if err := h.startStreamingResponse(params); err != nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return chatAttachments, nil
}

// Persist the submitting identity even when the page has shared no context.
// Embedding Snapshot retains the existing turn-context JSON shape.
type pageTurnContext struct {
	pageintegration.Snapshot
	Principal  string `json:"principal"`
	HasContext bool   `json:"has_context"`
}

func (h *Handler) pageChatParams(ctx context.Context, binding pageintegration.Conversation, task *models.Task, exec *models.Execution, agent *models.LLMConfig, message string, turn pageQueuedContext) (streamingResponseParams, error) {
	ctx, err := h.pagePrincipalContext(ctx, turn.Principal, binding.Page.ProjectID, authz.Edit)
	if err != nil {
		return streamingResponseParams{}, err
	}
	integration, err := binding.Resolve(ctx)
	if err != nil {
		return streamingResponseParams{}, err
	}
	saved := pageTurnContext{Snapshot: pageintegration.Snapshot{Page: binding.Page}, Principal: turn.Principal, HasContext: turn.Snapshot != nil}
	if turn.Snapshot != nil {
		saved.Snapshot = *turn.Snapshot
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return streamingResponseParams{}, err
	}
	if err = binding.Store.SaveSnapshot(ctx, exec.ID, string(raw)); err != nil {
		return streamingResponseParams{}, err
	}
	history, err := h.execRepo.ListByTaskChronologicalLimit(persistence.WithModelHistory(ctx, agent.Provider), task.ID, taskThreadHistoryLimit)
	if err != nil {
		return streamingResponseParams{}, err
	}
	runtime := pageintegration.BuildRuntimeTools(integration, binding.Resolve, pageintegration.Caller{PrincipalID: turn.Principal})
	editContext := ""
	workDir := h.resolveWorkDir(ctx, binding.Page.ProjectID)
	if binding.Page.Kind == pageplugins.Kind && h.pagePlugins != nil {
		edits, pageDir, err := h.pageEditTools(ctx, binding, turn.Principal)
		if err != nil {
			return streamingResponseParams{}, err
		}
		workDir = pageDir
		runtime = llmcontracts.CompositeRuntimeTools(edits, runtime)
		editContext = "Edit this custom page directly using the provided coding tools. Its instance-owned files persist across replies and restarts. Successful changes automatically update the page; no apply or reload tool is needed. Do not create a separate task or ask for installation or page approval. Read existing files first. Build backend source with shell as needed; failed commands retain edited files and the existing published page. Group related edits and builds into one shell command when intermediate changes would be invalid. Only claim an update is live when the tool reports success."
	}
	return streamingResponseParams{
		CancellationContext: binding.Lifetime(), ExecID: exec.ID, TaskID: task.ID, Message: message, Agent: *agent,
		ChatHistory: filterChatHistory(history, exec.ID), ProjectID: binding.Page.ProjectID, PrincipalID: turn.Principal,
		SystemContext: combineContexts(editContext, combineContexts(h.getPersonalityContext(ctx, binding.Page.ProjectID), pageintegration.RenderContextForPrompt(binding.Page, turn.Snapshot, integration.Operations()))),
		WorkDir:       workDir, TaskScopedChat: true, PageConversation: &binding,
		ChatMode: models.ChatModeOrchestrate, Surface: chatcontrol.SurfaceWeb,
		RuntimeTools: runtime,
	}, nil
}

func (h *Handler) authorizePageTurn(ctx context.Context, principal, projectID string) error {
	if _, err := h.pagePrincipalContext(ctx, principal, projectID, authz.Edit); err != nil {
		if errors.Is(err, authz.ErrDenied) {
			return errPageTurnUnauthorized
		}
		return err
	}
	if principal == "" {
		return errPageTurnUnauthorized
	}
	if h.organizationSvc != nil && strings.HasPrefix(principal, "user:") {
		user, err := h.organizationSvc.Store.UserByID(ctx, strings.TrimPrefix(principal, "user:"))
		if err != nil {
			return err
		}
		if user == nil || user.Disabled || user.Role == "" {
			return errPageTurnUnauthorized
		}
	}
	return nil
}

func (h *Handler) cancelPageConversation(ctx context.Context, binding pageintegration.Conversation, expectedTurn ...string) error {
	lock := persistence.LockTaskLifecycle(binding.LockKey())
	defer lock()
	taskID, err := binding.Store.TaskID(ctx)
	if err != nil {
		return err
	}
	if taskID == "" {
		return nil
	}
	if len(expectedTurn) > 0 && expectedTurn[0] != "" {
		active, err := h.pageChatActiveExecution(ctx, taskID)
		if err != nil {
			return err
		}
		if active == nil || active.ID != expectedTurn[0] {
			return nil
		}
	}
	if h.threadInputRepo != nil {
		if err := h.threadInputRepo.CancelPendingForTask(ctx, taskID); err != nil {
			return err
		}
	}
	observed, cutoff, err := h.taskSvc.ObserveTaskCancellation(ctx, taskID)
	if err != nil {
		return err
	}
	if observed == nil {
		return nil
	}
	if observed.Status != models.StatusRunning && observed.Status != models.StatusQueued {
		return nil
	}
	if err := h.taskSvc.CancelTaskObserved(ctx, observed, cutoff); err != nil {
		return err
	}
	if err := h.taskRepo.UpdateCategory(ctx, taskID, pageConversationCategory); err != nil {
		return err
	}
	h.cancelActiveExecutionsAndPublish(ctx, taskID, "PageChatStop", cutoff)
	return nil
}

func (h *Handler) loadPageChatWindow(ctx context.Context, binding pageintegration.Conversation, before string) (*models.Task, []models.Execution, bool, error) {
	taskID, err := binding.Store.TaskID(ctx)
	if err != nil || taskID == "" {
		return nil, nil, false, err
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, nil, false, err
	}
	if task == nil || task.ProjectID != binding.Page.ProjectID {
		return nil, nil, false, errors.New("invalid page conversation binding")
	}
	executions, hasEarlier, err := h.loadTaskExecutionWindow(ctx, taskID, before, 50)
	return task, executions, hasEarlier, err
}

// Page conversations have their own category, outside the task-thread lookup.
func (h *Handler) pageChatActiveExecution(ctx context.Context, taskID string) (*models.Execution, error) {
	executions, err := h.execRepo.ListByTaskChronologicalLimit(ctx, taskID, 1)
	if err != nil {
		return nil, err
	}
	for i := range executions {
		if executions[i].Status == models.ExecRunning {
			return &executions[i], nil
		}
	}
	return nil, nil
}

// Promote through the shared durable input queue, retaining page authorization,
// lifetime and the context captured when the message was submitted.
func (h *Handler) startNextQueuedPageTurn(ctx context.Context, binding pageintegration.Conversation) {
	if h.threadInputRepo == nil {
		return
	}
	ctx, done, err := binding.Begin(ctx)
	if err != nil {
		return
	}
	defer done()
	lock := persistence.LockTaskLifecycle(binding.LockKey())
	defer lock()
	integration, err := binding.Resolve(ctx)
	if err != nil || integration.Ref().Key() != binding.Page.Key() {
		return
	}
	taskID, err := binding.Store.TaskID(ctx)
	if err != nil || taskID == "" {
		return
	}
	active, err := h.pageChatActiveExecution(ctx, taskID)
	if err != nil || active != nil {
		return
	}
	queued, err := h.threadInputRepo.FindOldestQueuedForTask(ctx, taskID)
	if err != nil || queued == nil {
		return
	}
	raw, err := binding.Store.QueuedContext(ctx, queued.ID)
	if err != nil {
		return
	}
	var turn pageQueuedContext
	if json.Unmarshal([]byte(raw), &turn) != nil || turn.Principal == "" {
		return
	}
	if err = h.authorizePageTurn(ctx, turn.Principal, binding.Page.ProjectID); err != nil {
		if errors.Is(err, errPageTurnUnauthorized) {
			if _, err = h.threadInputRepo.CancelPending(ctx, queued.ID); err == nil {
				h.goStartNextQueuedTurnAfter(streamingResponseParams{ProjectID: binding.Page.ProjectID, TaskID: taskID, PageConversation: &binding}, "")
			}
		}
		return
	}
	ctx, err = h.pagePrincipalContext(ctx, turn.Principal, binding.Page.ProjectID, authz.Edit)
	if err != nil {
		return
	}
	agent, _, err := h.resolveQueuedInputAgent(ctx, *queued)
	if err != nil || agent == nil {
		return
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil || task.Category != models.CategoryPageChat {
		return
	}
	exec := &models.Execution{TaskID: taskID, AgentConfigID: agent.ID, PromptSent: queued.Content, IsFollowup: true}
	if err = h.threadInputRepo.ClaimQueuedForPageExecution(ctx, queued.ID, exec); err != nil {
		return
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.TaskThreadInputApplied, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: exec.ID, PendingInputID: queued.ID, Message: exec.PromptSent})
	}
	if _, err = h.launchPageChat(ctx, binding, task, exec, agent, exec.PromptSent, queued.AttachmentSessionID, turn); err != nil {
		applog.Infof("[page-chat] queued turn: %v", err)
	}
}
