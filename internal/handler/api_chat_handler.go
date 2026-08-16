package handler

import (
	"crypto/rand"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

const (
	apiMaxFileSize    = 10 << 20 // 10 MB per file
	apiMaxFilesPerReq = 10       // Max 10 files per API request
)

// allowedFileExtensions maps allowed extensions to their MIME types.
// Includes images, documents, and common programming language files.
var allowedFileExtensions = map[string]string{
	// Images
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	// Documents
	".pdf": "application/pdf",
	".txt": "text/plain",
	".md":  "text/markdown",
	".csv": "text/csv",
	// Code files
	".go":    "text/x-go",
	".py":    "text/x-python",
	".js":    "text/javascript",
	".ts":    "text/typescript",
	".jsx":   "text/javascript",
	".tsx":   "text/typescript",
	".rs":    "text/x-rust",
	".java":  "text/x-java",
	".c":     "text/x-c",
	".cpp":   "text/x-c++",
	".h":     "text/x-c",
	".rb":    "text/x-ruby",
	".php":   "text/x-php",
	".swift": "text/x-swift",
	".kt":    "text/x-kotlin",
	".sh":    "text/x-shellscript",
	".bash":  "text/x-shellscript",
	".sql":   "text/x-sql",
	".html":  "text/html",
	".css":   "text/css",
	".scss":  "text/x-scss",
	".xml":   "text/xml",
	".json":  "application/json",
	".yaml":  "text/x-yaml",
	".yml":   "text/x-yaml",
	".toml":  "text/x-toml",
	".ini":   "text/plain",
	".cfg":   "text/plain",
	".conf":  "text/plain",
	".log":   "text/plain",
	".diff":  "text/x-diff",
	".patch": "text/x-diff",
}

// isAllowedFileType checks if a filename has an allowed extension
func isAllowedFileType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	_, ok := allowedFileExtensions[ext]
	return ok
}

// ChatMessageAcceptedResponse represents the immediate response when a chat message is accepted for async processing
type ChatMessageAcceptedResponse struct {
	MessageID      string   `json:"message_id" example:"exec123"`
	Status         string   `json:"status" example:"processing"`
	StatusURL      string   `json:"status_url" example:"/api/chat/message/exec123"`
	AttachmentURLs []string `json:"attachment_urls,omitempty" example:"/chat/attachments/abc/download"`
	Queued         bool     `json:"queued,omitempty"`
}

// ChatMessageStatusResponse represents the status/result of an async chat message
type ChatMessageStatusResponse struct {
	MessageID  string   `json:"message_id" example:"exec123"`
	Status     string   `json:"status" example:"completed"`
	Response   string   `json:"response,omitempty" example:"Here's the information you requested..."`
	Error      string   `json:"error,omitempty" example:""`
	TaskIDs    []string `json:"task_ids,omitempty" example:"task1,task2"`
	TokensUsed int      `json:"tokens_used,omitempty" example:"150"`
	DurationMs int64    `json:"duration_ms,omitempty" example:"2500"`
}

