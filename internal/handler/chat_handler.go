package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const chatSSETimeout = chatProcessingTimeout + 30*time.Second

const (
	chatUIWindowLimitDefault = 5
	chatUIWindowLimitMax     = 100
)

func (h *Handler) Chat(c echo.Context) error {
	isHTMX := isHTMX(c)
	applog.Debugf("[handler] Chat requested htmx=%v", isHTMX)

	agents, err := h.llmConfigRepo.ListPickerOptions(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] Chat error listing model picker options: %v", err)
		return err
	}

	currentProjectID, _ := h.getCurrentProjectID(c)

	limit := parseThreadWindowLimit(c.QueryParam("limit"), chatUIWindowLimitDefault, chatUIWindowLimitMax)
	beforeExecID := strings.TrimSpace(c.QueryParam("before"))
	chatHistory, hasEarlier, err := h.loadChatExecutionWindow(c.Request().Context(), currentProjectID, beforeExecID, limit)
	if err != nil {
		applog.Infof("[handler] Chat error loading chat history: %v", err)
		// Continue even if history load fails - just show empty chat
		chatHistory = []models.Execution{}
		hasEarlier = false
	}

	chatAttachmentsByExec := h.loadChatAttachmentsForExecutions(c.Request().Context(), chatHistory, "Chat")

	pendingInputs := []models.ThreadInput{}
	if h.threadInputRepo != nil && currentProjectID != "" {
		if inputs, inputErr := h.threadInputRepo.ListPendingForChat(c.Request().Context(), currentProjectID); inputErr == nil {
			pendingInputs = inputs
		} else {
			applog.Infof("[handler] Chat error loading pending inputs: %v", inputErr)
		}
	}

	latestPlanComplete := chatHistoryHasPlanCompletion(chatHistory)

	if beforeExecID != "" {
		return render(c, http.StatusOK, pages.ChatEarlierMessages(chatHistory, chatAttachmentsByExec, currentProjectID, hasEarlier, limit))
	}

	// For HTMX requests, return just the chat content
	if isHTMX {
		return render(c, http.StatusOK, pages.ChatContent(agents, chatHistory, currentProjectID, chatAttachmentsByExec, pendingInputs, latestPlanComplete, hasEarlier, limit))
	}

	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	return render(c, http.StatusOK, pages.Chat(projects, currentProjectID, agents, chatHistory, chatAttachmentsByExec, pendingInputs, latestPlanComplete, hasEarlier, limit))
}

