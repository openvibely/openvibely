package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const chatSSETimeout = chatProcessingTimeout + 30*time.Second

func (h *Handler) Chat(c echo.Context) error {
	isHTMX := isHTMX(c)
	log.Printf("[handler] Chat requested htmx=%v", isHTMX)

	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] Chat error listing agents: %v", err)
		return err
	}

	currentProjectID, _ := h.getCurrentProjectID(c)

	// Load recent chat history for this project (last 50 messages)
	chatHistory, err := h.execRepo.ListChatHistory(c.Request().Context(), currentProjectID, 50)
	if err != nil {
		log.Printf("[handler] Chat error loading chat history: %v", err)
		// Continue even if history load fails - just show empty chat
		chatHistory = []models.Execution{}
	}

	// Load attachments for all executions in a single query
	execIDs := make([]string, len(chatHistory))
	for i, exec := range chatHistory {
		execIDs[i] = exec.ID
	}
	chatAttachmentsByExec, err := h.chatAttachmentRepo.ListByExecutionIDs(c.Request().Context(), execIDs)
	if err != nil {
		log.Printf("[handler] Chat error loading attachments: %v", err)
		chatAttachmentsByExec = make(map[string][]models.ChatAttachment)
	}

	// For HTMX requests, return just the chat content
	if isHTMX {
		return render(c, http.StatusOK, pages.ChatContent(agents, chatHistory, currentProjectID, chatAttachmentsByExec))
	}

	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.Chat(projects, currentProjectID, agents, chatHistory, chatAttachmentsByExec))
}

func (h *Handler) ChatSend(c echo.Context) error {
	message := c.FormValue("message")
	agentID := c.FormValue("agent_id")
	chatMode := models.NormalizeChatMode(c.FormValue("chat_mode"))

	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}

	log.Printf("[handler] ChatSend message=%q agent_id=%s chat_mode=%s", message, agentID, chatMode)

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		log.Printf("[handler] ChatSend model availability check error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
	}
	if !hasModels {
		log.Printf("[handler] ChatSend blocked: no models configured")
		return noModelsConfiguredResponse(c)
	}

	// Check for pending image attachments (for vision-aware auto-selection)
	sessionID := c.FormValue("attachment_session_id")
	hasImages := hasPendingImages(sessionID)

	// Select agent (auto or explicit)
	agent, err := h.selectAgent(c.Request().Context(), agentID, message, hasImages)
	if err != nil {
		log.Printf("[handler] ChatSend agent selection error: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Get project from query param or use default
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		log.Printf("[handler] ChatSend error getting project: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "no project available")
	}

	// Note: Interactive chat intentionally bypasses task worker capacity checks.
	// Task worker limits (per-project/per-model) only gate task execution, not chat.
	// This ensures the chat orchestrator remains responsive even when all task workers are busy.

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
		log.Printf("[handler] ChatSend error creating task: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create chat task")
	}

	// Create execution record for streaming
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    message,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		log.Printf("[handler] ChatSend error creating execution: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create execution")
	}

	log.Printf("[handler] ChatSend created exec=%s for chat message", exec.ID)

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
		log.Printf("[handler] ChatSend processing attachments for session=%s", sessionID)
		var attErr error
		attachmentContext, imageAttachments, chatAttachments, attErr = h.processAttachmentsWithReturn(c.Request().Context(), sessionID, exec.ID)
		if attErr != nil {
			log.Printf("[handler] ChatSend error processing attachments: %v", attErr)
			message = message + fmt.Sprintf("\n\n⚠️ Attachment processing error: %v", attErr)
		}
	}

	// Load recent chat history and filter for conversation context
	chatHistory, err := h.execRepo.ListChatHistory(c.Request().Context(), projectID, chatHistoryLimit)
	if err != nil {
		log.Printf("[handler] ChatSend error loading chat history: %v", err)
		chatHistory = []models.Execution{}
	}
	priorHistory := filterChatHistory(chatHistory, exec.ID)

	// Render user message and streaming placeholder
	var userMsg templ.Component
	if len(chatAttachments) > 0 {
		userMsg = components.ChatBubbleWithAttachments("User", message, chatAttachments)
	} else {
		userMsg = components.ChatBubble("User", message)
	}
	agentMsg := components.ChatBubbleStreaming("Assistant", exec.ID, "chat-messages", "", false)

	// Build context and spawn LLM processing goroutine
	availableModels, _ := h.llmConfigRepo.List(c.Request().Context())
	taskContext := h.buildChatContext(c.Request().Context(), projectID, availableModels)
	personalityContext := h.getPersonalityContext(c.Request().Context(), projectID)
	workDir := h.resolveWorkDir(c.Request().Context(), projectID)

	go h.processStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          message,
		Agent:            *agent,
		ChatHistory:      priorHistory,
		ProjectID:        projectID,
		SystemContext:    combineContexts(combineContexts(taskContext, attachmentContext), personalityContext),
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   false,
		ProcessMarkers:   false,
		ChatMode:         chatMode,
		Surface:          chatcontrol.SurfaceWeb,
	})

	return render(c, http.StatusOK, templ.Join(userMsg, agentMsg))
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
	pendingDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)

	// Check if pending directory exists
	if _, err := os.Stat(pendingDir); os.IsNotExist(err) {
		log.Printf("[handler] processAttachmentsWithReturn pending directory not found: %s", pendingDir)
		return "", nil, nil, nil // Not an error, just no attachments
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

	var attachmentContents []string
	var imageAttachments []models.Attachment
	var chatAttachments []models.ChatAttachment

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		srcPath := filepath.Join(pendingDir, file.Name())
		destPath := filepath.Join(execDir, file.Name())

		// Move file
		if err := os.Rename(srcPath, destPath); err != nil {
			log.Printf("[handler] processAttachments error moving file %s: %v", file.Name(), err)
			continue
		}

		// Get file info
		info, err := os.Stat(destPath)
		if err != nil {
			log.Printf("[handler] processAttachments error getting file info %s: %v", file.Name(), err)
			continue
		}

		// Detect media type from extension
		mediaType := mediaTypeFromExtension(file.Name())

		// Create database record
		attachment := &models.ChatAttachment{
			ExecutionID: execID,
			FileName:    file.Name(),
			FilePath:    destPath,
			MediaType:   mediaType,
			FileSize:    info.Size(),
		}

		if err := h.chatAttachmentRepo.Create(ctx, attachment); err != nil {
			log.Printf("[handler] processAttachmentsWithReturn error creating attachment record: %v", err)
			continue
		}

		// Add to chatAttachments list
		chatAttachments = append(chatAttachments, *attachment)

		if isImageFile(file.Name()) {
			// Image files: pass as multimodal attachments for the API to handle natively,
			// instead of reading binary content as text (which causes "prompt too long" errors)
			imageAttachments = append(imageAttachments, models.Attachment{
				FileName:  file.Name(),
				FilePath:  destPath,
				MediaType: mediaType,
				FileSize:  info.Size(),
			})
			log.Printf("[handler] processAttachmentsWithReturn image attachment id=%s file=%s size=%d", attachment.ID, file.Name(), info.Size())
		} else if info.Size() <= maxTextAttachmentSize {
			// Text files within size limit: read content and include in prompt context
			content, readErr := os.ReadFile(destPath)
			if readErr != nil {
				log.Printf("[handler] processAttachmentsWithReturn error reading file %s: %v", file.Name(), readErr)
				continue
			}
			attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s\n```\n%s\n```\n", file.Name(), string(content)))
			log.Printf("[handler] processAttachmentsWithReturn text attachment id=%s file=%s size=%d", attachment.ID, file.Name(), info.Size())
		} else {
			// Large text files: mention but don't include content to avoid prompt overflow
			attachmentContents = append(attachmentContents, fmt.Sprintf("\nFile: %s (attached, %d bytes - too large to include inline)\n", file.Name(), info.Size()))
			log.Printf("[handler] processAttachmentsWithReturn large file id=%s file=%s size=%d (skipped content)", attachment.ID, file.Name(), info.Size())
		}
	}

	// Clean up pending directory
	os.RemoveAll(pendingDir)

	var textContext string
	if len(attachmentContents) > 0 {
		textContext = "\n\n--- Attached Files ---\n" + strings.Join(attachmentContents, "")
	}

	return textContext, imageAttachments, chatAttachments, nil
}