// APIChatMessage godoc
// @Summary Send a chat message with optional file attachments (async)
// @Description Send a chat message to the AI agent with optional file attachments.
// @Description Returns 201 immediately with a message ID. The AI processes the message asynchronously.
// @Description Poll GET /api/chat/message/{id} to check status and retrieve the response.
// @Description Supported file types: Images (JPG, PNG, GIF, WebP), Documents (PDF, TXT, MD, CSV), Code (Go, Python, JS, TS, Rust, Java, C/C++, Ruby, PHP, Swift, Kotlin, Shell, SQL, HTML, CSS, SCSS, XML, JSON, YAML, TOML, INI, diff/patch)
// @Description Maximum file size: 10 MB per file
// @Description Maximum files per request: 10
// @Tags chat
// @Accept multipart/form-data
// @Produce json
// @Param message formData string true "Chat message text to send to the AI"
// @Param project_id formData string true "Project ID to associate with this chat message"
// @Param attachments formData file false "File attachments (screenshots, images, PDFs, text files)"
// @Success 201 {object} ChatMessageAcceptedResponse "Message accepted for async processing"
// @Failure 400 {object} ErrorResponse "Bad request - missing required fields or invalid input"
// @Failure 404 {object} ErrorResponse "Project not found"
// @Failure 413 {object} ErrorResponse "File too large"
// @Failure 415 {object} ErrorResponse "Unsupported file type"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/chat/message [post]
func (h *Handler) APIChatMessage(c echo.Context) error {
	// Parse multipart form (limit total request body to prevent abuse)
	if err := c.Request().ParseMultipartForm(apiMaxFileSize * int64(apiMaxFilesPerReq)); err != nil {
		// If it's not multipart, try regular form
		if c.Request().Header.Get("Content-Type") == "" || strings.HasPrefix(c.Request().Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			// That's OK, no attachments
		} else {
			applog.Infof("[handler] APIChatMessage error parsing form: %v", err)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to parse request form"})
		}
	}

	message := c.FormValue("message")
	projectID := c.FormValue("project_id")

	// Validate required fields
	if message == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "message is required"})
	}
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id is required"})
	}

	applog.Debugf("[handler] APIChatMessage message=%q project_id=%s", message, projectID)

	// Validate project exists
	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil || project == nil {
		applog.Infof("[handler] APIChatMessage project not found: %s err=%v", projectID, err)
		return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
	}

	// Auto-select an agent
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil || len(agents) == 0 {
		applog.Infof("[handler] APIChatMessage no agents available: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "no agents available"})
	}
	complexity := service.AnalyzeComplexity(message)
	result := service.SelectLLM(complexity, agents)
	var agent *models.LLMConfig
	if result != nil {
		agent = result.LLMConfig
	} else {
		agent = &agents[0]
	}

	// Note: Interactive chat intentionally bypasses task worker capacity checks.
	// Task worker limits (per-project/per-model) only gate task execution, not chat.
	// This ensures the chat orchestrator remains responsive even when all task workers are busy.

	// Process file attachments if present
	var savedFiles []apiSavedFile
	if c.Request().MultipartForm != nil && c.Request().MultipartForm.File != nil {
		files := c.Request().MultipartForm.File["attachments"]
		if len(files) > apiMaxFilesPerReq {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("maximum %d files per request", apiMaxFilesPerReq),
			})
		}

		for _, file := range files {
			// Validate file size
			if file.Size > apiMaxFileSize {
				return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{
					"error": fmt.Sprintf("file %q exceeds %dMB size limit", file.Filename, apiMaxFileSize>>20),
				})
			}

			// Validate file type
			if !isAllowedFileType(file.Filename) {
				return c.JSON(http.StatusUnsupportedMediaType, map[string]string{
					"error": fmt.Sprintf("file type %q not allowed; allowed types: images (jpg, png, gif, webp), documents (pdf, txt, md, csv), code files (go, py, js, ts, etc.)", filepath.Ext(file.Filename)),
				})
			}

			savedFiles = append(savedFiles, apiSavedFile{
				header: file,
			})
		}
	}

	activeChatExec, err := h.execRepo.FindLatestActiveChatExecution(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] APIChatMessage error checking active chat turn: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to check chat queue"})
	}

	if activeChatExec != nil {
		if h.threadInputRepo == nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "chat input queue is unavailable"})
		}
		attachmentSessionID := ""
		if len(savedFiles) > 0 {
			var sessionErr error
			attachmentSessionID, sessionErr = saveAPIChatFilesToPendingSession(savedFiles)
			if sessionErr != nil {
				applog.Infof("[handler] APIChatMessage error saving queued attachment session: %v", sessionErr)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to save queued attachments"})
			}
		}
		queued := &models.ThreadInput{
			Scope:               models.ThreadInputScopeChat,
			ProjectID:           projectID,
			RunExecutionID:      activeChatExec.ID,
			AgentConfigID:       agent.ID,
			InputMode:           models.ThreadInputModeQueued,
			InputStatus:         models.ThreadInputPending,
			Content:             message,
			AttachmentSessionID: attachmentSessionID,
			ChatMode:            models.ChatModeOrchestrate,
		}
		if err := h.threadInputRepo.CreateQueued(c.Request().Context(), queued); err != nil {
			applog.Infof("[handler] APIChatMessage error creating queued input: %v", err)
			if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), attachmentSessionID); cleanupErr != nil {
				applog.Infof("[handler] APIChatMessage error cleaning unpublished queued attachment session %s: %v", attachmentSessionID, cleanupErr)
			}
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to queue chat message"})
		}
		if h.chatBroadcaster != nil {
			h.chatBroadcaster.Publish(events.ChatEvent{
				Type:           events.ChatNewMessage,
				ProjectID:      projectID,
				ExecID:         queued.ID,
				Message:        message,
				Source:         "api",
				AgentName:      agent.Name,
				Queued:         true,
				HasAttachments: queued.AttachmentSessionID != "",
			})
		}
		return c.JSON(http.StatusCreated, ChatMessageAcceptedResponse{
			MessageID: queued.ID,
			Status:    "queued",
			StatusURL: fmt.Sprintf("/api/chat/message/%s", queued.ID),
			Queued:    true,
		})
	}

	// Create a task record for the chat message
	chatTitle := fmt.Sprintf("Chat %s: %s", time.Now().Format("15:04:05.000"), message[:min(50, len(message))])
	agentID := agent.ID
	task := &models.Task{
		ProjectID: projectID,
		Title:     chatTitle,
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agentID,
	}
	if err := h.taskRepo.Create(c.Request().Context(), task); err != nil {
		applog.Infof("[handler] APIChatMessage error creating task: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create chat task"})
	}

	// Create execution record for immediate processing.
	execStatus := models.ExecRunning
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        execStatus,
		PromptSent:    message,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		applog.Infof("[handler] APIChatMessage error creating execution: %v", err)
		if delErr := h.taskRepo.Delete(c.Request().Context(), task.ID); delErr != nil {
			applog.Infof("[handler] APIChatMessage error cleaning up chat task=%s after execution create failure: %v", task.ID, delErr)
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create execution"})
	}

	applog.Infof("[handler] APIChatMessage created exec=%s task=%s status=%s", exec.ID, task.ID, exec.Status)
	// Broadcast new message event
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Message:   message,
			Source:    "api",
			AgentName: agent.Name,
		})
	}

	// Save attachments to disk and database
	var attachmentURLs []string
	var attachmentContext string
	var imageAttachments []models.Attachment

	if len(savedFiles) > 0 {
		execDir := filepath.Join(uploadsDir, "chat", exec.ID)
		if err := os.MkdirAll(execDir, 0755); err != nil {
			applog.Infof("[handler] APIChatMessage error creating exec dir: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create upload directory"})
		}

		var contextFiles []chatAttachmentModelContextFile
		for _, sf := range savedFiles {
			filename := filepath.Base(sf.header.Filename)
			// Generate unique filename to avoid collisions
			uniqueName := fmt.Sprintf("%s_%s", generateShortID(), filename)
			destPath := filepath.Join(execDir, uniqueName)

			// Open uploaded file
			src, err := sf.header.Open()
			if err != nil {
				applog.Infof("[handler] APIChatMessage error opening file %s: %v", filename, err)
				continue
			}

			// Save to disk
			dst, err := os.Create(destPath)
			if err != nil {
				src.Close()
				applog.Infof("[handler] APIChatMessage error creating file %s: %v", filename, err)
				continue
			}
			if _, err := io.Copy(dst, src); err != nil {
				dst.Close()
				src.Close()
				os.Remove(destPath)
				applog.Infof("[handler] APIChatMessage error saving file %s: %v", filename, err)
				continue
			}
			dst.Close()
			src.Close()

			// Determine media type from extension
			mediaType := mediaTypeFromExtension(filename)

			// Create database record
			chatAtt := &models.ChatAttachment{
				ExecutionID: exec.ID,
				FileName:    filename,
				FilePath:    destPath,
				MediaType:   mediaType,
				FileSize:    sf.header.Size,
			}
			if err := h.chatAttachmentRepo.Create(c.Request().Context(), chatAtt); err != nil {
				applog.Infof("[handler] APIChatMessage error creating attachment record: %v", err)
				if cleanupErr := os.Remove(destPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
					applog.Infof("[handler] APIChatMessage error removing unpublished attachment %s: %v", destPath, cleanupErr)
				}
				_ = os.Remove(execDir) // Remove only when no published attachment still owns the directory.
				continue
			}

			attachmentURLs = append(attachmentURLs, fmt.Sprintf("/chat/attachments/%s/download", chatAtt.ID))

			contextFiles = append(contextFiles, chatAttachmentModelContextFile{
				FileName:  filename,
				FilePath:  destPath,
				MediaType: mediaType,
				FileSize:  sf.header.Size,
			})

			applog.Infof("[handler] APIChatMessage saved attachment file=%s size=%d", filename, sf.header.Size)
		}

		var contextErr error
		attachmentContext, imageAttachments, contextErr = buildChatAttachmentModelContext(contextFiles, chatAttachmentModelContextOptions{IgnoreReadErrors: true})
		if contextErr != nil {
			applog.Infof("[handler] APIChatMessage error building attachment context: %v", contextErr)
		}
	}

	// Load chat history for conversation context
	chatHistory, err := h.execRepo.ListChatHistory(c.Request().Context(), projectID, 50)
	if err != nil {
		applog.Infof("[handler] APIChatMessage error loading chat history: %v", err)
		chatHistory = []models.Execution{}
	}
	priorHistory := filterChatHistory(chatHistory, exec.ID)
	// Build task context using shared function (same as /chat and Telegram)
	taskContext := h.buildChatContext(c.Request().Context(), projectID, agents)

	// Combine context (including personality if set)
	fullContext := taskContext
	if attachmentContext != "" {
		if fullContext != "" {
			fullContext += "\n"
		}
		fullContext += attachmentContext
	}
	if personalityContext := h.getPersonalityContext(c.Request().Context(), projectID); personalityContext != "" {
		fullContext += personalityContext
	}

	// Get working directory for the provider call.
	workDir := project.RepoPath

	// Process the LLM call asynchronously using the shared streaming response processor.
	// This ensures consistent behavior (runtime action tools, proper completion ordering,
	// ChatResponseDone broadcast) regardless of whether the message came from web, API, or Telegram.
	// The client receives 201 immediately and polls GET /api/chat/message/:id for the result.
	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          message,
		Agent:            *agent,
		ChatHistory:      priorHistory,
		ProjectID:        projectID,
		PrincipalID:      h.authPrincipalID(c),
		SystemContext:    fullContext,
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   false,
		Surface:          chatcontrol.SurfaceAPI,
	}); err != nil {
		c.Response().Header().Set("Retry-After", "30")
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}

	applog.Infof("[handler] APIChatMessage exec=%s accepted for async processing status=%s", exec.ID, execStatus)
	// Return 201 immediately with the message ID for polling
	resp := ChatMessageAcceptedResponse{
		MessageID: exec.ID,
		Status:    "processing",
		StatusURL: fmt.Sprintf("/api/chat/message/%s", exec.ID),
	}
	if len(attachmentURLs) > 0 {
		resp.AttachmentURLs = attachmentURLs
	}

	return c.JSON(http.StatusCreated, resp)
}