func parseThreadWindowLimit(raw string, defaultLimit, maxLimit int) int {
	limit := defaultLimit
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func trimExecutionWindow(rows []models.Execution, limit int) ([]models.Execution, bool) {
	if limit <= 0 || len(rows) <= limit {
		return rows, false
	}
	return rows[len(rows)-limit:], true
}

func executionIDs(executions []models.Execution) []string {
	execIDs := make([]string, len(executions))
	for i, exec := range executions {
		execIDs[i] = exec.ID
	}
	return execIDs
}

func (h *Handler) loadChatExecutionWindow(ctx context.Context, projectID, beforeExecID string, limit int) ([]models.Execution, bool, error) {
	queryLimit := limit + 1
	var rows []models.Execution
	var err error
	if beforeExecID != "" {
		rows, err = h.execRepo.ListChatHistoryBefore(ctx, projectID, beforeExecID, queryLimit)
	} else {
		rows, err = h.execRepo.ListChatHistory(ctx, projectID, queryLimit)
	}
	if err != nil {
		return nil, false, err
	}
	visible, hasEarlier := trimExecutionWindow(rows, limit)
	return visible, hasEarlier, nil
}

func (h *Handler) loadChatAttachmentsForExecutions(ctx context.Context, executions []models.Execution, label string) map[string][]models.ChatAttachment {
	execIDs := executionIDs(executions)
	chatAttachmentsByExec, err := h.chatAttachmentRepo.ListByExecutionIDs(ctx, execIDs)
	if err != nil {
		applog.Infof("[handler] %s error loading attachments: %v", label, err)
		return make(map[string][]models.ChatAttachment)
	}
	return chatAttachmentsByExec
}

func (h *Handler) ChatSend(c echo.Context) error {
	message := c.FormValue("message")
	agentID := c.FormValue("agent_id")
	chatMode := models.NormalizeChatMode(c.FormValue("chat_mode"))

	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}

	applog.Infof("[handler] ChatSend message=%q agent_id=%s chat_mode=%s", message, agentID, chatMode)

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		applog.Infof("[handler] ChatSend model availability check error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
	}
	if !hasModels {
		applog.Infof("[handler] ChatSend blocked: no models configured")
		return noModelsConfiguredResponse(c)
	}

	// Check for pending image attachments (for vision-aware auto-selection)
	sessionID := c.FormValue("attachment_session_id")
	hasImages := hasPendingImages(sessionID)

	// Get project from query param or use default before resolving "default" model selection.
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		applog.Infof("[handler] ChatSend error getting project: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "no project available")
	}

	// Select agent (auto, explicit, or project-aware default)
	agent, err := h.selectAgentForProject(c.Request().Context(), agentID, message, hasImages, projectID)
	if err != nil {
		applog.Infof("[handler] ChatSend agent selection error: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Note: Interactive chat intentionally bypasses task worker capacity checks.
	// Task worker limits (per-project/per-model) only gate task execution, not chat.
	// This ensures the chat orchestrator remains responsive even when all task workers are busy.

	activeChatExec, err := h.execRepo.FindLatestActiveChatExecution(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ChatSend error checking active chat turn: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check chat queue")
	}

	if activeChatExec != nil {
		if h.threadInputRepo == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "chat input queue is unavailable")
		}
		queued := &models.ThreadInput{
			Scope:               models.ThreadInputScopeChat,
			ProjectID:           projectID,
			RunExecutionID:      activeChatExec.ID,
			AgentConfigID:       agent.ID,
			InputMode:           models.ThreadInputModeQueued,
			InputStatus:         models.ThreadInputPending,
			Content:             message,
			AttachmentSessionID: sessionID,
			ChatMode:            chatMode,
		}
		if err := h.threadInputRepo.CreateQueued(c.Request().Context(), queued); err != nil {
			applog.Infof("[handler] ChatSend error creating queued input: %v", err)
			if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
				applog.Infof("[handler] ChatSend error cleaning unpublished attachment session %s: %v", sessionID, cleanupErr)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue chat message")
		}
		if h.chatBroadcaster != nil {
			h.chatBroadcaster.Publish(events.ChatEvent{
				Type:           events.ChatNewMessage,
				ProjectID:      projectID,
				ExecID:         queued.ID,
				Message:        message,
				Source:         "web",
				AgentName:      agent.Name,
				Queued:         true,
				HasAttachments: queued.AttachmentSessionID != "",
			})
		}
		return render(c, http.StatusOK, components.ChatQueuedInputRowOOB(queued.ID, message, "/chat/queued/"+queued.ID+"/steer", queued.AttachmentSessionID != ""))
	}
	// Create a task record for the chat message (required for execution tracking)
	selectedAgentID := agent.ID
	chatTitle := fmt.Sprintf("Chat %s: %s", time.Now().Format("15:04:05.000"), message[:min(50, len(message))])
	task := &models.Task{
		ProjectID: projectID,
		Title:     chatTitle,
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &selectedAgentID,
	}
	if err := h.taskRepo.Create(c.Request().Context(), task); err != nil {
		applog.Infof("[handler] ChatSend error creating task: %v", err)
		if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
			applog.Infof("[handler] ChatSend error cleaning unpublished attachment session %s after task create failure: %v", sessionID, cleanupErr)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create chat task")
	}
	claimed, err := h.taskRepo.ClaimTask(c.Request().Context(), task.ID)
	if err != nil || !claimed {
		if err == nil {
			err = fmt.Errorf("chat task was not claimable")
		}
		applog.Infof("[handler] ChatSend error claiming chat task=%s: %v", task.ID, err)
		if delErr := h.taskRepo.Delete(c.Request().Context(), task.ID); delErr != nil {
			applog.Infof("[handler] ChatSend error cleaning up unclaimed chat task=%s: %v", task.ID, delErr)
		}
		if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
			applog.Infof("[handler] ChatSend error cleaning unpublished attachment session %s after claim failure: %v", sessionID, cleanupErr)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start chat task")
	}

	// Create execution record for immediate streaming delivery.
	execStatus := models.ExecRunning
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        execStatus,
		PromptSent:    message,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		applog.Infof("[handler] ChatSend error creating execution: %v", err)
		if delErr := h.taskRepo.Delete(c.Request().Context(), task.ID); delErr != nil {
			applog.Infof("[handler] ChatSend error cleaning up chat task=%s after execution create failure: %v", task.ID, delErr)
		}
		if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
			applog.Infof("[handler] ChatSend error cleaning unpublished attachment session %s after execution create failure: %v", sessionID, cleanupErr)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create execution")
	}

	applog.Infof("[handler] ChatSend created exec=%s for chat message status=%s", exec.ID, exec.Status)
	// Broadcast new message event so other tabs/clients update in real-time
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Message:   message,
			Source:    "web",
			AgentName: agent.Name,
		})
	}

	// Handle file attachments if present
	var attachmentContext string
	var imageAttachments []models.Attachment
	var chatAttachments []models.ChatAttachment
	if sessionID != "" {
		applog.Infof("[handler] ChatSend processing attachments for session=%s", sessionID)
		var attErr error
		attachmentContext, imageAttachments, chatAttachments, attErr = h.processAttachmentsWithReturn(c.Request().Context(), sessionID, exec.ID)
		if attErr != nil {
			applog.Infof("[handler] ChatSend error processing attachments: %v", attErr)
			message = message + fmt.Sprintf("\n\n⚠️ Attachment processing error: %v", attErr)
		}
	}

	// Load recent chat history and filter for conversation context
	chatHistory, err := h.execRepo.ListChatHistory(c.Request().Context(), projectID, chatHistoryLimit)
	if err != nil {
		applog.Infof("[handler] ChatSend error loading chat history: %v", err)
		chatHistory = []models.Execution{}
	}
	priorHistory := filterChatHistory(chatHistory, exec.ID)

	// Render user message and streaming/queued placeholder
	var userMsg templ.Component
	if len(chatAttachments) > 0 {
		userMsg = components.ChatBubbleWithAttachments("User", message, chatAttachments, projectID)
	} else {
		userMsg = components.ChatBubble("User", message)
	}
	agentMsg := components.ChatBubbleStreaming("Assistant", exec.ID, "chat-messages", "", false)
	// Build context and spawn LLM processing goroutine
	availableModels, _ := h.llmConfigRepo.List(c.Request().Context())
	taskContext := h.buildChatContext(c.Request().Context(), projectID, availableModels)
	personalityContext := h.getPersonalityContext(c.Request().Context(), projectID)
	workDir := h.resolveWorkDir(c.Request().Context(), projectID)

	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          message,
		Agent:            *agent,
		ChatHistory:      priorHistory,
		ProjectID:        projectID,
		PrincipalID:      h.authPrincipalID(c),
		SystemContext:    combineContexts(combineContexts(taskContext, attachmentContext), personalityContext),
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   false,
		ChatMode:         chatMode,
		Surface:          chatcontrol.SurfaceWeb,
	}); err != nil {
		c.Response().Header().Set("Retry-After", "30")
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return render(c, http.StatusOK, templ.Join(
		userMsg,
		agentMsg,
		components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id="+projectID, true, exec.ID),
	))
}