func (h *Handler) ClearChat(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] ClearChat project=%s", projectID)

	// Cancel any running chat goroutines before deleting.
	// Without this, running goroutines continue processing with old conversation
	// history in memory, and their responses may appear stale or confusing.
	if h.workerSvc != nil {
		runningIDs, _ := h.taskRepo.ListRunningChatTaskIDs(c.Request().Context(), projectID)
		for _, id := range runningIDs {
			log.Printf("[handler] ClearChat cancelling running chat task=%s", id)
			h.workerSvc.CancelRunningTask(id)
		}
	}

	count, err := h.taskSvc.DeleteAllChat(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] ClearChat error: %v", err)
		return err
	}
	log.Printf("[handler] ClearChat deleted %d chat tasks", count)

	// Return updated chat content
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ClearChat error listing agents: %v", err)
		return err
	}

	// Return empty chat content
	return render(c, http.StatusOK, pages.ChatContent(agents, []models.Execution{}, projectID, make(map[string][]models.ChatAttachment)))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// copyChatAttachmentsToTask copies attachments from a chat execution to a task.
// It also appends an attachment reference to the task's prompt so the executing agent
// knows about the attached files. Returns the number of attachments copied and any error.
func (h *Handler) copyChatAttachmentsToTask(ctx context.Context, executionID, taskID string) (int, error) {
	// Get chat attachments for this execution
	chatAttachments, err := h.chatAttachmentRepo.ListByExecution(ctx, executionID)
	if err != nil {
		return 0, fmt.Errorf("listing chat attachments: %w", err)
	}

	if len(chatAttachments) == 0 {
		return 0, nil
	}

	// Create task-specific directory
	taskDir := filepath.Join(uploadsDir, "tasks", taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return 0, fmt.Errorf("creating task directory: %w", err)
	}

	copiedCount := 0
	var copiedFileNames []string
	for _, chatAtt := range chatAttachments {
		// Copy file from chat directory to task directory
		srcPath := chatAtt.FilePath
		destPath := filepath.Join(taskDir, chatAtt.FileName)

		// Read source file
		data, err := os.ReadFile(srcPath)
		if err != nil {
			log.Printf("[handler] copyChatAttachmentsToTask error reading file %s: %v", srcPath, err)
			continue
		}

		// Write to destination
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			log.Printf("[handler] copyChatAttachmentsToTask error writing file %s: %v", destPath, err)
			continue
		}

		// Create task attachment record
		taskAttachment := &models.Attachment{
			TaskID:    taskID,
			FileName:  chatAtt.FileName,
			FilePath:  destPath,
			MediaType: chatAtt.MediaType,
			FileSize:  chatAtt.FileSize,
		}

		if err := h.attachmentRepo.Create(ctx, taskAttachment); err != nil {
			log.Printf("[handler] copyChatAttachmentsToTask error creating attachment record: %v", err)
			// Clean up the copied file
			os.Remove(destPath)
			continue
		}

		log.Printf("[handler] copyChatAttachmentsToTask copied attachment file=%s from exec=%s to task=%s", chatAtt.FileName, executionID, taskID)
		copiedCount++
		copiedFileNames = append(copiedFileNames, chatAtt.FileName)
	}

	// Append attachment context to the task prompt so the executing agent knows about the files.
	// Include absolute file paths so the agent can find them regardless of working directory.
	if copiedCount > 0 {
		task, getErr := h.taskRepo.GetByID(ctx, taskID)
		if getErr == nil && task != nil {
			var fileRefs []string
			for _, name := range copiedFileNames {
				absPath := filepath.Join(uploadsDir, "tasks", taskID, name)
				fileRefs = append(fileRefs, fmt.Sprintf("%s (path: %s)", name, absPath))
			}
			task.Prompt += fmt.Sprintf("\n\n[Attached files from chat:\n%s]", strings.Join(fileRefs, "\n"))
			if updateErr := h.taskRepo.Update(ctx, task); updateErr != nil {
				log.Printf("[handler] copyChatAttachmentsToTask error updating task prompt: %v", updateErr)
			}
		}
	}

	return copiedCount, nil
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