// APIChatMessageStatus godoc
// @Summary Get the status and result of a chat message
// @Description Poll this endpoint to check if an async chat message has completed processing.
// @Description Returns the current status (processing, completed, failed) and the response when available.
// @Tags chat
// @Produce json
// @Param id path string true "Message/execution ID returned from POST /api/chat/message"
// @Success 200 {object} ChatMessageStatusResponse "Message status and response"
// @Failure 404 {object} ErrorResponse "Message not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/chat/message/{id} [get]
func (h *Handler) APIChatMessageStatus(c echo.Context) error {
	execID := c.Param("id")
	if execID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "message id is required"})
	}

	exec, err := h.execRepo.GetByID(c.Request().Context(), execID)
	if err != nil {
		applog.Infof("[handler] APIChatMessageStatus exec=%s error: %v", execID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve message status"})
	}
	if exec == nil {
		if h.threadInputRepo != nil {
			input, inputErr := h.threadInputRepo.GetByID(c.Request().Context(), execID)
			if inputErr != nil {
				applog.Infof("[handler] APIChatMessageStatus input=%s error: %v", execID, inputErr)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve message status"})
			}
			if input != nil {
				if input.InputStatus == models.ThreadInputApplied && input.RunExecutionID != "" {
					exec, err = h.execRepo.GetByID(c.Request().Context(), input.RunExecutionID)
					if err != nil {
						applog.Infof("[handler] APIChatMessageStatus promoted exec=%s error: %v", input.RunExecutionID, err)
						return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve promoted message status"})
					}
					if exec != nil {
						return h.apiChatExecutionStatus(c, exec)
					}
				}
				status := string(input.InputStatus)
				if input.InputStatus == models.ThreadInputPending && input.InputMode == models.ThreadInputModeQueued {
					status = "queued"
				}
				return c.JSON(http.StatusOK, ChatMessageStatusResponse{MessageID: input.ID, Status: status})
			}
		}
		return c.JSON(http.StatusNotFound, map[string]string{"error": "message not found"})
	}

	return h.apiChatExecutionStatus(c, exec)
}