func (h *Handler) ChatStop(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		applog.Infof("[handler] ChatStop error getting project: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "no project available")
	}
	activeChatExec, err := h.execRepo.FindLatestActiveChatExecution(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ChatStop error checking active chat turn: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	if activeChatExec == nil {
		if isHTMX(c) {
			return render(c, http.StatusOK, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id="+projectID, false, ""))
		}
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.taskSvc.CancelTask(c.Request().Context(), activeChatExec.TaskID); err != nil {
		applog.Infof("[handler] ChatStop error cancelling chat task=%s: %v", activeChatExec.TaskID, err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	// TaskService.CancelTask moves normal tasks to Backlog for kanban visibility.
	// Chat backing tasks are hidden from kanban and chat history is keyed by
	// category=chat, so preserve the chat category after reusing the shared stop
	// semantics for cancel callbacks and goal pausing.
	if err := h.taskRepo.UpdateCategory(c.Request().Context(), activeChatExec.TaskID, models.CategoryChat); err != nil {
		applog.Infof("[handler] ChatStop error preserving chat category task=%s: %v", activeChatExec.TaskID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to preserve chat history")
	}
	h.cancelActiveExecutionsAndPublish(c.Request().Context(), activeChatExec.TaskID, "ChatStop")
	if isHTMX(c) {
		return render(c, http.StatusOK, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id="+projectID, false, ""))
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) ChatComposerAction(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		applog.Infof("[handler] ChatComposerAction error getting project: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "no project available")
	}
	activeChatExec, err := h.execRepo.FindLatestActiveChatExecution(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ChatComposerAction error checking active chat turn: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	activeTurnID := ""
	if activeChatExec != nil {
		activeTurnID = activeChatExec.ID
	}
	return render(c, http.StatusOK, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id="+projectID, activeChatExec != nil, activeTurnID))
}

func (h *Handler) ChatSteer(c echo.Context) error {
	message := strings.TrimSpace(c.FormValue("message"))
	chatMode := models.NormalizeChatMode(c.FormValue("chat_mode"))
	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "chat input queue is unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		applog.Infof("[handler] ChatSteer error getting project: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "no project available")
	}
	active, err := h.execRepo.FindLatestActiveChatExecution(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ChatSteer active execution check failed project=%s: %v", projectID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	if active == nil {
		return echo.NewHTTPError(http.StatusConflict, "no active response to steer; send a normal message instead")
	}
	expectedTurnID := c.FormValue("expected_turn_id")
	if expectedTurnID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "expected turn id is required")
	}
	if expectedTurnID != active.ID {
		return echo.NewHTTPError(http.StatusConflict, "active turn changed; queue the message instead")
	}
	input := &models.ThreadInput{
		Scope:               models.ThreadInputScopeChat,
		ProjectID:           projectID,
		RunExecutionID:      active.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              active.ID,
		ExpectedTurnID:      expectedTurnID,
		Content:             message,
		AttachmentSessionID: c.FormValue("attachment_session_id"),
		ChatMode:            chatMode,
	}
	if err := h.threadInputRepo.CreateSteeringForActiveExecution(c.Request().Context(), input, active.ID); err != nil {
		applog.Infof("[handler] ChatSteer error creating steering input: %v", err)
		if errors.Is(err, repository.ErrExpectedTurnEmpty) {
			return echo.NewHTTPError(http.StatusBadRequest, "expected turn id is required")
		}
		if errors.Is(err, repository.ErrNoActiveTurn) || errors.Is(err, repository.ErrActiveTurnChanged) {
			return echo.NewHTTPError(http.StatusConflict, "active turn changed; queue the message instead")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save steering input")
	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:           events.ChatTurnSteered,
			ProjectID:      projectID,
			ExecID:         input.ID,
			Message:        message,
			Source:         "web",
			Steering:       true,
			HasAttachments: input.AttachmentSessionID != "",
		})
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRow(input.ID, message, input.AttachmentSessionID != ""))
}

// ChatPendingInputs returns the current pending-inputs composer fragment for the project.
// Called by the chat page on SSE reconnect to reconcile any steering/queued rows missed
// while the tab was hidden. The server-side query excludes prepared/in-flight steering rows
// (expected_turn_id=NULL) so a stale "Steering pending" row is cleanly replaced.
func (h *Handler) ChatPendingInputs(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}
	pendingInputs := []models.ThreadInput{}
	if h.threadInputRepo != nil {
		if inputs, inputErr := h.threadInputRepo.ListPendingForChat(c.Request().Context(), projectID); inputErr == nil {
			pendingInputs = inputs
		}
	}
	return render(c, http.StatusOK, components.ChatComposerQueuedInputRows(pendingInputs, func(input models.ThreadInput) string {
		return "/chat/queued/" + input.ID + "/steer"
	}))
}

// isImageFile checks if a filename has a common image extension supported by Anthropic's API
func isImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// mediaTypeFromExtension returns the MIME type for common file extensions.
// Uses the allowedFileExtensions map from the API for consistent type detection.
func mediaTypeFromExtension(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if mt, ok := allowedFileExtensions[ext]; ok {
		return mt
	}
	return "text/plain"
}

type chatAttachmentModelContextFile struct {
	FileName  string
	FilePath  string
	MediaType string
	FileSize  int64
}

type chatAttachmentModelContextOptions struct {
	IgnoreReadErrors bool
}

func buildChatAttachmentModelContext(files []chatAttachmentModelContextFile, opts chatAttachmentModelContextOptions) (string, []models.Attachment, error) {
	var attachmentContents []string
	var imageAttachments []models.Attachment

	for _, file := range files {
		mediaType := file.MediaType
		if mediaType == "" {
			mediaType = mediaTypeFromExtension(file.FileName)
		}

		if isImageFile(file.FileName) {
			imageAttachments = append(imageAttachments, models.Attachment{
				FileName:  file.FileName,
				FilePath:  file.FilePath,
				MediaType: mediaType,
				FileSize:  file.FileSize,
			})
			continue
		}

		if file.FileSize <= maxTextAttachmentSize {
			content, err := os.ReadFile(file.FilePath)
			if err != nil {
				if opts.IgnoreReadErrors {
					continue
				}
				return "", nil, fmt.Errorf("reading file %s: %w", file.FilePath, err)
			}
			attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s\n```\n%s\n```\n", file.FileName, string(content)))
		} else {
			attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s (attached, %d bytes - too large to include inline)\n", file.FileName, file.FileSize))
		}
	}

	textContext := ""
	if len(attachmentContents) > 0 {
		textContext = "\n\n--- Attached Files ---\n" + strings.Join(attachmentContents, "")
	}
	return textContext, imageAttachments, nil
}

// processAttachments moves uploaded files from pending directory to execution directory,
// creates database records, and returns text context and image attachments separately.
// Image files are returned as models.Attachment for multimodal API handling instead of
// being injected as raw bytes into the text prompt (which would cause "prompt too long" errors).
func (h *Handler) processAttachments(ctx context.Context, sessionID, execID string) (string, []models.Attachment, error) {
	textContext, imageAttachments, _, err := h.processAttachmentsWithReturn(ctx, sessionID, execID)
	return textContext, imageAttachments, err
}

// processAttachmentsWithReturn is like processAttachments but also returns the created ChatAttachment records
func (h *Handler) processAttachmentsWithReturn(ctx context.Context, sessionID, execID string) (string, []models.Attachment, []models.ChatAttachment, error) {
	if h.chatAttachmentRepo == nil {
		return "", nil, nil, fmt.Errorf("chat attachment repository is unavailable")
	}
	pendingDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)

	// A persisted session ID is an attachment ownership claim. If its source directory
	// disappeared, continuing would silently drop the user's files.
	if _, err := os.Stat(pendingDir); err != nil {
		if os.IsNotExist(err) {
			applog.Infof("[handler] attachment lifecycle stage=session-source session=%s execution=%s source=%s error=file-not-found", sessionID, execID, pendingDir)
			return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-source session=%s execution=%s source=%s: %w", sessionID, execID, pendingDir, os.ErrNotExist)
		}
		return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-source session=%s execution=%s source=%s: %w", sessionID, execID, pendingDir, err)
	}

	// Read files from pending directory
	files, err := os.ReadDir(pendingDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("reading pending directory: %w", err)
	}

	if len(files) == 0 {
		return "", nil, nil, nil
	}

	// Create execution-specific directory
	execDir := filepath.Join(uploadsDir, "chat", execID)
	if err := os.MkdirAll(execDir, 0755); err != nil {
		return "", nil, nil, fmt.Errorf("creating execution directory: %w", err)
	}

	var contextFiles []chatAttachmentModelContextFile
	var chatAttachments []models.ChatAttachment
	var staged []attachmentPublication
	rollback := func() {
		for _, rollbackErr := range rollbackAttachmentPublications(ctx, staged, h.chatAttachmentRepo.Delete) {
			applog.Infof("[handler] attachment lifecycle stage=rollback-chat session=%s execution=%s error=%v", sessionID, execID, rollbackErr)
		}
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		srcPath := filepath.Join(pendingDir, file.Name())
		destPath := filepath.Join(execDir, file.Name())
		applog.Infof("[handler] attachment lifecycle stage=session-copy session=%s source=%s execution=%s destination=%s", sessionID, srcPath, execID, destPath)
		if err := copyFileAtomically(srcPath, destPath); err != nil {
			rollback()
			return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-copy session=%s source=%s execution=%s destination=%s: %w", sessionID, srcPath, execID, destPath, err)
		}

		info, err := os.Stat(destPath)
		if err != nil {
			_ = os.Remove(destPath)
			rollback()
			return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-verify session=%s source=%s execution=%s destination=%s: %w", sessionID, srcPath, execID, destPath, err)
		}
		mediaType := mediaTypeFromExtension(file.Name())
		attachment := &models.ChatAttachment{
			ExecutionID: execID, FileName: file.Name(), FilePath: destPath,
			MediaType: mediaType, FileSize: info.Size(),
		}
		if err := h.chatAttachmentRepo.Create(ctx, attachment); err != nil {
			_ = os.Remove(destPath)
			rollback()
			return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-metadata session=%s source=%s execution=%s destination=%s: %w", sessionID, srcPath, execID, destPath, err)
		}
		staged = append(staged, attachmentPublication{id: attachment.ID, path: destPath})
		chatAttachments = append(chatAttachments, *attachment)

		contextFiles = append(contextFiles, chatAttachmentModelContextFile{
			FileName:  file.Name(),
			FilePath:  destPath,
			MediaType: mediaType,
			FileSize:  info.Size(),
		})
	}

	textContext, imageAttachments, err := buildChatAttachmentModelContext(contextFiles, chatAttachmentModelContextOptions{})
	if err != nil {
		rollback()
		return "", nil, nil, fmt.Errorf("attachment lifecycle stage=session-read session=%s execution=%s: %w", sessionID, execID, err)
	}

	if err := os.RemoveAll(pendingDir); err != nil {
		applog.Infof("[handler] attachment lifecycle stage=session-cleanup session=%s execution=%s source=%s error=%v", sessionID, execID, pendingDir, err)
	}
	applog.Infof("[handler] attachment lifecycle stage=session-committed session=%s execution=%s attachments=%d", sessionID, execID, len(chatAttachments))

	return textContext, imageAttachments, chatAttachments, nil
}

func (h *Handler) previewPendingAttachments(sessionID string) (string, []models.Attachment, error) {
	pendingDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if _, err := os.Stat(pendingDir); os.IsNotExist(err) {
		return "", nil, nil
	}
	files, err := os.ReadDir(pendingDir)
	if err != nil {
		return "", nil, fmt.Errorf("reading pending directory: %w", err)
	}
	var contextFiles []chatAttachmentModelContextFile
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		path := filepath.Join(pendingDir, file.Name())
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, fmt.Errorf("getting file info %s: %w", file.Name(), err)
		}
		contextFiles = append(contextFiles, chatAttachmentModelContextFile{
			FileName:  file.Name(),
			FilePath:  path,
			MediaType: mediaTypeFromExtension(file.Name()),
			FileSize:  info.Size(),
		})
	}
	return buildChatAttachmentModelContext(contextFiles, chatAttachmentModelContextOptions{})
}

func (h *Handler) ClearChat(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	applog.Infof("[handler] ClearChat project=%s", projectID)

	// Cancel any running chat goroutines before deleting.
	// Without this, running goroutines continue processing with old conversation
	// history in memory, and their responses may appear stale or confusing.
	if h.workerSvc != nil {
		runningIDs, _ := h.taskRepo.ListRunningChatTaskIDs(c.Request().Context(), projectID)
		for _, id := range runningIDs {
			applog.Infof("[handler] ClearChat cancelling running chat task=%s", id)
			h.workerSvc.CancelRunningTask(id)
		}
	}
	if h.threadInputRepo != nil {
		if err := h.threadInputRepo.CancelPendingForChat(c.Request().Context(), projectID); err != nil {
			applog.Infof("[handler] ClearChat error cancelling pending chat inputs: %v", err)
			return err
		}
	}

	count, err := h.taskSvc.DeleteAllChat(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ClearChat error: %v", err)
		return err
	}
	applog.Infof("[handler] ClearChat deleted %d chat tasks", count)

	// Return updated chat content
	agents, err := h.llmConfigRepo.ListPickerOptions(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] ClearChat error listing model picker options: %v", err)
		return err
	}

	// Return empty chat content
	return render(c, http.StatusOK, pages.ChatContent(agents, []models.Execution{}, projectID, make(map[string][]models.ChatAttachment), []models.ThreadInput{}, false, false, chatUIWindowLimitDefault))
}