// ChatStreamSSE streams chat execution output via SSE
func (h *Handler) ChatStreamSSE(c echo.Context) error {
	execID := c.Param("exec_id")
	if execID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "exec_id is required")
	}

	log.Printf("[handler] ChatStreamSSE exec=%s connected", execID)

	// Set headers for SSE
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	ctx := c.Request().Context()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	lastOutput := ""
	timeout := time.After(chatSSETimeout)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[handler] ChatStreamSSE exec=%s client disconnected", execID)
			return nil

		case <-timeout:
			log.Printf("[handler] ChatStreamSSE exec=%s timeout", execID)
			fmt.Fprintf(c.Response(), "event: error\ndata: timeout\n\n")
			c.Response().Flush()
			return nil

		case <-ticker.C:
			// Get current execution state
			exec, err := h.execRepo.GetByID(ctx, execID)
			if err != nil {
				log.Printf("[handler] ChatStreamSSE exec=%s error: %v", execID, err)
				fmt.Fprintf(c.Response(), "event: error\ndata: %s\n\n", err.Error())
				c.Response().Flush()
				return nil
			}

			if exec == nil {
				log.Printf("[handler] ChatStreamSSE exec=%s not found", execID)
				fmt.Fprintf(c.Response(), "event: error\ndata: execution not found\n\n")
				c.Response().Flush()
				return nil
			}

			// Send new output if changed
			if exec.Output != lastOutput && len(exec.Output) > len(lastOutput) {
				// Send only the delta (new content)
				delta := exec.Output[len(lastOutput):]
				log.Printf("[handler] ChatStreamSSE exec=%s delta_len=%d delta=%q", execID, len(delta), delta)
				// SSE requires multi-line data to have each line prefixed with "data:".
				// Without this, content after the first newline is silently dropped
				// by the browser's EventSource parser.
				writeSSEData(c, delta)
				c.Response().Flush()
				lastOutput = exec.Output
			} else if exec.Output != lastOutput {
				// Output was modified (not just appended) — update tracking
				lastOutput = exec.Output
			}

			// Check if execution is complete
			if exec.Status == models.ExecCompleted {
				log.Printf("[handler] ChatStreamSSE exec=%s completed total_output_len=%d total_output=%q", execID, len(exec.Output), exec.Output)
				fmt.Fprintf(c.Response(), "event: done\ndata: completed\n\n")
				c.Response().Flush()
				return nil
			} else if exec.Status == models.ExecFailed {
				log.Printf("[handler] ChatStreamSSE exec=%s failed: %s", execID, exec.ErrorMessage)
				fmt.Fprintf(c.Response(), "event: error\ndata: %s\n\n", exec.ErrorMessage)
				c.Response().Flush()
				return nil
			}
		}
	}
}