func (h *Handler) apiChatExecutionStatus(c echo.Context, exec *models.Execution) error {
	resp := ChatMessageStatusResponse{
		MessageID: exec.ID,
	}

	switch exec.Status {
	case models.ExecCompleted:
		resp.Status = "completed"
		resp.Response = exec.Output
		resp.TokensUsed = exec.TokensUsed
		resp.DurationMs = exec.DurationMs

		for _, taskID := range extractTaskIDsFromOutput(exec.Output) {
			if task, taskErr := h.taskRepo.GetByID(c.Request().Context(), taskID); taskErr == nil && task != nil && task.Category != models.CategoryChat {
				resp.TaskIDs = append(resp.TaskIDs, taskID)
			}
		}
	case models.ExecFailed:
		resp.Status = "failed"
		resp.Error = exec.ErrorMessage
		resp.DurationMs = exec.DurationMs
	case models.ExecCancelled:
		resp.Status = "cancelled"
		resp.Error = exec.ErrorMessage
		resp.Response = exec.Output
		resp.DurationMs = exec.DurationMs
	default:
		resp.Status = "processing"
		// Include partial output if available
		if exec.Output != "" {
			resp.Response = exec.Output
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// apiSavedFile holds a multipart file header for deferred processing
type apiSavedFile struct {
	header *multipart.FileHeader
}

func saveAPIChatFilesToPendingSession(savedFiles []apiSavedFile) (string, error) {
	if len(savedFiles) == 0 {
		return "", nil
	}
	sessionID := generateSessionID()
	sessionDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return "", fmt.Errorf("creating pending upload directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(sessionDir)
		}
	}()
	for _, sf := range savedFiles {
		filename := filepath.Base(sf.header.Filename)
		uniqueName := fmt.Sprintf("%s_%s", generateShortID(), filename)
		destPath := filepath.Join(sessionDir, uniqueName)
		src, err := sf.header.Open()
		if err != nil {
			return "", fmt.Errorf("opening %s: %w", filename, err)
		}
		dst, err := os.Create(destPath)
		if err != nil {
			src.Close()
			return "", fmt.Errorf("creating %s: %w", filename, err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			src.Close()
			_ = os.Remove(destPath)
			return "", fmt.Errorf("saving %s: %w", filename, err)
		}
		if err := dst.Close(); err != nil {
			src.Close()
			return "", fmt.Errorf("closing %s: %w", filename, err)
		}
		if err := src.Close(); err != nil {
			return "", fmt.Errorf("closing upload %s: %w", filename, err)
		}
	}
	cleanup = false
	return sessionID, nil
}

// generateShortID creates a short random hex string for unique filenames
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "0000"
	}
	return fmt.Sprintf("%x", b)
}