// chatHistoryHasPlanCompletion checks if the latest completed assistant response
// in the chat history contains a <proposed_plan> block, indicating a plan-mode
// completion that should show the "Switch to Orchestrate" CTA.
func chatHistoryHasPlanCompletion(history []models.Execution) bool {
	for i := len(history) - 1; i >= 0; i-- {
		exec := history[i]
		if exec.Status == models.ExecCompleted && exec.Output != "" {
			return strings.Contains(exec.Output, "<proposed_plan>")
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type attachmentPublication struct {
	id   string
	path string
	name string
}

// rollbackAttachmentPublications preserves the file/metadata invariant during rollback.
// A file is removed only after its metadata row is successfully deleted; if metadata
// deletion fails, retaining the file is safer than leaving a persisted broken reference.
func rollbackAttachmentPublications(
	ctx context.Context,
	published []attachmentPublication,
	deleteMetadata func(context.Context, string) error,
) []error {
	var rollbackErrs []error
	rollbackCtx := context.WithoutCancel(ctx)
	for i := len(published) - 1; i >= 0; i-- {
		item := published[i]
		if item.id != "" {
			if err := deleteMetadata(rollbackCtx, item.id); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("delete metadata attachment=%s destination=%s: %w", item.id, item.path, err))
				continue
			}
		}
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("remove file destination=%s: %w", item.path, err))
		}
	}
	return rollbackErrs
}

