package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/persistence"
)

const pageConversationCategory = models.CategoryPageChat

var errPageChatBusy = errors.New("a response is already in progress for this page")

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
		Title:     "Page chat: " + binding.Page.Title,
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  pageConversationCategory,
		AgentID:   &agentID,
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
func (h *Handler) startPageChatTurn(c echo.Context, binding pageintegration.Conversation, message string) (*models.Execution, error) {
	ctx, done, err := binding.Begin(c.Request().Context())
	if err != nil {
		return nil, err
	}
	defer done()
	ref := binding.Page
	integration, err := binding.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if integration.Ref().Key() != ref.Key() || integration.Ref().ProjectID != ref.ProjectID {
		return nil, errors.New("page conversation identity changed")
	}
	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		return nil, err
	}
	if !hasModels {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "configure a model before chatting")
	}
	projectID := binding.Page.ProjectID
	agent, err := h.selectTaskAgent(ctx, projectID, "", message, false)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	lock := persistence.LockTaskLifecycle(binding.LockKey())
	defer lock()
	task, err := h.pageConversationTask(ctx, binding, message, agent.ID)
	if err != nil {
		return nil, err
	}
	active, err := h.execRepo.HasActiveTaskThreadExecution(ctx, task.ID, "")
	if err != nil {
		return nil, err
	}
	if active || task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return nil, errPageChatBusy
	}
	if task.Status != models.StatusPending {
		if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			return nil, err
		}
	}
	claimed, err := h.taskRepo.ClaimTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errPageChatBusy
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: message, IsFollowup: true}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		_ = h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed)
		return nil, err
	}
	principal := h.authPrincipalID(c)
	var snap *pageintegration.Snapshot
	if s, ok := binding.Contexts.Get(principal, ref); ok {
		snap = &s
		raw, _ := json.Marshal(s)
		if err := binding.Store.SaveSnapshot(ctx, exec.ID, string(raw)); err != nil {
			applog.Infof("[page-chat] save turn context exec=%s: %v", exec.ID, err)
		}
	}
	pageContext := pageintegration.RenderContextForPrompt(ref, snap, integration.Operations())
	tools := pageintegration.BuildRuntimeTools(integration, binding.Resolve, binding.Approvals, pageintegration.Caller{PrincipalID: principal})

	history, err := h.execRepo.ListByTaskChronologicalLimit(persistence.WithModelHistory(ctx, agent.Provider), task.ID, taskThreadHistoryLimit)
	if err != nil {
		history = nil
	}
	if err := h.startStreamingResponse(streamingResponseParams{
		CancellationContext: binding.Lifetime(),
		ExecID:              exec.ID,
		TaskID:              task.ID,
		Message:             message,
		Agent:               *agent,
		ChatHistory:         filterChatHistory(history, exec.ID),
		ProjectID:           projectID,
		PrincipalID:         principal,
		SystemContext:       combineContexts(h.getPersonalityContext(ctx, projectID), pageContext),
		WorkDir:             h.resolveWorkDir(ctx, projectID),
		IsTaskFollowup:      false,
		TaskScopedChat:      true,
		ChatMode:            models.ChatModeOrchestrate,
		Surface:             chatcontrol.SurfaceWeb,
		RuntimeTools:        tools,
	}); err != nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return exec, nil
}

func (h *Handler) cancelPageConversation(ctx context.Context, binding pageintegration.Conversation) error {
	lock := persistence.LockTaskLifecycle(binding.LockKey())
	defer lock()
	taskID, err := binding.Store.TaskID(ctx)
	if err != nil {
		return err
	}
	if taskID == "" {
		return nil
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

// loadPageChatHistory is also used by built-in page adapters; reopening never
// creates a fresh conversation or resumes another page's provider state.
func (h *Handler) loadPageChatHistory(ctx context.Context, binding pageintegration.Conversation) (*models.Task, []models.Execution, error) {
	taskID, err := binding.Store.TaskID(ctx)
	if err != nil || taskID == "" {
		return nil, nil, err
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil || task.ProjectID != binding.Page.ProjectID {
		return nil, nil, errors.New("invalid page conversation binding")
	}
	executions, err := h.execRepo.ListByTaskChronologicalLimit(ctx, taskID, 50)
	return task, executions, err
}