// copyChatAttachmentsToTask durably copies all attachments from a chat execution to a task.
// The batch is published file-first and metadata-second. Any failure rolls back publications
// when possible and retains files for metadata rows that cannot be deleted, so task execution
// never observes a metadata reference to a file removed by rollback.
func (h *Handler) copyChatAttachmentsToTask(ctx context.Context, executionID, taskID string) (int, error) {
	chatAttachments, err := h.chatAttachmentRepo.ListByExecution(ctx, executionID)
	if err != nil {
		return 0, fmt.Errorf("attachment lifecycle stage=list-source execution=%s task=%s: %w", executionID, taskID, err)
	}
	if len(chatAttachments) == 0 {
		return 0, nil
	}

	taskDir := filepath.Join(uploadsDir, "tasks", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return 0, fmt.Errorf("attachment lifecycle stage=create-destination execution=%s task=%s destination=%s: %w", executionID, taskID, taskDir, err)
	}

	published := make([]attachmentPublication, 0, len(chatAttachments))
	usedNames := make(map[string]bool, len(chatAttachments))
	rollback := func() {
		for _, rollbackErr := range rollbackAttachmentPublications(ctx, published, h.attachmentRepo.Delete) {
			applog.Infof("[handler] attachment lifecycle stage=rollback execution=%s task=%s error=%v", executionID, taskID, rollbackErr)
		}
	}

	for _, chatAtt := range chatAttachments {
		srcPath := chatAtt.FilePath
		fileName := uniqueAttachmentName(taskDir, filepath.Base(chatAtt.FileName), usedNames)
		destPath := filepath.Join(taskDir, fileName)
		applog.Infof("[handler] attachment lifecycle stage=copy-start execution=%s source=%s task=%s destination=%s", executionID, srcPath, taskID, destPath)

		if err := copyFileAtomically(srcPath, destPath); err != nil {
			rollback()
			return 0, fmt.Errorf("attachment lifecycle stage=copy-file execution=%s source=%s task=%s destination=%s: %w", executionID, srcPath, taskID, destPath, err)
		}

		info, err := os.Stat(destPath)
		if err != nil {
			_ = os.Remove(destPath)
			rollback()
			return 0, fmt.Errorf("attachment lifecycle stage=verify-destination execution=%s source=%s task=%s destination=%s: %w", executionID, srcPath, taskID, destPath, err)
		}
		attachment := &models.Attachment{
			TaskID: taskID, FileName: fileName, FilePath: destPath,
			MediaType: chatAtt.MediaType, FileSize: info.Size(),
		}
		if err := h.attachmentRepo.Create(ctx, attachment); err != nil {
			_ = os.Remove(destPath)
			rollback()
			return 0, fmt.Errorf("attachment lifecycle stage=persist-metadata execution=%s source=%s task=%s destination=%s: %w", executionID, srcPath, taskID, destPath, err)
		}
		published = append(published, attachmentPublication{id: attachment.ID, path: destPath, name: fileName})
		applog.Infof("[handler] attachment lifecycle stage=published execution=%s source=%s task=%s destination=%s attachment=%s", executionID, srcPath, taskID, destPath, attachment.ID)
	}

	task, getErr := h.taskRepo.GetByID(ctx, taskID)
	if getErr == nil && task != nil {
		fileRefs := make([]string, 0, len(published))
		for _, item := range published {
			fileRefs = append(fileRefs, fmt.Sprintf("%s (path: %s)", item.name, item.path))
		}
		task.Prompt += fmt.Sprintf("\n\n[Attached files from chat:\n%s]", strings.Join(fileRefs, "\n"))
		if updateErr := h.taskRepo.Update(ctx, task); updateErr != nil {
			applog.Infof("[handler] attachment lifecycle stage=update-prompt execution=%s task=%s error=%v", executionID, taskID, updateErr)
		}
	}
	return len(published), nil
}

func uniqueAttachmentName(dir, requested string, used map[string]bool) string {
	if requested == "." || requested == "" {
		requested = "attachment"
	}
	ext := filepath.Ext(requested)
	stem := strings.TrimSuffix(requested, ext)
	for i := 0; ; i++ {
		name := requested
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		if used[name] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil || !os.IsNotExist(err) {
			continue
		}
		used[name] = true
		return name
	}
}

var (
	renameAttachmentFile    = os.Rename
	syncAttachmentDirectory = syncDirectory
)

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func copyFileAtomically(srcPath, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".attachment-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := renameAttachmentFile(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := syncAttachmentDirectory(filepath.Dir(destPath)); err != nil {
		removeErr := os.Remove(destPath)
		cleanupSyncErr := syncAttachmentDirectory(filepath.Dir(destPath))
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("sync destination directory: %w (cleanup remove failed: %v)", err, removeErr)
		}
		if cleanupSyncErr != nil {
			return fmt.Errorf("sync destination directory: %w (cleanup sync failed: %v)", err, cleanupSyncErr)
		}
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}

// writeSSEData writes a potentially multi-line string as properly formatted SSE data.
// SSE spec requires each line to be prefixed with "data: ". The browser's EventSource
// automatically joins multiple "data:" lines with "\n" when firing onmessage.
func writeSSEData(c echo.Context, data string) {
	lines := strings.Split(data, "\n")
	for _, line := range lines {
		fmt.Fprintf(c.Response(), "data: %s\n", line)
	}
	fmt.Fprintf(c.Response(), "\n") // Empty line terminates the event
}

func writeChatSSEEvent(c echo.Context, eventName string, data string) {
	fmt.Fprintf(c.Response(), "event: %s\n", eventName)
	lines := strings.Split(data, "\n")
	for _, line := range lines {
		fmt.Fprintf(c.Response(), "data: %s\n", line)
	}
	fmt.Fprintf(c.Response(), "\n")
}

func parseStreamOffset(raw string) int {
	offset, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func clampOffsetToUTF8Boundary(offset int, s string) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(s) {
		return len(s)
	}
	for offset > 0 && !utf8.RuneStart(s[offset]) {
		offset--
	}
	return offset
}

func (h *Handler) writeExecutionTerminalSSE(c echo.Context, exec *models.Execution) bool {
	if exec == nil {
		return false
	}
	switch exec.Status {
	case models.ExecCompleted:
		writeChatSSEEvent(c, "done", "completed")
		c.Response().Flush()
		return true
	case models.ExecCancelled:
		writeChatSSEEvent(c, "done", "cancelled")
		c.Response().Flush()
		return true
	case models.ExecFailed:
		applog.Infof("[handler] ChatStreamSSE exec=%s failed: %s", exec.ID, exec.ErrorMessage)
		writeChatSSEEvent(c, "error", exec.ErrorMessage)
		c.Response().Flush()
		return true
	default:
		return false
	}
}

func (h *Handler) replayExecutionOutputSSE(ctx context.Context, c echo.Context, execID string, sentLen *int) (*models.Execution, error) {
	exec, err := h.execRepo.GetByID(ctx, execID)
	if err != nil {
		return nil, err
	}
	if exec == nil {
		return nil, fmt.Errorf("execution not found")
	}
	start := clampOffsetToUTF8Boundary(*sentLen, exec.Output)
	if len(exec.Output) > start {
		writeSSEData(c, exec.Output[start:])
		c.Response().Flush()
		*sentLen = len(exec.Output)
	}
	return exec, nil
}

func (h *Handler) catchUpExecutionSSE(ctx context.Context, c echo.Context, execID string, sentLen *int, appendTerminal bool) (bool, error) {
	exec, err := h.replayExecutionOutputSSE(ctx, c, execID, sentLen)
	if err != nil {
		return false, err
	}
	if appendTerminal || exec.Status != models.ExecRunning {
		if h.writeExecutionTerminalSSE(c, exec) {
			return true, nil
		}
	}
	return false, nil
}

// ChatStreamSSE streams chat execution output via SSE
func (h *Handler) ChatStreamSSE(c echo.Context) error {
	execID := c.Param("exec_id")
	if execID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "exec_id is required")
	}

	applog.Infof("[handler] ChatStreamSSE exec=%s connected", execID)

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no")

	ctx := c.Request().Context()
	exec, err := h.execRepo.GetByID(ctx, execID)
	if err != nil {
		applog.Infof("[handler] ChatStreamSSE exec=%s error: %v", execID, err)
		writeChatSSEEvent(c, "error", err.Error())
		c.Response().Flush()
		return nil
	}
	if exec == nil {
		applog.Infof("[handler] ChatStreamSSE exec=%s not found", execID)
		writeChatSSEEvent(c, "error", "execution not found")
		c.Response().Flush()
		return nil
	}

	sentLen := parseStreamOffset(c.QueryParam("offset"))
	if h.executionStreamHub == nil {
		return h.chatStreamSSEDBFallback(ctx, c, execID, sentLen)
	}

	sub, unsubscribe, err := h.executionStreamHub.Subscribe(execID)
	if err != nil {
		writeChatSSEEvent(c, "error", err.Error())
		c.Response().Flush()
		return nil
	}
	defer unsubscribe()

	if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
		applog.Infof("[handler] ChatStreamSSE exec=%s catch-up error: %v", execID, err)
		writeChatSSEEvent(c, "error", err.Error())
		c.Response().Flush()
		return nil
	} else if done {
		return nil
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	catchup := time.NewTicker(2 * time.Second)
	defer catchup.Stop()
	timeout := time.NewTimer(chatSSETimeout)
	defer timeout.Stop()
	gapPending := false

	resetTimeout := func() {
		if !timeout.Stop() {
			select {
			case <-timeout.C:
			default:
			}
		}
		timeout.Reset(chatSSETimeout)
	}

	for {
		select {
		case <-ctx.Done():
			applog.Infof("[handler] ChatStreamSSE exec=%s client disconnected", execID)
			return nil
		case <-timeout.C:
			applog.Infof("[handler] ChatStreamSSE exec=%s timeout", execID)
			writeChatSSEEvent(c, "error", "timeout")
			c.Response().Flush()
			return nil
		case <-heartbeat.C:
			fmt.Fprintf(c.Response(), ": heartbeat\n\n")
			c.Response().Flush()
		case <-catchup.C:
			if gapPending {
				if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
					applog.Infof("[handler] ChatStreamSSE exec=%s catch-up error: %v", execID, err)
					writeChatSSEEvent(c, "error", err.Error())
					c.Response().Flush()
					return nil
				} else if done {
					return nil
				}
				gapPending = false
				resetTimeout()
				continue
			}
			if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
				applog.Infof("[handler] ChatStreamSSE exec=%s terminal fallback error: %v", execID, err)
				writeChatSSEEvent(c, "error", err.Error())
				c.Response().Flush()
				return nil
			} else if done {
				return nil
			}
		case ev, ok := <-sub:
			if !ok {
				if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
					applog.Infof("[handler] ChatStreamSSE exec=%s final reconcile error: %v", execID, err)
					writeChatSSEEvent(c, "error", err.Error())
					c.Response().Flush()
					return nil
				} else if done {
					return nil
				}
				return nil
			}
			if ev.ExecID != execID {
				continue
			}
			switch ev.Type {
			case events.ExecutionStreamDelta:
				eventStart := ev.Offset - len(ev.Delta)
				if ev.Offset <= sentLen {
					continue
				}
				if eventStart > sentLen {
					if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
						applog.Infof("[handler] ChatStreamSSE exec=%s gap catch-up error: %v", execID, err)
						writeChatSSEEvent(c, "error", err.Error())
						c.Response().Flush()
						return nil
					} else if done {
						return nil
					}
					if eventStart > sentLen {
						gapPending = true
						continue
					}
				}
				start := sentLen - eventStart
				if start < 0 {
					start = 0
				}
				start = clampOffsetToUTF8Boundary(start, ev.Delta)
				if start < len(ev.Delta) {
					writeSSEData(c, ev.Delta[start:])
					c.Response().Flush()
					sentLen = ev.Offset
					gapPending = false
					resetTimeout()
				}
			case events.ExecutionStreamDone:
				if done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, false); err != nil {
					applog.Infof("[handler] ChatStreamSSE exec=%s terminal catch-up error: %v", execID, err)
					writeChatSSEEvent(c, "error", err.Error())
					c.Response().Flush()
					return nil
				} else if done {
					return nil
				}
				status := ev.Status
				if status == "" {
					status = "completed"
				}
				writeChatSSEEvent(c, "done", status)
				c.Response().Flush()
				return nil
			case events.ExecutionStreamError:
				if _, err := h.replayExecutionOutputSSE(ctx, c, execID, &sentLen); err != nil {
					applog.Infof("[handler] ChatStreamSSE exec=%s error catch-up error: %v", execID, err)
					writeChatSSEEvent(c, "error", err.Error())
					c.Response().Flush()
					return nil
				}
				writeChatSSEEvent(c, "error", ev.Error)
				c.Response().Flush()
				return nil
			}
		}
	}
}

func (h *Handler) chatStreamSSEDBFallback(ctx context.Context, c echo.Context, execID string, sentLen int) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(chatSSETimeout)
	for {
		select {
		case <-ctx.Done():
			applog.Infof("[handler] ChatStreamSSE exec=%s client disconnected", execID)
			return nil
		case <-timeout:
			applog.Infof("[handler] ChatStreamSSE exec=%s timeout", execID)
			writeChatSSEEvent(c, "error", "timeout")
			c.Response().Flush()
			return nil
		case <-ticker.C:
			done, err := h.catchUpExecutionSSE(ctx, c, execID, &sentLen, true)
			if err != nil {
				applog.Infof("[handler] ChatStreamSSE exec=%s fallback error: %v", execID, err)
				writeChatSSEEvent(c, "error", err.Error())
				c.Response().Flush()
				return nil
			}
			if done {
				return nil
			}
		}
	}
}
